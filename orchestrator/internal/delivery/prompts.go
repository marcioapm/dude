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
}

// promptVariable matches {{name}} in a saved prompt. The names are
// @dude/domain's PROMPT_VARIABLES; anything else in braces is left.
var promptVariable = regexp.MustCompile(`\{\{\s*([\w.]+)\s*\}\}`)

// fill puts the task and the Run into a saved prompt where it names them.
// placed says whether it placed the task itself (its goal or criteria), so
// the task need not follow it again.
func (in PromptInput) fill(prompt string) (filled string, placed bool) {
	criteria := make([]string, len(in.AcceptanceCriteria))
	for i, c := range in.AcceptanceCriteria {
		criteria[i] = "- " + c
	}
	values := map[string]string{
		"task.title":    in.Title,
		"task.goal":     in.Goal,
		"task.criteria": strings.Join(criteria, "\n"),
		"run.branch":    in.Branch,
		"run.base_ref":  in.BaseRef,
	}
	filled = promptVariable.ReplaceAllStringFunc(prompt, func(m string) string {
		name := promptVariable.FindStringSubmatch(m)[1]
		v, ok := values[name]
		if !ok {
			return m
		}
		if name == "task.goal" || name == "task.criteria" {
			placed = true
		}
		return v
	})
	return filled, placed
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

func (in PromptInput) task() string {
	parts := []string{in.Title}
	if g := strings.TrimSpace(in.Goal); g != "" {
		parts = append(parts, g)
	}
	if len(in.AcceptanceCriteria) > 0 {
		var b strings.Builder
		b.WriteString("Acceptance criteria:")
		for _, c := range in.AcceptanceCriteria {
			b.WriteString("\n- " + c)
		}
		parts = append(parts, b.String())
	}
	if d := in.decisions(); d != "" {
		parts = append(parts, d)
	}
	return strings.Join(parts, "\n\n")
}

// decisions is what people decided during this work, as a section, or "".
func (in PromptInput) decisions() string {
	if len(in.Decisions) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Decided by people during this work (part of the task; do not ask again):")
	for _, d := range in.Decisions {
		fmt.Fprintf(&b, "\n- Q: %s\n  A: %s", oneLine(d.Question), oneLine(d.Answer))
	}
	return b.String()
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
		"check the code as it is now and say whether it is fixed. Report a problem that is still there " +
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
	"request_repository) " +
	"act on the work you are part of: record work you find outside your task (a person decides on it), report " +
	"progress people can follow, ask for another repository you need."

// cliNote tells an agent about the dude CLI.
const cliNote = "The same, from the shell: the `dude` command (see `dude help`) — " +
	"`dude task list`, `dude epic list`, `dude task create` for work you find outside your task (a person " +
	"decides on it), `dude event progress --data '{\"done\":3,\"of\":10}'` for progress people can follow, " +
	"`dude repo list` and `dude repo request`, and `dude publish FILE` to keep a file for people."

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
var PromptRoles = []string{"implementer", "reviewer", "fixer", "simplifier", "qa_browser", "investigator"}

// builtinInstructions is what each role is told to do, before the work it
// is given: the part of a phase's prompt a person may rewrite (an
// organization's prompt replaces it, a project's adds to or replaces that).
// The rest — the task, findings, formats, tools, committing and the
// tester's recording — is dude's, and every phase gets it whatever its
// instructions say, because the workflow depends on it.
var builtinInstructions = map[string][]string{
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

// instructions is what the phase's role is told to do: the organization's
// prompt if it has one, else dude's; then the project's, added after it or
// in its place. dude's own fixer instructions come in two parts, either
// side of the feedback it is given (tail); a person's come in one piece,
// before it.
func (in PromptInput) instructions(phase string) (lead, tail []string) {
	lead = builtinInstructions[PromptRoleForPhase[phase]]
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
	if !custom && phase == PhaseFix && len(lead) > 1 {
		return lead[:1], lead[1:]
	}
	return lead, nil
}

// taskSection is the task as a prompt section, unless a saved prompt has
// already placed it with {{task.goal}} or {{task.criteria}}: then only what
// it could not have placed (people's decisions) follows.
func (in PromptInput) taskSection(placed bool, intro string) []string {
	if !placed {
		return []string{intro + in.task()}
	}
	if d := in.decisions(); d != "" {
		return []string{d}
	}
	return nil
}

// Prompt composes one phase's prompt.
func Prompt(phase string, in PromptInput) string {
	var sections []string
	add := func(s ...string) { sections = append(sections, s...) }

	lead, tail := in.instructions(phase)
	placed := false
	for i, s := range lead {
		var p bool
		lead[i], p = in.fill(s)
		placed = placed || p
	}

	switch phase {
	case PhaseInvestigate:
		add(lead...)
		add(in.taskSection(placed, "")...)

	case PhaseImplement:
		add(lead...)
		add(commitNote)
		add(in.taskSection(placed, "")...)
		add(in.ask()...)

	case PhaseReview:
		category := in.Category
		if reviewFocus[category] == "" {
			category = "correctness"
		}
		add(fmt.Sprintf("Review the changes on this branch for **%s**.", category), reviewFocus[category])
		add(lead...)
		add(in.taskSection(placed, "The task under review:\n\n")...)
		add(findingFormat)
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
		add(in.taskSection(placed, "The original task, for context:\n\n")...)
		add(in.ask()...)

	case PhaseSimplify:
		add(lead...)
		add(commitNote)
		add(in.taskSection(placed, "The task this branch implements:\n\n")...)

	case PhaseTest:
		add(lead...)
		add(testerTools)
		add(in.taskSection(placed, "")...)
		add(findingFormat)

	default:
		add(in.task())
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
