package delivery

import (
	"fmt"
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
	Findings   []Finding
	PRFeedback []forge.ActionableFeedback
	// Review phase: the severities that block, from the delivery's policy.
	BlockingSeverities []string
	// Per-project, per-role notes, appended last so a project can tell its
	// reviewer what this codebase considers a defect without that text
	// reaching every other role.
	Context string
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
	return strings.Join(parts, "\n\n")
}

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
	"file: path/to/file.ts\n" +
	"line: 123\n" +
	"title: One line naming the problem\n" +
	"description: What is wrong and why it matters.\n" +
	"suggested_fix: What to do instead.\n" +
	"```\n\n" +
	"Report nothing if you find nothing. A finding you are not confident in is a " +
	"`note`, not a `blocking` — a reviewer that cries wolf costs the next fix " +
	"attempt for nothing."

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

// askNote tells an agent that changes code how to stop for a person. The
// fenced block, not a question in prose, is what stops the run: an agent
// thinking aloud ("should I also…?") must not stall a delivery.
const askNote = "If you cannot go on without a decision only a person can make — the task is ambiguous " +
	"in a way that changes what you build, or two reasonable readings conflict — stop and ask. End your " +
	"reply with the question in a fenced block, and offer choices as `- ` lines when there are some:\n\n" +
	"```question\nShould the command read standard input when no path is given?\n- yes\n- no\n```\n\n" +
	"The answer comes back as your next message. Do not ask about anything you can decide or find out " +
	"yourself; most tasks need no question at all."

// Prompt composes one phase's prompt.
func Prompt(phase string, in PromptInput) string {
	var sections []string
	add := func(s ...string) { sections = append(sections, s...) }

	switch phase {
	case PhaseInvestigate:
		add("Investigate this task before any code is written. Read the relevant code, identify "+
			"what will have to change, and report what you found. Do not change anything.", in.task())

	case PhaseImplement:
		add("Implement this task. Run the project's formatter, type checks and tests before you "+
			"finish — handing over code that does not build is not finishing. "+commitNote, in.task(), askNote)

	case PhaseReview:
		category := in.Category
		if reviewFocus[category] == "" {
			category = "correctness"
		}
		add(fmt.Sprintf("Review the changes on this branch for **%s**.", category),
			reviewFocus[category],
			"You may run the code, run the tests, and write throwaway scripts to check a hypothesis. "+
				"Do not commit: your output is findings, and someone else will make the change.",
			"The task under review:\n\n"+in.task(),
			findingFormat)
		if note := severityNote(in.BlockingSeverities); note != "" {
			add(note)
		}

	case PhaseFix:
		add("Address the feedback below. " + commitNote)
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
				if f.Path != "" {
					parts = append(parts, "`"+f.Path+"`")
				}
				if f.Author != "" {
					parts = append(parts, "**"+f.Author+"**")
				}
				items = append(items, strings.Join(append(parts, f.Body), " — "))
			}
			add("## Pull request feedback\n\n" + strings.Join(items, "\n\n"))
		}
		add("Fix only what is raised above. Widening the change makes the re-review harder and risks new findings.",
			"The original task, for context:\n\n"+in.task(), askNote)

	case PhaseSimplify:
		add("Simplify the changes on this branch without changing what they do.",
			"Remove needless complexity, improve names and structure, delete dead code the change "+
				"introduced, and consolidate obvious duplication.",
			"Do not widen the scope, do not add features, and do not change behaviour. Run the tests: "+
				"if they do not pass, your simplification was not behaviour-preserving. "+commitNote,
			"The task this branch implements:\n\n"+in.task())

	case PhaseTest:
		add("Exercise this change the way a person would. Start the application, drive it in a "+
			"browser, and confirm it does what the task asked.",
			"Record what you did: screenshots at each meaningful step, and a video of the whole flow. "+
				"Read the console and report anything it says.",
			"You are not looking for what the unit tests already cover. You are looking for what they "+
				"cannot: does the feature actually work when used.",
			in.task(), findingFormat)

	default:
		add(in.task())
	}

	if c := strings.TrimSpace(in.Context); c != "" {
		add("## Project notes\n\n" + c)
	}
	return strings.Join(sections, "\n\n")
}
