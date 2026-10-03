package delivery

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/ids"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// A person's message in a task's Chat (on the conductor's Run), and dude's
// briefing of a new conductor (the text of its first prompt).
const (
	EvChatMessage      = "chat.message"
	EvConductorBriefed = "conductor.briefed"
)

// LockChat takes the task's Chat lock for the rest of tx. Everything that
// decides which conductor hears a message holds it: Chat taking a message,
// and the syncer ending a conductor. So a message is either queued before
// the conductor ends, and handed on by its end (HandOver), or written after
// it, and finds it ended. Its own lock, not the task's row: a conductor's
// own transactions (a question it asks) hold its Run and then update the
// task, and Chat takes the Run after this lock.
func LockChat(ctx context.Context, tx pgx.Tx, taskID string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('chat:' || $1))`, taskID)
	return err
}

// Writer is who wrote in a task's Chat: the ledger's actor, and the person
// behind it ("" for none).
type Writer struct{ ActorType, ActorID, Person string }

// StartConductor creates the task's conductor: a Run with no phase, role
// conductor, from the task's head in each repository (the default branch
// where nothing was published), briefed by dude with the message.
func StartConductor(ctx context.Context, tx pgx.Tx, org, projectID, taskID string, w Writer, message string) (string, error) {
	return startConductor(ctx, tx, org, projectID, taskID, w, message, false)
}

// startConductor is StartConductor; with woken, the message is dude's wake
// note, not a person's.
func startConductor(ctx context.Context, tx pgx.Tx, org, projectID, taskID string, w Writer, message string, woken bool) (string, error) {
	id := ids.New(ids.Run)
	var person string
	if w.Person != "" {
		_ = tx.QueryRow(ctx, `SELECT name FROM people WHERE id = $1`, w.Person).Scan(&person)
	}
	briefing, err := briefing(ctx, tx, taskID, id, person, message, woken)
	if err != nil {
		return "", err
	}
	heads, err := TaskHeads(ctx, tx, taskID)
	if err != nil {
		return "", err
	}
	baseRefs := map[string]string{}
	for _, h := range heads {
		if h.SHA != "" {
			baseRefs[h.Repo] = h.SHA
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, kind, role,
			base_refs, prompt, started_by)
		VALUES ($1, $2, $3, $4, COALESCE((SELECT max(attempt) FROM runs WHERE task_id = $4), 1), 'pending', 'agent',
			'conductor', $5, $6, NULLIF($7, ''))`,
		id, org, projectID, taskID, baseRefs, briefing, w.Person); err != nil {
		return "", err
	}
	_, err = ledger.Append(ctx, tx, ledger.Event{Type: EvRunCreated, OrganizationID: org, ProjectID: projectID,
		TaskID: taskID, RunID: id, ActorType: w.ActorType, ActorID: w.ActorID, Source: ledger.SourceOrchestrator,
		CorrelationID: taskID, Payload: map[string]any{"role": RoleConductor, "publishes": false, "baseRefs": baseRefs}})
	if err != nil {
		return "", err
	}
	// The message, then dude's briefing of the conductor about it: what the
	// Chat shows, whatever the agent's harness echoes back of its prompt. A
	// wake's note is recorded by the wake (WakeConductorTx).
	ref := RunRef{Org: org, ProjectID: projectID, TaskID: taskID, RunID: id}
	if !woken {
		if err := ChatEvent(ctx, tx, ref, w, map[string]any{"text": message}); err != nil {
			return "", err
		}
	}
	_, err = ledger.Append(ctx, tx, ledger.Event{Type: EvConductorBriefed, OrganizationID: org, ProjectID: projectID,
		TaskID: taskID, RunID: id, ActorType: ledger.ActorSystem, ActorID: "dude", Source: ledger.SourceOrchestrator,
		CorrelationID: taskID, Payload: map[string]any{"text": briefing}})
	return id, err
}

// ChatEvent records a person's message in the task's Chat, on the
// conductor's Run.
func ChatEvent(ctx context.Context, tx pgx.Tx, ref RunRef, w Writer, payload map[string]any) error {
	_, err := ledger.Append(ctx, tx, ledger.Event{
		Type: EvChatMessage, OrganizationID: ref.Org, ProjectID: ref.ProjectID, TaskID: ref.TaskID, RunID: ref.RunID,
		ActorType: w.ActorType, ActorID: w.ActorID, Source: ledger.SourceOrchestrator, CorrelationID: ref.TaskID, Payload: payload,
	})
	return err
}

