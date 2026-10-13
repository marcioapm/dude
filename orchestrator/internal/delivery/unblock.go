package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

// Unblocking, and start when unblocked (docs/design/task-relations.md):
// a merge releases what waits on it, and a task whose owner asked starts
// then — never in the merge's transaction, by a worker, after every
// blocker is read again.

type actingPersonKey struct{}

// ActingPerson carries who moves a task's status, for what the move
// records about them (Mark done's "marked done by Tom").
func ActingPerson(ctx context.Context, personID string) context.Context {
	return context.WithValue(ctx, actingPersonKey{}, personID)
}

func actingPerson(ctx context.Context) string {
	p, _ := ctx.Value(actingPersonKey{}).(string)
	return p
}

// notStartedStatuses are the statuses a task has before any start: where
// Deliver and Talk it through are offered.
var notStartedStatuses = []string{"received", "intake", "awaiting_confirmation", "queued"}

var notStartedSet = func() map[string]bool {
	m := make(map[string]bool, len(notStartedStatuses))
	for _, s := range notStartedStatuses {
		m[s] = true
	}
	return m
}()

// NotStarted says a task status is from before any start.
func NotStarted(status string) bool { return notStartedSet[status] }

// releaseDependantsTx runs as a task moves to done (RecordStatusTx). With a
// merged pull request, each not-started task it blocked that has nothing
// open in front of it any more is released: task.unblocked on it, dude's
// notice in its Chat, its live conductor woken, a delivery parked on it
// signalled, and its start when unblocked marked due. Done with nothing
// merged releases nobody, and says so on each task it still blocks.
func releaseDependantsTx(ctx context.Context, tx pgx.Tx, org, taskID string) error {
	var key string
	var prNumber *int
	if err := tx.QueryRow(ctx, `SELECT p.key_prefix || '-' || t.number,
			(SELECT pr.number FROM pull_requests pr WHERE pr.task_id = t.id AND pr.state = 'merged' ORDER BY pr.updated_at DESC LIMIT 1)
		FROM tasks t JOIN projects p ON p.id = t.project_id WHERE t.id = $1`, taskID).Scan(&key, &prNumber); err != nil {
		return err
	}
	if err := lockReleasesTx(ctx, tx, taskID); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT t.id, t.project_id, p.key_prefix || '-' || t.number,
			NOT EXISTS (SELECT 1 FROM task_blocked_by(t.id)),
			EXISTS (SELECT 1 FROM task_auto_starts a WHERE a.task_id = t.id)
		FROM task_relations r JOIN tasks t ON t.id = r.to_task_id JOIN projects p ON p.id = t.project_id
		WHERE r.from_task_id = $1 AND r.kind = 'blocks' AND t.status = ANY($2::task_status[])
		ORDER BY t.project_id, t.number`, taskID, notStartedStatuses)
	if err != nil {
		return err
	}
	type dependant struct {
		ID, ProjectID, Key string
		Free, Auto         bool
	}
	deps, err := pgx.CollectRows(rows, pgx.RowToStructByPos[dependant])
	if err != nil {
		return err
	}
	for _, d := range deps {
		event := func(typ string, payload map[string]any) error {
			_, err := ledger.Append(ctx, tx, ledger.Event{Type: typ, OrganizationID: org, ProjectID: d.ProjectID, TaskID: d.ID,
				ActorType: ledger.ActorSystem, ActorID: "dude", Source: ledger.SourceOrchestrator, CorrelationID: d.ID, Payload: payload})
			return err
		}
		if prNumber == nil {
			payload := map[string]any{"by": key}
			if p := actingPerson(ctx); p != "" {
				payload["person"] = p
			}
			if err := event(EvBlockerUnmerged, payload); err != nil {
				return err
			}
			continue
		}
		if !d.Free {
			continue
		}
		if err := event(EvTaskUnblocked, map[string]any{"by": key, "pr": *prNumber, "blockers": []any{}}); err != nil {
			return err
		}
		if d.Auto {
			// The worker starts it, after this commits: a start that fails
			// must never roll a merge back.
			if _, err := tx.Exec(ctx, `UPDATE task_auto_starts SET due_at = now() WHERE task_id = $1`, d.ID); err != nil {
				return err
			}
		} else if err := event(EvChatNotice, map[string]any{"about": "unblocked", "by": key,
			"text": fmt.Sprintf("%s merged — %s can start. Nothing started it: Deliver or Talk it through when you're ready.", key, d.Key)}); err != nil {
			return err
		}
		if err := wakeUnblockedTx(ctx, tx, org, d.ID, d.Key, key, d.Auto); err != nil {
			return err
		}
		// A delivery a person started, parked at its implement step on this
		// blocker (path 5), goes on: its step reads every blocker again.
		var wf string
		err := tx.QueryRow(ctx, `SELECT id FROM workflow_runs WHERE task_id = $1 AND workflow_type = $2 AND status = 'waiting'
			AND awaiting_signals ? $3 ORDER BY created_at DESC LIMIT 1`, d.ID, WorkflowType, SignalRelationsUnblock).Scan(&wf)
		if err == nil {
			err = workflow.SignalTx(ctx, tx, org, wf, SignalRelationsUnblock, map[string]any{"by": key}, "unblocked:"+taskID)
		}
		if err != nil && !db.IsNotFound(err) {
			return err
		}
	}
	return nil
}

// lockReleasesTx serialises the releases of each task taskID blocks, so
// two blockers of one task moving to done in overlapping transactions do
// not each see the other still open and release nobody: the second waits
// here, and the statement after it reads with a snapshot that sees the
// first's commit. An advisory lock per dependant, taken one statement each
// in id order, and taken by nothing else: a dependant's task row would put
// the merge (task, then delivery or task_auto_starts) against every start
// path (delivery or task_auto_starts, then task) and deadlock.
func lockReleasesTx(ctx context.Context, tx pgx.Tx, taskID string) error {
	rows, err := tx.Query(ctx, `SELECT to_task_id FROM task_relations WHERE from_task_id = $1 AND kind = 'blocks'
		ORDER BY to_task_id`, taskID)
	if err != nil {
		return err
	}
	deps, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, d := range deps {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('task_release:' || $1))`, d); err != nil {
			return err
		}
	}
	return nil
}

