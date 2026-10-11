package orchestrator_test

// The conductor steering a Run it conducts (design: the conductor, step 4),
// through the orchestrator's real code: the conductor's steer tool called
// with its Run's token, the syncer delivering to the fake lux, and the
// receipts that follow.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/agenttools"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

// steering is a conducted task whose implementer, started by its
// conductor, is running a long command: something live to steer.
type steering struct {
	*world
	task, conductor, implementer, spec string
}

func newSteering(t *testing.T, w *world) *steering {
	t.Helper()
	scripted := w.lux.Decide
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] == "implement" {
			return fakelux.Behaviour{Hang: true, Tools: []string{"bash"}, KeepToolsOpen: true}
		}
		return scripted(spec)
	}
	s := &steering{world: w, task: w.task()}
	s.conductor = w.talk(s.task)
	w.must(s.task, "start_phase", `{"phase":"implement"}`)
	w.until("the implementer running its command", func() bool {
		return w.count(`SELECT count(*) FROM events e JOIN runs r ON r.id = e.run_id
			WHERE r.task_id = $1 AND r.phase = 'implement' AND e.event_type = 'agent.tool.called'`, s.task) == 1
	})
	_ = w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE task_id = $1 AND phase = 'implement'`, s.task).Scan(&s.implementer)
	s.spec = w.conductorSpecOf(s.task)
	return s
}

// steer calls the steer tool as the conductor, without sweeping: what the
// test arranged in the database is what the tool sees.
func (s *steering) steer(run, text string) (int, map[string]any) {
	s.t.Helper()
	status, body := s.callTool(s.syncer.Agent.ToolsURL, s.spec, "steer", steerArgs(run, text))
	var out map[string]any
	_ = json.Unmarshal([]byte(body), &out)
	return status, out
}

func steerArgs(run, text string) string {
	b, _ := json.Marshal(map[string]any{"run": run, "text": text, "interrupt": false})
	return string(b)
}

// written is how many directives and run.steered events the task has.
func (s *steering) written() int {
	return s.count(`SELECT (SELECT count(*) FROM directives WHERE task_id = $1)
		+ (SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'run.steered')`, s.task)
}

// taken waits for the harness to take directive id.
func (s *steering) taken(id string) {
	s.t.Helper()
	s.until("taken", func() bool {
		return s.count(`SELECT count(*) FROM directives WHERE id = $1 AND accepted_at IS NOT NULL`, id) == 1
	})
}

// notesNaming is how many of the conductor's wake notes name steer id.
func (s *steering) notesNaming(id string) int {
	notes := 0
	for _, n := range s.woken(s.task) {
		if strings.Contains(n, "steer "+id) {
			notes++
		}
	}
	return notes
}

// The conductor steers its running implementer: a directive and a
// run.steered event that are the conductor's, not a person's; lux takes it
// while the command runs, and the agent reads it at its next step, in the
// same turn, nothing interrupted.
func TestTheConductorSteersItsRunningImplementer(t *testing.T) {
	s := newSteering(t, conducting(t))
	status, out := s.steer(s.implementer, "Use the staging database for this one.")
	if status != 200 || out["directiveId"] == nil || out["run"] != s.implementer {
		t.Fatalf("steer: %d %v", status, out)
	}
	id := out["directiveId"].(string)
	var by, text string
	var interrupt bool
	_ = s.owner.QueryRow(context.Background(), `SELECT COALESCE(conductor_run_id, ''), text, interrupt FROM directives
		WHERE id = $1 AND run_id = $2`, id, s.implementer).Scan(&by, &text, &interrupt)
	if by != s.conductor || text != "Use the staging database for this one." || interrupt {
		t.Errorf("the directive: by %q, %q, interrupt %v", by, text, interrupt)
	}
	var actorType, actorID string
	var payload map[string]any
	_ = s.owner.QueryRow(context.Background(), `SELECT actor_type, actor_id, payload FROM events
		WHERE run_id = $1 AND event_type = 'run.steered' AND payload->>'directiveId' = $2`, s.implementer, id).
		Scan(&actorType, &actorID, &payload)
	if actorType != "agent" || actorID != s.conductor || payload["by"] != "conductor" || payload["conductorRunId"] != s.conductor ||
		payload["attachments"] != nil {
		t.Errorf("run.steered by %s %s: %v", actorType, actorID, payload)
	}
	s.until("the harness to take it", func() bool {
		return s.count(`SELECT count(*) FROM directives WHERE id = $1 AND accepted_at IS NOT NULL AND lands = 'next_step'`, id) == 1
	})
	lr := s.luxRunOf(s.implementer)
	s.lux.FinishTools(lr)
	s.until("its next step to read it", func() bool {
		return s.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.directive.delivered'
			AND payload->>'directiveId' = $2 AND (payload->>'read')::boolean`, s.implementer, id) == 1
	})
	for _, r := range s.lux.Runs() {
		if r.ID == lr && (r.Interrupted != 0 || len(r.Inputs) != 1 || r.Inputs[0] != "Use the staging database for this one.") {
			t.Errorf("lux: interrupted %d, inputs %v", r.Interrupted, r.Inputs)
		}
	}
	if n := s.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running' AND turn_done_at IS NULL`, s.implementer); n != 1 {
		t.Error("reading the steer ended the implementer's turn")
	}
}

