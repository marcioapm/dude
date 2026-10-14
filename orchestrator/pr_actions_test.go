package orchestrator_test

// The GitHub actions dude takes for a task's agents on its own pull
// requests (request_review, resolve_thread, mark_ready,
// rerun_failed_checks), called with the conductor's Run token as its agent
// would, against the fake GitHub.

import (
	"context"
	"fmt"
	"slices"
	"strings"
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
	// Once per head.
	w.refused(task, "rerun_failed_checks", `{"pr":"1"}`, "already")

	if got := w.prActions(task); !slices.Equal(got, []string{"request_review", "resolve_thread", "mark_ready", "rerun_failed_checks"}) {
		t.Errorf("recorded %v", got)
	}
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