// Ending (SQL, over runs r): a live conductor whose container stopped
// without dude asking. Nothing will resume it: it is ended (EndConductor),
// by the syncer or by the next message in Chat, and nothing more is queued for it.
const Ending = `(r.status IN ('scheduled', 'starting', 'running') AND r.lux_state IN ('stopped', 'succeeded', 'failed', 'cancelled', 'lost')
	AND r.lux_stop_reason IS NULL AND r.control = 'none')`

// EndConductor completes a conductor that can no longer be resumed, and
// hands what it was sent and never read to the next (HandOver). The caller
// holds the task's Chat lock.
func EndConductor(ctx context.Context, tx pgx.Tx, ref RunRef, why string) error {
	tag, err := tx.Exec(ctx, `UPDATE runs r SET status = 'completed', ended_at = now(), lux_stop_reason = 'complete'
		WHERE r.id = $1 AND `+Ending, ref.RunID)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	if _, err := ledger.Append(ctx, tx, ref.Event("run.completed", ledger.ActorSystem,
		map[string]any{"status": "completed", "reason": why})); err != nil {
		return err
	}
	_, err = HandOver(ctx, tx, ref, true)
	return err
}

// Unheard (SQL, over runs r): an ended conductor holding input it never
// read, or a wake note it was started with and never heard, for the syncer
// to hand over (HandOver). The subqueries read only unsettled directives
// (directives_unsettled_idx) and unheard briefings
// (conductor_wake_attempts_briefing_idx), not every ended Run.
const Unheard = `(r.role = 'conductor' AND r.status IN ('completed', 'failed', 'aborted')
	AND (r.id IN (SELECT d.run_id FROM directives d WHERE d.delivered_at IS NULL AND d.failed_at IS NULL)
	  OR r.id IN (SELECT a.conductor_run_id FROM conductor_wake_attempts a
	    WHERE a.directive_id IS NULL AND a.heard_at IS NULL AND a.failed_at IS NULL)))`

// HandOver settles what a person sent an ended conductor and it never
// read. With replace, each message goes to the task's live conductor, or
// to a new one: the first becomes its briefing's message, as a first
// message is, and the rest are queued for it. Each keeps its writer and
// its images (DirectiveAttachments). A briefing carries no images, so a first
// message with images for a new conductor is failed, saying to send it
// again. The ended one's copies fail saying which conductor has them, so
// its Chat shows where they went. Without replace (a person aborted it)
// they fail, saying it was stopped. An interrupt (an idle nudge, an
// Interrupt now) is no one's words and only fails. Returns the conductor
// that has them, "" for none. The caller holds the task's Chat lock, and
// the Run has ended.
func HandOver(ctx context.Context, tx pgx.Tx, ref RunRef, replace bool) (string, error) {
	// The wake note it was started with and never heard is told again.
	if err := BriefingUnheardTx(ctx, tx, ref.RunID); err != nil {
		return "", err
	}
	rows, err := tx.Query(ctx, `SELECT d.id, d.text, d.interrupt OR EXISTS (SELECT 1 FROM conductor_wake_attempts a WHERE a.directive_id = d.id),
			COALESCE(e.actor_type, ''), COALESCE(e.actor_id, '')
		FROM directives d
		LEFT JOIN LATERAL (SELECT e.actor_type, e.actor_id FROM events e WHERE e.run_id = d.run_id
			AND e.event_type IN ('chat.message', 'question.answered', 'run.steered') AND e.payload->>'directiveId' = d.id
			ORDER BY e.cursor LIMIT 1) e ON true
		WHERE d.run_id = $1 AND d.delivered_at IS NULL AND d.failed_at IS NULL
		ORDER BY d.created_at, d.id FOR UPDATE OF d`, ref.RunID)
	if err != nil {
		return "", err
	}
	type unread struct {
		ID, Text string
		// An interrupt, or dude's wake note (whose reasons go back to
		// pending, RequeueWakesTx): no one's words to hand on.
		Interrupt          bool
		ActorType, ActorID string
	}
	left, err := pgx.CollectRows(rows, pgx.RowToStructByPos[unread])
	if err != nil || len(left) == 0 {
		return "", err
	}
	stopped := "the conductor stopped before reading it"
	why := map[string]string{}
	next := ""
	if replace {
		for _, d := range left {
			if d.Interrupt {
				continue
			}
			to, err := handTo(ctx, tx, ref, next, d.ID, Writer{ActorType: d.ActorType, ActorID: d.ActorID}, d.Text)
			if err != nil {
				return "", err
			}
			if to == "" {
				why[d.ID] = stopped + "; its images were not passed on; send it again"
				continue
			}
			next = to
		}
	}
	for _, d := range left {
		reason, to := why[d.ID], next
		switch {
		case reason != "":
			to = ""
		case !replace:
			reason = "the conductor was stopped before reading it"
		case next != "":
			reason = fmt.Sprintf("%s; the next conductor, %s, has it", stopped, next)
		default:
			reason = stopped
		}
		if _, err := tx.Exec(ctx, `UPDATE directives SET failed_at = now(), error = $2 WHERE id = $1`, d.ID, reason); err != nil {
			return "", err
		}
		// A wake note not inherited goes back to its reasons, for the next
		// conductor.
		if err := RequeueWakesTx(ctx, tx, d.ID); err != nil {
			return "", err
		}
		if _, err := ledger.Append(ctx, tx, ref.Event(evDirectiveFailed, ledger.ActorSystem,
			map[string]any{"directiveId": d.ID, "error": reason, "nextRunId": db.Nullable(to)})); err != nil {
			return "", err
		}
	}
	return next, nil
}

