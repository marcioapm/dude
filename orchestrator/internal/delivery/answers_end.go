package delivery

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// A delivery that ends — its pull requests merged or closed
// (pullRequestEnded, endLoop's included), or stopped (StopAllTx: a
// person's Stop, Close, Archive, done without a pull request) — reaches no
// later step to post its fixers' held answers. Under the task's Chat lock,
// as posting is, from dude's records alone (no GitHub call):
//
//   - it records the end (EvAnswersEnded on the task): no held answer made
//     before it is claimed again (claimAnswerTx reads it under the same
//     lock), so nothing starts a GitHub write after the end;
//   - it withholds every unsettled held answer nobody holds a claim on,
//     saying why;
//   - it leaves alone an answer a poster ever claimed (a claim still held,
//     or one released after a transient failure, whose write GitHub may
//     still have taken): the poster may be writing it to GitHub right now,
//     and a withheld answer GitHub then shows would be a lie. It is put in
//     answer_reconciliations instead
//     (migration 271): its poster records what happened (posted, failed:
//     settleReconciliationTx, in the record's transaction), and a poster
//     that never does — crashed, or gone past its claim — is settled by the
//     pr-reconciler's pass once the claim has long expired
//     (AnswerReconciler): dude's own marked comment, or the thread's state,
//     found ⇒ posted; conclusively absent ⇒ withheld; anything else ⇒
//     uncertain ("may have been posted; check the pull request"), settled
//     and told once.
//
// A live conductor is woken with what was withheld and what is still in
// flight, and later with what each in-flight answer ended as.

// Reasons a held answer is withheld when its delivery ends.
const (
	withheldMerged  = "the pull request was merged"
	withheldClosed  = "the pull request was closed"
	withheldStopped = "the delivery was stopped"
	// A held answer the delivery's end found unclaimed (released, or never
	// claimed) after its claim's write could not be confirmed.
	withheldBeforePosted = "the delivery ended before it was posted"
	// An answer whose write may or may not have reached GitHub.
	uncertainAnswer = "it may have been posted; check the pull request"
)

// EvAnswersEnded is a delivery's end for its fixers' held answers, on the
// task: no held answer recorded before it is claimed after it. Payload:
// { why }.
const EvAnswersEnded = "pull_request.answers_ended"

// endedAfterTx is why the task's delivery ended after held answer id was
// made ("" if it has not): the newest EvAnswersEnded with a later cursor.
// Its type is a literal: events_task_answers_ended_idx (migration 272).
func endedAfterTx(ctx context.Context, tx pgx.Tx, taskID, id string) (string, error) {
	var why string
	err := tx.QueryRow(ctx, `SELECT COALESCE(m.payload->>'why', '') FROM events m
		WHERE m.task_id = $1 AND m.event_type = '`+EvAnswersEnded+`'
		  AND m.cursor > (SELECT h.cursor FROM events h WHERE h.id = $2)
		ORDER BY m.cursor DESC LIMIT 1`, taskID, id).Scan(&why)
	if err == pgx.ErrNoRows {
		return "", nil
	}
	return why, err
}

// heldLabel is what a held answer answers, as a wake names it.
func heldLabel(h heldOf) string {
	if h.Kind == answerResolve {
		return "resolving " + h.Thread
	}
	return h.InReplyTo
}

