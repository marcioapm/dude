package orchestrator_test

// The refresh of a promoted fix's pull requests: GitHub failing it is
// tried again, durably and at most twice more, only for the pull requests
// it failed, and never moves a branch again.

import (
	"context"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakeagent"
	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

var bodyPatch = regexp.MustCompile(`/issues/\d+$`)

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

func (w *prFixWorld) verificationEvents(repo string) (ok, failed int) {
	q := `SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'pull_request.verification_updated'
		AND payload->>'repo' = $2 AND cursor > (SELECT min(cursor) FROM events WHERE task_id = $1
		AND event_type = 'pull_request.fix_moved') AND payload ? 'error' = $3`
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
		return w.stateStep() == "awaitPullRequest" && w.taskStatus(w.wi) == "review"
	})
	if r := w.escalationReason(w.wi); r != "" {
		w.t.Errorf("escalated %q", r)
	}
	if w.state("promotedRefresh") != "" {
		w.t.Errorf("the refresh is still pending: %s", w.state("promotedRefresh"))
	}
}

// GitHub fails the promoted fix's description update twice: the third
// try, after a durable wait each, brings it up to the head it shows.
func TestAPromotedRefreshGitHubFailsTwiceIsTriedAgainAndLands(t *testing.T) {
	w := newPRFixWorld(t)
	var armed atomic.Bool
	patches := bodyPatches(w.gh, &armed, 2)
	armed.Store(true)
	w.comment("Please rename the greeting.")
	w.untilTheFixIsOnThePullRequest()
	w.elapseRefreshWait(w.wi, 1)
	w.elapseRefreshWait(w.wi, 2)
	w.settledBackOnThePullRequest()
	head := w.gh.SHA(w.branch)
	body := w.gh.Pull(1).Body
	if !strings.Contains(body, "Tested at `"+head[:7]+"`.") || strings.Count(body, "<!-- dude:review -->") != 1 {
		t.Errorf("the description is not about %s:\n%s", head[:7], body)
	}
	if n := patches.Load(); n != 3 {
		t.Errorf("%d description updates, want 3", n)
	}
	if ok, failed := w.verificationEvents("target"); ok != 1 || failed != 0 {
		t.Errorf("recorded %d updates and %d failures, want the one that landed", ok, failed)
	}
	if n := w.moves(w.wi); n != 1 {
		t.Errorf("%d moves recorded", n)
	}
}

