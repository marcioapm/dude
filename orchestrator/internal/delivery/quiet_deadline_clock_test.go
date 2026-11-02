package delivery

import (
	"testing"
	"time"
)

// A quiet decision's deadline is weighed against the clock it was set by,
// the database's (dbNowTx), not the host's: a database ahead of the host
// past the deadline defaults it, where the host's clock would re-park it
// at a wake the runtime claims again on every poll; and a hand-back's
// signal an hour before the deadline waits, whatever the host says.
func TestTheQuietDeadlineIsWeighedByTheGivenClock(t *testing.T) {
	host := time.Now()
	by := host.Add(30 * time.Minute)
	d := &Pending{DecideBy: &by}
	// The database is an hour ahead of the host: its deadline has passed.
	if got := defaultedReason(d, false, false, host.Add(time.Hour)); got != DefaultedDeadline {
		t.Errorf("an hour past the deadline by the database's clock: %q, want %q", got, DefaultedDeadline)
	}
	// An hour before it: the conductor still has its time.
	if got := defaultedReason(d, false, false, by.Add(-time.Hour)); got != "" {
		t.Errorf("an hour before the deadline: %q, want to wait", got)
	}
	if got := defaultedReason(d, false, false, by); got != DefaultedDeadline {
		t.Errorf("at the deadline: %q, want %q", got, DefaultedDeadline)
	}
	// What is not the clock's goes first, deadline or not.
	for _, c := range []struct {
		d               *Pending
		noModel, failed bool
		want            string
	}{
		{&Pending{DecideBy: &by, Accepted: true}, true, true, DefaultedAccepted},
		{d, true, true, DefaultedNoModel},
		{d, false, true, DefaultedRunFailed},
	} {
		if got := defaultedReason(c.d, c.noModel, c.failed, by.Add(-time.Hour)); got != c.want {
			t.Errorf("before the deadline, no model %v, failed %v, accepted %v: %q, want %q",
				c.noModel, c.failed, c.d.Accepted, got, c.want)
		}
	}
}
