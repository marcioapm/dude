package delivery

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

// weighOnce runs weighReadiness over the fixture's pull request, the task
// at status first.
func (f *gateFixture) weighOnce(status string) error {
	f.t.Helper()
	f.exec(`UPDATE tasks SET status = $2::task_status WHERE id = 'wi_g' AND organization_id = $1`, status)
	f.st.PullRequestIDs = []string{"pr_g"}
	states, err := f.store.PullRequestStates(f.ctx, f.org, f.st.PullRequestIDs)
	if err != nil {
		f.t.Fatal(err)
	}
	w := &steps{s: f.store, forges: forgeOf{f.forge}}
	return w.weighReadiness(f.ctx, workflow.StepContext{OrganizationID: f.org, Step: "awaitPullRequest"}, &f.st, states)
}

func (f *gateFixture) taskStatus() string {
	f.t.Helper()
	var s string
	if err := f.owner.QueryRow(f.ctx, `SELECT status::text FROM tasks WHERE id = 'wi_g'`).Scan(&s); err != nil {
		f.t.Fatal(err)
	}
	return s
}

// GitHub not answering is no judgement: a task ready to merge stays ready
// to merge, and the step waits to read the gate again (workflow.Wait), its
// attempts not spent.
func TestWeighReadinessKeepsTheStatusThroughAGitHubOutage(t *testing.T) {
	f := newGateFixture(t)
	f.ready()
	if err := f.weighOnce("review"); err != nil || f.taskStatus() != "ready_to_merge" {
		t.Fatalf("the gate passing: %s (%v)", f.taskStatus(), err)
	}
	f.intercept = func(w http.ResponseWriter, r *http.Request) bool {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"message":"Server Error"}`))
		return true
	}
	err := f.weighOnce("ready_to_merge")
	var wait workflow.Wait
	if !errors.As(err, &wait) || wait.After < readyGateRetry {
		t.Fatalf("err %v, want a workflow.Wait of at least %s", err, readyGateRetry)
	}
	if s := f.taskStatus(); s != "ready_to_merge" {
		t.Fatalf("status %s through GitHub's outage, want ready_to_merge kept", s)
	}
}

// A rate-limited read is read again after GitHub's Retry-After, not after
// the fixed retry.
func TestWeighReadinessRetriesARateLimitedReadAfterItsRetryAfter(t *testing.T) {
	f := newGateFixture(t)
	f.ready()
	f.intercept = func(w http.ResponseWriter, r *http.Request) bool {
		if !strings.HasSuffix(r.URL.Path, fmt.Sprintf("/pulls/%d", f.n)) {
			return false
		}
		w.Header().Set("Retry-After", "300")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"message":"API rate limit exceeded"}`))
		return true
	}
	err := f.weighOnce("review")
	var wait workflow.Wait
	if !errors.As(err, &wait) || wait.After != 300*time.Second {
		t.Fatalf("err %#v, want a workflow.Wait of GitHub's 300s Retry-After", err)
	}
}