// settleHeldOnEndTx ends the task's fixers' held answers as the comment
// above says: why, or, when states is given, by the state of the pull
// request the answer is on (merged: withheldMerged, else withheldClosed).
// A live conductor is woken, once per fixer. It takes the Chat lock
// (re-entrant: a caller holding it already is fine).
func settleHeldOnEndTx(ctx context.Context, tx pgx.Tx, org, taskID, why string, states []PullRequestState) ([]FixerAnswers, error) {
	if err := LockChat(ctx, tx, taskID); err != nil {
		return nil, err
	}
	marker := why
	if states != nil {
		marker = withheldClosed
		for _, p := range states {
			if p.State == forge.StateMerged {
				marker = withheldMerged
			}
		}
	}
	// The end, recorded unless one already stands after every held answer
	// (a step run again): no claim after it.
	var project string
	var stands bool
	if err := tx.QueryRow(ctx, `SELECT t.project_id, EXISTS (SELECT 1 FROM events m WHERE m.task_id = t.id AND m.event_type = '`+EvAnswersEnded+`'
			AND m.cursor > COALESCE((SELECT max(h.cursor) FROM runs r JOIN events h ON h.run_id = r.id AND `+isHeld("h")+`
				WHERE r.task_id = t.id AND r.phase = 'fix'), 0))
		FROM tasks t WHERE t.id = $1`, taskID).Scan(&project, &stands); err != nil {
		return nil, err
	}
	if !stands {
		if _, err := ledger.Append(ctx, tx, ledger.Event{Type: EvAnswersEnded, OrganizationID: org, ProjectID: project, TaskID: taskID,
			ActorType: ledger.ActorSystem, ActorID: "dude", Source: ledger.SourceOrchestrator, CorrelationID: taskID,
			Payload: map[string]any{"why": marker}}); err != nil {
			return nil, err
		}
	}
	rows, err := tx.Query(ctx, `SELECT h.run_id, r.project_id, h.id, h.payload,
			COALESCE((SELECT jsonb_object_agg(c.repo, c.head) FROM (SELECT DISTINCT ON (e.payload->>'repo') e.payload->>'repo' AS repo,
				e.payload->>'headSha' AS head FROM events e WHERE e.run_id = h.run_id AND `+isCommitCreated("e")+`
				ORDER BY e.payload->>'repo', e.cursor DESC) c), '{}'::jsonb),
			c.occurred_at
		FROM runs r JOIN events h ON h.run_id = r.id AND `+isHeld("h")+`
		LEFT JOIN LATERAL (SELECT c.occurred_at FROM events c
			WHERE c.run_id = h.run_id AND c.event_type = '`+EvAnswerClaimed+`' AND `+settledAnswerID("c")+` = h.id
			ORDER BY c.cursor DESC LIMIT 1) c ON true
		WHERE r.task_id = $1 AND r.phase = 'fix' AND NOT `+answerSettled("h")+`
		ORDER BY r.created_at, r.id, h.cursor`, taskID)
	if err != nil {
		return nil, err
	}
	type unsettled struct {
		ref     RunRef
		h       heldOf
		commits map[string]string
		claimed *time.Time
	}
	var all []unsettled
	for rows.Next() {
		var u unsettled
		var raw []byte
		u.ref = RunRef{Org: org, TaskID: taskID}
		if err := rows.Scan(&u.ref.RunID, &u.ref.ProjectID, &u.h.ID, &raw, &u.commits, &u.claimed); err != nil {
			rows.Close()
			return nil, err
		}
		if err := json.Unmarshal(raw, &u.h.heldAnswer); err != nil {
			rows.Close()
			return nil, err
		}
		all = append(all, u)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []FixerAnswers
	for _, u := range all {
		reason := why
		if states != nil {
			reason = withheldClosed
			for _, p := range states {
				if p.Repo == u.h.Repo && p.Number == u.h.Number && p.State == forge.StateMerged {
					reason = withheldMerged
				}
			}
		}
		n := len(out)
		if n == 0 || out[n-1].RunID != u.ref.RunID || out[n-1].Ended != reason {
			out = append(out, FixerAnswers{RunID: u.ref.RunID, Commits: u.commits, Cause: CauseEnded, Ended: reason})
			n++
		}
		f := &out[n-1]
		f.ids = append(f.ids, u.h.ID)
		if u.claimed != nil {
			// A poster holds it: never withheld here. Its poster, or the
			// pass once its claim has long expired, settles it.
			if _, err := tx.Exec(ctx, `INSERT INTO answer_reconciliations (answer_id, organization_id, task_id, run_id, ended, label, due_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7) ON CONFLICT (answer_id) DO NOTHING`,
				u.h.ID, org, taskID, u.ref.RunID, reason, clip(heldLabel(u.h), 300), u.claimed.Add(answerReconcileAfter)); err != nil {
				return nil, err
			}
			f.InFlight = append(f.InFlight, heldLabel(u.h))
			continue
		}
		if err := failAnswerTx(ctx, tx, u.ref, u.h, reason, true); err != nil {
			return nil, err
		}
		f.Withheld, f.WithheldList = reason, append(f.WithheldList, heldLabel(u.h))
	}
	for _, f := range out {
		if err := wakeAnswersTx(ctx, tx, org, taskID, f); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// answerReconcileAfter is how long after a claim the pass reads GitHub for
// an in-flight answer of an ended delivery: twice the claim's lease, past
// which no poster still writes (one checks its lease before writing, and
// GitHub's request times out well inside it).
const answerReconcileAfter = 2 * answerClaimLease

// SettleHeldOnStopTx settles the task's held answers for a stopped
// delivery (settleHeldOnEndTx); StopAllTx calls it.
func SettleHeldOnStopTx(ctx context.Context, tx pgx.Tx, org, taskID string) error {
	_, err := settleHeldOnEndTx(ctx, tx, org, taskID, withheldStopped, nil)
	return err
}

// settleHeldOnEnd settles the task's held answers once none of its pull
// requests is open (states), each by its own pull request's end.
func (w *steps) settleHeldOnEnd(ctx context.Context, org, taskID string, states []PullRequestState) error {
	if len(states) == 0 || countStates(states).open > 0 {
		return nil
	}
	return w.s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		_, err := settleHeldOnEndTx(ctx, tx, org, taskID, "", states)
		return err
	})
}
