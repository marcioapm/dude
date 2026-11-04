package orchestrator_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
)

// directEditing is editing (a conducted task, implemented, its conductor
// woken after implement with a commit to publish) in a delivery that pushes
// its pull request fixes directly, with a pull request open on its branch
// at the implementer's head: a push dude made, not yet reviewed.
func directEditing(t *testing.T) (*editing, string) {
	t.Helper()
	e := newEditing(t)
	e.wokenWith(e.task, "after implement")
	head := e.pullRequestRow("open")
	return e, head
}

// unreviewedWords is the start of the I-3 refusal (pull-request-push-unreviewed).
const unreviewedWords = "which dude pushed and no review has covered yet"

// The pull request's last push, dude's, is unreviewed: the conductor's
// publish is refused saying so, naming start_phase review; once a review
// has started from that head, the publish goes to the pull request.
func TestDirectAConductorPublishOnAnOpenPullRequestIsAllowedOnceItsLastPushWasReviewed(t *testing.T) {
	e, head := directEditing(t)
	e.current()
	e.commit(map[string]string{"README.md": "# target\n\nfixed\n"})
	e.refused(e.task, "publish", `{"message":"a small fix"}`, unreviewedWords)
	e.refused(e.task, "publish", `{"message":"a small fix"}`, "start_phase review")
	e.refused(e.task, "publish", `{"message":"a small fix"}`, head[:7])
	if n := e.count(`SELECT count(*) FROM conductor_publishes WHERE task_id = $1`, e.task); n != 0 {
		t.Errorf("%d publishes recorded", n)
	}
	e.carriedOut(e.task, "decide", `{"action":"next"}`)
	if n := e.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review' AND status = 'completed'
		AND base_refs->>'target' = $2`, e.task, head); n == 0 {
		t.Fatalf("no review of %s\n%s", head, e.describeRuns())
	}
	id := e.publish()
	if status, why := e.published(id); status != delivery.PublishPublished {
		t.Fatalf("publish %s: %s", status, why)
	}
	moved := e.gh.SHA(e.branch)
	if moved == head || e.count(`SELECT count(*) FROM pull_requests WHERE id = 'pr_edit' AND head_sha = $1`, moved) != 1 {
		t.Fatalf("the pull request's head did not move to the publish: %s", moved)
	}
	// That publish is a push nobody reviewed: the next is refused.
	e.current()
	e.commit(map[string]string{"README.md": "# target\n\nfixed again\n"})
	e.refused(e.task, "publish", `{"message":"another"}`, unreviewedWords)
}

// A writer child the conductor starts is refused the same way while the
// pull request's last push is dude's and unreviewed, and admitted once a
// review covered it.
func TestDirectAWriterChildIsRefusedWhileThePullRequestsLastPushIsUnreviewed(t *testing.T) {
	e, head := directEditing(t)
	e.onModel("implementer", "fake/scripted")
	e.refused(e.task, "start_agent", `{"profile":"implementer","prompt":"Rename the greeting.","key":"rename"}`, unreviewedWords)
	e.carriedOut(e.task, "decide", `{"action":"next"}`)
	if n := e.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review' AND status = 'completed'
		AND base_refs->>'target' = $2`, e.task, head); n == 0 {
		t.Fatalf("no review of %s\n%s", head, e.describeRuns())
	}
	out := e.must(e.task, "start_agent", `{"profile":"implementer","prompt":"Rename the greeting.","key":"rename"}`)
	if id, _ := out["runId"].(string); id == "" {
		t.Fatalf("start_agent: %v", out)
	}
}

// A writer child the conductor starts once a review covered the pull
// request's last push publishes to the pull request's head: its runs.branch
// is the task branch (the one it pushes), not a candidate's, so it is
// direct, and the pull request's head moves to its commit.
func TestDirectAnAdmittedWriterChildPublishesToThePullRequestsHead(t *testing.T) {
	e, head := directEditing(t)
	e.onModel("implementer", "fake/scripted")
	e.carriedOut(e.task, "decide", `{"action":"next"}`)
	if n := e.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review' AND status = 'completed'
		AND base_refs->>'target' = $2`, e.task, head); n == 0 {
		t.Fatalf("no review of %s\n%s", head, e.describeRuns())
	}
	out := e.must(e.task, "start_agent", `{"profile":"implementer","prompt":"Rename the greeting.","key":"rename"}`)
	child, _ := out["runId"].(string)
	if child == "" {
		t.Fatalf("start_agent: %v", out)
	}
	if n := e.count(`SELECT count(*) FROM runs WHERE id = $1 AND branch = $2`, child, e.branch); n != 1 {
		t.Fatalf("the child's runs.branch is not the task branch\n%s", e.describeRuns())
	}
	e.until("the child ended", func() bool {
		return e.count(`SELECT count(*) FROM runs WHERE id = $1 AND status IN ('completed', 'failed', 'aborted')`, child) == 1
	})
	if n := e.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'completed'`, child); n != 1 {
		var why string
		_ = e.owner.QueryRow(context.Background(), `SELECT COALESCE(error, '') FROM runs WHERE id = $1`, child).Scan(&why)
		t.Fatalf("the child did not complete: %s\n%s", why, e.describeRuns())
	}
	moved := e.gh.SHA(e.branch)
	if moved == head {
		t.Fatalf("the task branch did not move from %s", head)
	}
	e.until("the pull request's head at the child's commit", func() bool {
		return e.count(`SELECT count(*) FROM pull_requests WHERE id = 'pr_edit' AND head_sha = $1`, moved) == 1 ||
			e.count(`SELECT count(*) FROM runs WHERE id = $1 AND heads->'target'->>'sha' = $2`, child, moved) == 1
	})
}

