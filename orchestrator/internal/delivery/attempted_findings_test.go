package delivery_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// What the next review round is asked to judge. A review-only task's
// round judges every open finding and every one the conductor kept as a
// suggestion, so a kept one the author fixes can be judged fixed; never
// one withdrawn as wrong or a duplicate, nor one a person accepted. A
// writing task's is, as before, the open findings a fix was sent.
func TestAttemptedFindingsOfAReviewOnlyTaskIncludeItsKeptSuggestions(t *testing.T) {
	w := newConvergenceWorld(t) // f: open, no fix sent
	for _, c := range []struct{ id, status, note string }{
		{"kept", "accepted", delivery.DismissedByConductor + "suggestion: gate 2: only DEV_MODE=1 reaches it, cmd/dev.go:12."},
		{"wrong", "accepted", delivery.DismissedByConductor + "wrong: the caller checks it, api/x.go:4."},
		{"duplicate", "accepted", delivery.DismissedByConductor + "duplicate: f"},
		{"person", "accepted", "accepted by a person: suggestion: gate 2: only DEV_MODE=1 reaches it."},
		{"bad_gate", "accepted", delivery.DismissedByConductor + "suggestion: gate 1: no such gate to keep it for"},
		{"fixed", "resolved", "judged fixed by the re-review"},
		{"sent", "open", ""},
	} {
		w.finding(t, c.id, "origin", "medium", false)
		w.exec(t, `UPDATE review_findings SET status = $2::finding_status, resolution_note = $3 WHERE id = $1`, c.id, c.status, c.note)
	}
	w.exec(t, `UPDATE review_findings SET fix_attempts = 1 WHERE id IN ('sent', 'kept', 'wrong')`)
	s := &delivery.Store{DB: w.app}
	for _, tc := range []struct {
		name string
		src  *delivery.Source
		want []string
	}{
		{"review only", &delivery.Source{Mode: delivery.ModeReviewOnly}, []string{"f", "kept", "sent"}},
		{"writing", nil, []string{"sent"}},
	} {
		st := w.state()
		st.Source = tc.src
		got, err := s.AttemptedFindings(context.Background(), w.org, st)
		if err != nil {
			t.Fatal(err)
		}
		if want := map[string][]string{"correctness": tc.want}; !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %v, want %v", tc.name, got, want)
		}
	}
}
