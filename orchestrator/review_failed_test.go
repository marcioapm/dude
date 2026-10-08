package orchestrator_test

// A review round is complete only when every reviewer finished: one that
// failed found nothing because it read nothing, and must not count as a
// reviewer that found nothing.

import (
	"context"
	"fmt"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

// reviewerFails delivers a change two reviewers read (correctness and
// security); the first reviewer lux starts dies, every other one finds
// nothing. Returns the task and the failed reviewer's category.
func (w *world) reviewerFails() (string, string) {
	w.t.Helper()
	var reviews atomic.Int32
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		labels, _ := spec["labels"].(map[string]any)
		switch labels["dude.phase"] {
		case "implement":
			return fakelux.Behaviour{Commit: map[string]string{"auth/session.go": "package auth\n"}, Message: "work"}
		case "review":
			if reviews.Add(1) == 1 {
				return fakelux.Behaviour{Crash: true}
			}
		}
		return fakelux.Behaviour{Reply: "Looks good."}
	}
	wi := w.task()
	w.deliver(wi)
	w.until("the round to end", func() bool {
		return w.taskStatus(wi) == "awaiting_input" || len(w.gh.Pulls()) > 0
	})
	var category string
	_ = w.owner.QueryRow(context.Background(), `SELECT category FROM runs WHERE task_id = $1 AND phase = 'review'
		AND status = 'failed'`, wi).Scan(&category)
	return wi, category
}

func TestAFailedReviewerIsNotAClearReview(t *testing.T) {
	w := newWorld(t)
	wi, category := w.reviewerFails()
	if n := len(w.gh.Pulls()); n != 0 {
		t.Fatalf("a round with a failed reviewer opened %d pull requests", n)
	}
	if got := w.escalationReason(wi); got != "review_failed" {
		t.Fatalf("escalated for %q, want review_failed", got)
	}
	var detailCategory, actions string
	_ = w.owner.QueryRow(context.Background(), `SELECT payload->'detail'->>'category', payload->>'actions' FROM events
		WHERE task_id = $1 AND event_type = 'question.asked' AND payload->>'kind' = 'escalation'`, wi).Scan(&detailCategory, &actions)
	if category == "" || detailCategory != category {
		t.Errorf("the escalation names category %q, the failed reviewer was %q", detailCategory, category)
	}
	if actions != `["resume", "retry", "accept", "stop"]` {
		t.Errorf("a failed reviewer offers %s", actions)
	}
}

// A round already waited on when this version deploys has no reviewRunIds
// in its state, only the Runs still pending; one of its reviewers may have
// settled before. Either failing is still not a clear review.
func TestF6UpgradeFailedReviewProbe(t *testing.T) {
	cases := []struct {
		name string
		// Whether the first reviewer failed and was settled before the
		// deploy; the second then ends as second.
		settledFirst bool
		second       string
		failed       []int
	}{
		{"both fail after the deploy", false, "failed", []int{0, 1}},
		{"one failed before it, the other completes", true, "completed", []int{0}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			w.hangingReviews(taskCall)
			wi := w.task()
			w.deliver(wi)
			runs := w.reviewersOpen(wi, 2)
			pending := func() int {
				return w.count(`SELECT COALESCE(jsonb_array_length(state->'pendingRunIds'), 0) FROM workflow_runs WHERE task_id = $1`, wi)
			}
			end := func(id, status string) {
				mustExec(t, w.owner, `UPDATE runs SET status = $2::run_status, error = CASE WHEN $2 = 'failed' THEN 'lux lost it' END,
					ended_at = now() WHERE id = $1`, id, status)
			}
			if c.settledFirst {
				end(runs[0], "failed")
				w.until("the first reviewer settled", func() bool { return pending() == 1 })
			}
			mustExec(t, w.owner, `UPDATE workflow_runs SET state = state - 'reviewRunIds' WHERE task_id = $1`, wi)
			if !c.settledFirst {
				end(runs[0], "failed")
			}
			end(runs[1], c.second)
			w.until("the round to end", func() bool {
				return w.taskStatus(wi) == "awaiting_input" || len(w.gh.Pulls()) > 0
			})
			if got, n := w.escalationReason(wi), len(w.gh.Pulls()); got != "review_failed" || n != 0 {
				t.Fatalf("escalation=%q, PRs=%d; want review_failed and none", got, n)
			}
			var ids []string
			_ = w.owner.QueryRow(context.Background(), `SELECT ARRAY(SELECT jsonb_array_elements_text(payload->'detail'->'runIds'))
				FROM events WHERE task_id = $1 AND event_type = 'question.asked' AND payload->>'kind' = 'escalation'`, wi).Scan(&ids)
			var want []string
			for _, i := range c.failed {
				want = append(want, runs[i])
			}
			slices.Sort(ids)
			slices.Sort(want)
			if !slices.Equal(ids, want) {
				t.Errorf("the escalation's runIds %v, want %v", ids, want)
			}
		})
	}
}

