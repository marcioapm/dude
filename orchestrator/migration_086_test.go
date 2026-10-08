package orchestrator_test

import (
	"context"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

// A Run belongs to a task or to a session, never both and never neither,
// and a session's Run has no project; a session has exactly one owner, and
// a session Run's events carry its session whoever writes them.
func TestARunBelongsToATaskOrASession(t *testing.T) {
	_, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	ctx := context.Background()
	mustExec(t, owner, `INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_c', $1, 'P', 'p', 'P')`, org)
	mustExec(t, owner, `INSERT INTO tasks (id, organization_id, project_id, number, title, goal) VALUES ('wi_c', $1, 'prj_c', 1, 'T', 'G')`, org)
	mustExec(t, owner, `INSERT INTO people (id, organization_id, name) VALUES ('per_c', $1, 'C')`, org)
	mustExec(t, owner, `BEGIN`)
	mustExec(t, owner, `INSERT INTO sessions (id, organization_id, title) VALUES ('sess_c', $1, 'Ideas')`, org)
	mustExec(t, owner, `INSERT INTO session_people (session_id, person_id, organization_id, role, accepted_at)
		VALUES ('sess_c', 'per_c', $1, 'owner', now())`, org)
	mustExec(t, owner, `COMMIT`)

	refused := func(what, sql string) {
		t.Helper()
		_, err := owner.Exec(ctx, sql, org)
		if err == nil || !strings.Contains(err.Error(), "runs_task_or_session") {
			t.Errorf("%s: want runs_task_or_session, got %v", what, err)
		}
	}
	refused("both", `INSERT INTO runs (id, organization_id, project_id, task_id, session_id, attempt) VALUES ('run_b', $1, 'prj_c', 'wi_c', 'sess_c', 1)`)
	refused("neither", `INSERT INTO runs (id, organization_id, attempt) VALUES ('run_n', $1, 1)`)
	refused("a task's with no project", `INSERT INTO runs (id, organization_id, task_id, attempt) VALUES ('run_p', $1, 'wi_c', 1)`)
	mustExec(t, owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt) VALUES ('run_t', $1, 'prj_c', 'wi_c', 1)`, org)
	mustExec(t, owner, `INSERT INTO runs (id, organization_id, session_id, attempt, role, kind) VALUES ('run_s', $1, 'sess_c', 1, 'brainstorm', 'agent')`, org)
	if _, err := owner.Exec(ctx, `INSERT INTO runs (id, organization_id, session_id, attempt, role, kind)
		VALUES ('run_s2', $1, 'sess_c', 1, 'brainstorm', 'agent')`, org); err == nil || !strings.Contains(err.Error(), "runs_live_brainstorm_idx") {
		t.Errorf("a second live brainstorm: %v", err)
	}

	mustExec(t, owner, `INSERT INTO events (id, organization_id, event_type, run_id, actor_type, actor_id, source)
		VALUES ('evt_s', $1, 'agent.message', 'run_s', 'agent', 'run_s', 'runner')`, org)
	var session string
	if err := owner.QueryRow(ctx, `SELECT COALESCE(session_id, '') FROM events WHERE id = 'evt_s'`).Scan(&session); err != nil || session != "sess_c" {
		t.Errorf("a session Run's event names session %q: %v", session, err)
	}

	// Exactly one owner, checked at commit.
	mustExec(t, owner, `INSERT INTO people (id, organization_id, name) VALUES ('per_d', $1, 'D')`, org)
	if _, err := owner.Exec(ctx, `INSERT INTO session_people (session_id, person_id, organization_id, role, accepted_at)
		VALUES ('sess_c', 'per_d', $1, 'owner', now())`, org); err == nil {
		t.Error("a second owner was accepted")
	}
	if _, err := owner.Exec(ctx, `DELETE FROM session_people WHERE person_id = 'per_c'`); err == nil {
		t.Error("a session was left with no owner")
	}
	if _, err := owner.Exec(ctx, `INSERT INTO session_people (session_id, person_id, organization_id, role)
		VALUES ('sess_c', 'per_d', $1, 'owner')`, org); err == nil {
		t.Error("an owner that has not accepted was accepted")
	}
}
