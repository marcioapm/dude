package orchestrator_test

// A writer's publish when the task branch had its base merged in under it
// (abs#5477): by dude's own update-branch in the sync that started the
// fix, by a person's Update branch on GitHub, or by a later sync. The
// automatic update is not asked for by a sync that has just signalled;
// one that lands anyway is merged with the writer's commit on GitHub. A
// person's own commit under the writer is never merged over.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
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
	// The next fix's own commit is made on the merge, whether or not its
	// publish then goes through.
	var next string
	w.until("the next fix pushed", func() bool {
		_ = w.owner.QueryRow(context.Background(), `SELECT COALESCE(push_result->'results'->0->>'commit', '') FROM runs
			WHERE task_id = $1 AND phase = 'fix' AND pr_feedback <> '[]'::jsonb AND status IN ('completed', 'failed')
			ORDER BY created_at DESC LIMIT 1 OFFSET 0`, wi).Scan(&next)
		return next != "" && next != fix.commit
	})
	if got := w.gh.Parents(next); len(got) == 0 || got[0] != m {
		t.Errorf("the next fix's commit %.8s has parents %v, want the merge %.8s first", next, got, m)
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
	// The base takes the task branch's work and rewrites the file the fixer
	// writes; GitHub merges it in cleanly while the fix is at work, and no
	// sync sees the pull request conflict.
	landed := w.underTheWriter(branch, func() string {
		w.gh.MergeCommit("main", w.gh.SHA(branch))
		w.gh.CommitFiles("main", "Someone else's work", map[string]string{"FIXED.md": "the base's\n"})
		return w.gh.MergeInto(branch, "main", nil)
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

// A merge of the base that also adds a person's file in its resolution
// lands under a fix: its parents are the task branch and the base, but it
// is not only the base's, so the fix's publish is refused as not a
// fast-forward and the branch keeps the merge. Made on a person's machine
// or by GitHub alike.
func TestAFixIsRefusedOverABaseMergeCarryingAPersonsEdit(t *testing.T) {
	for _, made := range []struct {
		by    string
		merge func(s *fakegithub.Server, branch string) string
	}{
		{"locally", func(s *fakegithub.Server, branch string) string {
			return s.MergeLocally(branch, "main", map[string]string{"PERSON.md": "theirs\n"})
		}},
		{"by GitHub", func(s *fakegithub.Server, branch string) string {
			return s.MergeInto(branch, "main", map[string]string{"PERSON.md": "theirs\n"})
		}},
	} {
		t.Run(made.by, func(t *testing.T) {
			w := newWorld(t)
			wi := w.reviewing()
			branch := w.gh.Pull(1).Head
			start := w.gh.SHA(branch)
			landed := w.underTheWriter(branch, func() string {
				w.gh.CommitFiles("main", "Someone else's work", map[string]string{"BASE.md": "the base's\n"})
				return made.merge(w.gh, branch)
			})
			fix := w.prFixSettled(wi)
			x := landed()
			if got := w.gh.Parents(x); x == "" || len(got) != 2 || got[0] != start {
				t.Fatalf("the evil merge %.8s has parents %v, want the task branch %.8s first", x, got, start)
			}
			if fix.status != "failed" || !strings.Contains(fix.error, "Update is not a fast forward") {
				t.Fatalf("fix %s (%s), want refused as not a fast-forward", fix.status, fix.error)
			}
			if r := w.escalationReason(wi); r != "pr_fix_failed" {
				t.Errorf("escalated %q, want pr_fix_failed", r)
			}
			if got := w.gh.SHA(branch); got != x {
				t.Errorf("the task branch at %.8s, want the evil merge %.8s", got, x)
			}
		})
	}
}

// A clean merge of the base that a person made on their machine and
// pushed is not GitHub's: the fix's publish does not merge over it.
func TestAFixIsRefusedOverABaseMergeNotMadeByGitHub(t *testing.T) {
	w := newWorld(t)
	wi := w.reviewing()
	branch := w.gh.Pull(1).Head
	landed := w.underTheWriter(branch, func() string {
		w.gh.CommitFiles("main", "Someone else's work", map[string]string{"BASE.md": "the base's\n"})
		return w.gh.MergeLocally(branch, "main", nil)
	})
	fix := w.prFixSettled(wi)
	if fix.status != "failed" || !strings.Contains(fix.error, "Update is not a fast forward") {
		t.Fatalf("fix %s (%s), want refused as not a fast-forward", fix.status, fix.error)
	}
	if x := landed(); x == "" || w.gh.SHA(branch) != x {
		t.Errorf("the task branch at %.8s, want the person's merge %.8s", w.gh.SHA(branch), x)
	}
}

// A fix whose publish fast-forwards the task branch, as every phase of the
// delivery before it did, never looks up whether its Run attempted a merge:
// only a branch holding a merge of the fix's commit asks.
func TestAFastForwardPublishLooksUpNoMergeAttempt(t *testing.T) {
	w := newWorld(t)
	var lookups atomic.Int64
	w.syncer.MergeAttemptLookup = func() { lookups.Add(1) }
	wi := w.reviewing()
	fix := w.prFixSettled(wi)
	if r := w.escalationReason(wi); r != "" || fix.status != "completed" {
		t.Fatalf("fix %s (%s), escalated %q", fix.status, fix.error, r)
	}
	if got := w.gh.SHA(w.gh.Pull(1).Head); got != fix.commit || fix.head != fix.commit {
		t.Fatalf("the branch at %.8s, the fix recorded %.8s, want both fast-forwarded to its commit %.8s", got, fix.head, fix.commit)
	}
	if n := lookups.Load(); n != 0 {
		t.Errorf("%d merge-attempt lookups over fast-forward publishes, want 0", n)
	}
}

// The fix's merge over a base merge lands on GitHub and its answer is lost:
// the retried finish finds the merge on the branch and adopts it as the
// fix's, as the Run recorded attempting it, sending no second merge.
func TestAFixsMergeWhoseAnswerWasLostIsAdoptedOnRetry(t *testing.T) {
	w := newWorld(t)
	w.syncer.RetryAhead = time.Minute
	wi := w.reviewing()
	branch := w.gh.Pull(1).Head
	hook, landed := landOnce(isMoveOf(branch), func(*http.Request) string {
		w.gh.CommitFiles("main", "Someone else's work", map[string]string{"BASE.md": "the base's\n"})
		return w.gh.MergeInto(branch, "main", nil)
	})
	var merges atomic.Int64
	w.onGitHub(hook, func(r *http.Request) int {
		if isMergesCall(r) {
			merges.Add(1)
		}
		return 0
	})
	w.gh.Set(func(s *fakegithub.Server) { s.LoseMergeResponses = 1 })
	fix := w.prFixSettled(wi)
	if r := w.escalationReason(wi); r != "" || fix.status != "completed" {
		t.Fatalf("fix %s (%s), escalated %q", fix.status, fix.error, r)
	}
	x, m := landed(), w.gh.SHA(branch)
	if got := w.gh.Parents(m); x == "" || !slices.Equal(got, []string{x, fix.commit}) {
		t.Fatalf("the branch's head %.8s has parents %v, want GitHub's merge %.8s then the fix's %.8s", m, got, x, fix.commit)
	}
	if n := merges.Load(); n != 1 {
		t.Errorf("%d merge requests, want 1", n)
	}
	if fix.head != m {
		t.Errorf("the fix recorded head %.8s, want its merge %.8s", fix.head, m)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'git.merge_attempted'
		AND payload->>'commit' = $2`, wi, fix.commit); n != 1 {
		t.Errorf("%d merge attempts recorded for the fix's commit, want 1", n)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'git.commit_created' AND payload->>'headSha' = $2
		AND payload->'mergedBase'->>'commit' = $3 AND payload->'mergedBase'->>'into' = $4`, wi, m, fix.commit, x); n != 1 {
		t.Errorf("%d commit events saying dude merged the fix in", n)
	}
}

// Someone merges the fix's own commit (its per-Run branch is on GitHub)
// onto the task branch over a base merge while the fix is at work. dude
// never asked for that merge: it is not taken as the fix's, and the
// fast-forward's refusal stands.
func TestAPersonsMergeOfAFixsCommitIsNotAdopted(t *testing.T) {
	w := newWorld(t)
	wi := w.reviewing()
	branch := w.gh.Pull(1).Head
	hook, landed := landOnce(isMoveOf(branch), func(r *http.Request) string {
		w.gh.CommitFiles("main", "Someone else's work", map[string]string{"BASE.md": "the base's\n"})
		w.gh.MergeInto(branch, "main", nil)
		return w.gh.MergeCommit(branch, movedTo(r))
	})
	w.onGitHub(hook)
	fix := w.prFixSettled(wi)
	m := landed()
	if got := w.gh.Parents(m); m == "" || len(got) != 2 || got[1] != fix.commit {
		t.Fatalf("their merge %.8s has parents %v, want the fix's %.8s second", m, got, fix.commit)
	}
	if fix.status != "failed" || !strings.Contains(fix.error, "Update is not a fast forward") {
		t.Fatalf("fix %s (%s), want refused as not a fast-forward", fix.status, fix.error)
	}
	if r := w.escalationReason(wi); r != "pr_fix_failed" {
		t.Errorf("escalated %q, want pr_fix_failed", r)
	}
	if got := w.gh.SHA(branch); got != m {
		t.Errorf("the task branch at %.8s, want their merge %.8s", got, m)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'git.commit_created'
		AND (payload->>'headSha' = $2 OR payload ? 'mergedBase')`, wi, m); n != 0 {
		t.Errorf("%d commit events taking their merge as dude's", n)
	}
}

// The fix's merge lands carrying content other than the fix's change (as
// a merge GitHub made and someone then replaced would), and its answer is
// lost: the Run did attempt a merge, but this one is not exactly the
// fix's, so the retry refuses it as not a fast-forward.
func TestAFixsLostMergeCarryingOtherContentIsRefused(t *testing.T) {
	w := newWorld(t)
	w.syncer.RetryAhead = time.Minute
	wi := w.reviewing()
	branch := w.gh.Pull(1).Head
	under, x := landOnce(isMoveOf(branch), func(*http.Request) string {
		w.gh.CommitFiles("main", "Someone else's work", map[string]string{"BASE.md": "the base's\n"})
		return w.gh.MergeInto(branch, "main", nil)
	})
	evil, landed := landOnce(isMergesCall, func(r *http.Request) string {
		return w.gh.MergeCommitResolved(branch, mergeOf(r), map[string]string{"PERSON.md": "theirs\n"})
	})
	w.onGitHub(under, evil, func(r *http.Request) int {
		if isMergesCall(r) && landed() != "" {
			return http.StatusBadGateway
		}
		return 0
	})
	fix := w.prFixSettled(wi)
	m := landed()
	if got := w.gh.Parents(m); m == "" || !slices.Equal(got, []string{x(), fix.commit}) {
		t.Fatalf("the merge %.8s has parents %v, want %.8s then the fix's %.8s", m, got, x(), fix.commit)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'git.merge_attempted'
		AND payload->>'commit' = $2`, wi, fix.commit); n != 1 {
		t.Fatalf("%d merge attempts recorded for the fix's commit, want 1", n)
	}
	if fix.status != "failed" || !strings.Contains(fix.error, "Update is not a fast forward") {
		t.Fatalf("fix %s (%s), want refused as not a fast-forward", fix.status, fix.error)
	}
	if r := w.escalationReason(wi); r != "pr_fix_failed" {
		t.Errorf("escalated %q, want pr_fix_failed", r)
	}
	if got := w.gh.SHA(branch); got != m {
		t.Errorf("the task branch at %.8s, want the merge %.8s kept", got, m)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'git.commit_created' AND payload->>'headSha' = $2`, wi, m); n != 0 {
		t.Errorf("%d commit events taking the merge as the fix's", n)
	}
}

// The task branch and the base both edit NOTES.md, in different hunks,
// and GitHub merges the base in cleanly under a fix. The merge's NOTES.md
// is neither side's, so it is not proven only the base's: the fix's
// publish is refused as not a fast-forward, and the branch keeps the merge.
func TestAFixIsRefusedOverACleanBaseMergeOfAFileBothSidesEdited(t *testing.T) {
	w := newWorld(t)
	lines := func(first, last string) string {
		return first + "\n2\n3\n4\n5\n6\n7\n8\n9\n" + last + "\n"
	}
	w.gh.CommitFiles("main", "Notes", map[string]string{"NOTES.md": lines("1", "10")})
	scripted := w.lux.Decide
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		b := scripted(spec)
		if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] == "implement" {
			b.Commit = maps.Clone(b.Commit)
			if b.Commit == nil {
				b.Commit = map[string]string{}
			}
			b.Commit["NOTES.md"] = lines("1", "ten, the task's")
		}
		return b
	}
	wi := w.reviewing()
	branch := w.gh.Pull(1).Head
	start := w.gh.SHA(branch)
	landed := w.underTheWriter(branch, func() string {
		w.gh.CommitFiles("main", "Someone else's work", map[string]string{"NOTES.md": lines("one, the base's", "10")})
		return w.gh.MergeInto(branch, "main", nil)
	})
	fix := w.prFixSettled(wi)
	x := landed()
	if got := w.gh.Parents(x); x == "" || len(got) != 2 || got[0] != start {
		t.Fatalf("GitHub's merge %q has parents %v, want the task branch %.8s first", x, got, start)
	}
	if out, _ := exec.Command("git", "-C", w.gh.Repo, "show", x+":NOTES.md").Output(); string(out) != lines("one, the base's", "ten, the task's") {
		t.Fatalf("the merge's NOTES.md is %q, want both edits", out)
	}
	if fix.status != "failed" || !strings.Contains(fix.error, "Update is not a fast forward") {
		t.Fatalf("fix %s (%s), want refused as not a fast-forward", fix.status, fix.error)
	}
	if r := w.escalationReason(wi); r != "pr_fix_failed" {
		t.Errorf("escalated %q, want pr_fix_failed", r)
	}
	if got := w.gh.SHA(branch); got != x {
		t.Errorf("the task branch at %.8s, want GitHub's merge %.8s", got, x)
	}
}

// GitHub's compare answering with fewer commits than it counts, or with
// the writer's start not under the branch: the history is not judged, and
// the fix's publish is refused as not a fast-forward.
func TestAFixIsRefusedWhenGitHubsCompareCannotProveTheBaseMerges(t *testing.T) {
	for _, c := range []struct {
		name   string
		answer func(answer map[string]any)
	}{
		{"its commit list cut short", func(a map[string]any) { a["total_commits"] = len(a["commits"].([]any)) + 1 }},
		{"behind the start", func(a map[string]any) { a["behind_by"] = 1 }},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			wi := w.reviewing()
			branch := w.gh.Pull(1).Head
			start := w.gh.SHA(branch)
			landed := w.underTheWriter(branch, func() string {
				w.gh.CommitFiles("main", "Someone else's work", map[string]string{"BASE.md": "the base's\n"})
				return w.gh.MergeInto(branch, "main", nil)
			})
			w.gh.Set(func(s *fakegithub.Server) {
				s.CompareAnswer = func(base, head string, answer map[string]any) {
					if x := landed(); base == start && x != "" && head == x {
						c.answer(answer)
					}
				}
			})
			fix := w.prFixSettled(wi)
			if fix.status != "failed" || !strings.Contains(fix.error, "Update is not a fast forward") {
				t.Fatalf("fix %s (%s), want refused as not a fast-forward", fix.status, fix.error)
			}
			if x := landed(); x == "" || w.gh.SHA(branch) != x {
				t.Errorf("the task branch at %.8s, want GitHub's merge %.8s", w.gh.SHA(branch), x)
			}
		})
	}
}

// The base, holding the fix's own commit already, merged in under the fix:
// GitHub has nothing to merge (204), and the branch's head, the base
// merge, is what the fix records.
func TestAFixAlreadyInABaseMergeUnderItRecordsThatMerge(t *testing.T) {
	w := newWorld(t)
	wi := w.reviewing()
	branch := w.gh.Pull(1).Head
	hook, landed := landOnce(isMoveOf(branch), func(r *http.Request) string {
		w.gh.CommitFiles("main", "Someone else's work", map[string]string{"BASE.md": "the base's\n"})
		w.gh.MergeCommit("main", movedTo(r))
		return w.gh.MergeInto(branch, "main", nil)
	})
	var merges atomic.Int64
	w.onGitHub(hook, func(r *http.Request) int {
		if isMergesCall(r) {
			merges.Add(1)
		}
		return 0
	})
	fix := w.prFixSettled(wi)
	x := landed()
	if r := w.escalationReason(wi); r != "" || fix.status != "completed" {
		t.Fatalf("fix %s (%s), escalated %q", fix.status, fix.error, r)
	}
	if n := merges.Load(); n != 1 {
		t.Errorf("%d merge requests, want 1 answered 204", n)
	}
	if x == "" || w.gh.SHA(branch) != x || fix.head != x {
		t.Fatalf("the branch at %.8s, the fix recorded %.8s, want GitHub's merge %.8s", w.gh.SHA(branch), fix.head, x)
	}
	if n := w.count(`SELECT count(*) FROM pull_requests WHERE task_id = $1 AND head_sha = $2`, wi, x); n != 1 {
		t.Error("the pull request's head is not the base merge")
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'git.commit_created' AND payload->>'headSha' = $2`, wi, x); n != 1 {
		t.Errorf("%d commit events at the base merge", n)
	}
}

// With whenBehind tell, a pull request that falls behind with nothing
// asked of it is not updated: the falling behind is recorded in the
// task's feed, and the delivery waits on its people. No push notification
// is sent for it.
func TestABehindPullRequestIsRecordedInTheTasksFeedWhenTheOrganizationAsksToBeTold(t *testing.T) {
	w := newWorld(t)
	mustExec(t, w.owner, `UPDATE forge_credentials SET settings = '{"whenBehind":"tell"}' WHERE organization_id = $1`, w.org)
	wi := w.reviewing()
	w.gh.AdvanceBase("main", "Someone else's work")
	w.until("falling behind recorded in the task's feed", func() bool {
		w.sync()
		return w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'pull_request.mergeable_changed'
			AND payload->>'to' = 'behind' AND (payload->>'behindBy')::int = 1`, wi) == 1
	})
	for range 3 {
		w.sync()
	}
	if n := w.count(`SELECT count(*) FROM pull_requests WHERE task_id = $1 AND mergeable_state = 'behind' AND behind_by = 1`, wi); n != 1 {
		t.Error("the pull request is not recorded 1 behind")
	}
	if n := w.updateCalls(); n != 0 {
		t.Errorf("updated %d times; the organization asked only to be told", n)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'pull_request.action'
		AND payload->>'action' = 'update-branch'`, wi); n != 0 {
		t.Errorf("%d update-branch actions recorded", n)
	}
	if r := w.escalationReason(wi); r != "" || w.taskStatus(wi) != "review" || w.fixes(wi) != 0 {
		t.Errorf("escalated %q, task %s, %d fixes; want waiting in review", r, w.taskStatus(wi), w.fixes(wi))
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
	// The next writer's own commit is made on the merge.
	e.dismissOpenFinding()
	e.must(e.task, "start_phase", `{"phase":"simplify"}`)
	var next string
	e.until("the simplify pushed", func() bool {
		_ = e.owner.QueryRow(context.Background(), `SELECT COALESCE(push_result->'results'->0->>'commit', '') FROM runs
			WHERE task_id = $1 AND phase = 'simplify' AND status IN ('completed', 'failed')`, e.task).Scan(&next)
		return next != ""
	})
	if got := e.gh.Parents(next); len(got) == 0 || got[0] != m {
		t.Errorf("the simplify's commit %.8s has parents %v, want the merge %.8s first", next, got, m)
	}
}

// The task branch moves again between the conductor's publish judging a
// base merge under it and GitHub merging its commit in: GitHub merges onto
// whatever is there. Onto a person's commit, the merge is not adopted, and
// the publish is refused naming the merge and its parents; onto another
// of GitHub's base merges, it is adopted, merged into that.
func TestAConductorsMergeOntoABranchThatMovedAgainIsJudgedAgain(t *testing.T) {
	t.Run("onto a person's commit", func(t *testing.T) {
		e, sha := newCommitted(t)
		landed := e.underTheWriter(e.branch, func() string { return e.baseMerge("BASE.md") })
		again, moved := landOnce(isMergesCall, func(*http.Request) string {
			return e.gh.CommitFiles(e.branch, "A person's own fix", map[string]string{"PERSON.md": "mine\n"})
		})
		e.underTheWriterAnd(again)
		id := e.publish()
		status, why := e.published(id)
		x, p, m := landed(), moved(), e.gh.SHA(e.branch)
		if got := e.gh.Parents(m); x == "" || p == "" || !slices.Equal(got, []string{p, sha}) {
			t.Fatalf("the branch's head %.8s has parents %v, want the person's %.8s then the conductor's %.8s", m, got, p, sha)
		}
		if status != delivery.PublishRefused || !strings.Contains(why, m) || !strings.Contains(why, p) || !strings.Contains(why, sha) {
			t.Fatalf("publish %s: %q, want refused naming the merge %s, its parents %s and %s", status, why, m, p, sha)
		}
		// What was found, then what dude's merge would have been.
		found := "GitHub merge " + m + " has parents [" + p + " " + sha + "], expected first parent " + x +
			" or a merge of main on it, and second parent the writer's commit " + sha + ";"
		if !strings.Contains(why, found) {
			t.Errorf("refusal %q, want it to say %q", why, found)
		}
		if n := e.count(`SELECT count(*) FROM conductor_publishes WHERE id = $1 AND moves->'target'->>'status' = 'refused'
			AND strpos(moves->'target'->>'error', $2) > 0`, id, m); n != 1 {
			t.Error("the move's refusal does not name the merge")
		}
		e.recordedNothing(id, sha, m)
	})
	t.Run("onto another of GitHub's base merges", func(t *testing.T) {
		e, sha := newCommitted(t)
		landed := e.underTheWriter(e.branch, func() string { return e.baseMerge("BASE.md") })
		again, _ := landOnce(isMergesCall, func(*http.Request) string { return e.baseMerge("BASE2.md") })
		e.underTheWriterAnd(again)
		id := e.publish()
		if status, why := e.published(id); status != delivery.PublishPublished {
			t.Fatalf("publish %s: %s", status, why)
		}
		m := e.gh.SHA(e.branch)
		y := e.gh.Parents(m)
		if len(y) != 2 || y[1] != sha || y[0] == landed() || e.gh.Parents(y[0])[0] != landed() {
			t.Fatalf("the branch's head %.8s has parents %v, want the second base merge on %.8s, then %.8s", m, y, landed(), sha)
		}
		if n := e.count(`SELECT count(*) FROM conductor_publishes WHERE id = $1 AND heads->'target'->>'sha' = $2
			AND moves->'target'->>'mergedInto' = $3`, id, m, y[0]); n != 1 {
			t.Errorf("the publish did not record the merge %.8s merged into its actual first parent %.8s", m, y[0])
		}
		if n := e.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'git.commit_created' AND payload->>'publishId' = $2
			AND payload->>'headSha' = $3 AND payload->'mergedBase'->>'into' = $4`, e.task, id, m, y[0]); n != 1 {
			t.Errorf("%d commit events saying the conductor's commit was merged into %.8s", n, y[0])
		}
	})
}

// The conductor's merge lands on GitHub and its answer is lost: the retry
// finds it on the branch and adopts it as the publish's, as dude attempted
// it, sending no second merge. A merge of the conductor's commit dude
// never attempted is not taken as dude's.
func TestAConductorsMergeWhoseAnswerWasLostIsAdoptedOnRetry(t *testing.T) {
	t.Run("dude's own", func(t *testing.T) {
		e, sha := newCommitted(t)
		landed := e.underTheWriter(e.branch, func() string { return e.baseMerge("BASE.md") })
		var merges atomic.Int64
		e.underTheWriterAnd(func(r *http.Request) int {
			if isMergesCall(r) {
				merges.Add(1)
			}
			return 0
		})
		e.gh.Set(func(s *fakegithub.Server) { s.LoseMergeResponses = 1 })
		id := e.publish()
		e.until("merged, its answer lost", func() bool {
			return merges.Load() == 1 && e.count(`SELECT count(*) FROM conductor_publishes WHERE id = $1 AND status = 'moving'
				AND (moves->'target'->>'mergeAttempted')::bool AND failures > 0`, id) == 1
		})
		e.due(id)
		if status, why := e.published(id); status != delivery.PublishPublished {
			t.Fatalf("publish %s: %s", status, why)
		}
		x, m := landed(), e.gh.SHA(e.branch)
		if got := e.gh.Parents(m); !slices.Equal(got, []string{x, sha}) {
			t.Fatalf("the branch's head %.8s has parents %v, want %.8s then %.8s", m, got, x, sha)
		}
		if n := merges.Load(); n != 1 {
			t.Errorf("%d merge requests, want 1", n)
		}
		if n := e.count(`SELECT count(*) FROM conductor_publishes WHERE id = $1 AND heads->'target'->>'sha' = $2
			AND moves->'target'->>'mergedInto' = $3`, id, m, x); n != 1 {
			t.Error("the publish's head is not its merge, merged into the base merge")
		}
		if n := e.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND state->'heads'->>'target' = $2`, e.task, m); n != 1 {
			t.Error("the delivery's head is not the merge")
		}
	})
	// Someone merged the conductor's commit into the task branch on GitHub
	// themselves, over a base merge.
	merged := func(e *editing, sha string) string {
		e.baseMerge("BASE.md")
		return e.gh.MergeCommit(e.branch, sha)
	}
	t.Run("someone else's, found on a retry", func(t *testing.T) {
		// The first move is answered 502 after their merge landed: the retry
		// finds the commit in the branch and records the commit, not their
		// merge, as the publish's head.
		e, sha := newCommitted(t)
		hook, landed := landOnce(isMoveOf(e.branch), func(*http.Request) string { return merged(e, sha) })
		var lost atomic.Bool
		e.onGitHub(hook, func(r *http.Request) int {
			if isMoveOf(e.branch)(r) && !lost.Swap(true) {
				return http.StatusBadGateway
			}
			return 0
		})
		id := e.publish()
		e.until("the move's answer lost", func() bool {
			return lost.Load() && e.count(`SELECT count(*) FROM conductor_publishes WHERE id = $1 AND status = 'moving' AND failures > 0`, id) == 1
		})
		e.due(id)
		if status, why := e.published(id); status != delivery.PublishPublished {
			t.Fatalf("publish %s: %s", status, why)
		}
		if landed() == "" {
			t.Fatal("their merge never landed")
		}
		e.notAdopted(id, sha, landed())
	})
	t.Run("someone else's, as the publish moves", func(t *testing.T) {
		e, sha := newCommitted(t)
		landed := e.underTheWriter(e.branch, func() string { return merged(e, sha) })
		id := e.publish()
		status, why := e.published(id)
		if m := landed(); m == "" || e.gh.SHA(e.branch) != m {
			t.Fatalf("the branch at %.8s, want their merge %.8s", e.gh.SHA(e.branch), m)
		}
		if status != delivery.PublishRefused || !strings.Contains(why, "the task branch moved meanwhile") {
			t.Fatalf("publish %s: %q, want refused as the task branch moved", status, why)
		}
		e.recordedNothing(id, sha, landed())
	})
}

// A conductor's move reserved before moves named their base branch: the
// base branch is the task repository's default, and its commit is merged
// in over a base merge as any other's.
func TestAConductorsMoveWithNoBaseBranchRecordedIsMergedInOverABaseMerge(t *testing.T) {
	e, sha := newCommitted(t)
	var id string
	landed := e.underTheWriter(e.branch, func() string {
		// As a row an earlier version reserved: no baseBranch.
		mustExec(t, e.owner, `UPDATE conductor_publishes SET moves = moves #- '{target,baseBranch}' WHERE task_id = $1 AND status = 'moving'`, e.task)
		return e.baseMerge("BASE.md")
	})
	var lost atomic.Bool
	e.underTheWriterAnd(func(r *http.Request) int {
		if isMoveOf(e.branch)(r) && !lost.Swap(true) {
			// That earlier version's move: sent, its answer lost.
			return http.StatusBadGateway
		}
		return 0
	})
	_ = landed
	id = e.publish()
	e.until("the first move's answer lost", func() bool {
		return lost.Load() && e.count(`SELECT count(*) FROM conductor_publishes WHERE id = $1 AND status = 'moving'
			AND NOT moves->'target' ? 'baseBranch' AND failures > 0`, id) == 1
	})
	e.due(id)
	if status, why := e.published(id); status != delivery.PublishPublished {
		t.Fatalf("publish %s: %s", status, why)
	}
	x, m := landed(), e.gh.SHA(e.branch)
	if got := e.gh.Parents(m); x == "" || !slices.Equal(got, []string{x, sha}) {
		t.Fatalf("the branch's head %.8s has parents %v, want GitHub's merge %.8s then the conductor's %.8s", m, got, x, sha)
	}
	if n := e.count(`SELECT count(*) FROM conductor_publishes WHERE id = $1 AND heads->'target'->>'sha' = $2`, id, m); n != 1 {
		t.Error("the publish's head is not the merge")
	}
}

// The conductor's commit conflicts with a base merge that landed under
// it: GitHub refuses the merge (409), the publish is refused saying how to
// take the branch in, and no head is recorded.
func TestAConductorsPublishConflictingWithABaseMergeIsRefused(t *testing.T) {
	e, sha := newCommitted(t)
	landed := e.underTheWriter(e.branch, func() string {
		e.gh.CommitFiles("main", "Someone else's work", map[string]string{"README.md": "# target\n\nthe base's\n"})
		return e.gh.MergeInto(e.branch, "main", nil)
	})
	id := e.publish()
	status, why := e.published(id)
	x := landed()
	if x == "" || e.gh.SHA(e.branch) != x {
		t.Fatalf("the branch at %.8s, want GitHub's merge %.8s", e.gh.SHA(e.branch), x)
	}
	if status != delivery.PublishRefused || !strings.Contains(why, "main was merged into the task branch meanwhile") ||
		!strings.Contains(why, "conflicts with that merge") || !strings.Contains(why, "`git merge lux/"+e.branch+"`, then publish again") {
		t.Fatalf("publish %s: %q, want refused naming the base merge and how to take it in", status, why)
	}
	e.recordedNothing(id, sha, x)
}

// baseMerge commits path on main and merges main into the task branch on
// GitHub, as its Update branch button does; the merge.
func (e *editing) baseMerge(path string) string {
	e.t.Helper()
	e.gh.CommitFiles("main", "Someone else's work", map[string]string{path: "the base's\n"})
	x := e.gh.MergeInto(e.branch, "main", nil)
	if x == "" {
		e.t.Fatal("no base merge")
	}
	return x
}

// underTheWriterAnd keeps the hook underTheWriter set, and adds hooks
// after it.
func (e *editing) underTheWriterAnd(hooks ...func(r *http.Request) int) {
	var first func(r *http.Request) int
	e.gh.Set(func(s *fakegithub.Server) { first = s.Intercept })
	e.onGitHub(append([]func(r *http.Request) int{first}, hooks...)...)
}

// recordedNothing: the publish recorded no head, neither the conductor's
// commit nor head, for the delivery or as a commit event.
func (e *editing) recordedNothing(id, sha, head string) {
	e.t.Helper()
	if n := e.count(`SELECT count(*) FROM conductor_publishes WHERE id = $1 AND heads IS NULL`, id); n != 1 {
		e.t.Error("the refused publish recorded heads")
	}
	if n := e.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'git.commit_created' AND payload->>'publishId' = $2`, e.task, id); n != 0 {
		e.t.Errorf("%d commit events for the refused publish", n)
	}
	if n := e.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND state->'heads'->>'target' IN ($2, $3)`, e.task, sha, head); n != 0 {
		e.t.Error("the delivery's head moved to the refused publish's")
	}
}

// notAdopted: the publish recorded the conductor's commit as its head,
// not the merge someone else made of it.
func (e *editing) notAdopted(id, sha, merge string) {
	e.t.Helper()
	if got := e.gh.SHA(e.branch); got != merge {
		e.t.Fatalf("the branch at %.8s, want their merge %.8s kept", got, merge)
	}
	if n := e.count(`SELECT count(*) FROM conductor_publishes WHERE id = $1 AND heads->'target'->>'sha' = $2
		AND NOT moves->'target' ? 'mergedHead'`, id, sha); n != 1 {
		e.t.Error("the publish's head is not the conductor's commit")
	}
	if n := e.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'git.commit_created' AND payload->>'publishId' = $2
		AND (payload->>'headSha' = $3 OR payload ? 'mergedBase')`, e.task, id, merge); n != 0 {
		e.t.Error("their merge was recorded as the conductor's")
	}
}

// underTheWriter has land run once, on GitHub, as the first move of branch
// arrives: what lands on the task branch while a writer is at work, before
// its publish. Returns what land returned, once it has.
func (w *world) underTheWriter(branch string, land func() string) func() string {
	w.t.Helper()
	hook, landed := landOnce(isMoveOf(branch), func(*http.Request) string { return land() })
	w.onGitHub(hook)
	return landed
}

// onGitHub has hooks see each API request before the fake GitHub answers
// it, in order; the first to return a status fails the request with it.
func (w *world) onGitHub(hooks ...func(r *http.Request) int) {
	w.gh.Set(func(s *fakegithub.Server) {
		s.Intercept = func(r *http.Request) int {
			for _, h := range hooks {
				if status := h(r); status != 0 {
					return status
				}
			}
			return 0
		}
	})
}

// landOnce is a hook that runs land on the first request when matches,
// and what land returned once it has ("" before).
func landOnce(when func(r *http.Request) bool, land func(r *http.Request) string) (func(r *http.Request) int, func() string) {
	var once sync.Once
	var landed atomic.Pointer[string]
	hook := func(r *http.Request) int {
		if when(r) {
			once.Do(func() { sha := land(r); landed.Store(&sha) })
		}
		return 0
	}
	return hook, func() string {
		if p := landed.Load(); p != nil {
			return *p
		}
		return ""
	}
}

// isMoveOf: a fast-forward of branch (PATCH git/refs/heads/<branch>).
func isMoveOf(branch string) func(r *http.Request) bool {
	return func(r *http.Request) bool {
		return r.Method == http.MethodPatch && strings.HasSuffix(r.URL.Path, "/git/refs/heads/"+branch)
	}
}

// isMergesCall: POST /merges, a writer merging its commit in on GitHub.
func isMergesCall(r *http.Request) bool {
	return r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/merges")
}

// movedTo is the commit a fast-forward request asks for, its body left
// for the fake to read.
func movedTo(r *http.Request) string {
	body, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(body))
	var in struct {
		SHA string `json:"sha"`
	}
	_ = json.Unmarshal(body, &in)
	return in.SHA
}

// mergeOf is the commit a POST /merges request asks to merge in, its body
// left for the fake to read.
func mergeOf(r *http.Request) string {
	body, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(body))
	var in struct {
		Head string `json:"head"`
	}
	_ = json.Unmarshal(body, &in)
	return in.Head
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
