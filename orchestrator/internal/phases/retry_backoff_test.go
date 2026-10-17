package phases

import (
	"errors"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// A Run whose lux call keeps failing is retried 5 s later, then 10 s, 20 s
// and so on up to 5 minutes, sweep after sweep; a failure long after the
// last retry was due starts over at 5 s; and the forge's wait wins over the
// back-off when longer, and loses to it when shorter.
func TestARetryBacksOffWhileItsFailuresComeInARow(t *testing.T) {
	w := newResumeWorld(t)
	// A failed Run not kept: the sweep asks lux to cancel it, and lux is
	// unavailable.
	r, fake := endedRun(t, w, "failed", "running", false)
	fake.err = map[string]error{r.LuxRunID: &lux.Error{Status: 503, Code: "unavailable"}}
	backoff := func() (time.Duration, int) {
		t.Helper()
		var secs float64
		var failures int
		if err := w.owner.QueryRow(w.ctx, `SELECT EXTRACT(EPOCH FROM next_attempt_at - now())::float8, retry_failures
			FROM runs WHERE id = $1`, r.ID).Scan(&secs, &failures); err != nil {
			t.Fatal(err)
		}
		return time.Duration(secs * float64(time.Second)), failures
	}
	// sweepLate sweeps once the last retry is due and was due dueAgo ago,
	// as a sweep after waiting it out finds it.
	sweepLate := func(dueAgo string, want time.Duration, wantFailures int) {
		t.Helper()
		w.exec(`UPDATE runs SET next_attempt_at = now(), retry_due_at = now() - $2::interval WHERE id = $1`, r.ID, dueAgo)
		calls := len(fake.Calls())
		if _, err := w.s.Sweep(w.ctx); err != nil {
			t.Fatal(err)
		}
		if got := len(fake.Calls()); got != calls+1 {
			t.Fatalf("the sweep asked lux %d times, want once", got-calls)
		}
		got, failures := backoff()
		if got > want+250*time.Millisecond || got < want-2*time.Second || failures != wantFailures {
			t.Fatalf("backed off %v after %d failures in a row, want %v after %d", got, failures, want, wantFailures)
		}
	}
	sweep := func(want time.Duration, wantFailures int) { t.Helper(); sweepLate("30 seconds", want, wantFailures) }
	for i, want := range []time.Duration{5, 10, 20, 40, 80, 160, 300, 300} {
		sweep(want*time.Second, i+1)
	}
	// The retry was due over a minute before this failure: not in a row.
	sweepLate("61 seconds", 5*time.Second, 1)
	sweep(10*time.Second, 2)

	// GitHub's wait, longer than the back-off, is waited out.
	setErr := func(err error) {
		fake.mu.Lock()
		fake.err[r.LuxRunID] = err
		fake.mu.Unlock()
	}
	setErr(&forge.Error{Status: 429, Message: "rate limit", RetryAfter: 10 * time.Minute})
	sweep(10*time.Minute, 3)
	// Shorter than the back-off, the back-off wins.
	setErr(&forge.Error{Status: 429, Message: "rate limit", RetryAfter: time.Second})
	sweep(40*time.Second, 4)
}

// A retry that succeeds ends the streak: the Run's next failure, though
// within a minute of that retry being due, backs off 5 s again.
func TestARetryThatSucceedsEndsTheStreak(t *testing.T) {
	w := newResumeWorld(t)
	r, fake := endedRun(t, w, "failed", "running", false)
	unavailable := map[string]error{r.LuxRunID: &lux.Error{Status: 503, Code: "unavailable"}}
	setErr := func(errs map[string]error) {
		fake.mu.Lock()
		fake.err = errs
		fake.mu.Unlock()
	}
	streak := func() (time.Duration, int) {
		t.Helper()
		var secs float64
		var failures int
		if err := w.owner.QueryRow(w.ctx, `SELECT COALESCE(EXTRACT(EPOCH FROM next_attempt_at - now())::float8, 0), retry_failures
			FROM runs WHERE id = $1`, r.ID).Scan(&secs, &failures); err != nil {
			t.Fatal(err)
		}
		return time.Duration(secs * float64(time.Second)), failures
	}
	sweep := func() {
		t.Helper()
		if _, err := w.s.Sweep(w.ctx); err != nil {
			t.Fatal(err)
		}
	}
	// The retry scheduled is due, as if waited out, 30 s ago.
	due := func() {
		w.exec(`UPDATE runs SET next_attempt_at = now(), retry_due_at = now() - interval '30 seconds' WHERE id = $1`, r.ID)
	}

	setErr(unavailable)
	sweep()
	if got, failures := streak(); failures != 1 || got > 5*time.Second+250*time.Millisecond || got < 3*time.Second {
		t.Fatalf("backed off %v after %d failures, want 5s after 1", got, failures)
	}
	// lux answers the retry: the Run is cancelled.
	setErr(nil)
	due()
	sweep()
	if reason := w.stopReason(); reason != "cancel" {
		t.Fatalf("the retry left lux_stop_reason %q, want cancel", reason)
	}
	if _, failures := streak(); failures != 0 {
		t.Fatalf("%d failures in a row after a retry that succeeded, want 0", failures)
	}
	// The Run needs lux again and lux fails within the minute.
	w.exec(`UPDATE runs SET lux_stop_reason = NULL WHERE id = $1`, r.ID)
	setErr(unavailable)
	sweep()
	if got, failures := streak(); failures != 1 || got > 5*time.Second+250*time.Millisecond || got < 3*time.Second {
		t.Fatalf("backed off %v after %d failures, want 5s after 1: a retry succeeded in between", got, failures)
	}
}

// A retry that fails again through a step returning no error (retryLater's
// callers return nil) stays in the streak: the sweep's reset finds the
// back-off it read already moved on.
func TestARetryThatFailsWithoutAnErrorStaysInTheStreak(t *testing.T) {
	w := newHeldWorld(t, heldExit, 2*time.Minute)
	w.s.Lux.(*eventsLux).err = errors.New("lux unreachable")
	// One failure before, whose retry was due 30 s ago.
	if _, err := w.owner.Exec(w.ctx, `UPDATE runs SET retry_failures = 1, retry_due_at = now() - interval '30 seconds'
		WHERE id = $1`, w.id); err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.Sweep(w.ctx); err != nil {
		t.Fatal(err)
	}
	var secs float64
	var failures int
	if err := w.owner.QueryRow(w.ctx, `SELECT COALESCE(EXTRACT(EPOCH FROM next_attempt_at - now())::float8, 0), retry_failures
		FROM runs WHERE id = $1`, w.id).Scan(&secs, &failures); err != nil {
		t.Fatal(err)
	}
	if got := time.Duration(secs * float64(time.Second)); failures != 2 || got > 10*time.Second+250*time.Millisecond || got < 8*time.Second {
		t.Fatalf("backed off %v after %d failures in a row, want 10s after 2", got, failures)
	}
}
