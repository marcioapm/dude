package phases

import (
	"context"
	"slices"
	"sync"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// callLux records the stops and cancels a Run's end asks lux for.
type callLux struct {
	lux.Client
	mu    sync.Mutex
	calls []string
}

func (c *callLux) record(call string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, call)
	return nil
}

func (c *callLux) Stop(_ context.Context, id string) error   { return c.record("stop " + id) }
func (c *callLux) Cancel(_ context.Context, id string) error { return c.record("cancel " + id) }

func (c *callLux) Calls() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.calls)
}

// endedRun is a failed or aborted phase Run in luxState, kept or not, as
// the sweep hands it to end.
func endedRun(t *testing.T, w *resumeWorld, status, luxState string, keep bool) (phaseRun, *callLux) {
	t.Helper()
	w.exec(`UPDATE runs SET status = $2::text::run_status, lux_state = $3, keep = $4, lux_stop_reason = NULL,
		kept_until = NULL, ended_at = now() WHERE id = $1`, w.run.ID, status, luxState, keep)
	fake := &callLux{}
	w.s.Lux = fake
	r := w.run
	r.Status, r.LuxState, r.Keep, r.LuxStopReason = status, luxState, keep, ""
	return r, fake
}

func (w *resumeWorld) stopReason() string {
	w.t.Helper()
	var reason string
	if err := w.owner.QueryRow(w.ctx, `SELECT COALESCE(lux_stop_reason, '') FROM runs WHERE id = $1`, w.run.ID).Scan(&reason); err != nil {
		w.t.Fatal(err)
	}
	return reason
}

// A kept Run whose lux Run succeeded is kept as a stopped one is: lux
// resumes a succeeded Run. Only a Run lux has ended for good, under either
// name, is let go without a call.
func TestAKeptRunThatSucceededInLuxIsKept(t *testing.T) {
	for _, c := range []struct {
		luxState, reason string
		calls            []string
	}{
		{"succeeded", "kept", nil},
		{"stopped", "kept", nil},
		{"failed", "kept", nil},
		{"terminated", "cancel", nil},
		{"cancelled", "cancel", nil},
	} {
		t.Run(c.luxState, func(t *testing.T) {
			w := newResumeWorld(t)
			r, fake := endedRun(t, w, "failed", c.luxState, true)
			if err := w.s.end(w.ctx, r); err != nil {
				t.Fatal(err)
			}
			if got := w.stopReason(); got != c.reason {
				t.Errorf("lux_stop_reason %q, want %q", got, c.reason)
			}
			// lux reports every one of these as over: nothing to stop.
			if got := fake.Calls(); !slices.Equal(got, c.calls) {
				t.Errorf("asked lux %v, want %v", got, c.calls)
			}
		})
	}
}

// A failed or aborted Run nobody keeps has its lux Run cancelled whatever
// state lux left it in — failed and succeeded included, which lux keeps to
// resume — and only one lux has ended for good, by either name, is not.
func TestAnEndedRunNotKeptIsCancelledInLux(t *testing.T) {
	for _, status := range []string{"failed", "aborted"} {
		for _, luxState := range []string{"failed", "succeeded", "stopped", "lost", "running", "terminated", "cancelled"} {
			t.Run(status+"/"+luxState, func(t *testing.T) {
				w := newResumeWorld(t)
				r, fake := endedRun(t, w, status, luxState, false)
				if err := w.s.end(w.ctx, r); err != nil {
					t.Fatal(err)
				}
				var want []string
				if !lux.Terminated(luxState) {
					want = []string{"cancel lrun_1"}
				}
				if got := fake.Calls(); !slices.Equal(got, want) {
					t.Errorf("asked lux %v, want %v", got, want)
				}
				if got := w.stopReason(); got != "cancel" {
					t.Errorf("lux_stop_reason %q, want cancel", got)
				}
			})
		}
	}
}

// A completed Run's lux Run is terminated once its exit is collected, and
// once only; one still being collected, a kept failed one, and a branch
// preview's (ended by the preview loop) are left alone.
func TestACompletedRunIsTerminatedInLuxOnceCollected(t *testing.T) {
	w := newResumeWorld(t)
	fake := &callLux{}
	w.s.Lux = fake
	w.exec(`UPDATE runs SET status = 'completed', lux_state = 'stopped', lux_stop_reason = 'complete', ended_at = now(),
		artifacts_due_at = NULL WHERE id = $1`, w.run.ID)
	w.exec(`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, phase, status, lux_run_id, lux_state,
			lux_stop_reason, ended_at)
		VALUES ('run_due', $1, 'prj_'||$1, 'wi_'||$1, 1, 'review', 'completed', 'lrun_due', 'stopped', 'complete', now()),
		       ('run_kept', $1, 'prj_'||$1, 'wi_'||$1, 1, 'review', 'failed', 'lrun_kept', 'stopped', 'kept', now()),
		       ('run_conductor', $1, 'prj_'||$1, 'wi_'||$1, 1, NULL, 'completed', 'lrun_cond', 'succeeded', 'complete', now())`,
		w.run.Org)
	w.exec(`UPDATE runs SET role = 'conductor', kind = 'agent' WHERE id = 'run_conductor'`)
	// Still collecting run_due's exit; run_kept is kept until it expires.
	w.exec(`UPDATE runs SET artifacts_due_at = now() WHERE id = 'run_due'`)
	w.exec(`UPDATE runs SET artifacts_due_at = NULL WHERE id IN ($1, 'run_kept', 'run_conductor')`, w.run.ID)
	w.exec(`INSERT INTO tasks (id, organization_id, project_id, number, title, goal) VALUES ('wi_p'||$1, $1, 'prj_'||$1, 2, 'T', 'G')`, w.run.Org)
	w.exec(`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, kind, status, lux_run_id, lux_state, ended_at)
		VALUES ('run_preview', $1, 'prj_'||$1, 'wi_p'||$1, 1, 'preview', 'completed', 'lrun_preview', 'stopped', now())`, w.run.Org)
	w.exec(`UPDATE runs SET artifacts_due_at = NULL WHERE id = 'run_preview'`)

	if n, err := w.s.RetireCompleted(w.ctx); err != nil || n != 2 {
		t.Fatalf("retired %d, %v; want 2", n, err)
	}
	got := fake.Calls()
	slices.Sort(got)
	if want := []string{"cancel lrun_1", "cancel lrun_cond"}; !slices.Equal(got, want) {
		t.Errorf("terminated %v, want %v", got, want)
	}
	// Collected now: its turn comes; and nothing is asked twice.
	w.exec(`UPDATE runs SET artifacts_due_at = NULL WHERE id = 'run_due'`)
	if n, err := w.s.RetireCompleted(w.ctx); err != nil || n != 1 {
		t.Fatalf("retired %d, %v; want 1", n, err)
	}
	if got := fake.Calls(); len(got) != 3 || got[2] != "cancel lrun_due" {
		t.Errorf("after the second pass, asked lux %v", got)
	}
	// Nothing left: the loop sleeps.
	if n, err := w.s.RetireCompleted(w.ctx); err != nil || n != 0 {
		t.Fatalf("retired %d, %v; want 0", n, err)
	}
}
