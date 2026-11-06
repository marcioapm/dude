package agenttools_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// heldFixture is a conducted task with a live writer and conductor, and a
// second repository of the project ("web"), one of another project
// ("infra").
func heldFixture(t *testing.T, state string) (*fixture, string, string) {
	t.Helper()
	f := setup(t)
	writer, conductor := conductedWorld(t, f, state)
	mustExec(t, f.owner, `INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch)
		VALUES ('repo_web', $1, $2, 'web', 'git://h/acme/web.git', 'main')`, f.org, f.project)
	mustExec(t, f.owner, `INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_other', $1, 'O', 'prj_other', 'OTH')`, f.org)
	mustExec(t, f.owner, `INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch)
		VALUES ('repo_infra', $1, 'prj_other', 'infra', 'git://h/acme/infra.git', 'main')`, f.org)
	return f, writer, conductor
}

// conductedWorld is a running writer and its task's live conductor, the
// delivery's state state: their tokens.
func conductedWorld(t *testing.T, f *fixture, state string) (writer, conductor string) {
	t.Helper()
	writer = f.run(t, "run_writer", "implementer", "running")
	conductor = f.run(t, "run_conductor", "conductor", "running")
	mustExec(t, f.owner, `UPDATE runs SET phase=NULL WHERE id='run_conductor'`)
	questionDelivery(t, f, state)
	return writer, conductor
}

