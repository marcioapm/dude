package phases

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/capabilities"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// grantsWithout are role's default capabilities less keys.
func grantsWithout(role string, keys ...capabilities.Key) *capabilities.Grants {
	g := capabilities.Default(role).Without(keys...)
	return &g
}

// The Run's spec on harness, for role, with grants (nil: the role's
// defaults), on a network list so harness hosts show.
func nativeSpec(t *testing.T, role, harness string, grants *capabilities.Grants) lux.Spec {
	t.Helper()
	c, in := goldenInput("claude-sonnet-5")
	everyRole[role](&in)
	in.Harness, in.Egress, in.Grants = harness, []string{"pypi.org"}, grants
	return buildSpec(c, in)
}

func openCodePermission(t *testing.T, spec lux.Spec) map[string]any {
	t.Helper()
	var config struct {
		Permission map[string]any `json:"permission"`
	}
	if err := json.Unmarshal([]byte(spec.Env["OPENCODE_CONFIG_CONTENT"]), &config); err != nil {
		t.Fatal(err)
	}
	return config.Permission
}

func hasExaHost(spec lux.Spec) bool {
	return spec.Network != nil && slices.Contains(spec.Network.Egress, lux.EgressRule{Host: "mcp.exa.ai"})
}

func claudeDisallowed(spec lux.Spec) []string {
	cmd := spec.Workload.Command
	i := slices.Index(cmd, "--disallowedTools")
	if i < 0 {
		return nil
	}
	end := slices.Index(cmd, "--settings")
	return cmd[i+1 : end]
}

func codexWebSearch(t *testing.T, spec lux.Spec) any {
	t.Helper()
	i := slices.IndexFunc(spec.Secrets, func(s lux.Secret) bool { return s.Name == "CODEX_CONFIG" })
	if i < 0 {
		t.Fatal("no CODEX_CONFIG secret")
	}
	return parsedCodexTOML(t, spec.Secrets[i].Value)["web_search"]
}

// Every role's default capabilities give it its harness's web search and
// fetch as before capabilities: the spec is the same as with none named.
func TestTheDefaultGrantsConfigureTheWebAsBefore(t *testing.T) {
	for role := range everyRole {
		for _, harness := range []string{delivery.HarnessOpenCode, delivery.HarnessClaudeCode, delivery.HarnessCodex} {
			name := role + " on " + harness
			defaults := capabilities.Default(role)
			if !reflect.DeepEqual(nativeSpec(t, role, harness, &defaults), nativeSpec(t, role, harness, nil)) {
				t.Errorf("%s: the resolved defaults build another spec than the role's", name)
			}
			spec := nativeSpec(t, role, harness, &defaults)
			switch harness {
			case delivery.HarnessOpenCode:
				if spec.Env["OPENCODE_ENABLE_EXA"] != "1" || !hasExaHost(spec) {
					t.Errorf("%s: EXA %q, exa host %v", name, spec.Env["OPENCODE_ENABLE_EXA"], hasExaHost(spec))
				}
				if p := openCodePermission(t, spec); !reflect.DeepEqual(p, map[string]any{"*": "allow"}) {
					t.Errorf("%s: permission %v", name, p)
				}
			case delivery.HarnessClaudeCode:
				if d := claudeDisallowed(spec); d != nil {
					t.Errorf("%s: disallowed %q", name, d)
				}
			case delivery.HarnessCodex:
				if got := codexWebSearch(t, spec); got != "live" {
					t.Errorf("%s: web_search %v", name, got)
				}
			}
		}
	}
}

// A Run whose capabilities lack web search has its harness's search off,
// each harness its own way; its other web tool stays as it was.
func TestARunWithoutWebSearchHasItsHarnessSearchOff(t *testing.T) {
	for role := range everyRole {
		g := grantsWithout(role, capabilities.WebSearch)
		name := role + " without web_search"

		oc := nativeSpec(t, role, delivery.HarnessOpenCode, g)
		if _, set := oc.Env["OPENCODE_ENABLE_EXA"]; set || hasExaHost(oc) {
			t.Errorf("%s on OpenCode: EXA set %v, exa host %v: %+v", name, set, hasExaHost(oc), oc.Network)
		}
		if !slices.Contains(oc.Network.Egress, lux.EgressRule{Host: "pypi.org"}) {
			t.Errorf("%s on OpenCode: the Run's own list is gone: %+v", name, oc.Network)
		}
		if p := openCodePermission(t, oc); p["webfetch"] != nil {
			t.Errorf("%s on OpenCode: webfetch %v, want it as before", name, p["webfetch"])
		}

		cc := nativeSpec(t, role, delivery.HarnessClaudeCode, g)
		if d := claudeDisallowed(cc); !slices.Equal(d, []string{"WebSearch"}) {
			t.Errorf("%s on Claude Code: --disallowedTools %q, want WebSearch", name, d)
		}

		cx := nativeSpec(t, role, delivery.HarnessCodex, g)
		if got := codexWebSearch(t, cx); got != "disabled" {
			t.Errorf("%s on Codex: web_search %v, want disabled", name, got)
		}
		if !slices.Contains(cx.Workload.Command, `web_search="disabled"`) {
			t.Errorf("%s on Codex: command %q", name, cx.Workload.Command)
		}
	}
}

