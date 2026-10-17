package delivery

import (
	"strings"
	"text/template"
)

// InstructionsInput is what a Run's instruction file (/workspace/AGENTS.md)
// is assembled from: what the Run has, and the saved prompts for its role.
// Nothing in it changes during a Run, so a resume assembles the same file.
type InstructionsInput struct {
	// The Run has dude's tools, and the dude CLI on its shell.
	Tools, CLI bool
	// A brainstorm that may reach any host.
	Anywhere bool
	// The severities that send a change back (a reviewer's and a tester's).
	BlockingSeverities []string
	// The organisation's saved prompt for the role (nil: none), and the
	// project's with its mode ("add" or "replace"; "" has none).
	OrgPrompt                        *string
	ProjectPrompt, ProjectPromptMode string
	// Named in the saved-prompt headings when known.
	OrgName, ProjectName string
	// The project's notes for the role (its agent settings' context).
	Context string
	// What a saved prompt's {{task.*}} and {{run.*}} variables say.
	Vars PromptVars
}

// PromptVars are the values a saved prompt's variables are filled with.
type PromptVars struct {
	Title, Goal     string
	Criteria        []string
	Branch, BaseRef string
}

// InstructionsInput is the instruction file's part of a phase's input,
// image references in the goal and criteria written as the Run reads them.
func (in PromptInput) InstructionsInput() InstructionsInput {
	in = in.withImages()
	return InstructionsInput{
		Tools: in.Tools, CLI: in.CLI, Anywhere: in.Anywhere, BlockingSeverities: in.BlockingSeverities,
		OrgPrompt: in.OrgPrompt, ProjectPrompt: in.ProjectPrompt, ProjectPromptMode: in.ProjectPromptMode,
		Context: in.Context,
		Vars:    PromptVars{in.Title, in.Goal, in.AcceptanceCriteria, in.Branch, in.BaseRef},
	}
}

// toolRoles is the role each settings role's Run calls dude's tools as: a
// fixer is the implementer's Run, told something else.
var toolRoles = map[string]string{"fixer": "implementer"}

// ToolRole is the role a Run of settings role calls dude's tools as.
func ToolRole(role string) string {
	if r, ok := toolRoles[role]; ok {
		return r
	}
	return role
}

// InstructionTools are the tools the instruction file names, by the role a
// Run calls them as; agenttools' tests hold each list to the tools that
// role is given.
var InstructionTools = map[string][]string{
	"implementer":  {"ask_person", "search_memory", "remember", "emit_event", "create_task", "request_repository", "run_diff", "request_machine", "sync_base"},
	"investigator": {"ask_person", "search_memory", "remember", "emit_event", "create_task", "request_repository", "run_diff", "request_machine"},
	"reviewer":     {"search_memory", "remember", "emit_event", "request_repository", "run_diff", "request_machine"},
	"simplifier":   {"search_memory", "remember", "emit_event", "request_repository", "run_diff", "request_machine", "sync_base"},
	"qa_browser":   {"search_memory", "remember", "emit_event", "request_repository", "run_diff", "request_machine"},
	RoleConductor:  {"ask_person", "search_memory", "remember", "emit_event", "create_task", "request_repository", "run_diff", "request_machine", "sync_base"},
	RoleBrainstorm: {"ask_person", "search_memory", "remember", "request_machine"},
}

// instructionData is what every instruction template is given.
type instructionData struct {
	InstructionsInput
	Role string
	// Which of the tools dude.md names this Run has.
	Ask, Event, CreateTask, Repository, RunDiff, SyncBase bool
}

func (in InstructionsInput) data(role string) instructionData {
	has := map[string]bool{}
	if in.Tools {
		for _, t := range InstructionTools[ToolRole(role)] {
			has[t] = true
		}
	}
	return instructionData{InstructionsInput: in, Role: role, Ask: has["ask_person"], Event: has["emit_event"],
		CreateTask: has["create_task"], Repository: has["request_repository"], RunDiff: has["run_diff"], SyncBase: has["sync_base"]}
}

