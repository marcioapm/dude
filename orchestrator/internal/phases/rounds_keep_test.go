package phases

import "testing"

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
