package phases

import (
	"errors"
	"strings"
	"testing"
)

// A continuing fixer whose phase was given a project secret after its row
// was made (the implementer's lux Run has none of it) is refused its first
// resume, naming the secret; a later resume (finishes > 0) is a person's,
// and goes on.
func TestAContinuingFixerGivenANewSecretCannotResume(t *testing.T) {
	w := newResumeWorld(t)
	w.exec(`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, phase, role, status, ended_at)
		VALUES ('run_im_'||$1, $1, 'prj_'||$1, 'wi_'||$1, 1, 'implement', 'implementer', 'completed', now())`, w.run.Org)
	w.exec(`UPDATE runs SET phase = 'fix', role = 'implementer', continues_run_id = 'run_im_'||organization_id, continues_how = 'resume',
		project_secret_names = '{}', finishes = 0 WHERE id = $1`, w.run.ID)
	w.run.Phase, w.run.Role = "fix", "implementer"
	if err := w.s.roundSecrets(w.ctx, w.run); err != nil {
		t.Fatalf("no secret added: %v", err)
	}
	w.exec(`INSERT INTO project_secrets (project_id, organization_id, name, value, hint, recipients)
		VALUES ($1, $2, 'FIX_TOKEN', 'fix-token-value', 'test', '{fixer}')`, w.run.ProjectID, w.run.Org)
	var cannot errCannotResume
	if err := w.s.roundSecrets(w.ctx, w.run); !errors.As(err, &cannot) || !strings.Contains(err.Error(), "its phase is now given FIX_TOKEN") {
		t.Fatalf("a secret added for fixers: %v, want errCannotResume naming FIX_TOKEN", err)
	}
	w.exec(`UPDATE runs SET finishes = 1 WHERE id = $1`, w.run.ID)
	if err := w.s.roundSecrets(w.ctx, w.run); err != nil {
		t.Fatalf("a later resume: %v, want nil", err)
	}
}

// A completed reviewer or tester is kept for its next round on a project
// that goes on from the previous one; an integration's reviewer never is,
// since no round goes on from it.
func TestAnIntegrationReviewerIsNotKeptForANextRound(t *testing.T) {
	w := newResumeWorld(t)
	w.exec(`UPDATE projects SET delivery_policy = '{"resumeRounds":true}' WHERE id = $1`, w.run.ProjectID)
	w.exec(`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, phase, status, ended_at, lux_run_id, pr_feedback)
		VALUES ('run_rev_'||$1, $1, 'prj_'||$1, 'wi_'||$1, 1, 'review', 'completed', now(), 'lrun_rev', '[]'),
		       ('run_int_'||$1, $1, 'prj_'||$1, 'wi_'||$1, 1, 'review', 'completed', now(), 'lrun_int', '[{"source":"integration"}]')`, w.run.Org)
	kept := func(id string) bool {
		t.Helper()
		var k bool
		if err := w.owner.QueryRow(w.ctx, `SELECT `+keptForNextRound+` FROM runs r WHERE id = $1 || $2`, id, w.run.Org).Scan(&k); err != nil {
			t.Fatal(err)
		}
		return k
	}
	if !kept("run_rev_") {
		t.Error("a completed reviewer of a project that resumes rounds is not kept")
	}
	if kept("run_int_") {
		t.Error("an integration's completed reviewer is kept for a next round")
	}
}
