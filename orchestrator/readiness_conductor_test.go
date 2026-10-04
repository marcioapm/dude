package orchestrator_test

// A conducted task's pull requests (design: the conductor, step 5):
// readiness is dude's notice in Chat and wakes nobody; a merge wakes the
// conductor once, to close out.

import (
	"context"
	"fmt"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

// syncs reads the pull requests and runs every loop, n times.
func (w *world) syncs(n int) {
	for range n {
		w.sync()
		w.pump()
	}
}

// Ready to merge under the conductor: dude's notice in Chat, and no wake;
// no longer ready, a notice saying why. Under Deliver, no notice.
func TestReadinessIsANoticeNotAWake(t *testing.T) {
	for _, conducted := range []bool{true, false} {
		t.Run(fmt.Sprintf("conducted=%v", conducted), func(t *testing.T) {
			w := conducting(t)
			task := w.reviewing()
			if conducted {
				if status, out := w.chat(task, "mine now"); status != 201 || out["decider"] != "conductor" {
					t.Fatalf("take-over: %d %v", status, out)
				}
				c, _, _ := w.conductor(task)
				w.until("the conductor's turn", func() bool { return len(w.said(c)) > 0 })
			}
			wakes := w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1`, task)
			w.gh.Review(1, "alice", "APPROVED")
			w.until("ready to merge", func() bool { w.sync(); return w.taskStatus(task) == "ready_to_merge" })
			w.syncs(3)
			var notices []string
			rows, _ := w.owner.Query(context.Background(), `SELECT payload->>'text' FROM events WHERE task_id = $1
				AND event_type = 'chat.notice' ORDER BY cursor`, task)
			for rows.Next() {
				var s string
				_ = rows.Scan(&s)
				notices = append(notices, s)
			}
			rows.Close()
			if !conducted {
				if len(notices) != 0 {
					t.Errorf("notices under Deliver: %v", notices)
				}
				return
			}
			if len(notices) != 1 || notices[0] != "target#1 is ready to merge: approved, checks green. Merging is yours." {
				t.Errorf("the notices: %q", notices)
			}
			if n := w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1`, task); n != wakes {
				t.Errorf("readiness recorded %d reasons to wake the conductor", n-wakes)
			}
			// An approval dismissed is no approval.
			w.gh.Review(1, "alice", "DISMISSED")
			w.until("no longer ready", func() bool { w.sync(); return w.taskStatus(task) != "ready_to_merge" })
			if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'chat.notice'
				AND payload->>'about' = 'no_longer_ready' AND payload->>'text' = 'target#1 is no longer ready to merge: nobody has approved it.'`, task); n != 1 {
				t.Errorf("%d notices that it is no longer ready", n)
			}
			if n := w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1`, task); n != wakes {
				t.Errorf("readiness lost recorded %d reasons to wake the conductor", n-wakes)
			}
		})
	}
}

// Merged, or closed without merging, under the conductor: exactly one
// reason to wake it, to close out, however many syncs see it; the
// conductor is woken with it. Under Deliver, none. The task ends done, or
// aborted, either way.
func TestAnEndWakesTheConductorOnceToCloseOut(t *testing.T) {
	for _, end := range []struct{ how, status, kind, note string }{
		{"merged", "done", "pr_merged", "Pull request target#1 was merged."},
		{"closed", "aborted", "pr_closed", "Pull request target#1 was closed without merging."},
	} {
		for _, conducted := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/conducted=%v", end.how, conducted), func(t *testing.T) {
				w := conducting(t)
				task := w.reviewing()
				if conducted {
					if status, _ := w.chat(task, "mine now"); status != 201 {
						t.Fatalf("take-over: %d", status)
					}
				}
				if end.how == "merged" {
					w.gh.Merge(1)
				} else {
					w.gh.Close(1)
				}
				w.until(end.status, func() bool { w.sync(); return w.taskStatus(task) == end.status })
				w.syncs(5)
				want := 0
				if conducted {
					want = 1
				}
				if n := w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1 AND kind = $2`, task, end.kind); n != want {
					t.Fatalf("%d %s reasons, want %d", n, end.kind, want)
				}
				if n := w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1 AND kind IN ('pr_merged', 'pr_closed')`, task); n != want {
					t.Errorf("%d close-out reasons in all, want %d", n, want)
				}
				if conducted {
					w.wokenWith(task, end.note)
				}
				if s := w.taskStatus(task); s != end.status {
					t.Errorf("the task is %s, want %s", s, end.status)
				}
			})
		}
	}
}

// Two pull requests, merged one after the other: each wakes the conductor
// once, though the second merge reads the first again.
func TestEachMergeWakesTheConductorOnce(t *testing.T) {
	w := conducting(t)
	scripted := w.lux.Decide
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		b := scripted(spec)
		if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] == "implement" {
			b.Commit = map[string]string{"target:API.md": "api\n", "web:PAGE.md": "page\n"}
		}
		return b
	}
	task := w.task()
	w.addWeb(task, "write")
	w.deliver(task)
	w.until("two pull requests", func() bool {
		return len(w.gh.Pulls()) == 1 && len(w.web.Pulls()) == 1 && w.taskStatus(task) == "review"
	})
	if status, _ := w.chat(task, "mine now"); status != 201 {
		t.Fatalf("take-over: %d", status)
	}
	w.gh.Merge(1)
	w.until("target's close-out", func() bool {
		w.sync()
		return w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1 AND kind = 'pr_merged'`, task) == 1
	})
	w.web.Merge(1)
	w.until("done", func() bool { w.sync(); return w.taskStatus(task) == "done" })
	w.syncs(3)
	if n := w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1 AND kind = 'pr_merged'`, task); n != 2 {
		t.Errorf("%d close-out reasons for two merges", n)
	}
	for _, key := range []string{"pr_merged:target:1", "pr_merged:web:1"} {
		if n := w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1 AND key = $2`, task, key); n != 1 {
			t.Errorf("%d reasons %s", n, key)
		}
	}
}

// One pull request closed while its sibling stays open is a person's
// decision; the sibling merged while the delivery waits on it still wakes
// the conductor once, to close out, though no step is listening.
func TestASiblingMergedDuringAnEscalationWakesTheConductorOnce(t *testing.T) {
	w := conducting(t)
	scripted := w.lux.Decide
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		b := scripted(spec)
		if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] == "implement" {
			b.Commit = map[string]string{"target:API.md": "api\n", "web:PAGE.md": "page\n"}
		}
		return b
	}
	task := w.task()
	w.addWeb(task, "write")
	w.deliver(task)
	w.until("two pull requests", func() bool {
		return len(w.gh.Pulls()) == 1 && len(w.web.Pulls()) == 1 && w.taskStatus(task) == "review"
	})
	if status, _ := w.chat(task, "mine now"); status != 201 {
		t.Fatalf("take-over: %d", status)
	}
	w.gh.Close(1)
	w.until("the closure escalated", func() bool {
		w.sync()
		return w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1 AND key = 'pr_closed:target:1'`, task) == 1 &&
			w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND status = 'waiting' AND step <> 'awaitPullRequest'`, task) == 1
	})
	w.web.Merge(1)
	w.syncs(5)
	if n := w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1 AND kind = 'pr_merged' AND key = 'pr_merged:web:1'`, task); n != 1 {
		t.Errorf("%d close-out reasons for web#1 merged during the escalation, want 1", n)
	}
	if n := w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1 AND kind IN ('pr_merged', 'pr_closed')`, task); n != 2 {
		t.Errorf("%d close-out reasons for two ended pull requests", n)
	}
}
