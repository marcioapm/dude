package agenttools

import (
	"slices"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/capabilities"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// Each role's tools under its default capabilities: as the per-role role
// lists gave them before capabilities existed, plus the conductor's and
// brainstorm's runs.read, pr.refresh and limits.read tools. A grant changed
// in capabilities.Defaults shows here as a diff to read.
var toolsBeforeCapabilities = map[string]string{
	"conductor": "list_machine_sizes request_machine list_tasks list_epics ask_person emit_event list_repositories request_repository " +
		"create_task search_memory get_memory remember run_diff findings pull_requests start_phase start_agent steer restart_run decide " +
		"dismiss_finding decide_escalation update_task reply_on_pull_request publish request_review resolve_thread mark_ready " +
		"rerun_failed_checks sync_base github_search github_issue github_pull_request github_check_log message wait_for_reply note " +
		"feedback feedback_reply limits run_chat refresh_pull_requests",
	"brainstorm": "list_machine_sizes request_machine github_search github_issue github_pull_request github_check_log list_tasks " +
		"list_epics list_repositories pull_requests findings search_memory get_memory remember propose relate ask_person name_session " +
		"start_agent run_chat refresh_pull_requests",
	"implementer": "list_machine_sizes request_machine list_tasks list_epics ask_person emit_event list_repositories request_repository " +
		"create_task search_memory get_memory remember run_diff reply_on_pull_request resolve_thread sync_base message wait_for_reply " +
		"note feedback feedback_reply feedback_resolve",
	"reviewer": "list_machine_sizes request_machine list_tasks list_epics emit_event list_repositories request_repository search_memory " +
		"get_memory remember run_diff",
	"fixer": "list_machine_sizes request_machine list_tasks list_epics emit_event list_repositories request_repository search_memory " +
		"get_memory remember run_diff",
	"simplifier": "list_machine_sizes request_machine list_tasks list_epics emit_event list_repositories request_repository search_memory " +
		"get_memory remember run_diff sync_base",
	"qa_browser": "list_machine_sizes request_machine list_tasks list_epics emit_event list_repositories request_repository search_memory " +
		"get_memory remember run_diff",
	"investigator": "list_machine_sizes request_machine list_tasks list_epics ask_person emit_event list_repositories request_repository " +
		"create_task search_memory get_memory remember run_diff message wait_for_reply note",
	"": "list_tasks list_epics emit_event list_repositories request_repository search_memory get_memory remember run_diff",
}

func TestEachRolesToolsAreTheSameAsBeforeCapabilities(t *testing.T) {
	roles := append(slices.Clone(delivery.PromptRoles), "")
	if len(roles) != len(toolsBeforeCapabilities) {
		t.Fatalf("roles %q, pinned %d", roles, len(toolsBeforeCapabilities))
	}
	for _, role := range roles {
		want := strings.Fields(toolsBeforeCapabilities[role])
		var got []string
		for _, tl := range granted(role, capabilities.Default(role)) {
			got = append(got, tl.name)
		}
		slices.Sort(want)
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Errorf("%q: got %q\nwant %q", role, got, want)
		}
	}
}

// A role's tool resolves to the definition its audience uses: a session
// agent's own where it has one, the task agents' otherwise.
func TestASessionAgentsNameFindsItsOwnDefinition(t *testing.T) {
	brainstorm := capabilities.Default(delivery.RoleBrainstorm)
	conductor := capabilities.Default(delivery.RoleConductor)
	own := map[string]bool{}
	for _, def := range allTools() {
		if !def.session {
			continue
		}
		own[def.name] = true
		tl, ok := find(def.name, delivery.RoleBrainstorm, brainstorm)
		if !ok || !tl.session || tl.description != def.description {
			t.Errorf("brainstorm %s: found %v, session %v, its own description %v", def.name, ok, tl.session,
				tl.description == def.description)
		}
		if tl, ok := find(def.name, delivery.RoleConductor, conductor); ok != conductor.Tool(def.name) || tl.session {
			t.Errorf("conductor %s: found %v, granted %v, session %v", def.name, ok, conductor.Tool(def.name), tl.session)
		}
	}
	// A shared definition answers a session agent where it has none of its
	// own (github_*, list_machine_sizes, request_machine).
	shared := 0
	for _, tl := range granted(delivery.RoleBrainstorm, brainstorm) {
		if own[tl.name] {
			continue
		}
		shared++
		if tl.session {
			t.Errorf("brainstorm %s: a session definition not among the registry's", tl.name)
		}
		if found, ok := find(tl.name, delivery.RoleBrainstorm, brainstorm); !ok || found.session {
			t.Errorf("brainstorm %s: found %v, session %v", tl.name, ok, found.session)
		}
	}
	if shared == 0 {
		t.Error("brainstorm reaches no shared definition")
	}
	if tl, ok := find("refresh_pull_requests", delivery.RoleBrainstorm, brainstorm); !ok || !tl.session {
		t.Errorf("brainstorm refresh_pull_requests: found %v, session %v", ok, tl.session)
	}
	// A session agent's own tool is no one else's, whatever they are granted.
	all := capabilities.Of(keys(capabilities.All)...)
	if _, ok := find("propose", delivery.RoleConductor, all); ok {
		t.Error("a conductor granted everything finds propose")
	}
	if _, ok := find("create_task", delivery.RoleBrainstorm, capabilities.Default(delivery.RoleBrainstorm)); ok {
		t.Error("brainstorm finds create_task")
	}
}

