package phases

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

func llmSpec(model, effort string) lux.Spec {
	c := AgentConfig{LLMURL: "https://llm.example/v1", LLMKey: "sk-secret-key"}
	return buildSpec(c, specInput{RunID: "run_1", TaskID: "wi_1", Phase: "implement", Model: model, Effort: effort})
}

// The key reaches the agent only as a lux secret in its environment: never
// in the spec's plain env or labels, which lux stores and shows.
func TestTheLLMKeyIsAnEnvSecretAndNowhereElse(t *testing.T) {
	spec := llmSpec("llm/impl", "high")
	i := slices.IndexFunc(spec.Secrets, func(s lux.Secret) bool { return s.Name == "DUDE_LLM_KEY" })
	if i < 0 {
		t.Fatalf("secrets = %+v, want DUDE_LLM_KEY", spec.Secrets)
	}
	if s := spec.Secrets[i]; s.As != "env" || s.Value != "sk-secret-key" || s.Path != "" {
		t.Errorf("DUDE_LLM_KEY = %+v, want the key delivered as an env var", s)
	}
	for k, v := range spec.Env {
		if strings.Contains(v, "sk-secret-key") {
			t.Errorf("env %s holds the key", k)
		}
	}
	if _, ok := spec.Env["DUDE_LLM_KEY"]; ok {
		t.Error("DUDE_LLM_KEY is plain env")
	}
	for k, v := range spec.Labels {
		if strings.Contains(v, "sk-secret-key") {
			t.Errorf("label %s holds the key", k)
		}
	}
	if spec.Env["DUDE_LLM_URL"] != "https://llm.example/v1" {
		t.Errorf("DUDE_LLM_URL = %q", spec.Env["DUDE_LLM_URL"])
	}
	for _, s := range spec.Secrets {
		if s.As == "file" {
			t.Errorf("secret %s is a file: the image holds OpenCode's config", s.Name)
		}
	}
}

func TestTheModelAndEffortAreInlineOpenCodeConfig(t *testing.T) {
	for _, tc := range []struct {
		effort string
		want   map[string]any
	}{
		{"high", map[string]any{"model": "llm/impl", "agent": map[string]any{"build": map[string]any{"reasoningEffort": "high"}}}},
		{"low", map[string]any{"model": "llm/impl", "agent": map[string]any{"build": map[string]any{"reasoningEffort": "low"}}}},
		{"max", map[string]any{"model": "llm/impl", "agent": map[string]any{"build": map[string]any{"reasoningEffort": "high"}}}},
		{"", map[string]any{"model": "llm/impl"}},
	} {
		spec := llmSpec("llm/impl", tc.effort)
		var got map[string]any
		if err := json.Unmarshal([]byte(spec.Env["OPENCODE_CONFIG_CONTENT"]), &got); err != nil {
			t.Fatalf("effort %q: OPENCODE_CONFIG_CONTENT = %q: %v", tc.effort, spec.Env["OPENCODE_CONFIG_CONTENT"], err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("effort %q: config = %v, want %v", tc.effort, got, tc.want)
		}
	}
}

// Building one Run's env must not change the next one's.
func TestRunEnvsAreTheirOwn(t *testing.T) {
	a := llmSpec("llm/a", "")
	b := llmSpec("llm/b", "")
	if a.Env["OPENCODE_CONFIG_CONTENT"] == b.Env["OPENCODE_CONFIG_CONTENT"] {
		t.Errorf("both Runs have %s", a.Env["OPENCODE_CONFIG_CONTENT"])
	}
	if _, ok := colourEnv["OPENCODE_CONFIG_CONTENT"]; ok {
		t.Error("the shared colour env was written to")
	}
}

func TestEgressAllowsTheLLMURLsHost(t *testing.T) {
	hosts := func(n *lux.Network) (hs []string) {
		for _, e := range n.Egress {
			hs = append(hs, e.Host+e.CIDR)
		}
		slices.Sort(hs)
		return hs
	}
	n := egress(AgentConfig{LLMURL: "https://llmproxy.example.com:8443/v1", ToolsURL: "http://10.0.0.5:3120/mcp"})
	if n.Unrestricted || !reflect.DeepEqual(hosts(n), []string{"10.0.0.5/32", "llmproxy.example.com"}) {
		t.Errorf("egress = %+v, want the LLM host and the tools", n)
	}
	n = egress(AgentConfig{LLMURL: "https://llm.example/v1", Egress: []string{"pypi.org"}})
	if !reflect.DeepEqual(hosts(n), []string{"llm.example", "pypi.org"}) {
		t.Errorf("egress = %+v", n)
	}
	if n := egress(AgentConfig{LLMURL: "https://llm.example/v1", Egress: []string{"*"}}); !n.Unrestricted {
		t.Errorf("egress = %+v, want * to turn filtering off", n)
	}
	if n := egress(AgentConfig{ToolsURL: "http://10.0.0.5:3120/mcp"}); !n.Unrestricted {
		t.Errorf("egress = %+v, want no filtering with nothing to restrict to", n)
	}
}

// The scripted agent neither needs nor gets the LLM's URL or key.
func TestTheScriptedAgentGetsNoLLM(t *testing.T) {
	spec := llmSpec("fake/scripted", "high")
	if len(spec.Env) != 0 || slices.ContainsFunc(spec.Secrets, func(s lux.Secret) bool { return s.Name == "DUDE_LLM_KEY" }) {
		t.Errorf("env %v secrets %+v", spec.Env, spec.Secrets)
	}
}
