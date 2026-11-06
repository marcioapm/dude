package delivery

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/ids"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// EvQuestionRebound is a question moved from a conductor's Run lux lost to
// the Run that replaces it (ReplaceLostConductorTx): it stays open, and its
// answer reaches the replacement. Not replayed: the question.asked is.
const EvQuestionRebound = "question.rebound"

// question.closed's by for a person's question that died with its
// conductor's Run: lux lost it (QuestionLost), or it failed otherwise
// (QuestionRunFailed).
const (
	QuestionLost      = "lost"
	QuestionRunFailed = "run_failed"
)

// LostMode is how ReplaceLostConductorTx replaces a conductor.
type LostMode int

const (
	// Parked: lux lost a live conductor that held nothing in flight and
	// waited on a person. It ends failed, and a placeholder takes its
	// slot and its questions, started only once the person answers.
	Parked LostMode = iota
	// Reask: Try again on a failed conductor with nothing unread. A fresh
	// conductor, briefed on the failure, with the question lost with it
	// asked again by dude.
	Reask
)

// LostByLux says a conductor's failure reason is lux no longer having its
// Run: the question.closed it settles its questions with says "lost".
func LostByLux(reason string) bool {
	return strings.Contains(reason, "lux no longer has")
}

// ReplaceLostConductorTx replaces the task's conductor ref. In Parked
// mode, lux lost it while it held nothing in flight: it is ended failed
// with lux_lost_at, and a replacement is inserted in its slot paused for
// the person with no lux Run (lux_run_id NULL, no prompt yet: written when
// the person's answer starts it, LostBriefing). Its open questions, waits
// and unread input move to the replacement, so the question stays open and
// its answer reaches it. Returns "" when it cannot be replaced (not a live
// conductor, publishing, halted, nothing a person is asked): the caller
// fails it as before. The caller holds no lock.
//
// In Reask mode (RetryConductor; the caller holds the Chat lock) ref
// already failed: a fresh conductor is started pending, briefed on the
// failure, and the person question lost with ref is asked again on it by
// dude (a gate question only at the heads it was asked at).
func ReplaceLostConductorTx(ctx context.Context, tx pgx.Tx, ref RunRef, why string, mode LostMode) (string, error) {
	if err := LockChat(ctx, tx, ref.TaskID); err != nil {
		return "", err
	}
	projectID, d, err := LockEscalationTx(ctx, tx, ref.TaskID)
	if err != nil {
		return "", err
	}
	if ref.ProjectID == "" {
		ref.ProjectID = projectID
	}
	if mode == Reask {
		return reaskTx(ctx, tx, ref, d)
	}
	var eligible bool
	var startedBy *string
	err = tx.QueryRow(ctx, `SELECT r.role = 'conductor' AND r.kind = 'agent' AND r.status IN ('running', 'paused')
			AND r.lux_lost_at IS NULL
			AND ((r.status = 'paused' AND r.dude_pause IN ('person', 'conductor'))
			     OR ((r.turn_done_at IS NOT NULL OR r.waiting_since IS NOT NULL) AND COALESCE(r.push_request_id, '') = ''))
			AND (EXISTS (SELECT 1 FROM questions q WHERE q.run_id = r.id AND q.status = 'open' AND q.addressee = 'person')
			     OR EXISTS (SELECT 1 FROM questions q
			         JOIN events e ON e.run_id = q.run_id AND e.organization_id = q.organization_id AND e.task_id = q.task_id
			           AND e.event_type = 'question.answered' AND e.payload->>'questionId' = q.id
			         JOIN directives x ON x.id = e.payload->>'directiveId' AND x.run_id = q.run_id
			           AND x.organization_id = q.organization_id AND x.task_id = q.task_id
			         WHERE q.run_id = r.id AND q.organization_id = r.organization_id AND q.task_id = r.task_id
			           AND q.status = 'answered' AND q.addressee = 'person'
			           AND x.delivered_at IS NULL AND x.failed_at IS NULL)),
			r.started_by
		FROM runs r WHERE r.id = $1 FOR NO KEY UPDATE`, ref.RunID).Scan(&eligible, &startedBy)
	if db.IsNotFound(err) || err == nil && !eligible {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if d != nil && Ended(d.TaskStatus) {
		return "", nil
	}
	if halted, err := HaltedTx(ctx, tx, ref.TaskID); err != nil || halted {
		return "", err
	}
	// A publish moving the task branch must finish (or fail) first: the
	// Run is failed as before. Those that moved nothing are refused.
	if moving, err := publishMovingTx(ctx, tx, ref.RunID); err != nil || moving {
		return "", err
	}
	if err := RefuseUnmovedTx(ctx, tx, ref, "your conductor's Run was lost before it was published"); err != nil {
		return "", err
	}
	name, err := askedName(ctx, tx, ref.TaskID, ref.RunID)
	if err != nil {
		return "", err
	}
	reason := "lux lost this Run while it waited on " + name
	if why != "" {
		reason += " (" + why + ")"
	}
	// lux_lost_at in the same statement as status: the immediate trigger
	// (cancel_open_questions) skips the Run, the deferred one
	// (cancel_lost_questions) cancels at commit what is not moved.
	tag, err := tx.Exec(ctx, `UPDATE runs SET status = 'failed', error = $2, ended_at = now(), keep = false, lux_lost_at = now()
		WHERE id = $1 AND status IN ('running', 'paused') AND lux_lost_at IS NULL`, ref.RunID, reason)
	if err != nil || tag.RowsAffected() == 0 {
		return "", err
	}
	if err := MarkChildParentEndedTx(ctx, tx, ref.TaskID, ref.RunID); err != nil {
		return "", err
	}
	baseRefs, err := conductorBases(ctx, tx, ref.TaskID)
	if err != nil {
		return "", err
	}
	id := ids.New(ids.Run)
	if _, err := tx.Exec(ctx, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, kind, role,
			base_refs, started_by, dude_pause, control, waiting_since, control_reason)
		VALUES ($1, $2, $3, $4, COALESCE((SELECT max(attempt) FROM runs WHERE task_id = $4), 1), 'paused', 'agent',
			'conductor', $5, $6, 'person', 'none', now(), 'parked while it waits for a person')`,
		id, ref.Org, ref.ProjectID, ref.TaskID, baseRefs, startedBy); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `UPDATE runs SET replaced_by = $2 WHERE id = $1`, ref.RunID, id); err != nil {
		return "", err
	}
	rows, err := tx.Query(ctx, `UPDATE questions SET run_id = $2 WHERE run_id = $1 AND status = 'open' RETURNING id`, ref.RunID, id)
	if err != nil {
		return "", err
	}
	moved, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return "", err
	}
	// What waits on the conductor's Run id and is still pending.
	for _, q := range []string{
		`UPDATE conductor_wake_attempts SET conductor_run_id = $2
			WHERE conductor_run_id = $1 AND directive_id IS NULL AND heard_at IS NULL AND failed_at IS NULL`,
		`UPDATE conductor_waits SET conductor_run_id = $2 WHERE conductor_run_id = $1 AND ended_at IS NULL`,
		`UPDATE peer_waits SET run_id = $2 WHERE run_id = $1 AND closed_at IS NULL`,
	} {
		if _, err := tx.Exec(ctx, q, ref.RunID, id); err != nil {
			return "", err
		}
	}
	next := RunRef{Org: ref.Org, ProjectID: ref.ProjectID, TaskID: ref.TaskID, RunID: id}
	dude := func(r RunRef, typ string, payload map[string]any) error {
		e := r.Event(typ, ledger.ActorSystem, payload)
		e.ActorID = "dude"
		_, err := ledger.Append(ctx, tx, e)
		return err
	}
	if err := dude(ref, "run.failed", map[string]any{"status": "failed", "error": reason, "luxLost": true, "replacedBy": id}); err != nil {
		return "", err
	}
	if err := dude(next, EvRunCreated, map[string]any{"role": RoleConductor, "publishes": false, "baseRefs": baseRefs,
		"replaces": ref.RunID, "why": "lux_lost"}); err != nil {
		return "", err
	}
	for _, q := range moved {
		if err := dude(next, EvQuestionRebound, map[string]any{"questionId": q, "from": ref.RunID, "to": id}); err != nil {
			return "", err
		}
	}
	var parkedAt any
	if err := tx.QueryRow(ctx, `SELECT now()`).Scan(&parkedAt); err != nil {
		return "", err
	}
	if err := dude(next, "run.parked", map[string]any{"reason": "person", "parkedAt": parkedAt,
		"message": "lux lost the conductor's Run while it waited for " + name + "; your question is still open. " +
			"Answering starts a fresh conductor with the conversation so far."}); err != nil {
		return "", err
	}
	// Messages written while it was parked, and an answer that committed
	// just before: queued on the replacement (LiveConductor finds it).
	if _, err := HandOver(ctx, tx, ref, true); err != nil {
		return "", err
	}
	return id, nil
}

// publishMovingTx says one of the Run's publishes is moving the task branch.
func publishMovingTx(ctx context.Context, tx pgx.Tx, runID string) (bool, error) {
	var moving bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM conductor_publishes WHERE run_id = $1 AND status = $2)`,
		runID, PublishMoving).Scan(&moving)
	return moving, err
}

