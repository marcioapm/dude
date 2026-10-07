package delivery

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// Waking the conductor: the reasons a task's conductor has to take a turn
// (conductor_wakes) are delivered together, as one note, once none has
// arrived for the window and the conductor is not mid-turn. A note is
// fixed-size facts and ids, capped in lines.

// wakeLines caps the lines of one note; the rest are counted.
const wakeLines = 10

// WakeNote is the note for reasons' lines, in the order they came.
func WakeNote(lines []string) string {
	var b strings.Builder
	b.WriteString("dude woke you; ")
	if len(lines) == 1 {
		b.WriteString("one thing:")
	} else {
		fmt.Fprintf(&b, "%d things:", len(lines))
	}
	for i, l := range lines {
		if i == wakeLines {
			fmt.Fprintf(&b, "\n- … and %d more", len(lines)-wakeLines)
			break
		}
		b.WriteString("\n- " + l)
	}
	b.WriteString("\n\nRead what you need with your tools; take the decision waiting, or tell the person what you propose.")
	return b.String()
}

// WakeConductorTx delivers the task's pending reasons to its conductor as
// one note, unless they should wait: the newest arrived within the window
// (more may come), or the conductor is mid-turn (they join its next). A
// conductor that is parked is resumed by the note; with none, one is
// started, briefed with it. Returns the conductor woken, "" for none yet.
// The caller holds the task's Chat lock.
func WakeConductorTx(ctx context.Context, tx pgx.Tx, org, taskID string, windowSecs float64) (string, error) {
	type reason struct{ ID, Kind, Line string }
	pendingNow := func() ([]reason, error) {
		rows, err := tx.Query(ctx, `SELECT id, kind, line FROM conductor_wakes
			WHERE task_id = $1 AND delivered_at IS NULL ORDER BY created_at, id FOR UPDATE`, taskID)
		if err != nil {
			return nil, err
		}
		return pgx.CollectRows(rows, pgx.RowToStructByPos[reason])
	}
	// The live conductor's Run is locked before any reason: its follower
	// holds that Run while a receipt settles reasons (heardTx), so the
	// opposite order would deadlock with it.
	var runID string
	var between, ending bool
	liveNow := func() error {
		runID, between, ending = "", false, false
		err := tx.QueryRow(ctx, `SELECT r.id, r.status = 'paused' OR (r.status = 'running'
				AND (r.turn_done_at IS NOT NULL OR r.waiting_since IS NOT NULL)), COALESCE(`+Ending+`, false)
			FROM runs r WHERE r.task_id = $1 AND `+LiveConductor+` FOR NO KEY UPDATE`, taskID).Scan(&runID, &between, &ending)
		if db.IsNotFound(err) {
			return nil
		}
		return err
	}
	if err := liveNow(); err != nil {
		return "", err
	}
	pending, err := pendingNow()
	if err != nil || len(pending) == 0 {
		return "", err
	}
	var settled bool
	if err := tx.QueryRow(ctx, `SELECT max(created_at) < now() - make_interval(secs => $2)
		FROM conductor_wakes WHERE task_id = $1 AND delivered_at IS NULL`, taskID, windowSecs).Scan(&settled); err != nil {
		return "", err
	}
	if !settled {
		return "", nil
	}
	var projectID string
	if err := tx.QueryRow(ctx, `SELECT project_id FROM tasks WHERE id = $1`, taskID).Scan(&projectID); err != nil {
		return "", err
	}
	// A conductor whose container stopped without dude asking holds the
	// live slot until it is ended, turn end or not: ended here, as Chat
	// ends it, so the note can start its replacement.
	if ending {
		if err := EndConductor(ctx, tx, RunRef{Org: org, ProjectID: projectID, TaskID: taskID, RunID: runID}, "its container stopped"); err != nil {
			return "", err
		}
		// What it was told and never heard is pending again: told now too,
		// to the replacement HandOver may have started, or to none.
		if pending, err = pendingNow(); err != nil {
			return "", err
		}
		if err := liveNow(); err != nil {
			return "", err
		}
	}
	if runID != "" && !between {
		// Mid-turn, or not started yet: what it hears next.
		return "", nil
	}
	lines, kinds := make([]string, len(pending)), make([]string, len(pending))
	ids := make([]string, len(pending))
	for i, p := range pending {
		lines[i], kinds[i], ids[i] = p.Line, p.Kind, p.ID
	}
	note := WakeNote(lines)
	payload := map[string]any{"text": note, "reasons": kinds}
	var directiveID string
	if runID == "" {
		if runID, err = startConductor(ctx, tx, org, projectID, taskID, Writer{ActorType: ledger.ActorSystem, ActorID: "dude"},
			note, true); err != nil {
			return "", err
		}
		payload["started"] = true
	} else {
		ref := RunRef{Org: org, ProjectID: projectID, TaskID: taskID, RunID: runID}
		if directiveID, _, err = QueueDirective(ctx, tx, ref, Directive{Text: note, Scope: "run"}); err != nil {
			return "", err
		}
		payload["directiveId"] = directiveID
		// Parked by dude for its warm period's end, the directive resumes
		// it (resumable); idle, it is asked back. A person's own pause
		// holds: it hears the note when they resume it.
		if _, err := tx.Exec(ctx, `UPDATE runs SET control = 'resume', control_requested_at = now(), control_reason = 'woken by dude'
			WHERE id = $1 AND status = 'paused' AND dude_pause = 'idle'`, runID); err != nil {
			return "", err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE conductor_wakes SET delivered_at = now(), conductor_run_id = $2 WHERE id = ANY($1)`,
		ids, runID); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO conductor_wake_attempts (organization_id, wake_id, conductor_run_id, directive_id)
		SELECT $1, w, $3, NULLIF($4, '') FROM unnest($2::text[]) w`, org, ids, runID, directiveID); err != nil {
		return "", err
	}
	_, err = ledger.Append(ctx, tx, ledger.Event{Type: EvConductorWoken, OrganizationID: org, ProjectID: projectID,
		TaskID: taskID, RunID: runID, ActorType: ledger.ActorSystem, ActorID: "dude", Source: ledger.SourceOrchestrator,
		CorrelationID: taskID, Payload: payload})
	return runID, err
}

// Recovery: each note that carried a reason is an attempt
// (conductor_wake_attempts). A reason goes back to pending once an attempt
// fails unheard and no other attempt holds it — heard, or still on its way.
// Any attempt heard settles it, and withdraws retries not yet claimed by
// the sender. The guarantee is at least once: attempts already claimed
// may both be heard, a duplicate note accepted rather than a reason lost.

// attemptHolds (SQL, over conductor_wake_attempts o): o was heard, or may
// still be — a briefing heard, or not failed; a directive delivered (its
// consumption wins over a failure), or not failed.
const attemptHolds = `(CASE WHEN o.directive_id IS NULL THEN o.heard_at IS NOT NULL OR o.failed_at IS NULL
	ELSE EXISTS (SELECT 1 FROM directives od WHERE od.id = o.directive_id AND (od.delivered_at IS NOT NULL OR od.failed_at IS NULL)) END)`

// requeueTx puts reasons back to pending once no attempt holds them.
func requeueTx(ctx context.Context, tx pgx.Tx, wakeIDs []string) error {
	if len(wakeIDs) == 0 {
		return nil
	}
	_, err := tx.Exec(ctx, `UPDATE conductor_wakes c SET delivered_at = NULL, conductor_run_id = NULL
		WHERE c.id = ANY($1) AND c.delivered_at IS NOT NULL
		  AND NOT EXISTS (SELECT 1 FROM conductor_wake_attempts o WHERE o.wake_id = c.id AND `+attemptHolds+`)`, wakeIDs)
	return err
}

// RequeueWakesTx puts the reasons a wake note carried back to pending once
// its directive definitively failed (lux.input.failed, or its conductor
// ended without reading it), unless a note holds them (attemptHolds): the
// next sweep tells the live conductor, or its replacement, again. A
// directive that was delivered — its consumption receipt — holds them.
func RequeueWakesTx(ctx context.Context, tx pgx.Tx, directiveID string) error {
	wakes, err := directiveWakesTx(ctx, tx, directiveID)
	if err != nil {
		return err
	}
	return requeueTx(ctx, tx, wakes)
}

// WakesHeardTx settles the reasons a wake note carried once its directive
// is delivered — after a failure too: a consumption receipt wins.
func WakesHeardTx(ctx context.Context, tx pgx.Tx, directiveID string) error {
	wakes, err := directiveWakesTx(ctx, tx, directiveID)
	if err != nil {
		return err
	}
	return heardTx(ctx, tx, wakes, directiveID)
}

func directiveWakesTx(ctx context.Context, tx pgx.Tx, directiveID string) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT wake_id FROM conductor_wake_attempts WHERE directive_id = $1`, directiveID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// heardTx settles reasons an attempt was heard for (heard, the directive
// it was, "" for a briefing). Their retries not yet claimed are withdrawn,
// and what else those carried is pending again.
func heardTx(ctx context.Context, tx pgx.Tx, wakeIDs []string, heard string) error {
	if len(wakeIDs) == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, `UPDATE conductor_wakes SET delivered_at = COALESCE(delivered_at, now()) WHERE id = ANY($1)`,
		wakeIDs); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT DISTINCT d.id, d.run_id, r.project_id, r.task_id, d.organization_id
		FROM conductor_wake_attempts o
		JOIN directives d ON d.id = o.directive_id JOIN runs r ON r.id = d.run_id
		WHERE o.wake_id = ANY($1) AND o.directive_id <> $2
		  AND d.sent_at IS NULL AND d.claimed_at IS NULL AND d.delivered_at IS NULL AND d.failed_at IS NULL`, wakeIDs, heard)
	if err != nil {
		return err
	}
	type retry struct{ ID, RunID, ProjectID, TaskID, Org string }
	retries, err := pgx.CollectRows(rows, pgx.RowToStructByPos[retry])
	if err != nil {
		return err
	}
	// A retry the sender claimed may be in flight: it counts as sent, two
	// notes heard rather than a reason lost. The claim's row lock orders
	// the two, and whichever commits second sees the other.
	const why = "an earlier note with its reasons was heard"
	for _, d := range retries {
		tag, err := tx.Exec(ctx, `UPDATE directives SET failed_at = now(), error = $2
			WHERE id = $1 AND sent_at IS NULL AND claimed_at IS NULL AND delivered_at IS NULL AND failed_at IS NULL`, d.ID, why)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			continue
		}
		ref := RunRef{Org: d.Org, ProjectID: d.ProjectID, TaskID: d.TaskID, RunID: d.RunID}
		if _, err := ledger.Append(ctx, tx, ref.Event(evDirectiveFailed, ledger.ActorSystem,
			map[string]any{"directiveId": d.ID, "error": why})); err != nil {
			return err
		}
		if err := RequeueWakesTx(ctx, tx, d.ID); err != nil {
			return err
		}
	}
	return nil
}

