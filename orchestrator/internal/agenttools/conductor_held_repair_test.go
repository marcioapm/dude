package agenttools_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// personAnswersConductor has the conductor ask the person (ask_person),
// the person answer it, and the answer path end the conductor's wait
// (delivery.EndConductorWait), as the API's answer does.
func personAnswersConductor(t *testing.T, f *fixture, conductor string) {
	t.Helper()
	if status, out := f.post(t, conductor, "ask_person", `{"question":"Owner: ship it?"}`); status != 200 {
		t.Fatalf("ask_person %d %v", status, out)
	}
	if got := f.scalar(t, `SELECT status::text FROM tasks WHERE id=$1`, f.item); got != "awaiting_input" {
		t.Fatalf("after ask_person: task %s", got)
	}
	mustExec(t, f.owner, `UPDATE questions SET status='answered', answer='yes', answered_at=now()
		WHERE run_id='run_conductor' AND status='open'`)
	if err := f.app.InOrg(context.Background(), f.org, func(tx pgx.Tx) error {
		return delivery.EndConductorWait(context.Background(), tx, f.org, f.project, f.item)
	}); err != nil {
		t.Fatal(err)
	}
}

// A blocking repository request the conductor holds is not a person's to
// answer: once the person answered the conductor's own question, the task
// goes back to running — it does not sit in Needs you with nothing in it.
// (The asking Run itself still waits on the request: PersonAsk.)
func TestAConductorHeldRequestDoesNotKeepThePersonWaiting(t *testing.T) {
	for _, mode := range []string{"no-request", "held-request", "person-request"} {
		t.Run(mode, func(t *testing.T) {
			f, writer, conductor := heldFixture(t, `{"attempt":1,"decider":"conductor"}`)
			var req struct{ RequestID string }
			if mode != "no-request" {
				if status := f.postAs(t, writer, "request_repository", `{"repository":"web","reason":"read the client","wait":true}`, &req); status != 200 {
					t.Fatal(status)
				}
				if mode == "person-request" {
					mustExec(t, f.owner, `UPDATE repository_requests SET addressee='person' WHERE id=$1`, req.RequestID)
				}
			}
			personAnswersConductor(t, f, conductor)
			got := f.scalar(t, `SELECT status::text FROM tasks WHERE id=$1`, f.item)
			want := "running"
			if mode == "person-request" {
				// The person's own request still waits on them.
				want = "awaiting_input"
			}
			if got != want {
				t.Fatalf("task %s, want %s", got, want)
			}
			if mode == "held-request" {
				// The writer itself still waits on it (its turn is held).
				if n := f.n(t, `SELECT count(*) FROM runs r WHERE r.id='run_writer' AND `+delivery.PersonAsk); n != 1 {
					t.Fatal("the asking Run no longer waits on its held request")
				}
			}
		})
	}
}

// The conductor's own request_repository is the person's to decide: it is
// never routed to the conductor itself.
func TestTheConductorsOwnRepositoryRequestGoesToThePerson(t *testing.T) {
	f, _, conductor := heldFixture(t, `{"attempt":1,"decider":"conductor"}`)
	var req struct{ RequestID string }
	if status := f.postAs(t, conductor, "request_repository", `{"repository":"web","reason":"read the client"}`, &req); status != 200 {
		t.Fatal(status)
	}
	addressee := f.scalar(t, `SELECT addressee FROM repository_requests WHERE id=$1`, req.RequestID)
	requested := f.n(t, `SELECT count(*) FROM events WHERE event_type='repository.requested' AND payload->>'requestId'=$1`, req.RequestID)
	routed := f.n(t, `SELECT count(*) FROM events WHERE event_type='repository.routed' AND payload->>'requestId'=$1`, req.RequestID)
	wakes := f.n(t, `SELECT count(*) FROM conductor_wakes WHERE key=$1`, "repository-request:"+req.RequestID)
	if addressee != "person" || requested != 1 || routed != 0 || wakes != 0 {
		t.Fatalf("addressee=%s requested=%d routed=%d wakes=%d", addressee, requested, routed, wakes)
	}
}

