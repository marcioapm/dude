// Package fakeagent is the scripted agent dude's tests run instead of a
// model: what each phase does, written once.
//
// It is played two ways. Against the fake lux (the default suites), the
// fake plays it directly as a Behaviour. Against a real lux (the contract
// suite), it is compiled to a lux-fake script — lux-fake being the
// scripted ACP agent lux ships for tests — and sent as the Run's prompt.
// Both describe the same agent, so the two suites test the same thing.
//
// The loop it exercises: the implementer commits; the reviewer raises one
// blocking finding unless the tree already holds the fixer's file; the fixer
// adds that file; the re-review is then clean; the simplifier commits.
// Deciding from the tree rather than a counter keeps a replayed Run
// deterministic.
//
// A model named "fake/hang" never finishes its turn, which is how a test
// gets a live agent to steer, pause and abort.
package fakeagent

import (
	"fmt"
	"strings"
)

// ModelPrefix selects the scripted agent in a project's model settings.
const ModelPrefix = "fake/"

// HangModel keeps its agent busy until stopped.
const HangModel = "fake/hang"

// AskModel's implementer stops on a question first, and does its work in
// the turn the answer starts.
const AskModel = "fake/ask"

// Question is what AskModel's implementer asks, in the format the prompt
// asks a model for.
const Question = "I need a decision first.\n\n```question\nShould FACTORY.md be in English?\n- yes\n- no\n```\n"

// Is says whether a model is the scripted agent rather than a real one.
func Is(model string) bool { return strings.HasPrefix(model, ModelPrefix) }

// FixedFile is what the fixer writes, and what tells the reviewer a fix
// has been made.
const FixedFile = "FIXED.md"

// Finding is the one blocking finding the reviewer raises, in the format
// the review prompt asks a model for.
const Finding = "---\n" +
	"severity: blocking\n" +
	"category: correctness\n" +
	"file: FACTORY.md\n" +
	"line: 1\n" +
	"title: FACTORY.md does not record the fix\n" +
	"description: The change is missing a record that the review was addressed.\n" +
	"suggested_fix: Add a file naming what was fixed.\n"

// Verdict is the reviewer's judgement of its earlier finding once the fix
// is in the tree, in the format the re-review prompt asks for.
const Verdict = "```yaml\nverdicts:\n  F1: fixed\n```\n"

// Step is what the agent does for one phase.
type Step struct {
	// Files to commit, path → one line of content; none changes nothing.
	Commit map[string]string
	// Commit message.
	Message string
	// What it replies.
	Reply string
	// Never finishes its turn.
	Hang bool
	// Its first turn ends on this question instead.
	Ask string
}

// For is the agent's step for a phase Run. fixed says whether the tree it
// starts from already has the fixer's file — which only the reviewer reads.
func For(phase, model, runID string, fixed bool) Step {
	if model == HangModel {
		return Step{Hang: true}
	}
	switch phase {
	case "implement":
		step := Step{Commit: map[string]string{"FACTORY.md": "Written by run " + runID},
			Message: "Add FACTORY.md for " + runID, Reply: "Implemented it."}
		if model == AskModel {
			step.Ask = Question
		}
		return step
	case "fix":
		// The content names the Run, so a second fix is still a change.
		return Step{Commit: map[string]string{FixedFile: "addressed by " + runID},
			Message: "Address review findings for " + runID, Reply: "Addressed it."}
	case "simplify":
		return Step{Commit: map[string]string{"SIMPLE.md": "simplified by " + runID},
			Message: "Simplify " + runID, Reply: "Simplified."}
	case "review":
		if fixed {
			// Shown the finding it raised, it judges the fix.
			return Step{Reply: "Reviewed the fix; no further problems.\n\n" + Verdict}
		}
		return Step{Reply: "One problem:\n\n```yaml\n" + Finding + "```\n"}
	}
	return Step{Reply: "Nothing to do for " + phase + "."}
}

// Script compiles the agent's step to lux-fake's script language
// (lux/cmd/lux-fake), for a real lux.
//
// The reviewer decides inside the container, from the tree it checked out,
// as a real one would.
func Script(phase, model, runID string) string {
	if model == HangModel {
		return "sleep 3600"
	}
	if phase == "review" {
		// Built in a file and read back, because separate reply lines would
		// run into one another; reported only when the fixer's file is not
		// in the tree this review checked out.
		var b strings.Builder
		for _, line := range strings.Split(strings.TrimSuffix(Finding, "\n"), "\n") {
			b.WriteString("append /tmp/review.yaml " + line + "\n")
		}
		b.WriteString("unless-exists " + FixedFile + " read /tmp/review.yaml\n")
		for _, line := range strings.Split(strings.TrimSuffix(Verdict, "\n"), "\n") {
			b.WriteString("append /tmp/verdict.yaml " + line + "\n")
		}
		b.WriteString("if-exists " + FixedFile + " read /tmp/verdict.yaml\n")
		b.WriteString("echo reviewed")
		return b.String()
	}
	step := For(phase, model, runID, false)
	if step.Ask != "" {
		// lux-fake has no way to wait for an answer mid-script yet; the fake
		// lux plays the asking agent, and a real lux says plainly it cannot.
		return "fail " + AskModel + " is only played by the fake lux"
	}
	var b strings.Builder
	for path, line := range step.Commit {
		fmt.Fprintf(&b, "append %s %s\n", path, line)
	}
	if len(step.Commit) > 0 {
		b.WriteString("commit " + step.Message + "\n")
	}
	b.WriteString("echo " + step.Reply)
	return b.String()
}
