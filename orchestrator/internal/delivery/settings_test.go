package delivery

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestARolesNoProgressLimitIsReadAtHalfAnHourAtLeast(t *testing.T) {
	for stored, want := range map[int]int{0: DefaultTimeLimitMinutes, 10: 30, 30: 30, 45: 45} {
		if got := (RoleSettings{TimeLimitMinutes: stored}).TimeLimit(); got != want {
			t.Errorf("stored %d reads as %d, want %d", stored, got, want)
		}
	}
}

func TestARoleResolvesFieldByFieldProjectThenOrganization(t *testing.T) {
	org := json.RawMessage(`{"reviewer":{"tier":"mtr_thinker","timeLimitMinutes":20},
		"implementer":{"tier":"mtr_coder","context":"org notes"}}`)
	project := json.RawMessage(`{"reviewer":{"timeLimitMinutes":45},"implementer":{"tier":"mtr_fast"}}`)

	// The project changed only the reviewer's time limit: the tier is still
	// its organization's.
	if got, want := ResolveRole("reviewer", project, org), (RoleSettings{Tier: "mtr_thinker", TimeLimitMinutes: 45}); got != want {
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

// A role's harness layers like its tier: the project's, then the
// organization's, the fixer following the implementer; none is OpenCode,
// and a value that names no harness is passed over.
func TestARolesHarnessIsLayeredAndOpenCodeByDefault(t *testing.T) {
	org := json.RawMessage(`{"implementer":{"harness":"codex"},"reviewer":{"harness":"claude-code"}}`)
	project := json.RawMessage(`{"reviewer":{"harness":"opencode"},"simplifier":{"harness":"aider"}}`)
	for role, want := range map[string]string{"reviewer": HarnessOpenCode, "implementer": HarnessCodex, "fixer": HarnessCodex, "simplifier": HarnessOpenCode, "conductor": HarnessOpenCode} {
		if got := ResolveRole(role, project, org).HarnessName(); got != want {
			t.Errorf("%s = %s, want %s", role, got, want)
		}
	}
	if got := ResolveRole("reviewer", nil, org).HarnessName(); got != HarnessClaudeCode {
		t.Errorf("reviewer without the project = %s", got)
	}
}

func TestTheFixerIsTheImplementerUnlessSetApart(t *testing.T) {
	org := json.RawMessage(`{"implementer":{"tier":"mtr_coder","context":"coder notes"}}`)
	if got := ResolveRole("fixer", nil, org); got.Tier != "mtr_coder" || got.Context != "coder notes" {
		t.Errorf("fixer = %+v, want the implementer's", got)
	}
	project := json.RawMessage(`{"fixer":{"context":"fixer notes"}}`)
	if got := ResolveRole("fixer", project, org); got.Tier != "mtr_coder" || got.Context != "fixer notes" {
		t.Errorf("fixer with its own notes = %+v", got)
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

// What happens before the pull request is a project's setting: Open it by
// default, the project's value over it, and an organisation's stored value
// passed over.
func TestThePullRequestChoiceIsTheProjectsAndNeverTheOrganisations(t *testing.T) {
	if p, err := ResolvePolicy(nil, nil); err != nil || p.PullRequest != PullRequestOpen {
		t.Fatalf("default = %q, %v; want open", p.PullRequest, err)
	}
	p, err := ResolvePolicy(json.RawMessage(`{"pullRequest":"ask","maxReviewIterations":3}`), nil)
	if err != nil || p.PullRequest != PullRequestOpen || p.MaxReviewIterations != 3 {
		t.Fatalf("an organisation's ask = %q (%v), max %d; want open and the rest of its layer kept", p.PullRequest, err, p.MaxReviewIterations)
	}
	if p, _ := ResolvePolicy(json.RawMessage(`{"pullRequest":"ask"}`), json.RawMessage(`{"pullRequest":"draft"}`)); p.PullRequest != PullRequestDraft {
		t.Fatalf("the project's draft = %q", p.PullRequest)
	}
	// The task's own layer is over the project's.
	if p, _ := ResolvePolicy(nil, json.RawMessage(`{"pullRequest":"draft"}`), json.RawMessage(`{"pullRequest":"ask"}`)); p.PullRequest != PullRequestAsk {
		t.Fatalf("the task's ask = %q", p.PullRequest)
	}
	if _, err := ResolvePolicy(nil, json.RawMessage(`{"pullRequest":"later"}`)); err == nil {
		t.Fatal("pullRequest later was accepted")
	}
	_, from, err := PolicySources(json.RawMessage(`{"pullRequest":"ask"}`), json.RawMessage(`{}`))
	if err != nil || from["pullRequest"] != SetInDefault {
		t.Fatalf("an organisation's stored value is its source: %q %v", from["pullRequest"], err)
	}
}

func TestAnOrganizationsPromptReplacesTheBuiltInOnly(t *testing.T) {
	in := PromptInput{Title: "Greet people", Context: "Use bun.", Tools: true, CLI: true}
	builtin := Instructions("implementer", "", in.InstructionsInput())
	if !strings.Contains(builtin, BuiltinPrompt("implementer")) {
		t.Fatalf("the built-in implementer stance is missing:\n%s", builtin)
	}

	org := "Write the change and its tests."
	in.OrgPrompt = &org
	got := Instructions("implementer", "", in.InstructionsInput())
	if strings.Contains(got, BuiltinPrompt("implementer")) {
		t.Errorf("the built-in stance survived the organization's:\n%s", got)
	}
	// dude's own sections are kept whatever the saved prompt says: how to
	// publish, the role's hand-back, the project's notes, last.
	for _, want := range []string{org, "dude publish FILE", "## What you hand back", "# Notes for your role\n\n_Set in the project's settings for this role._\n\nUse bun."} {
		if !strings.Contains(got, want) {
			t.Errorf("instructions lack %q:\n%s", want, got)
		}
	}
	if !strings.HasSuffix(got, "Use bun.\n") {
		t.Errorf("the project's notes are not last:\n%s", got)
	}
	// The first message carries the task, and no saved prompt.
	if msg := Prompt(PhaseImplement, in); !strings.Contains(msg, "Greet people") || strings.Contains(msg, org) || strings.Contains(msg, "Use bun.") {
		t.Errorf("first message:\n%s", msg)
	}
}

func TestAProjectAddsToOrReplacesTheOrganizationsPrompt(t *testing.T) {
	org := "ORG INSTRUCTIONS"
	base := InstructionsInput{OrgPrompt: &org}

	add := base
	add.ProjectPrompt, add.ProjectPromptMode = "PROJECT ADDITION", "add"
	got := Instructions("simplifier", "", add)
	if i, j := strings.Index(got, org), strings.Index(got, "PROJECT ADDITION"); i < 0 || j < i {
		t.Errorf("add: the project's prompt should follow the organization's:\n%s", got)
	}

	replace := base
	replace.ProjectPrompt, replace.ProjectPromptMode = "PROJECT ONLY", "replace"
	got = Instructions("simplifier", "", replace)
	if strings.Contains(got, org) || !strings.Contains(got, "PROJECT ONLY") {
		t.Errorf("replace: the organization's prompt should be gone:\n%s", got)
	}

	// Added to dude's own when the organization has none.
	builtin := InstructionsInput{ProjectPrompt: "PROJECT ADDITION", ProjectPromptMode: "add"}
	got = Instructions("simplifier", "", builtin)
	if !strings.Contains(got, BuiltinPrompt("simplifier")) || !strings.Contains(got, "PROJECT ADDITION") {
		t.Errorf("add over the built-in:\n%s", got)
	}

	// An empty addition is "use the organization's as is".
	same := base
	same.ProjectPromptMode = "add"
	if Instructions("simplifier", "", same) != Instructions("simplifier", "", base) {
		t.Error("an empty addition changed the instructions")
	}
}

func TestAReviewerKeepsItsCategoryAndFormatUnderACustomPrompt(t *testing.T) {
	custom := "Be terse."
	in := PromptInput{Title: "T", Category: "security", OrgPrompt: &custom, BlockingSeverities: []string{"high"}}
	got := Instructions("reviewer", "security", in.InstructionsInput())
	for _, want := range []string{reviewFocus["security"], custom, "severity: blocking | high", "`high`"} {
		if !strings.Contains(got, want) {
			t.Errorf("reviewer instructions lack %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, BuiltinPrompt("reviewer")) {
		t.Errorf("the built-in reviewer stance survived:\n%s", got)
	}
	if msg := Prompt(PhaseReview, in); !strings.Contains(msg, "for **security**") {
		t.Errorf("the first message does not name the category:\n%s", msg)
	}
}

// The findings a fixer is given are its first message; how it fixes, and
// its saved prompt, are its instruction file, before them in the context.
func TestTheFixersFeedbackSitsBetweenItsBuiltInInstructions(t *testing.T) {
	in := PromptInput{Title: "T", Findings: []Finding{{Severity: "high", Title: "Broken"}}}
	msg := Prompt(PhaseFix, in)
	if !strings.HasPrefix(msg, "## Review findings") || strings.Contains(msg, "Fix what was raised") {
		t.Errorf("fixer message:\n%s", msg)
	}
	file := Instructions("fixer", "", in.InstructionsInput())
	stance, fix := strings.Index(file, "Fix what was raised"), strings.Index(file, fixContract)
	if stance < 0 || fix < stance || strings.Contains(file, "Broken") {
		t.Errorf("fixer instructions: stance=%d fix=%d\n%s", stance, fix, file)
	}
	custom := "Fix it."
	in.OrgPrompt = &custom
	file = Instructions("fixer", "", in.InstructionsInput())
	if strings.Contains(file, "Fix what was raised") || !strings.Contains(file, custom) || !strings.Contains(file, fixContract) {
		t.Errorf("custom fixer instructions:\n%s", file)
	}
	if Prompt(PhaseFix, in) != msg {
		t.Error("a saved prompt changes the fixer's message")
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
	for role, note := range map[string]string{"implementer": commitNote, "fixer": commitNote, "simplifier": commitNote, "qa_browser": testerTools} {
		org := InstructionsInput{OrgPrompt: &mine}
		replaced := InstructionsInput{ProjectPrompt: mine, ProjectPromptMode: "replace"}
		for _, in := range []InstructionsInput{{}, org, replaced} {
			if got := Instructions(role, "", in); strings.Count(got, note) != 1 {
				t.Errorf("%s (saved %v) should say %q once:\n%s", role, in.OrgPrompt != nil || in.ProjectPrompt != "", note, got)
			}
		}
	}
}

// A saved prompt's variables are filled in where it names them, and the
// task still follows in full in the first message, however much of it the
// prompt names.
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
		file := Instructions(PromptRoleForPhase[phase], "", in.InstructionsInput())
		for _, want := range []string{"**Goal:** Say hello to whoever arrives.", "Work on `dude/tsk_1/run-1`, from `main`.", "Keep {{unknown.thing}} as it is."} {
			if !strings.Contains(file, want) {
				t.Errorf("%s: instructions lack %q:\n%s", phase, want, file)
			}
		}
		// The prompt named the goal only: the rest of the task is not lost.
		msg := Prompt(phase, in)
		for _, want := range []string{"Greet people", "Acceptance criteria:\n- It greets\n- It has a test", "Q: Which language?\n  A: English"} {
			if !strings.Contains(msg, want) {
				t.Errorf("%s: message lacks %q:\n%s", phase, want, msg)
			}
		}
	}
	if got := Prompt(PhaseReview, in); !strings.Contains(got, "The task under review:\n\nGreet people") {
		t.Errorf("the review lost its framing of the task:\n%s", got)
	}
	// A task's own text is never read for variables: filling is one pass.
	in.Goal = "Mention {{run.branch}} literally."
	if got := Instructions("implementer", "", in.InstructionsInput()); !strings.Contains(got, "**Goal:** Mention {{run.branch}} literally.") {
		t.Errorf("a goal's braces were expanded:\n%s", got)
	}
}

// How fixes reach an open pull request is a project's setting, direct for
// every project's new delivery unless it chose candidate; an organisation's
// value is passed over. A delivery's frozen policy without the key is one
// from before it existed: it stays candidate.
func TestDirectFixesIsAProjectOnlySettingFrozenPerDelivery(t *testing.T) {
	p, err := ResolvePolicy(json.RawMessage(`{"pullRequestFixes":"candidate"}`), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := p.ForNewDelivery(); !got.DirectFixes() {
		t.Errorf("an organisation's candidate was read: %q", got.PullRequestFixes)
	}
	p, _ = ResolvePolicy(nil, json.RawMessage(`{"pullRequestFixes":"candidate"}`))
	if got := p.ForNewDelivery(); got.DirectFixes() || got.PullRequestFixes != PullRequestFixesCandidate {
		t.Errorf("the project's candidate = %q", got.PullRequestFixes)
	}
	if _, err := ResolvePolicy(nil, json.RawMessage(`{"pullRequestFixes":"later"}`)); err == nil {
		t.Error("pullRequestFixes later was accepted")
	}
	if _, from, _ := PolicySources(json.RawMessage(`{"pullRequestFixes":"candidate"}`), json.RawMessage(`{}`)); from["pullRequestFixes"] != SetInDefault {
		t.Errorf("an organisation's stored value is its source: %q", from["pullRequestFixes"])
	}
	if slices.Contains(LivePolicyKeys, "pullRequestFixes") {
		t.Error("pullRequestFixes is read live: a running delivery would change how it fixes")
	}
	// A delivery frozen before the key: candidate, until it ends.
	var frozen State
	if err := json.Unmarshal([]byte(`{"taskId":"t","policy":{"maxReviewIterations":5}}`), &frozen); err != nil {
		t.Fatal(err)
	}
	if frozen.Policy.DirectFixes() {
		t.Error("a frozen policy without pullRequestFixes reads as direct")
	}
	if b := frozen.Policy.prFixReviewBound(); b != 2 {
		t.Errorf("the narrow loop's bound of a policy without it = %d, want 2", b)
	}
	if d := DefaultPolicy(); d.MaxPRFixReviewIterations != 2 {
		t.Errorf("maxPrFixReviewIterations default %d", d.MaxPRFixReviewIterations)
	}
}

// Every project, new and existing (one whose policy names nothing), starts
// a new delivery fixing its pull requests directly.
func TestEveryProjectResolvesDirectForANewDelivery(t *testing.T) {
	for _, layers := range [][]json.RawMessage{{nil, nil}, {json.RawMessage(`{}`), json.RawMessage(`{}`)},
		{json.RawMessage(`{"maxReviewIterations":3}`), json.RawMessage(`{"pullRequest":"ask"}`)}} {
		p, err := ResolvePolicy(layers...)
		if err != nil || !p.ForNewDelivery().DirectFixes() {
			t.Errorf("%s: %q %v", layers, p.ForNewDelivery().PullRequestFixes, err)
		}
	}
}
