package orchestrator_test

// recover_run's refusals: what the conductor may not recover, and where it
// may not push, checked synchronously under the task's Chat lock, writing
// nothing.

import (
	"strings"
	"testing"
)

// recoverRefused is recover_run refused, saying want, writing nothing.
func (w *world) recoverRefused(task, args, want string) {
	w.t.Helper()
	before := w.count(`SELECT count(*) FROM run_recoveries`)
	w.refused(task, "recover_run", args, want)
	if after := w.count(`SELECT count(*) FROM run_recoveries`); after != before {
		w.t.Errorf("a refused recover_run %s wrote a recovery", args)
	}
}

// A reviewer's Run and an investigator's never push; a live Run is
// stopped first; another task's Run, the conductor itself and a Run whose
// push was refused for a secret are refused too, and nothing is written.
func TestRecoverRunRefusesReviewersLiveRunsAndOthersTasks(t *testing.T) {
	w := conducting(t)
	task := w.task()
	conductor := w.talk(task)
	other := w.task()
	for _, r := range []struct{ id, task, status, phase, role, source, caps string }{
		{"run_rev", task, "completed", "review", "reviewer", "", "{}"},
		{"run_test", task, "failed", "test", "qa_browser", "", "{}"},
		{"run_inv", task, "aborted", "investigate", "investigator", "task_conductor", `{"publishCode":false}`},
		{"run_live", task, "running", "implement", "implementer", "", "{}"},
		{"run_other", other, "failed", "implement", "implementer", "", "{}"},
		{"run_secret", task, "failed", "implement", "implementer", "", "{}"},
	} {
		mustExec(t, w.owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, source_kind,
			parent_run_id, child_profile, child_capabilities)
			VALUES ($1, $2, $3, $4, 1, $5::run_status, $6::run_phase, $7::agent_role, NULLIF($8::text, ''), CASE WHEN $8::text <> '' THEN $9 END,
			CASE WHEN $8::text <> '' THEN $7::text END, $10::jsonb)`, r.id, w.org, w.project, r.task, r.status, r.phase, r.role, r.source, conductor, r.caps)
	}
	mustExec(t, w.owner, `INSERT INTO events (id, organization_id, project_id, task_id, run_id, event_type, actor_type, actor_id, source, payload)
		VALUES ('evt_rcv_secret', $1, $2, $3, 'run_secret', 'run.push.refused', 'system', 'x', 'runner', '{}')`, w.org, w.project, task)
	w.recoverRefused(task, `{"run":"run_rev"}`, "its work is never pushed")
	w.recoverRefused(task, `{"run":"run_test"}`, "its work is never pushed")
	w.recoverRefused(task, `{"run":"run_inv"}`, "its work is never pushed")
	w.recoverRefused(task, `{"run":"run_live"}`, "stop it first")
	w.recoverRefused(task, `{"run":"run_other"}`, "is not a Run of your task")
	w.recoverRefused(task, `{"run":"`+conductor+`"}`, "is you")
	w.recoverRefused(task, `{"run":"run_secret"}`, "a secret was found")
}

// The branch is the task's own, under dude/<task>/recovered-: anything
// else, the task branch and an open pull request's head are refused.
func TestRecoverRunRefusesABranchOutsideItsNamespaceOrThePullRequests(t *testing.T) {
	w := conducting(t)
	task := w.task()
	conductor := w.talk(task)
	mustExec(t, w.owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role)
		VALUES ('run_dead', $1, $2, $3, 1, 'failed', 'implement', 'implementer')`, w.org, w.project, task)
	_ = conductor
	prefix := "dude/" + task + "/recovered-"
	mustExec(t, w.owner, `INSERT INTO pull_requests (id, organization_id, project_id, task_id, repository_id, number, url,
		title, head_branch, head_sha, base_branch, state) VALUES ('pr_rec', $1, $2, $3, $4, 9, 'https://github.com/acme/target/pull/9', 'T', $5, 'abc', 'main', 'open')`,
		w.org, w.project, task, w.repoID, prefix+"pr")
	for _, branch := range []string{"main", "dude/" + task + "/attempt-1", "dude/other/recovered-x", prefix, prefix + "a/b", prefix + strings.Repeat("x", 61)} {
		w.recoverRefused(task, `{"run":"run_dead","branch":"`+branch+`"}`, "is outside")
	}
	w.recoverRefused(task, `{"run":"run_dead","branch":"`+prefix+`pr"}`, "an open pull request's")
	// Named well, it is asked; asked again, it is the same recovery; another
	// branch for the same Run is refused while the first stands (left to
	// stand: the worker would fail it at once, this Run never ran on lux).
	w.noRecoveries = true
	first := w.must(task, "recover_run", `{"run":"run_dead","branch":"`+prefix+`mine"}`)
	again := w.must(task, "recover_run", `{"run":"run_dead","branch":"`+prefix+`mine"}`)
	if first["recoveryId"] != again["recoveryId"] || first["branch"] != prefix+"mine" {
		t.Errorf("asked twice: %v then %v", first, again)
	}
	w.recoverRefused(task, `{"run":"run_dead","branch":"`+prefix+`yours"}`, "already recovered to "+prefix+"mine")
	if n := w.count(`SELECT count(*) FROM run_recoveries WHERE run_id = 'run_dead'`); n != 1 {
		t.Errorf("%d recoveries for one Run and branch", n)
	}
}
