package orchestrator_test

// A writer's publish when the task branch had its base merged in under it
// (abs#5477): by dude's own update-branch in the sync that started the
// fix, by a person's Update branch on GitHub, or by a later sync. The
// automatic update is not asked for by a sync that has just signalled;
// one that lands anyway is merged with the writer's commit on GitHub. A
// person's own commit under the writer is never merged over.

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
)

// One sync reads a change request and the pull request falling behind
// together, and the workflow takes the change request into a fix before
// that sync goes on to the branch: that sync asks GitHub for no update,
// the fix publishes, and the branch is updated once the delivery waits on
// the pull request again.
func TestABranchIsNotUpdatedUnderTheFixItsOwnSyncStarted(t *testing.T) {
	w := newWorld(t)
	wi := w.reviewing()
	head := w.gh.SHA(w.gh.Pull(1).Head)
	signal := w.prs.Signal
	// The workflow poller, running as the sync signals: the fix Run exists
	// before the sync reaches the branch.
	w.prs.Signal = func(ctx context.Context, org, wf, name string, payload any, key string) error {
		if err := signal(ctx, org, wf, name, payload, key); err != nil {
			return err
		}
		for range 5 {
			if _, err := w.runtime.Tick(ctx, 10); err != nil || w.fixes(wi) > 0 {
				return err
			}
		}
		return nil
	}
	w.gh.AdvanceBase("main", "Someone else's work")
	w.gh.ReviewSaying(1, "jerv", "CHANGES_REQUESTED", "Please rename the greeting.")
	w.sync()
	w.prs.Signal = signal
	if n := w.fixes(wi); n != 1 {
		t.Fatalf("%d fixes started by the sync's signal", n)
	}
	if n := w.updateCalls(); n != 0 {
		t.Errorf("the sync that started the fix asked GitHub to update the branch %d times", n)
	}
	w.until("the fix settled", func() bool {
		w.sync()
		return w.taskStatus(wi) == "awaiting_input" || w.taskStatus(wi) == "review" && w.branchUpdates(wi) == 1
	})
	w.fixPublishedFrom(wi, head)
}

// With whenBehind tell, falling behind with feedback is a fix and never a
// branch update.
func TestABranchBehindIsNotUpdatedWhenTheOrganizationAsksToBeTold(t *testing.T) {
	w := newWorld(t)
	mustExec(t, w.owner, `UPDATE forge_credentials SET settings = '{"whenBehind":"tell"}' WHERE organization_id = $1`, w.org)
	wi := w.reviewing()
	w.gh.AdvanceBase("main", "Someone else's work")
	w.gh.ReviewSaying(1, "jerv", "CHANGES_REQUESTED", "Please rename the greeting.")
	w.until("the fix settled", func() bool {
		w.sync()
		return w.fixes(wi) == 1 && w.taskStatus(wi) == "review"
	})
	for range 3 {
		w.sync()
	}
	if n := w.updateCalls(); n != 0 {
		t.Errorf("updated %d times; the organization asked only to be told", n)
	}
}

