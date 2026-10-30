package branchdiff_test

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// requestRow is one capture request, as the owner reads it.
type requestRow struct {
	status, reason string
	failures       int
	due            bool // next_attempt_at <= now()
	nextIn         time.Duration
}

func (w *world) requestOf(task string, attempt int) requestRow {
	w.t.Helper()
	var r requestRow
	var secs float64
	if err := w.owner.QueryRow(context.Background(), `SELECT status, COALESCE(reason, ''), failures, next_attempt_at <= now(),
		extract(epoch FROM next_attempt_at - now())::float8
		FROM task_branch_capture_requests WHERE task_id = $1 AND attempt = $2`, task, attempt).
		Scan(&r.status, &r.reason, &r.failures, &r.due, &secs); err != nil {
		w.t.Fatal(err)
	}
	r.nextIn = time.Duration(secs * float64(time.Second))
	return r
}

// sweep runs one pass and returns what it says about more waiting.
func (w *world) sweep() int {
	w.t.Helper()
	n, err := w.cap.Sweep(context.Background())
	if err != nil {
		w.t.Fatal(err)
	}
	return n
}

// A request whose reads fail before GitHub is asked — a delivery state
// the decoder refuses, a repository of another organization — keeps its
// claim, is counted and backed off, and the pass says nothing more is
// waiting: the loop sleeps instead of spinning on it.
func TestARequestWhoseReadsFailIsBackedOffAndNeverSpins(t *testing.T) {
	for _, tc := range []struct {
		name   string
		poison func(w *world)
	}{
		{"an undecodable delivery state", func(w *world) {
			w.exec(`INSERT INTO workflow_runs (id, organization_id, workflow_type, idempotency_key, task_id, status, step, state)
				VALUES ('wf_b', $1, 'task.delivery', 'd', 'wi_b', 'running', 's', '{"branch": 5}')`)
		}},
		{"a repository of another organization", func(w *world) {
			other := fmt.Sprintf("%s_other", w.org)
			w.exec(`INSERT INTO organizations (id, name, slug) VALUES ($1 || '_other', 'Other', $1 || '_other')`)
			if _, err := w.owner.Exec(context.Background(), `INSERT INTO projects (id, organization_id, name, slug, key_prefix)
				VALUES ('prj_o', $1, 'O', 'o', 'O')`, other); err != nil {
				w.t.Fatal(err)
			}
			if _, err := w.owner.Exec(context.Background(), `INSERT INTO repositories (id, organization_id, project_id, name, url)
				VALUES ('repo_o', $1, 'prj_o', 'api', 'https://github.com/acme/other')`, other); err != nil {
				w.t.Fatal(err)
			}
			w.exec(`UPDATE task_branch_capture_requests SET repository_id = 'repo_o' WHERE organization_id = $1`)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t)
			w.fake.CommitFiles(w.branch, "first", map[string]string{"app.ts": "1\n"})
			w.exec(`SELECT want_branch_capture($1, 'wi_b', 1, 'repo_api', '')`)
			tc.poison(w)
			for i := 1; i <= 3; i++ {
				if n := w.sweep(); n != 0 {
					t.Fatalf("pass %d says %d more may be waiting", i, n)
				}
				r := w.requestOf("wi_b", 1)
				if r.status != "pending" || r.failures != i || r.due || r.nextIn <= 0 {
					t.Fatalf("after pass %d: %+v", i, r)
				}
				// Only the clock: due again now.
				w.exec(`UPDATE task_branch_capture_requests SET next_attempt_at = now() WHERE organization_id = $1`)
			}
			if n := len(w.snapshots()); n != 0 {
				t.Errorf("%d snapshots", n)
			}
		})
	}
}

// Twenty requests that always fail, due first, do not starve the one that
// can be captured.
func TestPoisonedRequestsDoNotStarveAHealthyOne(t *testing.T) {
	w := newWorld(t)
	w.exec(`INSERT INTO workflow_runs (id, organization_id, workflow_type, idempotency_key, task_id, status, step, state)
		VALUES ('wf_b', $1, 'task.delivery', 'd', 'wi_b', 'running', 's', '{"branch": 5}')`)
	for a := 1; a <= 20; a++ {
		w.exec(`SELECT want_branch_capture($1, 'wi_b', $2, 'repo_api', '')`, a)
	}
	w.exec(`UPDATE task_branch_capture_requests SET next_attempt_at = now() - interval '1 hour' WHERE organization_id = $1`)
	w.exec(`INSERT INTO tasks (id, organization_id, project_id, number, title, goal) VALUES ('wi_c', $1, 'prj_b', 2, 'T', 'G')`)
	w.exec(`INSERT INTO task_repositories (task_id, repository_id, organization_id, access) VALUES ('wi_c', 'repo_api', $1, 'write')`)
	git(t, w.origin, "update-ref", "refs/heads/dude/wi_c/attempt-1", "refs/heads/main")
	w.fake.CommitFiles("dude/wi_c/attempt-1", "c", map[string]string{"c.ts": "c\n"})
	w.exec(`SELECT want_branch_capture($1, 'wi_c', 1, 'repo_api', '')`)
	for i := 0; i < 3; i++ {
		w.sweep()
	}
	if r := w.requestOf("wi_c", 1); r.status != "done" {
		t.Fatalf("the healthy request behind twenty poisoned ones: %+v", r)
	}
}
