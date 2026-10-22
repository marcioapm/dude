package delivery

import (
	"encoding/json"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// These pure functions are all that stands between a review → fix cycle and
// an unbounded spend, so the tests worth having pin termination.

var policy = DefaultPolicy()

func finding(severity, status string, attempts int, id string) FindingState {
	return FindingState{ID: id, Severity: severity, Status: status, FixAttempts: attempts}
}

func TestOnlyOpenFindingsOfABlockingSeverityBlock(t *testing.T) {
	for sev, want := range map[string]bool{"blocking": true, "high": true, "medium": false, "note": false} {
		if got := IsBlocking(policy, finding(sev, "open", 0, "f")); got != want {
			t.Errorf("%s: %v, want %v", sev, got, want)
		}
	}
	// Fixed, moot, or a person decided to ship it: none hold up the task.
	for _, status := range []string{"resolved", "superseded", "accepted"} {
		if IsBlocking(policy, finding("blocking", status, 0, "f")) {
			t.Errorf("a %s finding blocked", status)
		}
	}
}

func TestPolicyDecidesNotSeverity(t *testing.T) {
	strict := policy
	strict.BlockingSeverities = []string{"blocking", "high", "medium", "low", "note"}
	if !IsBlocking(strict, finding("note", "open", 0, "f")) {
		t.Error("a strict policy did not block a note")
	}
	lax := policy
	lax.BlockingSeverities = nil
	if IsBlocking(lax, finding("blocking", "open", 0, "f")) {
		t.Error("a lax policy blocked")
	}
}

func TestLoopExits(t *testing.T) {
	if e := Exit(policy, nil, 1); e == nil || e.Reason != "clear" {
		t.Errorf("no findings: %+v, want clear — a review that found nothing is the good case", e)
	}
	if e := Exit(policy, []FindingState{finding("medium", "open", 0, "a"), finding("blocking", "resolved", 0, "b")}, 1); e == nil || e.Reason != "clear" {
		t.Errorf("nothing blocking: %+v", e)
	}
	if e := Exit(policy, []FindingState{finding("blocking", "open", 0, "a")}, 1); e != nil {
		t.Errorf("blocking early: %+v, want keep going", e)
	}
	if e := Exit(policy, []FindingState{finding("blocking", "open", 0, "a")}, policy.MaxReviewIterations); e == nil || e.Reason != "exhausted" {
		t.Errorf("at the bound: %+v", e)
	}
}

func TestAStubbornFindingEscalatesBeforeTheLoopBudget(t *testing.T) {
	e := Exit(policy, []FindingState{
		finding("blocking", "open", policy.MaxAttemptsPerFinding, "f_stuck"),
		finding("blocking", "open", 0, "f_fresh"),
	}, 1)
	if e == nil || e.Reason != "stuck" || !slices.Equal(e.FindingIDs, []string{"f_stuck"}) {
		t.Fatalf("exit = %+v", e)
	}
	// Attempts spent on something since resolved must not strand the work.
	if e := Exit(policy, []FindingState{finding("blocking", "resolved", 9, "x")}, 1); e == nil || e.Reason != "clear" {
		t.Errorf("resolved stubborn finding: %+v", e)
	}
}

// "It terminates" is a claim about all inputs, so it is simulated.
func TestEveryLoopTerminatesWithinItsBound(t *testing.T) {
	findings := []FindingState{finding("blocking", "open", 0, "a")}
	iteration := 0
	for Exit(policy, findings, iteration) == nil {
		iteration++
		if iteration > policy.MaxReviewIterations {
			t.Fatal("the loop outlived its bound")
		}
	}
	if iteration != policy.MaxReviewIterations {
		t.Errorf("stopped at %d, want %d", iteration, policy.MaxReviewIterations)
	}
}

func TestConvergenceExits(t *testing.T) {
	criterion := FindingState{ID: "criterion", Severity: "high", Status: "open", HasCriterion: true, StillRounds: 2, FixAttempts: 2}
	regression := FindingState{ID: "regression", Severity: "high", Status: "open"}
	for _, tc := range []struct {
		name      string
		mutate    func(*Policy, *FindingState)
		conv      Convergence
		iteration int
		want      string
		ids       []string
	}{
		{name: "criterion beats stuck and exhausted", iteration: 5, want: "not_converging", ids: []string{"criterion"}},
		{name: "criterion below bound", mutate: func(p *Policy, f *FindingState) { f.StillRounds = 1; f.FixAttempts = 0 }},
		{name: "criterion above bound", mutate: func(p *Policy, f *FindingState) { f.StillRounds = 3 }, want: "not_converging", ids: []string{"criterion"}},
		{name: "criterion absent", mutate: func(p *Policy, f *FindingState) { f.HasCriterion = false }, want: "stuck", ids: []string{"criterion"}},
		{name: "still disabled", mutate: func(p *Policy, f *FindingState) { p.MaxStillRounds = 0 }, want: "stuck", ids: []string{"criterion"}},
		{name: "resolved is clear", mutate: func(p *Policy, f *FindingState) { f.Status = "resolved" }, want: "clear"},
		{name: "medium nonblocking", mutate: func(p *Policy, f *FindingState) { f.Severity = "medium" }, want: "clear"},
		{name: "medium dynamically blocking", mutate: func(p *Policy, f *FindingState) { f.Severity = "medium"; p.BlockingSeverities = []string{"medium"} }, want: "not_converging", ids: []string{"criterion"}},
		{name: "human excluded", mutate: func(p *Policy, f *FindingState) { f.Human = true }, conv: Convergence{RegressionRounds: 2, FindingIDs: []string{"criterion"}}, want: "stuck"},
		{name: "regression at bound", mutate: func(p *Policy, f *FindingState) { *f = regression }, conv: Convergence{RegressionRounds: 2, FindingIDs: []string{"regression", "missing"}}, want: "not_converging", ids: []string{"regression"}},
		{name: "regression below bound", mutate: func(p *Policy, f *FindingState) { *f = regression }, conv: Convergence{RegressionRounds: 1, FindingIDs: []string{"regression"}}},
		{name: "regression disabled", mutate: func(p *Policy, f *FindingState) { *f = regression; p.MaxRegressionRounds = 0 }, conv: Convergence{RegressionRounds: 9, FindingIDs: []string{"regression"}}},
		{name: "clear beats regression", mutate: func(p *Policy, f *FindingState) { f.Status = "accepted" }, conv: Convergence{RegressionRounds: 2, FindingIDs: []string{"criterion"}}, want: "clear"},
		{name: "streak map is caller derived", mutate: func(p *Policy, f *FindingState) { f.StillRounds = 0; f.FixAttempts = 0 }, conv: Convergence{StillRounds: map[string]int{"criterion": 9}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, f := DefaultPolicy(), criterion
			if tc.mutate != nil {
				tc.mutate(&p, &f)
			}
			got := Exit(p, []FindingState{f}, tc.iteration, tc.conv)
			if tc.want == "" {
				if got != nil {
					t.Fatalf("exit = %+v, want continue", got)
				}
				return
			}
			if got == nil || got.Reason != tc.want || !slices.Equal(got.FindingIDs, tc.ids) {
				t.Fatalf("exit = %+v, want %s %v", got, tc.want, tc.ids)
			}
			if tc.want == "not_converging" && got.RegressionRounds != tc.conv.RegressionRounds {
				t.Fatalf("regression rounds = %d", got.RegressionRounds)
			}
			if f.Human && !slices.Equal(got.FeedbackIDs, []string{f.ID}) {
				t.Fatalf("human feedback lost: %+v", got)
			}
		})
	}
}

func TestConvergenceCombinesFindingsWithoutDuplicates(t *testing.T) {
	findings := []FindingState{
		{ID: "both", Severity: "high", Status: "open", HasCriterion: true, StillRounds: 2},
		{ID: "regression", Severity: "high", Status: "open"},
		{ID: "human", Human: true, Status: "open", HasCriterion: true, StillRounds: 2},
		{ID: "resolved", Severity: "high", Status: "resolved"},
	}
	got := Exit(DefaultPolicy(), findings, 5, Convergence{RegressionRounds: 3, FindingIDs: []string{"both", "regression", "human", "resolved"}})
	if got == nil || got.Reason != "not_converging" || !slices.Equal(got.FindingIDs, []string{"both", "regression"}) || len(got.FeedbackIDs) != 0 {
		t.Fatalf("exit = %+v", got)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["regressionRounds"] != float64(3) {
		t.Fatalf("exit JSON = %s", raw)
	}
}

func TestConvergencePolicyFrozenCompatibility(t *testing.T) {
	p := DefaultPolicy()
	if p.MaxStillRounds != 2 || p.MaxRegressionRounds != 2 {
		t.Fatalf("defaults = %+v", p)
	}
	var frozen Policy
	if err := json.Unmarshal([]byte(`{"blockingSeverities":["high"],"maxAttemptsPerFinding":4,"maxReviewIterations":5}`), &frozen); err != nil {
		t.Fatal(err)
	}
	if frozen.MaxStillRounds != 0 || frozen.MaxRegressionRounds != 0 {
		t.Fatalf("old frozen policy enabled convergence: %+v", frozen)
	}
	f := FindingState{ID: "old", Severity: "high", Status: "open", HasCriterion: true, StillRounds: 9}
	if got := Exit(frozen, []FindingState{f}, 1, Convergence{RegressionRounds: 9, FindingIDs: []string{"old"}}); got != nil {
		t.Fatalf("old delivery exited: %+v", got)
	}
	resolved, err := ResolvePolicy(json.RawMessage(`{"maxStillRounds":0,"maxRegressionRounds":0}`))
	if err != nil {
		t.Fatal(err)
	}
	if resolved.MaxStillRounds != 0 || resolved.MaxRegressionRounds != 0 {
		t.Fatalf("explicit zero overwritten: %+v", resolved)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var roundTrip Policy
	if err := json.Unmarshal(raw, &roundTrip); err != nil {
		t.Fatal(err)
	}
	if roundTrip.MaxStillRounds != 2 || roundTrip.MaxRegressionRounds != 2 {
		t.Fatalf("round trip = %+v", roundTrip)
	}
}

func TestAPathRuleMatchesWithinEachRepository(t *testing.T) {
	// Changed paths arrive as <repo>/<path>: a project's rule names paths in
	// its repositories, or a repository.
	p := policy
	p.ConditionalReviewers = []ReviewerRule{
		{Category: "database", WhenPathsMatch: []string{"migrations/**"}},
		{Category: "frontend", WhenPathsMatch: []string{"web/**"}},
	}
	if got := ReviewersFor(p, []string{"api/migrations/019_x.sql"}); !slices.Contains(got, "database") {
		t.Errorf("a migration in api did not summon database: %v", got)
	}
	if got := ReviewersFor(p, []string{"web/src/app.tsx"}); !slices.Contains(got, "frontend") {
		t.Errorf("a change in web did not summon frontend: %v", got)
	}
	if got := ReviewersFor(p, []string{"api/src/app.go"}); slices.Contains(got, "database") || slices.Contains(got, "frontend") {
		t.Errorf("api code summoned %v", got)
	}
}

func TestReviewerSelection(t *testing.T) {
	if got := ReviewersFor(policy, []string{"README.md"}); !slices.Equal(got, []string{"correctness", "tests", "performance"}) {
		t.Errorf("README: %v", got)
	}
	if got := ReviewersFor(policy, []string{"src/styles/app.css"}); slices.Contains(got, "security") {
		t.Errorf("css summoned security: %v", got)
	}
	if got := ReviewersFor(policy, []string{"src/auth/session.ts"}); !slices.Contains(got, "security") {
		t.Errorf("auth did not summon security: %v", got)
	}
	if got := ReviewersFor(policy, []string{"migrations/011_pull_requests.sql"}); !slices.Contains(got, "database") {
		t.Errorf("migration did not summon database: %v", got)
	}
	got := ReviewersFor(policy, []string{"src/auth/login.tsx", "migrations/012_phases.sql", "src/api/routes/work.ts"})
	if !slices.Equal(got, []string{"correctness", "tests", "security", "database", "api", "frontend", "performance"}) {
		t.Errorf("several: %v", got)
	}
	paths := []string{"src/auth/x.ts", "migrations/y.sql"}
	if a, b := ReviewersFor(policy, paths), ReviewersFor(policy, []string{paths[1], paths[0]}); !slices.Equal(a, b) {
		t.Errorf("order depends on input order: %v vs %v", a, b)
	}
}

func TestGlobs(t *testing.T) {
	for _, c := range []struct {
		pattern, path string
		want          bool
	}{
		{"**/auth/**", "apps/web/src/auth/login.ts", true},
		{"**/*.sql", "migrations/001_initial.sql", true},
		{"src/*.ts", "src/index.ts", true},
		// A single star must not swallow a separator, or src/*.ts would
		// match every file in the tree.
		{"src/*.ts", "src/nested/index.ts", false},
		// A leading **/ also matches at the root.
		{"**/migrations/**", "migrations/001.sql", true},
		// A dot is literal.
		{"**/*.sql", "migrations/001xsql", false},
	} {
		if got := MatchesGlob(c.pattern, c.path); got != c.want {
			t.Errorf("MatchesGlob(%q, %q) = %v, want %v", c.pattern, c.path, got, c.want)
		}
	}
}

// The categories a project may require are @dude/domain's
// REVIEWER_CATEGORIES; the ones the orchestrator runs and the conductor may
// start are reviewerOrder. Nothing generates one from the other, so a
// category added on one side only fails here, as does a change of order.
func TestTheReviewerCatalogueIsTheDomainPackages(t *testing.T) {
	raw, err := os.ReadFile("../../../packages/domain/src/hierarchy.ts")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`REVIEWER_CATEGORIES = \[([^\]]*)\] as const`).FindSubmatch(raw)
	if m == nil {
		t.Fatal("REVIEWER_CATEGORIES not found in hierarchy.ts")
	}
	var ts []string
	for _, q := range regexp.MustCompile(`"([^"]+)"`).FindAllSubmatch(m[1], -1) {
		ts = append(ts, string(q[1]))
	}
	if !slices.Equal(ts, reviewerOrder) {
		t.Errorf("REVIEWER_CATEGORIES = %v, reviewerOrder = %v", ts, reviewerOrder)
	}
	for _, c := range reviewerOrder {
		if reviewFocus[c] == "" {
			t.Errorf("reviewer %q has no focus in its prompt", c)
		}
	}
	if len(reviewFocus) != len(reviewerOrder) {
		t.Errorf("reviewFocus has %d categories, the catalogue %d", len(reviewFocus), len(reviewerOrder))
	}
}

// Every reviewer costs a model run on each delivery its rules match, so
// documentation runs only where a project or organisation requires it:
// the factory's policy selects it for no change, its own docs included.
func TestTheFactoryNeverSelectsTheDocumentationReviewer(t *testing.T) {
	for _, paths := range [][]string{
		nil,
		{"README.md"},
		{"docs/guide.md", "CHANGELOG.md"},
		{"web/src/settings.tsx", "api/routes/settings.ts", "cli/main.go", "migrations/001.sql", "src/auth/x.ts"},
	} {
		if got := ReviewersFor(DefaultPolicy(), paths); slices.Contains(got, "documentation") {
			t.Errorf("%v: the default policy selected %v", paths, got)
		}
	}
}

func TestARequiredDocumentationReviewerRunsInCatalogueOrderWithItsOwnAspect(t *testing.T) {
	p, err := ResolvePolicy(json.RawMessage(`{"requiredReviewers":["performance","documentation","correctness"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := ReviewersFor(p, []string{"lib/invoice.go"}); !slices.Equal(got, []string{"correctness", "documentation", "performance"}) {
		t.Errorf("required documentation: %v", got)
	}
	// The Run's instruction file carries the documentation aspect, not the
	// correctness one an unknown category falls back to.
	text := Instructions("reviewer", "documentation", InstructionsInput{Tools: true})
	for c, focus := range reviewFocus {
		want := 0
		if c == "documentation" {
			want = 1
		}
		if got := strings.Count(text, focus); got != want {
			t.Errorf("the documentation reviewer's instructions hold the %s aspect %d times, want %d", c, got, want)
		}
	}
}

// The factory's required reviewers run on every change, in the
// catalogue's order; a project that names its own keeps them.
func TestTheFactoryRequiresCorrectnessTestsAndPerformance(t *testing.T) {
	if got := DefaultPolicy().RequiredReviewers; !slices.Equal(got, []string{"correctness", "tests", "performance"}) {
		t.Errorf("default required reviewers = %v", got)
	}
	own, err := ResolvePolicy(json.RawMessage(`{"requiredReviewers":["security"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := ReviewersFor(own, []string{"README.md"}); !slices.Equal(got, []string{"security"}) {
		t.Errorf("a project's own required reviewers: %v", got)
	}
}