// More of heldDue's predicates: a conductor that failed before the ask is
// no reason to hand it over; a delivery no conductor decides any more is;
// one that ended is not (nothing is asked on it, the task left as it is);
// an item of an earlier attempt is never handed over.
func TestHeldAsksHandOverPredicates(t *testing.T) {
	for _, why := range []string{"failed-before", "policy", "delivery-ended", "earlier-attempt"} {
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
			want := false
			switch why {
			case "failed-before":
				// Failed an hour before both were raised (and not replaced).
				mustExec(t, f.owner, `UPDATE runs SET status='failed', ended_at=now() - interval '1 hour' WHERE id='run_conductor'`)
			case "policy":
				mustExec(t, f.owner, `UPDATE workflow_runs SET state=state||'{"decider":"policy"}'`)
				want = true
			case "delivery-ended":
				mustExec(t, f.owner, `UPDATE workflow_runs SET status='completed'`)
				mustExec(t, f.owner, `UPDATE review_questions SET created_at = now() - interval '2 hours'`)
				mustExec(t, f.owner, `UPDATE repository_requests SET created_at = now() - interval '2 hours'`)
			case "earlier-attempt":
				// Try again: attempt 2, the question of attempt 1's reviewer.
				mustExec(t, f.owner, `UPDATE workflow_runs SET state=state||'{"attempt":2}'`)
				mustExec(t, f.owner, `UPDATE review_questions SET created_at = now() - interval '2 hours'`)
			}
			before := f.scalar(t, `SELECT status::text FROM tasks WHERE id=$1`, f.item)
			for range 2 {
				handOver(t, f, time.Hour)
			}
			asked := f.n(t, `SELECT count(*) FROM events WHERE event_type='question.asked' AND payload->>'questionId'='rvq_1'`)
			requested := f.n(t, `SELECT count(*) FROM events WHERE event_type='repository.requested' AND payload->>'requestId'=$1`, req.RequestID)
			status := f.scalar(t, `SELECT status::text FROM tasks WHERE id=$1`, f.item)
			switch {
			case why == "earlier-attempt":
				if asked != 0 {
					t.Fatalf("an earlier attempt's question was handed over: asked=%d", asked)
				}
			case want:
				if asked != 1 || requested != 1 || status != "awaiting_input" {
					t.Fatalf("asked=%d requested=%d status=%s", asked, requested, status)
				}
			default:
				if asked+requested != 0 || status != before {
					t.Fatalf("asked=%d requested=%d status=%s (was %s)", asked, requested, status, before)
				}
			}
		})
	}
}