func (f *fixture) scalar(t *testing.T, sql string, args ...any) string {
	t.Helper()
	var out string
	if err := f.owner.QueryRow(context.Background(), sql, args...).Scan(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

// A writer's repository request on a conducted task is its conductor's:
// no repository.requested (nothing pushed), the task keeps running, one
// conductor wake. Not conducted, or with no conductor model, it is the
// person's as before.
func TestARepositoryRequestGoesToTheConductorFirst(t *testing.T) {
	for _, mode := range []string{"conducted", "policy", "no-model"} {
		t.Run(mode, func(t *testing.T) {
			f, writer, _ := heldFixture(t, `{"attempt":1,"decider":"conductor"}`)
			switch mode {
			case "policy":
				mustExec(t, f.owner, `UPDATE workflow_runs SET state=state||'{"decider":"policy"}'`)
			case "no-model":
				mustExec(t, f.owner, `UPDATE workflow_runs SET state=state||'{"voice":"quiet"}'`)
				mustExec(t, f.owner, `UPDATE runs SET status='completed',ended_at=now() WHERE id='run_conductor'`)
				mustExec(t, f.owner, `UPDATE organizations SET default_agent_models='{}' WHERE id=$1`, f.org)
				mustExec(t, f.owner, `UPDATE projects SET agent_models='{}' WHERE id=$1`, f.project)
			}
			var req struct{ RequestID, Next string }
			if status := f.postAs(t, writer, "request_repository", `{"repository":"web","reason":"read the client","wait":true}`, &req); status != 200 {
				t.Fatal(status)
			}
			addressee := f.scalar(t, `SELECT addressee FROM repository_requests WHERE id=$1`, req.RequestID)
			requested := f.n(t, `SELECT count(*) FROM events WHERE event_type='repository.requested' AND payload->>'requestId'=$1`, req.RequestID)
			routed := f.n(t, `SELECT count(*) FROM events WHERE event_type='repository.routed' AND payload->>'requestId'=$1`, req.RequestID)
			wakes := f.n(t, `SELECT count(*) FROM conductor_wakes WHERE key=$1`, "repository-request:"+req.RequestID)
			status := f.scalar(t, `SELECT status::text FROM tasks WHERE id=$1`, f.item)
			if mode == "conducted" {
				if addressee != "conductor" || requested != 0 || routed != 1 || wakes != 1 || status != "running" {
					t.Fatalf("addressee=%s requested=%d routed=%d wakes=%d status=%s", addressee, requested, routed, wakes, status)
				}
				return
			}
			if addressee != "person" || requested != 1 || routed != 0 || wakes != 0 {
				t.Fatalf("today's route: addressee=%s requested=%d routed=%d wakes=%d", addressee, requested, routed, wakes)
			}
		})
	}
}

// The conductor denies, approves read on a same-project repository, or
// forwards; it is refused write and another project's repository, a
// request it does not hold, and an unknown action.
func TestTheConductorDecidesARepositoryRequest(t *testing.T) {
	type tc struct {
		name, repo, action   string
		write, crossProject  bool
		status               int
		want, access, holder string
		requested            int
	}
	for _, c := range []tc{
		{name: "deny", repo: "web", action: "deny", status: 200, want: "denied", holder: "conductor"},
		{name: "approve read", repo: "web", action: "approve", status: 200, want: "approved", access: "read", holder: "conductor"},
		{name: "approve write refused", repo: "web", action: "approve", write: true, status: 422, want: "pending", holder: "conductor"},
		{name: "approve other project refused", repo: "web", action: "approve", crossProject: true, status: 422, want: "pending", holder: "conductor"},
		{name: "forward write", repo: "web", action: "forward", write: true, status: 200, want: "pending", holder: "person", requested: 1},
		{name: "bad action", repo: "web", action: "maybe", status: 422, want: "pending", holder: "conductor"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, writer, conductor := heldFixture(t, `{"attempt":1,"decider":"conductor"}`)
			var req struct{ RequestID string }
			if status := f.postAs(t, writer, "request_repository", fmt.Sprintf(`{"repository":%q,"write":%v,"reason":"needed"}`, c.repo, c.write), &req); status != 200 {
				t.Fatal(status)
			}
			if c.crossProject {
				mustExec(t, f.owner, `UPDATE repository_requests SET repository_id='repo_infra' WHERE id=$1`, req.RequestID)
			}
			status, body := f.post(t, conductor, "decide_repository_request", fmt.Sprintf(`{"id":%q,"action":%q,"note":"not this one"}`, req.RequestID, c.action))
			if status != c.status {
				t.Fatalf("status=%d %s", status, body)
			}
			got := f.scalar(t, `SELECT status::text FROM repository_requests WHERE id=$1`, req.RequestID)
			holder := f.scalar(t, `SELECT addressee FROM repository_requests WHERE id=$1`, req.RequestID)
			access := f.scalar(t, `SELECT COALESCE((SELECT access::text FROM task_repositories WHERE task_id=$1 AND repository_id='repo_web'), '')`, f.item)
			requested := f.n(t, `SELECT count(*) FROM events WHERE event_type='repository.requested' AND payload->>'requestId'=$1`, req.RequestID)
			if got != c.want || holder != c.holder || access != c.access || requested != c.requested {
				t.Fatalf("status=%s holder=%s access=%q requested=%d", got, holder, access, requested)
			}
			if c.want != "pending" {
				by := f.n(t, `SELECT count(*) FROM events WHERE event_type=$1 AND payload->>'by'='conductor' AND actor_id='run_conductor'`, "repository."+c.want)
				told := f.n(t, `SELECT count(*) FROM directives WHERE run_id='run_writer'`)
				if by != 1 || (c.want == "denied" && told != 1) {
					t.Fatalf("audit by conductor=%d directives=%d", by, told)
				}
			}
			if c.action == "forward" {
				// A second forward, or a decision, is refused: it is the person's now.
				if status, _ := f.post(t, conductor, "decide_repository_request", fmt.Sprintf(`{"id":%q,"action":"forward"}`, req.RequestID)); status != 422 {
					t.Fatalf("second forward=%d", status)
				}
				if n := f.n(t, `SELECT count(*) FROM events WHERE event_type='repository.requested' AND payload->>'requestId'=$1`, req.RequestID); n != 1 {
					t.Fatalf("requested=%d after a second forward", n)
				}
			}
		})
	}
}

// A request the conductor does not hold is refused: the person's, another
// task's, a decided one; and a non-conductor may not call the tool.
func TestTheConductorIsRefusedARepositoryRequestItDoesNotHold(t *testing.T) {
	for _, mode := range []string{"person", "decided", "writer"} {
		t.Run(mode, func(t *testing.T) {
			f, writer, conductor := heldFixture(t, `{"attempt":1,"decider":"conductor"}`)
			var req struct{ RequestID string }
			if status := f.postAs(t, writer, "request_repository", `{"repository":"web","reason":"needed"}`, &req); status != 200 {
				t.Fatal(status)
			}
			caller := conductor
			switch mode {
			case "person":
				mustExec(t, f.owner, `UPDATE repository_requests SET addressee='person' WHERE id=$1`, req.RequestID)
			case "decided":
				mustExec(t, f.owner, `UPDATE repository_requests SET status='denied' WHERE id=$1`, req.RequestID)
			case "writer":
				caller = writer
			}
			status, _ := f.post(t, caller, "decide_repository_request", fmt.Sprintf(`{"id":%q,"action":"deny"}`, req.RequestID))
			if status == 200 {
				t.Fatalf("refusal expected, got 200")
			}
			if n := f.n(t, `SELECT count(*) FROM events WHERE event_type='repository.denied'`); n != 0 {
				t.Fatalf("denied events %d", n)
			}
		})
	}
}

// Two conductor decisions at once: one wins, the other is refused.
func TestTwoRepositoryDecisionsOneWins(t *testing.T) {
	f, writer, conductor := heldFixture(t, `{"attempt":1,"decider":"conductor"}`)
	var req struct{ RequestID string }
	if status := f.postAs(t, writer, "request_repository", `{"repository":"web","reason":"needed"}`, &req); status != 200 {
		t.Fatal(status)
	}
	start := make(chan struct{})
	results := make(chan int, 2)
	var wg sync.WaitGroup
	for _, action := range []string{"approve", "deny"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			status, _ := f.post(t, conductor, "decide_repository_request", fmt.Sprintf(`{"id":%q,"action":%q}`, req.RequestID, action))
			results <- status
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	wins := 0
	for s := range results {
		if s == 200 {
			wins++
		}
	}
	decided := f.n(t, `SELECT count(*) FROM events WHERE event_type IN ('repository.approved','repository.denied')`)
	if wins != 1 || decided != 1 {
		t.Fatalf("wins=%d decided events=%d", wins, decided)
	}
}

// The handover: a conductor-held request or reviewer question past the
// wait, or whose conductor failed since, or has no model, goes to the
// person once; twice handed over is one push.
func TestHeldAsksAreHandedOverOnce(t *testing.T) {
	for _, why := range []string{"deadline", "failed", "no-model", "not-due"} {
		t.Run(why, func(t *testing.T) {
			f, writer, _ := heldFixture(t, `{"attempt":1,"decider":"conductor","questionsHeld":true}`)
			var req struct{ RequestID string }
			if status := f.postAs(t, writer, "request_repository", `{"repository":"web","reason":"needed","wait":true}`, &req); status != 200 {
				t.Fatal(status)
			}
			mustExec(t, f.owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, category)
				VALUES ('run_rev', $1, $2, $3, 1, 'completed', 'review', 'reviewer', 'correctness')`, f.org, f.project, f.item)
			mustExec(t, f.owner, `INSERT INTO review_questions (id, organization_id, task_id, run_id, n, category, question, addressee)
				VALUES ('rvq_1', $1, $2, 'run_rev', 1, 'correctness', 'UTC?', 'conductor')`, f.org, f.item)
			wait := time.Hour
			switch why {
			case "deadline":
				wait = time.Minute
				mustExec(t, f.owner, `UPDATE repository_requests SET created_at = now() - interval '2 minutes'`)
				mustExec(t, f.owner, `UPDATE review_questions SET created_at = now() - interval '2 minutes'`)
			case "failed":
				mustExec(t, f.owner, `UPDATE runs SET status='failed', ended_at=now() + interval '1 second' WHERE id='run_conductor'`)
			case "no-model":
				mustExec(t, f.owner, `UPDATE workflow_runs SET state=state||'{"voice":"quiet"}'`)
				mustExec(t, f.owner, `UPDATE runs SET status='completed',ended_at=now() WHERE id='run_conductor'`)
				mustExec(t, f.owner, `UPDATE organizations SET default_agent_models='{}' WHERE id=$1`, f.org)
				mustExec(t, f.owner, `UPDATE projects SET agent_models='{}' WHERE id=$1`, f.project)
			}
			for range 2 {
				handOver(t, f, wait)
			}
			requested := f.n(t, `SELECT count(*) FROM events WHERE event_type='repository.requested' AND payload->>'requestId'=$1`, req.RequestID)
			asked := f.n(t, `SELECT count(*) FROM events WHERE event_type='question.asked' AND payload->>'questionId'='rvq_1'`)
			holders := f.scalar(t, `SELECT (SELECT addressee FROM repository_requests) || '/' || (SELECT addressee FROM review_questions)`)
			status := f.scalar(t, `SELECT status::text FROM tasks WHERE id=$1`, f.item)
			if why == "not-due" {
				if requested+asked != 0 || holders != "conductor/conductor" || status != "running" {
					t.Fatalf("not due: requested=%d asked=%d holders=%s status=%s", requested, asked, holders, status)
				}
				return
			}
			if requested != 1 || asked != 1 || holders != "person/person" || status != "awaiting_input" {
				t.Fatalf("requested=%d asked=%d holders=%s status=%s", requested, asked, holders, status)
			}
		})
	}
}

// handOver runs the sweep's selection and handover, as phases does.
func handOver(t *testing.T, f *fixture, wait time.Duration) {
	t.Helper()
	for _, sel := range []struct {
		sql  string
		hand func(ctx context.Context, tx pgx.Tx, org, task, id string) (bool, error)
	}{
		{delivery.ReviewQuestionsDueSQL, func(ctx context.Context, tx pgx.Tx, org, task, id string) (bool, error) {
			return delivery.HandOverReviewQuestionTx(ctx, tx, org, task, id, wait)
		}},
		{delivery.RepositoryRequestsDueSQL, func(ctx context.Context, tx pgx.Tx, org, task, id string) (bool, error) {
			return delivery.HandOverRepositoryRequestTx(ctx, tx, org, task, id, wait)
		}},
	} {
		var due [][3]string
		if err := f.app.InSystem(context.Background(), "test", func(tx pgx.Tx) error {
			rows, err := tx.Query(context.Background(), sel.sql, wait.Seconds())
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var r [3]string
				if err := rows.Scan(&r[0], &r[1], &r[2]); err != nil {
					return err
				}
				due = append(due, r)
			}
			return rows.Err()
		}); err != nil {
			t.Fatal(err)
		}
		for _, r := range due {
			if err := f.app.InOrg(context.Background(), r[1], func(tx pgx.Tx) error {
				_, err := sel.hand(context.Background(), tx, r[1], r[2], r[0])
				return err
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
}
