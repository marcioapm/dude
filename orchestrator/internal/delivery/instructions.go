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
	// A conductor-started child: a writer gets the writer-child contract,
	// any other the report-only contract (childContract).
	Child        bool
	SessionChild bool
	// The Run has dude's tools, and the dude CLI on its shell.
	Tools, CLI bool
	// The Run's capabilities (capabilities.Resolve): which tools the file
	// names. nil: its role's defaults, a fixer's being the implementer's.
	Grants *capabilities.Grants
	// A brainstorm that may reach any host.
	Anywhere bool
	// What a brainstorm's harness reaches of the web, as its spec configures
	// it (phases.runWeb). nil: its grants on a harness with both tools, any
	// host when Anywhere, else none.
	Web *Web
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
	// A fix on an open pull request goes to it directly, then is reviewed
	// (Policy.DirectFixes); else it is checked on a candidate first.
	DirectFixes bool
	// The delivery's review loop is the conductor's (State.ConductedLoop)
	// as the Run is submitted: a reviewer's severities are by outcome, and
	// each finding at medium or above carries its evidence.
	ConductedLoop bool
}

// Web is what a Run reaches of the web with its harness's own tools.
type Web struct {
	Search bool `json:"search"`
	// Fetch: the harness has a fetch tool, which reaches any host when
	// AnyHost, else Hosts alone (the egress lists dude was given).
	Fetch   bool     `json:"fetch"`
	AnyHost bool     `json:"anyHost"`
	Hosts   []string `json:"hosts,omitempty"`
}

// Fetches says the fetch tool can reach some host.
func (w Web) Fetches() bool { return w.Fetch && (w.AnyHost || len(w.Hosts) > 0) }

// HostList is Hosts as a sentence names them.
func (w Web) HostList() string { return joinList(w.Hosts, ", ", ", and ") }

// Equal says w and o give the agent the same web.
func (w Web) Equal(o Web) bool {
	return w.Search == o.Search && w.Fetches() == o.Fetches() &&
		(!w.Fetches() || (w.AnyHost == o.AnyHost && slices.Equal(w.Hosts, o.Hosts)))
}

// WebChanged is what a session's new agent is told when the agent before
// it had another web: the replayed conversation shows that one's searches
// and fetches.
func WebChanged(before, now Web) string {
	return webChangedTmpl.Render(struct{ Before, Now Web }{before, now})
}

var webChangedTmpl = MustText("notices/web-changed")

// webOf is the web a Run's file describes: in.Web, or its defaults, with
// what grants do not unlock taken away.
func (in InstructionsInput) webOf(grants capabilities.Grants) Web {
	w := Web{Search: true, Fetch: true, AnyHost: in.Anywhere}
	if in.Web != nil {
		w = *in.Web
	}
	w.Search = w.Search && grants.Native(capabilities.NativeWebSearch)
	w.Fetch = w.Fetch && grants.Native(capabilities.NativeWebFetch)
	return w
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
		Child: in.Child, SessionChild: in.SessionChild, Tools: in.Tools, CLI: in.CLI, Anywhere: in.Anywhere, Checkouts: len(in.Repositories) > 0, BlockingSeverities: in.BlockingSeverities,
		OrgPrompt: in.OrgPrompt, ProjectPrompt: in.ProjectPrompt, ProjectPromptMode: in.ProjectPromptMode,
		Context:     in.Context,
		Vars:        PromptVars{in.Title, in.Goal, in.AcceptanceCriteria, in.Branch, in.BaseRef},
		DirectFixes: in.DirectFixes, ConductedLoop: in.ConductedLoop,
	}
}

// InstructionTools are the tools the instruction file can name; a Run's
// file names those its capabilities grant.
var InstructionTools = []string{"ask_person", "search_memory", "remember", "emit_event", "create_task", "request_repository",
	"run_diff", "request_machine", "sync_base", "request_limit"}

// ChildTools are the tools a conductor-started child may call.
var ChildTools = []string{"list_tasks", "list_epics", "list_repositories", "search_memory", "get_memory", "run_diff", "list_machine_sizes", "emit_event"}

