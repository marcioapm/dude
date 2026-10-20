package delivery

import (
	"fmt"
	"regexp"
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

// Each tool section of the instruction file is there exactly when the
// Run's capabilities grant its tool: for every role, taking away the
// capability that unlocks it takes away that section.
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
		}
	}
	for _, tool := range InstructionTools {
		if !checked[tool] {
			t.Errorf("%s: no role's file was checked for its section", tool)
		}
	}
}

// The shell form of each dude tool the instruction files show, as
// cmd/dude parses it.
var toolCommands = map[string][]string{
	"list_tasks": {"dude task list"}, "list_epics": {"dude epic list"}, "list_repositories": {"dude repo list"},
	"create_task": {"dude task create"}, "search_memory": {"dude memory search"}, "get_memory": {"dude memory show"},
	"remember": {"dude memory add"}, "emit_event": {"dude event"}, "run_diff": {"dude diff"},
	"request_repository": {"dude repo request"}, "ask_person": {"dude ask"}, "list_machine_sizes": {"dude machine sizes"},
	"request_machine": {"dude machine request"}, "message": {"dude message"}, "wait_for_reply": {"dude wait"},
	"note": {"dude note"}, "feedback": {"dude feedback"}, "feedback_reply": {"dude feedback reply"},
	"feedback_resolve": {"dude feedback resolve"}, "reply_on_pull_request": {"dude pr reply"}, "findings": {"dude findings"},
	"pull_requests": {"dude prs"}, "start_phase": {"dude phase start"}, "start_agent": {"dude agent start"},
	"steer": {"dude steer"}, "restart_run": {"dude restart"}, "decide": {"dude decide"},
	"dismiss_finding": {"dude finding dismiss"}, "decide_escalation": {"dude escalation decide"},
	"update_task": {"dude task update"}, "publish": {"dude publish --message"}, "relate": {"dude relate"},
	"refresh_pull_requests": {"dude prs --refresh", "--refresh"}, "run_chat": {"dude chat"}, "limits": {"dude limits"},
}

// Prose in which a tool's name is an ordinary word, or a flag of another
// command, pinned as the exact phrase it occurs in. A tool name anywhere
// else in the file names the tool.
var notTheTool = map[string][]string{
	"decide": {"**People decide**", "and what you decide", "read what you need with your tools, then decide.",
		"start nothing and decide nothing", "whatever you decide", "only a person may decide", "the states that decide it"},
	"feedback": {"reviews and feedback)", "reviews and feedback, as dude last read them", "reviews and feedback, read from GitHub now",
		"come back as feedback.", "findings and feedback you are given"},
	// refresh_pull_requests' shell form is a flag of dude prs.
	"pull_requests": {"`dude prs --refresh`"},
	"findings": {"triages findings", "(some or all findings)", "report findings with a severity",
		"structured findings and tester verdicts", "Your findings go", "risks new findings", "after any findings",
		"are findings.", "role's findings or verdict", "on earlier findings", "Your output is findings", "resolve the findings", "report and findings"},
	"message": {"first message lists them", "default merge message", "a Chat message", "your next message", "--message M",
		"Each message reaches you"},
	"note": {"not a note to leave", "a short note at each decision", "--note N", "(a note, optionally another tier)",
		"`low` or `note`", "`note` is an observation", "| low | note", "is a `note`", "a design note"},
	"propose": {"before you propose anything"},
	"publish": {"`dude publish", "the files you publish", "publish the report", "publish the video", "Never publish code"},
}

// Mentions of a tool the role's defaults never grant, which the goldens
// pin: the simplifier's checks line names ask_person, which no simplifier
// has. Known defects, not prose; each is its exact phrase.
var ungrantedInGoldens = map[string][]string{
	"simplifier/ask_person": {"If you cannot, ask with `ask_person`"},
}

// toolMentions are the places role's text names tool, by its name as a
// whole word or by its shell command, outside the excused phrases.
func toolMentions(text, role, tool string) []string {
	var excused [][2]int
	for _, phrase := range append(slices.Clone(notTheTool[tool]), ungrantedInGoldens[role+"/"+tool]...) {
		for at := 0; ; {
			i := strings.Index(text[at:], phrase)
			if i < 0 {
				break
			}
			excused = append(excused, [2]int{at + i, at + i + len(phrase)})
			at += i + len(phrase)
		}
	}
	var out []string
	for _, word := range append([]string{tool}, toolCommands[tool]...) {
		re := regexp.MustCompile(`(^|[^A-Za-z0-9_])(` + regexp.QuoteMeta(word) + `)($|[^A-Za-z0-9_])`)
		for _, m := range re.FindAllStringSubmatchIndex(text, -1) {
			if slices.ContainsFunc(excused, func(e [2]int) bool { return e[0] <= m[4] && m[5] <= e[1] }) {
				continue
			}
			out = append(out, fmt.Sprintf("%q in …%s…", word, text[max(0, m[4]-60):min(len(text), m[5]+40)]))
		}
	}
	return out
}

// instructionRuns are the instruction files a role's Run can have with
// dude's tools: its own, and an independent child's.
func instructionRuns(role string, grants *capabilities.Grants) map[string]InstructionsInput {
	in := InstructionsInput{Tools: true, CLI: true, Checkouts: true, Anywhere: role == RoleBrainstorm, Grants: grants,
		BlockingSeverities: []string{"blocking", "high"}}
	out := map[string]InstructionsInput{role: in}
	if role != RoleConductor && role != RoleBrainstorm {
		in.Child = true
		out[role+" child"] = in
	}
	return out
}

// For every role and every capability it has, a Run without that
// capability is never told of its tools: not by name, not by command,
// anywhere in its instruction file.
func TestAnInstructionFileNamesNoToolOfACapabilityItLacks(t *testing.T) {
	for _, role := range PromptRoles {
		defaults := capabilities.Default(toolRoleOf(role))
		for _, c := range capabilities.All {
			if !defaults.Has(c.Key) || len(c.Tools) == 0 {
				continue
			}
			less := defaults.Without(c.Key)
			for name, in := range instructionRuns(role, &less) {
				got := Instructions(role, "correctness", in)
				for _, tool := range c.Tools {
					for _, m := range toolMentions(got, role, tool) {
						t.Errorf("%s without %s names %s: %s", name, c.Key, tool, m)
					}
				}
			}
		}
	}
}

// A Run given dude's tools but no capability that grants one reads what a
// Run without dude's tools reads, and is told of no tool.
func TestARunWithToolsButNoGrantsIsToldItHasNone(t *testing.T) {
	var none capabilities.Grants
	for _, role := range PromptRoles {
		for name, in := range instructionRuns(role, &none) {
			got := Instructions(role, "correctness", in)
			bare := in
			bare.Tools, bare.CLI = false, false
			if want := Instructions(role, "correctness", bare); got != want {
				t.Errorf("%s with no grants differs from its file without dude's tools", name)
			}
			if !strings.Contains(got, "This Run has none of dude's tools:") && !in.Child {
				t.Errorf("%s with no grants is not told it has no tools", name)
			}
			for _, c := range capabilities.All {
				for _, tool := range c.Tools {
					for _, m := range toolMentions(got, role, tool) {
						t.Errorf("%s with no grants names %s: %s", name, tool, m)
					}
				}
			}
		}
	}
}

func capabilityUnlocking(tool string) capabilities.Key {
	for _, c := range capabilities.All {
		if slices.Contains(c.Tools, tool) {
			return c.Key
		}
	}
	return ""
}
