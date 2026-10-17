package delivery

import (
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

// PromptInput is everything a phase's prompt is composed from — what the
// ledger already knows, rather than a model summarising work another model
// already did.
type PromptInput struct {
	Title              string
	Goal               string
	AcceptanceCriteria []string
	// Review phase: which reviewer flavour this is.
	Category string
	// Fix phase: the findings to address, and any pull request feedback.
	// Review phase: the earlier findings in its category, to judge.
	Findings   []Finding
	PRFeedback []forge.ActionableFeedback
	// Implement and fix: people's feedback items given to the Run, in
	// their own words, each to resolve or reply to.
	Feedback []FeedbackItem
	// Review phase: the severities that block, from the delivery's policy.
	BlockingSeverities []string
	// Review phase limited to a range (PhaseRun.ReviewRange): the commit
	// per repository it starts after, and why (RangeSimplify,
	// RangePullRequestFix). Empty reviews the whole branch.
	ReviewBase map[string]string
	ReviewOf   string
	// Per-project, per-role notes, appended last so a project can tell its
	// reviewer what this codebase considers a defect without that text
	// reaching every other role.
	Context string
	// The repositories checked out for the agent. Named in the prompt only
	// when there are several, or one it must not change, or none.
	Repositories []PromptRepo
	// The Run has dude's tools over MCP: the only way it can ask a person.
	Tools bool
	// It also has the dude CLI (lux serves dude's tools in the container).
	CLI bool
	// A brainstorm that may reach any host, and is told it can read the web.
	Anywhere bool
	// The organization's prompt for the phase's role, when it has saved
	// one: it replaces dude's built-in instructions. nil runs dude's.
	OrgPrompt *string
	// The project's prompt for the role, and whether it is added after the
	// organization's ("add") or replaces it ("replace"). "" has none.
	ProjectPrompt, ProjectPromptMode string
	// What people decided while this work was delivered: agents' questions
	// and their answers. Part of the task from then on, for every phase.
	Decisions []Decision
	// The branch the Run works on, and what it started from: what a saved
	// prompt's {{run.branch}} and {{run.base_ref}} say.
	Branch, BaseRef string
	// A conductor's: the task branch its checkout is kept current with.
	TaskBranch string
	// The task took over an existing branch: what it merges into, by
	// repository. Empty for new work, whose base is the default branch.
	Bases map[string]string
	// The existing pull request the task took over, when it did: its
	// unresolved threads, changes requested and red checks are the
	// reviewers' context.
	Adopted *AdoptedContext
	// Implement phase of a task that took over unfinished work: the work
	// it carries on. nil for new work.
	Continues *Source
	// What the task's conductor asked of this Run, when it started it.
	ConductorNote string
	// Why this Run replaces one restarted in its step (RestartRunTx).
	RestartNote string
	// The images the Run is given with its prompt, in the order lux gets
	// them (TaskImages): each reference in the goal and criteria is
	// written as its place in this list.
	Images []PromptImage
	// Where the agent runs and the sizes it may move to
	// (MachineChoices.Line), last in a phase's prompt; "" says nothing. A
	// conductor's and a session agent's prompt get it after, at submit.
	Machine string
	// The task's epic as the Run is briefed on it (EpicBriefing): its
	// peers, the notes in force, messages held for it. "" off an epic.
	Coordination string
}

// promptVariable matches {{name}} in a saved prompt. The names are
// @dude/domain's PROMPT_VARIABLES; anything else in braces is left.
var promptVariable = regexp.MustCompile(`\{\{\s*([\w.]+)\s*\}\}`)

// fill puts the task and the Run into a saved prompt where it names them.
// The task still follows in full, as every phase frames it: a prompt that
// names one part of it must not cost the agent the rest.
func (in PromptInput) fill(prompt string) string {
	values := map[string]string{
		"task.title":    in.Title,
		"task.goal":     in.Goal,
		"task.criteria": CriteriaList(in.AcceptanceCriteria),
		"run.branch":    in.Branch,
		"run.base_ref":  in.BaseRef,
	}
	return promptVariable.ReplaceAllStringFunc(prompt, func(m string) string {
		if v, ok := values[promptVariable.FindStringSubmatch(m)[1]]; ok {
			return v
		}
		return m
	})
}

// Decision is a question an agent asked about this work, and a person's
// answer.
type Decision struct{ Question, Answer string }

// PromptRepo is a repository as the agent is told about it.
type PromptRepo struct {
	Name, Path string
	ReadOnly   bool
}

// workspaceNote says where the code is, when that is not simply "here".
// A reviewer and a tester check work and change none of it, so neither is
// told to commit.
func workspaceNote(repos []PromptRepo, phase string) string {
	if len(repos) == 1 && !repos[0].ReadOnly {
		return ""
	}
	return render("contracts/workspace", struct {
		Repos        []PromptRepo
		Review, Test bool
	}{repos, phase == PhaseReview, phase == PhaseTest})
}

// CriteriaList writes the criteria as a Markdown list, one item each. A
// criterion is Markdown and may run over several lines; its later lines are
// indented under its marker, or they would read as text after the list (or,
// starting with "- ", as criteria of their own).
func CriteriaList(criteria []string) string {
	items := make([]string, len(criteria))
	for i, c := range criteria {
		items[i] = "- " + strings.ReplaceAll(c, "\n", "\n  ")
	}
	return strings.Join(items, "\n")
}

func (in PromptInput) task() string {
	return render("contracts/task", struct {
		Title, Goal string
		Criteria    []string
		Decisions   []Decision
	}{in.Title, strings.TrimSpace(in.Goal), in.AcceptanceCriteria, in.Decisions})
}

// oneLine keeps a decision on its line: its own line breaks become spaces.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// What each reviewer flavour looks for. Narrow on purpose: a reviewer told to
// "look for problems" finds the same generic ones every time, and one told to
// look along a specific axis reads the diff differently — which is the reason
// for fanning out at all.
// Each is agents/contracts/review-focus/<category>.md.
var reviewFocus = agentTexts("contracts/review-focus/")

// reviewContract is how every reviewer works and what its reply carries,
// whatever its instructions say: findings a fixer can act on and a person
// can trust, and an account of what the review did and did not cover.
var reviewContract = reviewContractFor(nil, "", 0)

// What a review limited to a range (ReviewRange) checks, by its Of.
const (
	RangeSimplify       = "simplify"
	RangePullRequestFix = "pull_request_fix"
)

// rangeReason says why a review covers only its range, by ReviewRange.Of:
// agents/contracts/review-range/<of>.md.
var rangeReason = agentTexts("contracts/review-range/")

// reviewContractFor is the review contract with the range the reviewer
// establishes: the whole branch when base is empty, else the commits named,
// per repository, to HEAD. With more than one checkout each range names its
// repository.
func reviewContractFor(base map[string]string, of string, checkouts int) string {
	type reviewRange struct {
		Repo, Base string
		Named      bool
	}
	var ranges []reviewRange
	for _, repo := range slices.Sorted(maps.Keys(base)) {
		ranges = append(ranges, reviewRange{repo, base[repo], len(base) > 1 || checkouts > 1})
	}
	return render("contracts/review", struct {
		Ranges []reviewRange
		Reason string
	}{ranges, rangeReason[of]})
}

// reviewSummary closes a review's reply. Its fields sit indented under
// `review_summary:`, so ParseFindings reads the document as one untitled
// block and drops it, as it drops any document without a title.
var reviewSummary = render("contracts/review-summary", nil)

var findingFormat = render("contracts/finding-format", nil)

// earlierFindings asks a re-review to judge what the last round raised.
// Whether a finding was fixed is the reviewer's call, made by reading the
// code — not inferred from which files a fix touched, which retires a
// finding at line 40 because line 200 of the same file changed.
func earlierFindings(findings []Finding) string {
	return render("contracts/earlier-findings", findings)
}

// severityNote tells a reviewer what its severities cause, from the
// policy: a reviewer that does not know `medium` lets a pull request open
// rates an unmet acceptance criterion `medium`.
func severityNote(blocking []string) string {
	if len(blocking) == 0 {
		return ""
	}
	return render("contracts/severity", blocking)
}

// fixContract is how every fix is made, whatever the fixer's instructions
// say: a defect shown before it is changed, and shown gone after.
var fixContract = render("contracts/fix", nil)

// implementContract asks the implementer to check its own work: the
// independent tester after it verifies, but does not do this for it.
var implementContract = render("contracts/implement", nil)

// Every phase that changes code commits it; lux pushes what was committed.
// Uncommitted work is not part of the result, and saying so is cheaper than
// discovering an empty push. The image's commit hook adds the task's people
// as Co-authored-by trailers; an agent editing them would undo it.
var commitNote = render("contracts/commit", nil)

// Every move of the task branch is a fast-forward, so a writer brings the
// base in by merging it: a rebase would never publish, and would orphan
// review threads tied to the old commits. dude's, like commitNote: an
// organisation's prompt does not drop it.
var baseIntegrationNote = render("contracts/base-integration", nil)

// conductorBaseNote is baseIntegrationNote for the conductor, which runs
// no tests and publishes with its tool.
var conductorBaseNote = render("contracts/conductor-base", nil)

// testerContract is what a person merging the change relies on from the
// tester, whatever its instructions say: every criterion accounted for in
// one report, evidence behind each pass, and a product it did not touch.
var testerContract = render("contracts/tester", nil)

// testerTools is how the tester drives a browser and records it. Playwright
// and Chromium are in the runtime image (images/runtime); the evidence is
// published as files people open next to the task.
var testerTools = render("contracts/tester-tools", nil)

// publishNote tells an agent how to hand a person something that is not
// code. Every phase may: an implementer's design notes, a reviewer's
// reproduction, a tester's screenshots.
var publishNote = render("contracts/publish", nil)

// ask is how this agent stops for a person: dude's ask_person tool.
// Without dude's tools it cannot ask, and decides for itself.
func (in PromptInput) ask() []string {
	if in.Tools {
		return []string{askToolNote}
	}
	return nil
}

var askToolNote = render("contracts/ask", nil)

// toolsNote tells an agent about dude's tools.
var toolsNote = render("contracts/tools", nil)

// cliNote tells an agent about the dude CLI.
var cliNote = render("contracts/cli", nil)

// fixerGitHubNote tells a fixer of pull request feedback how it answers
// on GitHub: through dude, on the comments it was given only.
var fixerGitHubNote = render("contracts/fixer-github", nil)

// PromptRoleForPhase is whose prompt and settings each phase runs with: an
// agent role, or the fixer's — the implementer's model, told something
// else, whose settings fall back to the implementer's.
var PromptRoleForPhase = map[string]string{
	PhaseInvestigate: "investigator",
	PhaseImplement:   "implementer",
	PhaseReview:      "reviewer",
	PhaseFix:         "fixer",
	PhaseSimplify:    "simplifier",
	PhaseTest:        "qa_browser",
}

// PromptRoles are the prompts a person can edit, in the order they are
// shown.
var PromptRoles = []string{"conductor", "brainstorm", "implementer", "reviewer", "fixer", "simplifier", "qa_browser", "investigator"}

// builtinInstructions is what each role is told to do, before the work it
// is given: the part of a phase's prompt a person may rewrite (an
// organization's prompt replaces it, a project's adds to or replaces that).
// The rest — the task, findings, formats, tools, committing and the
// tester's recording — is dude's, and every phase gets it whatever its
// instructions say, because the workflow depends on it.
//
// Each is agents/roles/<role>.md, one paragraph per blank-line-separated
// block.
var builtinInstructions = mustLoadRoles(loadRoles(agentFiles))

// BuiltinPrompt is a role's instructions as dude ships them: what an
// organization that never edits its prompt runs, and where the first
// edit starts from.
func BuiltinPrompt(role string) string {
	return strings.Join(builtinInstructions[role], "\n\n")
}

// PromptRoleFor is whose prompt and settings a Run runs with: its phase's,
// or the conductor's for a task's conductor, which is no phase.
func PromptRoleFor(phase, role string) string {
	if phase == "" && (role == RoleConductor || role == RoleBrainstorm) {
		return role
	}
	return PromptRoleForPhase[phase]
}

// PhaseForPromptRole is the phase whose Runs take a settings role; "" for
// the conductor and brainstorm, which are Runs with no phase.
func PhaseForPromptRole(role string) string {
	for phase, r := range PromptRoleForPhase {
		if r == role {
			return phase
		}
	}
	return ""
}

// instructions is what the role is told to do: the organization's
// prompt if it has one, else dude's; then the project's, added after it or
// in its place. dude's own fixer instructions come in two parts, either
// side of the feedback it is given (tail); a person's come in one piece,
// before it.
func (in PromptInput) instructions(role string) (lead, tail []string) {
	lead = builtinInstructions[role]
	custom := in.OrgPrompt != nil
	if custom {
		lead = []string{*in.OrgPrompt}
	}
	project := strings.TrimSpace(in.ProjectPrompt)
	switch {
	case in.ProjectPromptMode == "replace":
		lead, custom = []string{project}, true
	case in.ProjectPromptMode == "add" && project != "":
		lead, custom = append(slices.Clone(lead), project), true
	}
	lead = slices.DeleteFunc(slices.Clone(lead), func(s string) bool { return strings.TrimSpace(s) == "" })
	if !custom && role == "fixer" && len(lead) > 1 {
		return lead[:1], lead[1:]
	}
	return lead, nil
}

// conductorToolsNote is the conductor's own tools: the read ones, its
// decisions, and create_task for a change it is asked for.
var conductorToolsNote = render("contracts/conductor-tools", nil)

// ConductorPrompt is a conductor's first prompt: dude's briefing and the
// person's message (written once, when it was created), then how it works.
//
// The briefing quotes the task's goal and criteria as written: each image
// reference in it reads as the image the Run is given (in.Images), as in a
// phase's prompt, and a saved prompt's {{task.goal}} does too.
func ConductorPrompt(briefing string, in PromptInput) string {
	in = in.withImages()
	sections := []string{ReplaceImageRefs(briefing, imageText(in.Images)), render("contracts/how-you-work", nil)}
	lead, _ := in.instructions(RoleConductor)
	for _, s := range lead {
		sections = append(sections, in.fill(s))
	}
	sections = append(sections, render("contracts/conductor-checkout", struct {
		Repos      []PromptRepo
		TaskBranch string
	}{in.Repositories, in.TaskBranch}))
	if len(in.Repositories) > 0 {
		sections = append(sections, conductorBaseNote)
	}
	if in.Tools {
		sections = append(sections, conductorToolsNote)
	}
	if c := strings.TrimSpace(in.Context); c != "" {
		sections = append(sections, render("contracts/project-notes", c))
	}
	return strings.Join(sections, "\n\n")
}

// Prompt composes one phase's prompt.
func Prompt(phase string, in PromptInput) string {
	var sections []string
	add := func(s ...string) { sections = append(sections, s...) }

	// Before anything reads the goal or criteria: a saved prompt's
	// {{task.goal}} reads them as the task section does.
	in = in.withImages()
	lead, tail := in.instructions(PromptRoleForPhase[phase])
	for i, s := range lead {
		lead[i] = in.fill(s)
	}

	switch phase {
	case PhaseInvestigate:
		add(lead...)
		add(in.task())

	case PhaseImplement:
		add(lead...)
		if in.Continues != nil {
			add(continueNote(in.Continues))
		}
		add(implementContract, commitNote, baseIntegrationNote)
		add(in.task())
		if len(in.Feedback) > 0 {
			add(FeedbackText(in.Feedback, in.CLI))
		}
		add(in.ask()...)

	case PhaseReview:
		category := in.Category
		if reviewFocus[category] == "" {
			category = "correctness"
		}
		add(render("contracts/review-lead", category), reviewFocus[category])
		add(lead...)
		contract := reviewContract
		if len(in.ReviewBase) > 0 {
			contract = reviewContractFor(in.ReviewBase, in.ReviewOf, len(in.Repositories))
		}
		add(render("contracts/task-under-review", in.task()), contract, findingFormat, reviewSummary)
		for _, f := range in.PRFeedback {
			if f.Source == "integration" {
				add(f.Body)
			}
		}
		if note := severityNote(in.BlockingSeverities); note != "" {
			add(note)
		}
		if len(in.Findings) > 0 {
			add(earlierFindings(in.Findings))
		}

	case PhaseFix:
		add(lead...)
		if len(in.Findings) > 0 {
			add(render("contracts/review-findings", in.Findings))
		}
		if len(in.PRFeedback) > 0 {
			type item struct {
				forge.ActionableFeedback
				Tools bool
			}
			var items, bots []item
			for _, f := range in.PRFeedback {
				if f.Source == forge.SourceBot {
					bots = append(bots, item{f, in.Tools})
					continue
				}
				items = append(items, item{f, in.Tools})
			}
			if len(items) > 0 {
				add(render("contracts/pr-feedback", items))
			}
			if len(bots) > 0 {
				add(render("contracts/pr-feedback-bots", bots))
			}
			if in.Tools {
				add(fixerGitHubNote)
			}
		}
		if len(in.Feedback) > 0 {
			add(FeedbackText(in.Feedback, in.CLI))
		}
		add(tail...)
		add(fixContract)
		add(commitNote, baseIntegrationNote)
		add(render("contracts/task-for-context", in.task()))
		add(in.ask()...)

	case PhaseSimplify:
		add(lead...)
		add(commitNote, baseIntegrationNote)
		add(render("contracts/task-implemented", in.task()))

	case PhaseTest:
		add(lead...)
		add(testerContract, testerTools)
		add(in.task(), findingFormat)
		if note := severityNote(in.BlockingSeverities); note != "" {
			add(note)
		}
		if len(in.Findings) > 0 {
			add(earlierFindings(in.Findings))
		}

	default:
		add(in.task())
	}

	if n := strings.TrimSpace(in.ConductorNote); n != "" {
		add(render("contracts/from-conductor", n))
	}
	if n := strings.TrimSpace(in.RestartNote); n != "" {
		add(render("contracts/restarted", n))
	}
	if c := strings.TrimSpace(in.Coordination); c != "" {
		add(c)
	}
	if note := workspaceNote(in.Repositories, phase); note != "" {
		add(note)
	}
	if note := baseNote(in.Bases); note != "" {
		add(note)
	}
	if phase == PhaseReview && in.Adopted != nil {
		add(in.Adopted.note())
	}
	if in.Tools {
		add(toolsNote)
	}
	if in.CLI {
		add(cliNote)
	}
	add(publishNote)
	if c := strings.TrimSpace(in.Context); c != "" {
		add(render("contracts/project-notes", c))
	}
	if in.Machine != "" {
		add(in.Machine)
	}
	return strings.Join(sections, "\n\n")
}
