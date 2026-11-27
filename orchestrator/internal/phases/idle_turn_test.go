package phases

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// idleWorld is a receipt world whose Run is a conducted task's conductor,
// mid-turn, its delivery waiting on its decision after implement in voice,
// and nothing else of the task at work.
func newIdleWorld(t *testing.T, voice string) *wakeWorld {
	w := &wakeWorld{newReceiptWorld(t)}
	w.exec(`UPDATE tasks SET status = 'running' WHERE id = 'wi_'||$1`)
	w.exec(`UPDATE runs SET role = 'conductor', kind = 'agent', status = 'running', agent_busy_at = now() WHERE id = 'run_'||$1`)
	w.exec(`UPDATE runs SET status = 'completed' WHERE id = 'other_'||$1`)
	w.exec(`UPDATE directives SET delivered_at = now() WHERE id = 'dir_1' AND organization_id = $1`)
	if _, err := w.owner.Exec(context.Background(), `INSERT INTO workflow_runs (id, organization_id, task_id, workflow_type, idempotency_key, status, step, state)
		VALUES ('wf_'||$1, $1, 'wi_'||$1, 'task.delivery', 'delivery:'||$1, 'waiting', 'conductorDecision',
			jsonb_build_object('attempt', 1, 'decider', 'conductor', 'hadConductor', true, 'voice', $2::text,
				'decision', jsonb_build_object('point', 'after_implement')))`, w.org, voice); err != nil {
		t.Fatal(err)
	}
	w.tr.run.Role = delivery.RoleConductor
	return w
}

// said records the conductor's message, as the translator does.
func (w *wakeWorld) said(text string) {
	w.t.Helper()
	if err := w.s.DB.InOrg(context.Background(), w.org, func(tx pgx.Tx) error {
		return w.s.event(context.Background(), tx, w.tr.run, "agent.message", "agent", map[string]any{"text": text})
	}); err != nil {
		w.t.Fatal(err)
	}
}

// turnEnds ends the conductor's turn as lux's idle does, and starts the
// next one (busy) so a later end is a turn end of its own.
func (w *wakeWorld) turnEnds() {
	w.t.Helper()
	if err := w.s.DB.InOrg(context.Background(), w.org, func(tx pgx.Tx) error {
		return w.tr.idle(context.Background(), tx, w.s)
	}); err != nil {
		w.t.Fatal(err)
	}
}

func (w *wakeWorld) nextTurn() {
	w.t.Helper()
	w.exec(`UPDATE runs SET turn_done_at = NULL, waiting_since = NULL, agent_busy_at = now() WHERE id = 'run_'||$1`)
	// The idle wake was told to it.
	w.exec(`UPDATE conductor_wakes SET delivered_at = now() WHERE organization_id = $1`)
}

func (w *wakeWorld) idleWakes() []string {
	w.t.Helper()
	rows, err := w.owner.Query(context.Background(), `SELECT line FROM conductor_wakes WHERE organization_id = $1 AND kind = 'idle_turn' ORDER BY created_at`, w.org)
	if err != nil {
		w.t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		w.t.Fatal(err)
	}
	return out
}

func (w *wakeWorld) idleNotices() int {
	return w.count(`SELECT count(*) FROM events WHERE organization_id = $1 AND event_type = 'chat.notice' AND payload->>'about' = 'idle_turn'`)
}

func (w *wakeWorld) turnsDone() int {
	return w.count(`SELECT count(*) FROM events WHERE organization_id = $1 AND event_type = 'agent.session.stopped'`)
}

// A conductor whose turn ends with nothing that will wake it (no wait, no
// question, no other Run, no dispatch, no pull request) is woken once, told
// the start of its last message; in either voice.
func TestAnIdleConductorTurnIsWokenOnce(t *testing.T) {
	for _, voice := range []string{delivery.VoiceChatty, delivery.VoiceQuiet} {
		t.Run(voice, func(t *testing.T) {
			w := newIdleWorld(t, voice)
			w.said("Do you want this (3 runs), a single run, or a different approach? " + strings.Repeat("x", 400))
			w.turnEnds()
			wakes := w.idleWakes()
			if len(wakes) != 1 {
				t.Fatalf("%d idle wakes, want 1: %q", len(wakes), wakes)
			}
			if !strings.Contains(wakes[0], "«Do you want this (3 runs), a single run, or a different approach? xxx") ||
				!strings.Contains(wakes[0], "ask it with ask_person") || strings.Contains(wakes[0], strings.Repeat("x", 300)) {
				t.Errorf("wake line %q", wakes[0])
			}
			if n := w.idleNotices(); n != 0 {
				t.Errorf("%d notices", n)
			}
		})
	}
}

