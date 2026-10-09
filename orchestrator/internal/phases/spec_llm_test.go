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
	spec := llmSpec("claude-opus-5-5", "high")
	i := slices.IndexFunc(spec.Secrets, func(s lux.Secret) bool { return s.Name == "DUDE_LLM_KEY" })
	if i < 0 {
		t.Fatalf("secrets = %+v, want DUDE_LLM_KEY", spec.Secrets)
	}
	if s := spec.Secrets[i]; s.As != "env" || s.Value != "sk-secret-key" || s.Path != "" {
		t.Errorf("DUDE_LLM_KEY = %+v, want the key delivered as an env var", s)
	}
	for where, m := range map[string]map[string]string{"env": spec.Env, "label": spec.Labels} {
		for k, v := range m {
			if strings.Contains(v, "sk-secret-key") {
				t.Errorf("%s %s holds the key", where, k)
			}
		}
	}
	if _, ok := spec.Env["DUDE_LLM_KEY"]; ok {
		t.Error("DUDE_LLM_KEY is plain env")
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

// A Run's OpenCode config names the model under the provider its name goes
// through, and declares it there, so a model the image's file does not list
// still resolves; the effort is the agent's reasoningEffort, max as high.
func TestTheModelAndEffortAreInlineOpenCodeConfig(t *testing.T) {
	declare := func(provider, model string) map[string]any {
		return map[string]any{provider: map[string]any{"models": map[string]any{model: map[string]any{}}}}
	}
	effort := func(e string) map[string]any {
		return map[string]any{"build": map[string]any{"reasoningEffort": e}}
	}
	for _, tc := range []struct {
		model, effort string
		want          map[string]any
	}{
		{"claude-opus-5-5", "high", map[string]any{"model": "llm-anthropic/claude-opus-5-5",
			"provider": declare("llm-anthropic", "claude-opus-5-5"), "agent": effort("high")}},
		// Not in the image's opencode.json: declared all the same.
		{"claude-nova-7", "low", map[string]any{"model": "llm-anthropic/claude-nova-7",
			"provider": declare("llm-anthropic", "claude-nova-7"), "agent": effort("low")}},
		{"gpt-5.6-sol", "max", map[string]any{"model": "llm-openai/gpt-5.6-sol",
			"provider": declare("llm-openai", "gpt-5.6-sol"), "agent": effort("high")}},
		{"gemini-3.8-pro", "", map[string]any{"model": "llm-openai/gemini-3.8-pro",
			"provider": declare("llm-openai", "gemini-3.8-pro")}},
		// Only a name starting claude- is Anthropic's.
		{"my-claude-proxy", "", map[string]any{"model": "llm-openai/my-claude-proxy",
			"provider": declare("llm-openai", "my-claude-proxy")}},
	} {
		spec := llmSpec(tc.model, tc.effort)
		var got map[string]any
		if err := json.Unmarshal([]byte(spec.Env["OPENCODE_CONFIG_CONTENT"]), &got); err != nil {
			t.Fatalf("%s: OPENCODE_CONFIG_CONTENT = %q: %v", tc.model, spec.Env["OPENCODE_CONFIG_CONTENT"], err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s effort %q: config = %v, want %v", tc.model, tc.effort, got, tc.want)
		}
		if spec.Labels["dude.model"] != tc.model {
			t.Errorf("%s: label dude.model = %q, want the model as the proxy names it", tc.model, spec.Labels["dude.model"])
		}
	}
}

func TestATiersRunIsLabelledWithTheTier(t *testing.T) {
	c := AgentConfig{LLMURL: "https://llm.example/v1"}
	spec := buildSpec(c, specInput{RunID: "run_1", TaskID: "wi_1", Phase: "review", Model: "claude-fable-5-1", ModelTier: "Thinker"})
	if spec.Labels["dude.model_tier"] != "Thinker" || spec.Labels["dude.model"] != "claude-fable-5-1" {
		t.Errorf("labels = %v", spec.Labels)
	}
	if _, ok := buildSpec(c, specInput{Phase: "review", Model: "claude-fable-5-1"}).Labels["dude.model_tier"]; ok {
		t.Error("a Run with no tier is labelled with one")
	}
}

// lux names the tool behind a cost by the app label: a phase Run and a
// session both carry it, though a session drops dude.task.
func TestEveryRunIsLabelledAsDudes(t *testing.T) {
	c := AgentConfig{LLMURL: "https://llm.example/v1"}
	for _, in := range []specInput{
		{RunID: "run_1", TaskID: "wi_1", Phase: "implement", Model: "claude-fable-5-1"},
		{RunID: "run_1", TaskID: "wi_1", SessionID: "ses_1", Model: "claude-fable-5-1"},
	} {
		if got := buildSpec(c, in).Labels[lux.AppLabel]; got != lux.App {
			t.Errorf("%+v: app label = %q", in, got)
		}
	}
}

// Building one Run's env must not change the next one's.
func TestRunEnvsAreTheirOwn(t *testing.T) {
	a := llmSpec("claude-a", "")
	b := llmSpec("claude-b", "")
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
	n := egress(AgentConfig{LLMURL: "https://llmproxy.example.com:8443/v1", ToolsURL: "http://10.0.0.5:3120/mcp"}, nil)
	if n.Unrestricted || !reflect.DeepEqual(hosts(n), []string{"10.0.0.5/32", "llmproxy.example.com"}) {
		t.Errorf("egress = %+v, want the LLM host and the tools", n)
	}
	n = egress(AgentConfig{LLMURL: "https://llm.example/v1", Egress: []string{"pypi.org"}}, nil)
	if !reflect.DeepEqual(hosts(n), []string{"llm.example", "pypi.org"}) {
		t.Errorf("egress = %+v", n)
	}
	if n := egress(AgentConfig{LLMURL: "https://llm.example/v1", Egress: []string{"*"}}, nil); !n.Unrestricted {
		t.Errorf("egress = %+v, want * to turn filtering off", n)
	}
	if n := egress(AgentConfig{ToolsURL: "http://10.0.0.5:3120/mcp"}, nil); !n.Unrestricted {
		t.Errorf("egress = %+v, want no filtering with nothing to restrict to", n)
	}
}

// A Run's own list (its organisation's and project's) goes on top of the
// operator's floor: hosts, wildcards and ranges as lux takes them, each
// once, and "*" in either turns filtering off.
func TestARunsEgressIsTheOperatorsFloorAndItsOwnList(t *testing.T) {
	hosts := func(n *lux.Network) (hs []string) {
		for _, e := range n.Egress {
			hs = append(hs, e.Host+e.CIDR)
		}
		slices.Sort(hs)
		return hs
	}
	c := AgentConfig{LLMURL: "https://llm.example/v1", Egress: []string{"mirror.internal"}}
	n := egress(c, []string{"*.github.com", "10.60.0.0/16", "10.0.0.5", "mirror.internal", "Registry.npmjs.org"})
	if want := []string{"*.github.com", "10.0.0.5/32", "10.60.0.0/16", "llm.example", "mirror.internal", "registry.npmjs.org"}; n.Unrestricted ||
		!reflect.DeepEqual(hosts(n), want) {
		t.Errorf("egress = %v, want %v", hosts(n), want)
	}
	if n := egress(c, []string{"pypi.org", "*"}); !n.Unrestricted {
		t.Errorf("egress = %+v, want the Run's * to turn filtering off", n)
	}
	// Without a model's host, a Run's own list is still something to
	// restrict to: only nothing anywhere leaves it unrestricted.
	if n := egress(AgentConfig{}, []string{"pypi.org"}); n.Unrestricted || !reflect.DeepEqual(hosts(n), []string{"pypi.org"}) {
		t.Errorf("egress = %+v, want the Run's list", n)
	}
}

// Which lists a Run gets: its organisation's and its project's, or with
// 'only' its project's alone.
func TestARunsListIsItsOrganisationsAndProjectsOrTheProjectsOnly(t *testing.T) {
	org, project := []string{"github.com", "pypi.org"}, []string{"pypi.org", "10.60.0.0/16"}
	if got := RunEgress(org, project, "add"); !reflect.DeepEqual(got, []string{"github.com", "pypi.org", "10.60.0.0/16"}) {
		t.Errorf("add = %v", got)
	}
	if got := RunEgress(org, project, "only"); !reflect.DeepEqual(got, project) {
		t.Errorf("only = %v", got)
	}
	if got := RunEgress([]string{"*"}, nil, "only"); len(got) != 0 {
		t.Errorf("only, with the organisation's *: %v", got)
	}
}

// The scripted agent neither needs nor gets the LLM's URL or key.
func TestTheScriptedAgentGetsNoLLM(t *testing.T) {
	spec := llmSpec("fake/scripted", "high")
	if len(spec.Env) != 0 || slices.ContainsFunc(spec.Secrets, func(s lux.Secret) bool { return s.Name == "DUDE_LLM_KEY" }) {
		t.Errorf("env %v secrets %+v", spec.Env, spec.Secrets)
	}
}