// A round recovered without reviewRunIds is the current round's reviewers
// as they stand: a restarted reviewer counts as its replacement, not the
// aborted original, and an earlier round's reviewers, failed or clear,
// count for nothing.
func TestARecoveredRoundCountsOnlyItsCurrentReviewers(t *testing.T) {
	cases := []struct {
		name string
		// How the other category's reviewer ends, and whether it settled
		// before the deploy; then how the replacement ends.
		other        string
		otherSettled bool
		replacement  string
		// The escalation's runIds by name: "replacement", "other"; none is
		// a clear round reaching its pull request.
		failed []string
	}{
		{"the replacement fails", "completed", false, "failed", []string{"replacement"}},
		{"every current reviewer completes", "completed", false, "completed", nil},
		{"the other failed before the deploy, the replacement completes", "failed", true, "completed", []string{"other"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			ana := w.person("Ana")
			w.hangingReviews(taskCall)
			// Every reviewer hangs, the replacement too: each ends as the case says.
			hang := w.lux.Decide
			w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
				if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] == "review" {
					return fakelux.Behaviour{Hang: true, OpenCalls: [][3]string{taskCall}}
				}
				return hang(spec)
			}
			wi := w.task()
			w.deliver(wi)
			w.assignOwner(wi, ana)
			runs := w.reviewersOpen(wi, 2)
			restarted, other := runs[0], runs[1]
			ctx := context.Background()
			var wf string
			_ = w.owner.QueryRow(ctx, `SELECT id FROM workflow_runs WHERE task_id = $1`, wi).Scan(&wf)

			// The current round is the second: the first had one reviewer
			// fail and the other clear.
			if n := w.count(`SELECT count(*) FROM runs WHERE id = ANY($1) AND creation_key LIKE '%:review:0:' || category`, runs); n != 2 {
				t.Fatalf("%d of the round's reviewers are keyed as round 0", n)
			}
			mustExec(t, w.owner, `UPDATE runs SET creation_key = replace(creation_key, ':review:0:', ':review:1:') WHERE id = ANY($1)`, runs)
			mustExec(t, w.owner, `UPDATE workflow_runs SET state = jsonb_set(state, '{iteration}', '1') WHERE id = $1`, wf)
			for i, earlier := range []struct{ of, status string }{{restarted, "failed"}, {other, "completed"}} {
				mustExec(t, w.owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role,
						category, creation_key, error, created_at, ended_at)
					SELECT $1, organization_id, project_id, task_id, attempt, $2::run_status, phase, role, category,
						replace(creation_key, ':review:1:', ':review:0:'), CASE WHEN $2 = 'failed' THEN 'lux lost it' END,
						now() - interval '2 hours', now() - interval '1 hour'
					FROM runs WHERE id = $3`, fmt.Sprintf("run_earlier_%d_%s", i, wi), earlier.status, earlier.of)
			}

			if status, body := w.callAs(ana, "/internal/runs/"+restarted+"/restart", map[string]any{"note": "again"}); status != 200 {
				t.Fatalf("restart: %d %v", status, body)
			}
			var fresh string
			_ = w.owner.QueryRow(ctx, `SELECT COALESCE(replaced_by, '') FROM runs WHERE id = $1`, restarted).Scan(&fresh)
			if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND creation_key IS NULL`, fresh); n != 1 {
				t.Fatalf("the replacement %q is not a keyless Run", fresh)
			}
			w.until("the replacement on lux", func() bool { return w.luxRunOf(fresh) != "" })

			pending := func() int {
				return w.count(`SELECT COALESCE(jsonb_array_length(state->'pendingRunIds'), 0) FROM workflow_runs WHERE id = $1`, wf)
			}
			end := func(id, status string) {
				mustExec(t, w.owner, `UPDATE runs SET status = $2::run_status, error = CASE WHEN $2 = 'failed' THEN 'lux lost it' END,
					ended_at = now() WHERE id = $1`, id, status)
			}
			if c.otherSettled {
				end(other, c.other)
				w.until("the other reviewer settled", func() bool { return pending() == 1 })
			}
			// The deploy: the round's state no longer lists its reviewers.
			mustExec(t, w.owner, `UPDATE workflow_runs SET state = state - 'reviewRunIds' WHERE id = $1`, wf)
			if !c.otherSettled {
				end(other, c.other)
			}
			end(fresh, c.replacement)
			w.until("the round to end", func() bool {
				return w.taskStatus(wi) == "awaiting_input" || len(w.gh.Pulls()) > 0
			})

			reason, prs := w.escalationReason(wi), len(w.gh.Pulls())
			if len(c.failed) == 0 {
				if reason != "" || prs != 1 {
					t.Fatalf("escalation=%q, PRs=%d; want none and a pull request", reason, prs)
				}
				return
			}
			if reason != "review_failed" || prs != 0 {
				t.Fatalf("escalation=%q, PRs=%d; want review_failed and none", reason, prs)
			}
			var ids []string
			_ = w.owner.QueryRow(ctx, `SELECT ARRAY(SELECT jsonb_array_elements_text(payload->'detail'->'runIds'))
				FROM events WHERE task_id = $1 AND event_type = 'question.asked' AND payload->>'kind' = 'escalation'`, wi).Scan(&ids)
			named := map[string]string{"replacement": fresh, "other": other}
			var want []string
			for _, n := range c.failed {
				want = append(want, named[n])
			}
			slices.Sort(ids)
			slices.Sort(want)
			if !slices.Equal(ids, want) {
				t.Errorf("the escalation's runIds %v, want %v (aborted original %s)", ids, want, restarted)
			}
		})
	}
}