// GitHub fails it every time: after the third try the failure is recorded
// once and the delivery goes on, with no fourth.
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
	if n := patches.Load(); n != 3 {
		t.Errorf("%d description updates, want 3", n)
	}
	if ok, failed := w.verificationEvents("target"); ok != 0 || failed != 1 {
		t.Errorf("recorded %d updates and %d failures, want the last failure once", ok, failed)
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
	if n := patches.Load(); n != 3 {
		t.Errorf("%d description updates, want 3", n)
	}
	if ok, failed := w.verificationEvents("target"); ok != 0 || failed != 1 {
		t.Errorf("recorded %d updates and %d failures, want the last failure once", ok, failed)
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

// Of two pull requests the promotion moved, GitHub fails one's update
// once: only that one is tried again.
func TestAPromotedRefreshRetriesOnlyThePullRequestItFailed(t *testing.T) {
	w := newPRFixWorldIn(t, true)
	w.play = func(phase string, n int, b *fakelux.Behaviour) {
		if phase == "fix" && n == 1 {
			b.Commit = map[string]string{"*:" + fakeagent.FixedFile: "addressed for both"}
		}
	}
	var armed atomic.Bool
	target := bodyPatches(w.gh, &armed, 0)
	web := bodyPatches(w.web, &armed, 1)
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
	if n := target.Load(); n != 1 {
		t.Errorf("target's description updated %d times, want once", n)
	}
	if n := web.Load(); n != 2 {
		t.Errorf("web's description updated %d times, want the failure and its retry", n)
	}
	for repo, server := range map[string]*fakegithub.Server{"target": w.gh, "web": w.web} {
		if ok, failed := w.verificationEvents(repo); ok != 1 || failed != 0 {
			t.Errorf("%s: recorded %d updates and %d failures, want one update", repo, ok, failed)
		}
		head := server.SHA(w.branch)
		if !strings.Contains(server.Pull(1).Body, repo+" `"+head[:7]+"`") {
			t.Errorf("%s's description is not about %s:\n%s", repo, head[:7], server.Pull(1).Body)
		}
	}
}

// A fix promoted to target alone: web's pull request, which the fix did not
// move, is refreshed too, since its Review and Verification are about both
// heads. GitHub fails web's first update; it is tried again and lands.
func TestAPromotionRefreshesASiblingPullRequestItDidNotMove(t *testing.T) {
	w := newPRFixWorldIn(t, true)
	var armed atomic.Bool
	target := bodyPatches(w.gh, &armed, 0)
	web := bodyPatches(w.web, &armed, 1)
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
	if n := target.Load(); n != 1 {
		t.Errorf("target's description updated %d times, want once", n)
	}
	if n := web.Load(); n != 2 {
		t.Errorf("web's description updated %d times, want the failure and its retry", n)
	}
	moved := w.gh.SHA(w.branch)
	want := "Tested at target `" + moved[:7] + "`, web `" + w.webBefore[:7] + "`."
	for repo, server := range map[string]*fakegithub.Server{"target": w.gh, "web": w.web} {
		if ok, failed := w.verificationEvents(repo); ok != 1 || failed != 0 {
			t.Errorf("%s: recorded %d updates and %d failures, want one update", repo, ok, failed)
		}
		if body := server.Pull(1).Body; !strings.Contains(body, want) || strings.Contains(body, "**Stale:**") {
			t.Errorf("%s's description is not about the promoted heads (%s):\n%s", repo, want, body)
		}
	}
}

// The orchestrator restarts while the refresh waits, and a run of the
// refresh is lost before its transition commits: each time the refresh
// alone runs again; the promotion's move is made and recorded once.
func TestARestartDuringAPromotedRefreshRetryDoesNotPromoteAgain(t *testing.T) {
	w := newPRFixWorld(t)
	var armed atomic.Bool
	var refPatches atomic.Int32
	var bodies atomic.Int32
	w.gh.Set(func(s *fakegithub.Server) {
		s.Intercept = func(r *http.Request) int {
			if !armed.Load() || r.Method != "PATCH" {
				return 0
			}
			if strings.HasSuffix(r.URL.Path, "/git/refs/heads/"+w.branch) {
				refPatches.Add(1)
			}
			if bodyPatch.MatchString(r.URL.Path) && bodies.Add(1) <= 2 {
				return http.StatusBadGateway
			}
			return 0
		}
	})
	armed.Store(true)
	w.comment("Please rename the greeting.")
	w.untilTheFixIsOnThePullRequest()
	w.elapseRefreshWait(w.wi, 1)

	// A restart: another orchestrator process, its own poller, from what
	// the database holds. Its first run of the refresh is lost before it
	// commits, as a crash mid-step loses it.
	w.runtime = workflow.New(w.app, "restarted", w.log)
	def := delivery.Workflow(&delivery.Store{DB: w.app}, forge.Resolver{DB: w.app})
	var mu sync.Mutex
	var ran []string
	var lost atomic.Bool
	def.BeforeCommit = func(_, step, next string) {
		mu.Lock()
		ran = append(ran, step)
		mu.Unlock()
		if step == "refreshPromoted" && lost.CompareAndSwap(false, true) {
			mustExec(t, w.owner, `UPDATE workflow_runs SET locked_until = now() - interval '1 second' WHERE task_id = $1`, w.wi)
		}
	}
	w.runtime.Register(def)
	// The lost run's failure (the second) is not recorded; its replay, at
	// once and from the one wait recorded, is the third update, which lands.
	w.pump()
	if !lost.Load() || w.state("promotedRefresh") != "" {
		t.Fatalf("the lost run's transition landed or was not replayed at once: %s", w.state("promotedRefresh"))
	}
	w.settledBackOnThePullRequest()
	if bodies.Load() != 3 {
		t.Errorf("%d description updates; want the lost one replayed, 3 updates", bodies.Load())
	}
	mu.Lock()
	steps := slices.Clone(ran)
	mu.Unlock()
	if len(steps) < 2 || steps[0] != "refreshPromoted" || steps[1] != "refreshPromoted" {
		t.Errorf("after the restart the steps ran %v, want the refresh, lost, then again", steps)
	}
	for _, s := range steps {
		if s == "promoteCandidate" {
			t.Errorf("the promotion ran again after the restart: %v", steps)
		}
	}
	if n := refPatches.Load(); n != 1 {
		t.Errorf("the pull request's branch was moved %d times", n)
	}
	if n := w.moves(w.wi); n != 1 {
		t.Errorf("%d moves recorded", n)
	}
	head := w.gh.SHA(w.branch)
	if body := w.gh.Pull(1).Body; !strings.Contains(body, "Tested at `"+head[:7]+"`.") {
		t.Errorf("the description is not about %s:\n%s", head[:7], body)
	}
}

// An adopted pull request's verdict comment GitHub fails to edit after a
// promotion is edited again, in place: one comment, at the promoted head,
// and one record of it.
func TestAnAdoptedVerdictCommentAPromotionFailedToEditIsEditedAgainInPlace(t *testing.T) {
	w := newWorld(t)
	wi, n := w.testingAdopted("fake/scripted")
	first := w.verdictComments(wi, n)
	if len(first) != 1 {
		t.Fatalf("verdict comments %+v, want one", first)
	}
	seen := len(w.verdictEvents(wi))
	moved := w.moves(wi)
	var edits atomic.Int32
	w.gh.Set(func(s *fakegithub.Server) {
		s.Intercept = func(r *http.Request) int {
			if r.Method == "PATCH" && strings.Contains(r.URL.Path, "/issues/comments/") && edits.Add(1) <= 2 {
				return http.StatusBadGateway
			}
			return 0
		}
	})
	fixed := w.prFixPushed(wi)
	w.elapseRefreshWait(wi, 1)
	w.elapseRefreshWait(wi, 2)
	w.until("the verdict at the fixed head", func() bool {
		got := w.verdictComments(wi, n)
		return len(got) == 1 && strings.Contains(got[0].Body, "Tested at `"+fixed[:7]+"`.")
	})
	if got := w.verdictComments(wi, n); len(got) != 1 || got[0].ID != first[0].ID {
		t.Errorf("verdict comments %+v, want comment %d edited in place", got, first[0].ID)
	}
	if e := edits.Load(); e != 3 {
		t.Errorf("%d edits, want 3", e)
	}
	events := w.verdictEvents(wi)[seen:]
	if len(events) != 1 || events[0]["error"] != nil || events[0]["edited"] != true {
		t.Errorf("verdict events after the move %v, want the one edit", events)
	}
	w.verdictOnlyAfterTheMove(wi, seen, moved)
}
