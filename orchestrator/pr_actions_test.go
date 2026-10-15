package orchestrator_test

// The GitHub actions dude takes for a task's agents on its own pull
// requests (request_review, resolve_thread, mark_ready,
// rerun_failed_checks), called with the conductor's Run token as its agent
// would, against the fake GitHub.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

// prActions is the task's pull_request.action events by an agent, oldest first.
func (w *world) prActions(task string) []string {
	rows, err := w.owner.Query(context.Background(), `SELECT payload->>'action' FROM events WHERE task_id = $1
		AND event_type = 'pull_request.action' AND actor_type = 'agent' ORDER BY cursor`, task)
	if err != nil {
		w.t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		w.t.Fatal(err)
	}
	return out
}

// A conductor asks for reviews, resolves a thread, marks its draft ready
// and re-runs failed checks on its task's pull request: each done on
// GitHub, each recorded as its Run's.
func TestTheConductorActsOnItsOwnPullRequest(t *testing.T) {
	w := conducting(t)
	task := w.reviewing()
	if status, out := w.chat(task, "Look after the pull request for me."); status >= 300 {
		t.Fatalf("chat: %d %v", status, out)
	}

	w.must(task, "request_review", `{"pr":"1","logins":["ana","@tom"]}`)
	if p := w.gh.Pull(1); !slices.Equal(p.Requested, []string{"ana", "tom"}) {
		t.Errorf("requested %v", p.Requested)
	}

	line := w.gh.LineComment(1, "ana", "FACTORY.md", "Say why.")
	w.gh.Set(func(s *fakegithub.Server) { s.Unresolved[1] = 1 })
	w.sync()
	w.must(task, "resolve_thread", fmt.Sprintf(`{"pr":"target#1","thread":"line-comment-%d"}`, line))
	var resolved bool
	w.gh.Set(func(s *fakegithub.Server) { resolved = s.ResolvedThreads[s.ThreadID(line)] })
	if !resolved {
		t.Error("the thread was not resolved on GitHub")
	}

	pull := w.gh.Pull(1)
	w.gh.Set(func(*fakegithub.Server) { pull.Draft = true })
	w.sync()
	w.must(task, "mark_ready", `{"pr":"1"}`)
	w.gh.Set(func(s *fakegithub.Server) {
		if !slices.Equal(s.MarkedReady, []int{1}) || pull.Draft {
			t.Errorf("marked ready %v, draft %v", s.MarkedReady, pull.Draft)
		}
	})

	w.gh.SetChecks("run:failure")
	w.sync()
	w.must(task, "rerun_failed_checks", `{"pr":"1"}`)
	w.gh.Set(func(s *fakegithub.Server) {
		if !slices.Equal(s.JobsRerun, []int64{77}) {
			t.Errorf("jobs re-run %v", s.JobsRerun)
		}
	})
	// Once per head, even when the re-run fails again.
	w.gh.SetChecks("run:failure")
	w.sync()
	w.refused(task, "rerun_failed_checks", `{"pr":"1"}`, "already")

	if got := w.prActions(task); !slices.Equal(got, []string{"request_review", "resolve_thread", "mark_ready", "rerun_failed_checks"}) {
		t.Errorf("recorded %v", got)
	}
}

// storedPR is the task's pull request as dude has it recorded: its state,
// checks rollup, unresolved threads, and who is asked for a review.
func (w *world) storedPR(task string) (state, checks string, unresolved int, requested []string) {
	w.t.Helper()
	var raw []byte
	if err := w.owner.QueryRow(context.Background(), `SELECT state::text, checks::text, unresolved_threads, reviews_json
		FROM pull_requests WHERE task_id = $1`, task).Scan(&state, &checks, &unresolved, &raw); err != nil {
		w.t.Fatal(err)
	}
	var reviews []forge.Review
	_ = json.Unmarshal(raw, &reviews)
	return state, checks, unresolved, forge.Requested(reviews)
}

