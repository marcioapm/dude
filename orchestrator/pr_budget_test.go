package orchestrator_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

// budgetWorld is a pull request under review whose PR fixers commit only
// while commit is set, and crash while crash is.
type budgetWorld struct {
	*world
	wi            string
	commit, crash atomic.Bool
}

func newBudgetWorld(t *testing.T, fixRounds string) *budgetWorld {
	w := &budgetWorld{world: newWorld(t)}
	mustExec(t, w.owner, `UPDATE forge_credentials SET settings = '{"fixRoundsPerPr":`+fixRounds+`}' WHERE organization_id = $1`, w.org)
	w.commit.Store(true)
	scripted := w.lux.Decide
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		b := scripted(spec)
		if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] == "fix" {
			if w.crash.Load() {
				return fakelux.Behaviour{Crash: true}
			}
			if !w.commit.Load() {
				b.Commit = nil
			}
		}
		return b
	}
	w.wi = w.reviewing()
	return w
}

// spent is what the delivery has charged the target pull request's budget.
func (w *budgetWorld) spent() int {
	return w.count(`SELECT COALESCE((state->'prFixesByRepo'->>'target')::int, 0) FROM workflow_runs WHERE task_id = $1`, w.wi)
}

func (w *budgetWorld) spentDetail() string {
	var s string
	if err := w.owner.QueryRow(context.Background(), `SELECT COALESCE(payload->'detail'->>'spent', '') FROM events WHERE task_id = $1
		AND event_type = 'question.asked' ORDER BY cursor DESC LIMIT 1`, w.wi).Scan(&s); err != nil && err != pgx.ErrNoRows {
		w.t.Fatal(err)
	}
	return s
}

// A fix that commits nothing spends none of the pull request's budget: with
// one fix round per pull request, the next feedback still gets its fix, and
// only the one after the fix that committed is past the bound.
func TestAPullRequestFixThatCommitsNothingSpendsNoBudget(t *testing.T) {
	testAPullRequestFixThatCommitsNothingSpendsNoBudget(t)
}

func testAPullRequestFixThatCommitsNothingSpendsNoBudget(t *testing.T) {
	w := newBudgetWorld(t, "1")
	setPolicy(w.world, w.wi, `{"maxPrFixIterations":9}`)
	head := w.gh.SHA(w.gh.Pull(1).Head)
	w.commit.Store(false)
	w.gh.Comment(1, "alice", "Is this ready?")
	w.until("the fix that changed nothing", func() bool { w.sync(); return w.fixes(w.wi) == 1 && w.taskStatus(w.wi) == "review" })
	if got := w.gh.SHA(w.gh.Pull(1).Head); got != head {
		t.Fatalf("the quiet fixer moved the pull request: %s -> %s", head, got)
	}
	if n := w.spent(); n != 0 {
		t.Fatalf("a fix that committed nothing spent %d of the budget", n)
	}

	w.commit.Store(true)
	w.gh.Comment(1, "alice", "Please rename the greeting.")
	w.until("the fix that committed", func() bool {
		w.sync()
		return w.fixes(w.wi) == 2 && w.taskStatus(w.wi) == "review" && w.gh.SHA(w.gh.Pull(1).Head) != head
	})
	if r := w.escalationReason(w.wi); r != "" {
		t.Fatalf("escalated %s within the budget", r)
	}
	if n := w.spent(); n != 1 {
		t.Fatalf("the fix that committed spent %d, want 1", n)
	}

	w.gh.Comment(1, "alice", "And the farewell.")
	w.until("escalated", func() bool { w.sync(); return w.taskStatus(w.wi) == "awaiting_input" })
	if r := w.escalationReason(w.wi); r != "pr_loop_exhausted" {
		t.Fatalf("escalated %q, want pr_loop_exhausted", r)
	}
	if s := w.spentDetail(); s != `["target"]` {
		t.Errorf("spent = %s, want target's", s)
	}
	if n := w.fixes(w.wi); n != 2 {
		t.Errorf("%d PR fixers, want the two within the budget", n)
	}
	if n := w.spent(); n != 1 {
		t.Errorf("the refused fix charged the budget: %d", n)
	}
}

