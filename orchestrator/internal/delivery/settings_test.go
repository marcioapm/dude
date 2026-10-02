package delivery

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestARoleResolvesFieldByFieldProjectThenOrganization(t *testing.T) {
	org := json.RawMessage(`{"reviewer":{"tier":"mtr_thinker","effort":"high","timeLimitMinutes":20},
		"implementer":{"tier":"mtr_coder","context":"org notes"}}`)
	project := json.RawMessage(`{"reviewer":{"effort":"low"},"implementer":{"tier":"mtr_fast"}}`)

	// The project changed only the reviewer's effort: the tier and limit
	// are still its organization's.
	if got, want := ResolveRole("reviewer", project, org), (RoleSettings{Tier: "mtr_thinker", Effort: "low", TimeLimitMinutes: 20}); got != want {
		t.Errorf("reviewer = %+v, want %+v", got, want)
	}
	// Each field on its own: the project's tier, the organization's notes.
	if got, want := ResolveRole("implementer", project, org), (RoleSettings{Tier: "mtr_fast", Context: "org notes"}); got != want {
		t.Errorf("implementer = %+v, want %+v", got, want)
	}
	// Nothing configured anywhere is nothing: the caller refuses to run it.
	if got := ResolveRole("simplifier", project, org); got != (RoleSettings{}) {
		t.Errorf("simplifier = %+v", got)
	}
	// A layer that is not JSON (or absent) is skipped, not fatal.
	if got := ResolveRole("implementer", nil, json.RawMessage(`not json`), org); got.Tier != "mtr_coder" {
		t.Errorf("implementer over a broken layer = %+v", got)
	}
}

func TestTheFixerIsTheImplementerUnlessSetApart(t *testing.T) {
	org := json.RawMessage(`{"implementer":{"tier":"mtr_coder","effort":"medium"}}`)
	if got := ResolveRole("fixer", nil, org); got.Tier != "mtr_coder" || got.Effort != "medium" {
		t.Errorf("fixer = %+v, want the implementer's", got)
	}
	project := json.RawMessage(`{"fixer":{"effort":"high"}}`)
	if got := ResolveRole("fixer", project, org); got.Tier != "mtr_coder" || got.Effort != "high" {
		t.Errorf("fixer with its own effort = %+v", got)
	}
	// The fixer's own tier on any layer comes before the implementer's on
	// any layer: the organization's fixer beats the project's implementer.
	if got := ResolveRole("fixer", json.RawMessage(`{"implementer":{"tier":"fast"}}`),
		json.RawMessage(`{"fixer":{"tier":"thinker"},"implementer":{"tier":"coder"}}`)); got.Tier != "thinker" {
		t.Errorf("fixer set apart on the organization, implementer on the project = %+v, want thinker", got)
	}
	if PromptRoleForPhase[PhaseFix] != "fixer" || PromptRoleForPhase[PhaseReview] != "reviewer" {
		t.Error("phases take the wrong role's settings")
	}
}

func TestDeliveryPolicyIsTheFactorysThenTheOrganizationsThenTheProjects(t *testing.T) {
	org := json.RawMessage(`{"maxReviewIterations":3,"simplify":false}`)
	project := json.RawMessage(`{"maxReviewIterations":7}`)
	p, err := ResolvePolicy(org, project)
	if err != nil {
		t.Fatal(err)
	}
	d := DefaultPolicy()
	if p.MaxReviewIterations != 7 || p.Simplify || p.MaxPRFixIterations != d.MaxPRFixIterations ||
		!reflect.DeepEqual(p.RequiredReviewers, d.RequiredReviewers) {
		t.Errorf("policy = %+v", p)
	}
	// An organization that sets nothing changes nothing.
	if p, _ := ResolvePolicy(json.RawMessage(`{}`), nil); !reflect.DeepEqual(p, d) {
		t.Errorf("empty layers = %+v, want the defaults", p)
	}
	if _, err := ResolvePolicy(json.RawMessage(`{"simplify":"yes"}`)); err == nil {
		t.Error("a malformed layer was accepted")
	}
}