// GitHub merges the base into the task branch while a fix is at work, as a
// person's Update branch or a later sync's does: the fix's publish merges
// its commit in on GitHub. The branch's head is that merge, over GitHub's
// and the fix's; the fix's change is its own, not the base's; the
// delivery, the pull request and the next fix start from the merge.
func TestAFixPublishesOverABaseMergeThatLandedUnderIt(t *testing.T) {
	w := newWorld(t)
	wi := w.reviewing()
	branch := w.gh.Pull(1).Head
	start := w.gh.SHA(branch)
	landed := w.underTheWriter(branch, func() string {
		w.gh.CommitFiles("main", "Someone else's work", map[string]string{"BASE.md": "the base's\n"})
		return w.gh.MergeInto(branch, "main", nil)
	})
	fix := w.prFixSettled(wi)
	if r := w.escalationReason(wi); r != "" || fix.status != "completed" {
		t.Fatalf("fix %s (%s), escalated %q\nruns:\n%s", fix.status, fix.error, r, w.describeRuns())
	}
	x := landed()
	if x == "" || fix.base != start {
		t.Fatalf("GitHub's merge %q, fix from %.8s, want from %.8s", x, fix.base, start)
	}
	m := w.gh.SHA(branch)
	if got := w.gh.Parents(m); !slices.Equal(got, []string{x, fix.commit}) {
		t.Fatalf("the branch's head %.8s has parents %v, want GitHub's merge %.8s then the fix's %.8s", m, got, x, fix.commit)
	}
	if fix.head != m || !slices.Equal(fix.paths, []string{"FIXED.md"}) {
		t.Errorf("the fix recorded head %.8s changing %v, want %.8s changing [FIXED.md]", fix.head, fix.paths, m)
	}
	if n := w.count(`SELECT count(*) FROM pull_requests WHERE task_id = $1 AND head_sha = $2`, wi, m); n != 1 {
		t.Error("the pull request's head is not the merge")
	}
	if n := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND state->'heads'->>'target' = $2`, wi, m); n != 1 {
		t.Error("the delivery's head is not the merge")
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'git.commit_created' AND payload->>'headSha' = $2
		AND payload->'mergedBase'->>'commit' = $3 AND payload->'mergedBase'->>'into' = $4`, wi, m, fix.commit, x); n != 1 {
		t.Errorf("%d commit events saying dude merged the fix in", n)
	}
	w.sync()
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'pull_request.pushed'`, wi); n != 0 {
		t.Errorf("%d pushes recorded as someone else's", n)
	}
	w.gh.Comment(1, "alice", "Please also say goodbye.")
	w.until("the next fix", func() bool { w.sync(); return w.fixes(wi) == 2 })
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'fix' AND base_refs->>'target' = $2`, wi, m); n != 1 {
		t.Error("the next fix did not start from the merge")
	}
}

// A person's own commit lands on the task branch while a fix is at work:
// the fix's publish is refused as before, and the person's commit is the
// branch's head still.
func TestAFixIsRefusedOverAPersonsCommitThatLandedUnderIt(t *testing.T) {
	w := newWorld(t)
	wi := w.reviewing()
	branch := w.gh.Pull(1).Head
	landed := w.underTheWriter(branch, func() string {
		return w.gh.CommitFiles(branch, "A person's own fix", map[string]string{"PERSON.md": "mine\n"})
	})
	fix := w.prFixSettled(wi)
	if fix.status != "failed" || !strings.Contains(fix.error, "Update is not a fast forward") {
		t.Fatalf("fix %s (%s), want refused as not a fast-forward", fix.status, fix.error)
	}
	if r := w.escalationReason(wi); r != "pr_fix_failed" {
		t.Errorf("escalated %q, want pr_fix_failed", r)
	}
	if got := w.gh.SHA(branch); got != landed() {
		t.Errorf("the task branch at %.8s, want the person's commit %.8s", got, landed())
	}
}

// The base merged in under a fix conflicts with the fix: the publish is
// refused naming the base merge, and the delivery escalates as before.
func TestAFixConflictingWithABaseMergeUnderItIsRefused(t *testing.T) {
	w := newWorld(t)
	wi := w.reviewing()
	branch := w.gh.Pull(1).Head
	// The base writes the file the fixer writes, and a person resolves the
	// conflict on GitHub keeping the base's, while the fix is at work: no
	// sync sees the pull request conflict.
	landed := w.underTheWriter(branch, func() string {
		w.gh.CommitFiles("main", "Someone else's work", map[string]string{"FIXED.md": "the base's\n"})
		return w.gh.MergeInto(branch, "main", map[string]string{"FIXED.md": "the base's\n"})
	})
	fix := w.prFixSettled(wi)
	if fix.status != "failed" || !strings.Contains(fix.error, "main was merged into the task branch meanwhile") ||
		!strings.Contains(fix.error, "conflicts with that merge") {
		t.Fatalf("fix %s (%s), want refused naming the base merge; GitHub's merge %q, branch log %v", fix.status, fix.error, landed(), w.gh.Log(branch))
	}
	if r := w.escalationReason(wi); r != "pr_fix_failed" {
		t.Errorf("escalated %q, want pr_fix_failed", r)
	}
	if x := landed(); x == "" || w.gh.SHA(branch) != x {
		t.Errorf("the task branch at %.8s, want GitHub's merge %.8s", w.gh.SHA(branch), x)
	}
}

