package delivery

import (
	"strings"
	"testing"
)

// The merge-not-rebase rule is dude's, not the role's: an organisation's
// own prompt for a writer, or for the conductor, does not drop it.
func TestBaseIntegrationRuleSurvivesAnOrganisationsPrompt(t *testing.T) {
	org := "Do it our way."
	for _, custom := range []*string{nil, &org} {
		prompts := map[string]string{
			"implementer": Prompt(PhaseImplement, PromptInput{OrgPrompt: custom}),
			"fixer":       Prompt(PhaseFix, PromptInput{OrgPrompt: custom}),
			"simplifier":  Prompt(PhaseSimplify, PromptInput{OrgPrompt: custom}),
			"conductor":   ConductorPrompt("briefing", PromptInput{OrgPrompt: custom, Repositories: []PromptRepo{{Name: "target", Path: "/workspace/repos/target"}}}),
		}
		for role, p := range prompts {
			for _, want := range []string{"sync_base", "never rebase", "never force"} {
				if !strings.Contains(p, want) {
					t.Errorf("%s (org prompt %v) lacks %q", role, custom != nil, want)
				}
			}
		}
		if p := prompts["conductor"]; !strings.Contains(p, "delegate review and test") {
			t.Errorf("conductor (org prompt %v) is not told to delegate review and test:\n%s", custom != nil, p)
		}
		if p := Prompt(PhaseReview, PromptInput{OrgPrompt: custom}); strings.Contains(p, "sync_base") {
			t.Errorf("a reviewer (org prompt %v) is told of sync_base", custom != nil)
		}
	}
	for _, role := range []string{"implementer", "fixer", "simplifier", "conductor"} {
		if strings.Contains(BuiltinPrompt(role), "sync_base") {
			t.Errorf("%s's editable prompt carries the rule: an organisation's prompt would drop it", role)
		}
	}
	b := baseNote(map[string]string{"target": "release"})
	if strings.Contains(b, "git fetch origin") || !strings.Contains(b, "available") {
		t.Fatal("private origin advice", b)
	}
}

// A session's agent is told to name its session once the subject is clear
// and to leave a member's name alone (with tools), and how to publish a
// document for the members (always: it needs no tool), even under an
// organisation's own instructions.
func TestTheBrainstormIsToldToNameItsSessionAndHowToPublish(t *testing.T) {
	org := "Think with us."
	for _, in := range []PromptInput{{Tools: true}, {Tools: true, OrgPrompt: &org}} {
		got := BrainstormPrompt("briefing", in)
		for _, want := range []string{"name_session once its subject is clear", "at most 60 characters",
			"Name it again only if the subject clearly changes", "Once a member has named it, leave the name alone",
			"`dude publish FILE --name NAME --description \"…\"`", "one short line saying what the file is for",
			"a design note, a diagram, a table, a CSV", "Say in your reply what you published"} {
			if !strings.Contains(got, want) {
				t.Errorf("the brainstorm's prompt lacks %q", want)
			}
		}
	}
	bare := BrainstormPrompt("briefing", PromptInput{})
	if strings.Contains(bare, "name_session") || !strings.Contains(bare, "dude publish FILE") {
		t.Errorf("without tools it is told of a tool it lacks, or not how to publish:\n%s", bare)
	}
}

// A brainstorm that may reach any host is told it can read the web and to
// cite its sources, with or without tools and under an organisation's own
// prompt; one that may not is told nothing about the web.
func TestOnlyABrainstormThatMayReachAnyHostIsToldItCanReadTheWeb(t *testing.T) {
	org := "Think with us."
	for _, in := range []PromptInput{{}, {Tools: true}, {Tools: true, OrgPrompt: &org},
		{ProjectPrompt: "Ours alone.", ProjectPromptMode: "replace"}} {
		for _, anywhere := range []bool{false, true} {
			in.Anywhere = anywhere
			got := BrainstormPrompt("briefing", in)
			told := strings.Contains(got, "You can read the web: search for and fetch public pages") &&
				strings.Contains(got, "Say where each fact came from, with its URL.")
			if told != anywhere || (!anywhere && strings.Contains(strings.ToLower(got), "web")) {
				t.Errorf("anywhere %v, tools %v, org prompt %v: told of the web %v:\n%s", anywhere, in.Tools, in.OrgPrompt != nil, told, got)
			}
		}
	}
}

