package delivery

import (
	"encoding/json"
	"reflect"
	"testing"
)

// reviewLoop was a setting once (policy or conductor); a value an
// organisation, a project or a delivery's frozen policy still holds is
// inert. It resolves without error to what it would without the key, and a
// conducted delivery frozen with "policy" — one in flight when the setting
// went — runs the conducted loop on its next round; a Deliver one keeps the
// policy's.
func TestAStoredReviewLoopIsInertAndAnInFlightConductedDeliveryRunsTheConductedLoop(t *testing.T) {
	stored := json.RawMessage(`{"reviewLoop":"policy"}`)
	want, err := ResolvePolicy(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, layers := range map[string][]json.RawMessage{
		"organisation": {stored, nil},
		"project":      {nil, stored},
		"both":         {stored, stored},
		"unknown":      {json.RawMessage(`{"reviewLoop":"later"}`), json.RawMessage(`{"reviewLoop":"later"}`)},
	} {
		got, err := ResolvePolicy(layers...)
		if err != nil {
			t.Errorf("%s: %v", name, err)
		}
		if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(got.ForNewDelivery(), want.ForNewDelivery()) {
			t.Errorf("%s: resolves to %+v, want %+v", name, got, want)
		}
	}

	for _, c := range []struct {
		decider   string
		conducted bool
	}{{DeciderConductor, true}, {DeciderPolicy, false}, {"", false}} {
		var st State
		frozen := `{"taskId":"t","decider":"` + c.decider + `","policy":{"maxReviewIterations":5,"pullRequestFixes":"candidate","reviewLoop":"policy"}}`
		if err := json.Unmarshal([]byte(frozen), &st); err != nil {
			t.Fatalf("decider %q: %v", c.decider, err)
		}
		if st.ConductedLoop() != c.conducted {
			t.Errorf("decider %q, frozen reviewLoop policy: ConductedLoop = %v, want %v", c.decider, st.ConductedLoop(), c.conducted)
		}
		if st.Policy.MaxReviewIterations != 5 || st.Policy.PullRequestFixes != PullRequestFixesCandidate {
			t.Errorf("decider %q: the rest of the frozen policy is lost: %+v", c.decider, st.Policy)
		}
	}
}