// A task's preview is a Run that never wakes the conductor, running or
// parked unused for the whole task: it does not hold an idle turn.
func TestAnIdleTurnBesideATasksPreviewIsWokenOnce(t *testing.T) {
	for name, status := range map[string]string{
		"running":       `'running', NULL`,
		"paused unused": `'paused', 'unused'`,
	} {
		t.Run(name, func(t *testing.T) {
			w := newIdleWorld(t, delivery.VoiceChatty)
			w.exec(`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, kind, status, dude_pause)
				VALUES ('prv_'||$1, $1, 'prj_'||$1, 'wi_'||$1, 1, 'preview', ` + status + `)`)
			w.said("The preview is up. Shall I open the pull request?")
			w.turnEnds()
			if got := w.idleWakes(); len(got) != 1 {
				t.Errorf("%d idle wakes, want 1: %q", len(got), got)
			}
		})
	}
}

// What the turn itself opened holds it, seen in the transaction that ends
// it: a wait_for, an ask_person; and so does a phase Run at work or a
// workflow dude follows.
func TestAConductorTurnWithSomethingPendingIsNotWoken(t *testing.T) {
	for name, setup := range map[string]string{
		"wait_for": `INSERT INTO conductor_waits (id, organization_id, task_id, conductor_run_id, attempt, conditions, deadline_at, since_cursor, call_key)
			VALUES ('cwt_'||$1, $1, 'wi_'||$1, 'run_'||$1, 1, '[]', now() + interval '1 hour', 0, 'k')`,
		"ask_person": `INSERT INTO questions (id, organization_id, task_id, run_id, prompt, options, addressee)
			VALUES ('q_'||$1, $1, 'wi_'||$1, 'run_'||$1, 'May I start it?', '["Yes","No"]', 'person')`,
		"question on the task": `INSERT INTO questions (id, organization_id, task_id, prompt, options, addressee)
			VALUES ('q_'||$1, $1, 'wi_'||$1, 'Which one?', '["A","B"]', 'person')`,
		"escalation": `UPDATE workflow_runs SET state = state || '{"escalation":{"reason":"review_rounds_exhausted"}}' WHERE task_id = 'wi_'||$1`,
		"phase run":  `UPDATE runs SET status = 'running', phase = 'implement' WHERE id = 'other_'||$1`,
		"dispatch": `INSERT INTO workflow_dispatches (id, organization_id, task_id, repository_id, workflow, ref, state)
			VALUES ('wfd_'||$1, $1, 'wi_'||$1, (SELECT id FROM repositories WHERE organization_id = $1 LIMIT 1), 'ci.yml', 'b', 'bound')`,
	} {
		t.Run(name, func(t *testing.T) {
			w := newIdleWorld(t, delivery.VoiceChatty)
			if name == "dispatch" {
				w.exec(`INSERT INTO repositories (id, organization_id, project_id, name, url) VALUES ('rep_'||$1, $1, 'prj_'||$1, 'api', 'https://github.com/o/api')`)
			}
			w.said("I'd like one more run. May I start it?")
			w.exec(setup)
			w.turnEnds()
			if got := w.idleWakes(); len(got) != 0 {
				t.Errorf("woken: %q", got)
			}
		})
	}
}

// The conductor's own open question holds a turn marked done by any path
// (the clone settle too), not only the idle one.
func TestAConductorTurnDoneWithItsQuestionOpenIsNotWoken(t *testing.T) {
	w := newIdleWorld(t, delivery.VoiceChatty)
	w.exec(`INSERT INTO questions (id, organization_id, task_id, run_id, prompt, options, addressee)
		VALUES ('q_'||$1, $1, 'wi_'||$1, 'run_'||$1, 'May I start it?', '["Yes","No"]', 'person')`)
	if err := w.s.DB.InOrg(context.Background(), w.org, func(tx pgx.Tx) error {
		return w.tr.turnDone(context.Background(), tx, w.s, false)
	}); err != nil {
		t.Fatal(err)
	}
	if n := w.turnsDone(); n != 1 {
		t.Fatalf("%d turn ends", n)
	}
	if got := w.idleWakes(); len(got) != 0 {
		t.Errorf("woken: %q", got)
	}
}

