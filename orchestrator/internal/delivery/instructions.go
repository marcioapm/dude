package delivery

import (
	"slices"
	"strings"
	"text/template"

	"github.com/marciomartins/dude/orchestrator/internal/capabilities"
)

// InstructionsInput is what a Run's instruction file (/workspace/AGENTS.md)
// is assembled from: what the Run has, and the saved prompts for its role.
// Nothing in it changes during a Run, so a resume assembles the same file.
type InstructionsInput struct {
	// An independent child receives the report-only contract.
	Child bool
	// The Run has dude's tools, and the dude CLI on its shell.
	Tools, CLI bool
	// The Run's capabilities (capabilities.Resolve): which tools the file
	// names. nil: its role's defaults, a fixer's being the implementer's.
	Grants *capabilities.Grants
	// A brainstorm that may reach any host.
	Anywhere bool
	// A conductor with a checkout, which merges the base itself.
	Checkouts bool
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
		Child: in.Child, Tools: in.Tools, CLI: in.CLI, Anywhere: in.Anywhere, Checkouts: len(in.Repositories) > 0, BlockingSeverities: in.BlockingSeverities,
		OrgPrompt: in.OrgPrompt, ProjectPrompt: in.ProjectPrompt, ProjectPromptMode: in.ProjectPromptMode,
		Context: in.Context,
		Vars:    PromptVars{in.Title, in.Goal, in.AcceptanceCriteria, in.Branch, in.BaseRef},
	}
}

// InstructionTools are the tools the instruction file can name; a Run's
// file names those its capabilities grant.
var InstructionTools = []string{"ask_person", "search_memory", "remember", "emit_event", "create_task", "request_repository",
	"run_diff", "request_machine", "sync_base", "request_limit"}

// ChildTools are the tools an independent report child may call.
var ChildTools = []string{"list_tasks", "list_epics", "list_repositories", "search_memory", "get_memory", "run_diff", "list_machine_sizes", "emit_event"}

// instructionData is what every instruction template is given.
type instructionData struct {
	InstructionsInput
	Role string
	// Which of the tools dude.md names this Run has.
	Ask, Event, CreateTask, Repository, RunDiff, SyncBase, Remember, RequestMachine bool
	// Which of the conductor's tools its contract names this Run has.
	RequestLimit, Verify bool
	ChildToolNames       string
	// How a role that reads GitHub reads what was written outside dude.
	ExternalTextNote string
}

func (in InstructionsInput) data(role string) instructionData {
	grants := in.Grants
	if grants == nil {
		// A fixer is the implementer's Run, told something else.
		toolRole := role
		if role == "fixer" {
			toolRole = "implementer"
		}
		g := capabilities.Default(toolRole)
		grants = &g
	}
	has := map[string]bool{}
	if in.Tools {
		for _, t := range InstructionTools {
			if grants.Tool(t) && (!in.Child || slices.Contains(ChildTools, t)) {
				has[t] = true
			}
		}
		// Not in InstructionTools: "verify" is a word every role's text uses.
		has["verify"] = grants.Tool("verify") && !in.Child
	}
	return instructionData{InstructionsInput: in, Role: role, Ask: has["ask_person"], Event: has["emit_event"],
		CreateTask: has["create_task"], Repository: has["request_repository"], RunDiff: has["run_diff"], SyncBase: has["sync_base"],
		Remember: has["remember"], RequestMachine: has["request_machine"], RequestLimit: has["request_limit"], Verify: has["verify"],
		ChildToolNames:   strings.Join(ChildTools[:len(ChildTools)-1], ", ") + ", and " + ChildTools[len(ChildTools)-1],
		ExternalTextNote: ExternalTextNote}
}

var (
	charterTmpl    = mustTemplate("instructions/charter")
	dudeTmpl       = mustTemplate("instructions/dude")
	savedTmpl      = mustTemplate("instructions/saved")
	roleNotesTmpl  = mustTemplate("instructions/notes")
	childToolsTmpl = mustTemplate("contracts/child-tools")
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
// replaces the organisation's too in replace mode.
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
	stance := render(parts[1], d)
	if org == nil && !replaced {
		sections = append(sections, stance)
	}
	if fixed := render(parts[2], d); fixed != "" {
		sections = append(sections, fixed)
	}
	if role == "reviewer" {
		sections = append(sections, reviewFocus[reviewCategory(aspect)])
	}
	stanceTitle, _, _ := strings.Cut(stance, "\n")
	saved := struct {
		Org, Project, OrgName, ProjectName, Stance string
		OrgReplaces, ProjectReplaces, OrgDropped   bool
	}{OrgName: in.OrgName, ProjectName: in.ProjectName, Stance: strings.TrimPrefix(stanceTitle, "## "),
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
	if in.Child {
		sections = append(sections, render(childToolsTmpl, d))
	}
	return strings.Join(sections, "\n\n") + "\n"
}

// reviewCategory is category when it has an aspect, else correctness.
func reviewCategory(category string) string {
	if reviewFocus[category] == "" {
		return "correctness"
	}
	return category
}

// BuiltinPrompt is the part of a role's instructions a saved prompt stands
// in for: its "How you work as …" section as dude ships it, what the
// settings page shows as dude's and where a first edit starts from. The
// rest of the instruction file is dude's whatever is saved.
func BuiltinPrompt(role string) string {
	parts, ok := roleParts[role]
	if !ok {
		return ""
	}
	return render(parts[1], InstructionsInput{Tools: true, CLI: true}.data(role))
}