// The handover reads heldDue again under its locks: an item selected as
// due whose conductor was replaced by a running one before the handover's
// transaction is not handed over.
func TestAHeldAskNoLongerDueUnderTheLocksIsNotHandedOver(t *testing.T) {
	f, writer, _ := heldFixture(t, `{"attempt":1,"decider":"conductor","questionsHeld":true}`)
	var req struct{ RequestID string }
	if status := f.postAs(t, writer, "request_repository", `{"repository":"web","reason":"needed","wait":true}`, &req); status != 200 {
		t.Fatal(status)
	}
	mustExec(t, f.owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, category)
		VALUES ('run_rev', $1, $2, $3, 1, 'completed', 'review', 'reviewer', 'correctness')`, f.org, f.project, f.item)
	mustExec(t, f.owner, `INSERT INTO review_questions (id, organization_id, task_id, run_id, n, category, question, addressee)
		VALUES ('rvq_1', $1, $2, 'run_rev', 1, 'correctness', 'UTC?', 'conductor')`, f.org, f.item)
	mustExec(t, f.owner, `UPDATE runs SET status='failed', ended_at=now() + interval '1 second' WHERE id='run_conductor'`)
	wait := time.Hour
	var due [][3]string
	for _, sql := range []string{delivery.ReviewQuestionsDueSQL, delivery.RepositoryRequestsDueSQL} {
		rows, err := f.owner.Query(context.Background(), sql, wait.Seconds())
		if err != nil {
			t.Fatal(err)
		}
		got, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) ([3]string, error) {
			var v [3]string
			return v, r.Scan(&v[0], &v[1], &v[2])
		})
		if err != nil {
			t.Fatal(err)
		}
		due = append(due, got...)
	}
	if len(due) != 2 {
		t.Fatalf("due=%d, want both", len(due))
	}
	// Replaced before the handover runs: a new conductor, running.
	f.run(t, "run_conductor2", "conductor", "running")
	mustExec(t, f.owner, `UPDATE runs SET phase=NULL, created_at = now() + interval '1 minute' WHERE id='run_conductor2'`)
	if err := f.app.InOrg(context.Background(), f.org, func(tx pgx.Tx) error {
		if _, err := delivery.HandOverReviewQuestionTx(context.Background(), tx, f.org, f.item, "rvq_1", wait); err != nil {
			return err
		}
		_, err := delivery.HandOverRepositoryRequestTx(context.Background(), tx, f.org, f.item, req.RequestID, wait)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	asked := f.n(t, `SELECT count(*) FROM events WHERE event_type='question.asked' AND payload->>'questionId'='rvq_1'`)
	requested := f.n(t, `SELECT count(*) FROM events WHERE event_type='repository.requested' AND payload->>'requestId'=$1`, req.RequestID)
	holders := f.scalar(t, `SELECT (SELECT addressee FROM repository_requests) || '/' || (SELECT addressee FROM review_questions)`)
	if asked+requested != 0 || holders != "conductor/conductor" {
		t.Fatalf("asked=%d requested=%d holders=%s", asked, requested, holders)
	}
}

// A conductor-held reviewer question dies with its attempt: the delivery
// completed (merged, closed, a Start over) or the task closed withdraws it;
// a Stop alone (the delivery aborted, kept for a pick-up) keeps it, and it
// is handed over to nobody while stopped.
func TestHeldReviewQuestionsDieWithTheirAttempt(t *testing.T) {
	for _, end := range []string{"completed", "start-over", "closed", "stopped"} {
		t.Run(end, func(t *testing.T) {
			f, _, _ := heldFixture(t, `{"attempt":1,"decider":"conductor","questionsHeld":true}`)
			mustExec(t, f.owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, category)
				VALUES ('run_rev', $1, $2, $3, 1, 'completed', 'review', 'reviewer', 'correctness')`, f.org, f.project, f.item)
			mustExec(t, f.owner, `INSERT INTO review_questions (id, organization_id, task_id, run_id, n, category, question, addressee, created_at)
				VALUES ('rvq_1', $1, $2, 'run_rev', 1, 'correctness', 'UTC?', 'conductor', now() - interval '2 hours')`, f.org, f.item)
			switch end {
			case "completed":
				mustExec(t, f.owner, `UPDATE workflow_runs SET status='completed'`)
			case "start-over":
				mustExec(t, f.owner, `UPDATE workflow_runs SET status='aborted'`)
				mustExec(t, f.owner, `UPDATE tasks SET status='aborted' WHERE id=$1`, f.item)
				// startOver completes the stopped attempt's delivery.
				mustExec(t, f.owner, `UPDATE workflow_runs SET status='completed'`)
				mustExec(t, f.owner, `INSERT INTO workflow_runs(id,organization_id,task_id,workflow_type,idempotency_key,status,step,state)
					VALUES ('wf_2',$1,$2,'task.delivery','attempt-2','waiting','implement','{"attempt":2,"decider":"conductor"}')`, f.org, f.item)
				mustExec(t, f.owner, `UPDATE tasks SET status='running' WHERE id=$1`, f.item)
			case "closed":
				mustExec(t, f.owner, `UPDATE workflow_runs SET status='aborted'`)
				mustExec(t, f.owner, `UPDATE tasks SET status='aborted', resolution='wont_do' WHERE id=$1`, f.item)
			case "stopped":
				mustExec(t, f.owner, `UPDATE workflow_runs SET status='aborted'`)
				mustExec(t, f.owner, `UPDATE tasks SET status='aborted' WHERE id=$1`, f.item)
			}
			handOver(t, f, time.Hour)
			status := f.scalar(t, `SELECT status FROM review_questions WHERE id='rvq_1'`)
			want := "withdrawn"
			if end == "stopped" {
				want = "open"
			}
			if status != want {
				t.Fatalf("question %s, want %s", status, want)
			}
			if n := f.n(t, `SELECT count(*) FROM events WHERE event_type='question.asked' AND payload->>'questionId'='rvq_1'`); n != 0 {
				t.Fatalf("question.asked=%d", n)
			}
		})
	}
}

