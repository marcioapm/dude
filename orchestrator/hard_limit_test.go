package orchestrator_test

// The hard limit: every phase Run is sent lux's timeout, and a Run lux
// stops at it fails with that reason and escalates like any failure. The
// conductor, which parks while idle, gets none.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
	"github.com/marciomartins/dude/orchestrator/internal/phases"
)

func TestEveryPhaseRunIsSentTheHardLimitAndTheConductorNone(t *testing.T) {
	w := conducting(t)
	task := w.task()
	w.talk(task)
	w.must(task, "start_phase", `{"phase":"implement"}`)
	w.until("the implementer on lux", func() bool { return w.specOf("implement") != nil })
	seen := map[string]string{}
	for _, r := range w.lux.Runs() {
		var spec lux.Spec
		_ = json.Unmarshal(r.Spec, &spec)
		seen[spec.Labels["dude.phase"]] = spec.Timeout
	}
	if got, ok := seen["conductor"]; !ok || got != "" {
		t.Errorf("the conductor's timeout is %q (seen %v), want none", got, ok)
	}
	if seen["implement"] != phases.DefaultTimeout {
		t.Errorf("the implementer's timeout is %q, want %s", seen["implement"], phases.DefaultTimeout)
	}
	w2 := newWorld(t)
	w2.syncer.Agent.Timeout = "90m"
	wi := w2.task()
	w2.deliver(wi)
	w2.until("a reviewer on lux", func() bool { return w2.specOf("review") != nil })
	for _, phase := range []string{"implement", "review"} {
		if got := w2.specOf(phase).Timeout; got != "90m" {
			t.Errorf("the %s spec's timeout is %q, want the deployment's 90m", phase, got)
		}
	}
}

// lux stops a Run at its time limit: it fails saying so, is not kept (its
// running time is spent), and the delivery stops for a person.
func TestARunLuxTimesOutFailsAndEscalates(t *testing.T) {
	w := newWorld(t)
	w.lux.Decide = func(map[string]any) fakelux.Behaviour { return fakelux.Behaviour{Hang: true} }
	wi := w.task()
	w.deliver(wi)
	var runID, luxID string
	w.until("the implementer to be working", func() bool {
		_ = w.owner.QueryRow(context.Background(), `SELECT id, COALESCE(lux_run_id, '') FROM runs
			WHERE task_id = $1 AND phase = 'implement' AND agent_busy_at IS NOT NULL`, wi).Scan(&runID, &luxID)
		return luxID != ""
	})
	w.lux.Timeout(luxID)
	w.until("the delivery to stop for a person", func() bool { return w.taskStatus(wi) == "awaiting_input" })
	var status, errText string
	var keep bool
	_ = w.owner.QueryRow(context.Background(), `SELECT status::text, COALESCE(error, ''), keep FROM runs WHERE id = $1`, runID).
		Scan(&status, &errText, &keep)
	if status != "failed" || !strings.Contains(errText, "time limit") || !strings.Contains(errText, phases.DefaultTimeout) || keep {
		t.Errorf("run %s %q keep=%v: want failed at its time limit, not kept", status, errText, keep)
	}
	if got := w.escalationReason(wi); got != "implement_failed" {
		t.Errorf("escalated for %q, want implement_failed", got)
	}
}
