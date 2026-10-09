package phases

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite testdata/*.golden from the current buildSpec")

// goldenInput is a phase Run with every part of the spec in play: two
// repositories (one read only), a push branch, the forge token and dude's
// tools. One egress host only, so the rules' order is fixed.
func goldenInput(model string) (AgentConfig, specInput) {
	c := AgentConfig{
		LLMURL:       "https://llm.example/v1",
		LLMKey:       "sk-golden",
		DefaultImage: "registry.example/dude/agent:1",
		ToolsURL:     "http://10.9.8.7:3120/mcp",
		ToolsService: true,
	}
	in := specInput{
		RunID: "run_1", OrganizationID: "org_1", TaskID: "wi_1", Phase: "implement", Role: "implementer",
		Image: "registry.example/dude/agent:1", Model: model, ModelTier: "Coder", Prompt: "Do the thing.",
		Repos: []specRepo{
			{Name: "api", URL: "https://github.com/acme/api.git", Ref: "main"},
			{Name: "web", URL: "https://github.com/acme/web.git", Ref: "abc123", ReadOnly: true},
		},
		PushBranch: "dude/wi_1/run-run_1", ForgeToken: "ghp_golden", ToolsToken: "tok_golden",
	}
	return c, in
}

// What dude sends lux for a phase Run, byte for byte: a change to the spec
// shows up here as a diff to read, not as a silent change on the wire.
func TestTheSpecIsTheGoldenOne(t *testing.T) {
	for name, model := range map[string]string{"opencode": "claude-opus-5-5", "scripted": "fake/scripted", "registry": "claude-opus-5-5",
		"network": "claude-opus-5-5", "scripted-network": "fake/scripted"} {
		t.Run(name, func(t *testing.T) {
			c, in := goldenInput(model)
			if name == "registry" {
				in.Registry = &RegistryLogin{Registry: "registry.example", Credential: "AWS:pw-golden"}
			}
			if name == "network" || name == "scripted-network" {
				// The operator's floor, then the Run's own list: a host, a
				// wildcard, an address, a range, and one the operator
				// already has. The scripted agent stands in for one that
				// reaches them, with no model to reach.
				c.Egress = []string{"mirror.internal"}
				in.Egress = []string{"pypi.org", "*.github.com", "10.0.0.5", "10.60.0.0/16", "mirror.internal"}
			}
			got, err := json.MarshalIndent(buildSpec(c, in), "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, '\n')
			path := filepath.Join("testdata", "spec-"+name+".golden")
			if *update {
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != string(want) {
				t.Errorf("spec differs from %s:\n got: %s\nwant: %s", path, got, want)
			}
		})
	}
}

// A Run whose image cannot run containers must not wait for a host
// offering them; the operator's fallback flag alone does not ask either:
// the image the Run resolved decides (images.Site.Containers).
func TestNestedContainersAreAskedForOnlyWhenTheImageCan(t *testing.T) {
	// The scripted harness returns early without tools or egress, so each
	// shape is its own case; "egress" also pins that the sandbox and the
	// Run's allowlist ride the same spec.
	shapes := map[string]func(*AgentConfig, *specInput){
		"tools":    func(*AgentConfig, *specInput) {},
		"no tools": func(c *AgentConfig, in *specInput) { c.ToolsURL, in.ToolsToken = "", "" },
		"no tools, egress": func(c *AgentConfig, in *specInput) {
			c.ToolsURL, in.ToolsToken, in.Egress = "", "", []string{"pypi.org"}
		},
	}
	for _, name := range []string{"claude-opus-5-5", "fake/scripted"} {
		for shape, edit := range shapes {
			for _, set := range []bool{false, true} {
				c, in := goldenInput(name)
				edit(&c, &in)
				model := name + " (" + shape + ")"
				c.NestedContainers = !set
				in.NestedContainers = set
				b, err := json.Marshal(buildSpec(c, in))
				if err != nil {
					t.Fatal(err)
				}
				// lux reads sandbox at the spec's top level.
				var wire map[string]json.RawMessage
				if err := json.Unmarshal(b, &wire); err != nil {
					t.Fatal(err)
				}
				sandbox, present := wire["sandbox"]
				if want := `{"nestedContainers":true}`; set && string(sandbox) != want {
					t.Errorf("%s set: sandbox = %s, want %s", model, sandbox, want)
				}
				if !set && present {
					t.Errorf("%s unset: sandbox = %s", model, sandbox)
				}
				if len(in.Egress) > 0 && !strings.Contains(string(wire["network"]), `"host":"pypi.org"`) {
					t.Errorf("%s: network = %s, want the Run's pypi.org beside the sandbox", model, wire["network"])
				}
			}
		}
	}
}