// A fixer that commits nothing while a person's feedback item stays open is
// followed by another, and the per-round bound still ends that loop, though
// the pull request's budget is unspent.
func TestFixersThatCommitNothingAreStillBoundedPerRound(t *testing.T) {
	testFixersThatCommitNothingAreStillBoundedPerRound(t)
}

func testFixersThatCommitNothingAreStillBoundedPerRound(t *testing.T) {
	w := newBudgetWorld(t, "1")
	w.commit.Store(false)
	if code, body := w.feedback(w.person("Ana"), w.wi, "fbi_quiet", "Log the retry count at info."); code != 201 {
		t.Fatalf("submit: %d %v", code, body)
	}
	w.until("pr_loop_exhausted", func() bool { w.sync(); return w.escalationReason(w.wi) == "pr_loop_exhausted" })
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'fix' AND 'fbi_quiet' = ANY(feedback_item_ids)`, w.wi); n != scriptedPolicy().MaxPRFixIterations {
		t.Errorf("%d fixers given the item, want the round's %d", n, scriptedPolicy().MaxPRFixIterations)
	}
	if n := w.spent(); n != 0 {
		t.Errorf("fixers that committed nothing spent %d", n)
	}
	if s := w.spentDetail(); s != `[]` {
		t.Errorf("spent = %s, want none: the round's bound stopped it", s)
	}
}

// A fix that failed before landing spent nothing, so its retry gives back
// nothing: the budget is what the committed fixes spent, no more, no less.
func TestARetryAfterAFailedPullRequestFixRefundsNothing(t *testing.T) {
	testARetryAfterAFailedPullRequestFixRefundsNothing(t)
}

func testARetryAfterAFailedPullRequestFixRefundsNothing(t *testing.T) {
	w := newBudgetWorld(t, "2")
	setPolicy(w.world, w.wi, `{"maxPrFixIterations":9}`)
	w.gh.Comment(1, "alice", "Please rename the greeting.")
	w.until("the first fix", func() bool { w.sync(); return w.fixes(w.wi) == 1 && w.taskStatus(w.wi) == "review" })
	if n := w.spent(); n != 1 {
		t.Fatalf("the first fix spent %d, want 1", n)
	}

	w.crash.Store(true)
	w.gh.Comment(1, "alice", "And the farewell.")
	w.until("pr_fix_failed", func() bool { w.sync(); return w.escalationReason(w.wi) == "pr_fix_failed" })
	if n := w.spent(); n != 1 {
		t.Fatalf("after the failed fix the budget is %d, want the first fix's 1", n)
	}
	w.crash.Store(false)
	if code, body := w.decide(w.person("owner"), w.wi, "retry", ""); code != 200 {
		t.Fatalf("retry: %d %v", code, body)
	}
	w.until("the retried fix", func() bool { w.sync(); return w.fixes(w.wi) == 3 && w.taskStatus(w.wi) == "review" })
	if n := w.spent(); n != 2 {
		t.Fatalf("after the retried fix the budget is %d, want 2", n)
	}

	w.gh.Comment(1, "alice", "And a test.")
	w.until("escalated", func() bool { w.sync(); return w.taskStatus(w.wi) == "awaiting_input" })
	if r, s := w.escalationReason(w.wi), w.spentDetail(); r != "pr_loop_exhausted" || s != `["target"]` {
		t.Fatalf("escalated %q spent %s, want pr_loop_exhausted on target", r, s)
	}
	if n := w.fixes(w.wi); n != 3 {
		t.Errorf("%d PR fixers, want 3", n)
	}
}

func TestALegacyPRFixAlreadyChargedAtStartIsNotChargedAgain(t *testing.T) {
	testALegacyPRFixAlreadyChargedAtStartIsNotChargedAgain(t)
}

func testALegacyPRFixAlreadyChargedAtStartIsNotChargedAgain(t *testing.T) {
	w := newPRFixWorld(t)
	w.play = func(phase string, n int, b *fakelux.Behaviour) {
		if phase == "fix" {
			b.Hang = true
		}
	}
	w.comment("Please rename the greeting.")
	w.until("fix running", func() bool {
		w.sync()
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'fix' AND status = 'running' AND pr_feedback <> '[]'::jsonb AND base_refs <> '{}'::jsonb`, w.wi) == 1
	})
	mustExec(t, w.owner, `UPDATE workflow_runs SET state = jsonb_set(state - 'prFixCharged', '{prFixesByRepo}', '{"target":5}') WHERE task_id = $1 AND step = 'awaitPRFix'`, w.wi)
	var luxID string
	if err := w.owner.QueryRow(context.Background(), `SELECT lux_run_id FROM runs WHERE task_id = $1 AND phase = 'fix' AND status = 'running'`, w.wi).Scan(&luxID); err != nil {
		t.Fatal(err)
	}
	w.lux.EndTurn(luxID)
	w.until("legacy fix promoted", func() bool { return w.gh.SHA(w.branch) != w.before && w.taskStatus(w.wi) == "review" })
	if f := w.state("prFixesByRepo"); f != `{"target": 5}` {
		t.Fatalf("legacy landing charged again: %s, want 5", f)
	}
}

