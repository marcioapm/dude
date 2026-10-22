package delivery

import (
	"encoding/json"
	"os"
	"testing"
)

// A vocabulary check, not behaviour: packages/domain/test/reviewQuestionEvents.test.ts
// reads the same rows, so an event type renamed on one side only fails.
func TestReviewQuestionEventTypeNamesEqualTheSharedFixture(t *testing.T) {
	raw, err := os.ReadFile("../../../packages/domain/test/review-question-event-types.json")
	if err != nil {
		t.Fatal(err)
	}
	var want map[string]string
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{"ReviewQuestionRaised": EvReviewQuestionRaised,
		"ReviewQuestionAnswered": EvReviewQuestionAnswered, "ReviewQuestionWithdrawn": EvReviewQuestionWithdrawn}
	if len(got) != len(want) {
		t.Fatalf("fixture has %d types, delivery %d", len(want), len(got))
	}
	for name, v := range want {
		if got[name] != v {
			t.Errorf("%s = %q, want %q", name, got[name], v)
		}
	}
}