var (
	charterTmpl   = mustTemplate("instructions/charter")
	dudeTmpl      = mustTemplate("instructions/dude")
	savedTmpl     = mustTemplate("instructions/saved")
	roleNotesTmpl = mustTemplate("instructions/notes")
)

// roleParts are each role file's three parts (parseRole), resolved at init.
var roleParts = func() map[string][3]*template.Template {
	out := map[string][3]*template.Template{}
	for _, role := range PromptRoles {
		out[role] = [3]*template.Template{mustTemplate("roles/" + role + "/head"),
			mustTemplate("roles/" + role + "/stance"), mustTemplate("roles/" + role + "/fixed")}
	}
	return out
}()

// Instructions is a Run's instruction file: How you work, How dude works,
// the role, a reviewer's aspect, then the saved prompts for the role, each
// under its own heading.
//
// A saved prompt never removes a section dude ships: in replace mode it
// stands in for the role's stance ("How you work as <role>") alone, and in
// add mode it follows it. An organisation's prompt replaces; a project's
// replaces the organisation's too in replace mode, as it always has.
func Instructions(role, aspect string, in InstructionsInput) string {
	parts, ok := roleParts[role]
	if !ok {
		return ""
	}
	d := in.data(role)
	sections := []string{render(charterTmpl, d), render(dudeTmpl, d), render(parts[0], d)}

	org := in.OrgPrompt
	if org != nil && strings.TrimSpace(*org) == "" {
		org = nil
	}
	project := strings.TrimSpace(in.ProjectPrompt)
	replaced := in.ProjectPromptMode == "replace"
	if replaced {
		org = nil
	}
	if org == nil && !replaced {
		sections = append(sections, render(parts[1], d))
	}
	if fixed := render(parts[2], d); fixed != "" {
		sections = append(sections, fixed)
	}
	if role == "reviewer" {
		if text := reviewerAspect(aspect); text != "" {
			sections = append(sections, text)
		}
	}
	saved := struct {
		Org, Project, OrgName, ProjectName, Stance string
		OrgReplaces, ProjectReplaces, OrgDropped   bool
	}{OrgName: in.OrgName, ProjectName: in.ProjectName, Stance: stanceTitle(parts[1], d),
		OrgReplaces: org != nil, ProjectReplaces: replaced, OrgDropped: replaced && in.OrgPrompt != nil}
	if org != nil {
		saved.Org = strings.TrimSpace(in.Vars.fill(*org))
	}
	if replaced || in.ProjectPromptMode == "add" {
		saved.Project = in.Vars.fill(project)
	}
	if saved.Org != "" || saved.Project != "" {
		sections = append(sections, render(savedTmpl, saved))
	}
	if c := strings.TrimSpace(in.Context); c != "" {
		sections = append(sections, render(roleNotesTmpl, c))
	}
	return strings.Join(sections, "\n\n") + "\n"
}

// reviewerAspect is the reviewer's aspect for category, correctness for a
// category without one.
func reviewerAspect(category string) string {
	if reviewFocus[category] == "" {
		category = "correctness"
	}
	return reviewFocus[category]
}

// stanceTitle is the stance section's heading, without its "## ".
func stanceTitle(stance *template.Template, d instructionData) string {
	first, _, _ := strings.Cut(render(stance, d), "\n")
	return strings.TrimPrefix(first, "## ")
}

// BuiltinStance is the part of a role's instructions a saved prompt in
// replace mode stands in for: what the settings page shows as dude's.
func BuiltinStance(role string) string {
	parts, ok := roleParts[role]
	if !ok {
		return ""
	}
	return render(parts[1], InstructionsInput{Tools: true, CLI: true}.data(role))
}

// fill puts the task and the Run into a saved prompt where it names them.
func (v PromptVars) fill(prompt string) string {
	return PromptInput{Title: v.Title, Goal: v.Goal, AcceptanceCriteria: v.Criteria, Branch: v.Branch, BaseRef: v.BaseRef}.fill(prompt)
}