// Each action the conductor takes on GitHub is in dude's record of the pull
// request when the call returns: no webhook, no reconciler pass after it.
// The read back records a person's comment once and signals it once,
// however often the pull request is read again.
func TestAConductorsActionIsInThePullRequestRecordWithoutAWebhook(t *testing.T) {
	w := conducting(t)
	task := w.reviewing()
	if status, out := w.chat(task, "Look after the pull request for me."); status >= 300 {
		t.Fatalf("chat: %d %v", status, out)
	}
	line := w.gh.LineComment(1, "ana", "FACTORY.md", "Say why.")
	pull := w.gh.Pull(1)
	w.gh.Set(func(s *fakegithub.Server) { s.Unresolved[1] = 1; pull.Draft = true })
	w.gh.SetChecks("run:failure")
	w.sync()
	if state, checks, unresolved, _ := w.storedPR(task); state != "draft" || checks != "failing" || unresolved != 1 {
		t.Fatalf("before: %s, checks %s, %d unresolved", state, checks, unresolved)
	}
	// Said after the last read: the read after the action is what records it.
	comment := w.gh.Comment(1, "bo", "Please rename the greeting.")

	w.must(task, "request_review", `{"pr":"1","logins":["cy"]}`)
	if _, _, _, requested := w.storedPR(task); !slices.Contains(requested, "cy") {
		t.Errorf("after request_review, recorded as asked: %v", requested)
	}
	w.must(task, "resolve_thread", fmt.Sprintf(`{"pr":"1","thread":"line-comment-%d"}`, line))
	if _, _, unresolved, _ := w.storedPR(task); unresolved != 0 {
		t.Errorf("after resolve_thread, %d unresolved recorded", unresolved)
	}
	w.must(task, "mark_ready", `{"pr":"1"}`)
	if state, _, _, _ := w.storedPR(task); state != "open" {
		t.Errorf("after mark_ready, recorded as %s", state)
	}
	w.must(task, "rerun_failed_checks", `{"pr":"1"}`)
	if _, checks, _, _ := w.storedPR(task); checks != "pending" {
		t.Errorf("after rerun_failed_checks, checks recorded as %s", checks)
	}

	feedback := fmt.Sprintf("issue-comment-%d", comment)
	signalled := func() int {
		return w.count(`SELECT count(*) FROM workflow_signals s JOIN workflow_runs r ON r.id = s.workflow_run_id
			WHERE r.task_id = $1 AND s.name = 'pr.feedback' AND s.idempotency_key LIKE '%' || $2 || '%'`, task, feedback)
	}
	recorded := func() int {
		return w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'pull_request.commented'
			AND payload->>'feedbackId' = $2`, task, feedback)
	}
	if r, s := recorded(), signalled(); r != 1 || s != 1 {
		t.Fatalf("bo's comment recorded %d times, signalled %d times, want once each", r, s)
	}
	for range 3 {
		w.sync()
	}
	if r, s := recorded(), signalled(); r != 1 || s != 1 {
		t.Errorf("after more reads, bo's comment recorded %d times, signalled %d times, want once each", r, s)
	}
}

// Two simultaneous reruns claim the head before GitHub is asked: exactly
// one request reaches GitHub, and the other is refused.
func TestSimultaneousRerunsAskGitHubOnce(t *testing.T) {
	w := conducting(t)
	task := w.reviewing()
	if status, out := w.chat(task, "Look after the pull request for me."); status >= 300 {
		t.Fatalf("chat: %d %v", status, out)
	}
	w.gh.SetChecks("run:failure")
	w.sync()
	spec := w.conductorSpecOf(task)
	results := make(chan int, 2)
	for range 2 {
		go func() {
			status, _ := w.callTool(w.syncer.Agent.ToolsURL, spec, "rerun_failed_checks", `{"pr":"1"}`)
			results <- status
		}()
	}
	statuses := []int{<-results, <-results}
	slices.Sort(statuses)
	if statuses[0] != 200 || statuses[1] == 200 {
		t.Fatalf("statuses %v, want one success", statuses)
	}
	w.gh.Set(func(s *fakegithub.Server) {
		if !slices.Equal(s.JobsRerun, []int64{77}) {
			t.Errorf("jobs re-run %v", s.JobsRerun)
		}
	})
}

// A scripted fixer makes the calls its feedback names: through the tool
// server, it resolves its own batch's thread and none other.
func TestAScriptedFixerResolvesItsThreadThroughTheToolServer(t *testing.T) {
	w := newWorld(t)
	w.withTools()
	task := w.reviewing()
	earlier := w.gh.LineComment(1, "bo", "FACTORY.md", "Not this one.")
	w.syncs(1)
	own := w.gh.LineComment(1, "ana", "FACTORY.md", "Please explain.")
	w.gh.EditComment(1, own, fmt.Sprintf("Please explain.\ntool: resolve_thread {\"pr\":\"1\",\"thread\":\"line-comment-%d\"}\n"+
		"tool: resolve_thread {\"pr\":\"1\",\"thread\":\"line-comment-%d\"}", own, earlier))
	w.until("the thread resolved", func() bool {
		w.sync()
		var done bool
		w.gh.Set(func(s *fakegithub.Server) { done = s.ResolvedThreads[s.ThreadID(own)] })
		return done
	})
	w.gh.Set(func(s *fakegithub.Server) {
		if s.ResolvedThreads[s.ThreadID(earlier)] {
			t.Error("resolved a thread outside the fixer's batch")
		}
	})
	_ = task
}

// Another task's pull request, or one of no task, is not the conductor's to
// act on; nor is a reviewer who is not a collaborator. Nothing reaches GitHub.
func TestTheConductorIsRefusedOutsideItsOwnPullRequest(t *testing.T) {
	w := conducting(t)
	task := w.reviewing()
	if status, out := w.chat(task, "Look after the pull request for me."); status >= 300 {
		t.Fatalf("chat: %d %v", status, out)
	}
	other := w.task()
	n := w.gh.OpenAsPerson("alice", "feature/other", "main", "Someone else's")
	mustExec(t, w.owner, `INSERT INTO pull_requests (id, organization_id, project_id, task_id, repository_id, number, url,
		head_branch, base_branch, head_sha, title, state) VALUES ('pr_other', $1, $2, $3, $4, $5, 'https://github.test/acme/target/pull/2',
		'feature/other', 'main', 'x', 'Other', 'open')`, w.org, w.project, other, w.repoID, n)
	line := w.gh.LineComment(n, "ana", "x.go", "Hm.")

	w.refused(task, "request_review", fmt.Sprintf(`{"pr":"%d","logins":["ana"]}`, n), "not a pull request of your task")
	w.refused(task, "mark_ready", fmt.Sprintf(`{"pr":"%d"}`, n), "not a pull request of your task")
	w.refused(task, "rerun_failed_checks", `{"pr":"9"}`, "not a pull request of your task")
	// A thread of the other pull request, named on this one.
	w.refused(task, "resolve_thread", fmt.Sprintf(`{"pr":"1","thread":"line-comment-%d"}`, line), "not a review thread of target#1")
	w.refused(task, "resolve_thread", fmt.Sprintf(`{"pr":"1","thread":"%s"}`, w.gh.ThreadID(line)), "not a review thread of target#1")

	w.gh.Set(func(s *fakegithub.Server) { s.Permissions["stranger"] = "none" })
	w.refused(task, "request_review", `{"pr":"1","logins":["stranger"]}`, "not collaborators")
	if reqs := w.reviewRequests(); len(reqs) != 0 {
		t.Errorf("GitHub was asked %v", reqs)
	}
	// One collaborator among them is asked; the other is named as refused.
	out := w.must(task, "request_review", `{"pr":"1","logins":["stranger","ana"]}`)
	if !slices.Equal(strs(out["logins"]), []string{"ana"}) || !slices.Equal(strs(out["refused"]), []string{"stranger"}) {
		t.Errorf("answered %v", out)
	}
	var resolved int
	w.gh.Set(func(s *fakegithub.Server) { resolved = len(s.ResolvedThreads) })
	if resolved != 0 {
		t.Errorf("%d threads resolved", resolved)
	}
}

// A request GitHub refuses still spends the tool's budget: past it, no
// call reaches GitHub. A success is counted once.
func TestRefusedReviewRequestsSpendTheBudget(t *testing.T) {
	w := conducting(t)
	task := w.reviewing()
	if status, out := w.chat(task, "Look after the pull request for me."); status >= 300 {
		t.Fatalf("chat: %d %v", status, out)
	}
	var reads atomic.Int64
	w.gh.Set(func(s *fakegithub.Server) {
		s.Permissions["stranger"] = "none"
		s.Intercept = func(r *http.Request) int {
			if strings.Contains(r.URL.Path, "/collaborators/") {
				reads.Add(1)
			}
			return 0
		}
	})
	w.must(task, "request_review", `{"pr":"1","logins":["ana"]}`)
	for range 19 {
		w.refused(task, "request_review", `{"pr":"1","logins":["stranger"]}`, "not collaborators")
	}
	if n := reads.Load(); n != 20 {
		t.Fatalf("%d permission reads before the budget, want 20", n)
	}
	for range 3 {
		w.refused(task, "request_review", `{"pr":"1","logins":["stranger"]}`, "limited to 20 calls")
	}
	if n := reads.Load(); n != 20 {
		t.Errorf("%d permission reads, want them to stop at 20", n)
	}
}

// A fixer answering pull request feedback resolves the threads in its
// own batch, and only those; it may not ask for reviews.
func TestAFixerResolvesOnlyTheThreadsItWasSent(t *testing.T) {
	w := newWorld(t)
	task := w.reviewing()
	mine := w.gh.LineComment(1, "ana", "FACTORY.md", "Say why.")
	theirs := w.gh.LineComment(1, "bo", "FACTORY.md", "Unrelated.")
	feedback := fmt.Sprintf(`[{"source":"review","id":"line-comment-%d","author":"ana","body":"Say why.","kind":"line_comment"}]`, mine)
	mustExec(t, w.owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, pr_feedback)
		VALUES ('run_fixer', $1, $2, $3, 1, 'running', 'fix', 'implementer', $4::jsonb)`, w.org, w.project, task, feedback)
	ref := delivery.RunRef{Org: w.org, ProjectID: w.project, TaskID: task, RunID: "run_fixer"}
	gh, err := forge.Resolver{DB: w.app}.For(context.Background(), w.org)
	if err != nil {
		t.Fatal(err)
	}
	act := func(in delivery.PRAction) error {
		var c delivery.PRActionCheck
		if err := w.app.InOrg(context.Background(), w.org, func(tx pgx.Tx) (err error) {
			c, err = delivery.CheckPRActionTx(context.Background(), tx, ref, gh, in)
			return err
		}); err != nil {
			return err
		}
		done, err := delivery.DoPRAction(context.Background(), gh, c)
		if err != nil {
			return err
		}
		return w.app.InOrg(context.Background(), w.org, func(tx pgx.Tx) error {
			_, err := delivery.RecordPRActionTx(context.Background(), tx, c, done)
			return err
		})
	}
	if err := act(delivery.PRAction{Action: delivery.ActionResolveThread, PR: "1", Thread: fmt.Sprintf("line-comment-%d", theirs)}); err == nil ||
		!strings.Contains(err.Error(), "not a thread in your feedback") {
		t.Errorf("resolving another thread: %v", err)
	}
	if err := act(delivery.PRAction{Action: delivery.ActionRequestReview, PR: "1", Logins: []string{"ana"}}); err == nil ||
		!strings.Contains(err.Error(), "not yours to do") {
		t.Errorf("a fixer asking for reviews: %v", err)
	}
	if err := act(delivery.PRAction{Action: delivery.ActionResolveThread, PR: "1", Thread: fmt.Sprintf("line-comment-%d", mine)}); err != nil {
		t.Fatalf("resolving its own thread: %v", err)
	}
	w.gh.Set(func(s *fakegithub.Server) {
		if !s.ResolvedThreads[s.ThreadID(mine)] || s.ResolvedThreads[s.ThreadID(theirs)] {
			t.Errorf("resolved %v", s.ResolvedThreads)
		}
	})
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = 'run_fixer' AND event_type = 'pull_request.action'
		AND payload->>'action' = 'resolve_thread'`); n != 1 {
		t.Errorf("%d resolutions recorded", n)
	}

	// It answers its own batch's comment in the thread, and no other.
	reply := func(to int64) error {
		var p delivery.ReplyPost
		if err := w.app.InOrg(context.Background(), w.org, func(tx pgx.Tx) (err error) {
			p, err = delivery.CheckReplyTx(context.Background(), tx, ref, gh,
				delivery.Reply{PR: "1", Text: "Done: it says why now.", InReplyTo: fmt.Sprintf("line-comment-%d", to)})
			return err
		}); err != nil {
			return err
		}
		_, err := delivery.PostReply(context.Background(), gh, p)
		return err
	}
	w.sync()
	if err := reply(theirs); err == nil || !strings.Contains(err.Error(), "one of the comments in your feedback") {
		t.Errorf("replying outside its batch: %v", err)
	}
	if err := reply(mine); err != nil {
		t.Fatalf("replying in its batch: %v", err)
	}
	lines := w.gh.Pull(1).LineComments
	if last := lines[len(lines)-1]; last.InReplyTo != mine || !strings.HasPrefix(last.Body, "Done: it says why now.") {
		t.Errorf("the reply: %+v", last)
	}
}
