package delivery

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

// A delivery that ends — its pull requests merged or closed
// (pullRequestEnded, endLoop's included), or stopped (StopAllTx: a
// person's Stop, Close, Archive, done without a pull request) — reaches no
// later step to post its fixers' held answers. They are settled there, as
// withheld, from dude's records alone (no GitHub call), under the task's
// Chat lock as posting is, and a live conductor is woken: every held
// answer is settled, by one record, on every path.

// Reasons a held answer is withheld when its delivery ends.
const (
	withheldMerged  = "the pull request was merged"
	withheldClosed  = "the pull request was closed"
	withheldStopped = "the delivery was stopped"
)

// settleHeldOnEndTx settles every unsettled held answer of the task's fix
// Runs as withheld: why, or, when states is given, by the state of the
// pull request the answer is on (merged: withheldMerged, else
// withheldClosed). A live conductor is woken, once per fixer. It takes the
// Chat lock (re-entrant: a caller holding it already is fine).
func settleHeldOnEndTx(ctx context.Context, tx pgx.Tx, org, taskID, why string, states []PullRequestState) ([]FixerAnswers, error) {
	if err := LockChat(ctx, tx, taskID); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT h.run_id, r.project_id, h.id, h.payload,
			COALESCE((SELECT jsonb_object_agg(c.repo, c.head) FROM (SELECT DISTINCT ON (e.payload->>'repo') e.payload->>'repo' AS repo,
				e.payload->>'headSha' AS head FROM events e WHERE e.run_id = h.run_id AND e.event_type = $3
				ORDER BY e.payload->>'repo', e.cursor DESC) c), '{}'::jsonb)
		FROM runs r JOIN events h ON h.run_id = r.id AND h.event_type = $2
		WHERE r.task_id = $1 AND r.phase = 'fix' AND NOT `+answerSettled("h")+`
		ORDER BY r.created_at, r.id, h.cursor`, taskID, EvAnswerHeld, EvGitCommitCreated)
	if err != nil {
		return nil, err
	}
	type unsettled struct {
		ref     RunRef
		h       heldOf
		commits map[string]string
	}
	var all []unsettled
	for rows.Next() {
		var u unsettled
		var raw []byte
		u.ref = RunRef{Org: org, TaskID: taskID}
		if err := rows.Scan(&u.ref.RunID, &u.ref.ProjectID, &u.h.ID, &raw, &u.commits); err != nil {
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
		if err := failAnswerTx(ctx, tx, u.ref, u.h, reason, true); err != nil {
			return nil, err
		}
		if n := len(out); n > 0 && out[n-1].RunID == u.ref.RunID && out[n-1].Withheld == reason {
			out[n-1].ids = append(out[n-1].ids, u.h.ID)
			continue
		}
		out = append(out, FixerAnswers{RunID: u.ref.RunID, Commits: u.commits, Withheld: reason, ids: []string{u.h.ID}})
	}
	for _, f := range out {
		if err := wakeAnswersTx(ctx, tx, org, taskID, f); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// SettleHeldOnStopTx settles the task's held answers as withheld for a
// stopped delivery (settleHeldOnEndTx); StopAllTx calls it.
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
