package delivery

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

// A verdict deferred at H keeps its timer whatever signal arrives that
// does not run it: a person's comment on the pull request, their words to
// dude, their decision. The wait still sleeps until the grace's end or the
// cap, never on signals alone (C-M2/P-M1).
func TestADeferredVerdictKeepsItsTimerOnAnUnrelatedSignal(t *testing.T) {
	comment, _ := json.Marshal(forge.Signal{Kind: "actionable", Feedback: []forge.ActionableFeedback{{Source: "review", Body: "nice"}}})
	for _, c := range []struct {
		name string
		sig  workflow.Signal
	}{
		{"human.feedback", workflow.Signal{Name: SignalHumanFeedback}},
		{"human.decision", workflow.Signal{Name: SignalHumanDecision}},
		{"pr.feedback", workflow.Signal{Name: SignalPRFeedback, Payload: comment}},
	} {
		t.Run(c.name, func(t *testing.T) {
			now := time.Now()
			at := now.Add(time.Hour)
			st := &State{TaskID: "t", Source: &Source{Mode: ModeReviewOnly},
				Verdict: &VerdictState{Due: "abc", DeferredSince: &now}, ReweighAt: &at}
			raw, _ := json.Marshal(st)
			res, err := (&steps{}).awaitReviewOnly(context.Background(), workflow.StepContext{State: raw, Signals: []workflow.Signal{c.sig}}, st)
			if err != nil {
				t.Fatal(err)
			}
			if res.Next != "awaitPullRequest" {
				t.Fatalf("next %q: an unrelated signal ran the verdict", res.Next)
			}
			if st.ReweighAt == nil || !st.ReweighAt.Equal(at) || !res.SleepUntil.Equal(at) {
				t.Fatalf("sleepUntil=%v reweighAt=%v: the deferred verdict's timer is gone, want %v", res.SleepUntil, st.ReweighAt, at)
			}
		})
	}
	// No verdict owed: nothing to wake for, the timer goes.
	at := time.Now().Add(time.Hour)
	st := &State{TaskID: "t", Source: &Source{Mode: ModeReviewOnly}, Verdict: &VerdictState{}, ReweighAt: &at}
	res, err := (&steps{}).awaitReviewOnly(context.Background(), workflow.StepContext{Signals: []workflow.Signal{{Name: SignalHumanFeedback}}}, st)
	if err != nil || st.ReweighAt != nil || !res.SleepUntil.IsZero() {
		t.Fatalf("no verdict due: reweighAt=%v sleepUntil=%v err=%v", st.ReweighAt, res.SleepUntil, err)
	}
}