// A conductor's publish over a base merge that landed under it: merged in
// on GitHub, its change its own, and the next phase starts from the merge.
func TestAConductorsPublishOverABaseMergeIsMergedIn(t *testing.T) {
	e, sha := newCommitted(t)
	start := e.gh.SHA(e.branch)
	landed := e.underTheWriter(e.branch, func() string {
		e.gh.CommitFiles("main", "Someone else's work", map[string]string{"BASE.md": "the base's\n"})
		return e.gh.MergeInto(e.branch, "main", nil)
	})
	id := e.publish()
	if status, why := e.published(id); status != delivery.PublishPublished {
		t.Fatalf("publish %s: %s", status, why)
	}
	x, m := landed(), e.gh.SHA(e.branch)
	if got := e.gh.Parents(m); x == "" || !slices.Equal(got, []string{x, sha}) {
		t.Fatalf("the branch's head %.8s has parents %v, want GitHub's merge %.8s then the conductor's %.8s (from %.8s)", m, got, x, sha, start)
	}
	if n := e.count(`SELECT count(*) FROM conductor_publishes WHERE id = $1 AND heads->'target'->>'sha' = $2
		AND heads->'target'->'changedPaths' = '["README.md"]'::jsonb`, id, m); n != 1 {
		t.Error("the publish's head is not the merge, or its change is not the conductor's own")
	}
	if n := e.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND state->'heads'->>'target' = $2
		AND state->'changedPaths' ? 'target/README.md' AND NOT state->'changedPaths' ? 'target/BASE.md'`, e.task, m); n != 1 {
		var heads, paths string
		_ = e.owner.QueryRow(context.Background(), `SELECT state->>'heads', state->>'changedPaths' FROM workflow_runs WHERE task_id = $1`, e.task).Scan(&heads, &paths)
		t.Errorf("the delivery has heads %s and changed paths %s, want the merge %.8s and the conductor's README.md", heads, paths, m)
	}
	if n := e.count(`SELECT count(*) FROM pull_requests WHERE task_id = $1`, e.task); n != 0 {
		t.Fatalf("%d pull requests", n)
	}
	if n := e.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'git.commit_created' AND payload->>'by' = 'conductor'
		AND payload->>'headSha' = $2 AND payload->'mergedBase'->>'commit' = $3 AND payload->'mergedBase'->>'into' = $4`, e.task, m, sha, x); n != 1 {
		t.Errorf("%d commit events saying dude merged the conductor's commit in", n)
	}
	e.must(e.task, "start_phase", `{"phase":"review","categories":["correctness"]}`)
	e.until("the review", func() bool { return e.decisionAt(e.task) == delivery.PointReviewed })
	if n := e.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review' AND base_refs->>'target' = $2`, e.task, m); n != 1 {
		t.Error("the review did not start from the merge")
	}
}

// underTheWriter has land run once, on GitHub, as the first move of branch
// arrives: what lands on the task branch while a writer is at work, before
// its publish. Returns what land returned, once it has.
func (w *world) underTheWriter(branch string, land func() string) func() string {
	w.t.Helper()
	var once sync.Once
	var landed atomic.Pointer[string]
	w.gh.Set(func(s *fakegithub.Server) {
		s.Intercept = func(r *http.Request) int {
			if r.Method == http.MethodPatch && strings.HasSuffix(r.URL.Path, "/git/refs/heads/"+branch) {
				once.Do(func() { sha := land(); landed.Store(&sha) })
			}
			return 0
		}
	})
	return func() string {
		if p := landed.Load(); p != nil {
			return *p
		}
		return ""
	}
}

