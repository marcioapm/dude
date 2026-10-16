package phases

import (
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/fakeagent"
)

// A scripted integration fixer on a real lux is lux-fake running the
// integration script, in the checkout of the repository its brief names;
// an ordinary scripted fix keeps fakeagent's script.
func TestAScriptedIntegrationFixRunsTheIntegrationScript(t *testing.T) {
	c, in := goldenInput("fake/scripted")
	in.Phase, in.Role = "fix", "implementer"
	in.Prompt = "Base integration: this branch conflicts with `release` in web. Call sync_base, then `git merge lux/release`."
	spec := buildSpec(c, in)
	if spec.Labels["dude.integration"] != "true" {
		t.Fatalf("labels %v", spec.Labels)
	}
	if want := fakeagent.IntegrationScript(in.Prompt, "/workspace/repos/web"); spec.Workload.Prompt != want {
		t.Fatalf("prompt:\n%s\nwant:\n%s", spec.Workload.Prompt, want)
	}
	if !strings.Contains(spec.Workload.Prompt, "git merge --no-edit lux/release") {
		t.Fatalf("prompt does not merge the named base: %s", spec.Workload.Prompt)
	}

	in.Prompt = "Rename greet to welcome."
	if spec := buildSpec(c, in); strings.Contains(spec.Workload.Prompt, "sync_base") || spec.Labels["dude.integration"] != "" {
		t.Fatalf("an ordinary fix got the integration script: %s", spec.Workload.Prompt)
	}
}