// Each refusal says why and writes nothing.
func TestTheConductorsSteerIsRefusedForWhatItDoesNotConduct(t *testing.T) {
	s := newSteering(t, conducting(t))
	ctx := context.Background()
	// Another task's Run, an ended Run, a branch preview, and an ending
	// one: rows the syncer never sees, since nothing sweeps meanwhile.
	other := s.task + "_other"
	mustExec(t, s.owner, `INSERT INTO tasks (id, organization_id, project_id, number, title) VALUES ($1, $2, $3, 990, 'Other')`,
		other, s.org, s.project)
	mustExec(t, s.owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role)
		VALUES ('run_otherstask', $1, $2, $3, 1, 'running', 'implement', 'implementer'),
		       ('run_ended', $1, $2, $4, 1, 'completed', 'review', 'reviewer'),
		       ('run_ending', $1, $2, $4, 1, 'running', 'fix', 'implementer')`, s.org, s.project, other, s.task)
	mustExec(t, s.owner, `UPDATE runs SET push_request_id = 'push_x' WHERE id = 'run_ending'`)
	mustExec(t, s.owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, kind)
		VALUES ('run_preview', $1, $2, $3, 1, 'running', 'preview')`, s.org, s.project, s.task)
	before := s.written()
	for _, c := range []struct{ run, text, want string }{
		{s.conductor, "hello", "is a conductor"},
		{"run_otherstask", "hello", "not a Run of your task"},
		{"run_nope", "hello", "not a Run of your task"},
		{"run_ended", "hello", "has ended (completed)"},
		{"run_ending", "hello", "is ending"},
		{"run_preview", "hello", "branch preview"},
		{s.implementer, "   ", "text is required"},
	} {
		status, out := s.steer(c.run, c.text)
		if msg, _ := out["error"].(string); status != 422 || !strings.Contains(msg, c.want) {
			t.Errorf("steer %s %q: %d %v, want refused saying %q", c.run, c.text, status, out, c.want)
		}
	}
	// An earlier attempt's: a later attempt exists.
	mustExec(t, s.owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role)
		VALUES ('run_later', $1, $2, $3, 2, 'completed', 'implement', 'implementer')`, s.org, s.project, s.task)
	if status, out := s.steer(s.implementer, "hello"); status != 422 ||
		!strings.Contains(out["error"].(string), "earlier attempt") {
		t.Errorf("an earlier attempt's Run: %d %v", status, out)
	}
	mustExec(t, s.owner, `DELETE FROM runs WHERE id = 'run_later'`)
	// The text bound, a person's: the tools' request bound refuses it first;
	// the tool's own check says it.
	long := strings.Repeat("x", delivery.SteerTextMax+1)
	if status, _ := s.steer(s.implementer, long); status != 413 {
		t.Errorf("a steer past the bound over HTTP: %d", status)
	}
	if err := s.app.InOrg(ctx, s.org, func(tx pgx.Tx) error {
		_, _, err := delivery.ConductSteer(ctx, tx, delivery.RunRef{Org: s.org, ProjectID: s.project, TaskID: s.task, RunID: s.conductor},
			s.implementer, long, false)
		return err
	}); err == nil || !strings.Contains(err.Error(), "at most") {
		t.Errorf("a steer past the bound: %v", err)
	}
	// The task done: read-only, in parked's words.
	mustExec(t, s.owner, `UPDATE tasks SET status = 'done' WHERE id = $1`, s.task)
	if status, out := s.steer(s.implementer, "hello"); status != 422 ||
		!strings.Contains(out["error"].(string), "read-only") {
		t.Errorf("a done task: %d %v", status, out)
	}
	if n := s.written(); n != before {
		t.Errorf("refusals wrote %d directives or events", n-before)
	}
}

// A conductor replaced after its call was authenticated, before the tool
// ran, steers nothing.
func TestASupersededConductorSteersNothing(t *testing.T) {
	w := conducting(t)
	paused, resume := make(chan struct{}), make(chan struct{})
	var once sync.Once
	tools := httptest.NewServer((&agenttools.Server{DB: w.app, Log: quiet, BeforeCall: func(tool string) {
		if tool == "steer" {
			once.Do(func() { close(paused); <-resume })
		}
	}}).Handler())
	t.Cleanup(tools.Close)
	w.syncer.Agent.ToolsURL = tools.URL
	s := newSteering(t, w)
	before := s.written()
	done := make(chan string, 1)
	go func() {
		status, body := w.callTool(tools.URL, s.spec, "steer", steerArgs(s.implementer, "late words"))
		if status != 422 {
			body = "status " + body
		}
		done <- body
	}()
	<-paused
	mustExec(t, w.owner, `UPDATE runs SET lux_state = 'terminated' WHERE id = $1`, s.conductor)
	if status, out := w.chat(s.task, "are you there?"); status != 201 {
		t.Fatalf("chat: %d %v", status, out)
	}
	if next, _, _ := w.conductor(s.task); next == s.conductor {
		t.Fatal("no replacement conductor")
	}
	close(resume)
	if body := <-done; !strings.Contains(body, "no longer this task's conductor") {
		t.Fatalf("the replaced conductor's steer: %s", body)
	}
	if n := s.written(); n != before {
		t.Errorf("the replaced conductor's steer wrote %d rows", n-before)
	}
}

// Steering takes no decision: a conductor steers under Deliver too, as a
// person does.
func TestTheConductorSteersUnderDeliverToo(t *testing.T) {
	s := newSteering(t, conducting(t))
	if status, out := s.steer(s.implementer, "while you decide"); status != 200 {
		t.Fatalf("under the conductor: %d %v", status, out)
	}
	if status, out := s.call("/internal/tasks/"+s.task+"/decider", map[string]any{"decider": "policy"}); status != 200 {
		t.Fatalf("hand back: %d %v", status, out)
	}
	if status, out := s.steer(s.implementer, "under Deliver"); status != 200 {
		t.Fatalf("under Deliver: %d %v", status, out)
	}
}

// A person's steer and the conductor's to the same Run are two directives,
// both delivered, in the order they were given.
func TestAPersonsSteerAndTheConductorsBothArriveInOrder(t *testing.T) {
	s := newSteering(t, conducting(t))
	if status, out := s.call("/internal/runs/"+s.implementer+"/steer", map[string]any{"text": "the person first"}); status != 201 {
		t.Fatalf("person: %d %v", status, out)
	}
	if status, out := s.steer(s.implementer, "then the conductor"); status != 200 {
		t.Fatalf("conductor: %d %v", status, out)
	}
	s.until("both taken", func() bool {
		return s.count(`SELECT count(*) FROM directives WHERE run_id = $1 AND accepted_at IS NOT NULL`, s.implementer) == 2
	})
	lr := s.luxRunOf(s.implementer)
	s.lux.FinishTools(lr)
	s.until("both read", func() bool {
		return s.count(`SELECT count(*) FROM directives WHERE run_id = $1 AND delivered_at IS NOT NULL`, s.implementer) == 2
	})
	for _, r := range s.lux.Runs() {
		if r.ID == lr && strings.Join(r.Inputs, "|") != "the person first|then the conductor" {
			t.Errorf("the agent read %v", r.Inputs)
		}
	}
	if n := s.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.steered' AND actor_type = 'agent'`,
		s.implementer); n != 1 {
		t.Errorf("%d steers attributed to an agent, want the conductor's one", n)
	}
}

