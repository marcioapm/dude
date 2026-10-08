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
