package orchestrator_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
)

// spendFreeDispatches dispatches deploy-docs.yml on the task's branch,
// without asking, until the task's free dispatches are spent.
func (w *world) spendFreeDispatches(task string) {
	w.t.Helper()
	for range delivery.FreeDispatches {
		w.dispatched(task, `{"workflow":"deploy-docs.yml"}`)
	}
}

// On the task's own branch, the conductor dispatches any workflow that has
// a workflow_dispatch trigger without asking, up to three per task; the
// fourth needs the person, as does any other ref from the first. A
// dispatch GitHub refused (nothing ran) spends none, free CI spends none,
// and an answered question still authorizes one past the three.
func TestThreeDispatchesOnTheTasksBranchNeedNoQuestion(t *testing.T) {
	w, task, branch := dispatchWorld(t)
	// Another ref is the person's, free runs left or not. (A Run makes at
	// most 10 run_workflow calls: this test makes 10.)
	w.gh.CommitFiles("main", "docs", map[string]string{".github/workflows/deploy-docs.yml": docsWorkflow})
	w.refused(task, "run_workflow", `{"workflow":"deploy-docs.yml","ref":"main"}`, "main is not this task's branch")

	// GitHub refuses one: nothing ran, nothing spent.
	w.gh.Set(func(s *fakegithub.Server) { s.DispatchStatus = 422 })
	w.refused(task, "run_workflow", `{"workflow":"nightly.yml"}`, "422")
	w.gh.Set(func(s *fakegithub.Server) { s.DispatchStatus = 0 })

	first := w.dispatched(task, `{"workflow":"nightly.yml"}`)
	w.dispatched(task, `{"workflow":"ci.yml"}`)
	w.dispatched(task, `{"workflow":"deploy.yml","inputs":{"env":"staging"}}`)
	w.dispatched(task, `{"workflow":"deploy-docs.yml"}`)
	if got := w.str(`SELECT COALESCE(question_id, 'none') FROM workflow_dispatches WHERE id = $1`, first); got != "none" {
		t.Fatalf("first free dispatch: %s", got)
	}
	approve := delivery.DispatchApproval("prod.yml", branch)
	w.refused(task, "run_workflow", `{"workflow":"prod.yml"}`, "this task's 3 runs without asking are spent, so the person decides this one. "+
		"Ask them (ask_person) with the choices \""+approve+"\" and \""+delivery.DispatchDecline+"\"")
	// Free CI still runs.
	w.dispatched(task, `{"workflow":"ci.yml"}`)
	// The person's answer authorizes one more, as before.
	mustExec(t, w.owner, `INSERT INTO questions (id, organization_id, task_id, prompt, options, status, answer, answered_at, addressee)
		VALUES ('q_prod', $1, $2, 'Run prod.yml?', $3::jsonb, 'answered', $4, now(), 'person')`,
		w.org, task, `["`+approve+`", "`+delivery.DispatchDecline+`"]`, approve)
	w.dispatched(task, `{"workflow":"prod.yml","questionId":"q_prod"}`)
	w.refused(task, "run_workflow", `{"workflow":"release.yml"}`, "runs without asking are spent")
	if n := w.count(`SELECT count(*) FROM workflow_dispatches WHERE task_id = $1 AND state <> 'failed'`, task); n != 6 {
		t.Errorf("%d dispatches", n)
	}
}

// Two un-asked dispatches at once, each the task's third: the first holds
// its POST at GitHub; the second, counted under the same lock after the
// first's reservation, is refused, and never posts.
func TestTwoConcurrentThirdFreeDispatchesDispatchOne(t *testing.T) {
	w, task, _ := dispatchWorld(t)
	w.dispatched(task, `{"workflow":"deploy-docs.yml"}`)
	w.dispatched(task, `{"workflow":"deploy-docs.yml"}`)
	posting, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	w.gh.Set(func(s *fakegithub.Server) {
		s.Intercept = func(r *http.Request) int {
			if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/dispatches") {
				once.Do(func() { close(posting) })
				<-release
			}
			return 0
		}
	})
	type result struct {
		status int
		out    map[string]any
	}
	results := make(chan result, 2)
	spec := w.conductorSpecOf(task)
	for range 2 {
		go func() {
			status, body := w.callTool(w.syncer.Agent.ToolsURL, spec, "run_workflow", `{"workflow":"deploy.yml"}`)
			var out map[string]any
			_ = json.Unmarshal([]byte(body), &out)
			results <- result{status, out}
		}()
	}
	select {
	case <-posting:
	case <-time.After(60 * time.Second):
		close(release)
		t.Fatal("neither call posted")
	}
	var refused result
	select {
	case refused = <-results:
	case <-time.After(60 * time.Second):
		close(release)
		t.Fatal("the second call was not refused before the first POST ended")
	}
	close(release)
	accepted := <-results
	if msg, _ := refused.out["error"].(string); refused.status != 422 || !strings.Contains(msg, "runs without asking are spent") {
		t.Fatalf("the second call: %d %v", refused.status, refused.out)
	}
	if accepted.status != 200 || accepted.out["dispatchId"] == nil {
		t.Fatalf("the first call: %d %v", accepted.status, accepted.out)
	}
	w.gh.Set(func(s *fakegithub.Server) {
		s.Intercept = nil
		if len(s.Dispatches) != 3 {
			t.Errorf("%d POSTs", len(s.Dispatches))
		}
	})
}