// evDirectiveFailed is the phase syncer's run.directive.failed.
const evDirectiveFailed = "run.directive.failed"

// handTo gives one message an ended conductor never read — directive
// from, written by w — to the conductor that now hears the task's Chat:
// to, once an earlier message reached it; else the task's live conductor;
// else a new one it starts, briefed with the message. Queued, the message
// takes its images with it. Returns the conductor, or "" when the message
// has images and would have to be a new conductor's briefing.
func handTo(ctx context.Context, tx pgx.Tx, ended RunRef, to, from string, w Writer, text string) (string, error) {
	if w.ActorType == "" {
		w.ActorType = ledger.ActorSystem
	}
	if to == "" {
		err := tx.QueryRow(ctx, `SELECT r.id FROM runs r WHERE r.task_id = $1 AND `+LiveConductor+` FOR NO KEY UPDATE`,
			ended.TaskID).Scan(&to)
		if err != nil && !db.IsNotFound(err) {
			return "", err
		}
	}
	if to == "" {
		images, err := DirectiveAttachments(ctx, tx, from)
		if err != nil || len(images) > 0 {
			return "", err
		}
		// The person behind an API key, or the person themself.
		if err := tx.QueryRow(ctx, `SELECT COALESCE((SELECT id FROM people WHERE id = $1),
			(SELECT person_id FROM api_keys WHERE id = $1), '')`, w.ActorID).Scan(&w.Person); err != nil {
			return "", err
		}
		return StartConductor(ctx, tx, ended.Org, ended.ProjectID, ended.TaskID, w, text)
	}
	ref := RunRef{Org: ended.Org, ProjectID: ended.ProjectID, TaskID: ended.TaskID, RunID: to}
	// Superseding from with the same words, the copy resolves from's images
	// through it (DirectiveAttachments): retries sharing them all keep them.
	id, _, err := QueueDirective(ctx, tx, ref, Directive{Text: text, Scope: "run", Supersedes: from})
	if err != nil {
		return "", err
	}
	images, err := directiveAttachmentInfo(ctx, tx, id)
	if err != nil {
		return "", err
	}
	if err := RequestResumeForMessage(ctx, tx, to, "a message handed on from the conductor before"); err != nil {
		return "", err
	}
	payload := map[string]any{"text": text, "directiveId": id}
	if len(images) > 0 {
		payload["attachments"] = images
	}
	return to, ChatEvent(ctx, tx, ref, w, payload)
}

// RequestResumeForMessage asks for a paused conductor back for a message
// queued for it, when a person paused it or it was parked as idle. Parked
// by dude otherwise, it resumes for the message on its own (resumable).
func RequestResumeForMessage(ctx context.Context, tx pgx.Tx, runID, reason string) error {
	_, err := tx.Exec(ctx, `UPDATE runs SET control = 'resume', control_requested_at = now(), control_reason = $2
		WHERE id = $1 AND status = 'paused' AND (dude_pause IS NULL OR dude_pause = 'idle')`, runID, reason)
	return err
}

// LiveConductor (SQL, over runs r): the task's conductor that can still
// hear a message — at most one (runs_live_conductor_idx).
const LiveConductor = `r.role = 'conductor' AND r.kind = 'agent' AND r.status IN ('pending', 'scheduled', 'starting', 'running', 'paused')`
