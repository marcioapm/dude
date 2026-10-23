package delivery_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// The time used is read with JIT off, which past a few thousand parks in
// an organisation costs ~60 ms of a ~1.5 ms query while the predecessor is
// locked; the caller's transaction gets its setting back.
func TestTheTimeUsedIsReadWithoutJIT(t *testing.T) {
	w := testerRound.world(t, 2*time.Hour+6*time.Minute)
	var during string
	delivery.BeforeTimeSpentQuery(func(ctx context.Context, tx pgx.Tx) {
		if err := tx.QueryRow(ctx, `SELECT current_setting('jit')`).Scan(&during); err != nil {
			t.Error(err)
		}
	})
	t.Cleanup(func() { delivery.BeforeTimeSpentQuery(nil) })
	var before, after, id string
	w.in(t, func(tx pgx.Tx) (err error) {
		ctx := context.Background()
		if err := tx.QueryRow(ctx, `SELECT set_config('jit', 'on', true)`).Scan(&before); err != nil {
			return err
		}
		if id, err = delivery.CreatePhaseRunTx(ctx, tx, w.org, delivery.PhaseRun{TaskID: w.task, Phase: delivery.PhaseTest, Attempt: 1,
			BaseRefs: map[string]string{"api": "bbb"}, Continue: &delivery.Continue{Predecessor: "run_b"}}, 4*time.Hour); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT current_setting('jit')`).Scan(&after)
	})
	if got := w.how(t, id); got != timeReplayed {
		t.Fatalf("how|lux|fresh = %q, want %q", got, timeReplayed)
	}
	if before != "on" || during != "off" || after != "on" {
		t.Errorf("jit %q before, %q during the query, %q after; want on, off, on", before, during, after)
	}
}

// budgetRound is a phase whose next round may take its predecessor's lux
// Run over: its agent role (also its settings role) and category.
type budgetRound struct {
	phase, role, category string
}

var (
	testerRound   = budgetRound{delivery.PhaseTest, "qa_browser", ""}
	reviewerRound = budgetRound{delivery.PhaseReview, "reviewer", "correctness"}
)

// world is a task whose last round, run_b, is kept and on the model its
// role runs on now (lux Run lrun_b), its round having run for used, with
// something said to replay.
func (b budgetRound) world(t *testing.T, used time.Duration) *roundWorld {
	t.Helper()
	w := newRoundWorld(t)
	w.exec(t, `INSERT INTO model_tiers (id, organization_id, name, model) VALUES ('tier_b_'||$1, $1, 'Checking', 'fake/scripted')`, w.org)
	w.exec(t, `UPDATE projects SET agent_models = jsonb_build_object($2::text, jsonb_build_object('tier', 'tier_b_'||$3::text)) WHERE id = $1`,
		w.project, b.role, w.org)
	w.exec(t, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, kind, category, base_refs,
			started_at, ended_at, keep, kept_until, lux_run_id, model, model_tier, harness)
		VALUES ('run_b', $1, $2, $3, 1, 'completed', $4::run_phase, $5::agent_role, 'agent', NULLIF($6, ''), '{"api":"aaa"}',
			now() - interval '10 minutes' - make_interval(secs => $7), now() - interval '10 minutes', true, now() + interval '1 day',
			'lrun_b', 'fake/scripted', 'Checking', 'opencode')`,
		w.org, w.project, w.task, b.phase, b.role, b.category, used.Seconds())
	w.exec(t, `INSERT INTO events (id, organization_id, event_type, run_id, task_id, actor_type, actor_id, source, payload)
		VALUES ('ev_b', $1, 'agent.message', 'run_b', $2, 'agent', 'run_b', 'test', '{"text":"Earlier round"}')`, w.org, w.task)
	return w
}

// next creates the round after run_b through the store, as the workflow's
// steps do, on a deployment whose phase Runs get limit.
func (b budgetRound) next(t *testing.T, w *roundWorld, limit time.Duration) string {
	t.Helper()
	id, err := (&delivery.Store{DB: w.app, RunTimeLimit: limit}).CreatePhaseRun(context.Background(), w.org, delivery.PhaseRun{
		TaskID: w.task, Phase: b.phase, Category: b.category, Key: "next", Attempt: 1,
		BaseRefs: map[string]string{"api": "bbb"}, Continue: &delivery.Continue{Predecessor: "run_b"}})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// how is the new round's continuation, lux Run and run.created's fresh.
func (w *roundWorld) how(t *testing.T, id string) string {
	t.Helper()
	return w.str(t, `SELECT COALESCE(continues_how, '') || '|' || COALESCE(lux_run_id, '') || '|' ||
		COALESCE((SELECT payload->>'fresh' FROM events WHERE run_id = r.id AND event_type = 'run.created'), '')
		FROM runs r WHERE id = $1`, id)
}

const (
	resumed      = "resume|lrun_b|"
	timeReplayed = "replay||time_budget"
)

// lux times a Run's running time over all its placements, and a resume
// cannot give it more: a tester's or reviewer's next round takes its
// predecessor's lux Run over only while at least half the limit is left;
// with less it is a fresh Run, with the full limit, given the
// predecessor's conversation, and the run.created says why.
func TestARoundIsNotHandedALuxRunWithLittleTimeLeft(t *testing.T) {
	for name, b := range map[string]budgetRound{"tester": testerRound, "reviewer": reviewerRound} {
		for _, tc := range []struct {
			used, limit time.Duration
			want        string
		}{
			{2*time.Hour + 6*time.Minute, 4 * time.Hour, timeReplayed},
			{90 * time.Minute, 4 * time.Hour, resumed},
			// Exactly half left is enough.
			{2 * time.Hour, 4 * time.Hour, resumed},
			// The configured limit, not the default: 90m of 4h is little
			// used, of 2h most of it.
			{90 * time.Minute, 2 * time.Hour, timeReplayed},
			// No limit known: nothing is spent.
			{5 * time.Hour, 0, resumed},
		} {
			t.Run(name+"/"+tc.used.String()+"-of-"+tc.limit.String(), func(t *testing.T) {
				w := b.world(t, tc.used)
				id := b.next(t, w, tc.limit)
				if got := w.how(t, id); got != tc.want {
					t.Fatalf("how|lux|fresh = %q, want %q", got, tc.want)
				}
				if tc.want == resumed {
					return
				}
				// Let go, for RetireCompleted to end its lux Run; the replay
				// continues it.
				if got := w.str(t, `SELECT keep::text || '|' || COALESCE(lux_run_id, '') FROM runs WHERE id = 'run_b'`); got != "false|lrun_b" {
					t.Errorf("predecessor keep|lux = %q, want released", got)
				}
				if got := w.str(t, `SELECT continues_run_id FROM runs WHERE id = $1`, id); got != "run_b" {
					t.Errorf("the fresh round continues %q, want run_b", got)
				}
			})
		}
	}
}

// What a lux Run has used is every round's that held it: the predecessor
// and each row it resumed, back to the row it was submitted for. A row
// before a replay held another lux Run and counts for nothing.
func TestTheTimeUsedIsTheWholeResumeChains(t *testing.T) {
	for _, b := range []budgetRound{testerRound, reviewerRound} {
		t.Run(b.phase, func(t *testing.T) {
			// Three rounds of 50 minutes each on one lux Run: each under
			// two hours, 2h30m together.
			w := b.world(t, 50*time.Minute)
			b.before(t, w, chainRow{"run_b1", 50 * time.Minute, ""}, chainRow{"run_b2", 50 * time.Minute, "resume"})
			if got := w.how(t, b.next(t, w, 4*time.Hour)); got != timeReplayed {
				t.Fatalf("how|lux|fresh = %q, want %q", got, timeReplayed)
			}
			// The same two rounds on this lux Run, after a replay of a round
			// that ran 2h10m on another: 1h40m used, 2h20m left.
			w = b.world(t, 50*time.Minute)
			b.before(t, w, chainRow{"run_b0", 2*time.Hour + 10*time.Minute, ""}, chainRow{"run_b1", 50 * time.Minute, "replay"})
			if got := w.how(t, b.next(t, w, 4*time.Hour)); got != resumed {
				t.Fatalf("after a replay: how|lux|fresh = %q, want %q", got, resumed)
			}
		})
	}
}

// A round of the chain ran 3h, on a lux Run an earlier replay left: its
// next round may run as long, so 2h20m left (over half of 4h) is not
// enough, and the round is replayed.
func TestARoundNeedsTheChainsLongestRoundLeft(t *testing.T) {
	for _, b := range []budgetRound{testerRound, reviewerRound} {
		t.Run(b.phase, func(t *testing.T) {
			w := b.world(t, 50*time.Minute)
			b.before(t, w, chainRow{"run_b0", 3 * time.Hour, ""}, chainRow{"run_b1", 50 * time.Minute, "replay"})
			if got := w.how(t, b.next(t, w, 4*time.Hour)); got != timeReplayed {
				t.Fatalf("how|lux|fresh = %q, want %q", got, timeReplayed)
			}
		})
	}
}

// A spent predecessor whose last exit's files are still being collected is
// not waited for: it would not be taken over once they are.
func TestASpentRoundStillCollectingIsReplayedAtOnce(t *testing.T) {
	w := testerRound.world(t, 2*time.Hour+6*time.Minute)
	w.exec(t, `UPDATE runs SET artifacts_due_at = now() WHERE id = 'run_b'`)
	if got := w.how(t, testerRound.next(t, w, 4*time.Hour)); got != timeReplayed {
		t.Fatalf("how|lux|fresh = %q, want %q", got, timeReplayed)
	}
}

// A fixer going on from the implementer takes its lux Run over by the same
// rule: with less than half the limit left it is a fresh writer, given the
// implementer's conversation.
func TestAFixerIsNotHandedAnImplementersLuxRunWithLittleTimeLeft(t *testing.T) {
	for _, tc := range []struct {
		used time.Duration
		want string
	}{{2*time.Hour + 6*time.Minute, "replay||time_budget"}, {90 * time.Minute, "resume|lrun_im|"}} {
		t.Run(tc.used.String(), func(t *testing.T) {
			w := writerWorld(t)
			w.exec(t, `UPDATE runs SET started_at = ended_at - make_interval(secs => $1) WHERE id = 'run_im'`, tc.used.Seconds())
			w.exec(t, `INSERT INTO events (id, organization_id, event_type, run_id, task_id, actor_type, actor_id, source, payload)
				VALUES ('ev_im', $1, 'agent.message', 'run_im', $2, 'agent', 'run_im', 'test', '{"text":"Implemented it"}')`, w.org, w.task)
			id, err := (&delivery.Store{DB: w.app, RunTimeLimit: 4 * time.Hour}).CreatePhaseRun(context.Background(), w.org, delivery.PhaseRun{
				TaskID: w.task, Phase: delivery.PhaseFix, Key: "f1", Attempt: 1,
				BaseRefs: map[string]string{"api": "bbb"}, Continue: &delivery.Continue{Predecessor: "run_im"}})
			if err != nil {
				t.Fatal(err)
			}
			if got := w.how(t, id); got != tc.want {
				t.Fatalf("how|lux|fresh = %q, want %q", got, tc.want)
			}
		})
	}
}

// chainRow is an earlier round of the same phase: its id, how long it ran
// and how it continues the row before it ("" for none).
type chainRow struct {
	id  string
	ran time.Duration
	how string
}

// before inserts rows, oldest first, each continuing the one before it as
// its how says, and has run_b resume the last.
func (b budgetRound) before(t *testing.T, w *roundWorld, rows ...chainRow) {
	t.Helper()
	prev := ""
	for i, r := range rows {
		ended := time.Duration(len(rows)-i) * 6 * time.Hour
		w.exec(t, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, kind, category,
				base_refs, started_at, ended_at, continues_run_id, continues_how)
			VALUES ($1, $2, $3, $4, 1, 'completed', $5::run_phase, $6::agent_role, 'agent', NULLIF($7, ''), '{}',
				now() - make_interval(secs => $8::float8 + $9::float8), now() - make_interval(secs => $8::float8), NULLIF($10, ''), NULLIF($11, ''))`,
			r.id, w.org, w.project, w.task, b.phase, b.role, b.category, ended.Seconds(), r.ran.Seconds(), prev, r.how)
		prev = r.id
	}
	w.exec(t, `UPDATE runs SET continues_run_id = $1, continues_how = 'resume' WHERE id = 'run_b'`, prev)
}
