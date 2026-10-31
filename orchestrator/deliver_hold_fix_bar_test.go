package orchestrator_test

import (
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

// A delivery only Deliver ever decided, opened by its owner's Open at the
// hold of Ask me first, keeps the project's fix bar: a medium finding on a
// later pull request fix is not fixed, as no conductor ever had the
// delivery. (Open is a POST /decider {decider: policy}, which marks the
// delivery handed back.)
func TestADeliverOnlyDeliveryOpenedAtItsHoldKeepsTheProjectsFixBar(t *testing.T) {
	w := newWorld(t)
	w.onModel("reviewer", "llm-review")
	ana := w.person("Ana")
	var armed atomic.Bool
	var reviews atomic.Int32
	scripted := w.lux.Decide
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		labels, _ := spec["labels"].(map[string]any)
		if armed.Load() && labels["dude.phase"] == "review" {
			if reviews.Add(1) == 1 {
				return fakelux.Behaviour{Reply: "```yaml\n" + evidencedFinding("severity: medium", "FACTORY.md says the wrong thing") + "```\n"}
			}
			return fakelux.Behaviour{Reply: "```yaml\nverdicts:\n  F1: fixed\n```\n"}
		}
		if armed.Load() && labels["dude.phase"] == "fix" {
			return fakelux.Behaviour{Commit: map[string]string{"FACTORY.md": fmt.Sprint(labels["dude.run"]) + "\n"}, Message: "fix"}
		}
		return scripted(spec)
	}
	task := w.atTheHold(ana)
	if status, out := w.callAs(ana, "/internal/tasks/"+task+"/decider", map[string]any{"decider": "policy", "openPullRequest": true}); status != 200 {
		t.Fatalf("the owner's Open: %d %v", status, out)
	}
	w.until("the pull request on record", func() bool {
		return w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND state ? 'pullRequestIds'`, task) == 1
	})
	armed.Store(true)
	if code, body := w.feedback(ana, task, "fbi_held", "Log the retry count at info, not debug."); code != 201 {
		t.Fatalf("submit: %d %v", code, body)
	}
	finding := func() string {
		ids := w.ids(`SELECT id FROM review_findings WHERE task_id = $1 AND title = 'FACTORY.md says the wrong thing'`, task)
		if len(ids) == 0 {
			return ""
		}
		return ids[0]
	}
	w.until("the medium on the pull request's fix", func() bool { w.sync(); return finding() != "" })
	id := finding()
	sentIt := func() int {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'fix' AND $2 = ANY(finding_ids)`, task, id)
	}
	w.until("the round after the medium", func() bool {
		w.sync()
		return sentIt() > 0 || w.escalationReason(task) != "" ||
			w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND step = 'awaitPullRequest'`, task) == 1 &&
				w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND status NOT IN ('completed', 'failed', 'cancelled')`, task) == 0
	})
	if n := sentIt(); n != 0 {
		t.Errorf("a delivery no conductor had: the medium got %d fix rounds, want none (the project's bar)\n%s", n, w.describeRuns())
	}
}
