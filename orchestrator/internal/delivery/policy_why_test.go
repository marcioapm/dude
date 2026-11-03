package delivery

import (
	"encoding/json"
	"maps"
	"os"
	"testing"
)

// A vocabulary check, not behaviour: packages/domain/test/policyWhy.test.ts
// reads the same rows (the domain's POLICY_WHY, which the control plane's
// task read matches), so a why reworded on one side only fails.
func TestPolicyWhysEqualTheSharedFixture(t *testing.T) {
	raw, err := os.ReadFile("../../../packages/domain/test/policy-why.json")
	if err != nil {
		t.Fatal(err)
	}
	var want map[string]string
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{"accepted": WhyAcceptedDefaults, "no_model": WhyNoConductorModel}
	if !maps.Equal(got, want) {
		t.Errorf("delivery's whys %q, the fixture's %q", got, want)
	}
}
