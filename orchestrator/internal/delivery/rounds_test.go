package delivery_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// roundWorld is a task with a completed first-round reviewer, its review
// of the base commit, for a next round to go on from.
type roundWorld struct {
	*feedbackWorld
	first string
}

func newRoundWorld(t *testing.T) *roundWorld {
	w := &roundWorld{feedbackWorld: newFeedbackWorld(t), first: "run_rv1"}
	w.exec(t, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, kind, category, base_refs,
			ended_at, keep, kept_until)
		VALUES ($1, $2, $3, $4, 1, 'completed', 'review', 'reviewer', 'agent', 'correctness', '{"api":"aaa"}', now(), false, NULL)`,
		w.first, w.org, w.project, w.task)
	return w
}

func (w *roundWorld) next(t *testing.T, key string) string {
	t.Helper()
	id, err := (&delivery.Store{DB: w.app}).CreatePhaseRun(context.Background(), w.org, delivery.PhaseRun{
		TaskID: w.task, Phase: delivery.PhaseReview, Category: "correctness", Key: key, Attempt: 1,
		BaseRefs: map[string]string{"api": "bbb"}, Continue: &delivery.Continue{Predecessor: w.first}})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func (w *roundWorld) str(t *testing.T, sql string, args ...any) string {
	t.Helper()
	var s string
	if err := w.owner.QueryRow(context.Background(), sql, args...).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

// A next round whose predecessor cannot be taken over (here: not kept)
// and said nothing worth replaying reviews as a fresh Run does: the whole
// branch, no conversation.
func TestAFreshRoundWithNothingToReplayReviewsTheWholeBranch(t *testing.T) {
	w := newRoundWorld(t)
	id := w.next(t, "k1")
	if got := w.str(t, `SELECT COALESCE(continues_run_id, '') || '|' || COALESCE(review_range::text, '') FROM runs WHERE id = $1`, id); got != "|" {
		t.Fatalf("continues|range = %q, want neither", got)
	}
	if got := w.str(t, `SELECT payload->>'fresh' FROM events WHERE run_id = $1 AND event_type = 'run.created'`, id); got != "nothing_to_replay" {
		t.Errorf("run.created fresh = %q", got)
	}
}

// A next round whose predecessor cannot be taken over but said something
// is a fresh agent given that conversation — the predecessor's and the
// rounds it continued — reviewing the commits since the predecessor's
// round.
func TestAFreshRoundReplaysItsPredecessorsChainAndReviewsSinceItsRound(t *testing.T) {
	w := newRoundWorld(t)
	w.exec(t, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, kind, category, base_refs, ended_at)
		VALUES ('run_rv0', $1, $2, $3, 1, 'aborted', 'review', 'reviewer', 'agent', 'correctness', '{}', now())`, w.org, w.project, w.task)
	w.exec(t, `UPDATE runs SET continues_run_id = 'run_rv0', continues_how = 'resume' WHERE id = $1`, w.first)
	for i, ev := range [][2]string{{"run_rv0", "EARLIEST ROUND"}, {w.first, "LATEST ROUND"}, {"run_other", "NOT IN THE CHAIN"}} {
		w.exec(t, `INSERT INTO events (id, organization_id, event_type, run_id, task_id, actor_type, actor_id, source, payload)
			VALUES ('ev_rd'||$1::text, $2, 'agent.message', $3, $4, 'agent', $3, 'test', jsonb_build_object('text', $5::text))`,
			fmt.Sprint(i), w.org, ev[0], w.task, ev[1])
	}
	id := w.next(t, "k1")
	if got := w.str(t, `SELECT continues_run_id || '|' || continues_how || '|' || (review_range->>'of') || '|' || (review_range->'base'->>'api')
		FROM runs WHERE id = $1`, id); got != w.first+"|replay|"+delivery.RangeSinceLastRound+"|aaa" {
		t.Fatalf("continues|how|range of|base = %q", got)
	}
	var replay string
	w.in(t, func(tx pgx.Tx) (err error) {
		replay, err = delivery.Replay(context.Background(), tx, delivery.Talker{RunID: w.first})
		return err
	})
	if i, j := strings.Index(replay, "EARLIEST ROUND"), strings.Index(replay, "LATEST ROUND"); i < 0 || j < i || strings.Contains(replay, "NOT IN THE CHAIN") {
		t.Errorf("the chain's replay:\n%s", replay)
	}
	if n := w.count(t, `SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.continues' AND payload->>'how' = 'replay'`, id); n != 1 {
		t.Error("no run.continues for the replay")
	}
	// Created again by its key: the same Run, nothing new.
	if again := w.next(t, "k1"); again != id {
		t.Errorf("the step again made %s, want %s", again, id)
	}
}

// A person's or the conductor's restart of a round that went on from its
// predecessor is a fresh Run, as any restart: it continues nothing and is
// given no conversation.
func TestARestartOfAContinuedRoundIsFresh(t *testing.T) {
	w := newRoundWorld(t)
	wf := w.delivery(t, "awaitReview", "policy", `["phase.finished"]`)
	w.exec(t, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, kind, category, base_refs,
			continues_run_id, continues_how)
		VALUES ('run_rv2', $1, $2, $3, 1, 'running', 'review', 'reviewer', 'agent', 'correctness', '{"api":"bbb"}', $4, 'resume')`,
		w.org, w.project, w.task, w.first)
	w.exec(t, `INSERT INTO events (id, organization_id, event_type, run_id, task_id, actor_type, actor_id, source, payload)
		VALUES ('ev_rs1', $1, 'agent.message', $2, $3, 'agent', $2, 'test', '{"text":"SAID BEFORE"}')`, w.org, w.first, w.task)
	w.exec(t, `UPDATE workflow_runs SET state = jsonb_set(state, '{pendingRunIds}', '["run_rv2"]') WHERE id = $1`, wf)
	var fresh string
	w.in(t, func(tx pgx.Tx) (err error) {
		fresh, err = delivery.RestartRunTx(context.Background(), tx, w.org, w.task, delivery.Restart{RunID: "run_rv2", Note: "stuck",
			Actor: delivery.Writer{ActorType: "human", ActorID: w.ana}})
		return err
	})
	if got := w.str(t, `SELECT COALESCE(continues_run_id, '') || '|' || COALESCE(review_range::text, '') FROM runs WHERE id = $1`, fresh); got != "|" {
		t.Errorf("the restarted Run continues|range %q, want a fresh Run", got)
	}
}

// A kept predecessor is let go when the task ends, and by a new attempt
// or a decision past a stop (ReleaseKeptTx): RetireCompleted takes it up.
func TestAKeptReviewerIsLetGoWhenItsTaskEnds(t *testing.T) {
	for _, how := range []string{"ended", "released"} {
		t.Run(how, func(t *testing.T) {
			w := newRoundWorld(t)
			w.exec(t, `UPDATE runs SET keep = true, kept_until = now() + interval '1 day', lux_run_id = 'lrun_kept' WHERE id = $1`, w.first)
			w.in(t, func(tx pgx.Tx) error {
				if how == "released" {
					return delivery.ReleaseKeptTx(context.Background(), tx, w.task)
				}
				_, err := delivery.SetTaskStatusTx(context.Background(), tx, w.org, w.project, w.task, "", "done", "merged")
				return err
			})
			if n := w.count(t, `SELECT count(*) FROM runs WHERE id = $1 AND NOT keep AND kept_until <= now()`, w.first); n != 1 {
				t.Fatal("the kept reviewer was not let go")
			}
		})
	}
}
