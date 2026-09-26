package delivery

import (
	"slices"
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
	if got := ReviewersFor(policy, []string{"README.md"}); !slices.Equal(got, []string{"correctness"}) {
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
	if !slices.Equal(got, []string{"correctness", "security", "database", "api", "frontend"}) {
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
