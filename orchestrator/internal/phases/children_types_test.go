package phases

import (
	"encoding/json"
	"os"
	"testing"
)

// The rows packages/domain/test/childEvents.test.ts reads too: the event
// types the translator writes for child sessions are the browser's.
func TestChildEventTypesMatchTheSharedFixture(t *testing.T) {
	raw, err := os.ReadFile("../../../packages/domain/test/child-event-types.json")
	if err != nil {
		t.Fatal(err)
	}
	var want map[string]string
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{
		"ChildStarted": evChildStarted, "ChildToolCalled": evChildToolCalled, "ChildToolCompleted": evChildToolCompleted,
		"ChildMessage": evChildMessage, "ChildWaiting": evChildWaiting, "ChildEnded": evChildEnded,
		"ToolCalled": evToolCalled, "ToolCompleted": evToolCompleted,
	}
	if len(got) != len(want) {
		t.Fatalf("fixture has %d types, the translator %d", len(want), len(got))
	}
	for name, v := range want {
		if got[name] != v {
			t.Errorf("%s = %q, want %q", name, got[name], v)
		}
	}
}
