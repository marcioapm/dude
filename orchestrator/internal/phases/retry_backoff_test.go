package phases

import (
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// A Run whose lux call keeps failing is retried 5 s later, then 10 s, 20 s
// and so on up to 5 minutes, sweep after sweep; a failure long after the
// last retry was due starts over at 5 s; and a longer wait the forge asked
// for wins over the back-off.
func TestARetryBacksOffWhileItsFailuresComeInARow(t *testing.T) {
	w := newResumeWorld(t)
	// A failed Run not kept: the sweep asks lux to cancel it, and lux is
	// unavailable.
	r, fake := endedRun(t, w, "failed", "running", false)
	fake.err = map[string]error{r.LuxRunID: &lux.Error{Status: 503, Code: "unavailable"}}
	// Every back-off is due at once, as if waited out.
	w.s.RetryAhead = time.Hour
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
	sweep := func(want time.Duration, wantFailures int) {
		t.Helper()
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
	for i, want := range []time.Duration{5, 10, 20, 40, 80, 160, 300, 300} {
		sweep(want*time.Second, i+1)
	}
	// The retry was due over a minute before this failure: not in a row.
	w.exec(`UPDATE runs SET retry_due_at = now() - interval '61 seconds' WHERE id = $1`, r.ID)
	sweep(5*time.Second, 1)
	sweep(10*time.Second, 2)

	// GitHub's wait, longer than the back-off, is waited out.
	fake.mu.Lock()
	fake.err[r.LuxRunID] = &forge.Error{Status: 429, Message: "rate limit", RetryAfter: 10 * time.Minute}
	fake.mu.Unlock()
	sweep(10*time.Minute, 3)
}
