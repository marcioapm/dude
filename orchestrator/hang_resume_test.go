package orchestrator_test

import (
	"context"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/fakeagent"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// fakeagent.HangModel is the e2e suite's live agent to pause, resume and
// abort: on lux its workload sleeps until stopped, so a resumed one is still
// working when a person aborts it. The resume's input (the carry-on nudge)
// must start a turn that hangs too; a turn that ended there would complete
// the Run, and "running" would be a status it only passes through.
func TestAResumedHangingAgentKeepsWorkingUntilAborted(t *testing.T) {
	w := newWorld(t)
	w.onModel("implementer", fakeagent.HangModel)
	wi := w.task()
	w.deliver(wi)
	w.until("the agent to be working", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND status = 'running' AND lux_state = 'running'`, wi) == 1
	})
	var runID string
	if err := w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE task_id = $1`, wi).Scan(&runID); err != nil {
		t.Fatal(err)
	}
	luxID := w.lux.Runs()[0].ID

	if code, body := w.call("/internal/runs/"+runID+"/pause", map[string]any{}); code != 200 {
		t.Fatalf("pause answered %d: %v", code, body)
	}
	w.until("lux to stop the paused run", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND lux_state = 'stopped'`, runID) == 1
	})
	if code, body := w.call("/internal/runs/"+runID+"/resume", map[string]any{}); code != 200 {
		t.Fatalf("resume answered %d: %v", code, body)
	}
	// The fake takes the resume's input and plays the turn it starts in one
	// critical section, so once the input is read the turn is as far as it
	// will get on its own.
	w.until("the resumed agent to read the resume's input", func() bool {
		return readInput(w.lux.Records(luxID), "resume")
	})
	if turnEnded(w.lux.Records(luxID)) {
		t.Fatal("the resumed agent ended its turn on the resume's input")
	}
	w.until("the resumed run to be running", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running' AND lux_state = 'running'`, runID) == 1
	})
	for range 5 {
		w.pump()
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running' AND turn_done_at IS NULL`, runID); n != 1 {
		t.Fatalf("the resumed run did not stay running with its turn open:\n%s", w.describeRuns())
	}

	if code, body := w.call("/internal/runs/"+runID+"/abort", map[string]any{"reason": "changed my mind"}); code != 200 {
		t.Fatalf("abort answered %d: %v", code, body)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'aborted'`, runID); n != 1 {
		t.Fatalf("the aborted run is not aborted:\n%s", w.describeRuns())
	}
}

// readInput: the agent took the input sent under requestID.
func readInput(records []map[string]any, requestID string) bool {
	for _, r := range records {
		if data, _ := r["data"].(map[string]any); r["type"] == lux.RecordInputConsumed && data["requestId"] == requestID {
			return true
		}
	}
	return false
}