// The earlier landing-charged version retained a previous round's charge
// marker when starting the next one. That is not a legacy start charge:
// refunding it on retry would forgive the previous successful fix.
func TestAnUpgradeDuringAFailedPRFixKeepsThePriorCharge(t *testing.T) {
	testAnUpgradeDuringAFailedPRFixKeepsThePriorCharge(t)
}

func testAnUpgradeDuringAFailedPRFixKeepsThePriorCharge(t *testing.T) {
	w := newBudgetWorld(t, "2")
	setPolicy(w.world, w.wi, `{"maxPrFixIterations":9}`)
	w.gh.Comment(1, "alice", "Please rename the greeting.")
	w.until("first fix", func() bool { w.sync(); return w.fixes(w.wi) == 1 && w.taskStatus(w.wi) == "review" })
	var previous string
	if err := w.owner.QueryRow(context.Background(), `SELECT (state->'prFixCharged')::text FROM workflow_runs WHERE task_id=$1`, w.wi).Scan(&previous); err != nil {
		t.Fatal(err)
	}
	if n := w.spent(); n != 1 {
		t.Fatalf("first fix spent %d, want 1", n)
	}
	w.crash.Store(true)
	w.gh.Comment(1, "alice", "And the farewell.")
	w.until("failed fix", func() bool { w.sync(); return w.escalationReason(w.wi) == "pr_fix_failed" })
	// Reproduce the intermediate release's persisted state at deploy:
	// round 2 failed without a charge, retaining round 1's marker.
	mustExec(t, w.owner, `UPDATE workflow_runs SET state=jsonb_set(state, '{prFixCharged}', $2::jsonb) WHERE task_id=$1`, w.wi, previous)
	w.crash.Store(false)
	if code, body := w.decide(w.person("owner"), w.wi, "retry", ""); code != 200 {
		t.Fatalf("retry: %d %v", code, body)
	}
	w.until("retried fix", func() bool { w.sync(); return w.fixes(w.wi) == 3 && w.taskStatus(w.wi) == "review" })
	if n := w.spent(); n != 2 {
		t.Fatalf("two successful rounds spent %d, want 2; prior successful charge was forgiven", n)
	}
	w.gh.Comment(1, "alice", "And a test.")
	w.until("budget exhausted", func() bool { w.sync(); return w.escalationReason(w.wi) == "pr_loop_exhausted" })
	if n := w.fixes(w.wi); n != 3 {
		t.Fatalf("%d fixers, want the three already run", n)
	}
}
