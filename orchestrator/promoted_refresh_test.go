package orchestrator_test

// The refresh of a fix's pull requests once it is on them: GitHub failing
// it is tried again, durably and at most twice more, only for the pull
// requests it failed, and never moves a branch again. A direct fix's push
// marks the verification stale at once (RefreshVerification, not retried);
// its narrow loop done (prFixDone, movedOnPR) brings review and
// verification up to the head (refreshPromoted, retried). These tests fail
// GitHub from the push's own update, so the refresh has a body to write.

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/fakeagent"
	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

var bodyPatch = regexp.MustCompile(`/issues/\d+$`)

// stateStep is the delivery's workflow step.
func (w *prFixWorld) stateStep() string {
	w.t.Helper()
	var step string
	if err := w.owner.QueryRow(context.Background(), `SELECT step FROM workflow_runs WHERE task_id = $1`, w.wi).Scan(&step); err != nil {
		w.t.Fatal(err)
	}
	return step
}

// bodyPatches counts gh's pull request description PATCHes once armed,
// failing the first fail of them with a 502.
func bodyPatches(gh *fakegithub.Server, armed *atomic.Bool, fail int32) *atomic.Int32 {
	var n atomic.Int32
	gh.Set(func(s *fakegithub.Server) {
		s.Intercept = func(r *http.Request) int {
			if !armed.Load() || r.Method != "PATCH" || !bodyPatch.MatchString(r.URL.Path) {
				return 0
			}
			if n.Add(1) <= fail {
				return http.StatusBadGateway
			}
			return 0
		}
	})
	return &n
}

// refreshWait is the delivery parked on a retry of the promoted refresh:
// its tries so far, and how far off its wake is.
func (w *world) refreshWait(wi string) (tries int, wake time.Duration, ok bool) {
	var waiting bool
	var secs float64
	_ = w.owner.QueryRow(context.Background(), `SELECT COALESCE((state->'promotedRefresh'->>'tries')::int, 0),
		status = 'waiting' AND step = 'refreshPromoted', COALESCE(EXTRACT(EPOCH FROM wake_at - now()), 0)
		FROM workflow_runs WHERE task_id = $1`, wi).Scan(&tries, &waiting, &secs)
	return tries, time.Duration(secs * float64(time.Second)), waiting
}

// elapseRefreshWait checks the delivery waits durably on its next refresh,
// at least forge's minimum wait, then lets that wait elapse.
func (w *world) elapseRefreshWait(wi string, tries int) {
	w.t.Helper()
	w.until("a durable wait on the refresh", func() bool { _, _, ok := w.refreshWait(wi); return ok })
	got, wake, _ := w.refreshWait(wi)
	if got != tries || wake < 2*time.Second {
		w.t.Fatalf("waiting on the refresh after %d tries, %s off; want %d tries, ~5s", got, wake, tries)
	}
	mustExec(w.t, w.owner, `UPDATE workflow_runs SET wake_at = now() WHERE task_id = $1`, wi)
}

// verificationEvents counts the description updates recorded for repo
// since the person's comment that started the fix: the fix's push marks
// the verification stale (RefreshVerification, not retried), then its
// narrow loop done (prFixDone) refreshes review and verification
// (refreshPromoted, retried).
func (w *prFixWorld) verificationEvents(repo string) (ok, failed int) {
	q := `SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'pull_request.verification_updated'
		AND payload->>'repo' = $2 AND cursor > (SELECT min(cursor) FROM events WHERE task_id = $1
		AND event_type = 'pull_request.commented') AND payload ? 'error' = $3`
	return w.count(q, w.wi, repo, false), w.count(q, w.wi, repo, true)
}

func (w *prFixWorld) untilTheFixIsOnThePullRequest() {
	w.agentsDone(w.wi, "the fix on the pull request", func() bool { return w.gh.SHA(w.branch) != w.before })
}

