package orchestrator_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakeagent"
)

// fixRow is one implement or fix Run as the test reads it.
type fixRow struct {
	ID, Phase, Continues, How, Branch string
}

func (w *world) writerRows(task string) []fixRow {
	w.t.Helper()
	rows, err := w.owner.Query(context.Background(), `SELECT id, phase::text, COALESCE(continues_run_id, ''), COALESCE(continues_how, ''),
			COALESCE(push_branch, '')
		FROM runs WHERE task_id = $1 AND phase IN ('implement', 'fix') ORDER BY created_at, id`, task)
	if err != nil {
		w.t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByPos[fixRow])
	if err != nil {
		w.t.Fatal(err)
	}
	return out
}

// stuckReview is a task whose reviewer judges every fix of its finding
// "still": fix rounds follow each other until the policy's bound.
func stuckReview(t *testing.T, iterations int) (*world, string) {
	w := resuming(t)
	w.onModel("reviewer", fakeagent.StuckModel)
	wi := w.task()
	w.names(wi, w.repoID)
	p := resumingPolicy()
	p.MaxReviewIterations = iterations
	w.start(wi, p)
	return w, wi
}

// The implementer is taken back up for the first fix round and that fixer
// for the second: each a new row on the implementer's lux Run, told the
// round with what it fixes — the fix contract only the first time, since
// the implementer's instruction file lacks it. Both rounds push to the one
// per-Run branch, which publish keeps while the writer is kept: lux's lease
// is that branch's last push, so had the first round's publish deleted it
// the second round's push would be rejected.
func TestFixRoundsResumeTheImplementerThenTheFixerAndPushAgain(t *testing.T) {
	w, wi := stuckReview(t, 3)
	w.until("the second fix round published", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'fix' AND status = 'completed'`, wi) >= 2
	})
	writers := w.writerRows(wi)
	if len(writers) < 3 {
		t.Fatalf("writers: %+v", writers)
	}
	impl, fix1, fix2 := writers[0], writers[1], writers[2]
	if fix1.Continues != impl.ID || fix1.How != delivery.ContinuesResume || fix2.Continues != fix1.ID || fix2.How != delivery.ContinuesResume {
		t.Fatalf("fix rounds: %+v; want the implementer resumed, then the first fixer", writers)
	}
	if fix1.Branch != impl.Branch || fix2.Branch != impl.Branch {
		t.Fatalf("push branches %q %q %q, want the implementer's throughout", impl.Branch, fix1.Branch, fix2.Branch)
	}
	if luxImpl, luxFix := w.luxRunsOf("implement"), w.luxRunsOf("fix"); len(luxImpl) != 1 || len(luxFix) != 0 || luxImpl[0].Resumed != 2 {
		t.Fatalf("%d implementers, %d fixers submitted to lux; want one implementer, resumed twice", len(luxImpl), len(luxFix))
	}
	// Each round's push was accepted and published: the task branch holds
	// the second round's commit.
	if n := w.count(`SELECT count(*) FROM runs WHERE id = ANY($1) AND push_result::text LIKE '%"status": "pushed"%'`,
		[]string{fix1.ID, fix2.ID}); n != 2 {
		t.Fatalf("%d of the two fix rounds pushed: %v", n,
			w.count(`SELECT count(*) FROM runs WHERE id = $1 AND push_result::text LIKE '%rejected%'`, fix2.ID))
	}
	var rounds []string
	for _, in := range w.lux.Inputs(w.luxRunsOf("implement")[0].ID) {
		if strings.HasPrefix(in, "## A new round") {
			rounds = append(rounds, in)
		}
	}
	if len(rounds) != 2 {
		t.Fatalf("%d round messages, want 2: %q", len(rounds), rounds)
	}
	// Only the reviewer ran between rounds: the head each round starts from
	// is the writer's own last push, so there is no range to name.
	for i, r := range rounds {
		if !strings.Contains(r, "## Review findings") || strings.Contains(r, "git log ") {
			t.Errorf("round %d: want the findings and no range:\n%s", i+1, r)
		}
	}
	if !strings.Contains(rounds[0], "## How you fix") || strings.Contains(rounds[1], "## How you fix") {
		t.Errorf("the fix contract: want it in the first round only:\n%s\n---\n%s", rounds[0], rounds[1])
	}
	// The finding: one, its attempts one per fix round, not doubled.
	if n := w.count(`SELECT count(*) FROM review_findings WHERE task_id = $1`, wi); n != 1 {
		t.Fatalf("%d findings, want one", n)
	}
	if n := w.count(`SELECT max(fix_attempts) FROM review_findings WHERE task_id = $1`, wi); n < 2 {
		t.Fatalf("fix attempts %d, want one per round", n)
	}
}

// A kept writer's per-Run branch outlives its publish only while the
// writer is kept: once the task lets it go, RetireCompleted terminates
// its lux Run and deletes the branch. With the policy off the branch goes
// at publish, as before, and nothing is kept.
func TestAKeptWritersBranchIsDeletedWhenItIsLetGo(t *testing.T) {
	for _, on := range []bool{true, false} {
		t.Run(map[bool]string{true: "resuming", false: "off"}[on], func(t *testing.T) {
			var w *world
			if on {
				w = resuming(t)
			} else {
				w = newWorld(t)
			}
			wi := w.task()
			w.names(wi, w.repoID)
			p := resumingPolicy()
			p.ResumeRounds = on
			w.start(wi, p)
			w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })
			impl := w.writerRows(wi)[0]
			if impl.Branch == "" {
				t.Fatal("the implementer has no push branch")
			}
			kept := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase IN ('implement', 'fix') AND keep`, wi)
			if !on {
				if kept != 0 || w.gh.SHA(impl.Branch) != "" {
					t.Fatalf("policy off: %d writers kept, branch at %q; want none, deleted", kept, w.gh.SHA(impl.Branch))
				}
				return
			}
			last := w.writerRows(wi)
			holder := last[len(last)-1]
			if kept != 1 || w.gh.SHA(holder.Branch) == "" {
				t.Fatalf("%d writers kept, branch %q at %q; want the last kept with its branch", kept, holder.Branch, w.gh.SHA(holder.Branch))
			}
			if err := w.app.InOrg(context.Background(), w.org, func(tx pgx.Tx) error {
				_, err := delivery.SetTaskStatusTx(context.Background(), tx, w.org, w.project, wi, "", "done", "merged")
				return err
			}); err != nil {
				t.Fatal(err)
			}
			w.until("the writer retired and its branch deleted", func() bool {
				return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase IN ('implement', 'fix') AND lux_stop_reason = 'cancel'
					AND lux_run_id IS NOT NULL`, wi) == 1 && w.gh.SHA(holder.Branch) == ""
			})
		})
	}
}