// wakeUnblockedTx tells a released task's live conductor it can start. A
// task with none gets none: a wake would start one, and that is a start.
func wakeUnblockedTx(ctx context.Context, tx pgx.Tx, org, taskID, taskKey, by string, auto bool) error {
	var live bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM runs r WHERE r.task_id = $1 AND `+LiveConductor+`)`, taskID).Scan(&live); err != nil || !live {
		return err
	}
	line := fmt.Sprintf("%s merged: %s can start now, nothing open in front of it. Tell the person; start nothing until they agree.", by, taskKey)
	if auto {
		line = fmt.Sprintf("%s merged: %s can start now. Its owner set it to start when unblocked, so it starts on its own: say so if it helps.", by, taskKey)
	}
	_, err := RecordWakeTx(ctx, tx, org, taskID, WakeUnblocked, "unblocked:"+by, line)
	return err
}

// EndAutoStartTx ends the task's start when unblocked without firing it,
// saying why (task.auto_start_cancelled): "owner", "started",
// "owner_changed", "unblocked_by_removal", "failed". Nothing when none is set.
func EndAutoStartTx(ctx context.Context, tx pgx.Tx, org, taskID, why, actorType, actorID string) error {
	_, err := endAutoStartTx(ctx, tx, org, taskID, why, actorType, actorID, nil)
	return err
}

// endAutoStartTx is EndAutoStartTx with more for the event's payload; it
// says whether there was a request to end.
func endAutoStartTx(ctx context.Context, tx pgx.Tx, org, taskID, why, actorType, actorID string, extra map[string]any) (bool, error) {
	var how, projectID string
	err := tx.QueryRow(ctx, `DELETE FROM task_auto_starts a USING tasks t WHERE a.task_id = $1 AND t.id = a.task_id
		RETURNING a.how, t.project_id`, taskID).Scan(&how, &projectID)
	if db.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if actorType == "" {
		actorType, actorID = ledger.ActorSystem, "dude"
	}
	payload := map[string]any{"how": how, "why": why}
	for k, v := range extra {
		payload[k] = v
	}
	_, err = ledger.Append(ctx, tx, ledger.Event{Type: EvAutoStartCancelled, OrganizationID: org, ProjectID: projectID, TaskID: taskID,
		ActorType: actorType, ActorID: actorID, Source: ledger.SourceOrchestrator, CorrelationID: taskID, Payload: payload})
	return true, err
}

// autoStartBatch bounds the requests one pass fires.
const autoStartBatch = 20

// AutoStarts is the worker that fires due start-when-unblocked requests.
type AutoStarts struct {
	DB      *db.DB
	Starter Starter
}

// Sweep fires the due requests, one transaction per task, each taken FOR
// UPDATE SKIP LOCKED so two workers never fire one twice. Returns how many
// it took.
func (a *AutoStarts) Sweep(ctx context.Context) (int, error) {
	type due struct{ Org, TaskID string }
	var todo []due
	if err := a.DB.InSystem(ctx, "auto-starts", func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT organization_id, task_id FROM task_auto_starts WHERE due_at <= now()
			ORDER BY due_at LIMIT $1`, autoStartBatch)
		if err != nil {
			return err
		}
		todo, err = pgx.CollectRows(rows, pgx.RowToStructByPos[due])
		return err
	}); err != nil {
		return 0, err
	}
	for _, d := range todo {
		err := a.DB.InOrg(ctx, d.Org, func(tx pgx.Tx) error { return a.fire(ctx, tx, d.Org, d.TaskID) })
		if err == nil {
			continue
		}
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		if ferr := a.DB.InOrg(ctx, d.Org, func(tx pgx.Tx) error { return a.notFired(ctx, tx, d.Org, d.TaskID, err) }); ferr != nil {
			return 0, ferr
		}
	}
	return len(todo), nil
}