// prFix is the task's first pull request fix as it settled.
type prFix struct {
	status, error, base, commit, head string
	paths                             []string
}

// prFixSettled asks for a pull request fix (a comment), and waits for it
// to settle and the delivery to go on: back in review, or escalated.
func (w *world) prFixSettled(wi string) prFix {
	w.t.Helper()
	w.gh.Comment(1, "alice", "Please rename the greeting.")
	w.until("a fix", func() bool { w.sync(); return w.fixes(wi) == 1 })
	var f prFix
	read := func() {
		f.paths = nil
		if err := w.app.InOrg(context.Background(), w.org, func(tx pgx.Tx) error {
			return tx.QueryRow(context.Background(), `SELECT status::text, COALESCE(error, ''), COALESCE(base_refs->>'target', ''),
					COALESCE(push_result->'results'->0->>'commit', ''), COALESCE(heads->'target'->>'sha', ''),
					COALESCE(ARRAY(SELECT jsonb_array_elements_text(heads->'target'->'changedPaths')), '{}')
				FROM runs WHERE task_id = $1 AND phase = 'fix' AND pr_feedback <> '[]'::jsonb`, wi).
				Scan(&f.status, &f.error, &f.base, &f.commit, &f.head, &f.paths)
		}); err != nil {
			w.t.Fatal(err)
		}
	}
	w.until("the fix settled", func() bool {
		read()
		s := w.taskStatus(wi)
		return f.status == "failed" && s == "awaiting_input" || f.status == "completed" && s == "review"
	})
	return f
}

// updateCalls counts the update-branch requests GitHub received.
func (w *world) updateCalls() int {
	n := 0
	w.gh.Set(func(s *fakegithub.Server) { n = len(s.Updates) })
	return n
}

// branchUpdates counts the update-branch requests GitHub took for the
// task's pull requests.
func (w *world) branchUpdates(wi string) int {
	return w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'pull_request.action'
		AND payload->>'action' = 'update-branch' AND payload->>'refused' IS NULL`, wi)
}

// fixPublishedFrom asserts the task's one pull request fix started at base,
// published, and that GitHub was asked to update the branch once, at the
// head the fix published, never under it.
func (w *world) fixPublishedFrom(wi, base string) {
	w.t.Helper()
	if r := w.escalationReason(wi); r != "" {
		w.t.Fatalf("escalated %s\nruns:\n%s", r, w.describeRuns())
	}
	var status, from, published string
	if err := w.app.InOrg(context.Background(), w.org, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `SELECT status::text, COALESCE(base_refs->>'target', ''),
				COALESCE(push_result->'results'->0->>'commit', '')
			FROM runs WHERE task_id = $1 AND phase = 'fix' AND pr_feedback <> '[]'::jsonb`, wi).Scan(&status, &from, &published)
	}); err != nil {
		w.t.Fatal(err)
	}
	if status != "completed" || from != base {
		w.t.Fatalf("fix %s from %.8s, want completed from %.8s", status, from, base)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND error LIKE '%not a fast forward%'`, wi); n != 0 {
		w.t.Errorf("%d Runs refused a fast-forward", n)
	}
	if n := w.branchUpdates(wi); n != 1 {
		w.t.Fatalf("%d branch updates, want 1", n)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'pull_request.action'
		AND payload->>'action' = 'update-branch' AND payload->>'refused' IS NULL AND payload->>'head' = $2`, wi, published); n != 1 {
		w.t.Errorf("the branch was not updated at the head the fix published (%.8s)", published)
	}
}
