package phases

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/llm"
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
// still resolves; the tier's effort is the model's options.
func TestTheModelAndEffortAreInlineOpenCodeConfig(t *testing.T) {
	for _, tc := range []struct {
		model, effort string
		want          string
	}{
		{"claude-opus-5-5", "high", `{"model":"llm-anthropic/claude-opus-5-5","provider":{"llm-anthropic":{"models":{"claude-opus-5-5":{"options":{"effort":"high","thinking":{"display":"summarized","type":"adaptive"}}}}}}}`},
		// Not in the image's opencode.json: declared all the same.
		{"claude-nova-7", "low", `{"model":"llm-anthropic/claude-nova-7","provider":{"llm-anthropic":{"models":{"claude-nova-7":{"options":{"effort":"low","thinking":{"display":"summarized","type":"adaptive"}}}}}}}`},
		{"gpt-5.6-sol", "max", `{"model":"llm-openai/gpt-5.6-sol","provider":{"llm-openai":{"models":{"gpt-5.6-sol":{"options":{"reasoningEffort":"max","reasoningSummary":"auto"}}}}}}`},
		{"gemini-3.8-pro", "", `{"model":"llm-openai/gemini-3.8-pro","provider":{"llm-openai":{"models":{"gemini-3.8-pro":{"options":{"reasoningSummary":"auto"}}}}}}`},
		// Only a name starting claude- is Anthropic's.
		{"my-claude-proxy", "", `{"model":"llm-openai/my-claude-proxy","provider":{"llm-openai":{"models":{"my-claude-proxy":{"options":{"reasoningSummary":"auto"}}}}}}`},
	} {
		spec := llmSpec(tc.model, tc.effort)
		if got := spec.Env["OPENCODE_CONFIG_CONTENT"]; got != tc.want {
			t.Errorf("%s effort %q:\n got %s\nwant %s", tc.model, tc.effort, got, tc.want)
		}
		if spec.Labels["dude.model"] != tc.model {
			t.Errorf("%s: label dude.model = %q, want the model as the proxy names it", tc.model, spec.Labels["dude.model"])
		}
	}
}

// A tier's effort as each provider's model options: Claude's thinking is
// always asked for summarized (else its text comes back empty) unless the
// effort is none, which turns it off; OpenAI's reasoning summary is always
// asked for, and its effort sent as it is, max included, none as nothing.
func TestATiersEffortIsItsProvidersModelOptions(t *testing.T) {
	const adaptive = `"thinking":{"display":"summarized","type":"adaptive"}`
	for _, tc := range []struct {
		model, effort, options string
	}{
		{"claude-sonnet-5", "", `{` + adaptive + `}`},
		{"claude-sonnet-5", "none", `{"thinking":{"type":"disabled"}}`},
		{"claude-sonnet-5", "low", `{"effort":"low",` + adaptive + `}`},
		{"claude-sonnet-5", "max", `{"effort":"max",` + adaptive + `}`},
		{"gpt-6-sol", "", `{"reasoningSummary":"auto"}`},
		{"gpt-6-sol", "none", `{"reasoningSummary":"auto"}`},
		{"gpt-6-sol", "high", `{"reasoningEffort":"high","reasoningSummary":"auto"}`},
		{"gpt-6-sol", "max", `{"reasoningEffort":"max","reasoningSummary":"auto"}`},
	} {
		want := `{"model":"` + llm.Provider(tc.model) + `/` + tc.model + `","provider":{"` + llm.Provider(tc.model) +
			`":{"models":{"` + tc.model + `":{"options":` + tc.options + `}}}}}`
		if got := openCodeConfig(tc.model, tc.effort, nil, nil); got != want {
			t.Errorf("%s at %q:\n got %s\nwant %s", tc.model, tc.effort, got, want)
		}
	}
}

// A tier's own options are merged over what its effort makes, object by
// object, and win; its headers are the model's headers.
func TestATiersOptionsWinAndItsHeadersAreTheModels(t *testing.T) {
	options := map[string]any{"effort": "xhigh", "thinking": map[string]any{"display": "omitted"}, "sendReasoning": true}
	got := openCodeConfig("claude-opus-5-5", "high", options, nil)
	want := `{"model":"llm-anthropic/claude-opus-5-5","provider":{"llm-anthropic":{"models":{"claude-opus-5-5":{"options":` +
		`{"effort":"xhigh","sendReasoning":true,"thinking":{"display":"omitted","type":"adaptive"}}}}}}}`
	if got != want {
		t.Errorf("options over effort:\n got %s\nwant %s", got, want)
	}
	got = openCodeConfig("gpt-6-sol", "low", map[string]any{"reasoningSummary": "detailed"},
		map[string]string{"X-Team": "dude", "anthropic-beta": "context-1m"})
	want = `{"model":"llm-openai/gpt-6-sol","provider":{"llm-openai":{"models":{"gpt-6-sol":{` +
		`"headers":{"X-Team":"dude","anthropic-beta":"context-1m"},"options":{"reasoningEffort":"low","reasoningSummary":"detailed"}}}}}}`
	if got != want {
		t.Errorf("headers:\n got %s\nwant %s", got, want)
	}
	// The tier's options are its own: building a config does not change them.
	if d, _ := options["thinking"].(map[string]any); len(d) != 1 {
		t.Errorf("the tier's options were written to: %v", options)
	}
}

// A Run on a tier carries the tier's effort through to its spec: as the
// model's options and as the dude.effort label.
func TestATiersEffortReachesTheSpec(t *testing.T) {
	c := AgentConfig{LLMURL: "https://llm.example/v1"}
	spec := buildSpec(c, specInput{RunID: "run_1", TaskID: "wi_1", Phase: "review", Model: "claude-fable-5-1",
		ModelTier: "Thinker", Effort: "high", Headers: map[string]string{"X-A": "1"}})
	if spec.Labels["dude.effort"] != "high" {
		t.Errorf("labels = %v", spec.Labels)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(spec.Env["OPENCODE_CONFIG_CONTENT"]), &got); err != nil {
		t.Fatal(err)
	}
	entry := got["provider"].(map[string]any)["llm-anthropic"].(map[string]any)["models"].(map[string]any)["claude-fable-5-1"]
	if !reflect.DeepEqual(entry, map[string]any{"headers": map[string]any{"X-A": "1"},
		"options": map[string]any{"effort": "high", "thinking": map[string]any{"type": "adaptive", "display": "summarized"}}}) {
		t.Errorf("model entry = %v", entry)
	}
}

// Prints the OPENCODE_CONFIG_CONTENT buildSpec gives a phase Run on a tier,
// for trying it against a real OpenCode (scripts/real_thinking.py). Only
// when DUDE_PRINT_TIER_CONFIG is "MODEL [EFFORT]".
func TestPrintATiersOpenCodeConfig(t *testing.T) {
	args := strings.Fields(os.Getenv("DUDE_PRINT_TIER_CONFIG"))
	if len(args) == 0 {
		t.Skip("DUDE_PRINT_TIER_CONFIG not set")
	}
	in := specInput{Phase: delivery.PhaseImplement, Model: args[0], ModelTier: "Probe"}
	if len(args) > 1 {
		in.Effort = args[1]
	}
	fmt.Println("OPENCODE_CONFIG_CONTENT=" + buildSpec(AgentConfig{}, in).Env["OPENCODE_CONFIG_CONTENT"])
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