// BriefingHeardTx records that a conductor dude started with a wake note
// has heard its first prompt: lux's consumption receipt for it, or, with
// no receipt to follow, its acceptance (or an older lux's handoff). Its
// reasons are not told again. overridesFailure: a consumption or handoff,
// the agent's own report, counts after the briefing was failed too; the
// failure stays recorded, and the hearing holds the reasons (attemptHolds).
func BriefingHeardTx(ctx context.Context, tx pgx.Tx, runID string, overridesFailure bool) error {
	rows, err := tx.Query(ctx, `UPDATE conductor_wake_attempts SET heard_at = now()
		WHERE conductor_run_id = $1 AND directive_id IS NULL AND heard_at IS NULL AND (failed_at IS NULL OR $2)
		RETURNING wake_id`, runID, overridesFailure)
	if err != nil {
		return err
	}
	wakes, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	// Settled, also when its failure had put them back to pending, and
	// their retries not yet sent withdrawn.
	return heardTx(ctx, tx, wakes, "")
}

// BriefingFailedTx puts back to pending the reasons a conductor was started
// with, once lux says its first prompt will not reach the agent, unless
// another note holds them.
func BriefingFailedTx(ctx context.Context, tx pgx.Tx, runID string) error {
	return failBriefingTx(ctx, tx, runID, false)
}

