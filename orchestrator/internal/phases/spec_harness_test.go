package phases

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// What dude sends lux for a role on each harness, byte for byte, for a
// Claude and a GPT model at each kind of effort (unset, none, high), with
// the tier's headers and extra args: one golden file per harness. A model
// the harness cannot run is not built: the golden holds the reason its Run
// fails with.
func TestEachHarnessesSpecIsTheGoldenOne(t *testing.T) {
	options := map[string]any{"args": []any{"--extra", "x y"}, "sendReasoning": true}
	headers := map[string]string{"X-Team": "dude", "anthropic-beta": "context-1m"}
	for _, harness := range []string{delivery.HarnessOpenCode, delivery.HarnessClaudeCode, delivery.HarnessCodex} {
		t.Run(harness, func(t *testing.T) {
			cases := map[string]any{}
			for _, model := range []string{"claude-sonnet-5", "gpt-6-sol"} {
				for _, effort := range []string{"", "none", "high"} {
					key := model + " effort=" + effort
					if why := harnessMisfit(delivery.RoleSettings{Harness: harness}, delivery.Tier{Name: "Coder", Model: model}, "implementer"); why != "" {
						cases[key] = map[string]string{"refused": why}
						continue
					}
					c, in := goldenInput(model)
					in.Harness, in.Effort, in.Headers = harness, effort, headers
					if harness != delivery.HarnessOpenCode {
						in.Options = options
					}
					cases[key] = buildSpec(c, in)
				}
			}
			got, err := json.MarshalIndent(cases, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, '\n')
			path := filepath.Join("testdata", "spec-harness-"+harness+".golden")
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

// OpenCode is what a role with no harness runs on, and it is built as it
// always was: naming it changes nothing.
func TestOpenCodeNamedOrNotIsTheSameSpec(t *testing.T) {
	c, in := goldenInput("claude-opus-5-5")
	unnamed := buildSpec(c, in)
	in.Harness = delivery.HarnessOpenCode
	if named := buildSpec(c, in); !reflect.DeepEqual(named, unnamed) {
		t.Errorf("named OpenCode:\n%+v\nunnamed:\n%+v", named, unnamed)
	}
}

// lux refuses a spec whose adapter's session directory is not on a state
// volume ($HOME/.claude, $HOME/.codex): the agent's home is one, so both are.
func TestEachHarnessesSessionIsOnAStateVolume(t *testing.T) {
	for harness, dir := range map[string]string{delivery.HarnessClaudeCode: "/home/agent/.claude", delivery.HarnessCodex: "/home/agent/.codex"} {
		c, in := goldenInput("claude-sonnet-5")
		in.Harness = harness
		spec := buildSpec(c, in)
		if !slices.ContainsFunc(spec.Volumes, func(v lux.Volume) bool {
			return v.Kind == "state" && (dir == v.Path || strings.HasPrefix(dir, v.Path+"/"))
		}) {
			t.Errorf("%s: no state volume holds %s: %+v", harness, dir, spec.Volumes)
		}
		if spec.Workload.Adapter != harness || spec.Labels["dude.harness"] != harness {
			t.Errorf("%s: adapter %q, label %q", harness, spec.Workload.Adapter, spec.Labels["dude.harness"])
		}
	}
}

// The key goes to Claude Code and Codex as their own env secret, and to
// nothing else: not in env, labels or the command lux records.
func TestTheKeyIsEachHarnessesOwnSecretAndNowhereElse(t *testing.T) {
	for harness, secret := range map[string]string{delivery.HarnessClaudeCode: "ANTHROPIC_API_KEY", delivery.HarnessCodex: "OPENAI_API_KEY"} {
		c, in := goldenInput("claude-sonnet-5")
		in.Harness = harness
		spec := buildSpec(c, in)
		var names []string
		for _, s := range spec.Secrets {
			names = append(names, s.Name)
			if s.Name == secret && (s.Value != c.LLMKey || s.As != "env") {
				t.Errorf("%s: %s = %+v", harness, secret, s)
			}
		}
		if !slices.Contains(names, secret) || slices.Contains(names, "DUDE_LLM_KEY") {
			t.Errorf("%s: secrets %v", harness, names)
		}
		raw, _ := json.Marshal(map[string]any{"env": spec.Env, "labels": spec.Labels, "command": spec.Workload.Command})
		if strings.Contains(string(raw), c.LLMKey) {
			t.Errorf("%s: the key is in plain sight: %s", harness, raw)
		}
	}
}

// Claude Code adds /v1 to its paths itself: the proxy's URL goes without it.
func TestClaudeCodeIsGivenTheProxysURLWithoutV1(t *testing.T) {
	for in, want := range map[string]string{
		"https://llm.example/v1": "https://llm.example", "https://llm.example/v1/": "https://llm.example",
		"https://llm.example/api/v1": "https://llm.example/api", "https://llm.example": "https://llm.example",
	} {
		if got := anthropicBaseURL(in); got != want {
			t.Errorf("%s: %s, want %s", in, got, want)
		}
	}
}

// A role on a harness that cannot run its tier's model fails its Run,
// saying which and why; OpenCode runs both, and the scripted agent stands
// in for any harness.
func TestAHarnessThatCannotRunItsTiersModelIsSaidSo(t *testing.T) {
	for _, tc := range []struct{ harness, model, want string }{
		{delivery.HarnessClaudeCode, "gpt-6-sol", "The Implementer runs on Claude Code, which takes an Anthropic model (claude-…), but its tier Coder requests gpt-6-sol. An admin picks another harness or tier in Agents."},
		{delivery.HarnessCodex, "claude-sonnet-5", "The Implementer runs on Codex, which takes an OpenAI model, but its tier Coder requests claude-sonnet-5. An admin picks another harness or tier in Agents."},
		{delivery.HarnessClaudeCode, "claude-sonnet-5", ""},
		{delivery.HarnessCodex, "gpt-6-sol", ""},
		{delivery.HarnessOpenCode, "gpt-6-sol", ""},
		{"", "claude-sonnet-5", ""},
		{delivery.HarnessCodex, "fake/scripted", ""},
	} {
		got := harnessMisfit(delivery.RoleSettings{Harness: tc.harness}, delivery.Tier{Name: "Coder", Model: tc.model}, "implementer")
		if got != tc.want {
			t.Errorf("%s on %s: %q, want %q", tc.model, tc.harness, got, tc.want)
		}
	}
}

// A tier's options are OpenCode's model options; for Claude Code and Codex
// only "args" means anything, and the rest is named to be logged.
func TestATiersArgsAreTheOnlyOptionsOtherHarnessesTake(t *testing.T) {
	options := map[string]any{"args": []any{"--a", "b", 3}, "thinking": map[string]any{}, "effort": "max"}
	if got := HarnessArgs(options); !reflect.DeepEqual(got, []string{"--a", "b"}) {
		t.Errorf("args = %v", got)
	}
	if got := IgnoredOptions(delivery.HarnessCodex, options); !reflect.DeepEqual(got, []string{"effort", "thinking"}) {
		t.Errorf("ignored = %v", got)
	}
	if got := IgnoredOptions(delivery.HarnessOpenCode, options); got != nil {
		t.Errorf("OpenCode ignores %v", got)
	}
}

// A resumed Run goes on with the harness it was submitted on.
func TestAResumeKeepsTheHarnessItWasSubmittedOn(t *testing.T) {
	if got := submittedHarness(delivery.HarnessCodex, delivery.HarnessClaudeCode); got != delivery.HarnessCodex {
		t.Errorf("got %s", got)
	}
	if got := submittedHarness("scripted", delivery.HarnessClaudeCode); got != delivery.HarnessClaudeCode {
		t.Errorf("scripted: got %s", got)
	}
}

// The scripted agent plays a role on Claude Code or Codex through that
// harness's adapter, lux-fake speaking its protocol; on OpenCode, ACP.
func TestTheScriptedAgentSpeaksItsRolesHarness(t *testing.T) {
	for harness, adapter := range map[string]string{"": "acp", delivery.HarnessOpenCode: "acp",
		delivery.HarnessClaudeCode: "claude-code", delivery.HarnessCodex: "codex"} {
		c, in := goldenInput("fake/scripted")
		in.Harness = harness
		spec := buildSpec(c, in)
		if spec.Workload.Adapter != adapter || !reflect.DeepEqual(spec.Workload.Command, []string{"lux-fake"}) || len(spec.Env) != 0 {
			t.Errorf("%q: adapter %s command %v env %v", harness, spec.Workload.Adapter, spec.Workload.Command, spec.Env)
		}
	}
}