func TestAnOrganizationsPromptReplacesTheBuiltInOnly(t *testing.T) {
	in := PromptInput{Title: "Greet people", Context: "Use bun."}
	builtin := Prompt(PhaseImplement, in)
	if !strings.HasPrefix(builtin, BuiltinPrompt("implementer")) {
		t.Fatalf("the built-in implementer prompt does not lead:\n%s", builtin)
	}

	org := "Write the change and its tests."
	in.OrgPrompt = &org
	got := Prompt(PhaseImplement, in)
	if strings.Contains(got, "handing over code that does not build") {
		t.Errorf("the built-in instructions survived the organization's:\n%s", got)
	}
	// dude's brief is kept whatever the instructions say: the task, where
	// to publish, the project's notes — last.
	for _, want := range []string{org, "Greet people", "LUX_ARTIFACTS", "## Project notes\n\nUse bun."} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt lacks %q:\n%s", want, got)
		}
	}
	if !strings.HasSuffix(got, "Use bun.") {
		t.Errorf("the project's notes are not last:\n%s", got)
	}
}

func TestAProjectAddsToOrReplacesTheOrganizationsPrompt(t *testing.T) {
	org := "ORG INSTRUCTIONS"
	base := PromptInput{Title: "T", OrgPrompt: &org}

	add := base
	add.ProjectPrompt, add.ProjectPromptMode = "PROJECT ADDITION", "add"
	got := Prompt(PhaseSimplify, add)
	if i, j := strings.Index(got, org), strings.Index(got, "PROJECT ADDITION"); i < 0 || j < i {
		t.Errorf("add: the project's prompt should follow the organization's:\n%s", got)
	}

	replace := base
	replace.ProjectPrompt, replace.ProjectPromptMode = "PROJECT ONLY", "replace"
	got = Prompt(PhaseSimplify, replace)
	if strings.Contains(got, org) || !strings.Contains(got, "PROJECT ONLY") {
		t.Errorf("replace: the organization's prompt should be gone:\n%s", got)
	}

	// Added to dude's own when the organization has none.
	builtin := PromptInput{Title: "T", ProjectPrompt: "PROJECT ADDITION", ProjectPromptMode: "add"}
	got = Prompt(PhaseSimplify, builtin)
	if !strings.Contains(got, BuiltinPrompt("simplifier")) || !strings.Contains(got, "PROJECT ADDITION") {
		t.Errorf("add over the built-in:\n%s", got)
	}

	// An empty addition is "use the organization's as is".
	same := base
	same.ProjectPromptMode = "add"
	if Prompt(PhaseSimplify, same) != Prompt(PhaseSimplify, base) {
		t.Error("an empty addition changed the prompt")
	}
}

