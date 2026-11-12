package delivery

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// pinnedFacts gives GLD (prj_g) two pinned facts — one Ana pinned, one its
// conductor did — an unpinned one, an archived pinned one, and a pinned
// fact of OPS (prj_o): only GLD's two live pinned ones are GLD's facts.
func (w goldenWorld) pinnedFacts(t *testing.T) {
	w.exec(t, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, role, kind)
		VALUES ('run_pincond', $1, 'prj_g', 'wi_g', 1, 'completed', 'conductor', 'agent')`)
	w.exec(t, `INSERT INTO memories (id, organization_id, project_id, title, content, author_kind, author_person_id, created_by_run_id, system_reason,
			pinned_at, pinned_by_person_id, pinned_by_run_id, archived_at, created_at, updated_at)
		VALUES
		('mem_flaky', $1, 'prj_g', 'TestMemo is flaky on main', 'Rerun it once
before blaming the change.', 'person', 'per_ana', NULL, NULL, '2026-01-01 09:00Z', 'per_ana', NULL, NULL, '2026-01-01 08:00Z', '2026-01-01 09:00Z'),
		('mem_github', $1, 'prj_g', 'Never touch .github without asking', 'Ask the owner first.', 'agent', NULL, 'run_pincond', NULL,
			'2026-01-01 10:00Z', NULL, 'run_pincond', NULL, '2026-01-01 10:00Z', '2026-01-01 10:00Z'),
		('mem_unpinned', $1, 'prj_g', 'Unpinned: the API pages by cursor', 'x', 'system', NULL, NULL, 'test',
			NULL, NULL, NULL, NULL, '2026-01-01 10:00Z', '2026-01-01 10:00Z'),
		('mem_archived', $1, 'prj_g', 'Archived: the old mirror', 'x', 'system', NULL, NULL, 'test',
			'2026-01-01 10:00Z', 'per_ana', NULL, '2026-01-01 11:00Z', '2026-01-01 10:00Z', '2026-01-01 11:00Z'),
		('mem_ops', $1, 'prj_o', 'Elsewhere: OPS deploys on Fridays', 'x', 'system', NULL, NULL, 'test',
			'2026-01-01 10:00Z', 'per_ana', NULL, NULL, '2026-01-01 10:00Z', '2026-01-01 10:00Z')`)
}

// A conductor's briefing carries the project's pinned facts, each with its
// title, text and who pinned it, oldest pin first; and no unpinned,
// archived or other project's fact.
func TestAConductorsBriefingCarriesTheProjectsPinnedFacts(t *testing.T) {
	w := newGoldenWorld(t)
	w.pinnedFacts(t)
	got := w.in(t, briefWith(byPerson, "Ana", "Go on."))
	flaky, github := strings.Index(got, "**TestMemo is flaky on main** (mem_flaky, pinned by Ana): Rerun it once\n  before blaming the change."),
		strings.Index(got, "**Never touch .github without asking** (mem_github, pinned by the conductor): Ask the owner first.")
	if !strings.Contains(got, "## Project facts") || flaky < 0 || github < 0 || flaky > github {
		t.Errorf("the briefing's project facts, oldest pin first:\n%s", got)
	}
	for _, never := range []string{"Unpinned:", "Archived:", "Elsewhere:"} {
		if strings.Contains(got, never) {
			t.Errorf("the briefing carries %q:\n%s", never, got)
		}
	}
	if epic := strings.Index(got, "## On this epic"); epic >= 0 && epic < github {
		t.Errorf("the facts come after the epic")
	}
	golden(t, "project-facts", w.in(t, func(ctx context.Context, tx pgx.Tx) (string, error) { return ProjectFacts(ctx, tx, "prj_g") }))

	// Unpinned, it is no longer there; with none pinned, no section.
	w.exec(t, `UPDATE memories SET pinned_at = NULL, pinned_by_person_id = NULL, pinned_by_run_id = NULL WHERE id = 'mem_flaky' AND organization_id = $1`)
	if got := w.in(t, briefWith(byPerson, "Ana", "Go on.")); strings.Contains(got, "TestMemo") {
		t.Errorf("an unpinned fact is still briefed")
	}
	w.exec(t, `UPDATE memories SET pinned_at = NULL, pinned_by_person_id = NULL, pinned_by_run_id = NULL WHERE organization_id = $1`)
	if got := w.in(t, briefWith(byPerson, "Ana", "Go on.")); strings.Contains(got, "Project facts") {
		t.Errorf("a project with no pinned fact has the section")
	}
}

// A brainstorm session's memory cannot be pinned, nor the organization's,
// nor a note: the database refuses it, whatever writes it.
func TestOnlyAProjectsFactCanBePinnedInTheDatabase(t *testing.T) {
	w := newGoldenWorld(t)
	w.exec(t, `INSERT INTO sessions (id, organization_id, title, titled_by) VALUES ('ses_p', $1, 'S', 'person')`)
	for name, row := range map[string]string{
		"session":      `'ses_p', NULL, 'fact'`,
		"organization": `NULL, NULL, 'fact'`,
		"note":         `NULL, 'prj_g', 'note'`,
	} {
		_, err := w.owner.Exec(context.Background(), `INSERT INTO memories (id, organization_id, session_id, project_id, kind, title, content, author_kind, system_reason, pinned_at)
			VALUES ('mem_`+name+`', $1, `+row+`, 't', 'x', 'system', 'test', now())`, w.org)
		if err == nil || !strings.Contains(err.Error(), "memories_pinned_project_fact") {
			t.Errorf("a pinned %s memory: %v", name, err)
		}
	}
}

// Over the bound the most recently confirmed or edited facts are kept,
// shown oldest pin first, and the section says how many more are pinned.
func TestOverTheBoundTheMostRecentFactsAreKeptAndTheRestCounted(t *testing.T) {
	w := newGoldenWorld(t)
	n := ProjectFactsMax + 3
	for i := range n {
		// Pinned in order; fact 0 edited last, so it is among the most recent.
		updated := fmt.Sprintf("2026-01-02 10:%02d:00Z", i)
		if i == 0 {
			updated = "2026-01-03 00:00:00Z"
		}
		w.exec(t, `INSERT INTO memories (id, organization_id, project_id, title, content, author_kind, system_reason, pinned_at, pinned_by_person_id, updated_at)
			VALUES ($2, $1, 'prj_g', $3, 'x', 'system', 'test', $4::timestamptz, 'per_ana', $5::timestamptz)`,
			fmt.Sprintf("mem_%02d", i), fmt.Sprintf("Fact %02d", i), fmt.Sprintf("2026-01-02 10:%02d:00Z", i), updated)
	}
	got := w.in(t, func(ctx context.Context, tx pgx.Tx) (string, error) { return ProjectFacts(ctx, tx, "prj_g") })
	if c := strings.Count(got, "\n- **"); c != ProjectFactsMax {
		t.Errorf("%d facts shown, want %d", c, ProjectFactsMax)
	}
	// Dropped: the three oldest not edited since (1, 2, 3); kept: 0, edited last.
	for _, dropped := range []string{"Fact 01", "Fact 02", "Fact 03"} {
		if strings.Contains(got, dropped) {
			t.Errorf("%s kept over a more recent one", dropped)
		}
	}
	if !strings.Contains(got, "Fact 00") || strings.Index(got, "Fact 00") > strings.Index(got, "Fact 04") {
		t.Errorf("the recently edited Fact 00 is not kept first:\n%s", got)
	}
	if !strings.Contains(got, "3 more are pinned than fit here") {
		t.Errorf("no count of what was left out:\n%s", got)
	}
	golden(t, "project-facts-bounded", got)

	// The bytes bound: long facts stop it sooner, and it still counts.
	w.exec(t, `UPDATE memories SET content = repeat('long ', 200) WHERE organization_id = $1`)
	got = w.in(t, func(ctx context.Context, tx pgx.Tx) (string, error) { return ProjectFacts(ctx, tx, "prj_g") })
	shown := strings.Count(got, "\n- **")
	if len(got) > projectFactsBytes+1000 || shown >= ProjectFactsMax || !strings.Contains(got, fmt.Sprintf("%d more are pinned", n-shown)) {
		t.Errorf("%d bytes, %d shown:\n%s", len(got), shown, got)
	}
}
