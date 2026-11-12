package orchestrator_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// holding makes a live task holding pull request n's head in mode: its
// source, a delivery waiting on it, and a review Run at work. Owned by
// owner.
func (w *world) holding(n int, head, mode, owner string) (task, run string) {
	w.t.Helper()
	task = w.adopted("pr", n, head, "main", mode)
	w.assignOwner(task, owner)
	mustExec(w.t, w.owner, `UPDATE tasks SET status = 'running' WHERE id = $1`, task)
	mustExec(w.t, w.owner, `INSERT INTO workflow_runs (id, organization_id, task_id, workflow_type, idempotency_key, status, step, state)
		VALUES ($1, $2, $3, 'task.delivery', $1, 'waiting', 'awaitReview', jsonb_build_object('taskId', $3::text, 'projectId', $4::text,
			'attempt', 1, 'branch', $5::text))`, "wf_"+task, w.org, task, w.project, head)
	run = "run_" + task
	mustExec(w.t, w.owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, kind, role, phase)
		VALUES ($1, $2, $3, $4, 1, 'running', 'agent', 'reviewer', 'review')`, run, w.org, w.project, task)
	return task, run
}

func (w *world) supersede(as, holder string, n int, head, mode string) (int, map[string]any) {
	w.t.Helper()
	// The world numbers its tasks itself (task): the project's counter goes past them.
	mustExec(w.t, w.owner, `UPDATE projects SET next_task_number = (SELECT max(number) + 1 FROM tasks WHERE project_id = $1)
		WHERE id = $1`, w.project)
	return w.callAs(as, "/internal/projects/"+w.project+"/tasks/supersede", map[string]any{
		"holder": holder,
		"source": map[string]any{"kind": "pr", "repositoryId": w.repoID, "number": n, "title": "Taken over",
			"headBranch": head, "baseBranch": "main", "mode": mode},
		"create": map[string]any{"title": "Take it over", "goal": "carry the pull request on from here"},
	})
}

// supersedeState is what a supersede changes, to show a refused one
// changed nothing.
func (w *world) supersedeState(old string) string {
	w.t.Helper()
	var s string
	if err := w.owner.QueryRow(context.Background(), `SELECT (SELECT status::text FROM tasks WHERE id = $1) || ' ' ||
		(SELECT string_agg(status::text, ',') FROM runs WHERE task_id = $1) || ' ' ||
		(SELECT status::text FROM workflow_runs WHERE task_id = $1) || ' tasks=' || (SELECT count(*) FROM tasks) ||
		' relations=' || (SELECT count(*) FROM task_relations) || ' sources=' || (SELECT count(*) FROM task_sources) ||
		' events=' || (SELECT count(*) FROM events WHERE event_type IN ('task.superseded', 'task.supersedes', 'task.stopped'))`,
		old).Scan(&s); err != nil {
		w.t.Fatal(err)
	}
	return s
}

// A confirmed supersede, in every combination of a writing mode and review
// only: the old task stopped as a person's Stop stops it (its Runs ended,
// its delivery aborted, the branch released), the new one made with the
// source, the two linked, an event on each, and the pull request left open
// on GitHub.
func TestSupersedeEveryCombination(t *testing.T) {
	for _, c := range []struct{ old, new string }{
		{delivery.ModeReviewOnly, delivery.ModeReview},
		{delivery.ModeReview, delivery.ModeReviewOnly},
		{delivery.ModeReview, delivery.ModeReview},
	} {
		t.Run(c.old+" to "+c.new, func(t *testing.T) {
			w := newWorld(t)
			n := w.gh.OpenAsPerson("alice", "feature/greet", "main", "Greet")
			ana := w.person("Ana")
			old, run := w.holding(n, "feature/greet", c.old, ana)
			// Asked first: refused, naming the holder.
			status, out := w.checkSource(map[string]any{"kind": "pr", "repositoryId": w.repoID, "pullRequest": fmt.Sprint(n), "mode": c.new})
			refusedAs(t, status, out, 409, "superseded_required")
			// Confirmed with the holder named: answered as it.
			status, out = w.checkSource(map[string]any{"kind": "pr", "repositoryId": w.repoID, "pullRequest": fmt.Sprint(n), "mode": c.new,
				"supersedeTask": old})
			if s, _ := out["supersedes"].(map[string]any); status != 200 || s["id"] != old || s["mode"] != c.old {
				t.Fatalf("confirmed check: %d %v", status, out)
			}
			status, out = w.supersede(ana, old, n, "feature/greet", c.new)
			if status != 201 {
				t.Fatalf("supersede: %d %v", status, out)
			}
			created, _ := out["taskId"].(string)
			if s := w.taskStatus(old); s != "aborted" {
				t.Errorf("the old task is %s", s)
			}
			if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'aborted'`, run); n != 1 {
				t.Errorf("the old task's Run was not ended")
			}
			if n := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND status = 'aborted'`, old); n != 1 {
				t.Errorf("the old task's delivery was not aborted")
			}
			access := "write"
			if c.new == delivery.ModeReviewOnly {
				access = "read"
			}
			if n := w.count(`SELECT count(*) FROM task_sources s JOIN task_repositories r ON r.task_id = s.task_id
				WHERE s.task_id = $1 AND s.mode = $2 AND s.head_branch = 'feature/greet' AND r.access = $3::repository_access`,
				created, c.new, access); n != 1 {
				t.Errorf("the new task's source is not attached in %s with %s access", c.new, access)
			}
			if n := w.count(`SELECT count(*) FROM task_relations WHERE from_task_id = $1 AND to_task_id = $2 AND kind = 'supersedes'`,
				created, old); n != 1 {
				t.Errorf("no supersedes edge from the new task to the old")
			}
			for _, e := range []struct{ task, typ string }{{old, "task.superseded"}, {created, "task.supersedes"},
				{old, "task.stopped"}, {old, "task.relation_added"}, {created, "task.relation_added"}} {
				if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = $2`, e.task, e.typ); n != 1 {
					t.Errorf("%d %s events on %s", n, e.typ, e.task)
				}
			}
			if pr := w.gh.Pull(n); pr.State != "open" {
				t.Errorf("the pull request is %s on GitHub", pr.State)
			}
			// The branch is the new task's now: asked again for a third, it is named.
			status, out = w.checkSource(map[string]any{"kind": "pr", "repositoryId": w.repoID, "pullRequest": fmt.Sprint(n)})
			if h := holderNamed(out); status != 409 || h["id"] != created {
				t.Errorf("after the supersede the branch is held by %v (%d)", h, status)
			}
		})
	}
}