// The held probe stays bounded by what is live: ten thousand held reviewer
// questions on ended deliveries are withdrawn, so the partial index holds
// none of them and the sweep's read scans it, not the table.
func TestStaleHeldReviewQuestionsLeaveThePartialIndex(t *testing.T) {
	f, _, _ := heldFixture(t, `{"attempt":1,"decider":"conductor"}`)
	mustExec(t, f.owner, `INSERT INTO tasks (id, organization_id, project_id, number, title, status)
		SELECT 'wi_s'||g, $1, $2, 100+g, 't', 'running' FROM generate_series(1, 10000) g`, f.org, f.project)
	mustExec(t, f.owner, `INSERT INTO workflow_runs(id,organization_id,task_id,workflow_type,idempotency_key,status,step,state)
		SELECT 'wf_s'||g, $1, 'wi_s'||g, 'task.delivery', 's'||g, 'waiting', 'review', '{"attempt":1,"decider":"conductor"}' FROM generate_series(1, 10000) g`, f.org)
	mustExec(t, f.owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, category)
		SELECT 'run_s'||g, $1, $2, 'wi_s'||g, 1, 'completed', 'review', 'reviewer', 'correctness' FROM generate_series(1, 10000) g`, f.org, f.project)
	mustExec(t, f.owner, `INSERT INTO review_questions (id, organization_id, task_id, run_id, n, category, question, addressee, created_at)
		SELECT 'rvq_s'||g, $1, 'wi_s'||g, 'run_s'||g, 1, 'correctness', 'q', 'conductor', now() - interval '1 day' FROM generate_series(1, 10000) g`, f.org)
	// Their deliveries end: merged, closed, started over.
	mustExec(t, f.owner, `UPDATE workflow_runs SET status='completed' WHERE id LIKE 'wf_s%'`)
	mustExec(t, f.owner, `ANALYZE review_questions`)
	if n := f.n(t, `SELECT count(*) FROM review_questions WHERE status='open' AND addressee='conductor'`); n != 0 {
		t.Fatalf("stale held questions left in the partial index: %d", n)
	}
	rows, err := f.owner.Query(context.Background(), `EXPLAIN (ANALYZE, BUFFERS, FORMAT TEXT) `+delivery.ReviewQuestionsDueSQL, 600.0)
	if err != nil {
		t.Fatal(err)
	}
	lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	plan := strings.Join(lines, "\n")
	outer := strings.SplitN(plan, "SubPlan", 2)[0]
	if !strings.Contains(outer, "review_questions_conductor_open_idx") || strings.Contains(plan, "Seq Scan on review_questions") {
		t.Fatalf("the held probe does not read the partial index:\n%s", plan)
	}
	t.Logf("plan:\n%s", plan)
}