// instructionData is what every instruction template is given.
type instructionData struct {
	InstructionsInput
	Role string
	// Which of the tools dude.md names this Run has.
	Ask, Event, CreateTask, Repository, RunDiff, SyncBase, SearchMemory, Remember, RequestMachine bool
	ChildToolNames                                                                                string
	// How a role that reads GitHub reads what was written outside dude.
	ExternalTextNote string
	// What the Run reaches of the web (webOf).
	Reach  Web
	grants capabilities.Grants
}

// Granted says the Run's capabilities grant every one of tools, with or
// without dude's tools (Tools): a role's prose names its tools either way.
// "!name" is satisfied when name is not granted.
func (d instructionData) Granted(tools ...string) bool {
	for _, t := range tools {
		if name, not := strings.CutPrefix(t, "!"); d.grants.Tool(name) == not {
			return false
		}
	}
	return true
}

// List joins the texts of pairs (a Granted condition, space-separated, then
// its text) whose condition holds and whose text is not empty: sep between
// them, last before the final one ("" when none is left).
func (d instructionData) List(sep, last string, pairs ...string) string {
	var items []string
	for i := 0; i+1 < len(pairs); i += 2 {
		if pairs[i+1] != "" && d.Granted(strings.Fields(pairs[i])...) {
			items = append(items, pairs[i+1])
		}
	}
	return joinList(items, sep, last)
}

// Wrap is s between before and after, or "" when s is.
func (instructionData) Wrap(before, s, after string) string {
	if s == "" {
		return ""
	}
	return before + s + after
}

// joinList joins items with sep, and last before the final one; of two,
// a last of ", and" loses its comma.
func joinList(items []string, sep, last string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	case 2:
		if last != sep {
			last = strings.TrimPrefix(last, ",")
		}
	}
	return strings.Join(items[:len(items)-1], sep) + last + items[len(items)-1]
}

// toolRoleOf is the role a Run calls its tools as: a fixer is the
// implementer's Run, told something else.
func toolRoleOf(role string) string {
	if role == "fixer" {
		return "implementer"
	}
	return role
}

func (in InstructionsInput) data(role string) instructionData {
	grants := in.Grants
	if grants == nil {
		g := capabilities.Default(toolRoleOf(role))
		grants = &g
	}
	// dude's tools with none of them granted are none: the file says so.
	if !slices.ContainsFunc(capabilities.All, func(c capabilities.Capability) bool { return len(c.Tools) > 0 && grants.Has(c.Key) }) {
		in.Tools, in.CLI = false, false
	}
	has := map[string]bool{}
	if in.Tools {
		for _, t := range InstructionTools {
			if grants.Tool(t) && (!in.Child || slices.Contains(ChildTools, t)) {
				has[t] = true
			}
		}
	}
	var childTools []string
	for _, t := range ChildTools {
		if grants.Tool(t) {
			childTools = append(childTools, t)
		}
	}
	return instructionData{InstructionsInput: in, Role: role, Ask: has["ask_person"], Event: has["emit_event"],
		CreateTask: has["create_task"], Repository: has["request_repository"], RunDiff: has["run_diff"], SyncBase: has["sync_base"],
		SearchMemory: has["search_memory"], Remember: has["remember"], RequestMachine: has["request_machine"],
		ChildToolNames:   joinList(childTools, ", ", ", and "),
		ExternalTextNote: ExternalTextNote, Reach: in.webOf(*grants), grants: *grants}
}

var (
	charterTmpl     = mustTemplate("instructions/charter")
	dudeTmpl        = mustTemplate("instructions/dude")
	savedTmpl       = mustTemplate("instructions/saved")
	roleNotesTmpl   = mustTemplate("instructions/notes")
	childToolsTmpl  = mustTemplate("contracts/child-tools")
	childWriterTmpl = mustTemplate("contracts/child-writer")
)

// childContract selects the contract from the phase's publishing authority.
func childContract(role string) *template.Template {
	if Publishes[PhaseForPromptRole(role)] {
		return childWriterTmpl
	}
	return childToolsTmpl
}

// ChildWriterContract renders the writer-child contract for a role.
func ChildWriterContract(role string, in InstructionsInput) string {
	return render(childWriterTmpl, in.data(role))
}

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
		sections = append(sections, render(childContract(role), d))
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
