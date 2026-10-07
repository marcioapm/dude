package delivery

import (
	"fmt"
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
	// Review phase: the severities that block, from the delivery's policy.
	BlockingSeverities []string
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
	// What the task's conductor asked of this Run, when it started it.
	ConductorNote string
	// Why this Run replaces one restarted in its step (RestartRunTx).
	RestartNote string
	// The images the Run is given with its prompt, in the order lux gets
	// them (TaskImages): each reference in the goal and criteria is
	// written as its place in this list.
	Images []PromptImage
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
func workspaceNote(repos []PromptRepo, review bool) string {
	if len(repos) == 0 {
		return "No repository is checked out for this work: it changes no code. Its result is what you " +
			"publish (see below) and what you reply."
	}
	if len(repos) == 1 && !repos[0].ReadOnly {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Repositories\n\nThis work spans these checkouts:\n")
	for _, r := range repos {
		access := "you may change it"
		if r.ReadOnly {
			access = "read only: for reference, do not change it — changes there are never kept"
		}
		fmt.Fprintf(&b, "\n- `%s` at `%s` — %s", r.Name, r.Path, access)
	}
	if review {
		b.WriteString("\n\nReview the changes in each checkout: `git log` and `git diff` against its default branch. " +
			"Name the repository in each finding's `repo` field.")
	} else {
		b.WriteString("\n\nCommit in each repository you change. Each changed repository gets its own pull request.")
	}
	return b.String()
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
	parts := []string{in.Title}
	if g := strings.TrimSpace(in.Goal); g != "" {
		parts = append(parts, g)
	}
	if len(in.AcceptanceCriteria) > 0 {
		var b strings.Builder
		b.WriteString("Acceptance criteria:\n")
		b.WriteString(CriteriaList(in.AcceptanceCriteria))
		parts = append(parts, b.String())
	}
	if len(in.Decisions) > 0 {
		var b strings.Builder
		b.WriteString("Decided by people during this work (part of the task; do not ask again):")
		for _, d := range in.Decisions {
			fmt.Fprintf(&b, "\n- Q: %s\n  A: %s", oneLine(d.Question), oneLine(d.Answer))
		}
		parts = append(parts, b.String())
	}
	return strings.Join(parts, "\n\n")
}

// oneLine keeps a decision on its line: its own line breaks become spaces.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// What each reviewer flavour looks for. Narrow on purpose: a reviewer told to
// "look for problems" finds the same generic ones every time, and one told to
// look along a specific axis reads the diff differently — which is the reason
// for fanning out at all.
var reviewFocus = map[string]string{
	"correctness": "Does this do what it claims? Look for logic errors, unhandled cases, " +
		"race conditions, and assumptions the code makes but does not check.",
	"security": "Look for credentials that reach somewhere they should not, missing " +
		"authorization checks, injection, and data crossing a tenant boundary.",
	"performance": "Look for work repeated per item that could be done once, queries in " +
		"loops, and anything that grows worse than linearly with real data.",
	"frontend": "Look for accessibility gaps, layout that breaks at other sizes, state " +
		"that can render an impossible combination, and re-render storms.",
	"database": "Look for migrations that lock or cannot be rolled back, missing " +
		"indexes on new query paths, and constraints the code assumes but the " +
		"schema does not enforce.",
	"api": "Look for breaking changes to existing callers, inconsistent error " +
		"shapes, and endpoints that leak more than the caller should see.",
}

const findingFormat = "Report each finding as one YAML document, separated by `---`:\n\n" +
	"```yaml\n" +
	"severity: blocking | high | medium | low | note\n" +
	"category: <your review category>\n" +
	"repo: <the repository, when there are several>\n" +
	"file: path/to/file.ts\n" +
	"line: 123\n" +
	"title: One line naming the problem\n" +
	"description: What is wrong and why it matters.\n" +
	"suggested_fix: What to do instead.\n" +
	"```\n\n" +
	"Report nothing if you find nothing. A finding you are not confident in is a " +
	"`note`, not a `blocking` — a reviewer that cries wolf costs the next fix " +
	"attempt for nothing."

// earlierFindings asks a re-review to judge what the last round raised.
// Whether a finding was fixed is the reviewer's call, made by reading the
// code — not inferred from which files a fix touched, which retires a
// finding at line 40 because line 200 of the same file changed.
func earlierFindings(findings []Finding) string {
	var b strings.Builder
	b.WriteString("## Findings from the last review\n\nA fix has been made since. For each finding below, " +
		"check the code as it is now and say whether it is fixed — every one, whatever its category or the " +
		"reviewer that raised it: you are the one judging it this round. Report a problem that is still there " +
		"in your verdicts, not as a new finding; report anything new as a finding as usual.")
	for i, f := range findings {
		b.WriteString(fmt.Sprintf("\n\n**F%d** [%s] %s", i+1, f.Severity, f.Title))
		if f.File != "" {
			loc := f.File
			if f.Line > 0 {
				loc = fmt.Sprintf("%s:%d", f.File, f.Line)
			}
			b.WriteString(" — `" + loc + "`")
		}
		if f.Description != "" {
			b.WriteString("\n" + f.Description)
		}
	}
	b.WriteString("\n\nAnswer for every one, as one more YAML document:\n\n```yaml\nverdicts:\n")
	for i := range findings {
		b.WriteString(fmt.Sprintf("  F%d: fixed | still\n", i+1))
	}
	b.WriteString("```")
	return b.String()
}

// severityNote tells a reviewer what its severities cause, from the
// policy: a reviewer that does not know `medium` lets a pull request open
// rates an unmet acceptance criterion `medium`.
func severityNote(blocking []string) string {
	if len(blocking) == 0 {
		return ""
	}
	names := make([]string, len(blocking))
	for i, b := range blocking {
		names[i] = "`" + b + "`"
	}
	return fmt.Sprintf("Severity decides what happens next: %s send the change back to be fixed "+
		"before a pull request opens; anything else goes into the pull request for a person to "+
		"weigh. So a finding that means an acceptance criterion is not met must be one of %s, "+
		"however small the fix.", strings.Join(names, ", "), strings.Join(names, ", "))
}

// Every phase that changes code commits it; lux pushes what was committed.
// Uncommitted work is not part of the result, and saying so is cheaper than
// discovering an empty push.
const commitNote = "Commit your work when you are done. Only committed changes are kept."

// testerTools is how the tester drives a browser and records it. Playwright
// and Chromium are in the runtime image (images/runtime); the evidence is
// published as files people open next to the task.
const testerTools = "Drive a real browser with Playwright (Python: `python3 -m playwright`, the `playwright` " +
	"package; Chromium is installed). Record the whole flow as a video — create the browser context with " +
	"`record_video_dir` — and take a screenshot at each step that matters. When you are done, close the " +
	"context so the video is written, then publish the video and the screenshots: copy them into " +
	"`$LUX_ARTIFACTS` (for example `$LUX_ARTIFACTS/walkthrough.webm`, `$LUX_ARTIFACTS/01-signed-in.png`), " +
	"named so the order reads. Read the browser console as you go, and report what it says. A flow that does " +
	"not work is a finding, with the step it failed at; one that works needs no finding — say what you did."

// publishNote tells an agent how to hand a person something that is not
// code. Every phase may: an implementer's design notes, a reviewer's
// reproduction, a tester's screenshots.
const publishNote = "To give the people following this work a file — notes, a design, a report, a " +
	"screenshot — write it into the directory named by the LUX_ARTIFACTS environment variable " +
	"(for example `$LUX_ARTIFACTS/notes.md`). They see each one next to the task, Markdown " +
	"rendered. Publish what a person would want to read; don't copy code there."

// ask is how this agent stops for a person: dude's ask_person tool.
// Without dude's tools it cannot ask, and decides for itself.
func (in PromptInput) ask() []string {
	if in.Tools {
		return []string{askToolNote}
	}
	return nil
}

const askToolNote = "If you cannot go on without a decision only a person can make — the task is ambiguous " +
	"in a way that changes what you build, or two reasonable readings conflict — ask with the dude tool " +
	"ask_person, then end your turn. The answer comes back as your next message, even if the person takes " +
	"hours to answer: your work is kept meanwhile. Do not ask about anything you can decide or find out " +
	"yourself; most tasks need no question at all."

// toolsNote tells an agent about dude's tools.
const toolsNote = "The dude tools (list_tasks, list_epics, list_repositories, create_task, emit_event, " +
	"request_repository, search_memory, get_memory, remember, run_diff) " +
	"act on the work you are part of: record work you find outside your task (a person decides on it), report " +
	"progress people can follow, ask for another repository you need, see what a Run of your task changed " +
	"(run_diff: the files, then the lines of those you name). Memory is what people and agents here " +
	"learned before you: search it (search_memory) before investigating something that may already be known, " +
	"and when you learn something the next agent should know — a fact that holds, a procedure that works, a trap " +
	"and its way around — save it (remember), after searching so you do not save it twice. Do not save what the " +
	"code or the task already says."

// cliNote tells an agent about the dude CLI.
const cliNote = "The same, from the shell: the `dude` command (see `dude help`) — " +
	"`dude task list`, `dude epic list`, `dude task create` for work you find outside your task (a person " +
	"decides on it), `dude event progress --data '{\"done\":3,\"of\":10}'` for progress people can follow, " +
	"`dude repo list` and `dude repo request`, `dude memory search QUERY`, `dude memory show ID` and " +
	"`dude memory add --title T --content C` for memory, `dude diff [RUN] [PATH...]` for what a Run of your " +
	"task changed, and `dude publish FILE` to keep a file for people."

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
var PromptRoles = []string{"conductor", "implementer", "reviewer", "fixer", "simplifier", "qa_browser", "investigator"}

// builtinInstructions is what each role is told to do, before the work it
// is given: the part of a phase's prompt a person may rewrite (an
// organization's prompt replaces it, a project's adds to or replaces that).
// The rest — the task, findings, formats, tools, committing and the
// tester's recording — is dude's, and every phase gets it whatever its
// instructions say, because the workflow depends on it.
var builtinInstructions = map[string][]string{
	"conductor": {"You are this task's conductor. The people on the task talk to you in its Chat: answer " +
		"their questions about the task, its code and how it was delivered, with the evidence — the file " +
		"and line, the Run, the finding, the pull request comment.",
		"Who decides is in your briefing, and dude tells you when it changes. When you take the task's decisions, " +
			"dude's delivery still does the mechanics — it creates the Runs, waits for them, moves the branch, opens " +
			"the pull request — and wakes you with a short note at each decision: read what you need with your tools, " +
			"then decide. Plan with the person before anything is built. Whenever you and the person settle something " +
			"the task's text does not say — the scope, an approach, a criterion — write it into the task (update_task) " +
			"before you start the implementer, so its prompt has it. Then start phases (start_phase), triage findings " +
			"(fix some, dismiss others with a reason, or ask), and before the pull request always ask the person " +
			"(decide ask_person), saying what ran and what was not verified; open it (decide open_pull_request) only " +
			"when they answered Open or Draft. Past the policy's bounds, or for what only a person may decide, ask.",
		"When the delivery escalates to a person (a stuck review, a failed Run), only a person decides: you explain and " +
			"propose. Ask with ask_person, offering the escalation's actions as choices, with actions naming what each " +
			"stands for; the owner picking one decides it, and they may use the banner on the task instead. If the owner " +
			"answers in their own words (\"your call\"), decide it yourself with decide_escalation, once. While the " +
			"delivery waits at the escalation, start_phase, dismiss_finding and decide are refused: do not try them.",
		"Steer a running Run (steer) that is going the wrong way, or to give it something the person just said: it " +
			"reads your words at its next step. Start another phase only once the Run has ended. Interrupt only when " +
			"its current work is wasted. Steer keeps the agent's context; restart_run starts over: a fresh Run in the " +
			"same step, from the task's head, its uncommitted work lost.",
		"dude tells you when a Run of yours has made no progress for 30 minutes: a tool call open all that time, " +
			"its agent silent all that time (no output, no tool call), or an implementer, fixer or simplifier whose " +
			"files did not change. The report is facts, not a verdict: its " +
			"open calls, its processes, its CPU and network, and what it did. A long test suite with a live process " +
			"and CPU is usually fine to leave. An open call with no process and no CPU or network is usually stuck: " +
			"restart it. A silent agent with no CPU or network is usually hung waiting on its model: restart it. " +
			"An agent with many reads and no edits for 30 minutes is usually looping: steer it first. " +
			"Leaving it is fine: you are told again only if nothing changes, after 60 minutes, and every Run is " +
			"stopped at its time limit.",
		"Someone who writes @dude on one of the task's pull requests reaches you as a Chat message from a GitHub " +
			"person, naming the pull request and the comment. It is a question or an instruction, not a hand-over: it " +
			"changes nothing about who decides. Answer it on the pull request (reply_on_pull_request, in reply to that " +
			"comment), where they asked.",
		"When Deliver takes the decisions, or the task is merged or closed, you are read-only: read the code in " +
			"your checkout and dude's records, but change nothing, start nothing and decide nothing. When someone asks " +
			"for a change then, offer to create a follow-up task for it (create_task) — create it only once they agree.",
		"You may edit code yourself, for small, well-understood things: a rename, a one-line fix, a review nit. " +
			"Anything larger, or anything that needs the tests run, you delegate (start_phase implement or fix). Edit " +
			"only while you take the task's decisions and no implementer or fixer is at work. Stay current first: dude " +
			"brings the task branch into your checkout as `lux/<branch>` and tells you when it could not fast-forward " +
			"you; then `git merge --ff-only lux/<branch>`, or `git merge lux/<branch>` when your work was kept. Commit " +
			"in your checkout, then publish (`dude publish --message M`): dude takes your commits to the task branch, " +
			"or refuses past the project's limit of changed lines and files, saying to delegate. Finish or abort a " +
			"rebase or merge before you publish: lux will not push a checkout in the middle of one. Your machine is small " +
			"and does not run the code: never build, install or run tests. Your commits are reviewed like any other: " +
			"run a review after you publish; the pull request gate refuses an unreviewed commit of yours. Whether " +
			"tests passed is what the Runs that ran them reported; do not claim what you did not see a Run do."},
	"investigator": {"Investigate this task before any code is written. Read the relevant code, identify " +
		"what will have to change, and report what you found. Do not change anything."},
	"implementer": {"Implement this task. Run the project's formatter, type checks and tests before you " +
		"finish — handing over code that does not build is not finishing."},
	"reviewer": {"You may run the code, run the tests, and write throwaway scripts to check a hypothesis. " +
		"Do not commit: your output is findings, and someone else will make the change."},
	// The fixer's second paragraph follows the feedback it is given.
	"fixer": {"Address the feedback below.",
		"Fix only what is raised above. Widening the change makes the re-review harder and risks new findings."},
	"simplifier": {"Simplify the changes on this branch without changing what they do.",
		"Remove needless complexity, improve names and structure, delete dead code the change " +
			"introduced, and consolidate obvious duplication.",
		"Do not widen the scope, do not add features, and do not change behaviour. Run the tests: " +
			"if they do not pass, your simplification was not behaviour-preserving."},
	"qa_browser": {"Exercise this change the way a person would. Start the application, drive it in a " +
		"browser, and confirm it does what the task asked.",
		"You are not looking for what the unit tests already cover. You are looking for what they " +
			"cannot: does the feature actually work when used.",
		"How to start the application and what data it needs are the project's to say (in the " +
			"project notes below); if they say nothing, find out from the repository — its README, " +
			"its scripts — and say in your report what you did."},
}

// BuiltinPrompt is a role's instructions as dude ships them: what an
// organization that never edits its prompt runs, and where the first
// edit starts from.
func BuiltinPrompt(role string) string {
	return strings.Join(builtinInstructions[role], "\n\n")
}

// PromptRoleFor is whose prompt and settings a Run runs with: its phase's,
// or the conductor's for a task's conductor, which is no phase.
func PromptRoleFor(phase, role string) string {
	if phase == "" && role == RoleConductor {
		return RoleConductor
	}
	return PromptRoleForPhase[phase]
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
const conductorToolsNote = "The dude tools read what dude knows about this task: run_diff (what a Run changed: " +
	"the files, then the lines of those you name), findings (the review findings and how each was settled; " +
	"name ids for their text), pull_requests (state, checks, reviews and feedback), list_tasks, " +
	"list_repositories, search_memory and get_memory. ask_person asks the person a question and waits for " +
	"the answer; end your turn after it. create_task records a change as a new task, for a person to " +
	"deliver. While you take the decisions: update_task writes an agreed goal or acceptance criteria into the " +
	"task, before the implementer; start_phase starts implement, review (some or all categories), fix (some or " +
	"all findings), simplify or test; decide takes the decision waited on (next, ask_person, wait, " +
	"open_pull_request); dismiss_finding leaves a finding as it is, with the reason. Each is refused, saying " +
	"why, when it is not yours to take. decide_escalation decides an escalation the owner handed you with a free " +
	"answer to your question about it. steer tells a running phase Run of this task something, whoever decides; " +
	"restart_run replaces one the delivery waits on with a fresh Run in its step (a note, optionally another tier). " +
	"reply_on_pull_request answers on one of the task's pull requests, whoever decides. " +
	"publish takes what you committed in your checkout to the task branch, while you decide. " +
	"From the shell: `dude diff [RUN] [PATH...]`, `dude findings [ID...]`, " +
	"`dude prs`, `dude task list`, `dude memory search QUERY`, `dude task create`, `dude task update`, " +
	"`dude phase start PHASE`, `dude steer RUN TEXT`, `dude restart RUN NOTE`, `dude decide ACTION`, `dude finding dismiss ID --reason R`, " +
	"`dude escalation decide ACTION --note N`, `dude ask Q --choice C --action A`, " +
	"`dude pr reply PR TEXT --in-reply-to ID`, `dude publish --message M`."

// ConductorPrompt is a conductor's first prompt: dude's briefing and the
// person's message (written once, when it was created), then how it works.
//
// The briefing quotes the task's goal and criteria as written: each image
// reference in it reads as the image the Run is given (in.Images), as in a
// phase's prompt, and a saved prompt's {{task.goal}} does too.
func ConductorPrompt(briefing string, in PromptInput) string {
	in = in.withImages()
	sections := []string{ReplaceImageRefs(briefing, imageText(in.Images)), "## How you work"}
	lead, _ := in.instructions(RoleConductor)
	for _, s := range lead {
		sections = append(sections, in.fill(s))
	}
	if len(in.Repositories) == 0 {
		sections = append(sections, "No repository is checked out for this task: it changes no code.")
	} else {
		var b strings.Builder
		b.WriteString("Your checkout is the task's head:")
		for _, r := range in.Repositories {
			access := "you may edit it, small things only"
			if r.ReadOnly {
				access = "read only"
			}
			fmt.Fprintf(&b, "\n- `%s` at `%s` (%s)", r.Name, r.Path, access)
		}
		if in.TaskBranch != "" {
			fmt.Fprintf(&b, "\n\nThe task branch is `%s`; dude brings it in as `lux/%s`.", in.TaskBranch, in.TaskBranch)
		}
		sections = append(sections, b.String())
	}
	if in.Tools {
		sections = append(sections, conductorToolsNote)
	}
	if c := strings.TrimSpace(in.Context); c != "" {
		sections = append(sections, "## Project notes\n\n"+c)
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
		add(commitNote)
		add(in.task())
		add(in.ask()...)

	case PhaseReview:
		category := in.Category
		if reviewFocus[category] == "" {
			category = "correctness"
		}
		add(fmt.Sprintf("Review the changes on this branch for **%s**.", category), reviewFocus[category])
		add(lead...)
		add("The task under review:\n\n"+in.task(), findingFormat)
		if note := severityNote(in.BlockingSeverities); note != "" {
			add(note)
		}
		if len(in.Findings) > 0 {
			add(earlierFindings(in.Findings))
		}

	case PhaseFix:
		add(lead...)
		if len(in.Findings) > 0 {
			var b strings.Builder
			b.WriteString("## Review findings")
			for _, f := range in.Findings {
				b.WriteString(fmt.Sprintf("\n\n### [%s] %s", f.Severity, f.Title))
				if f.File != "" {
					loc := f.File
					if f.Line > 0 {
						loc = fmt.Sprintf("%s:%d", f.File, f.Line)
					}
					b.WriteString("\n\n`" + loc + "`")
				}
				if f.Description != "" {
					b.WriteString("\n\n" + f.Description)
				}
				if f.SuggestedFix != "" {
					b.WriteString("\n\nSuggested: " + f.SuggestedFix)
				}
			}
			add(b.String())
		}
		if len(in.PRFeedback) > 0 {
			var items []string
			for _, f := range in.PRFeedback {
				var parts []string
				if f.Repo != "" {
					parts = append(parts, "in **"+f.Repo+"**")
				}
				if f.Path != "" {
					parts = append(parts, "`"+f.Path+"`")
				}
				if f.Author != "" {
					parts = append(parts, "**"+f.Author+"**")
				}
				item := strings.Join(append(parts, f.Body), " — ")
				for _, c := range f.Checks {
					item += "\n\n**Failing check: " + c.Name + "**"
					if c.URL != "" {
						item += " (" + c.URL + ")"
					}
					if c.Log != "" {
						item += "\n\n```\n" + strings.ReplaceAll(c.Log, "```", "`\u200b``") + "\n```"
					}
				}
				items = append(items, item)
			}
			add("## Pull request feedback\n\n" + strings.Join(items, "\n\n"))
		}
		add(tail...)
		add(commitNote)
		add("The original task, for context:\n\n" + in.task())
		add(in.ask()...)

	case PhaseSimplify:
		add(lead...)
		add(commitNote)
		add("The task this branch implements:\n\n" + in.task())

	case PhaseTest:
		add(lead...)
		add(testerTools)
		add(in.task(), findingFormat)

	default:
		add(in.task())
	}

	if n := strings.TrimSpace(in.ConductorNote); n != "" {
		add("## From the task's conductor\n\nThe conductor, who plans this task with its people, started this Run and asks:\n\n" + n)
	}
	if n := strings.TrimSpace(in.RestartNote); n != "" {
		add("## Restarted\n\n" + n)
	}
	if note := workspaceNote(in.Repositories, phase == PhaseReview); note != "" {
		add(note)
	}
	if in.Tools {
		add(toolsNote)
	}
	if in.CLI {
		add(cliNote)
	}
	add(publishNote)
	if c := strings.TrimSpace(in.Context); c != "" {
		add("## Project notes\n\n" + c)
	}
	return strings.Join(sections, "\n\n")
}