// BriefingUnheardTx puts back to pending the reasons a conductor was
// started with, once it ended without hearing its first prompt (it failed
// to submit, its image was refused, its model is missing, or it stopped
// before the agent read the prompt), unless another note holds them.
func BriefingUnheardTx(ctx context.Context, tx pgx.Tx, runID string) error {
	return failBriefingTx(ctx, tx, runID, true)
}

func failBriefingTx(ctx context.Context, tx pgx.Tx, runID string, ended bool) error {
	rows, err := tx.Query(ctx, `UPDATE conductor_wake_attempts a SET failed_at = now()
		FROM runs r WHERE r.id = a.conductor_run_id AND a.conductor_run_id = $1 AND a.directive_id IS NULL
		  AND a.heard_at IS NULL AND a.failed_at IS NULL AND (NOT $2 OR r.status IN ('completed', 'failed', 'aborted'))
		RETURNING a.wake_id`, runID, ended)
	if err != nil {
		return err
	}
	wakes, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	return requeueTx(ctx, tx, wakes)
}

// FailedForConductor (SQL, over runs r): a phase Run the task's conductor
// started that failed, not yet a reason to wake it.
const FailedForConductor = `(r.conductor_run_id IS NOT NULL AND r.status = 'failed'
	AND NOT EXISTS (SELECT 1 FROM conductor_wakes c WHERE c.task_id = r.task_id AND c.key = 'failed:' || r.id))`

