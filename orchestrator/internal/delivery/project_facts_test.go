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

// A fact's text over projectFactChars runes is cut, saying so; one fact
// over the whole bytes bound is still shown, as the first fact always is;
// and a fence a fact leaves open, cut or not, is closed, so neither the
// facts after it nor the prompt after them are read as code.
func TestALongFactIsCutAndAFenceIsClosed(t *testing.T) {
	w := newGoldenWorld(t)
	facts := func() string {
		return w.in(t, func(ctx context.Context, tx pgx.Tx) (string, error) { return ProjectFacts(ctx, tx, "prj_g") })
	}
	// 601 runes, multi-byte: cut at 599 and an ellipsis.
	w.exec(t, `INSERT INTO memories (id, organization_id, project_id, title, content, author_kind, system_reason, pinned_at, pinned_by_person_id)
		VALUES ('mem_long', $1, 'prj_g', 'Long', repeat('é', 601), 'system', 'test', now(), 'per_ana')`)
	got := facts()
	if !strings.Contains(got, ": "+strings.Repeat("é", projectFactChars-1)+"…") || strings.Contains(got, strings.Repeat("é", projectFactChars)) {
		t.Errorf("a %d-rune fact is not cut at %d with an ellipsis:\n%s", projectFactChars+1, projectFactChars-1, got)
	}
	// Exactly projectFactChars runes: shown whole.
	w.exec(t, `UPDATE memories SET content = repeat('é', 600) WHERE id = 'mem_long' AND organization_id = $1`)
	if got := facts(); !strings.Contains(got, strings.Repeat("é", projectFactChars)) || strings.Contains(got, "…") {
		t.Errorf("a %d-rune fact is cut:\n%s", projectFactChars, got)
	}

	// Whitespace at rune 601, or before the text, is not counted as the
	// text: what is shown is what the whole text, trimmed, would show.
	for name, c := range map[string]struct{ content, want string }{
		"a space at 601":          {`repeat('a', 600) || ' ' || 'TAIL'`, strings.Repeat("a", projectFactChars-1) + "…"},
		"a newline at 601":        {`repeat('a', 600) || E'\n' || 'TAIL'`, strings.Repeat("a", projectFactChars-1) + "…"},
		"a no-break space at 601": {`repeat('a', 600) || U&'\00A0' || 'TAIL'`, strings.Repeat("a", projectFactChars-1) + "…"},
		"leading whitespace":      {`E' \n\t' || repeat('a', 600)`, strings.Repeat("a", projectFactChars)},
		"trailing whitespace":     {`repeat('a', 600) || E' \n '`, strings.Repeat("a", projectFactChars)},
		// Trimmed as strings.TrimSpace trims, not ASCII alone.
		"a line separator before":  {`U&'\2028' || repeat('a', 600)`, strings.Repeat("a", projectFactChars)},
		"a no-break space at ends": {`U&'\00A0' || repeat('a', 600) || U&'\00A0'`, strings.Repeat("a", projectFactChars)},
	} {
		w.exec(t, `UPDATE memories SET content = `+c.content+` WHERE id = 'mem_long' AND organization_id = $1`)
		got := facts()
		if !strings.Contains(got+"\n", ": "+c.want+"\n") {
			t.Errorf("%s: not shown as %q:\n%s", name, c.want, got)
		}
		if !strings.HasSuffix(c.want, "…") && strings.Contains(got, "…") {
			t.Errorf("%s: cut:\n%s", name, got)
		}
	}

	// One fact longer than the whole bound is shown, the rest counted.
	w.exec(t, `UPDATE memories SET title = repeat('T', 200), content = repeat('é', 600) WHERE id = 'mem_long' AND organization_id = $1`)
	w.exec(t, `INSERT INTO people (id, organization_id, name) VALUES ('per_long', $1, repeat('N', 3400))`)
	w.exec(t, `UPDATE memories SET pinned_by_person_id = 'per_long' WHERE id = 'mem_long' AND organization_id = $1`)
	w.exec(t, `INSERT INTO memories (id, organization_id, project_id, title, content, author_kind, system_reason, pinned_at, pinned_by_person_id, updated_at)
		VALUES ('mem_older', $1, 'prj_g', 'Older', 'x', 'system', 'test', now() - interval '1 day', 'per_ana', now() - interval '1 day')`)
	got = facts()
	if !strings.Contains(got, "**"+strings.Repeat("T", 200)+"**") || strings.Contains(got, "**Older**") || !strings.Contains(got, "1 more is pinned") {
		t.Errorf("a fact over %d bytes is not shown alone, the other counted:\n%s", projectFactsBytes, got)
	}
	w.exec(t, `UPDATE memories SET pinned_at = NULL, pinned_by_person_id = NULL WHERE organization_id = $1`)

	// An open fence, not cut: closed before the next fact, and the prompt.
	w.exec(t, `INSERT INTO memories (id, organization_id, project_id, title, content, author_kind, system_reason, pinned_at, pinned_by_person_id)
		VALUES ('mem_fence', $1, 'prj_g', 'Fenced', E'Run it so:\n`+"```"+`sh\nmake test', 'system', 'test', now() - interval '1 hour', 'per_ana'),
		       ('mem_after', $1, 'prj_g', 'After', 'y', 'system', 'test', now(), 'per_ana')`)
	got = facts()
	prompt := Prompt(PhaseImplement, PromptInput{Title: "T", ProjectFacts: got, Coordination: "## On this epic\n\nC"})
	if f := UnclosedFence(prompt[:strings.Index(prompt, "**After**")]); f != "" {
		t.Errorf("the fence is still open at the next fact:\n%s", got)
	}
	if UnclosedFence(prompt) != "" {
		t.Errorf("the fence is still open at the prompt's end:\n%s", prompt)
	}
	// The facts come before the epic in a phase's prompt.
	if facts, epic := strings.Index(prompt, "## Project facts"), strings.Index(prompt, "## On this epic"); facts < 0 || epic < 0 || facts > epic {
		t.Errorf("the facts (at %d) are not before the epic (at %d):\n%s", facts, epic, prompt)
	}
}