// Two idle turn ends in a row: the first wakes the conductor, the second
// says so in Chat once and wakes nobody; a turn that ends waiting ends the
// stretch, and the next idle end wakes it again.
func TestASecondIdleTurnIsToldInChatNotWokenAgain(t *testing.T) {
	w := newIdleWorld(t, delivery.VoiceChatty)
	w.said("Shall I run it?")
	w.turnEnds()
	w.nextTurn()
	w.said("Shall I run it, then?")
	w.turnEnds()
	if got := w.idleWakes(); len(got) != 1 {
		t.Fatalf("%d idle wakes, want 1: %q", len(got), got)
	}
	if n := w.idleNotices(); n != 1 {
		t.Fatalf("%d notices, want 1", n)
	}
	w.nextTurn()
	w.turnEnds()
	if got, n := w.idleWakes(), w.idleNotices(); len(got) != 1 || n != 1 {
		t.Fatalf("third idle end: %d wakes, %d notices", len(got), n)
	}
	// A turn that waits ends the stretch.
	w.nextTurn()
	w.exec(`INSERT INTO conductor_waits (id, organization_id, task_id, conductor_run_id, attempt, conditions, deadline_at, since_cursor, call_key)
		VALUES ('cwt_'||$1, $1, 'wi_'||$1, 'run_'||$1, 1, '[]', now() + interval '1 hour', 0, 'k')`)
	w.turnEnds()
	w.exec(`UPDATE conductor_waits SET ended_at = now(), ended_by = 'held' WHERE organization_id = $1`)
	w.nextTurn()
	w.turnEnds()
	if got := w.idleWakes(); len(got) != 2 {
		t.Errorf("%d idle wakes after the stretch ended, want 2", len(got))
	}
	if n := w.turnsDone(); n != 5 {
		t.Errorf("%d turn ends recorded, want 5", n)
	}
}

// A task that ended, and a Run that is not its conductor, are not woken.
func TestAnIdleTurnOfAnEndedTaskOrNotTheConductorWakesNobody(t *testing.T) {
	w := newIdleWorld(t, delivery.VoiceChatty)
	w.exec(`UPDATE tasks SET status = 'done' WHERE id = 'wi_'||$1`)
	w.turnEnds()
	if got := w.idleWakes(); len(got) != 0 {
		t.Errorf("ended task woken: %q", got)
	}

	w = newIdleWorld(t, delivery.VoiceChatty)
	w.exec(`UPDATE runs SET role = 'implementer', phase = 'implement' WHERE id = 'run_'||$1`)
	w.tr.run.Role, w.tr.run.Phase = "implementer", "implement"
	w.turnEnds()
	if got := w.idleWakes(); len(got) != 0 {
		t.Errorf("a phase Run's turn woke the conductor: %q", got)
	}
}

// heard records what started the conductor's turn: actor's typ event
// carrying directive dir_1 and, when delivered, dir_1's delivery to the
// Run, as the API and the translator record them.
func (w *wakeWorld) heard(typ, actor string, payload map[string]any, delivered bool) {
	w.t.Helper()
	if err := w.s.DB.InOrg(context.Background(), w.org, func(tx pgx.Tx) error {
		if err := w.s.event(context.Background(), tx, w.tr.run, typ, actor, payload); err != nil {
			return err
		}
		if !delivered {
			return nil
		}
		return w.s.event(context.Background(), tx, w.tr.run, evDirectiveDelivered, "system", map[string]any{"directiveId": "dir_1", "read": true})
	}); err != nil {
		w.t.Fatal(err)
	}
}