// A person's push is their own review: once the pull request shows a head
// a person pushed, the conductor publishes on it with no review of it.
func TestDirectAPersonsHeadIsNeverAnUnreviewedDudeHead(t *testing.T) {
	e, _ := directEditing(t)
	theirs := e.gh.CommitOnTop(e.branch, "A person's own fix")
	mustExec(t, e.owner, `UPDATE pull_requests SET head_sha = $1 WHERE id = 'pr_edit'`, theirs)
	e.current()
	e.commit(map[string]string{"README.md": "# target\n\nfixed\n"})
	id := e.publish()
	if status, why := e.published(id); status != delivery.PublishPublished {
		t.Fatalf("publish %s: %s", status, why)
	}
	if log := e.gh.Log(e.branch); !strings.HasPrefix(log[1], "A person's own fix") {
		t.Errorf("the publish is not on the person's commit: %v", log)
	}
}

// A conductor's writer child that resolves a person's feedback item with
// the commit it publishes to the pull request's head has the resolution
// settled as a direct one when it finishes: verified at once, not left
// pending for a promotion that never comes (finish's own == task branch).
func TestDirectAWriterChildsResolutionIsVerifiedAsItFinishes(t *testing.T) {
	e, head := directEditing(t)
	e.onModel("implementer", "fake/scripted")
	ana := e.person("Ana")
	mustExec(t, e.owner, `INSERT INTO feedback_batches (id, organization_id, task_id, attempt, author_person_id, submission_key)
		SELECT 'fbb_child', $2, $3, task_attempt($3), person_id, 'k' FROM api_keys WHERE id = $1`, ana, e.org, e.task)
	mustExec(t, e.owner, `INSERT INTO feedback_items (id, organization_id, task_id, attempt, batch_id, author_person_id, body, anchor_kind,
			status, submitted_at)
		SELECT 'fbi_child', $2, $3, task_attempt($3), 'fbb_child', person_id, 'Rename the greeting.', 'general', 'open', now()
		FROM api_keys WHERE id = $1`, ana, e.org, e.task)
	e.carriedOut(e.task, "decide", `{"action":"next"}`)
	if n := e.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review' AND status = 'completed'
		AND base_refs->>'target' = $2`, e.task, head); n == 0 {
		t.Fatalf("no review of %s\n%s", head, e.describeRuns())
	}
	// The tool server refuses feedback_resolve to a child (rChildToolsReadOnly);
	// the resolution is recorded here as that tool records it
	// (AgentResolveTx), so that finish's settle is what is tested.
	var mu sync.Mutex
	var child, answer string
	e.gh.Set(func(s *fakegithub.Server) {
		s.Intercept = func(r *http.Request) int {
			if r.Method != "PATCH" || !strings.HasSuffix(r.URL.Path, "/git/refs/heads/"+e.branch) {
				return 0
			}
			mu.Lock()
			defer mu.Unlock()
			if child == "" || answer != "" {
				return 0
			}
			body, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(body))
			var in struct{ SHA string }
			_ = json.Unmarshal(body, &in)
			err := e.app.InOrg(context.Background(), e.org, func(tx pgx.Tx) error {
				_, err := delivery.AgentResolveTx(context.Background(), tx, delivery.RunRef{Org: e.org, TaskID: e.task, RunID: child},
					delivery.Resolution{ItemID: "fbi_child", Commit: in.SHA})
				return err
			})
			answer = "resolved"
			if err != nil {
				answer = err.Error()
			}
			return 0
		}
	})
	out := e.must(e.task, "start_agent", `{"profile":"implementer","prompt":"Rename the greeting.","key":"rename"}`)
	id, _ := out["runId"].(string)
	if id == "" {
		t.Fatalf("start_agent: %v", out)
	}
	mu.Lock()
	child = id
	mu.Unlock()
	e.until("the child completed", func() bool {
		return e.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'completed'`, id) == 1
	})
	mu.Lock()
	got := answer
	mu.Unlock()
	if got != "resolved" {
		t.Fatalf("resolving as the child published: %q", got)
	}
	if n := e.count(`SELECT count(*) FROM feedback_items WHERE id = 'fbi_child' AND status = 'resolved'
		AND resolved_by_run_id = $1 AND resolved_verified_at IS NOT NULL`, id); n != 1 {
		var status string
		_ = e.owner.QueryRow(context.Background(), `SELECT status || ' verified=' || (resolved_verified_at IS NOT NULL)::text
			FROM feedback_items WHERE id = 'fbi_child'`).Scan(&status)
		t.Errorf("the child's resolution is %s, want resolved and verified as it finished", status)
	}
}