// A person's Retry of the conductor's failed steer is the conductor's
// instruction again: read, the conductor is told, once, though the person
// sent it. New words superseding it are the person's own, and wake nobody.
func TestAPersonsRetryOfTheConductorsSteerWakesTheConductor(t *testing.T) {
	s := newSteering(t, conducting(t))
	_, out := s.steer(s.implementer, "use the staging database")
	d1, _ := out["directiveId"].(string)
	s.taken(d1)
	lr := s.luxRunOf(s.implementer)
	s.lux.FailInput(lr, d1, "the agent errored")
	s.wokenWith(s.task, "was not delivered: the agent errored")
	status, retry := s.call("/internal/runs/"+s.implementer+"/steer", map[string]any{"text": "use the staging database", "supersedes": d1})
	if status != 201 {
		t.Fatalf("retry: %d %v", status, retry)
	}
	d2, _ := retry["id"].(string)
	if n := s.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.steered' AND payload->>'directiveId' = $2
		AND actor_type <> 'agent' AND actor_id <> $3 AND NOT payload ? 'by'`, s.implementer, d2, s.conductor); n != 1 {
		t.Error("the retry is not the person's")
	}
	status, other := s.call("/internal/runs/"+s.implementer+"/steer", map[string]any{"text": "and the other thing", "supersedes": d1})
	if status != 201 {
		t.Fatalf("new words: %d %v", status, other)
	}
	if n := s.count(`SELECT count(*) FROM directives WHERE id = $1 AND conductor_run_id IS NULL`, other["id"]); n != 1 {
		t.Error("new words superseding the conductor's steer were linked to the conductor")
	}
	s.until("both taken", func() bool {
		return s.count(`SELECT count(*) FROM directives WHERE id IN ($1, $2) AND accepted_at IS NOT NULL`, d2, other["id"]) == 2
	})
	s.lux.FinishTools(lr)
	s.wokenWith(s.task, "read your steer "+d1)
	for range 3 {
		s.pump()
	}
	if w := s.steerWakes(); len(w) != 2 || !strings.HasPrefix(w[0], "steer_failed ") || !strings.HasPrefix(w[1], "steer_read ") {
		t.Errorf("steer wakes: %v, want the failure, then one read", w)
	}
}

// A person's Interrupt now of the conductor's steer lux already has goes
// as the interrupt alone: the words are read once, and the conductor is
// told once.
func TestAPersonsInterruptNowOfTheConductorsSteerWakesItOnce(t *testing.T) {
	s := newSteering(t, conducting(t))
	_, out := s.steer(s.implementer, "stop and use staging")
	d1, _ := out["directiveId"].(string)
	s.taken(d1)
	status, now := s.call("/internal/runs/"+s.implementer+"/steer", map[string]any{"text": "stop and use staging", "supersedes": d1,
		"interrupt": true})
	if status != 201 {
		t.Fatalf("interrupt now: %d %v", status, now)
	}
	s.until("both delivered", func() bool {
		return s.count(`SELECT count(*) FROM directives WHERE id IN ($1, $2) AND delivered_at IS NOT NULL`, d1, now["id"]) == 2
	})
	s.wokenWith(s.task, "read your steer "+d1)
	for range 3 {
		s.pump()
	}
	if w := s.steerWakes(); len(w) != 1 || !strings.HasPrefix(w[0], "steer_read ") {
		t.Errorf("steer wakes: %v, want one read", w)
	}
}

// A conductor's steer settled again — the same outcome, in another
// transaction, as a repeated receipt or a second terminal path would —
// wakes its conductor once: one reason, one note. A person's steer
// settled the same way wakes no conductor.
func TestASteerSettledTwiceWakesTheConductorOnce(t *testing.T) {
	settle := func(s *steering, id string, read bool) {
		s.t.Helper()
		for range 2 {
			if err := s.app.InOrg(context.Background(), s.org, func(tx pgx.Tx) error {
				return delivery.SteerSettledTx(context.Background(), tx, s.org, id, read, "the agent errored")
			}); err != nil {
				s.t.Fatal(err)
			}
		}
	}
	for _, c := range []struct {
		name, kind, says string
		read             bool
	}{
		{"read", "steer_read", "read your steer ", true},
		{"failed", "steer_failed", "was not delivered: the agent errored", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newSteering(t, conducting(t))
			_, out := s.steer(s.implementer, "settled twice")
			id, _ := out["directiveId"].(string)
			settle(s, id, c.read)
			s.wokenWith(s.task, c.says)
			for range 3 {
				s.pump()
			}
			if w := s.steerWakes(); len(w) != 1 || !strings.HasPrefix(w[0], c.kind+" ") {
				t.Errorf("steer wakes: %v, want one %s", w, c.kind)
			}
			if notes := s.notesNaming(id); notes != 1 {
				t.Errorf("%d notes name the steer, want 1", notes)
			}
		})
	}
	t.Run("a person's", func(t *testing.T) {
		s := newSteering(t, conducting(t))
		_, out := s.call("/internal/runs/"+s.implementer+"/steer", map[string]any{"text": "a person's words"})
		id, _ := out["id"].(string)
		settle(s, id, true)
		settle(s, id, false)
		for range 3 {
			s.pump()
		}
		if w := s.steerWakes(); len(w) != 0 {
			t.Errorf("a person's steer woke the conductor: %v", w)
		}
	})
}

// steerWakes are the task's steer wake reasons, "kind line" each.
func (s *steering) steerWakes() []string {
	s.t.Helper()
	rows, err := s.owner.Query(context.Background(), `SELECT kind || ' ' || line FROM conductor_wakes
		WHERE task_id = $1 AND kind LIKE 'steer_%' ORDER BY created_at`, s.task)
	if err != nil {
		s.t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		s.t.Fatal(err)
	}
	return out
}

// Read by the agent, the conductor's steer wakes the conductor once,
// with steer_read; a person's steer wakes nobody.
func TestTheConductorIsWokenOnceWhenItsSteerIsRead(t *testing.T) {
	s := newSteering(t, conducting(t))
	_, out := s.steer(s.implementer, "add a test for the empty case")
	id, _ := out["directiveId"].(string)
	s.call("/internal/runs/"+s.implementer+"/steer", map[string]any{"text": "a person's words"})
	s.until("both taken", func() bool {
		return s.count(`SELECT count(*) FROM directives WHERE run_id = $1 AND accepted_at IS NOT NULL`, s.implementer) == 2
	})
	if w := s.steerWakes(); len(w) != 0 {
		t.Fatalf("woken before it was read: %v", w)
	}
	s.lux.FinishTools(s.luxRunOf(s.implementer))
	note := s.wokenWith(s.task, "read your steer "+id)
	if !strings.Contains(note, "implement Run "+s.implementer) {
		t.Errorf("the note: %q", note)
	}
	for range 3 {
		s.pump()
	}
	if w := s.steerWakes(); len(w) != 1 || !strings.HasPrefix(w[0], "steer_read ") {
		t.Errorf("steer wakes: %v, want one steer_read", w)
	}
	if n := len(s.woken(s.task)); n != 1 {
		t.Errorf("%d notes, want 1", n)
	}
}

// A steer that will not reach the agent wakes the conductor with
// steer_failed and the reason: lux refusing it, or the Run finishing
// before the agent read it.
func TestTheConductorIsWokenWhenItsSteerFails(t *testing.T) {
	t.Run("lux refused it", func(t *testing.T) {
		s := newSteering(t, conducting(t))
		_, out := s.steer(s.implementer, "never heard")
		id, _ := out["directiveId"].(string)
		s.taken(id)
		s.lux.FailInput(s.luxRunOf(s.implementer), id, "the agent errored")
		note := s.wokenWith(s.task, "was not delivered: the agent errored")
		if !strings.Contains(note, id) {
			t.Errorf("the note: %q", note)
		}
		if w := s.steerWakes(); len(w) != 1 || !strings.HasPrefix(w[0], "steer_failed ") {
			t.Errorf("steer wakes: %v", w)
		}
	})
	t.Run("the run finished unread", func(t *testing.T) {
		s := newSteering(t, conducting(t))
		_, out := s.steer(s.implementer, "too late")
		id, _ := out["directiveId"].(string)
		s.taken(id)
		// Its turn ended with the steer taken and unread past the grace a
		// receipt has: the Run is collected, the steer failed.
		mustExec(t, s.owner, `UPDATE directives SET sent_at = now() - interval '121 seconds' WHERE id = $1`, id)
		mustExec(t, s.owner, `UPDATE runs SET turn_done_at = now() WHERE id = $1`, s.implementer)
		s.until("the implementer to complete", func() bool {
			return s.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'completed'`, s.implementer) == 1
		})
		s.wokenWith(s.task, "was not delivered: the run finished before the agent read it")
	})
}

