package orchestrator_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

// A stored legacy row — no hadConductor in its state, decided by Deliver's
// policy, as a delivery started before C1 is — runs its review loop as a
// new no-model delivery does: its reviewer is briefed with the conducted
// contract (asked for each finding's evidence), and a medium finding is at
// the fix bar, so it gets a fix round rather than shipping.
func TestALegacyRowRunsItsReviewRoundOnTheFixedBar(t *testing.T) {
	w := newWorld(t)
	w.onModel("reviewer", "llm-review")
	scripted := w.lux.Decide
	reviews := 0
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		labels, _ := spec["labels"].(map[string]any)
		switch labels["dude.phase"] {
		case "review":
			if reviews++; reviews == 1 {
				return fakelux.Behaviour{Reply: "```yaml\n" + evidencedFinding("severity: medium", "FACTORY.md says the wrong thing") + "```\n"}
			}
			return fakelux.Behaviour{Reply: "```yaml\nverdicts:\n  F1: fixed\n```\n"}
		case "fix":
			return fakelux.Behaviour{Commit: map[string]string{"FACTORY.md": fmt.Sprint(labels["dude.run"]) + "\n"}, Message: "fix"}
		}
		return scripted(spec)
	}
	task := w.task()
	w.names(task, w.repoID)
	// As the row was stored: the state's JSON has no hadConductor and no
	// decider (DeciderPolicy is the empty one).
	if _, _, err := w.runtime.Start(context.Background(), workflow.StartOptions{
		Type: delivery.WorkflowType, OrganizationID: w.org, IdempotencyKey: "delivery:" + task, TaskID: task,
		Input: delivery.State{TaskID: task, ProjectID: w.project, Policy: scriptedPolicy(), Branch: delivery.BranchFor(task, 1)},
	}); err != nil {
		t.Fatal(err)
	}
	if n := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND NOT state ? 'hadConductor' AND COALESCE(state->>'decider', '') = ''`, task); n != 1 {
		t.Fatalf("the row is not stored as a legacy one: %d", n)
	}
	review := w.reviewOnLux(task)
	if !strings.Contains(w.instructionsOf(review), askedForEvidence) {
		t.Error("a legacy row's reviewer is not briefed with the conducted contract (asked for evidence)")
	}
	w.until("the next phase", func() bool { return w.phaseRuns(task, "fix")+w.phaseRuns(task, "simplify") > 0 })
	if w.phaseRuns(task, "fix") == 0 {
		t.Errorf("a legacy row's medium finding shipped: no fix round, want one (the fixed bar)\n%s", w.describeRuns())
	}
}
