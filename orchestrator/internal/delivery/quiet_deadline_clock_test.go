package delivery

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

// A quiet decision's deadline is weighed against the clock it was set by,
// the database's (quietFactsTx), not the host's: a database ahead of the host
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
		{d, true, true, DefaultedNoModel},
		{d, false, true, DefaultedRunFailed},
	} {
		if got := defaultedReason(c.d, c.noModel, c.failed, by.Add(-time.Hour)); got != c.want {
			t.Errorf("before the deadline, no model %v, failed %v: %q, want %q",
				c.noModel, c.failed, got, c.want)
		}
	}
}

// quietParked is a quiet conductor's decision after the review, its wait
// long lapsed, as a delivery stopped or escalated there holds it.
func quietParked() *State {
	since, by := time.Now().Add(-2*time.Hour), time.Now().Add(-time.Hour)
	return &State{Decider: DeciderConductor, HadConductor: true, Voice: VoiceQuiet,
		Decision: &Pending{Point: PointReviewed, Policy: "reviewExit", Since: &since, DecideBy: &by}}
}

// A checks escalation resumed at a quiet decision runs the decision's step
// at once, its wait forgotten: quietDecision starts it afresh, its
// deadline with it, which the saved signals alone would lose (no wake).
func TestAResumeAtAQuietDecisionRestartsItsWait(t *testing.T) {
	st := quietParked()
	res := resumeEscalationAt(st, &Escalation{Reason: ReasonChecksUnverified, At: "conductorDecision",
		AtSignals: []string{SignalConductorDecision}})
	if res.Next != "conductorDecision" || len(res.AwaitSignals) != 0 {
		t.Fatalf("resumed to %q awaiting %v, want the decision's step at once", res.Next, res.AwaitSignals)
	}
	if st.Decision.Since != nil || st.Decision.DecideBy != nil {
		t.Errorf("the lapsed wait was kept: decide by %v", st.Decision.DecideBy)
	}
}

// A stopped quiet decision picked back up (resume) waits its whole time
// again: its lapsed deadline does not have the defaults take it at once.
func TestAQuietDecisionPickedBackUpWaitsAfresh(t *testing.T) {
	st := quietParked()
	st.Recover = &Recover{Action: "resume", At: "conductorDecision"}
	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	res, err := (&steps{s: &Store{}}).recover(context.Background(), workflow.StepContext{Step: "recover", State: raw})
	if err != nil {
		t.Fatal(err)
	}
	got := res.State.(*State)
	if res.Next != "conductorDecision" {
		t.Fatalf("resumed to %q", res.Next)
	}
	if got.Decision.Since != nil || got.Decision.DecideBy != nil {
		t.Errorf("the lapsed wait was kept: decide by %v", got.Decision.DecideBy)
	}
}
