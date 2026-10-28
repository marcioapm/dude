package orchestrator_test

import (
	"context"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

// A Run has exactly one owner — a task, a session or a branch of its
// project it previews — and a branch preview is a preview with a project,
// one live per project branch.
func TestABranchPreviewIsOwnedByItsProjectBranch(t *testing.T) {
	_, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	ctx := context.Background()
	mustExec(t, owner, `INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_b', $1, 'P', 'p', 'P')`, org)
	mustExec(t, owner, `INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_o', $1, 'O', 'o', 'O')`, org)
	mustExec(t, owner, `INSERT INTO tasks (id, organization_id, project_id, number, title, goal) VALUES ('wi_b', $1, 'prj_b', 1, 'T', 'G')`, org)
	mustExec(t, owner, `INSERT INTO people (id, organization_id, name) VALUES ('per_b', $1, 'B')`, org)
	mustExec(t, owner, `BEGIN`)
	mustExec(t, owner, `INSERT INTO sessions (id, organization_id, title) VALUES ('sess_b', $1, 'Ideas')`, org)
	mustExec(t, owner, `INSERT INTO session_people (session_id, person_id, organization_id, role, accepted_at)
		VALUES ('sess_b', 'per_b', $1, 'owner', now())`, org)
	mustExec(t, owner, `COMMIT`)

	refused := func(what, sql string) {
		t.Helper()
		_, err := owner.Exec(ctx, sql, org)
		if err == nil || !strings.Contains(err.Error(), "runs_task_or_session") {
			t.Errorf("%s: want runs_task_or_session, got %v", what, err)
		}
	}
	refused("none", `INSERT INTO runs (id, organization_id, project_id, attempt, kind) VALUES ('run_0', $1, 'prj_b', 1, 'preview')`)
	refused("task and session", `INSERT INTO runs (id, organization_id, project_id, task_id, session_id, attempt) VALUES ('run_1', $1, 'prj_b', 'wi_b', 'sess_b', 1)`)
	refused("task and branch", `INSERT INTO runs (id, organization_id, project_id, task_id, preview_branch, attempt, kind)
		VALUES ('run_2', $1, 'prj_b', 'wi_b', 'main', 1, 'preview')`)
	refused("session and branch", `INSERT INTO runs (id, organization_id, project_id, session_id, preview_branch, attempt, kind)
		VALUES ('run_3', $1, 'prj_b', 'sess_b', 'main', 1, 'preview')`)
	refused("all three", `INSERT INTO runs (id, organization_id, project_id, task_id, session_id, preview_branch, attempt, kind)
		VALUES ('run_4', $1, 'prj_b', 'wi_b', 'sess_b', 'main', 1, 'preview')`)
	refused("a branch's agent", `INSERT INTO runs (id, organization_id, project_id, preview_branch, attempt, kind)
		VALUES ('run_5', $1, 'prj_b', 'main', 1, 'agent')`)
	refused("a branch preview with no project", `INSERT INTO runs (id, organization_id, preview_branch, attempt, kind)
		VALUES ('run_6', $1, 'main', 1, 'preview')`)
	refused("a task's with no project", `INSERT INTO runs (id, organization_id, task_id, attempt) VALUES ('run_7', $1, 'wi_b', 1)`)

	mustExec(t, owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt) VALUES ('run_t', $1, 'prj_b', 'wi_b', 1)`, org)
	mustExec(t, owner, `INSERT INTO runs (id, organization_id, session_id, attempt, role, kind) VALUES ('run_s', $1, 'sess_b', 1, 'brainstorm', 'agent')`, org)
	mustExec(t, owner, `INSERT INTO runs (id, organization_id, project_id, preview_branch, attempt, kind)
		VALUES ('run_b', $1, 'prj_b', 'feature/x', 1, 'preview')`, org)

	// One live per project branch; another branch, another project, or one
	// once the first ended, is a preview of its own.
	if _, err := owner.Exec(ctx, `INSERT INTO runs (id, organization_id, project_id, preview_branch, attempt, kind)
		VALUES ('run_b2', $1, 'prj_b', 'feature/x', 1, 'preview')`, org); err == nil || !strings.Contains(err.Error(), "runs_live_branch_preview_idx") {
		t.Errorf("a second live preview of a branch: %v", err)
	}
	mustExec(t, owner, `INSERT INTO runs (id, organization_id, project_id, preview_branch, attempt, kind)
		VALUES ('run_b3', $1, 'prj_b', 'feature/y', 1, 'preview')`, org)
	mustExec(t, owner, `INSERT INTO runs (id, organization_id, project_id, preview_branch, attempt, kind)
		VALUES ('run_b4', $1, 'prj_o', 'feature/x', 1, 'preview')`, org)
	mustExec(t, owner, `UPDATE runs SET status = 'completed' WHERE id = 'run_b'`)
	mustExec(t, owner, `INSERT INTO runs (id, organization_id, project_id, preview_branch, attempt, kind)
		VALUES ('run_b5', $1, 'prj_b', 'feature/x', 1, 'preview')`, org)

	// A branch preview's run is no task's: the task's metrics and its
	// preview trigger see none of it.
	mustExec(t, owner, `UPDATE runs SET wakeable = true WHERE id = 'run_b5'`)
	mustExec(t, owner, `UPDATE runs SET heads = '{"app": {"sha": "abc"}}' WHERE id = 'run_t'`)
	var syncs int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM runs WHERE id = 'run_b5' AND sync_wanted_at IS NOT NULL`).Scan(&syncs); err != nil || syncs != 0 {
		t.Errorf("a task's publish asked a branch preview to sync: %d, %v", syncs, err)
	}
	var runs int64
	if err := owner.QueryRow(ctx, `SELECT runs FROM task_metrics('wi_b')`).Scan(&runs); err != nil || runs != 1 {
		t.Errorf("task metrics count %d runs, want 1: %v", runs, err)
	}
}
