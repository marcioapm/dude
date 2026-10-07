package delivery

import (
	"strings"
	"testing"
)

// The conductor is told when to steer rather than start another phase, and
// that interrupting is for wasted work only; with tools, how to from its shell.
func TestTheConductorIsToldWhenToSteer(t *testing.T) {
	got := ConductorPrompt("briefing", PromptInput{Tools: true})
	for _, want := range []string{"Steer a running Run (steer) that is going the wrong way",
		"Start another phase only once the Run has ended", "Interrupt only when its current work is wasted",
		"`dude steer RUN TEXT`"} {
		if !strings.Contains(got, want) {
			t.Errorf("the conductor's prompt lacks %q", want)
		}
	}
}

// A mention from a pull request is answered there, and hands over nothing.
func TestTheConductorIsToldToAnswerOnThePullRequest(t *testing.T) {
	got := ConductorPrompt("briefing", PromptInput{Tools: true})
	for _, want := range []string{"Chat message from a GitHub person", "changes nothing about who decides",
		"Answer it on the pull request (reply_on_pull_request", "`dude pr reply PR TEXT --in-reply-to ID`"} {
		if !strings.Contains(got, want) {
			t.Errorf("the conductor's prompt lacks %q", want)
		}
	}
}

// A conductor is told when it edits and when it delegates, how to stay
// current with the task branch, to commit then publish, and that it runs
// no tests and its commits are reviewed; its checkout says which
// repositories it may change and the branch it is kept current with.
func TestTheConductorIsToldWhenToEditAndWhenToDelegate(t *testing.T) {
	got := ConductorPrompt("briefing", PromptInput{Tools: true, TaskBranch: "dude/t1/attempt-1",
		Repositories: []PromptRepo{{Name: "app", Path: "/workspace/repos/app"}, {Name: "docs", Path: "/workspace/repos/docs", ReadOnly: true}}})
	for _, want := range []string{"a rename, a one-line fix, a review nit", "Anything larger, or anything that needs the tests run, you delegate",
		"`git merge --ff-only lux/<branch>`", "`git merge lux/<branch>` when your work was kept",
		"Commit in your checkout, then publish (`dude publish --message M`)", "never build, install or run tests",
		"the pull request gate refuses an unreviewed commit of yours",
		"`app` at `/workspace/repos/app` (you may edit it, small things only)", "`docs` at `/workspace/repos/docs` (read only)",
		"dude brings it in as `lux/dude/t1/attempt-1`"} {
		if !strings.Contains(got, want) {
			t.Errorf("the conductor's prompt lacks %q", want)
		}
	}
	if strings.Contains(got, "You never edit files") {
		t.Error("the conductor is still told it never edits")
	}
}

// A person's answer is part of the task from then on: a reviewer judging
// the change, or a fixer after it, is told what was decided rather than
// flagging it again (a real run's reviewers reported "chosen with no
// recorded decision" about an answered question).
func TestEveryPhaseIsToldWhatPeopleDecided(t *testing.T) {
	in := PromptInput{Title: "Add a reading-level score", Decisions: []Decision{{
		Question: "Which formula should reading_level use?\nThey differ a lot.",
		Answer:   "Flesch-Kincaid grade level",
	}}}
	for _, phase := range []string{PhaseImplement, PhaseReview, PhaseFix, PhaseSimplify} {
		p := Prompt(phase, in)
		if !strings.Contains(p, "Q: Which formula should reading_level use? They differ a lot.") ||
			!strings.Contains(p, "A: Flesch-Kincaid grade level") {
			t.Errorf("%s's prompt lacks the decision:\n%s", phase, p)
		}
	}
}

// A criterion written over several lines stays one item in the prompt: its
// later lines sit under its marker, so a line that starts with "- " is not
// read as a criterion of its own, and nothing drops out of the list.
func TestAMultiLineCriterionStaysOneItem(t *testing.T) {
	in := PromptInput{Title: "Checkout keeps SEPA", AcceptanceCriteria: []string{
		"Each step fires its funnel event once:\n- `checkout.plan_selected`\n- `checkout.completed`",
		// A fence keeps its own indentation under the marker, and a blank line
		// between paragraphs does not end the item.
		"Runs:\n```python\nif ready:\n    ship()\n```\n\nThen it ships.",
		"Invoice only for annual plans",
	}}
	want := "Acceptance criteria:\n" +
		"- Each step fires its funnel event once:\n  - `checkout.plan_selected`\n  - `checkout.completed`\n" +
		"- Runs:\n  ```python\n  if ready:\n      ship()\n  ```\n  \n  Then it ships.\n" +
		"- Invoice only for annual plans"
	for _, phase := range []string{PhaseInvestigate, PhaseImplement, PhaseReview, PhaseFix, PhaseSimplify, PhaseTest} {
		if got := Prompt(phase, in); !strings.Contains(got, want) {
			t.Errorf("%s: criteria not one item each:\n%s", phase, got)
		}
	}
	saved := "Criteria:\n{{task.criteria}}"
	in.OrgPrompt = &saved
	if got := Prompt(PhaseImplement, in); !strings.Contains(got, "Criteria:\n"+strings.TrimPrefix(want, "Acceptance criteria:\n")) {
		t.Errorf("{{task.criteria}} not one item each:\n%s", got)
	}
}