// Retry runs the failed reviewer's category again, and only it.
func TestRetryingAFailedReviewRunsOnlyThatReviewer(t *testing.T) {
	w := newWorld(t)
	wi, category := w.reviewerFails()
	if status, body := w.call("/internal/tasks/"+wi+"/decide", map[string]any{"action": "retry"}); status != 200 {
		t.Fatalf("retry: %d %v", status, body)
	}
	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review'`, wi); n != 3 {
		t.Errorf("%d reviewers, want the two of the round and the one retried", n)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review' AND category = $2 AND status = 'completed'`,
		wi, category); n != 1 {
		t.Errorf("the %s reviewer did not run again", category)
	}
}

// Accept goes on without that reviewer: a person's choice, recorded as theirs.
func TestAcceptingAFailedReviewGoesOnWithoutIt(t *testing.T) {
	w := newWorld(t)
	ana := w.person("Ana")
	wi, _ := w.reviewerFails()
	w.assignOwner(wi, ana)
	if status, body := w.callAs(ana, "/internal/tasks/"+wi+"/decide", map[string]any{"action": "accept"}); status != 200 {
		t.Fatalf("accept: %d %v", status, body)
	}
	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review'`, wi); n != 2 {
		t.Errorf("%d reviewers: accepting ran another", n)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'task.decided'
		AND payload->>'reason' = 'review_failed' AND payload->>'action' = 'accept' AND actor_id = $2`, wi, ana); n != 1 {
		t.Errorf("the acceptance is not recorded as Ana's")
	}
}