// One transaction: a failure after the old task stopped leaves everything
// as it was, the old task still live and nothing made.
func TestSupersedeIsOneTransaction(t *testing.T) {
	w := newWorld(t)
	n := w.gh.OpenAsPerson("alice", "feature/greet", "main", "Greet")
	ana := w.person("Ana")
	old, _ := w.holding(n, "feature/greet", delivery.ModeReviewOnly, ana)
	before := w.supersedeState(old)
	w.apiServer.AfterSupersedeStop = func() error { return errors.New("injected after the stop") }
	if status, out := w.supersede(ana, old, n, "feature/greet", delivery.ModeReview); status < 500 {
		t.Fatalf("supersede with a failure after the stop: %d %v", status, out)
	}
	if after := w.supersedeState(old); after != before {
		t.Fatalf("a failed supersede changed things:\n%s\n%s", before, after)
	}
	w.apiServer.AfterSupersedeStop = nil
	if status, out := w.supersede(ana, old, n, "feature/greet", delivery.ModeReview); status != 201 {
		t.Fatalf("supersede after: %d %v", status, out)
	}
}

// The holder is read again under the lock: one that changed between the
// ask and the confirmation is refused, naming the new one, and nothing
// changes.
func TestSupersedeRefusesAChangedHolder(t *testing.T) {
	w := newWorld(t)
	n := w.gh.OpenAsPerson("alice", "feature/greet", "main", "Greet")
	ana := w.person("Ana")
	first, _ := w.holding(n, "feature/greet", delivery.ModeReview, ana)
	mustExec(t, w.owner, `UPDATE tasks SET status = 'done' WHERE id = $1`, first)
	second, _ := w.holding(n, "feature/greet", delivery.ModeReview, ana)
	before := w.supersedeState(second)
	status, out := w.supersede(ana, first, n, "feature/greet", delivery.ModeReviewOnly)
	refusedAs(t, status, out, 409, "superseded_required")
	if h := holderNamed(out); h["id"] != second {
		t.Errorf("the refusal names %v, want %s", h, second)
	}
	if after := w.supersedeState(second); after != before {
		t.Fatalf("a refused supersede changed things:\n%s\n%s", before, after)
	}
	status, out = w.checkSource(map[string]any{"kind": "pr", "repositoryId": w.repoID, "pullRequest": fmt.Sprint(n), "supersedeTask": first})
	refusedAs(t, status, out, 409, "superseded_required")
}