// maxFireAttempts bounds the fires of one request that fail for a reason
// not known to be transient nor the start's own refusal.
const maxFireAttempts = 5

// fireRetryAfter is how long a request that did not fire waits before the
// next sweep takes it again, per counted attempt for an unknown error.
const fireRetryAfter = 5 * time.Second

// notFired settles a fire that failed with cause. The start's own refusal
// ends the request (failed); a transient failure leaves it due for a later
// sweep; anything else does too, counted, and ends it at maxFireAttempts.
func (a *AutoStarts) notFired(ctx context.Context, tx pgx.Tx, org, taskID string, cause error) error {
	if startRefused(cause) {
		return a.failed(ctx, tx, org, taskID, cause)
	}
	if transient(cause) {
		_, err := tx.Exec(ctx, `UPDATE task_auto_starts SET due_at = now() + make_interval(secs => $2)
			WHERE task_id = $1 AND due_at IS NOT NULL`, taskID, fireRetryAfter.Seconds())
		return err
	}
	var attempts int
	err := tx.QueryRow(ctx, `UPDATE task_auto_starts SET fire_attempts = fire_attempts + 1,
			due_at = now() + make_interval(secs => $2 * (fire_attempts + 1))
		WHERE task_id = $1 AND due_at IS NOT NULL RETURNING fire_attempts`, taskID, fireRetryAfter.Seconds()).Scan(&attempts)
	if db.IsNotFound(err) {
		return nil
	}
	if err != nil || attempts < maxFireAttempts {
		return err
	}
	return a.failed(ctx, tx, org, taskID, cause)
}

// startRefused says err is the start's own deterministic refusal, which
// firing again would meet again: the task gone (TaskNotFound, no row), a
// policy or delivery state that does not decode (PolicyError, a JSON
// error, as PolicyFor and LoadDelivery wrap it), or a write the data
// refuses (SQLSTATE class 22 data exception, 23 integrity violation:
// NameOnlyRepository's insert, the delivery's start).
func startRefused(err error) bool {
	var notFound TaskNotFound
	var policy PolicyError
	var syntax *json.SyntaxError
	var typ *json.UnmarshalTypeError
	if errors.As(err, &notFound) || errors.As(err, &policy) ||
		errors.As(err, &syntax) || errors.As(err, &typ) || db.IsNotFound(err) {
		return true
	}
	var pg *pgconn.PgError
	return errors.As(err, &pg) && (strings.HasPrefix(pg.Code, "22") || strings.HasPrefix(pg.Code, "23"))
}

