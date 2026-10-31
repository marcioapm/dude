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
	Child              bool
	SessionChild       bool
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
	// The delivery's fixes reach its open pull requests directly
	// (Policy.DirectFixes): its instruction file says so.
	DirectFixes bool
	// The review loop is the conductor's (State.ConductedLoop, at submit):
	// a reviewer's instruction file asks for each finding's evidence.
	ConductedLoop bool
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
	// A person's words on the decision this Run was started for, marked
	// as theirs (runs.person_note): first, after a replay.
	PersonNote string
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
	// A review or test that goes on from its previous round with a fresh
	// agent: that round's conversation (Replay), first in its message.
	Replay string
	// Review phase following a fix: what that fix published (LastFixTx).
	LastFix []FixRange
	// Test phase: the checks the attempt's writers reported
	// (WriterChecksTx), "" for none.
	WriterChecks string
	// Test phase: an earlier round of the attempt tested, and this one
	// resumed none of it (RetestsAfreshTx): every criterion again.
	RetestsAfresh bool
	// Review phase: what earlier reviewers of its category asked
	// (EarlierQuestionsTx).
	Questions []EarlierQuestion
	// Review phase of a conducted loop (ConductedLoop): its round
	// (runs.review_round), 0 for none, and the attempt's findings decided
	// in earlier rounds (DecidedFindingsTx).
	Round   int
	Decided []DecidedFinding
}

// promptVariable matches {{name}} in a saved prompt. The names are
// @dude/domain's PROMPT_VARIABLES; anything else in braces is left.
var promptVariable = regexp.MustCompile(`\{\{\s*([\w.]+)\s*\}\}`)

