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
		Image: "registry.example/dude/agent:1", Model: model, Prompt: "Do the thing.",
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
	for name, model := range map[string]string{"opencode": "llm/impl", "scripted": "fake/scripted", "registry": "llm/impl"} {
		t.Run(name, func(t *testing.T) {
			c, in := goldenInput(model)
			if name == "registry" {
				in.Registry = &RegistryLogin{Registry: "registry.example", Credential: "AWS:pw-golden"}
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

// agent.nested_containers asks lux for a sandbox that runs containers, on
// agent Runs only when set: lux places such a Run only on a host offering
// them, so an unset operator must not get Runs that wait for one.
func TestNestedContainersAreAskedForOnlyWhenSet(t *testing.T) {
	for _, model := range []string{"llm/impl", "fake/scripted"} {
		c, in := goldenInput(model)
		if sp := buildSpec(c, in); sp.Sandbox != nil {
			t.Errorf("%s: unset, sandbox = %+v", model, *sp.Sandbox)
		}
	}
	c, in := goldenInput("llm/impl")
	c.NestedContainers = true
	sp := buildSpec(c, in)
	if sp.Sandbox == nil || !sp.Sandbox.NestedContainers {
		t.Fatalf("set: sandbox = %+v", sp.Sandbox)
	}
	b, _ := json.Marshal(sp)
	if !strings.Contains(string(b), `"sandbox":{"nestedContainers":true}`) {
		t.Errorf("wire form: %s", b)
	}
}
