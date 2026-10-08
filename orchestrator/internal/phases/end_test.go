package phases

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// callLux records the stops and cancels a Run's end asks lux for. A cancel
// of a lux run id in err is refused with that error. For a pass of
// RetireCompleted (setPass): when gated, the cancels lux does not refuse as
// unhealthy wait until such a refusal has had time to end the batch; a
// retryable refusal waits (up to a second) until await calls are recorded.
type callLux struct {
	lux.Client
	mu    sync.Mutex
	calls []string
	err   map[string]error
	gate  chan struct{}
	once  *sync.Once
	await int
}

func (c *callLux) setPass(gated bool, await int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gate, c.once, c.await = nil, &sync.Once{}, await
	if gated {
		c.gate = make(chan struct{})
	}
}

// onCallLux makes the world's lux a callLux refusing with errs.
func (w *resumeWorld) onCallLux(errs map[string]error) *callLux {
	fake := &callLux{err: errs}
	w.s.Lux = fake
	return fake
}

func (c *callLux) Stop(_ context.Context, id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, "stop "+id)
	return nil
}

func (c *callLux) Cancel(_ context.Context, id string) error {
	c.mu.Lock()
	c.calls = append(c.calls, "cancel "+id)
	gate, once, await := c.gate, c.once, c.await
	c.mu.Unlock()
	err := c.err[id]
	if le, ok := lux.AsError(err); ok && le.Retryable() {
		for deadline := time.Now().Add(time.Second); len(c.Calls()) < await && time.Now().Before(deadline); {
			time.Sleep(5 * time.Millisecond)
		}
		if gate != nil {
			once.Do(func() { time.AfterFunc(50*time.Millisecond, func() { close(gate) }) })
		}
		return err
	}
	if gate != nil {
		<-gate
	}
	return err
}

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
	r := w.run
	r.Status, r.LuxState, r.Keep, r.LuxStopReason = status, luxState, keep, ""
	return r, w.onCallLux(nil)
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
// name, is let go without a call; one whose kept_until has passed is
// terminated, succeeded included.
func TestAKeptRunIsKeptUnlessLuxEndedItOrItsKeepExpired(t *testing.T) {
	for _, c := range []struct {
		luxState    string
		keepExpired bool
		reason      string
		calls       []string
	}{
		{"succeeded", false, "kept", nil},
		{"stopped", false, "kept", nil},
		{"failed", false, "kept", nil},
		{"terminated", false, "cancel", nil},
		{"cancelled", false, "cancel", nil},
		{"succeeded", true, "cancel", []string{"cancel lrun_1"}},
	} {
		t.Run(c.luxState+map[bool]string{true: "/expired"}[c.keepExpired], func(t *testing.T) {
			w := newResumeWorld(t)
			r, fake := endedRun(t, w, "failed", c.luxState, true)
			if c.keepExpired {
				w.exec(`UPDATE runs SET kept_until = now() - interval '1 second' WHERE id = $1`, r.ID)
				r.KeepExpired = true
			}
			if err := w.s.end(w.ctx, r); err != nil {
				t.Fatal(err)
			}
			if got := w.stopReason(); got != c.reason {
				t.Errorf("lux_stop_reason %q, want %q", got, c.reason)
			}
			// Kept, or lux reports it over: nothing to stop. Expired: terminated.
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
// once only, a conductor's and a session agent's included; one still being
// collected, a failed one (end deals with it), a parked session agent, and
// a branch preview's (ended by the preview loop) are left alone.
func TestACompletedRunIsTerminatedInLuxOnceCollected(t *testing.T) {
	w := newResumeWorld(t)
	fake := w.onCallLux(nil)
	// The status change sets artifacts_due_at (017's trigger); an insert does not.
	w.exec(`UPDATE runs SET status = 'completed', lux_state = 'stopped', lux_stop_reason = 'complete', ended_at = now() WHERE id = $1`, w.run.ID)
	w.exec(`UPDATE runs SET artifacts_due_at = NULL WHERE id = $1`, w.run.ID)
	w.exec(`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, phase, status, lux_run_id, lux_state,
			lux_stop_reason, ended_at)
		VALUES ('run_due', $1, 'prj_'||$1, 'wi_'||$1, 1, 'review', 'completed', 'lrun_due', 'stopped', 'complete', now()),
		       ('run_kept', $1, 'prj_'||$1, 'wi_'||$1, 1, 'review', 'failed', 'lrun_kept', 'stopped', 'kept', now()),
		       ('run_conductor', $1, 'prj_'||$1, 'wi_'||$1, 1, NULL, 'completed', 'lrun_cond', 'succeeded', 'complete', now())`,
		w.run.Org)
	w.exec(`UPDATE runs SET role = 'conductor', kind = 'agent' WHERE id = 'run_conductor'`)
	// Still collecting run_due's exit; run_kept is failed, not completed.
	w.exec(`UPDATE runs SET artifacts_due_at = now() WHERE id = 'run_due'`)
	w.exec(`INSERT INTO tasks (id, organization_id, project_id, number, title, goal) VALUES ('wi_p'||$1, $1, 'prj_'||$1, 2, 'T', 'G')`, w.run.Org)
	w.exec(`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, kind, status, lux_run_id, lux_state, ended_at)
		VALUES ('run_preview', $1, 'prj_'||$1, 'wi_p'||$1, 1, 'preview', 'completed', 'lrun_preview', 'stopped', now())`, w.run.Org)
	// A session with an ended agent Run and a parked one (paused), seeded as
	// the sessions tests do: its one owner is checked at commit.
	w.exec(`INSERT INTO people (id, organization_id, name) VALUES ('per_'||$1, $1, 'C')`, w.run.Org)
	w.exec(`BEGIN`)
	w.exec(`INSERT INTO sessions (id, organization_id, title) VALUES ('sess_'||$1, $1, 'Ideas')`, w.run.Org)
	w.exec(`INSERT INTO session_people (session_id, person_id, organization_id, role, accepted_at)
		VALUES ('sess_'||$1, 'per_'||$1, $1, 'owner', now())`, w.run.Org)
	w.exec(`COMMIT`)
	w.exec(`INSERT INTO runs (id, organization_id, session_id, attempt, status, kind, role, lux_run_id, lux_state,
			lux_stop_reason, ended_at)
		VALUES ('run_session', $1, 'sess_'||$1, 1, 'completed', 'agent', 'brainstorm', 'lrun_session', 'succeeded', 'complete', now()),
		       ('run_parked', $1, 'sess_'||$1, 2, 'paused', 'agent', 'brainstorm', 'lrun_parked', 'stopped', 'pause', NULL)`, w.run.Org)

	if n, err := w.s.RetireCompleted(w.ctx); err != nil || n != 3 {
		t.Fatalf("retired %d, %v; want 3", n, err)
	}
	got := fake.Calls()
	slices.Sort(got)
	if want := []string{"cancel lrun_1", "cancel lrun_cond", "cancel lrun_session"}; !slices.Equal(got, want) {
		t.Errorf("terminated %v, want %v", got, want)
	}
	// Collected now: its turn comes; and nothing is asked twice.
	w.exec(`UPDATE runs SET artifacts_due_at = NULL WHERE id = 'run_due'`)
	if n, err := w.s.RetireCompleted(w.ctx); err != nil || n != 1 {
		t.Fatalf("retired %d, %v; want 1", n, err)
	}
	if got := fake.Calls(); len(got) != 4 || got[3] != "cancel lrun_due" {
		t.Errorf("after the second pass, asked lux %v", got)
	}
	// Nothing left: the loop sleeps.
	if n, err := w.s.RetireCompleted(w.ctx); err != nil || n != 0 {
		t.Fatalf("retired %d, %v; want 0", n, err)
	}
	if got := fake.Calls(); slices.Contains(got, "cancel lrun_parked") {
		t.Errorf("a parked session agent was terminated: %v", got)
	}
}

// lux refusing a terminate for good (unknown, already terminated) counts as
// terminated; lux unhealthy (503), or a failure that is not lux's answer,
// leaves the Run to ask again and ends the batch, so the Runs behind it
// wait for the next pass.
func TestARetireLuxRefusesIsDoneOrAskedAgain(t *testing.T) {
	w := newResumeWorld(t)
	// Oldest first: the 503 one, the two refusals, six healthy ones, then
	// h7, whose cancel never reaches lux.
	ids := []string{"r503", "r404", "r409", "h1", "h2", "h3", "h4", "h5", "h6", "h7"}
	for i, id := range ids {
		w.exec(`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, phase, status, lux_run_id, lux_state,
				lux_stop_reason, ended_at)
			VALUES ($2, $1, 'prj_'||$1, 'wi_'||$1, 1, 'review', 'completed', 'l'||$2, 'stopped', 'complete',
				now() - make_interval(secs => $3))`, w.run.Org, id, 100-i)
	}
	dialErr := errors.New("dial tcp: connection refused")
	fake := w.onCallLux(map[string]error{
		"lr503": &lux.Error{Status: 503, Code: "unavailable"},
		"lr404": &lux.Error{Status: 404, Code: "not_found"},
		"lr409": &lux.Error{Status: 409, Code: "not_cancellable"},
		"lh7":   dialErr,
	})
	terminated := func() []string {
		t.Helper()
		var got []string
		if err := w.owner.QueryRow(w.ctx, `SELECT COALESCE(array_agg(id ORDER BY id), '{}') FROM runs
			WHERE organization_id = $1 AND lux_stop_reason = 'cancel'`, w.run.Org).Scan(&got); err != nil {
			t.Fatal(err)
		}
		return got
	}

	// The 503, answered once all 8 slots are asked, ends the batch: h6 and
	// h7 are not asked.
	fake.setPass(true, 8)
	n, err := w.s.RetireCompleted(w.ctx)
	if err == nil || n != 7 {
		t.Fatalf("first pass retired %d, %v; want 7 and lux's 503 (asked %v, recorded %v)", n, err, fake.Calls(), terminated())
	}
	got := fake.Calls()
	slices.Sort(got)
	if want := []string{"cancel lh1", "cancel lh2", "cancel lh3", "cancel lh4", "cancel lh5", "cancel lr404", "cancel lr409", "cancel lr503"}; !slices.Equal(got, want) {
		t.Errorf("first pass asked lux %v, want %v", got, want)
	}
	if got, want := terminated(), []string{"h1", "h2", "h3", "h4", "h5", "r404", "r409"}; !slices.Equal(got, want) {
		t.Errorf("first pass recorded %v terminated, want %v", got, want)
	}

	// Then the rest and the 503 again; then the 503 and h7, whose cancel
	// failed with an error that is not lux's answer and so proves nothing.
	for pass, want := range [][]string{{"cancel lh6", "cancel lh7", "cancel lr503"}, {"cancel lh7", "cancel lr503"}} {
		before := len(fake.Calls())
		fake.setPass(false, before+len(want))
		if _, err := w.s.RetireCompleted(w.ctx); !errors.Is(err, dialErr) {
			t.Errorf("pass %d returned %v; want the dial error", pass+2, err)
		}
		got := fake.Calls()[before:]
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Errorf("pass %d asked lux %v, want %v", pass+2, got, want)
		}
		if got := terminated(); slices.Contains(got, "h7") {
			t.Errorf("pass %d recorded h7 terminated after a dial error", pass+2)
		}
	}
	if got := terminated(); slices.Contains(got, "r503") || slices.Contains(got, "h7") || len(got) != 8 {
		t.Errorf("recorded %v terminated; want every one but r503 and h7", got)
	}
}