// fill puts the task and the Run into a saved prompt where it names them.
// The task still follows in full in the first message: a prompt that
// names one part of it must not cost the agent the rest.
func (v PromptVars) fill(prompt string) string {
	values := map[string]string{
		"task.title":    v.Title,
		"task.goal":     v.Goal,
		"task.criteria": CriteriaList(v.Criteria),
		"run.branch":    v.Branch,
		"run.base_ref":  v.BaseRef,
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

// workspaceNote says where each checkout is: the agent starts in
// /workspace, beside its instruction file, not in a repository. A reviewer
// and a tester check work; an investigator inspects and reports. None is
// told to commit.
func workspaceNote(repos []PromptRepo, phase string) string {
	return render(workspaceTmpl, struct {
		Repos                     []PromptRepo
		Review, Test, Investigate bool
	}{repos, phase == PhaseReview, phase == PhaseTest, phase == PhaseInvestigate})
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
	return render(taskTmpl, struct {
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
// Each is agents/aspects/reviewer/<category>.md.
var reviewFocus = agentTexts("aspects/reviewer/", reviewerOrder...)

// What a review limited to a range (ReviewRange) checks, by its Of.
const (
	RangeSimplify       = "simplify"
	RangePullRequestFix = "pull_request_fix"
	// A conducted loop's later round: what changed since the round before
	// it reviewed (State.roundRange).
	RangeFixRound = "fix_round"
)

// rangeReason says why a review covers only its range, by ReviewRange.Of:
// agents/contracts/review-range/<of>.md.
var rangeReason = agentTexts("contracts/review-range/", RangeSimplify, RangePullRequestFix, RangeSinceLastRound, RangeFixRound)

// reviewScope is what this review covers, in its first message: the whole
// branch when base is empty, else the commits named, per repository, to
// HEAD. With more than one checkout each range names its repository.
func reviewScope(base map[string]string, of string, checkouts int) string {
	type reviewRange struct {
		Repo, Base string
		Named      bool
	}
	var ranges []reviewRange
	for _, repo := range slices.Sorted(maps.Keys(base)) {
		ranges = append(ranges, reviewRange{repo, base[repo], len(base) > 1 || checkouts > 1})
	}
	return render(reviewScopeTmpl, struct {
		Ranges []reviewRange
		Reason string
	}{ranges, rangeReason[of]})
}

// reviewContract is how every reviewer works, whatever its saved prompts
// say: the reviewer's "How you review".
var reviewContract = render(mustTemplate("contracts/review"), InstructionsInput{}.data("reviewer"))

// reviewSummary closes a review's reply. Its fields sit indented under
// `review_summary:`, so ParseFindings reads the document as one untitled
// block and drops it, as it drops any document without a title.
var reviewSummary = render(mustTemplate("contracts/review-summary"), nil)

// findingFormat is the reviewer's: only a reviewer is shown the last fix,
// so only it is asked for caused_by_fix (the template keys on .Role).
var findingFormat = render(mustTemplate("contracts/finding-format"), InstructionsInput{}.data("reviewer"))

// conductedFindingFormat is findingFormat in a conducted loop: each
// finding's evidence asked for (ConductedLoop).
var conductedFindingFormat = render(mustTemplate("contracts/finding-format"), InstructionsInput{ConductedLoop: true}.data("reviewer"))

// earlierFindings asks a re-review to judge what the last round raised.
// Whether a finding was fixed is the reviewer's call, made by reading the
// code — not inferred from which files a fix touched, which retires a
// finding at line 40 because line 200 of the same file changed.
func earlierFindings(findings []Finding) string {
	return render(earlierFindingsTmpl, findings)
}

// severityNote tells a reviewer what its severities cause, from the
// policy: a reviewer that does not know `medium` lets a pull request open
// rates an unmet acceptance criterion `medium`.
func severityNote(role string, blocking []string) string {
	if len(blocking) == 0 {
		return ""
	}
	return render(severityTmpl, InstructionsInput{BlockingSeverities: blocking}.data(role))
}

// fixContract is how every fix is made, whatever the fixer's instructions
// say: a defect shown before it is changed, and shown gone after. As the
// fixer's default capabilities read it.
var fixContract = render(mustTemplate("contracts/fix"), InstructionsInput{}.data("fixer"))

// implementContract asks the implementer to check its own work: the
// independent tester after it verifies, but does not do this for it.
var implementContract = render(mustTemplate("contracts/implement"), nil)

// Every phase that changes code commits it; lux pushes what was committed.
// Uncommitted work is not part of the result, and saying so is cheaper than
// discovering an empty push. The image's commit hook adds the task's people
// as Co-authored-by trailers; an agent editing them would undo it.
var commitNote = render(mustTemplate("contracts/commit"), nil)

// Every move of the task branch is a fast-forward, so a writer brings the
// base in by merging it: a rebase would never publish, and would orphan
// review threads tied to the old commits. dude's, like commitNote: an
// organisation's prompt does not drop it.
var baseIntegrationNote = render(mustTemplate("contracts/base-integration"), nil)

// conductorBaseNote is baseIntegrationNote for the conductor, which runs
// no tests and publishes with its tool: as its default capabilities read it.
var conductorBaseNote = render(mustTemplate("contracts/conductor-base"), InstructionsInput{}.data(RoleConductor))

// testerContract is what a person merging the change relies on from the
// tester, whatever its instructions say: every criterion accounted for in
// one report, evidence behind each pass, and a product it did not touch.
var testerContract = render(mustTemplate("contracts/tester"), nil)

// testerTools is how the tester drives a browser and records it. Playwright
// and Chromium are in the runtime image (images/runtime); the evidence is
// published as files people open next to the task.
var testerTools = render(mustTemplate("contracts/tester-tools"), nil)

// fixerGitHubNote tells a fixer of pull request feedback how it answers
// on GitHub: through dude, on the comments it was given only.
var fixerGitHubNote = render(mustTemplate("contracts/fixer-github"), nil)

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

// ConductorPrompt is a conductor's first message: dude's briefing and the
// person's message (written once, when it was created), then its
// checkouts. How it works is its instruction file (Instructions).
//
// The briefing quotes the task's goal and criteria as written: each image
// reference in it reads as the image the Run is given (in.Images), as in a
// phase's prompt.
func ConductorPrompt(briefing string, in PromptInput) string {
	in = in.withImages()
	return ReplaceImageRefs(briefing, imageText(in.Images)) + "\n\n" + render(conductorCheckoutTmpl, struct {
		Repos      []PromptRepo
		TaskBranch string
	}{in.Repositories, in.TaskBranch})
}

// Prompt is a phase Run's first message: its task and what this Run is
// given, nothing standing. How it works, its role, its saved prompts and
// its project's notes are its instruction file (Instructions).
func Prompt(phase string, in PromptInput) string {
	var sections []string
	add := func(s ...string) { sections = append(sections, s...) }
	in = in.withImages()
	if in.Replay != "" {
		add(in.Replay)
	}
	if n := strings.TrimSpace(in.PersonNote); n != "" {
		add(n)
	}

	switch phase {
	case PhaseImplement:
		if in.Continues != nil {
			add(continueNote(in.Continues))
		}
		add(in.task())
		if len(in.Feedback) > 0 {
			add(FeedbackText(in.Feedback, in.CLI))
		}

	case PhaseReview:
		add(render(reviewLeadTmpl, reviewCategory(in.Category)))
		add(render(taskUnderReviewTmpl, in.task()), reviewScope(in.ReviewBase, in.ReviewOf, len(in.Repositories)))
		for _, f := range in.PRFeedback {
			if f.Source == "integration" {
				add(f.Body)
			}
		}
		if len(in.Findings) > 0 {
			add(earlierFindings(in.Findings))
		}
		if q := earlierQuestions(in.Questions); q != "" {
			add(q)
		}
		if note := lastFixNote(in.LastFix); note != "" {
			add(note)
		}
		if note := reviewRoundNote(in.ConductedLoop, in.Round, in.ReviewOf, in.Decided); note != "" {
			add(note)
		}

	case PhaseFix:
		add(fixSections(in)...)
		add(render(taskForContextTmpl, in.task()))

	case PhaseSimplify:
		add(render(taskImplementedTmpl, in.task()))

	case PhaseTest:
		add(in.task())
		if in.RetestsAfresh {
			add(render(retestAfreshTmpl, nil))
		}
		if len(in.Findings) > 0 {
			add(earlierFindings(in.Findings))
		}
		if in.WriterChecks != "" {
			add(in.WriterChecks)
		}

	default:
		add(in.task())
	}
	if in.Child && in.OrgPrompt != nil {
		if prompt := strings.TrimSpace(*in.OrgPrompt); prompt != "" {
			add(prompt)
		}
	}

	if n := strings.TrimSpace(in.ConductorNote); n != "" {
		add(render(fromConductorTmpl, n))
	}
	if n := strings.TrimSpace(in.RestartNote); n != "" {
		add(render(restartedTmpl, n))
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
	if in.Machine != "" {
		add(in.Machine)
	}
	return strings.Join(sections, "\n\n")
}

// fixSections are what a fixer is given to fix, in its prompt and in a
// resumed fixer's round message alike: review findings, pull request
// feedback, people's feedback.
func fixSections(in PromptInput) []string {
	var sections []string
	if len(in.Findings) > 0 {
		sections = append(sections, render(reviewFindingsTmpl, in.Findings))
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
			sections = append(sections, render(prFeedbackTmpl, items))
		}
		if len(bots) > 0 {
			sections = append(sections, render(prFeedbackBotsTmpl, bots))
		}
		if in.Tools {
			sections = append(sections, fixerGitHubNote)
		}
	}
	if len(in.Feedback) > 0 {
		sections = append(sections, FeedbackText(in.Feedback, in.CLI))
	}
	return sections
}