// transient says err is the database's or the connection's, not the
// start's: SQLSTATE class 40 (deadlock, serialisation), 57 (statement
// timeout, admin shutdown), 08 (connection), 53 (resources), or a
// connection that failed, timed out or was closed.
func transient(err error) bool {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		switch pg.Code[:min(2, len(pg.Code))] {
		case "40", "57", "08", "53":
			return true
		}
		return false
	}
	var connect *pgconn.ConnectError
	var netErr net.Error
	return errors.As(err, &connect) || errors.As(err, &netErr) || pgconn.Timeout(err) || pgconn.SafeToRetry(err) ||
		errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, context.DeadlineExceeded)
}

// fire is one due request: re-read every blocker, then the same start a
// person makes (StartTx), as dude for the person who set it.
func (a *AutoStarts) fire(ctx context.Context, tx pgx.Tx, org, taskID string) error {
	var how, setBy, setByName, projectID, taskKey, status string
	var setAt time.Time
	err := tx.QueryRow(ctx, `SELECT a.how, a.set_by_person, COALESCE(p.name, ''), a.set_at FROM task_auto_starts a
		LEFT JOIN people p ON p.id = a.set_by_person
		WHERE a.task_id = $1 AND a.due_at <= now() FOR UPDATE OF a SKIP LOCKED`, taskID).Scan(&how, &setBy, &setByName, &setAt)
	if db.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := tx.QueryRow(ctx, `SELECT t.project_id, p.key_prefix || '-' || t.number, t.status::text FROM tasks t
		JOIN projects p ON p.id = t.project_id WHERE t.id = $1`, taskID).Scan(&projectID, &taskKey, &status); err != nil {
		return err
	}
	event := func(typ string, payload map[string]any) error {
		_, err := ledger.Append(ctx, tx, ledger.Event{Type: typ, OrganizationID: org, ProjectID: projectID, TaskID: taskID,
			ActorType: ledger.ActorSystem, ActorID: "dude", Source: ledger.SourceOrchestrator, CorrelationID: taskID, Payload: payload})
		return err
	}
	if !NotStarted(status) {
		// Started some other way since: the request was consumed.
		return EndAutoStartTx(ctx, tx, org, taskID, "started", "", "")
	}
	by, pr, err := lastUnblock(ctx, tx, taskID)
	if err != nil {
		return err
	}
	first := firstName(setByName)
	// The locks a start takes, in its order (Chat's, the delivery's, the
	// task's), before the blockers are read under the task's. StartTx's own
	// guard reads them again, but skips that when it joins a live delivery
	// (one parked on its blockers at path 5 is live, its task queued), so
	// fire checks here too: a fired request never joins past an open one.
	if how == HowTalk {
		if err := LockChat(ctx, tx, taskID); err != nil {
			return err
		}
	}
	if _, err := LoadDelivery(ctx, tx, taskID); err != nil {
		return err
	}
	err = RefuseBlockedTx(ctx, tx, taskID)
	if blocked, ok := AsBlocked(err); ok {
		if _, err := tx.Exec(ctx, `UPDATE task_auto_starts SET due_at = NULL WHERE task_id = $1`, taskID); err != nil {
			return err
		}
		if err := RecordStartRefusedTx(ctx, tx, org, taskID, "auto_start", ledger.ActorSystem, "dude", blocked.Blockers); err != nil {
			return err
		}
		if err := event(EvAutoStartRefused, map[string]any{"by": by, "blockers": BlockersPayload(blocked.Blockers)}); err != nil {
			return err
		}
		words := make([]string, len(blocked.Blockers))
		for i, b := range blocked.Blockers {
			words[i] = b.Words()
		}
		that := "that merges"
		if len(words) > 1 {
			that = "they merge"
		}
		return event(EvChatNotice, map[string]any{"about": "auto_start_refused", "text": fmt.Sprintf(
			"%s merged, but %s is still blocked by %s: it starts when %s, as %s asked.", by, taskKey, KeyList(words), that, first)})
	}
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM task_auto_starts WHERE task_id = $1`, taskID); err != nil {
		return err
	}
	payload := map[string]any{"how": how, "by": by, "setBy": setBy, "setByName": setByName, "setAt": setAt.UTC()}
	if pr != nil {
		payload["pr"] = *pr
	}
	if err := event(EvAutoStarted, payload); err != nil {
		return err
	}
	in := StartInput{Org: org, TaskID: taskID, How: how, Writer: Writer{ActorType: ledger.ActorSystem, ActorID: "dude", Person: setBy}}
	if how == HowTalk {
		in.Message = fmt.Sprintf("%s set this task to be talked through once nothing was in front of it, and %s merged. "+
			"Read the task and the merged code, then ask %s what you need to know, or propose a plan. Nothing is built until they agree.",
			first, by, first)
	}
	if _, err := StartTx(ctx, tx, a.Starter, in); err != nil {
		return err
	}
	return event(EvChatNotice, map[string]any{"about": "auto_started", "text": fmt.Sprintf(
		"%s merged — %s started (%s), as %s asked. Nothing else was waiting in front of it.", by, taskKey, howLabel[how], first)})
}

var howLabel = map[string]string{HowDeliver: "Deliver", HowTalk: "Talk it through"}

// lastUnblock is the blocker whose merge released the task last, and its
// pull request's number: "" and nil when none is recorded.
func lastUnblock(ctx context.Context, tx pgx.Tx, taskID string) (string, *int, error) {
	var by *string
	var pr *int
	err := tx.QueryRow(ctx, `SELECT payload->>'by', (payload->>'pr')::int FROM events WHERE task_id = $1 AND event_type = $2
		ORDER BY cursor DESC LIMIT 1`, taskID, EvTaskUnblocked).Scan(&by, &pr)
	if db.IsNotFound(err) {
		return "", nil, nil
	}
	if err != nil {
		return "", nil, err
	}
	if by == nil {
		return "", pr, nil
	}
	return *by, pr, nil
}

// failed ends a request that will not fire (task.auto_start_cancelled, why
// "failed", with the error), and says so in the task's Chat, so none is
// left standing to fire unexpectedly on some later merge.
func (a *AutoStarts) failed(ctx context.Context, tx pgx.Tx, org, taskID string, cause error) error {
	msg := clip(cause.Error(), 500)
	ended, err := endAutoStartTx(ctx, tx, org, taskID, AutoStartEndFailed, "", "", map[string]any{"error": msg})
	if err != nil || !ended {
		return err
	}
	var projectID, taskKey string
	if err := tx.QueryRow(ctx, `SELECT t.project_id, p.key_prefix || '-' || t.number FROM tasks t
		JOIN projects p ON p.id = t.project_id WHERE t.id = $1`, taskID).Scan(&projectID, &taskKey); err != nil {
		return err
	}
	by, _, err := lastUnblock(ctx, tx, taskID)
	if err != nil {
		return err
	}
	text := fmt.Sprintf("%s could not start: %s. Start it from its page.", taskKey, msg)
	if by != "" {
		text = by + " merged, but " + text
	}
	_, err = ledger.Append(ctx, tx, ledger.Event{Type: EvChatNotice, OrganizationID: org, ProjectID: projectID, TaskID: taskID,
		ActorType: ledger.ActorSystem, ActorID: "dude", Source: ledger.SourceOrchestrator, CorrelationID: taskID,
		Payload: map[string]any{"about": "auto_start_failed", "text": text}})
	return err
}

// firstName is a person's first name, or "its owner" for none.
func firstName(name string) string {
	if f := strings.Fields(name); len(f) > 0 {
		return f[0]
	}
	return "its owner"
}