// A Run whose capabilities lack web fetch has its harness's fetch off:
// OpenCode denies webfetch, Claude Code disallows WebFetch. Codex has no
// fetch tool, and its search stays live.
func TestARunWithoutWebFetchHasItsHarnessFetchOff(t *testing.T) {
	for role := range everyRole {
		g := grantsWithout(role, capabilities.WebFetch)
		name := role + " without web_fetch"

		oc := nativeSpec(t, role, delivery.HarnessOpenCode, g)
		if p := openCodePermission(t, oc); !reflect.DeepEqual(p, map[string]any{"*": "allow", "webfetch": "deny"}) {
			t.Errorf("%s on OpenCode: permission %v", name, p)
		}
		if oc.Env["OPENCODE_ENABLE_EXA"] != "1" || !hasExaHost(oc) {
			t.Errorf("%s on OpenCode: search is off", name)
		}

		cc := nativeSpec(t, role, delivery.HarnessClaudeCode, g)
		if d := claudeDisallowed(cc); !slices.Equal(d, []string{"WebFetch"}) {
			t.Errorf("%s on Claude Code: --disallowedTools %q, want WebFetch", name, d)
		}

		if got := codexWebSearch(t, nativeSpec(t, role, delivery.HarnessCodex, g)); got != "live" {
			t.Errorf("%s on Codex: web_search %v", name, got)
		}
	}
}

// Claude Code's --disallowedTools takes every name up to the next flag, and
// --settings still follows as its own JSON argument.
func TestClaudeCodeDisallowsBothBeforeItsSettings(t *testing.T) {
	spec := nativeSpec(t, "implementer", delivery.HarnessClaudeCode, grantsWithout("implementer", capabilities.WebSearch, capabilities.WebFetch))
	cmd := spec.Workload.Command
	if d := claudeDisallowed(spec); !slices.Equal(d, []string{"WebSearch", "WebFetch"}) {
		t.Fatalf("--disallowedTools %q", d)
	}
	i := slices.Index(cmd, "--settings")
	var settings map[string]any
	if err := json.Unmarshal([]byte(cmd[i+1]), &settings); err != nil || settings["skipWebFetchPreflight"] != true {
		t.Errorf("--settings %q: %v", cmd[i+1], err)
	}
}

// A Run that may reach any host but is granted neither web tool has an
// unrestricted network and still has its harness's web tools off: the
// network is not what turns them off.
func TestAnAnywhereRunWithoutTheWebHasItsHarnessWebOff(t *testing.T) {
	g := grantsWithout(delivery.RoleBrainstorm, capabilities.WebSearch, capabilities.WebFetch)
	spec := func(harness string) lux.Spec {
		c, in := goldenInput("claude-sonnet-5")
		everyRole[delivery.RoleBrainstorm](&in)
		in.Harness, in.Anywhere, in.Egress, in.Grants = harness, true, []string{"pypi.org"}, g
		s := buildSpec(c, in)
		if !reflect.DeepEqual(s.Network, &lux.Network{Unrestricted: true}) {
			t.Errorf("%s: network %+v, want unrestricted", harness, s.Network)
		}
		return s
	}
	oc := spec(delivery.HarnessOpenCode)
	if _, set := oc.Env["OPENCODE_ENABLE_EXA"]; set {
		t.Error("OpenCode: OPENCODE_ENABLE_EXA set")
	}
	if p := openCodePermission(t, oc); !reflect.DeepEqual(p, map[string]any{"*": "allow", "webfetch": "deny"}) {
		t.Errorf("OpenCode: permission %v", p)
	}
	if d := claudeDisallowed(spec(delivery.HarnessClaudeCode)); !slices.Equal(d, []string{"WebSearch", "WebFetch"}) {
		t.Errorf("Claude Code: --disallowedTools %q", d)
	}
	if got := codexWebSearch(t, spec(delivery.HarnessCodex)); got != "disabled" {
		t.Errorf("Codex: web_search %v", got)
	}
}

// A tier's -c web_search cannot turn search back on for a Run without it,
// on any network.
func TestATiersCodexSearchCannotOverrideAMissingGrant(t *testing.T) {
	for _, anywhere := range []bool{false, true} {
		c, in := goldenInput("gpt-6-sol")
		in.Harness, in.Anywhere = delivery.HarnessCodex, anywhere
		in.Grants = grantsWithout("implementer", capabilities.WebSearch)
		in.Options = map[string]any{"args": []any{"-c", `web_search="live"`}}
		settings, dropped := codexSettings(c, in)
		name := fmt.Sprintf("anywhere %v", anywhere)
		if !slices.Contains(settings, `web_search="disabled"`) || slices.Contains(settings, `web_search="live"`) {
			t.Errorf("%s: settings %q", name, settings)
		}
		if !slices.Equal(dropped, []string{`-c web_search="live"`}) {
			t.Errorf("%s: dropped %q", name, dropped)
		}
	}
}