// Every agent publishes with dude publish alone: no prompt names lux's
// directory or lux-shim, which dude publish hides.
func TestPromptsNameOnlyDudePublish(t *testing.T) {
	prompts := map[string]string{"brainstorm": BrainstormPrompt("briefing", PromptInput{Tools: true, CLI: true})}
	for _, phase := range []string{PhaseImplement, PhaseReview, PhaseFix, PhaseSimplify, PhaseTest} {
		prompts[phase] = Prompt(phase, PromptInput{Title: "T", Tools: true, CLI: true})
	}
	for name, p := range prompts {
		if !strings.Contains(p, "dude publish FILE --name NAME --description") {
			t.Errorf("the %s prompt does not say how to publish:\n%s", name, p)
		}
		for _, banned := range []string{"LUX_ARTIFACTS", "lux-shim", "/.lux/"} {
			if strings.Contains(p, banned) {
				t.Errorf("the %s prompt names %q", name, banned)
			}
		}
	}
}

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

// On an epic a phase gets both the coordination section and the machine
// line: the coordination comes first and the machine line is the prompt's
// tail, after every other note, since a harness may drop the tail.
func TestAPhasePromptOnAnEpicEndsWithTheMachineAfterTheCoordination(t *testing.T) {
	const coordination, machine = "## Your epic\n\nC", "## Your machine\n\nM"
	in := PromptInput{Title: "T", Tools: true, CLI: true, Context: "Use tabs.",
		Coordination: coordination, Machine: machine}
	for _, phase := range []string{PhaseInvestigate, PhaseImplement, PhaseReview, PhaseFix} {
		got := Prompt(phase, in)
		if !strings.HasSuffix(got, "\n\n"+machine) {
			t.Errorf("%s: the prompt does not end with the machine line:\n%s", phase, got)
		}
		c, m := strings.Index(got, coordination), strings.Index(got, machine)
		if c < 0 || c >= m {
			t.Errorf("%s: the coordination (at %d) is not before the machine line (at %d):\n%s", phase, c, m, got)
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

// underEveryInstructions renders a phase's prompt with dude's built-in
// instructions, an organization's, and a project's that replaces them: a
// contract the workflow or a person relies on must survive all three.
func underEveryInstructions(phase string, in PromptInput) map[string]string {
	org, project := "ORG: do it our way.", "PROJECT: do it this way only."
	withOrg, replaced := in, in
	withOrg.OrgPrompt = &org
	replaced.OrgPrompt = &org
	replaced.ProjectPrompt, replaced.ProjectPromptMode = project, "replace"
	return map[string]string{"built-in": Prompt(phase, in), "organization": Prompt(phase, withOrg),
		"project replace": Prompt(phase, replaced)}
}

func twoRepos() []PromptRepo {
	return []PromptRepo{{Name: "api", Path: "/workspace/repos/api"}, {Name: "web", Path: "/workspace/repos/web"}}
}

// The tester accounts for every criterion, changes nothing, knows what its
// severities cause, and records its evidence, whatever its instructions say.
func TestTheTestersContractSurvivesAnyInstructions(t *testing.T) {
	in := PromptInput{Title: "Export invoices", AcceptanceCriteria: []string{"A CSV downloads", "Totals match"},
		Repositories: twoRepos(), BlockingSeverities: []string{"blocking", "high"}, Tools: true, CLI: true}
	for kind, got := range underEveryInstructions(PhaseTest, in) {
		for _, want := range []string{testerContract, testerTools, findingFormat,
			severityNote(in.BlockingSeverities), "commit in none of them"} {
			if strings.Count(got, want) != 1 {
				t.Errorf("%s: the tester's prompt should say %q once:\n%s", kind, want, got)
			}
		}
		for _, never := range []string{"Commit in each repository you change", commitNote, "you may change it", "asked to check again"} {
			if strings.Contains(got, never) {
				t.Errorf("%s: the tester is told %q:\n%s", kind, never, got)
			}
		}
	}
	// The contract's anchors, so a rewrite of it cannot drop one unnoticed.
	for _, want := range []string{"`acceptance.md`", "`pass`, `fail`, `blocked`", "`not-applicable`",
		"never `pass`", "commit nothing", "Test environment:", "head commit", "video was written and is not empty"} {
		if !strings.Contains(testerContract, want) {
			t.Errorf("the tester's contract lacks %q", want)
		}
	}
	if !strings.Contains(testerTools, "1920×1080 viewport") {
		t.Error("the tester does not record at 1920×1080 by default")
	}
}

// Every reviewer establishes what is under review, grades its evidence,
// and ends with what it read, ran and could not check, whatever its
// instructions say; each category has a focus of its own.
func TestTheReviewersContractSurvivesAnyInstructions(t *testing.T) {
	for _, category := range reviewerOrder {
		in := PromptInput{Title: "Export invoices", AcceptanceCriteria: []string{"A CSV downloads"}, Category: category,
			Repositories: twoRepos(), BlockingSeverities: []string{"blocking", "high"}, Tools: true,
			Findings: []Finding{{Severity: "high", Title: "Totals off by one"}}}
		for kind, got := range underEveryInstructions(PhaseReview, in) {
			for _, want := range []string{"for **" + category + "**", reviewFocus[category], reviewContract,
				findingFormat, reviewSummary, severityNote(in.BlockingSeverities), "verdicts:"} {
				if strings.Count(got, want) != 1 {
					t.Errorf("%s/%s: the reviewer's prompt should say %q once:\n%s", category, kind, want, got)
				}
			}
			// How to diff is the contract's to say, once, whatever the checkouts.
			if n := strings.Count(got, "`git diff`"); n != 1 {
				t.Errorf("%s/%s: the reviewer is told how to diff %d times:\n%s", category, kind, n, got)
			}
			if s, f := strings.Index(got, reviewSummary), strings.Index(got, findingFormat); s < f {
				t.Errorf("%s/%s: the summary is asked for before the findings", category, kind)
			}
		}
	}
	for _, want := range []string{"`Reproduced:`", "`Traced:`", "`Suspected:`", "same line as `description:`", "`low` or `note` at most",
		"`git merge-base`", "minimal local change", "`blocking` is wrong behaviour"} {
		if !strings.Contains(reviewContract, want) {
			t.Errorf("the reviewer's contract lacks %q", want)
		}
	}
	if !strings.Contains(reviewSummary, "review_summary:") || !strings.Contains(reviewSummary, "not_checked") {
		t.Errorf("the summary asks for no account of what was not checked:\n%s", reviewSummary)
	}
	// What the summary asks for is dropped by the parser, so its example is
	// never taken for a finding.
	if got := ParseFindings(reviewSummary); len(got) != 0 {
		t.Errorf("the summary's own example parses as findings: %+v", got)
	}
	if !strings.Contains(earlierFindings([]Finding{{Title: "x"}}), "never judge by which files the fix touched") {
		t.Error("a re-review is not told to judge a fix by the code")
	}
}

// Every fixer reproduces before it changes code, leaves a regression test,
// reruns the checks, and may say a finding is wrong, whatever its
// instructions say; dude's own keep the fix to what was raised.
func TestTheFixersContractSurvivesAnyInstructions(t *testing.T) {
	in := PromptInput{Title: "Export invoices", AcceptanceCriteria: []string{"A CSV downloads"}, Repositories: twoRepos(),
		Tools: true, Findings: []Finding{{Severity: "high", Title: "Totals off by one", File: "src/export.ts", Line: 40}}}
	for kind, got := range underEveryInstructions(PhaseFix, in) {
		for _, want := range []string{fixContract, commitNote, "Totals off by one"} {
			if strings.Count(got, want) != 1 {
				t.Errorf("%s: the fixer's prompt should say %q once:\n%s", kind, want, got)
			}
		}
		if f, c := strings.Index(got, "Totals off by one"), strings.Index(got, fixContract); c < f {
			t.Errorf("%s: how to fix comes before what to fix:\n%s", kind, got)
		}
	}
	if got := Prompt(PhaseFix, in); !strings.Contains(got, "Fix only what is raised above") {
		t.Errorf("the built-in fixer lost its scope rule:\n%s", got)
	}
	for _, want := range []string{"reproduce it first", "regression test that fails without the fix",
		"rerun the affected tests", "say so with your evidence", "`pushback.md`"} {
		if !strings.Contains(fixContract, want) {
			t.Errorf("the fixer's contract lacks %q", want)
		}
	}
}

// The implementer tests its own work, whatever its instructions say: the
// tester after it does not do that for it.
func TestTheImplementerTestsItsOwnWorkUnderAnyInstructions(t *testing.T) {
	in := PromptInput{Title: "Export invoices", AcceptanceCriteria: []string{"A CSV downloads"}, Repositories: twoRepos(), Tools: true}
	for kind, got := range underEveryInstructions(PhaseImplement, in) {
		for _, want := range []string{implementContract, commitNote} {
			if strings.Count(got, want) != 1 {
				t.Errorf("%s: the implementer's prompt should say %q once:\n%s", kind, want, got)
			}
		}
	}
	for _, want := range []string{"exercise the changed behaviour yourself", "a test for each acceptance criterion"} {
		if !strings.Contains(implementContract, want) {
			t.Errorf("the implementer's contract lacks %q", want)
		}
	}
}