// Only the holder's owner or an admin stops it, as Stop.
func TestSupersedeIsTheHoldersOwners(t *testing.T) {
	w := newWorld(t)
	n := w.gh.OpenAsPerson("alice", "feature/greet", "main", "Greet")
	ana, bo := w.person("Ana"), w.person("Bo")
	old, _ := w.holding(n, "feature/greet", delivery.ModeReview, ana)
	if status, out := w.supersede(bo, old, n, "feature/greet", delivery.ModeReview); status != 403 {
		t.Fatalf("another member's supersede: %d %v", status, out)
	}
	if s := w.taskStatus(old); s != "running" {
		t.Fatalf("a refused supersede left the holder %s", s)
	}
}

// A queued holder whose delivery is live (parked on a blocker added after
// its start) is stopped as a person's Stop stops it, not only marked: no
// running or waiting delivery and no live Run is left, and its woken
// workflow makes no Run on the superseded task.
func TestSupersedeStopsAQueuedHolderWithALiveDelivery(t *testing.T) {
	w := newWorld(t)
	n := w.gh.OpenAsPerson("alice", "feature/greet", "main", "Greet")
	ana := w.person("Ana")
	old, _ := w.holding(n, "feature/greet", delivery.ModeContinue, ana)
	mustExec(t, w.owner, `UPDATE tasks SET status = 'queued' WHERE id = $1`, old)
	mustExec(t, w.owner, `UPDATE workflow_runs SET step = 'implementRun' WHERE task_id = $1`, old)
	status, out := w.supersede(ana, old, n, "feature/greet", delivery.ModeReview)
	if status != 201 {
		t.Fatalf("supersede: %d %v", status, out)
	}
	if s := w.taskStatus(old); s != "aborted" {
		t.Errorf("the old task is %s", s)
	}
	if n := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND status IN ('running', 'waiting')`, old); n != 0 {
		t.Errorf("the old task's delivery is still live")
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND status IN ('pending', 'scheduled', 'starting', 'running', 'paused')`, old); n != 0 {
		t.Errorf("%d live Runs left on the old task", n)
	}
	before := w.count(`SELECT count(*) FROM runs WHERE task_id = $1`, old)
	// Its wake comes, whatever it was waiting for.
	mustExec(t, w.owner, `UPDATE workflow_runs SET wake_at = now() WHERE task_id = $1`, old)
	for range 5 {
		w.pump()
	}
	if after := w.count(`SELECT count(*) FROM runs WHERE task_id = $1`, old); after != before {
		t.Fatalf("the superseded task's workflow made %d Runs after the supersede\n%s", after-before, w.describeRuns())
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'task.stopped'`, old); n != 1 {
		t.Errorf("%d task.stopped on the old task", n)
	}
}

// A person's Stop of the same queued task is unchanged: not in progress.
func TestStopRefusesAQueuedTaskStill(t *testing.T) {
	w := newWorld(t)
	n := w.gh.OpenAsPerson("alice", "feature/greet", "main", "Greet")
	ana := w.person("Ana")
	old, _ := w.holding(n, "feature/greet", delivery.ModeContinue, ana)
	mustExec(t, w.owner, `UPDATE tasks SET status = 'queued' WHERE id = $1`, old)
	status, out := w.callAs(ana, "/internal/tasks/"+old+"/stop", map[string]any{})
	refusedAs(t, status, out, 409, "not_in_progress")
}