// askedName is who the Run's latest person question is to: its addressee,
// else the task's owner, else "the person".
func askedName(ctx context.Context, tx pgx.Tx, taskID, runID string) (string, error) {
	var name string
	err := tx.QueryRow(ctx, `SELECT COALESCE(
			(SELECT p.name FROM questions q JOIN people p ON p.id = q.to_person WHERE q.run_id = $2 AND q.addressee = 'person'
				ORDER BY q.asked_at DESC LIMIT 1),
			(SELECT p.name FROM task_people tp JOIN people p ON p.id = tp.person_id WHERE tp.task_id = $1 ORDER BY tp.position LIMIT 1),
			'the person')`, taskID, runID).Scan(&name)
	return name, err
}

// LostQuestion is the person's question a failed conductor's Run was
// waiting on when it ended: the newest cancelled with it and not closed
// for a reason of its own (a decision, the gate left, withdrawn).
type LostQuestion struct {
	ID, Prompt                   string
	Items                        []QuestionItem
	GateHeads                    json.RawMessage
	Escalation, ToPerson, Relays *string
	Actions                      json.RawMessage
}

// LostQuestionTx is the Run's LostQuestion, nil for none.
func LostQuestionTx(ctx context.Context, tx pgx.Tx, runID string) (*LostQuestion, error) {
	var q LostQuestion
	err := tx.QueryRow(ctx, `SELECT q.id, q.prompt, q.items, q.pr_gate_heads, q.escalation, q.actions, q.to_person, q.relays FROM questions q
		WHERE q.run_id = $1 AND q.addressee = 'person' AND q.status = 'cancelled'
		  AND NOT EXISTS (SELECT 1 FROM events e WHERE e.run_id = q.run_id AND e.event_type = 'question.closed'
		    AND e.payload->>'questionId' = q.id AND COALESCE(e.payload->>'by', '') NOT IN ($2, $3))
		ORDER BY q.asked_at DESC, q.id DESC LIMIT 1`, runID, QuestionLost, QuestionRunFailed).
		Scan(&q.ID, &q.Prompt, &q.Items, &q.GateHeads, &q.Escalation, &q.Actions, &q.ToPerson, &q.Relays)
	if db.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &q, nil
}