// settledOnce checks the conductor was told once that directive id was
// not delivered, saying why: one steer_failed reason, one note naming it.
func (s *steering) settledOnce(id, why string) {
	s.t.Helper()
	s.wokenWith(s.task, "was not delivered: "+why)
	for range 3 {
		s.pump()
	}
	if n := s.count(`SELECT count(*) FROM directives WHERE id = $1 AND failed_at IS NOT NULL AND error = $2`, id, why); n != 1 {
		s.t.Errorf("the steer is not failed with %q", why)
	}
	if w := s.steerWakes(); len(w) != 1 || !strings.HasPrefix(w[0], "steer_failed ") {
		s.t.Errorf("steer wakes: %v, want one steer_failed", w)
	}
	if notes := s.notesNaming(id); notes != 1 {
		s.t.Errorf("%d notes name the steer, want 1", notes)
	}
}

// A Run that ends any way but finishing fails what its agent never read,
// and the conductor whose steer that was is told once: a pending Run whose
// submit fails, a running one a person aborts, one whose agent dies.
func TestTheConductorIsWokenWhenItsSteeredRunEnds(t *testing.T) {
	t.Run("the submit fails", func(t *testing.T) {
		w := conducting(t)
		// The implementer's role names no model tier: its submit fails
		// before lux has it. The conductor's, already submitted, is unmoved.
		mustExec(t, w.owner, `UPDATE organizations SET default_agent_models = default_agent_models - 'implementer' WHERE id = $1`, w.org)
		mustExec(t, w.owner, `UPDATE projects SET agent_models = agent_models - 'implementer' WHERE organization_id = $1`, w.org)
		s := &steering{world: w, task: w.task()}
		s.conductor = w.talk(s.task)
		s.spec = w.conductorSpecOf(s.task)
		w.must(s.task, "start_phase", `{"phase":"implement"}`)
		// The workflow alone: the syncer must not submit it before the steer.
		deadline := time.Now().Add(20 * time.Second)
		for s.implementer == "" && time.Now().Before(deadline) {
			if _, err := w.runtime.Tick(context.Background(), 10); err != nil {
				t.Fatal(err)
			}
			_ = w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE task_id = $1 AND phase = 'implement'
				AND status = 'pending'`, s.task).Scan(&s.implementer)
		}
		if s.implementer == "" {
			t.Fatal("no pending implementer")
		}
		status, out := s.steer(s.implementer, "before you start")
		if status != 200 {
			t.Fatalf("steer a pending Run: %d %v", status, out)
		}
		s.until("the implementer to fail", func() bool {
			return s.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'failed' AND lux_run_id IS NULL`, s.implementer) == 1
		})
		s.settledOnce(out["directiveId"].(string), "the run failed before the agent read it")
	})
	t.Run("a person aborts it", func(t *testing.T) {
		s := newSteering(t, conducting(t))
		_, out := s.steer(s.implementer, "aborted before read")
		id, _ := out["directiveId"].(string)
		s.taken(id)
		if status, body := s.call("/internal/runs/"+s.implementer+"/abort", map[string]any{}); status != 200 {
			t.Fatalf("abort: %d %v", status, body)
		}
		s.settledOnce(id, "the run was aborted before the agent read it")
	})
	t.Run("its agent dies", func(t *testing.T) {
		s := newSteering(t, conducting(t))
		_, out := s.steer(s.implementer, "dies before read")
		id, _ := out["directiveId"].(string)
		s.taken(id)
		s.lux.Crash(s.luxRunOf(s.implementer))
		s.until("the implementer to fail", func() bool {
			return s.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'failed'`, s.implementer) == 1
		})
		s.settledOnce(id, "the run failed before the agent read it")
	})
}

// A person aborts the Run while its follower is recording the agent's read
// of the conductor's steer: the receipt's batch has the directive, and the
// abort fails the Run's unread directives. Both commit, one after the
// other: the abort answers 200, the Run is aborted, and the read wins.
func TestAnAbortDuringASteersReceiptStopsTheRun(t *testing.T) {
	s := newSteering(t, conducting(t))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	// Registered after the world's: runs before its server closes.
	t.Cleanup(cancel)
	_, out := s.steer(s.implementer, "read while aborted")
	id, _ := out["directiveId"].(string)
	s.taken(id)
	// The receipt's batch is held after it has delivered the directive: its
	// conductor wake is the key another transaction is inserting.
	holder, err := pgx.Connect(ctx, s.owner.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = holder.Close(context.Background()) })
	hold, err := holder.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = hold.Rollback(context.Background()) })
	if _, err := hold.Exec(ctx, `INSERT INTO conductor_wakes (id, organization_id, task_id, kind, key, line)
		VALUES ('cwk_hold', $1, $2, 'steer_read', 'steer_read:' || $3, 'held')`, s.org, s.task, id); err != nil {
		t.Fatal(err)
	}
	var holderPID int
	if err := holder.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&holderPID); err != nil {
		t.Fatal(err)
	}
	s.lux.FinishTools(s.luxRunOf(s.implementer))
	receiptPID := s.blockedBy(ctx, holderPID, "the receipt's batch")

	type answer struct {
		status int
		body   map[string]any
		err    error
	}
	aborted := make(chan answer, 1)
	go func() {
		status, body, err := s.post(ctx, "/internal/runs/"+s.implementer+"/abort", map[string]any{})
		aborted <- answer{status, body, err}
	}()
	s.blockedBy(ctx, receiptPID, "the abort")
	if err := hold.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case a := <-aborted:
		if a.err != nil {
			t.Fatalf("abort: %v", a.err)
		}
		if a.status != 200 {
			t.Fatalf("abort: %d %v", a.status, a.body)
		}
	case <-ctx.Done():
		t.Fatalf("the abort did not answer within the test's deadline: %v", ctx.Err())
	}
	countIn := func(sql string, args ...any) int {
		t.Helper()
		var n int
		if err := s.owner.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := countIn(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'aborted'`, s.implementer); n != 1 {
		t.Error("the Run is not aborted")
	}
	if n := countIn(`SELECT count(*) FROM directives WHERE id = $1 AND delivered_at IS NOT NULL AND failed_at IS NULL`, id); n != 1 {
		t.Error("the steer is not read")
	}
	if n := countIn(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.directive.delivered'
		AND payload->>'directiveId' = $2 AND (payload->>'read')::boolean`, s.implementer, id); n != 1 {
		t.Errorf("%d read events, want 1", n)
	}
}

// blockedBy waits, without sweeping and until ctx ends, for a backend of
// the test's database to wait on a lock pid holds, and returns it.
func (s *steering) blockedBy(ctx context.Context, pid int, what string) int {
	s.t.Helper()
	for {
		var waiter int
		err := s.owner.QueryRow(ctx, `SELECT pid FROM pg_stat_activity
			WHERE datname = current_database() AND $1 = ANY (pg_blocking_pids(pid)) ORDER BY pid LIMIT 1`, pid).Scan(&waiter)
		switch {
		case err == nil:
			return waiter
		case ctx.Err() != nil:
			s.t.Fatalf("%s never waited on backend %d: %v", what, pid, ctx.Err())
		case !errors.Is(err, pgx.ErrNoRows):
			s.t.Fatalf("waiting for %s: %v", what, err)
		}
		select {
		case <-ctx.Done():
			s.t.Fatalf("%s never waited on backend %d: %v", what, pid, ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// post is world.call bounded by ctx, and safe off the test's goroutine:
// it returns transport and decoding errors instead of failing the test.
func (s *steering) post(ctx context.Context, path string, body any) (int, map[string]any, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", s.api+path, bytes.NewReader(b))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer svc")
	req.Header.Set("X-Dude-Organization", s.org)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer res.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil && !errors.Is(err, io.EOF) {
		return res.StatusCode, nil, fmt.Errorf("decoding the answer (status %d): %w", res.StatusCode, err)
	}
	return res.StatusCode, out, nil
}

// A paused Run reads its steers when it resumes: nothing fails them, and
// the conductor is not woken.
func TestAPausedRunsSteerStaysQueued(t *testing.T) {
	s := newSteering(t, conducting(t))
	if status, body := s.call("/internal/runs/"+s.implementer+"/pause", map[string]any{}); status != 200 {
		t.Fatalf("pause: %d %v", status, body)
	}
	s.until("paused", func() bool {
		return s.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused'`, s.implementer) == 1
	})
	status, out := s.steer(s.implementer, "when you are back")
	if status != 200 {
		t.Fatalf("steer a paused Run: %d %v", status, out)
	}
	for range 5 {
		s.pump()
	}
	if n := s.count(`SELECT count(*) FROM directives WHERE id = $1 AND failed_at IS NULL AND delivered_at IS NULL`,
		out["directiveId"]); n != 1 {
		t.Error("a paused Run's steer did not stay queued")
	}
	if w := s.steerWakes(); len(w) != 0 {
		t.Errorf("steer wakes for a paused Run: %v", w)
	}
}