// moves counts the task's recorded moves of a fix onto its pull requests.
func (w *world) moves(wi string) int {
	return w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'pull_request.fix_moved'`, wi)
}

// settledBackOnThePullRequest: the delivery is waiting on its pull
// requests again, nobody asked.
func (w *prFixWorld) settledBackOnThePullRequest() {
	w.t.Helper()
	w.until("waiting on the pull request", func() bool {
		w.sync()
		// In review, or ready to merge: the gate may pass at once.
		s := w.taskStatus(w.wi)
		return w.stateStep() == "awaitPullRequest" && (s == "review" || s == "ready_to_merge")
	})
	if r := w.escalationReason(w.wi); r != "" {
		w.t.Errorf("escalated %q", r)
	}
	if w.state("promotedRefresh") != "" {
		w.t.Errorf("the refresh is still pending: %s", w.state("promotedRefresh"))
	}
}

// GitHub fails the fix's push's description update and the refresh's
// first two: the third try, after a durable wait each, brings it up to
// the head it shows (its test stale there, a direct fix not tested again).
func TestAPromotedRefreshGitHubFailsTwiceIsTriedAgainAndLands(t *testing.T) {
	w := newPRFixWorld(t)
	var armed atomic.Bool
	patches := bodyPatches(w.gh, &armed, 3)
	armed.Store(true)
	w.comment("Please rename the greeting.")
	w.untilTheFixIsOnThePullRequest()
	w.elapseRefreshWait(w.wi, 1)
	w.elapseRefreshWait(w.wi, 2)
	w.settledBackOnThePullRequest()
	body := w.gh.Pull(1).Body
	if !strings.Contains(body, "Tested at `"+w.before[:7]+"`. **Stale:**") || strings.Count(body, "<!-- dude:review -->") != 1 {
		t.Errorf("the description does not say the test at %s is stale:\n%s", w.before[:7], body)
	}
	if n := patches.Load(); n != 4 {
		t.Errorf("%d description updates, want 4: the push's, and the refresh's three tries", n)
	}
	// The push's failure is recorded (it is not retried); the refresh's
	// two failures are not, only its update that landed.
	if ok, failed := w.verificationEvents("target"); ok != 1 || failed != 1 {
		t.Errorf("recorded %d updates and %d failures, want the push's failure and the update that landed", ok, failed)
	}
	if n := w.moves(w.wi); n != 0 {
		t.Errorf("%d promotions recorded for a direct fix", n)
	}
}

// GitHub fails it every time: after the refresh's third try its failure is
// recorded once (beside the push's own) and the delivery goes on, with no
// fourth.
func TestAPromotedRefreshGitHubKeepsFailingIsRecordedAndLeftAfterThreeTries(t *testing.T) {
	w := newPRFixWorld(t)
	var armed atomic.Bool
	patches := bodyPatches(w.gh, &armed, 1<<30)
	armed.Store(true)
	before := w.gh.Pull(1).Body
	w.comment("Please rename the greeting.")
	w.untilTheFixIsOnThePullRequest()
	w.elapseRefreshWait(w.wi, 1)
	w.elapseRefreshWait(w.wi, 2)
	w.settledBackOnThePullRequest()
	// Nothing left to wake it for another.
	for range 3 {
		mustExec(t, w.owner, `UPDATE workflow_runs SET wake_at = now() WHERE task_id = $1 AND wake_at IS NOT NULL`, w.wi)
		w.pump()
	}
	if n := patches.Load(); n != 4 {
		t.Errorf("%d description updates, want 4: the push's, and the refresh's three tries", n)
	}
	if ok, failed := w.verificationEvents("target"); ok != 0 || failed != 2 {
		t.Errorf("recorded %d updates and %d failures, want the push's failure and the refresh's last once", ok, failed)
	}
	if body := w.gh.Pull(1).Body; body != before {
		t.Errorf("the description changed though every update failed:\n%s", body)
	}
	if w.stateStep() != "awaitPullRequest" {
		t.Errorf("at %s, want waiting on the pull request", w.stateStep())
	}
}

// The step after the last failed try is lost before it commits: only that
// step runs again; the last try is neither repeated nor recorded twice.
func TestAPromotedRefreshsLastFailureIsNotTriedAgainWhenTheNextStepIsLost(t *testing.T) {
	w := newPRFixWorld(t)
	var lost atomic.Bool
	gate := w.gated(func(step, next string) {
		if step == "refreshPromoted" && next != "refreshPromoted" && lost.CompareAndSwap(false, true) {
			mustExec(w.t, w.owner, `UPDATE workflow_runs SET locked_until = now() - interval '1 second' WHERE task_id = $1`, w.wi)
		}
	})
	var armed atomic.Bool
	patches := bodyPatches(w.gh, &armed, 1<<30)
	armed.Store(true)
	w.comment("Please rename the greeting.")
	w.untilTheFixIsOnThePullRequest()
	w.elapseRefreshWait(w.wi, 1)
	w.elapseRefreshWait(w.wi, 2)
	gate.Store(true)
	w.settledBackOnThePullRequest()
	if !lost.Load() {
		t.Fatal("no step after the refresh was lost")
	}
	if n := patches.Load(); n != 4 {
		t.Errorf("%d description updates, want 4: the push's, and the refresh's three tries", n)
	}
	if ok, failed := w.verificationEvents("target"); ok != 0 || failed != 2 {
		t.Errorf("recorded %d updates and %d failures, want the push's failure and the refresh's last once", ok, failed)
	}
}

// The pull request is merged while the refresh waits to retry: the signal
// kept over the wait is handled once the refresh settles, and the task is
// done.
func TestAPullRequestMergedDuringAPromotedRefreshWaitEndsTheTask(t *testing.T) {
	w := newPRFixWorld(t)
	var armed atomic.Bool
	bodyPatches(w.gh, &armed, 1<<30)
	armed.Store(true)
	w.comment("Please rename the greeting.")
	w.untilTheFixIsOnThePullRequest()
	w.elapseRefreshWait(w.wi, 1)
	w.gh.Merge(1)
	w.sync()
	if _, _, ok := w.refreshWait(w.wi); !ok {
		t.Fatal("the merge woke the refresh's wait")
	}
	mustExec(t, w.owner, `UPDATE workflow_runs SET wake_at = now() WHERE task_id = $1`, w.wi)
	w.until("the task to finish", func() bool { w.sync(); return w.taskStatus(w.wi) == "done" })
	if r := w.escalationReason(w.wi); r != "" {
		t.Errorf("escalated %q", r)
	}
}

// Of two pull requests a direct fix moved, GitHub fails one's refresh
// once (after failing its push's stale mark): only that one is tried again.
func TestAPromotedRefreshRetriesOnlyThePullRequestItFailed(t *testing.T) {
	w := newPRFixWorldIn(t, true)
	w.play = func(phase string, n int, b *fakelux.Behaviour) {
		if phase == "fix" && n == 1 {
			b.Commit = map[string]string{"*:" + fakeagent.FixedFile: "addressed for both"}
		}
	}
	var armed atomic.Bool
	target := bodyPatches(w.gh, &armed, 0)
	web := bodyPatches(w.web, &armed, 2)
	armed.Store(true)
	w.comment("Please rename the greeting.")
	w.until("the fix on both pull requests", func() bool {
		w.sync()
		return w.gh.SHA(w.branch) != w.before && w.web.SHA(w.branch) != w.webBefore
	})
	w.elapseRefreshWait(w.wi, 1)
	if repos := w.state("promotedRefresh"); !strings.Contains(repos, `"repos": ["web"]`) {
		t.Errorf("retrying %s, want web alone", repos)
	}
	w.settledBackOnThePullRequest()
	// target's refresh finds the body the push left right but for the
	// simplifier that ran on the fix after it: one PATCH to add its line.
	if n := target.Load(); n != 2 {
		t.Errorf("target's description updated %d times, want the push's and the simplifier's line", n)
	}
	if n := web.Load(); n != 3 {
		t.Errorf("web's description updated %d times, want the push's, the refresh's failure and its retry", n)
	}
	want := map[string][2]int{"target": {2, 0}, "web": {1, 1}}
	for repo, server := range map[string]*fakegithub.Server{"target": w.gh, "web": w.web} {
		if ok, failed := w.verificationEvents(repo); ok != want[repo][0] || failed != want[repo][1] {
			t.Errorf("%s: recorded %d updates and %d failures, want %v", repo, ok, failed, want[repo])
		}
		tested := "Tested at target `" + w.before[:7] + "`, web `" + w.webBefore[:7] + "`. **Stale:**"
		if !strings.Contains(server.Pull(1).Body, tested) {
			t.Errorf("%s's description does not say the test is stale:\n%s", repo, server.Pull(1).Body)
		}
	}
}

// A direct fix to target alone: web's pull request, which the fix did not
// move, is refreshed too, since its Review and Verification are about both
// heads. GitHub fails web's update for the push's stale mark and the
// refresh's first try; it is tried again and lands.
func TestAPromotionRefreshesASiblingPullRequestItDidNotMove(t *testing.T) {
	w := newPRFixWorldIn(t, true)
	var armed atomic.Bool
	target := bodyPatches(w.gh, &armed, 0)
	web := bodyPatches(w.web, &armed, 2)
	armed.Store(true)
	w.comment("Please rename the greeting.")
	w.until("the fix on target's pull request", func() bool { w.sync(); return w.gh.SHA(w.branch) != w.before })
	w.elapseRefreshWait(w.wi, 1)
	if repos := w.state("promotedRefresh"); !strings.Contains(repos, `"repos": ["web"]`) {
		t.Errorf("retrying %s, want web alone", repos)
	}
	w.settledBackOnThePullRequest()
	if at := w.web.SHA(w.branch); at != w.webBefore {
		t.Fatalf("web moved to %s; the fix was to change target alone", at)
	}
	// target's refresh finds the body the push left right but for the
	// simplifier that ran on the fix after it: one PATCH to add its line.
	if n := target.Load(); n != 2 {
		t.Errorf("target's description updated %d times, want the push's and the simplifier's line", n)
	}
	if n := web.Load(); n != 3 {
		t.Errorf("web's description updated %d times, want the push's, the refresh's failure and its retry", n)
	}
	// A direct fix is not tested again: the verdict is the one before it,
	// stale at the head the fix left.
	want := "Tested at target `" + w.before[:7] + "`, web `" + w.webBefore[:7] + "`. **Stale:**"
	recorded := map[string][2]int{"target": {2, 0}, "web": {1, 1}}
	for repo, server := range map[string]*fakegithub.Server{"target": w.gh, "web": w.web} {
		if ok, failed := w.verificationEvents(repo); ok != recorded[repo][0] || failed != recorded[repo][1] {
			t.Errorf("%s: recorded %d updates and %d failures, want %v", repo, ok, failed, recorded[repo])
		}
		if body := server.Pull(1).Body; !strings.Contains(body, want) {
			t.Errorf("%s's description is not about the heads (%s):\n%s", repo, want, body)
		}
	}
}

// An adopted pull request's verdict comment GitHub fails to edit after a
// direct fix is edited again, in place: one comment, marked stale, and one
// record of it.
func TestAnAdoptedVerdictCommentAPromotionFailedToEditIsEditedAgainInPlace(t *testing.T) {
	w := newWorld(t)
	wi, n := w.testingAdopted("fake/scripted")
	first := w.verdictComments(wi, n)
	if len(first) != 1 {
		t.Fatalf("verdict comments %+v, want one", first)
	}
	seen := len(w.verdictEvents(wi))
	var edits atomic.Int32
	w.gh.Set(func(s *fakegithub.Server) {
		s.Intercept = func(r *http.Request) int {
			if r.Method == "PATCH" && strings.Contains(r.URL.Path, "/issues/comments/") && edits.Add(1) <= 2 {
				return http.StatusBadGateway
			}
			return 0
		}
	})
	w.prFixPushed(wi)
	w.elapseRefreshWait(wi, 1)
	w.elapseRefreshWait(wi, 2)
	w.until("the verdict marked stale", func() bool {
		got := w.verdictComments(wi, n)
		return len(got) == 1 && strings.Contains(got[0].Body, "Stale")
	})
	if got := w.verdictComments(wi, n); len(got) != 1 || got[0].ID != first[0].ID {
		t.Errorf("verdict comments %+v, want comment %d edited in place", got, first[0].ID)
	}
	if e := edits.Load(); e != 3 {
		t.Errorf("%d edits, want 3", e)
	}
	events := w.verdictEvents(wi)[seen:]
	if len(events) != 1 || events[0]["error"] != nil || events[0]["edited"] != true {
		t.Errorf("verdict events after the fix %v, want the one edit", events)
	}
}