// A turn that answers a person (their Chat message, the message Talk it
// through started the conductor with, their steer) is conversation: it
// ends in prose with nothing pending and is not woken nor told in Chat.
// The next turn, started by nothing a person said, is.
func TestAConductorTurnAnsweringAPersonIsNotWoken(t *testing.T) {
	for name, heard := range map[string]struct {
		typ, actor string
		payload    map[string]any
		delivered  bool
	}{
		"chat message":                  {delivery.EvChatMessage, "person", map[string]any{"text": "What do you think?", "directiveId": "dir_1"}, true},
		"chat message by a key":         {delivery.EvChatMessage, "human", map[string]any{"text": "What do you think?", "directiveId": "dir_1"}, true},
		"talk it through":               {delivery.EvChatMessage, "person", map[string]any{"text": "Let's talk about the login page."}, false},
		"talk it through, no principal": {delivery.EvChatMessage, "system", map[string]any{"text": ""}, false},
		"steer":                         {delivery.EvRunSteered, "person", map[string]any{"text": "Keep it small.", "directiveId": "dir_1", "scope": "run"}, true},
	} {
		t.Run(name, func(t *testing.T) {
			w := newIdleWorld(t, delivery.VoiceChatty)
			w.heard(heard.typ, heard.actor, heard.payload, heard.delivered)
			w.said("I think one run is enough; the change is a single file.")
			w.turnEnds()
			if n := w.turnsDone(); n != 1 {
				t.Fatalf("%d turn ends", n)
			}
			if got, n := w.idleWakes(), w.idleNotices(); len(got) != 0 || n != 0 {
				t.Fatalf("a turn answering a person: %d wakes %q, %d notices", len(got), got, n)
			}
			w.nextTurn()
			w.said("Done thinking.")
			w.turnEnds()
			if got := w.idleWakes(); len(got) != 1 {
				t.Errorf("the next turn, started by nobody's words: %d idle wakes, want 1", len(got))
			}
		})
	}
}

// A conductor started or told by dude's own words (a pick-up's briefing, an
// auto-start's Talk it through, a message of dude's handed on from an
// ended conductor) did not hear a person: its idle turn is woken once.
func TestAConductorTurnStartedByDudesWordsIsWoken(t *testing.T) {
	for name, c := range map[string]struct {
		actor     string
		payload   map[string]any
		delivered bool
	}{
		"picked up":                {"dude", map[string]any{"text": "A person picked this task back up."}, false},
		"dude's message handed on": {"", map[string]any{"text": "Sync your branch.", "supersedes": "dir_0"}, false},
		"dude's message queued on": {"", map[string]any{"text": "Sync your branch.", "supersedes": "dir_0", "directiveId": "dir_1"}, true},
	} {
		t.Run(name, func(t *testing.T) {
			w := newIdleWorld(t, delivery.VoiceChatty)
			if err := w.s.DB.InOrg(context.Background(), w.org, func(tx pgx.Tx) error {
				if err := delivery.ChatEvent(context.Background(), tx, w.tr.run.ref(),
					delivery.Writer{ActorType: ledger.ActorSystem, ActorID: c.actor}, c.payload); err != nil {
					return err
				}
				if !c.delivered {
					return nil
				}
				return w.s.event(context.Background(), tx, w.tr.run, evDirectiveDelivered, "system", map[string]any{"directiveId": "dir_1", "read": true})
			}); err != nil {
				t.Fatal(err)
			}
			w.said("I have read the task. Shall I start over?")
			w.turnEnds()
			if got := w.idleWakes(); len(got) != 1 {
				t.Errorf("%d idle wakes, want 1: %q", len(got), got)
			}
		})
	}
}

// A person's answer to the conductor's ask_person is a choice, not a
// conversation: a turn it started that ends with nothing pending is woken
// once.
func TestAConductorTurnStartedByAnAnswerIsWoken(t *testing.T) {
	w := newIdleWorld(t, delivery.VoiceChatty)
	w.heard("question.answered", "person", map[string]any{"questionId": "q_1", "directiveId": "dir_1"}, true)
	w.said("Thanks, I will go with three runs.")
	w.turnEnds()
	if got := w.idleWakes(); len(got) != 1 {
		t.Fatalf("%d idle wakes, want 1: %q", len(got), got)
	}
	if n := w.idleNotices(); n != 0 {
		t.Errorf("%d notices", n)
	}
}