func TestAReviewerKeepsItsCategoryAndFormatUnderACustomPrompt(t *testing.T) {
	custom := "Be terse."
	got := Prompt(PhaseReview, PromptInput{Title: "T", Category: "security", OrgPrompt: &custom, BlockingSeverities: []string{"high"}})
	for _, want := range []string{"for **security**", reviewFocus["security"], custom, "severity: blocking | high", "`high`"} {
		if !strings.Contains(got, want) {
			t.Errorf("review prompt lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Do not commit: your output is findings") {
		t.Errorf("the built-in reviewer instructions survived:\n%s", got)
	}
}

func TestTheFixersFeedbackSitsBetweenItsBuiltInInstructions(t *testing.T) {
	got := Prompt(PhaseFix, PromptInput{Title: "T", Findings: []Finding{{Severity: "high", Title: "Broken"}}})
	address, feedback, only := strings.Index(got, "Address the feedback"), strings.Index(got, "Broken"), strings.Index(got, "Fix only what")
	if address < 0 || feedback < address || only < feedback {
		t.Errorf("fixer order: address=%d feedback=%d only=%d\n%s", address, feedback, only, got)
	}
	// A person's fixer prompt comes in one piece, before the feedback.
	custom := "Fix it."
	got = Prompt(PhaseFix, PromptInput{Title: "T", OrgPrompt: &custom, Findings: []Finding{{Severity: "high", Title: "Broken"}}})
	if strings.Index(got, custom) > strings.Index(got, "Broken") || strings.Contains(got, "Fix only what") {
		t.Errorf("custom fixer prompt:\n%s", got)
	}
}

func TestEveryPromptRoleHasABuiltInPrompt(t *testing.T) {
	for _, role := range PromptRoles {
		if strings.TrimSpace(BuiltinPrompt(role)) == "" {
			t.Errorf("%s has no built-in prompt", role)
		}
	}
	for phase, role := range PromptRoleForPhase {
		if BuiltinPrompt(role) == "" {
			t.Errorf("phase %s runs role %s, which has no prompt", phase, role)
		}
	}
}

func TestACustomPromptStillCommitsAndRecords(t *testing.T) {
	mine := "Do it my way."
	for phase, note := range map[string]string{PhaseImplement: commitNote, PhaseFix: commitNote, PhaseSimplify: commitNote, PhaseTest: testerTools} {
		org := PromptInput{Title: "T", OrgPrompt: &mine}
		replaced := PromptInput{Title: "T", ProjectPrompt: mine, ProjectPromptMode: "replace"}
		for _, in := range []PromptInput{org, replaced} {
			if got := Prompt(phase, in); strings.Count(got, note) != 1 {
				t.Errorf("%s under a custom prompt should say %q once:\n%s", phase, note, got)
			}
		}
		if got := Prompt(phase, PromptInput{Title: "T"}); strings.Count(got, note) != 1 {
			t.Errorf("%s's built-in prompt should say %q once:\n%s", phase, note, got)
		}
	}
}

// A saved prompt's variables are filled in where it names them, and the
// task still follows in full, however much of it the prompt names.
func TestASavedPromptsVariablesAreFilledAndTheTaskStillFollows(t *testing.T) {
	in := PromptInput{
		Title: "Greet people", Goal: "Say hello to whoever arrives.",
		AcceptanceCriteria: []string{"It greets", "It has a test"},
		Decisions:          []Decision{{Question: "Which language?", Answer: "English"}},
		Branch:             "dude/tsk_1/run-1", BaseRef: "main",
	}
	org := "**Goal:** {{task.goal}}\n\nWork on `{{ run.branch }}`, from `{{run.base_ref}}`. Keep {{unknown.thing}} as it is."
	in.OrgPrompt = &org
	for _, phase := range []string{PhaseImplement, PhaseReview, PhaseFix, PhaseSimplify, PhaseTest} {
		got := Prompt(phase, in)
		for _, want := range []string{
			"**Goal:** Say hello to whoever arrives.",
			"Work on `dude/tsk_1/run-1`, from `main`.",
			"Keep {{unknown.thing}} as it is.",
			// The prompt named the goal only: the rest of the task is not lost.
			"Greet people", "Acceptance criteria:\n- It greets\n- It has a test",
			"Q: Which language?\n  A: English",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("%s: prompt lacks %q:\n%s", phase, want, got)
			}
		}
	}
	if got := Prompt(PhaseReview, in); !strings.Contains(got, "The task under review:\n\nGreet people") {
		t.Errorf("the review lost its framing of the task:\n%s", got)
	}
	// A task's own text is never read for variables: filling is one pass.
	in.Goal = "Mention {{run.branch}} literally."
	if got := Prompt(PhaseImplement, in); !strings.Contains(got, "**Goal:** Mention {{run.branch}} literally.") {
		t.Errorf("a goal's braces were expanded:\n%s", got)
	}
}
