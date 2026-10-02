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
	rows, err := tx.Query(ctx, `SELECT id, kind, line FROM conductor_wakes
		WHERE task_id = $1 AND delivered_at IS NULL ORDER BY created_at, id FOR UPDATE`, taskID)
	if err != nil {
		return "", err
	}
	type reason struct{ ID, Kind, Line string }
	pending, err := pgx.CollectRows(rows, pgx.RowToStructByPos[reason])
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
	var runID string
	var between bool
	err = tx.QueryRow(ctx, `SELECT r.id, r.status = 'paused' OR (r.status = 'running'
			AND (r.turn_done_at IS NOT NULL OR r.waiting_since IS NOT NULL))
		FROM runs r WHERE r.task_id = $1 AND `+LiveConductor+` AND NOT `+Ending+` FOR NO KEY UPDATE`, taskID).Scan(&runID, &between)
	if err != nil && !db.IsNotFound(err) {
		return "", err
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
	if runID == "" {
		if runID, err = startConductor(ctx, tx, org, projectID, taskID, Writer{ActorType: ledger.ActorSystem, ActorID: "dude"},
			note, true); err != nil {
			return "", err
		}
		payload["started"] = true
	} else {
		ref := RunRef{Org: org, ProjectID: projectID, TaskID: taskID, RunID: runID}
		directiveID, _, err := QueueDirective(ctx, tx, ref, Directive{Text: note, Scope: "run"})
		if err != nil {
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
	_, err = ledger.Append(ctx, tx, ledger.Event{Type: EvConductorWoken, OrganizationID: org, ProjectID: projectID,
		TaskID: taskID, RunID: runID, ActorType: ledger.ActorSystem, ActorID: "dude", Source: ledger.SourceOrchestrator,
		CorrelationID: taskID, Payload: payload})
	return runID, err
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

// SafetyNet (SQL, over runs r, $1 seconds): a task's conductor whose last
// turn ended that long ago, with a phase Run it started still in flight
// that has not woken it yet. Returns that Run as k.id.
const SafetyNet = `r.role = 'conductor' AND r.kind = 'agent' AND r.status IN ('running', 'paused')
	AND r.turn_done_at < now() - make_interval(secs => $1)
	AND EXISTS (SELECT 1 FROM runs k WHERE k.conductor_run_id = r.id
		AND k.status IN ('pending', 'scheduled', 'starting', 'running', 'paused')
		AND NOT EXISTS (SELECT 1 FROM conductor_wakes c WHERE c.task_id = k.task_id AND c.key = 'safety:' || k.id))`

// RecordSafetyTx is the safety net's reason: once per Run in flight.
func RecordSafetyTx(ctx context.Context, tx pgx.Tx, org, taskID, conductorID string) error {
	rows, err := tx.Query(ctx, `SELECT id, COALESCE(phase::text, ''), status::text FROM runs
		WHERE conductor_run_id = $1 AND status IN ('pending', 'scheduled', 'starting', 'running', 'paused')`, conductorID)
	if err != nil {
		return err
	}
	type kid struct{ ID, Phase, Status string }
	kids, err := pgx.CollectRows(rows, pgx.RowToStructByPos[kid])
	if err != nil {
		return err
	}
	for _, k := range kids {
		if _, err := RecordWakeTx(ctx, tx, org, taskID, "safety", "safety:"+k.ID,
			fmt.Sprintf("Still in flight: your %s Run %s is %s. Say in Chat what you are waiting for.", k.Phase, k.ID, k.Status)); err != nil {
			return err
		}
	}
	return nil
}
