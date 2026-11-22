package delivery

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

// prEnded (waits_pr.go): a tracked pull request read from its row; an
// untracked one read by wait_for before its transaction and then by the
// sweep's observation step, against the task's GitHub read budget.

// fakePulls is a GitHub serving pull requests by "owner/repo#N": its JSON
// (absent: 404), counting reads.
type fakePulls struct {
	mu    sync.Mutex
	pulls map[string]map[string]any
	reads atomic.Int32
	srv   *httptest.Server
	// When set, what transactions are open during each read.
	watch atomic.Pointer[txWatch]
}

func newFakePulls(t *testing.T) *fakePulls {
	f := &fakePulls{pulls: map[string]map[string]any{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var owner, repo string
		var n int
		if _, err := fmt.Sscanf(strings.ReplaceAll(r.URL.Path, "/", " "), " repos %s %s pulls %d", &owner, &repo, &n); err != nil {
			http.NotFound(w, r)
			return
		}
		f.reads.Add(1)
		f.watch.Load().see()
		f.mu.Lock()
		p, ok := f.pulls[fmt.Sprintf("%s/%s#%d", owner, repo, n)]
		f.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(p)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakePulls) set(name string, p map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pulls[name] = p
}

func openPull(n int, title string) map[string]any {
	return map[string]any{"number": n, "state": "open", "title": title, "head": map[string]any{"sha": headA}}
}

func mergedPull(n int, title, by string) map[string]any {
	p := openPull(n, title)
	p["state"], p["merged_at"], p["closed_at"] = "closed", "2026-11-21T10:30:00Z", "2026-11-21T10:30:00Z"
	p["merged_by"] = map[string]any{"login": by}
	return p
}

func closedPull(n int, title string) map[string]any {
	p := openPull(n, title)
	p["state"], p["closed_at"] = "closed", "2026-11-21T11:00:00Z"
	return p
}

func (f *fakePulls) forges() Forges {
	return forgeOf{forge.NewGitHub(forge.Credential{Auth: "pat", Secret: "ghp", APIBaseURL: f.srv.URL})}
}

func prEndedCond(pr, how string) WaitCondition {
	return WaitCondition{PREnded: &PREndedAsk{PR: pr, How: how}}
}

// waitForWithReads is wait_for as the tool makes it: the untracked pull
// requests read before the transaction, then the call.
func (w *childWorld) waitForWithReads(f Forges, key string, ask WaitAsk) (WaitOpened, error) {
	w.t.Helper()
	ask.PRReads = PrereadWaitPRs(w.ctx, w.s.DB, f, w.in.Parent.Run, ask.Conditions)
	return w.waitFor(key, ask)
}

// waitOnPR is waitForWithReads on the one prEnded condition.
func (w *childWorld) waitOnPR(f Forges, key, pr, how, deadline string) (WaitOpened, error) {
	w.t.Helper()
	return w.waitForWithReads(f, key, WaitAsk{Conditions: []WaitCondition{prEndedCond(pr, how)}, Deadline: deadline})
}

// endPR moves pr_life the way dude's pull request sync does: its row's
// state, with merged_at or closed_at set as it sets them (prs.go).
func (w *childWorld) endPR(state string) {
	w.t.Helper()
	w.exec(`UPDATE pull_requests SET state = $2::pull_request_state, updated_at = now(),
		merged_at = CASE WHEN $2 = 'merged' THEN COALESCE(merged_at, now()) ELSE merged_at END,
		closed_at = CASE WHEN $2 = 'closed' THEN COALESCE(closed_at, now()) ELSE closed_at END
		WHERE id = $1`, "pr_life", state)
}

func TestWaitForPREndedOnATrackedPullRequest(t *testing.T) {
	for _, tc := range []struct {
		how, state string
		holds      bool
	}{
		{"merged", "merged", true},
		{"merged", "closed", false},
		{"closed", "closed", true},
		{"closed", "merged", false},
		{"any", "merged", true},
		{"any", "closed", true},
		{"", "closed", true},
	} {
		t.Run(tc.how+"/"+tc.state, func(t *testing.T) {
			w := newWaitWorld(t)
			// A GitHub that answers nothing: a tracked pull request is
			// never read from it.
			pulls := newFakePulls(t)
			opened, err := w.waitOnPR(pulls.forges(), "c1", "test/repo#7", tc.how, "1h")
			if err != nil || opened.Holds {
				t.Fatalf("%+v, %v", opened, err)
			}
			w.mustStillWait(opened.WaitID, "open")
			w.endPR(tc.state)
			if tc.holds {
				w.mustHold(opened.WaitID, tc.state)
			} else {
				w.mustStillWait(opened.WaitID, tc.state+" is not "+tc.how)
			}
			if n := w.observe(pulls.forges()); n != 0 || pulls.reads.Load() != 0 || w.events(EvGitHubRead) != 0 {
				t.Fatalf("a tracked pull request was read from GitHub: %d observed, %d reads", n, pulls.reads.Load())
			}
		})
	}
}

// repo#7 and #7 name the project's repository as prChecksFinal's do.
func TestWaitForPREndedResolvesNamesAgainstTheProject(t *testing.T) {
	for _, name := range []string{"repo#7", "#7", "7", "test/repo#7"} {
		w := newWaitWorld(t)
		opened, err := w.waitOnPR(nil, "c1", name, "merged", "1h")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		w.endPR("merged")
		w.mustHold(opened.WaitID, name)
	}
}

func TestWaitForPREndedUntrackedAtTheCall(t *testing.T) {
	w := newWaitWorld(t)
	pulls := newFakePulls(t)
	pulls.set("other/lib#40", mergedPull(40, "Bump ⟦the⟧ parser", "octo"))
	pulls.set("other/lib#41", openPull(41, "Still open"))

	t.Run("already merged holds at once", func(t *testing.T) {
		opened, err := w.waitOnPR(pulls.forges(), "c1", "other/lib#40", "merged", "1h")
		if err != nil || !opened.Holds || opened.WaitID != "" {
			t.Fatalf("%+v, %v", opened, err)
		}
	})
	t.Run("already merged is not closed", func(t *testing.T) {
		opened, err := w.waitOnPR(pulls.forges(), "c1b", "other/lib#40", "closed", "1h")
		if err != nil || opened.Holds || opened.WaitID == "" {
			t.Fatalf("%+v, %v", opened, err)
		}
	})
	t.Run("GitHub cannot see it", func(t *testing.T) {
		before := w.count("conductor_waits")
		_, err := w.waitOnPR(pulls.forges(), "c2", "other/lib#99", "", "1h")
		want := fmt.Sprintf(rWaitPRUnread, "other/lib#99", "GitHub does not show it to this organisation's installation (not found)")
		if !IsRefusal(err) || err.Error() != want {
			t.Fatalf("%v, want %q", err, want)
		}
		if w.count("conductor_waits") != before {
			t.Fatal("a refused call wrote")
		}
	})
	t.Run("open opens a wait and spends one read", func(t *testing.T) {
		reads := w.events(EvGitHubRead)
		opened, err := w.waitOnPR(pulls.forges(), "c3", "other/lib#41", "", "1h")
		if err != nil || opened.Holds || opened.WaitID == "" {
			t.Fatalf("%+v, %v", opened, err)
		}
		if got := w.events(EvGitHubRead) - reads; got != 1 {
			t.Fatalf("%d github.read events, want 1", got)
		}
		if w.obsField(opened.WaitID, 0, "state") != "open" {
			t.Fatalf("obs %s", w.obs(opened.WaitID, 0))
		}
	})
}

func TestPREndedObservation(t *testing.T) {
	w := newWaitWorld(t)
	pulls := newFakePulls(t)
	pulls.set("other/lib#41", openPull(41, "Still open"))
	opened, err := w.waitOnPR(pulls.forges(), "c1", "other/lib#41", "merged", "2h")
	if err != nil || opened.WaitID == "" {
		t.Fatalf("%+v, %v", opened, err)
	}
	read := pulls.reads.Load()
	pulls.set("other/lib#41", mergedPull(41, "Still open", "octo"))
	// Read at the call: not due again for 5 minutes.
	if n := w.observe(pulls.forges()); n != 0 || pulls.reads.Load() != read {
		t.Fatalf("read again within 5 minutes: %d stored, %d reads", n, pulls.reads.Load()-read)
	}
	w.mustStillWait(opened.WaitID, "merged on GitHub, not yet read")
	w.ageObservations(4 * time.Minute)
	if n := w.observe(pulls.forges()); n != 0 || pulls.reads.Load() != read {
		t.Fatal("read again at 4 minutes")
	}
	w.ageObservations(time.Minute)
	spent := w.events(EvGitHubRead)
	watch := w.watchTxs()
	pulls.watch.Store(watch)
	if n := w.observe(pulls.forges()); n != 1 || pulls.reads.Load() != read+1 {
		t.Fatalf("at 5 minutes: %d stored, %d reads", n, pulls.reads.Load()-read)
	}
	pulls.watch.Store(nil)
	watch.mustSeeNone("the observation's read of the pull request")
	if w.events(EvGitHubRead) != spent+1 {
		t.Fatal("the observation's read was not counted")
	}
	if w.obsField(opened.WaitID, 0, "state") != "merged" || w.obsField(opened.WaitID, 0, "merged_by") != "octo" {
		t.Fatalf("obs %s", w.obs(opened.WaitID, 0))
	}
	w.mustHold(opened.WaitID, "merged, read")
	w.evaluate()
	if len(w.waitWakes()) != 1 {
		t.Fatal("held twice")
	}
	// An ended wait is not read again.
	w.ageObservations(time.Hour)
	if n := w.observe(pulls.forges()); n != 0 || pulls.reads.Load() != read+1 {
		t.Fatal("an ended wait was read")
	}
}

// spendBudget records the task's hourly GitHub reads as spent.
func (w *childWorld) spendBudget(n int) {
	w.t.Helper()
	for i := range n {
		w.exec(`INSERT INTO events (id, organization_id, event_type, occurred_at, project_id, task_id, run_id, actor_type, actor_id, source, correlation_id, payload)
			VALUES ($1, $2, $3, now(), 'prj_life', 'wi_life', 'run_parent', 'agent', 'run_parent', 'orchestrator', 'wi_life', '{"tool":"github_search","attempted":true}')`,
			fmt.Sprintf("evt_budget%02d", i), w.org, EvToolAttempted)
	}
}

func TestPREndedObservationWithTheBudgetSpent(t *testing.T) {
	w := newWaitWorld(t)
	pulls := newFakePulls(t)
	pulls.set("other/lib#41", openPull(41, "Still open"))
	opened, err := w.waitOnPR(pulls.forges(), "c1", "other/lib#41", "", "2h")
	if err != nil || opened.WaitID == "" {
		t.Fatalf("%+v, %v", opened, err)
	}
	pulls.set("other/lib#41", mergedPull(41, "Still open", "octo"))
	w.spendBudget(GitHubReadsPerHour)
	read, spent := pulls.reads.Load(), w.events(EvGitHubRead)
	w.ageObservations(10 * time.Minute)
	if n := w.observe(pulls.forges()); n != 1 {
		t.Fatalf("%d stored", n)
	}
	if pulls.reads.Load() != read || w.events(EvGitHubRead) != spent {
		t.Fatal("read with the budget spent")
	}
	if w.obsField(opened.WaitID, 0, "error") != obsBudget {
		t.Fatalf("obs %s", w.obs(opened.WaitID, 0))
	}
	w.mustStillWait(opened.WaitID, "budget spent")
}

func TestWaitForPREndedUntrackedWithTheBudgetSpent(t *testing.T) {
	w := newWaitWorld(t)
	pulls := newFakePulls(t)
	pulls.set("other/lib#41", openPull(41, "Still open"))
	w.spendBudget(GitHubReadsPerHour)
	_, err := w.waitOnPR(pulls.forges(), "c1", "other/lib#41", "", "2h")
	if !IsRefusal(err) || err.Error() != fmt.Sprintf(rWaitPRBudget, "other/lib#41", GitHubReadsPerHour) || pulls.reads.Load() != 0 {
		t.Fatalf("%v (%d reads)", err, pulls.reads.Load())
	}
}

func TestWaitForPREndedRefusals(t *testing.T) {
	w := newWaitWorld(t)
	for _, tc := range []struct {
		cond WaitCondition
		want string
	}{
		{prEndedCond("test/repo#7", "abandoned"), fmt.Sprintf(rWaitPRHowUnknown, "abandoned")},
		{prEndedCond("nope#7", ""), fmt.Sprintf(rWaitPRRepoUnknown, "nope#7")},
		{prEndedCond("other/lib#x", ""), fmt.Sprintf(rNamePullRequestByItsNumber, "other/lib#x")},
		{WaitCondition{PREnded: &PREndedAsk{PR: "#7"}, After: "45m"}, rWaitOneKindPerCondition},
	} {
		_, err := w.waitForWithReads(nil, "c1", WaitAsk{Conditions: []WaitCondition{tc.cond}, Deadline: "1h"})
		if !IsRefusal(err) || err.Error() != tc.want {
			t.Errorf("%+v: %v, want %q", tc.cond, err, tc.want)
		}
	}
	if w.count("conductor_waits") != 0 {
		t.Fatal("a refused call wrote")
	}
}
