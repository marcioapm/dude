package orchestrator_test

import (
	"context"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

// Migration 262, up from main's schema: every memory already there is
// unpinned and stays as it was, a project's fact can then be pinned, and
// nothing else can — a session's memory, the organization's, a note.
func TestMigration262PinsOnlyAProjectsFact(t *testing.T) {
	ctx := context.Background()
	owner, apply := dbtest.Upgrade(t, "262")
	for _, q := range []string{
		`INSERT INTO organizations (id, name, slug) VALUES ('org_m', 'O', 'o')`,
		`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_m', 'org_m', 'P', 'p', 'P')`,
		`INSERT INTO sessions (id, organization_id, title, titled_by) VALUES ('ses_m', 'org_m', 'S', 'person')`,
		`INSERT INTO memories (id, organization_id, project_id, session_id, kind, title, content, author_kind, system_reason) VALUES
			('mem_fact', 'org_m', 'prj_m', NULL, 'fact', 'F', 'x', 'system', 'test'),
			('mem_note', 'org_m', 'prj_m', NULL, 'note', 'N', 'x', 'system', 'test'),
			('mem_org', 'org_m', NULL, NULL, 'fact', 'O', 'x', 'system', 'test'),
			('mem_ses', 'org_m', NULL, 'ses_m', 'fact', 'S', 'x', 'system', 'test')`,
	} {
		if _, err := owner.Exec(ctx, q); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
	}
	apply()
	var pinned, all int
	if err := owner.QueryRow(ctx, `SELECT count(*) FILTER (WHERE pinned_at IS NOT NULL), count(*) FROM memories`).Scan(&pinned, &all); err != nil || pinned != 0 || all != 4 {
		t.Fatalf("after 262: %d of %d pinned (%v)", pinned, all, err)
	}
	if _, err := owner.Exec(ctx, `UPDATE memories SET pinned_at = now() WHERE id = 'mem_fact'`); err != nil {
		t.Errorf("a project's fact is not pinned: %v", err)
	}
	for _, id := range []string{"mem_note", "mem_org", "mem_ses"} {
		if _, err := owner.Exec(ctx, `UPDATE memories SET pinned_at = now() WHERE id = $1`, id); err == nil || !strings.Contains(err.Error(), "memories_pinned_project_fact") {
			t.Errorf("%s pinned: %v", id, err)
		}
	}
	var indexed bool
	if err := owner.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_indexes WHERE indexname = 'memories_pinned_idx')`).Scan(&indexed); err != nil || !indexed {
		t.Errorf("no index for the briefing's read (%v)", err)
	}
}
