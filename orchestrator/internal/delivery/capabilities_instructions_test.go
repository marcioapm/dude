package delivery

import (
	"slices"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/capabilities"
)

// The instruction file names the tools the Run's capabilities grant: the
// role's defaults name what they always did, and a Run whose capabilities
// lack one is not told of it.
func TestTheInstructionFileFollowsTheRunsCapabilities(t *testing.T) {
	for _, role := range []string{"implementer", "fixer", RoleConductor, "investigator"} {
		in := InstructionsInput{Tools: true, CLI: true}
		builtin := Instructions(role, "correctness", in)
		defaults := capabilities.Default(toolRoleOf(role))
		in.Grants = &defaults
		if got := Instructions(role, "correctness", in); got != builtin {
			t.Errorf("%s: its default capabilities, named, change its instruction file", role)
		}
	}
}

// The sections of the instruction file that exist for one tool, as they
// render for role: each is in the file while the tool is granted, and gone
// with its capability.
func sectionsFor(tool, role string) []string {
	switch tool {
	case "ask_person":
		out := []string{"- **Ask a person**: `ask_person`, when you cannot go on without a decision only a person can make. " +
			"Several questions go in one call. The answer comes back as your next message, even hours later; your work is kept meanwhile."}
		if role == "implementer" || role == "fixer" {
			out = append(out, "## Asking a person\n"+render(mustTemplate("contracts/ask"), nil))
		}
		return out
	case "search_memory":
		return []string{"- **Memory**: what people and agents here learned before you. Search it (`search_memory`) before investigating " +
			"something that may be known"}
	case "remember":
		return []string{"save what the next agent should know (`remember`), after searching so you do not save it twice."}
	case "emit_event":
		return []string{"- **Progress**: `emit_event` / `dude event progress` so people can follow long work."}
	case "create_task":
		return []string{"- **Other work you find**: record it as a task (`create_task`); a person decides on it. Do not do it inside yours."}
	case "request_repository":
		return []string{"- **Another repository you need**: `request_repository`."}
	case "run_diff":
		return []string{"- **What a Run changed**: `run_diff`."}
	case "request_machine":
		return []string{"- **A bigger machine**: `dude machine request SIZE --reason \"…\"` when something is killed for memory or far too slow."}
	case "sync_base":
		switch role {
		case "implementer", "fixer", "simplifier":
			return []string{baseIntegrationNote}
		case RoleConductor:
			return []string{"## Merging the base\n" + conductorBaseNote}
		}
	}
	return nil
}

// Lines that name a tool whatever the Run is granted: a role's prose or
// its tool contract, not one of the file's tool sections. Pinned so that a
// new unconditional mention is a failure to look at.
var unconditionalMentions = map[string][]string{
	"implementer/ask_person": {"- A failed check or one that will not run is a problem to solve"},
	"fixer/ask_person":       {"- A failed check or one that will not run is a problem to solve"},
	"conductor/ask_person": {"- Ask the pull-request question (`decide ask_person`)", "When the delivery escalates to a person",
		"The dude tools read what dude knows about this task"},
	"conductor/create_task":   {"When the delivery takes the decisions", "The dude tools read what dude knows about this task"},
	"conductor/search_memory": {"The dude tools read what dude knows about this task"},
	"conductor/run_diff":      {"The dude tools read what dude knows about this task"},
	"brainstorm/ask_person":   {"- Several people may write to you.", "The dude tools read the linked projects"},
	"brainstorm/search_memory": {"The dude tools read the linked projects"},
	"brainstorm/remember":      {"The dude tools read the linked projects"},
}

// Each tool the instruction file can name is named, in its own section,
// exactly when the Run's capabilities grant it: for every role, taking
// away the capability that unlocks it takes away that section and every
// other mention of the tool but the pinned prose.
func TestEachToolSectionIsInTheFileExactlyWhenItsToolIsGranted(t *testing.T) {
	checked := map[string]bool{}
	for _, role := range PromptRoles {
		defaults := capabilities.Default(toolRoleOf(role))
		for _, tool := range InstructionTools {
			key := capabilityUnlocking(tool)
			if !defaults.Has(key) {
				continue
			}
			full := InstructionsInput{Tools: true, CLI: true, Checkouts: true, Grants: &defaults}
			less := defaults.Without(key)
			without := full
			without.Grants = &less
			with, got := Instructions(role, "correctness", full), Instructions(role, "correctness", without)
			for _, section := range sectionsFor(tool, role) {
				checked[tool] = true
				if !strings.Contains(with, section) {
					t.Errorf("%s granted %s: no section %q", role, tool, section)
				}
				if strings.Contains(got, section) {
					t.Errorf("%s without %s: section %q", role, key, section)
				}
			}
			var left []string
			for _, line := range strings.Split(got, "\n") {
				if strings.Contains(line, tool) && !slices.ContainsFunc(unconditionalMentions[role+"/"+tool],
					func(prefix string) bool { return strings.HasPrefix(line, prefix) }) {
					left = append(left, line)
				}
			}
			if len(left) > 0 {
				t.Errorf("%s without %s still names %s:\n%s", role, key, tool, strings.Join(left, "\n"))
			}
		}
	}
	for _, tool := range InstructionTools {
		if !checked[tool] {
			t.Errorf("%s: no role's file was checked for its section", tool)
		}
	}
}

func toolRoleOf(role string) string {
	if role == "fixer" {
		return "implementer"
	}
	return role
}

func capabilityUnlocking(tool string) capabilities.Key {
	for _, c := range capabilities.All {
		if slices.Contains(c.Tools, tool) {
			return c.Key
		}
	}
	return ""
}
