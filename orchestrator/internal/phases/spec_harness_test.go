package phases

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
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
// the tier's headers and extra args: one golden file per harness. Every
// harness takes every model (the LLM proxy translates), so every pair is
// built.
func TestEachHarnessesSpecIsTheGoldenOne(t *testing.T) {
	options := map[string]any{"args": []any{"--extra", "x y"}, "sendReasoning": true}
	codexOptions := map[string]any{"args": []any{"--extra", "-c", `model_verbosity="low"`, "-c", "model_reasoning_summary=detailed"},
		"sendReasoning": true}
	headers := map[string]string{"X-Team": "dude", "anthropic-beta": "context-1m"}
	for _, harness := range []string{delivery.HarnessOpenCode, delivery.HarnessClaudeCode, delivery.HarnessCodex} {
		t.Run(harness, func(t *testing.T) {
			cases := map[string]any{}
			for _, model := range []string{"claude-sonnet-5", "gpt-6-sol"} {
				for _, effort := range []string{"", "none", "high"} {
					key := model + " effort=" + effort
					c, in := goldenInput(model)
					in.Harness, in.Effort, in.Headers = harness, effort, headers
					switch harness {
					case delivery.HarnessClaudeCode:
						in.Options = options
					case delivery.HarnessCodex:
						in.Options = codexOptions
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

// Claude Code on a GPT model and Codex on a Claude one are built for the
// model the tier names: Claude Code's command carries it, Codex's
// config.toml names it, and the proxy translates.
func TestAnyHarnessIsBuiltForAnyModel(t *testing.T) {
	c, in := goldenInput("gpt-6-sol")
	in.Harness = delivery.HarnessClaudeCode
	spec := buildSpec(c, in)
	if spec.Workload.Adapter != delivery.HarnessClaudeCode {
		t.Errorf("adapter = %s", spec.Workload.Adapter)
	}
	if i := slices.Index(spec.Workload.Command, "--model"); i < 0 || i+1 >= len(spec.Workload.Command) || spec.Workload.Command[i+1] != "gpt-6-sol" {
		t.Errorf("Claude Code command = %v, want --model gpt-6-sol", spec.Workload.Command)
	}

	c, in = goldenInput("claude-sonnet-5")
	in.Harness = delivery.HarnessCodex
	spec = buildSpec(c, in)
	if spec.Workload.Adapter != delivery.HarnessCodex {
		t.Errorf("adapter = %s", spec.Workload.Adapter)
	}
	i := slices.IndexFunc(spec.Secrets, func(s lux.Secret) bool { return s.Name == "CODEX_CONFIG" })
	if i < 0 {
		t.Fatal("no CODEX_CONFIG secret")
	}
	if got := parsedCodexTOML(t, spec.Secrets[i].Value)["model"]; got != "claude-sonnet-5" {
		t.Errorf("Codex config model = %v, want claude-sonnet-5", got)
	}
}

// A tier's options are OpenCode's model options; for Claude Code and Codex
// only "args" means anything, and the rest is named to be logged.
func TestATiersArgsAreTheOnlyOptionsOtherHarnessesTake(t *testing.T) {
	options := map[string]any{"args": []any{"--a", "b", 3}, "thinking": map[string]any{}, "effort": "max"}
	if got := harnessArgs(options); !reflect.DeepEqual(got, []string{"--a", "b"}) {
		t.Errorf("args = %v", got)
	}
	if got := ignoredOptions(delivery.HarnessCodex, options); !reflect.DeepEqual(got, []string{"effort", "thinking"}) {
		t.Errorf("ignored = %v", got)
	}
	if got := ignoredOptions(delivery.HarnessOpenCode, options); got != nil {
		t.Errorf("OpenCode ignores %v", got)
	}
}

// Codex drops every argument given before app-server once lux's own -c
// follow it, so a tier's -c overrides are written to its config.toml, each
// replacing dude's line for its key or adding one; the rest are dropped,
// and so is one that would rewrite dude's provider.
func TestACodexTiersConfigArgsGoToItsConfigFile(t *testing.T) {
	c, in := goldenInput("gpt-6-sol")
	in.Harness = delivery.HarnessCodex
	in.Options = map[string]any{"args": []any{"-c", `model_reasoning_effort="low"`, "--config=model_verbosity=low",
		"-c", "sandbox_mode=read-only", "-c", `model_providers.dude.base_url="https://evil.example"`,
		"-c", "tools.web_search=true", "-m", "other", "-c", `notify=["say", 1]`, "-c", "x={a=1}"}}
	spec := buildSpec(c, in)
	var config string
	for _, s := range spec.Secrets {
		if s.Name == "CODEX_CONFIG" {
			config = s.Value
		}
	}
	lines := strings.Split(strings.TrimSuffix(config, "\n"), "\n")
	for _, want := range []string{`model_reasoning_effort="low"`, `model_verbosity="low"`, `sandbox_mode="read-only"`,
		"tools.web_search=true", `notify=["say", 1]`} {
		if !slices.Contains(lines, want) {
			t.Errorf("config.toml has no %s:\n%s", want, config)
		}
	}
	if slices.Contains(lines, `sandbox_mode="danger-full-access"`) || strings.Contains(config, "evil") || strings.Contains(config, "x=") {
		t.Errorf("config.toml kept an overridden line or took a refused one:\n%s", config)
	}
	if slices.Contains(spec.Workload.Command, "-m") || slices.Contains(spec.Workload.Command, "other") {
		t.Errorf("command = %v", spec.Workload.Command)
	}
	parsed := parsedCodexTOML(t, config)
	for key, want := range map[string]any{"model_reasoning_effort": "low", "model_verbosity": "low", "sandbox_mode": "read-only"} {
		if parsed[key] != want {
			t.Errorf("%s = %v, want %v", key, parsed[key], want)
		}
	}
	if parsed["tools"].(map[string]any)["web_search"] != true || !reflect.DeepEqual(parsed["notify"], []any{"say", float64(1)}) {
		t.Errorf("tools/notify = %v / %v", parsed["tools"], parsed["notify"])
	}
	_, dropped := codexSettings(c, in)
	if want := []string{`-c model_providers.dude.base_url="https://evil.example"`, "-m", "other", "-c x={a=1}"}; !slices.Equal(dropped, want) {
		t.Errorf("dropped = %q, want %q", dropped, want)
	}
}

func parsedCodexTOML(t *testing.T, config string) map[string]any {
	t.Helper()
	cmd := exec.Command("python3", "-c", "import sys,tomllib,json; json.dump(tomllib.loads(sys.stdin.read()),sys.stdout)")
	cmd.Stdin = strings.NewReader(config)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("parse TOML: %v: %s", err, out)
	}
	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatal(err)
	}
	return parsed
}

func TestCodexProtectsItsExactProviderTable(t *testing.T) {
	c, in := goldenInput("gpt-6-sol")
	in.Options = map[string]any{"args": []any{"-c", "model_providers.dude=evil", "-c", "model_providers=evil", "-c", "model_providers.dude.base_url=evil", "-c", "model_providers.other.name=other"}}
	settings, dropped := codexSettings(c, in)
	parsed := parsedCodexTOML(t, strings.Join(settings, "\n"))
	providers := parsed["model_providers"].(map[string]any)
	provider, ok := providers["dude"].(map[string]any)
	if !ok || provider["base_url"] != c.LLMURL || provider["env_key"] != "OPENAI_API_KEY" {
		t.Errorf("protected provider = %v", providers["dude"])
	}
	if providers["other"].(map[string]any)["name"] != "other" {
		t.Errorf("sibling = %v", providers["other"])
	}
	if want := []string{"-c model_providers.dude=evil", "-c model_providers=evil", "-c model_providers.dude.base_url=evil"}; !slices.Equal(dropped, want) {
		t.Errorf("dropped = %v, want %v", dropped, want)
	}
}

// A header value may hold DEL, which TOML does not allow raw: Codex's
// config.toml carries it escaped.
func TestACodexHeaderWithDELIsValidTOML(t *testing.T) {
	c, in := goldenInput("gpt-6-sol")
	in.Harness = delivery.HarnessCodex
	in.Headers = map[string]string{"X-Odd": "a\x7fb"}
	found := false
	for _, s := range buildSpec(c, in).Secrets {
		if s.Name != "CODEX_CONFIG" {
			continue
		}
		found = true
		if strings.ContainsRune(s.Value, 0x7f) || !strings.Contains(s.Value, `"X-Odd"="a\u007fb"`) {
			t.Errorf("config.toml = %q", s.Value)
		}
		provider := parsedCodexTOML(t, s.Value)["model_providers"].(map[string]any)["dude"].(map[string]any)
		if got := provider["http_headers"].(map[string]any)["X-Odd"]; got != "a\x7fb" {
			t.Errorf("header = %v", got)
		}
	}
	if !found {
		t.Fatal("no CODEX_CONFIG secret")
	}
}

// everyRole turns goldenInput's implementer Run into each kind of Run: a
// phase that publishes, one that does not, a task's conductor and a
// session's brainstorm.
var everyRole = map[string]func(*specInput){
	"implementer": func(*specInput) {},
	"reviewer":    func(in *specInput) { in.Phase, in.Role, in.PushBranch = "review", "reviewer", "" },
	"conductor":   func(in *specInput) { in.Phase, in.Role = "", "conductor" },
	"brainstorm": func(in *specInput) {
		in.Phase, in.Role, in.TaskID, in.SessionID, in.PushBranch = "", "brainstorm", "", "ssn_1", ""
	},
}

// Claude Code never asks api.anthropic.com about a host before a WebFetch,
// on any role or effort: its command carries --settings with the check
// off, as JSON Claude Code parses.
func TestClaudeCodeSkipsTheWebFetchPreflightOnEveryRole(t *testing.T) {
	for role, edit := range everyRole {
		for _, effort := range []string{"", "none", "high"} {
			c, in := goldenInput("claude-sonnet-5")
			in.Harness, in.Effort = delivery.HarnessClaudeCode, effort
			in.Options = map[string]any{"args": []any{"--settings-like", "x"}}
			edit(&in)
			cmd := buildSpec(c, in).Workload.Command
			i := slices.Index(cmd, "--settings")
			if i < 0 || i+1 >= len(cmd) {
				t.Errorf("%s effort=%q: no --settings in %v", role, effort, cmd)
				continue
			}
			var settings map[string]any
			if err := json.Unmarshal([]byte(cmd[i+1]), &settings); err != nil || settings["skipWebFetchPreflight"] != true {
				t.Errorf("%s effort=%q: --settings %q (%v), want skipWebFetchPreflight true", role, effort, cmd[i+1], err)
			}
		}
	}
}

// A tier's own --settings would replace dude's (Claude Code keeps the last
// one): its JSON is merged into dude's, with the preflight still off even
// where the tier turns it on, and one that is not a JSON object (a path,
// null, an array) is dropped and named.
func TestATiersClaudeCodeSettingsAreMergedWithThePreflightOff(t *testing.T) {
	c, in := goldenInput("claude-sonnet-5")
	in.Harness = delivery.HarnessClaudeCode
	in.Options = map[string]any{"args": []any{"--verbose", "--settings", `{"model":"x","skipWebFetchPreflight":false}`,
		"--add-dir", "/x", `--settings={"env":{"A":"1"},"model":"y"}`, "--settings", "/etc/claude.json",
		"--settings", "null", "--settings", "[1]", "--settings-like", "x", "--extra"}}
	cmd := buildSpec(c, in).Workload.Command
	n := slices.Index(cmd, "--settings")
	if n < 0 || slices.Index(cmd[n+1:], "--settings") >= 0 ||
		slices.ContainsFunc(cmd, func(a string) bool { return strings.HasPrefix(a, "--settings=") || a == "/etc/claude.json" }) {
		t.Fatalf("command = %q, want dude's --settings alone", cmd)
	}
	var settings map[string]any
	if err := json.Unmarshal([]byte(cmd[n+1]), &settings); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"model": "y", "env": map[string]any{"A": "1"}, "skipWebFetchPreflight": true}
	if !reflect.DeepEqual(settings, want) {
		t.Errorf("settings = %v, want %v", settings, want)
	}
	// The tier's other args follow dude's --settings in their order, pairs
	// kept together; one merely starting with --settings is one of them.
	tail, wantTail := cmd[n+2:], []string{"--verbose", "--add-dir", "/x", "--settings-like", "x", "--extra"}
	if !slices.Equal(tail, wantTail) {
		t.Errorf("command ends %q, want %q", tail, wantTail)
	}
	if _, _, dropped := claudeCodeArgs(in.Options); !slices.Equal(dropped, []string{"--settings /etc/claude.json", "--settings null", "--settings [1]"}) {
		t.Errorf("dropped = %q", dropped)
	}
	if _, _, dropped := claudeCodeArgs(map[string]any{"args": []any{"--settings"}}); !slices.Equal(dropped, []string{"--settings"}) {
		t.Errorf("a --settings with no value: dropped = %q", dropped)
	}
}

// A tier's --settings that Claude Code leaves out is the operator's to see:
// a Run on Claude Code warns with what it dropped. One whose settings are
// all inline JSON, or a Run on OpenCode, which passes no args, does not.
func TestClaudeCodeWarnsOfTheTiersSettingsItDropped(t *testing.T) {
	path := map[string]any{"args": []any{"--settings", "/etc/c.json"}}
	for _, tc := range []struct {
		name, harness string
		options       map[string]any
		warn          bool
	}{
		{"a path on Claude Code", delivery.HarnessClaudeCode, path, true},
		{"inline JSON on Claude Code", delivery.HarnessClaudeCode, map[string]any{"args": []any{"--settings", `{"model":"y"}`}}, false},
		{"a path on OpenCode", delivery.HarnessOpenCode, path, false},
	} {
		var logs bytes.Buffer
		s := &Syncer{Log: slog.New(slog.NewTextHandler(&logs, nil))}
		s.logIgnoredOptions(phaseRun{ID: "run_1"}, specInput{Harness: tc.harness, ModelTier: "T", Options: tc.options})
		warned := strings.Contains(logs.String(), "level=WARN")
		if warned != tc.warn || (tc.warn && !strings.Contains(logs.String(), `dropped="[--settings /etc/c.json]"`)) {
			t.Errorf("%s: logged %q, want a warn naming the path %v", tc.name, logs.String(), tc.warn)
		}
	}
}

// OpenCode never fetches models.opencode.ai, on any role: the image and the
// Run declare every model it uses.
func TestOpenCodeNeverFetchesTheModelsCatalogue(t *testing.T) {
	for role, edit := range everyRole {
		c, in := goldenInput("claude-sonnet-5")
		edit(&in)
		if got := buildSpec(c, in).Env["OPENCODE_DISABLE_MODELS_FETCH"]; got != "1" {
			t.Errorf("%s: OPENCODE_DISABLE_MODELS_FETCH = %q, want 1", role, got)
		}
	}
}

// A Run that may reach any host is unrestricted whatever the lists say, on
// every harness and on the scripted agent; on OpenCode it also gets Exa,
// its websearch tool. A Run on the lists gets neither.
func TestARunThatMayReachAnywhereIsUnrestrictedAndOpenCodeSearches(t *testing.T) {
	harnesses := []string{delivery.HarnessOpenCode, delivery.HarnessClaudeCode, delivery.HarnessCodex}
	for _, model := range []string{"claude-sonnet-5", "fake/scripted"} {
		for _, harness := range harnesses {
			for _, anywhere := range []bool{false, true} {
				c, in := goldenInput(model)
				everyRole["brainstorm"](&in)
				c.Egress, in.Egress, in.Harness, in.Anywhere = []string{"mirror.internal"}, []string{"pypi.org"}, harness, anywhere
				spec := buildSpec(c, in)
				name := fmt.Sprintf("%s on %s, anywhere %v", model, harness, anywhere)
				if spec.Network == nil || spec.Network.Unrestricted != anywhere || anywhere != (len(spec.Network.Egress) == 0) {
					t.Errorf("%s: network = %+v", name, spec.Network)
				}
				exa, has := spec.Env["OPENCODE_ENABLE_EXA"]
				if want := anywhere && harness == delivery.HarnessOpenCode && model != "fake/scripted"; has != want || (has && exa != "1") {
					t.Errorf("%s: OPENCODE_ENABLE_EXA = %q (set %v), want set %v", name, exa, has, want)
				}
			}
		}
	}
	// The scripted agent with no lists and no tools to reach still may
	// reach any host when its Run may.
	for _, harness := range harnesses {
		c, in := goldenInput("fake/scripted")
		everyRole["brainstorm"](&in)
		c.ToolsURL, in.Egress, in.Harness, in.Anywhere = "", nil, harness, true
		if spec := buildSpec(c, in); len(spec.Workload.MCPServers) > 0 || !reflect.DeepEqual(spec.Network, &lux.Network{Unrestricted: true}) {
			t.Errorf("fake/scripted on %s, anywhere, no lists or MCP: mcp = %v, network = %+v", harness, spec.Workload.MCPServers, spec.Network)
		}
	}
}

// Codex sends its hosted web_search tool unless told not to: on every role,
// a Run on Codex searches live only when it may reach any host, and says
// "disabled" otherwise. A tier's -c web_search changes neither way and is
// dropped.
func TestCodexSearchesTheWebOnlyWhenItsRunMayReachAnyHost(t *testing.T) {
	configOf := func(spec lux.Spec) string {
		for _, s := range spec.Secrets {
			if s.Name == "CODEX_CONFIG" {
				return s.Value
			}
		}
		t.Fatal("no CODEX_CONFIG secret")
		return ""
	}
	for role, edit := range everyRole {
		for _, anywhere := range []bool{false, true} {
			for _, tier := range []string{"", `"live"`, "disabled"} {
				c, in := goldenInput("gpt-6-sol")
				edit(&in)
				in.Harness, in.Anywhere = delivery.HarnessCodex, anywhere
				if tier != "" {
					in.Options = map[string]any{"args": []any{"-c", "web_search=" + tier}}
				}
				want := map[bool]string{false: "disabled", true: "live"}[anywhere]
				spec := buildSpec(c, in)
				name := fmt.Sprintf("%s, anywhere %v, tier web_search %q", role, anywhere, tier)
				if got := parsedCodexTOML(t, configOf(spec))["web_search"]; got != want {
					t.Errorf("%s: config.toml web_search = %v, want %s", name, got, want)
				}
				line := `web_search="` + want + `"`
				if n := slices.Index(spec.Workload.Command, line); n < 1 || spec.Workload.Command[n-1] != "-c" ||
					slices.ContainsFunc(spec.Workload.Command, func(a string) bool { return strings.HasPrefix(a, "web_search=") && a != line }) {
					t.Errorf("%s: command = %q, want -c %s alone", name, spec.Workload.Command, line)
				}
				_, dropped := codexSettings(c, in)
				if tier != "" && !slices.Equal(dropped, []string{"-c web_search=" + tier}) {
					t.Errorf("%s: dropped = %q", name, dropped)
				}
			}
		}
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

// Prints the command, env and secret names buildSpec gives a phase Run on
// a harness, as JSON, for running the real CLI with them
// (scripts/real_harnesses.py). Only when DUDE_PRINT_HARNESS_SPEC is
// "HARNESS MODEL [EFFORT [anywhere]]"; anywhere makes it a brainstorm that
// may reach any host. The LLM URL is DUDE_LLM_URL, and the key is a
// placeholder the script replaces.
func TestPrintAHarnessSpec(t *testing.T) {
	args := strings.Fields(os.Getenv("DUDE_PRINT_HARNESS_SPEC"))
	if len(args) < 2 {
		t.Skip("DUDE_PRINT_HARNESS_SPEC not set")
	}
	in := specInput{Phase: delivery.PhaseImplement, Harness: args[0], Model: args[1], ModelTier: "Probe"}
	if len(args) > 2 {
		in.Effort = args[2]
	}
	if len(args) > 3 && args[3] == "anywhere" {
		in.Phase, in.Role, in.Anywhere = "", delivery.RoleBrainstorm, true
	}
	spec := buildSpec(AgentConfig{LLMURL: os.Getenv("DUDE_LLM_URL"), LLMKey: "<key>"}, in)
	secrets := map[string]any{}
	for _, s := range spec.Secrets {
		secrets[s.Name] = map[string]string{"value": s.Value, "as": s.As, "path": s.Path}
	}
	b, _ := json.Marshal(map[string]any{"adapter": spec.Workload.Adapter, "command": spec.Workload.Command, "env": spec.Env, "secrets": secrets})
	fmt.Println("HARNESS_SPEC=" + string(b))
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