// RecordFailedTx is the reason a Run the conductor started failed.
func RecordFailedTx(ctx context.Context, tx pgx.Tx, org, taskID, runID string) error {
	var phase, category, errText string
	if err := tx.QueryRow(ctx, `SELECT COALESCE(phase::text, ''), COALESCE(category, ''), COALESCE(error, '') FROM runs WHERE id = $1`,
		runID).Scan(&phase, &category, &errText); err != nil {
		return err
	}
	if category != "" {
		phase += " · " + category
	}
	_, err := RecordWakeTx(ctx, tx, org, taskID, "decision", "failed:"+runID,
		fmt.Sprintf("Your %s Run %s failed: %s", phase, runID, clip(oneLine(errText), 120)))
	return err
}

// Wakeable (SQL, over conductor_wakes c, $1 the window in seconds): a task
// with pending reasons that can be delivered now — none arrived within the
// window, and its live conductor can hear a note (between turns, or
// paused), or it has none (one is started), or the one it has is ending
// (it is ended first). Selected before the sweep's batch limit, so tasks
// that must wait (a conductor mid-turn) cannot fill the batch.
const Wakeable = `c.delivered_at IS NULL
	AND NOT EXISTS (SELECT 1 FROM conductor_wakes n WHERE n.task_id = c.task_id AND n.delivered_at IS NULL
		AND n.created_at >= now() - make_interval(secs => $1))
	AND NOT EXISTS (SELECT 1 FROM runs r WHERE r.task_id = c.task_id AND ` + LiveConductor + ` AND NOT COALESCE(` + Ending + `, false)
		AND NOT (r.status = 'paused' OR (r.status = 'running' AND (r.turn_done_at IS NOT NULL OR r.waiting_since IS NOT NULL))))`
