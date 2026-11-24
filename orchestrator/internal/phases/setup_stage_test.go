package phases

import (
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// A brainstorm's new Run carries no setup; one lux holds with the manifest
// declared keeps being given it.
func TestABrainstormRunsNoSetupUnlessLuxHoldsItsManifest(t *testing.T) {
	if !noSetup(nil) {
		t.Error("a new submission keeps its setup")
	}
	if !noSetup(&lux.StoredSpec{}) {
		t.Error("a Run lux holds with no manifest is given one")
	}
	if noSetup(&lux.StoredSpec{Secrets: []lux.StoredSecret{{Name: setupSecretName}}}) {
		t.Error("a Run lux holds with the manifest is not given it")
	}
	spec, err := runSpec(AgentConfig{}, specInput{RunID: "run_1", Role: "brainstorm", Harness: "opencode",
		Repos: []specRepo{{Name: "abs", URL: "https://github.com/acme/abs.git", Ref: "main", ReadOnly: true, Setup: "npm ci"}}, NoSetup: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, sec := range spec.Secrets {
		if sec.Name == setupSecretName {
			t.Error("a NoSetup spec carries the manifest")
		}
	}
}
