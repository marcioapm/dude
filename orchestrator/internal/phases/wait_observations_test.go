package phases

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

// hangingForges never answers: a GitHub that holds every read until the
// caller gives up.
type hangingForges struct{ asked atomic.Int32 }

func (h *hangingForges) For(ctx context.Context, _ string) (*forge.GitHub, error) {
	h.asked.Add(1)
	<-ctx.Done()
	return nil, ctx.Err()
}

// A wait on something outside dude whose read hangs never holds the phase
// sweep: the sweep reads nothing outside dude for a wait (its own loop,
// ObserveWaits, does), so it returns within its bound and still evaluates
// the wait from what is stored — here, its deadline passed.
func TestASweepIsNotHeldByAHangingWaitObservation(t *testing.T) {
	w := newWakeSweepWorld(t)
	hang := &hangingForges{}
	w.s.Forges = hang
	w.s.SweepWait = 2 * time.Second
	w.exec(`INSERT INTO tasks (id, organization_id, project_id, number, title, goal) VALUES ('wi_h', $1, 'prj_'||$1, 1, 'T', 'G')`, w.org)
	w.exec(`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, role, kind, status, dude_pause, turn_done_at)
		VALUES ('run_h', $1, 'prj_'||$1, 'wi_h', 1, 'conductor', 'agent', 'paused', 'conductor', now())`, w.org)
	w.exec(`INSERT INTO conductor_waits (id, organization_id, task_id, conductor_run_id, attempt, conditions, deadline_at, since_cursor, call_key)
		VALUES ('cwt_h', $1, 'wi_h', 'run_h', 1,
			'[{"kind": "pr_ended", "slug": "other/lib", "number": 41, "prName": "other/lib#41", "how": "any"}]',
			now() - interval '1 second', 0, 'c')`, w.org)

	// The fixture's read does hang: the observation loop's pass reaches it
	// and is ended only by its context.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	if _, err := w.s.ObserveWaits(ctx); err != nil {
		t.Fatal(err)
	}
	cancel()
	if hang.asked.Load() != 1 {
		t.Fatalf("the observation asked GitHub %d times, want 1", hang.asked.Load())
	}

	ctx, cancel = context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	start := time.Now()
	if _, err := w.s.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("the sweep took %s behind a hanging observation", took)
	}
	if hang.asked.Load() != 1 {
		t.Fatal("the sweep read a wait's condition outside dude")
	}
	if n := w.count(`SELECT count(*) FROM conductor_waits WHERE id = 'cwt_h' AND ended_by = 'deadline'`); n != 1 {
		t.Fatal("the sweep did not evaluate the wait")
	}
}
