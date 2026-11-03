package orchestrator_test

// A delivery whose conductor has no model (step 3b, PR C1):
// Deliver's start, a Start over and the boot sweep leave it to Deliver's
// policy, as before every delivery had a conductor, the fix bar the
// conductor's; the sweep hands it to the quiet conductor once a model is
// set. Through the real API, workflow, syncer and fake lux and GitHub.

import (
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakeagent"
)

// noConductorModel leaves the project's conductor on the organization's
// tier, and that tier with no model, as the seed leaves a new org's.
func (w *world) noConductorModel() {
	w.t.Helper()
	mustExec(w.t, w.owner, `UPDATE projects SET agent_models = agent_models - 'conductor' WHERE id = $1`, w.project)
	mustExec(w.t, w.owner, `UPDATE model_tiers SET model = NULL WHERE organization_id = $1
		AND id = (SELECT default_agent_models->'conductor'->>'tier' FROM organizations WHERE id = $1)`, w.org)
}

// onDeliversPolicyForNoModel says the task's newest delivery is Deliver's
// policy's because its conductor has no model: the conductor's fix bar
// and voice kept, and the change recorded as why.
func (w *world) onDeliversPolicyForNoModel(task string) {
	w.t.Helper()
	if got := w.str(`SELECT COALESCE(state->>'decider', 'policy') || '/' || COALESCE(state->>'voice', '') || '/'
		|| COALESCE(state->>'handedBack', 'false') FROM workflow_runs WHERE task_id = $1 ORDER BY created_at DESC LIMIT 1`,
		task); got != "policy/quiet/false" {
		w.t.Errorf("a delivery with no conductor model is decided %s, want policy/quiet", got)
	}
	if n := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND (state->>'hadConductor')::boolean
		AND created_at = (SELECT max(created_at) FROM workflow_runs WHERE task_id = $1)`, task); n != 1 {
		w.t.Errorf("no conductor's fix bar (hadConductor)")
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'task.decider_changed'
		AND payload->>'to' = 'policy' AND payload->>'why' = $2 AND payload->>'voice' = 'quiet'`, task, delivery.WhyNoConductorModel); n != 1 {
		w.t.Errorf("%d decider changes saying the conductor has no model, want 1", n)
	}
}

// Deliver pressed in an organization whose conductor has no model reaches
// its pull request on Deliver's policy, with no escalation, no decision
// defaulted and no conductor Run, as it did before every delivery had one.
func TestDeliverWithNoConductorModelReachesItsPullRequestOnDeliversPolicy(t *testing.T) {
	w := newWorld(t)
	w.noConductorModel()
	task := w.task()
	w.deliverByAPI(task)
	w.onDeliversPolicyForNoModel(task)
	w.until("the pull request", func() bool {
		return len(w.gh.Pulls()) == 1 || w.escalationReason(task) != ""
	})
	if r := w.escalationReason(task); r != "" {
		t.Fatalf("escalated %s\n%s", r, w.describeRuns())
	}
	if d := w.decider(task); d != delivery.DeciderPolicy {
		t.Errorf("decided by %s at the pull request, want policy", d)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = $2`, task, delivery.EvDecisionDefaulted); n != 0 {
		t.Errorf("%d decisions defaulted", n)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND role = 'conductor'`, task); n != 0 {
		t.Errorf("%d conductor Runs", n)
	}
}

// The sweep leaves such a delivery Deliver's while the model is missing
// (skipped, no model), and hands it to the quiet conductor once one is set.
func TestTheSweepConvertsADeliveryWithNoConductorModelOnceOneIsSet(t *testing.T) {
	w := newWorld(t)
	w.noConductorModel()
	task := w.task()
	w.deliverByAPI(task)
	w.advanced()
	if n := w.convert(); n.Converted != 0 || n.NoModel != 1 || n.Accepted != 0 {
		t.Fatalf("sweep with no model: %+v, want one skipped for no model", n)
	}
	w.onDeliversPolicyForNoModel(task)
	w.onModel("conductor", fakeagent.DefaultsModel)
	if n := w.convert(); n.Converted != 1 || n.NoModel != 0 {
		t.Fatalf("sweep once a model is set: %+v", n)
	}
	w.convertedQuietly(task)
}

// A Start over of a quiet delivery whose conductor has since lost its
// model starts attempt 2 on Deliver's policy, as a start does; naming the
// conductor still hands it to a chatty one.
func TestStartingOverWithNoConductorModelStartsOnDeliversPolicy(t *testing.T) {
	for _, c := range []struct{ decider, want string }{
		{"", "policy/quiet"},
		{"policy", "policy/quiet"},
		{"conductor", "conductor/chatty"},
	} {
		t.Run("decider="+c.decider, func(t *testing.T) {
			w := conducting(t)
			task := w.task()
			owner, _, _ := w.people(task)
			w.deliverByAPI(task)
			w.until("the implementer", func() bool { return w.phaseRuns(task, "implement") > 0 })
			if got := w.deciderState(task); got != "conductor/quiet/false" {
				t.Fatalf("with a model: %s, want a quiet conductor", got)
			}
			w.noConductorModel()
			body := map[string]any{"note": "again", "andRestart": true}
			if c.decider != "" {
				body["decider"] = c.decider
			}
			if out := w.stop(owner, task, body); out["attempt"] != float64(2) {
				t.Fatalf("stop and start over answered %v", out)
			}
			got := w.str(`SELECT COALESCE(state->>'decider', 'policy') || '/' || COALESCE(state->>'voice', '')
				FROM workflow_runs WHERE task_id = $1 AND (state->>'attempt')::int = 2`, task)
			if got != c.want {
				t.Errorf("attempt 2 decided %s, want %s", got, c.want)
			}
			if c.want == "policy/quiet" {
				w.onDeliversPolicyForNoModel(task)
			}
		})
	}
}
