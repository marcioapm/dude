package phases

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// luxFrame feeds the translator one of lux's lifecycle events of epoch, in
// a transaction of its own as a batch would be.
func (w *harnessWorld) luxFrame(epoch int, typ string, data map[string]any) {
	w.t.Helper()
	raw, _ := json.Marshal(data)
	ctx := context.Background()
	if err := w.s.DB.InOrg(ctx, w.run.Org, func(tx pgx.Tx) error {
		return w.tr.luxEvent(ctx, tx, w.s, lux.Frame{Kind: "lux", Epoch: epoch, EventType: typ, EventData: raw})
	}); err != nil {
		w.t.Fatal(err)
	}
}

// failedWith is the Run's run.failed payload; the test fails without one.
func (w *harnessWorld) failedWith() map[string]any {
	w.t.Helper()
	failed := ofType(w.events(), "run.failed")
	if len(failed) != 1 {
		w.t.Fatalf("%d run.failed, want 1", len(failed))
	}
	return failed[0].Payload
}

func (w *harnessWorld) stages(epoch int, stages ...string) {
	w.t.Helper()
	for _, s := range stages {
		w.luxFrame(epoch, "stage", map[string]any{"stage": s, "epoch": epoch, "since": "2026-10-12T10:00:00Z"})
	}
}

// A Run that ran and then failed (lux's timeout: stopping, then failed)
// did not fail to start: its run.failed names no stage and no lux reason.
func TestARunThatRanThenFailedNamesNoStage(t *testing.T) {
	w := newHarnessWorld(t)
	w.stages(1, "waiting", "image", "volumes", "repositories", "container")
	w.luxFrame(1, "state", map[string]any{"state": "stopping", "reason": "timeout"})
	w.luxFrame(1, "state", map[string]any{"state": "failed", "reason": "timeout"})
	if p := w.failedWith(); p["stage"] != nil || p["reason"] != nil {
		t.Errorf("run.failed = %v, want no stage or reason", p)
	}
}

// A drained Run whose resumePolicy (manual) fails it on the move: its
// stop was written as park, but park is not a start, so its run.failed
// names no stage rather than "failed while stopping it".
func TestADrainedManualRunNamesNoStage(t *testing.T) {
	w := newHarnessWorld(t)
	w.luxFrame(1, "state", map[string]any{"state": "stopping", "reason": "drain"})
	w.luxFrame(1, "stage", map[string]any{"stage": "stopping", "reason": "drain", "epoch": 1, "since": "2026-10-12T10:00:00Z"})
	w.luxFrame(1, "state", map[string]any{"state": "stopped", "reason": "drain"})
	w.luxFrame(1, "state", map[string]any{"state": "failed", "reason": "drain: not resumed (resumePolicy manual)"})
	if p := w.failedWith(); p["stage"] != nil {
		t.Errorf("run.failed = %v, want no stage", p)
	}
}
