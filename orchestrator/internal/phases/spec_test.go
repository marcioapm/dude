package phases

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

var update = flag.Bool("update", false, "rewrite testdata/*.golden from the current buildSpec")

// goldenInput is a phase Run with every part of the spec in play: two
// repositories (one read only), a push branch, the forge token and dude's
// tools. One egress host only, so the rules' order is fixed.
func goldenInput(model string) (AgentConfig, specInput) {
	c := AgentConfig{
		OpenCodeAuth:      `{"llm":{"type":"api","key":"sk-golden"}}`,
		OpenCodeProviders: json.RawMessage(`{"llm":{"options":{"baseURL":"https://llm.example/v1"}}}`),
		DefaultImage:      "registry.example/dude/agent:1",
		ToolsURL:          "http://10.9.8.7:3120/mcp",
		ToolsService:      true,
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
	for name, model := range map[string]string{"opencode": "llm/impl", "scripted": "fake/scripted"} {
		t.Run(name, func(t *testing.T) {
			c, in := goldenInput(model)
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

// A login adds image.registryAuth and its secret, and changes nothing else.
func TestARegistryLoginAddsRegistryAuthAndItsSecretOnly(t *testing.T) {
	for _, model := range []string{"llm/impl", "fake/scripted"} {
		c, in := goldenInput(model)
		without := buildSpec(c, in)
		in.Registry = &RegistryLogin{Registry: "registry.example", Credential: "AWS:pw-golden"}
		with := buildSpec(c, in)

		want := []lux.RegistryAuth{{Registry: "registry.example", Secret: "DUDE_REGISTRY_AUTH"}}
		if !reflect.DeepEqual(with.Image.RegistryAuth, want) {
			t.Errorf("%s: registryAuth = %+v, want %+v", model, with.Image.RegistryAuth, want)
		}
		added := []lux.Secret{{Name: "DUDE_REGISTRY_AUTH", Value: "AWS:pw-golden"}}
		if !reflect.DeepEqual(with.Secrets, append(added, without.Secrets...)) {
			t.Errorf("%s: secrets = %+v, want the login's and then %+v", model, with.Secrets, without.Secrets)
		}
		with.Image.RegistryAuth, with.Secrets = nil, without.Secrets
		if !reflect.DeepEqual(with, without) {
			t.Errorf("%s: the login changed more than image.registryAuth and secrets", model)
		}
	}
}