// A role without a capability is not given its tools, nor found calling
// them; taking one capability away takes away exactly its tools.
func TestTakingACapabilityAwayTakesItsTools(t *testing.T) {
	full := capabilities.Default(delivery.RoleConductor)
	less := full.Without(capabilities.Conduct)
	for _, tl := range granted(delivery.RoleConductor, full) {
		_, kept := find(tl.name, delivery.RoleConductor, less)
		unlocked := slices.Contains(capabilityOf(capabilities.Conduct).Tools, tl.name)
		if kept == unlocked {
			t.Errorf("%s: kept %v without conduct, conduct unlocks it %v", tl.name, kept, unlocked)
		}
	}
	if got := granted(delivery.RoleConductor, capabilities.Grants{}); len(got) != 0 {
		t.Errorf("no grants: %d tools", len(got))
	}
}

// The tool names more than one capability unlocks, in capabilities.All's
// order: a conductor starts task children, a brainstorm session children.
var unlockedTwice = map[string][]capabilities.Key{
	"start_agent": {capabilities.Conduct, capabilities.SessionAgents},
}

// Every tool the registry names exists, every tool is reachable through
// some capability, and every capability is granted to some role.
func TestTheRegistryHasNoDeadEntries(t *testing.T) {
	names := map[string]bool{}
	for _, tl := range allTools() {
		names[tl.name] = true
	}
	unlocked := map[string][]capabilities.Key{}
	seen := map[capabilities.Key]bool{}
	for _, c := range capabilities.All {
		if seen[c.Key] {
			t.Errorf("%s listed twice", c.Key)
		}
		seen[c.Key] = true
		if strings.TrimSpace(c.Description) == "" || len(c.Tools)+len(c.Native) == 0 {
			t.Errorf("%s: no description or nothing unlocked", c.Key)
		}
		for _, name := range c.Tools {
			if !names[name] {
				t.Errorf("%s unlocks %s, which is no tool", c.Key, name)
			}
			unlocked[name] = append(unlocked[name], c.Key)
		}
		for _, n := range c.Native {
			if n != capabilities.NativeWebSearch && n != capabilities.NativeWebFetch {
				t.Errorf("%s unlocks native %q, which no harness knows", c.Key, n)
			}
		}
	}
	for name := range names {
		if len(unlocked[name]) == 0 {
			t.Errorf("tool %s is reachable through no capability", name)
		}
	}
	for name, by := range unlocked {
		if len(by) < 2 {
			continue
		}
		// Only a name with a task and a session definition may be unlocked
		// twice, once for each audience.
		want, pinned := unlockedTwice[name]
		if !pinned || !slices.Equal(by, want) || !hasSessionDefinition(name) ||
			!slices.ContainsFunc(allTools(), func(tl tool) bool { return tl.name == name && !tl.session }) {
			t.Errorf("%s is unlocked by %q", name, by)
		}
	}
	for name := range unlockedTwice {
		if len(unlocked[name]) < 2 {
			t.Errorf("%s is pinned as unlocked twice, by %q", name, unlocked[name])
		}
	}
	granted := map[capabilities.Key]bool{}
	for role, keys := range capabilities.Defaults {
		if role != "" && !slices.Contains(delivery.PromptRoles, role) {
			t.Errorf("defaults for %q, which is no role", role)
		}
		for _, k := range keys {
			if !seen[k] {
				t.Errorf("%q is granted %s, which is no capability", role, k)
			}
			granted[k] = true
		}
	}
	for _, c := range capabilities.All {
		if !granted[c.Key] {
			t.Errorf("%s is granted to no role", c.Key)
		}
	}
	for _, role := range delivery.PromptRoles {
		if _, ok := capabilities.Defaults[role]; !ok {
			t.Errorf("%s has no default capabilities", role)
		}
	}
}

// The instruction file names a tool when the Run's capabilities grant it
// (delivery.InstructionTools): each such name is a tool the Run is given.
func TestTheInstructionFileNamesOnlyGrantedTools(t *testing.T) {
	for _, role := range delivery.PromptRoles {
		grants := capabilities.Default(role)
		for _, name := range delivery.InstructionTools {
			if _, ok := find(name, role, grants); grants.Tool(name) != ok {
				t.Errorf("%s: %s granted %v, found %v", role, name, grants.Tool(name), ok)
			}
		}
	}
}

func keys(all []capabilities.Capability) []capabilities.Key {
	var out []capabilities.Key
	for _, c := range all {
		out = append(out, c.Key)
	}
	return out
}

func capabilityOf(k capabilities.Key) capabilities.Capability {
	for _, c := range capabilities.All {
		if c.Key == k {
			return c
		}
	}
	return capabilities.Capability{}
}
