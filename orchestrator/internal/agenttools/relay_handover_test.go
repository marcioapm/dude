package agenttools_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// relayWorld is a conducted task with a current writer and its live
// conductor, the writer's question for the conductor asked: the tokens and
// the question's id.
func relayWorld(t *testing.T, f *fixture) (writer, conductor, qid string) {
	t.Helper()
	writer, conductor = conductedWorld(t, f, `{"attempt":1,"decider":"conductor"}`)
	var ask struct{ QuestionID string }
	if status := f.postAs(t, writer, "ask_conductor", `{"question":"Which API shape?"}`, &ask); status != 200 || ask.QuestionID == "" {
		t.Fatalf("ask=%d", status)
	}
	return writer, conductor, ask.QuestionID
}

func (f *fixture) qn(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := f.owner.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A relay asks the person once, addressed to them, relaying the writer's
// question; it is refused for anything but a writer's question still with
// this conductor, and for a second relay while the first is open.
func TestRelayValidation(t *testing.T) {
	for _, mode := range []string{"ok", "wrong-task", "stale-attempt", "closed", "person-addressed", "writer-caller", "twice", "unknown", "escalation", "halted", "ended"} {
		t.Run(mode, func(t *testing.T) {
			f := setup(t)
			writer, conductor, qid := relayWorld(t, f)
			caller := conductor
			switch mode {
			case "wrong-task":
				mustExec(t, f.owner, `INSERT INTO tasks(id,organization_id,project_id,number,title,status) VALUES('wi_other',$1,$2,2,'Other','running')`, f.org, f.project)
				mustExec(t, f.owner, `INSERT INTO workflow_runs(id,organization_id,task_id,workflow_type,idempotency_key,status,step,state) VALUES('wf_other',$1,'wi_other','task.delivery','other','waiting','implement','{"attempt":1,"decider":"conductor"}')`, f.org)
				caller = f.runOn(t, "run_other_conductor", "wi_other", "implement", "conductor", "running")
				mustExec(t, f.owner, `UPDATE runs SET phase=NULL WHERE id='run_other_conductor'`)
			case "stale-attempt":
				mustExec(t, f.owner, `UPDATE workflow_runs SET state=state||'{"attempt":2}'`)
			case "closed":
				mustExec(t, f.owner, `UPDATE questions SET status='cancelled' WHERE id=$1`, qid)
			case "person-addressed":
				mustExec(t, f.owner, `UPDATE questions SET addressee='person' WHERE id=$1`, qid)
			case "writer-caller":
				caller = writer
				// Its own question open would refuse it first: closed here.
				mustExec(t, f.owner, `UPDATE questions SET status='cancelled' WHERE id=$1`, qid)
			case "twice":
				if status, out := f.post(t, conductor, "ask_person", fmt.Sprintf(`{"question":"Owner: A or B?","relay":%q}`, qid)); status != 200 {
					t.Fatalf("first relay %d %v", status, out)
				}
				// The conductor's own open question would refuse the second
				// before the relay check: a second conductor Run of the task
				// asks (the first ended its turn), as after a replacement.
				mustExec(t, f.owner, `UPDATE runs SET status='completed',ended_at=now() WHERE id='run_conductor'`)
				mustExec(t, f.owner, `UPDATE questions SET status='open' WHERE relays=$1`, qid)
				caller = f.runOn(t, "run_conductor2", f.item, "implement", "conductor", "running")
				mustExec(t, f.owner, `UPDATE runs SET phase=NULL WHERE id='run_conductor2'`)
			case "unknown":
				qid = "q_nope"
			case "escalation":
				// The task's escalation waits at decide, held by the person.
				mustExec(t, f.owner, `UPDATE workflow_runs SET step='decide', state=state||'{"escalation":{"reason":"stuck","holder":"person"}}'`)
			case "halted":
				mustExec(t, f.owner, `UPDATE workflow_runs SET state=state||'{"halt":{}}'`)
			case "ended":
				mustExec(t, f.owner, `UPDATE tasks SET status='done' WHERE id=$1`, f.item)
			}
			body := fmt.Sprintf(`{"question":"Owner: A or B?","relay":%q}`, qid)
			if mode == "escalation" {
				// A well-formed question about the escalation, but a relay.
				body = fmt.Sprintf(`{"questions":[{"question":"Stop?","choices":[{"label":"Stop","recommended":true}]}],"actions":["stop"],"relay":%q}`, qid)
			}
			status, out := f.post(t, caller, "ask_person", body)
			if mode == "ok" {
				if status != 200 {
					t.Fatalf("relay %d %v", status, out)
				}
				id, _ := out["questionId"].(string)
				if n := f.qn(t, `SELECT count(*) FROM questions WHERE id=$1 AND addressee='person' AND relays=$2 AND status='open' AND run_id='run_conductor'`, id, qid); n != 1 {
					t.Fatal("relay row")
				}
				if n := f.qn(t, `SELECT count(*) FROM events WHERE task_id=$1 AND event_type='question.asked' AND payload->>'relays'=$2`, f.item, qid); n != 1 {
					t.Fatalf("question.asked=%d", n)
				}
				if n := f.qn(t, `SELECT count(*) FROM tasks WHERE id=$1 AND status='awaiting_input'`, f.item); n != 1 {
					t.Fatal("task not awaiting the person")
				}
				return
			}
			if status != 422 {
				t.Fatalf("%s: relay=%d %v", mode, status, out)
			}
			msg := fmt.Sprint(out)
			want := "relay names a writer's question"
			switch mode {
			case "twice":
				want = "relayed to the person already"
			case "writer-caller":
				// A writer's ask_person is its ask_conductor, which has no relay.
				want = `unknown field "relay"`
			case "escalation":
				want = "not a question about the escalation"
			}
			if !strings.Contains(msg, want) {
				t.Fatalf("%s: %s, want %q", mode, msg, want)
			}
			if n := f.qn(t, `SELECT count(*) FROM questions WHERE relays IS NOT NULL AND status='open'`); n != map[bool]int{true: 1, false: 0}[mode == "twice"] {
				t.Fatalf("%s: open relays=%d", mode, n)
			}
		})
	}
}

// The conductor answering the writer's question itself while its relay is
// open withdraws the relay: the person is not left asked a dead question.
func TestConductorAnswerWithdrawsOpenRelay(t *testing.T) {
	f := setup(t)
	_, conductor, qid := relayWorld(t, f)
	status, out := f.post(t, conductor, "ask_person", fmt.Sprintf(`{"question":"Owner: A or B?","relay":%q}`, qid))
	if status != 200 {
		t.Fatalf("relay %d %v", status, out)
	}
	relay, _ := out["questionId"].(string)
	// The conductor's own question is open: its answer tool still works on
	// the writer's.
	if status, out := f.post(t, conductor, "answer_question", fmt.Sprintf(`{"id":%q,"answer":"A, per the docs"}`, qid)); status != 200 {
		t.Fatalf("answer %d %v", status, out)
	}
	if n := f.qn(t, `SELECT count(*) FROM questions WHERE id=$1 AND status='cancelled'`, relay); n != 1 {
		t.Fatal("relay left open")
	}
	if n := f.qn(t, `SELECT count(*) FROM events WHERE event_type='question.closed' AND payload->>'questionId'=$1 AND payload->>'by'='conductor'`, relay); n != 1 {
		t.Fatal("no question.closed for the relay")
	}
	if n := f.qn(t, `SELECT count(*) FROM tasks WHERE id=$1 AND status='awaiting_input'`, f.item); n != 0 {
		t.Fatal("task still waits on the person")
	}
	if n := f.qn(t, `SELECT count(*) FROM directives WHERE run_id='run_writer'`); n != 1 {
		t.Fatalf("writer directives=%d", n)
	}
}

func handOverWriterQuestion(t *testing.T, f *fixture, qid string, wait time.Duration) bool {
	t.Helper()
	var got bool
	if err := f.app.InOrg(context.Background(), f.org, func(tx pgx.Tx) error {
		var err error
		got, err = delivery.HandOverDueTx(context.Background(), tx, f.org, qid, wait)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return got
}

func dueIDs(t *testing.T, f *fixture, wait time.Duration) []string {
	t.Helper()
	rows, err := f.owner.Query(context.Background(), `SELECT q.id FROM `+delivery.ConductorQuestionsDue+` ORDER BY q.id`, wait.Seconds())
	if err != nil {
		t.Fatal(err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	return ids
}

// A writer's question is handed to the person once: past the wait, when
// its conductor failed since the ask, or when the quiet conductor has no
// model; never while young, relayed, or after it was settled.
func TestConductorQuestionHandOverPredicates(t *testing.T) {
	for _, mode := range []string{"deadline", "failed", "no-model", "young", "relayed", "answered", "failed-before"} {
		t.Run(mode, func(t *testing.T) {
			f := setup(t)
			_, conductor, qid := relayWorld(t, f)
			wait := time.Hour
			switch mode {
			case "deadline":
				mustExec(t, f.owner, `UPDATE questions SET asked_at=now()-interval '2 hours' WHERE id=$1`, qid)
			case "failed":
				mustExec(t, f.owner, `UPDATE runs SET status='failed',ended_at=now() WHERE id='run_conductor'`)
			case "failed-before":
				mustExec(t, f.owner, `UPDATE runs SET status='failed',ended_at=now()-interval '1 hour' WHERE id='run_conductor'`)
			case "no-model":
				mustExec(t, f.owner, `UPDATE workflow_runs SET state=state||'{"voice":"quiet"}'`)
				mustExec(t, f.owner, `UPDATE organizations SET default_agent_models='{}' WHERE id=$1`, f.org)
				mustExec(t, f.owner, `UPDATE projects SET agent_models='{}' WHERE id=$1`, f.project)
			case "relayed":
				mustExec(t, f.owner, `UPDATE questions SET asked_at=now()-interval '2 hours' WHERE id=$1`, qid)
				if status, out := f.post(t, conductor, "ask_person", fmt.Sprintf(`{"question":"Owner?","relay":%q}`, qid)); status != 200 {
					t.Fatalf("relay %d %v", status, out)
				}
			case "answered":
				mustExec(t, f.owner, `UPDATE questions SET asked_at=now()-interval '2 hours' WHERE id=$1`, qid)
				if status, out := f.post(t, conductor, "answer_question", fmt.Sprintf(`{"id":%q,"answer":"A"}`, qid)); status != 200 {
					t.Fatalf("answer %d %v", status, out)
				}
			}
			want := mode == "deadline" || mode == "failed" || mode == "no-model"
			if got := len(dueIDs(t, f, wait)) == 1; got != want {
				t.Fatalf("due=%v want %v", got, want)
			}
			// Twice: the second finds nothing to hand over.
			if got := handOverWriterQuestion(t, f, qid, wait); got != want {
				t.Fatalf("handed over=%v want %v", got, want)
			}
			if handOverWriterQuestion(t, f, qid, wait) {
				t.Fatal("handed over twice")
			}
			asked := f.qn(t, `SELECT count(*) FROM events WHERE task_id=$1 AND event_type='question.asked' AND payload->>'handedOverFrom'='conductor' AND payload->>'questionId'=$2`, f.item, qid)
			person := f.qn(t, `SELECT count(*) FROM questions WHERE id=$1 AND addressee='person' AND status='open'`, qid)
			news := f.qn(t, `SELECT count(*) FROM conductor_wakes WHERE task_id=$1 AND key='base-moved:handover:'||$2`, f.item, qid)
			if !want {
				if asked != 0 || person != 0 || news != 0 {
					t.Fatalf("asked=%d person=%d news=%d", asked, person, news)
				}
				return
			}
			if asked != 1 || person != 1 || news != 1 {
				t.Fatalf("asked=%d person=%d news=%d", asked, person, news)
			}
			if n := f.qn(t, `SELECT count(*) FROM tasks WHERE id=$1 AND status='awaiting_input'`, f.item); n != 1 {
				t.Fatal("task not awaiting the person")
			}
			if len(dueIDs(t, f, wait)) != 0 {
				t.Fatal("still due")
			}
			// The conductor can no longer answer it: it is the person's.
			if status, _ := f.post(t, conductor, "answer_question", fmt.Sprintf(`{"id":%q,"answer":"late"}`, qid)); status != 422 && mode != "failed" {
				t.Fatalf("conductor answered a handed-over question: %d", status)
			}
		})
	}
}

// The sweep's read is driven by the partial index questions_conductor_open_idx
// with many questions of other kinds and states.
func TestConductorQuestionSweepUsesPartialIndex(t *testing.T) {
	f := setup(t)
	relayWorld(t, f)
	mustExec(t, f.owner, `INSERT INTO questions (id, organization_id, task_id, run_id, prompt, status, addressee)
		SELECT 'q_bulk_'||g, $1, $2, 'run_writer', 'old', CASE WHEN g % 10 = 0 THEN 'open' ELSE 'answered' END::question_status, 'person'
		FROM generate_series(1, 50000) g`, f.org, f.item)
	mustExec(t, f.owner, `ANALYZE questions`)
	rows, err := f.owner.Query(context.Background(), `EXPLAIN SELECT q.id, q.organization_id FROM `+delivery.ConductorQuestionsDue+` ORDER BY q.asked_at, q.id`, 600.0)
	if err != nil {
		t.Fatal(err)
	}
	lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	plan := strings.Join(lines, "\n")
	outer := strings.SplitN(plan, "SubPlan", 2)[0]
	if !strings.Contains(outer, "questions_conductor_open_idx") || strings.Contains(outer, "Seq Scan on questions q") {
		t.Fatalf("the sweep does not use the partial index:\n%s", plan)
	}
}

// ToPersonTx's hand-over is idempotent by itself (its own WHERE
// addressee='conductor' AND status='open'), not only through the sweep's
// read: a second call hands nothing over and asks nobody again.
func TestHandOverIsIdempotentAtTheChokepoint(t *testing.T) {
	f := setup(t)
	_, _, qid := relayWorld(t, f)
	for i := range 2 {
		var got string
		if err := f.app.InOrg(context.Background(), f.org, func(tx pgx.Tx) error {
			var err error
			got, err = delivery.ToPersonTx(context.Background(), tx, delivery.ToPerson{
				Ref: delivery.RunRef{Org: f.org, ProjectID: f.project, TaskID: f.item}, Why: delivery.WhyHandover,
				HandOver: qid, Reason: "its conductor did not answer in time"})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if (got != "") != (i == 0) {
			t.Fatalf("call %d handed over %q", i, got)
		}
	}
	if n := f.qn(t, `SELECT count(*) FROM events WHERE event_type='question.asked' AND payload->>'questionId'=$1`, qid); n != 1 {
		t.Fatalf("question.asked=%d", n)
	}
}
