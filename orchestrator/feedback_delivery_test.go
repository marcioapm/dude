package orchestrator_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// feedback drafts an item as person (an api key's person) on the task's
// current attempt and submits it, as the backend does.
func (w *world) feedback(key, wi, id, body string) (int, map[string]any) {
	w.t.Helper()
	mustExec(w.t, w.owner, `INSERT INTO feedback_items (id, organization_id, task_id, attempt, author_person_id, body, anchor_kind, anchor)
		SELECT $1, $2, $3, task_attempt($3), person_id, $5, 'general', '{"kind":"general"}' FROM api_keys WHERE id = $4`,
		id, w.org, wi, key, body)
	return w.callAs(key, "/internal/tasks/"+wi+"/feedback/submit", map[string]any{"draftIds": []string{id}, "submissionKey": "k-" + id})
}

func (w *world) feedbackStatus(id string) string {
	var s string
	_ = w.owner.QueryRow(context.Background(), `SELECT status FROM feedback_items WHERE id = $1`, id).Scan(&s)
	return s
}

// At the pull request wait, a person's feedback sends a fixer the words;
// a fixer that leaves it unanswered is followed by another, up to the pull
// request fix bound. Until the item is handled the task is never ready to
// merge and dude will not merge it; a person resolving it lets it through.
func TestFeedbackAtThePullRequestWaitGoesToAFixerAndHoldsTheMerge(t *testing.T) {
	w := newWorld(t)
	ana := w.person("Ana")
	wi := w.reviewing()
	// The fixer is a real model, so its spec carries the prompt a real agent gets.
	w.onModel("implementer", "llm-impl")
	if code, body := w.feedback(ana, wi, "fbi_pr", "Log the retry count at info, not debug."); code != 201 || body["routedTo"] != "workflow" {
		t.Fatalf("submit: %d %v", code, body)
	}
	// Approved with the item open: nothing may call it ready from here on.
	w.gh.Review(1, "alice", "APPROVED")
	readyBefore := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'task.ready_to_merge'`, wi)
	var fixer string
	w.until("a fixer given the item", func() bool {
		_ = w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE task_id = $1 AND phase = 'fix' AND 'fbi_pr' = ANY(feedback_item_ids)`, wi).Scan(&fixer)
		return fixer != ""
	})
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.created'
		AND payload->'feedbackItemIds' ? 'fbi_pr' AND payload::text NOT LIKE '%retry count%'`, fixer); n != 1 {
		t.Errorf("the fixer's run.created does not name the item, or carries its words")
	}
	w.until("the fixer's prompt", func() bool { return w.promptOf(fixer) != "" })
	if p := w.promptOf(fixer); !strings.Contains(p, "### fbi_pr — Ana") || !strings.Contains(p, "> Log the retry count at info, not debug.") {
		t.Errorf("the fixer is not given the person's words:\n%s", p)
	}
	// The fixer commits but answers nothing: the item goes to another, and
	// past the bound a person is asked.
	w.until("pr_loop_exhausted", func() bool { w.sync(); return w.escalationReason(wi) == "pr_loop_exhausted" })
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'fix' AND 'fbi_pr' = ANY(feedback_item_ids)`, wi); n != scriptedPolicy().MaxPRFixIterations {
		t.Errorf("%d fixers given the item, want %d", n, scriptedPolicy().MaxPRFixIterations)
	}
	if s := w.feedbackStatus("fbi_pr"); s != "open" {
		t.Fatalf("an item its fixers left unanswered is %s", s)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'task.ready_to_merge'`, wi); n != readyBefore {
		t.Fatal("ready to merge with a person's feedback open")
	}
	w.sync()
	var prID string
	_ = w.owner.QueryRow(context.Background(), `SELECT id FROM pull_requests WHERE task_id = $1`, wi).Scan(&prID)
	if code, body := w.callAs(ana, "/internal/pull-requests/"+prID+"/merge", map[string]any{}); code != 409 ||
		!strings.Contains(fmt.Sprint(body), "feedback_open") {
		t.Fatalf("dude merged past open feedback: %d %v", code, body)
	}
	if len(w.gh.Merges) != 0 {
		t.Fatal("GitHub was asked to merge")
	}
	bo := w.person("Bo")
	if code, body := w.callAs(bo, "/internal/feedback/fbi_pr/resolve", map[string]any{}); code != 200 {
		t.Fatalf("resolve: %d %v", code, body)
	}
	if code, body := w.callAs(ana, "/internal/pull-requests/"+prID+"/merge", map[string]any{}); code != 200 {
		t.Fatalf("merge after the item was handled: %d %v", code, body)
	}
}

// Work that opened no pull request waits on its reader: feedback starts a
// fixer, a fixer that keeps not handling it escalates stuck on the item's
// id, and a person accepting resolves that item; mark done then finishes.
func TestFeedbackOnWorkReadyToReadStartsAFixerAndBoundsIt(t *testing.T) {
	w := newWorld(t)
	mustExec(t, w.owner, `INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch)
		VALUES ($1, $2, $3, 'web', 'git://127.0.0.1/acme/web.git', 'main')`, "repo_web_"+w.org, w.org, w.project)
	ana := w.person("Ana")
	wi := w.task()
	w.assignOwner(wi, ana)
	w.deliver(wi)
	w.until("ready to read", func() bool {
		return w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND step = 'awaitRead' AND status = 'waiting'`, wi) == 1
	})
	fixesBefore := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'fix'`, wi)
	if code, body := w.feedback(ana, wi, "fbi_read", "The summary should list the open risks first."); code != 201 {
		t.Fatalf("submit: %d %v", code, body)
	}
	// Mark done is refused while it is open.
	if code, _ := w.callAs(ana, "/internal/tasks/"+wi+"/done", map[string]any{}); code != 409 {
		t.Errorf("mark done with feedback open: %d", code)
	}
	w.until("stuck on the person's item", func() bool {
		var reason, ids string
		_ = w.owner.QueryRow(context.Background(), `SELECT COALESCE(state->'escalation'->>'reason', ''),
			COALESCE(state->'escalation'->'detail'->>'feedbackIds', '') FROM workflow_runs WHERE task_id = $1`, wi).Scan(&reason, &ids)
		return reason == "stuck" && strings.Contains(ids, "fbi_read")
	})
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'fix' AND 'fbi_read' = ANY(feedback_item_ids)`, wi); n < 2 ||
		w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'fix'`, wi) <= fixesBefore {
		t.Errorf("%d fixers given the item", n)
	}
	var detail string
	_ = w.owner.QueryRow(context.Background(), `SELECT state->'escalation'->'detail'->>'findingIds' FROM workflow_runs WHERE task_id = $1`, wi).Scan(&detail)
	if strings.Contains(detail, "fbi_read") {
		t.Error("a feedback id among the findings")
	}
	if code, body := w.callAs(ana, "/internal/tasks/"+wi+"/decide", map[string]any{"action": "accept"}); code != 200 {
		t.Fatalf("accept: %d %v", code, body)
	}
	w.until("the item resolved by the person and the work ready to read again", func() bool {
		return w.feedbackStatus("fbi_read") == "resolved" &&
			w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND step = 'awaitRead' AND status = 'waiting'`, wi) == 1
	})
	if code, body := w.callAs(ana, "/internal/tasks/"+wi+"/done", map[string]any{}); code != 200 {
		t.Fatalf("mark done: %d %v", code, body)
	}
	if n := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND status = 'completed'`, wi); n != 1 {
		t.Errorf("the reader's wait was not finished")
	}
}
