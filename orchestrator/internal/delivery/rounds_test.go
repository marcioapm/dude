package delivery_test

import (
	"context"
	"errors"
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
	if got := w.str(t, `SELECT COALESCE((SELECT payload->>'fresh' FROM events WHERE run_id = $1 AND event_type = 'run.created'), '')`, id); got != "nothing_to_replay" {
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
	for _, how := range []string{"resume", "replay"} {
		t.Run(how, func(t *testing.T) {
			w := newRoundWorld(t)
			wf := w.delivery(t, "awaitReview", "policy", `["phase.finished"]`)
			w.exec(t, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, kind, category, base_refs,
			continues_run_id, continues_how)
		VALUES ('run_rv2', $1, $2, $3, 1, 'running', 'review', 'reviewer', 'agent', 'correctness', '{"api":"bbb"}', $4, 'resume')`,
				w.org, w.project, w.task, w.first)
			w.exec(t, `UPDATE runs SET continues_how = $1, review_range = '{"of":"since_last_round","base":{"api":"aaa"}}' WHERE id = 'run_rv2'`, how)
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
		})
	}
}

func TestRoundReplacementWaitsForCreatingStep(t *testing.T) {
	w := newRoundWorld(t)
	wf := w.delivery(t, "review", "policy", `[]`)
	w.exec(t, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, category, continues_run_id, continues_how) VALUES ('run_creating', $1, $2, $3, 1, 'paused', 'review', 'reviewer', 'correctness', $4, 'resume')`, w.org, w.project, w.task, w.first)
	w.exec(t, `INSERT INTO events (id, organization_id, event_type, run_id, task_id, actor_type, actor_id, source, payload) VALUES ('ev_busy', $1, 'agent.message', $2, $3, 'agent', $2, 'test', '{"text":"Previous review"}')`, w.org, w.first, w.task)
	for _, status := range []string{"running", "waiting"} {
		w.exec(t, `UPDATE workflow_runs SET status = $2::workflow_run_status WHERE id = $1`, wf, status)
		err := w.app.InOrg(context.Background(), w.org, func(tx pgx.Tx) error {
			_, err := delivery.RestartRunTx(context.Background(), tx, w.org, w.task, delivery.Restart{RunID: "run_creating", ByDude: true, Replays: w.first, Actor: delivery.Writer{ActorType: "system", ActorID: "dude"}})
			return err
		})
		if !errors.Is(err, delivery.ErrStepRunning) {
			t.Fatalf("creating step %s returned %v, want busy", status, err)
		}
	}
	w.exec(t, `UPDATE workflow_runs SET step = 'awaitFix', status = 'waiting' WHERE id = $1`, wf)
	err := w.app.InOrg(context.Background(), w.org, func(tx pgx.Tx) error {
		_, err := delivery.RestartRunTx(context.Background(), tx, w.org, w.task, delivery.Restart{RunID: "run_creating", ByDude: true, Replays: w.first, Actor: delivery.Writer{ActorType: "system", ActorID: "dude"}})
		return err
	})
	if !delivery.IsRefusal(err) {
		t.Fatalf("a delivery that moved on returned %v, want a refusal", err)
	}
	w.exec(t, `UPDATE workflow_runs SET step = 'awaitReview', state = jsonb_set(state, '{pendingRunIds}', '["run_creating"]') WHERE id = $1`, wf)
	var fresh string
	w.in(t, func(tx pgx.Tx) (err error) {
		fresh, err = delivery.RestartRunTx(context.Background(), tx, w.org, w.task, delivery.Restart{RunID: "run_creating", ByDude: true, Replays: w.first, Actor: delivery.Writer{ActorType: "system", ActorID: "dude"}})
		return err
	})
	if got := w.str(t, `SELECT continues_how FROM runs WHERE id = $1`, fresh); got != "replay" {
		t.Fatalf("replacement = %s", got)
	}
}

func TestLargeRoundClearsNarrowRanges(t *testing.T) {
	for _, of := range []string{delivery.RangeSimplify, delivery.RangePullRequestFix} {
		for _, lines := range []int{300, 301} {
			t.Run(fmt.Sprintf("%s/%d", of, lines), func(t *testing.T) {
				w := newRoundWorld(t)
				w.exec(t, `INSERT INTO events (id, organization_id, event_type, run_id, task_id, actor_type, actor_id, source, payload)
					VALUES ('ev_range', $1, 'agent.message', $2, $3, 'agent', $2, 'test', '{"text":"Earlier review"}')`, w.org, w.first, w.task)
				id, err := (&delivery.Store{DB: w.app}).CreatePhaseRun(context.Background(), w.org, delivery.PhaseRun{
					TaskID: w.task, Phase: delivery.PhaseReview, Category: "correctness", Attempt: 1,
					BaseRefs: map[string]string{"api": "bbb"}, ReviewRange: &delivery.ReviewRange{Of: of, Base: map[string]string{"api": "aaa"}},
					Continue: &delivery.Continue{Predecessor: w.first, DeltaLines: lines}})
				if err != nil {
					t.Fatal(err)
				}
				got := w.str(t, `SELECT COALESCE(review_range->>'of', '') || '|' || COALESCE(continues_how, '') FROM runs WHERE id = $1`, id)
				want := of + "|replay"
				if lines > 300 {
					want = "|"
				}
				if got != want {
					t.Fatalf("range|continuation = %q, want %q", got, want)
				}
			})
		}
	}
}

// A kept predecessor is taken over only when it runs on the model the
// project's reviewer runs on now; one whose model changed is let go and
// its conversation replayed to a fresh reviewer.
func TestAKeptReviewerOnAnotherModelIsReplayed(t *testing.T) {
	for _, tc := range []struct{ model, how string }{{"old/model", "replay"}, {"fake/scripted", "resume"}} {
		t.Run(tc.how, func(t *testing.T) {
			w := newRoundWorld(t)
			w.exec(t, `INSERT INTO model_tiers (id, organization_id, name, model) VALUES ('tier_rv_'||$1, $1, 'Reviewing', 'fake/scripted')`, w.org)
			w.exec(t, `UPDATE projects SET agent_models = jsonb_build_object('reviewer', jsonb_build_object('tier', 'tier_rv_'||$2::text)) WHERE id = $1`,
				w.project, w.org)
			w.exec(t, `UPDATE runs SET keep = true, kept_until = now() + interval '1 day', lux_run_id = 'lrun_k',
				model = $2, model_tier = 'Reviewing', harness = 'opencode' WHERE id = $1`, w.first, tc.model)
			// Its last exit's files are collected.
			w.exec(t, `UPDATE runs SET artifacts_due_at = NULL WHERE id = $1`, w.first)
			w.exec(t, `INSERT INTO events (id, organization_id, event_type, run_id, task_id, actor_type, actor_id, source, payload)
				VALUES ('ev_spec', $1, 'agent.message', $2, $3, 'agent', $2, 'test', '{"text":"Earlier review"}')`, w.org, w.first, w.task)
			id := w.next(t, "k1")
			got := w.str(t, `SELECT COALESCE(continues_how, '') || '|' || status || '|' || COALESCE(lux_run_id, '') FROM runs WHERE id = $1`, id)
			kept := w.str(t, `SELECT keep::text || '|' || COALESCE(lux_run_id, '') FROM runs WHERE id = $1`, w.first)
			want, wantKept := "replay|pending|", "false|lrun_k"
			if tc.how == "resume" {
				want, wantKept = "resume|paused|lrun_k", "false|"
			}
			if got != want || kept != wantKept {
				t.Fatalf("new how|status|lux = %q, predecessor keep|lux = %q; want %q, %q", got, kept, want, wantKept)
			}
		})
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

// writerWorld is newRoundWorld with a kept implementer, completed at its
// head bbb from base aaa on a commit (detached), whose lux Run a fix round
// may take over; tier is the project's implementer tier.
func writerWorld(t *testing.T) *roundWorld {
	w := newRoundWorld(t)
	w.exec(t, `INSERT INTO model_tiers (id, organization_id, name, model) VALUES ('tier_im_'||$1, $1, 'Building', 'fake/scripted')`, w.org)
	w.exec(t, `UPDATE projects SET agent_models = jsonb_build_object('implementer', jsonb_build_object('tier', 'tier_im_'||$2::text)) WHERE id = $1`,
		w.project, w.org)
	w.exec(t, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, kind, base_refs, heads,
			ended_at, keep, kept_until, lux_run_id, model, model_tier, harness)
		VALUES ('run_im', $1, $2, $3, 1, 'completed', 'implement', 'implementer', 'agent', '{"api":"aaa"}',
			'{"api":{"sha":"bbb"}}', now() - interval '1 hour', true, now() + interval '1 day', 'lrun_im', 'fake/scripted', 'Building', 'opencode')`,
		w.org, w.project, w.task)
	return w
}

// fix creates a fix round going on from the kept implementer, from bbb.
func (w *roundWorld) fix(t *testing.T, key string) string {
	t.Helper()
	id, err := (&delivery.Store{DB: w.app}).CreatePhaseRun(context.Background(), w.org, delivery.PhaseRun{
		TaskID: w.task, Phase: delivery.PhaseFix, Key: key, Attempt: 1,
		BaseRefs: map[string]string{"api": "bbb"}, Continue: &delivery.Continue{Predecessor: "run_im"}})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// A resumed fixer is told what a fresh one's prompt would carry and its
// conversation lacks: the task as edited after its last round ended, and
// what people decided since. Nothing from before its round ended.
func TestAResumedFixerIsToldTheTaskEditedAndDecidedSinceItsRound(t *testing.T) {
	w := writerWorld(t)
	w.exec(t, `UPDATE tasks SET acceptance_criteria = '["amounts are in cents"]' WHERE id = $1`, w.task)
	w.exec(t, `INSERT INTO events (id, organization_id, event_type, task_id, actor_type, actor_id, source, payload, occurred_at)
		VALUES ('ev_old_edit', $1, 'task.updated', $2, 'human', 'p', 'test', '{"goal":"x"}', now() - interval '2 hours'),
		       ('ev_edit', $1, 'task.updated', $2, 'human', 'p', 'test', '{"acceptanceCriteria":["amounts are in cents"]}', now())`, w.org, w.task)
	w.exec(t, `INSERT INTO questions (id, organization_id, task_id, prompt, items, status, answer, answered_at)
		VALUES ('q_old', $1, $2, 'Old question?', '[{"question":"Old question?"}]', 'answered', 'old answer', now() - interval '2 hours'),
		       ('q_new', $1, $2, 'Floats or cents?', '[{"question":"Floats or cents?"}]', 'answered', 'use cents, not floats', now())`, w.org, w.task)
	id := w.fix(t, "f1")
	if got := w.str(t, `SELECT COALESCE(continues_how, '') FROM runs WHERE id = $1`, id); got != delivery.ContinuesResume {
		t.Fatalf("the fix %s, want the implementer resumed", got)
	}
	text := w.str(t, `SELECT text FROM directives WHERE run_id = $1`, id)
	for _, want := range []string{"the task was changed", "- amounts are in cents", "Floats or cents?", "use cents, not floats"} {
		if !strings.Contains(text, want) {
			t.Errorf("the round message lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "Old question?") {
		t.Errorf("the round message repeats a decision from before its round:\n%s", text)
	}
}

// A fix goes on from the attempt's last writer: its implementer, until a
// fixer has run, then that fixer; never a simplifier. Its round started
// where the writer left its checkouts: its published head.
func TestAFixGoesOnFromTheLastFixerElseTheImplementer(t *testing.T) {
	w := newRoundWorld(t)
	store := &delivery.Store{DB: w.app}
	ctx := context.Background()
	prev := func() string {
		t.Helper()
		p, err := store.PreviousRound(ctx, w.org, w.task, 1, delivery.PhaseFix, "")
		if err != nil {
			t.Fatal(err)
		}
		if p == nil {
			return "none"
		}
		return p.ID + "@" + p.BaseRefs["api"]
	}
	insert := func(id, phase, role, ended string) {
		t.Helper()
		w.exec(t, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, kind, base_refs, heads, ended_at)
			VALUES ($1, $2, $3, $4, 1, 'completed', $5::run_phase, $6::agent_role, 'agent', '{"api":"base"}',
				jsonb_build_object('api', jsonb_build_object('sha', 'head_'||$1)), now() - $7::interval)`,
			id, w.org, w.project, w.task, phase, role, ended)
	}
	if got := prev(); got != "none" {
		t.Fatalf("before any writer: %s", got)
	}
	insert("run_im", "implement", "implementer", "3 hours")
	if got := prev(); got != "run_im@head_run_im" {
		t.Fatalf("after the implementer: %s, want it at its head", got)
	}
	insert("run_sp", "simplify", "implementer", "2 hours")
	if got := prev(); got != "run_im@head_run_im" {
		t.Fatalf("after a simplifier: %s, want the implementer", got)
	}
	insert("run_fx", "fix", "implementer", "1 hour")
	if got := prev(); got != "run_fx@head_run_fx" {
		t.Fatalf("after a fixer: %s, want the fixer", got)
	}
	// A reviewer's round still goes on from its reviewer only.
	p, err := store.PreviousRound(ctx, w.org, w.task, 1, delivery.PhaseReview, "correctness")
	if err != nil || p == nil || p.ID != w.first || p.BaseRefs["api"] != "aaa" {
		t.Fatalf("a review's predecessor: %+v %v", p, err)
	}
}