// reaskTx is ReplaceLostConductorTx's Reask: a fresh conductor for the
// failed ref, pending, asked again what was lost with ref.
func reaskTx(ctx context.Context, tx pgx.Tx, ref RunRef, d *Delivery) (string, error) {
	var failedWith string
	var baseRefs map[string]string
	var startedBy *string
	if err := tx.QueryRow(ctx, `SELECT COALESCE(error, ''), base_refs, started_by FROM runs WHERE id = $1 AND status = 'failed'`,
		ref.RunID).Scan(&failedWith, &baseRefs, &startedBy); err != nil {
		if db.IsNotFound(err) {
			return "", ErrNothingToRetry
		}
		return "", err
	}
	lost, err := LostQuestionTx(ctx, tx, ref.RunID)
	if err != nil {
		return "", err
	}
	if fresh, err := conductorBases(ctx, tx, ref.TaskID); err != nil {
		return "", err
	} else if len(fresh) > 0 || baseRefs == nil {
		baseRefs = fresh
	}
	id := ids.New(ids.Run)
	if _, err := tx.Exec(ctx, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, kind, role,
			base_refs, prompt, started_by)
		VALUES ($1, $2, $3, $4, COALESCE((SELECT max(attempt) FROM runs WHERE task_id = $4), 1), 'pending', 'agent',
			'conductor', $5, '', $6)`,
		id, ref.Org, ref.ProjectID, ref.TaskID, baseRefs, startedBy); err != nil {
		return "", err
	}
	next := RunRef{Org: ref.Org, ProjectID: ref.ProjectID, TaskID: ref.TaskID, RunID: id}
	if _, err := tx.Exec(ctx, `UPDATE runs SET replaced_by = $2 WHERE id = $1 AND replaced_by IS NULL`, ref.RunID, id); err != nil {
		return "", err
	}
	e := next.Event(EvRunCreated, ledger.ActorSystem, map[string]any{"role": RoleConductor, "publishes": false,
		"baseRefs": baseRefs, "retries": ref.RunID})
	e.ActorID = "dude"
	if _, err := ledger.Append(ctx, tx, e); err != nil {
		return "", err
	}
	l := lostBrief{Old: ref.RunID, Failed: true, Error: failedWith}
	if l.Name, err = askedName(ctx, tx, ref.TaskID, ref.RunID); err != nil {
		return "", err
	}
	if lost != nil {
		l.Prompt, l.Choices = lost.Prompt, AskOptions(lost.Items)
		if l.Reasked, l.NotReasked, err = reask(ctx, tx, next, d, lost); err != nil {
			return "", err
		}
	}
	prompt, err := briefing(ctx, tx, ref.TaskID, id, "", render(lostRunTmpl, l), rebrief)
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `UPDATE runs SET prompt = $2 WHERE id = $1`, id, prompt); err != nil {
		return "", err
	}
	b := next.Event(EvConductorBriefed, ledger.ActorSystem, map[string]any{"text": prompt})
	b.ActorID = "dude"
	_, err = ledger.Append(ctx, tx, b)
	return id, err
}

// reask asks the lost question again on the new conductor, without a
// model call: a gate question only while the delivery is at the gate at
// the heads it was asked at, and not at an untested head; a relay
// only while the writer's question may still be relayed (stillRelays), and
// not at all once it was handed to the person. Returns the new question, or
// why it was not asked.
func reask(ctx context.Context, tx pgx.Tx, ref RunRef, d *Delivery, q *LostQuestion) (string, string, error) {
	if len(q.GateHeads) == 0 || string(q.GateHeads) == "null" {
		p := ToPerson{Ref: ref, Why: WhyConductor, Items: q.Items}
		if q.Relays != nil {
			relays, handed, err := stillRelays(ctx, tx, ref, *q.Relays)
			if err != nil {
				return "", "", err
			}
			if handed {
				return "", "the writer's question was handed to the person since", nil
			}
			p.Relays = relays
		}
		id, err := ToPersonTx(ctx, tx, p)
		if err != nil {
			return "", "", err
		}
		// These are typed links, never inferred from the prompt. Preserve the
		// cancelled audit row and restore the new question's decision routing.
		if _, err := tx.Exec(ctx, `UPDATE questions SET escalation = $2, actions = $3, to_person = $4 WHERE id = $1`,
			id, q.Escalation, q.Actions, q.ToPerson); err != nil {
			return "", "", err
		}
		if err := rebindLimitRequestTx(ctx, tx, ref.TaskID, q.ID, id); err != nil {
			return "", "", err
		}
		return id, "", nil
	}
	if d == nil || !d.AtGate() {
		return "", "the delivery is no longer at the pull request gate", nil
	}
	var asked, now map[string]string
	_ = json.Unmarshal(q.GateHeads, &asked)
	now = nonNilMap(d.State.Heads)
	if !sameHeads(asked, now) {
		return "", "the work moved on since: the heads are not the ones it was asked about", nil
	}
	if untested, err := UntestedConductorHeadTx(ctx, tx, ref.TaskID, d.State.Heads); err != nil {
		return "", "", err
	} else if untested {
		return "", "the head is a conductor's commit nothing has reviewed", nil
	}
	id, err := askGate(ctx, tx, ref, &d.State, q.Prompt, WhyConductor)
	return id, "", err
}

// stillRelays rechecks, under the locks reask holds (Chat → delivery →
// task, then the question and its Run, as CheckRelayTx takes them), that
// the lost relay's writer question id may still be relayed: open with the
// conductor, asked by a current writer Run, and relayed by no other open
// question. Returns id when it may, "" when not; handed when the writer's
// question is the person's own now (the hand-over sweep gave it to them),
// so asking again would ask them twice.
func stillRelays(ctx context.Context, tx pgx.Tx, ref RunRef, id string) (relays string, handed bool, err error) {
	var writer, prompt string
	var items []QuestionItem
	err = tx.QueryRow(ctx, openWriterQuestion, id, ref.TaskID, ref.Org).Scan(&writer, &prompt, &items)
	if db.IsNotFound(err) {
		err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM questions WHERE id = $1 AND task_id = $2
			AND status = 'open' AND addressee = 'person')`, id, ref.TaskID).Scan(&handed)
		return "", handed, err
	}
	if err != nil {
		return "", false, err
	}
	var open bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM questions WHERE relays = $1 AND status = 'open')`, id).Scan(&open); err != nil || open {
		return "", false, err
	}
	return id, false, nil
}

func sameHeads(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// lostRunTmpl is dude's message in a replacement conductor's briefing.
var lostRunTmpl = mustTemplate("briefings/lost-run")

// lostBrief is what briefings/lost-run.md is written from.
type lostBrief struct {
	// Old is the predecessor's Run; Failed when it failed (Try again)
	// rather than lux losing it while it waited.
	Old          string
	Failed       bool
	Error        string
	Name, Prompt string
	Choices      []string
	Reasked      string
	NotReasked   string
	Answered     bool
}

// LostBriefing is the briefing of the replacement conductor ref of a Run
// lux lost while it waited on a person (oldRunID), written as the person's
// answer starts it: the task's briefing as it stands now, with the
// conversation so far, and dude's note that its predecessor was lost and
// that the answer is its next message.
func LostBriefing(ctx context.Context, tx pgx.Tx, ref RunRef, oldRunID string) (string, error) {
	l := lostBrief{Old: oldRunID}
	var err error
	if l.Name, err = askedName(ctx, tx, ref.TaskID, oldRunID); err != nil {
		return "", err
	}
	var items []QuestionItem
	var status string
	err = tx.QueryRow(ctx, `SELECT q.prompt, q.items, q.status::text FROM questions q
		WHERE q.run_id IN ($1, $2) AND q.addressee = 'person' ORDER BY q.asked_at DESC, q.id DESC LIMIT 1`,
		ref.RunID, oldRunID).Scan(&l.Prompt, &items, &status)
	if err != nil && !db.IsNotFound(err) {
		return "", err
	}
	l.Choices, l.Answered = AskOptions(items), status == "answered"
	return briefing(ctx, tx, ref.TaskID, ref.RunID, "", render(lostRunTmpl, l), rebrief)
}
