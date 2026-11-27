package phases

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// sentLux is eventsLux that records what deliverDirectives sends.
type sentLux struct {
	*eventsLux
	sentMu sync.Mutex
	sent   []string
}

func (f *sentLux) Input(_ context.Context, _ string, in lux.InputRequest) error {
	f.sentMu.Lock()
	defer f.sentMu.Unlock()
	f.sent = append(f.sent, in.Text)
	return nil
}

func (f *sentLux) said() []string {
	f.sentMu.Lock()
	defer f.sentMu.Unlock()
	return append([]string(nil), f.sent...)
}

func (f *sentLux) stopsAsked() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stops
}

// setRun is what Get answers from now on.
func (f *sentLux) setRun(run *lux.Run) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.run = run
}

// withLux has the Syncer talk to a sentLux whose Get answers run (nil for
// eventsLux's default).
func (w *harnessWorld) withLux(run *lux.Run) *sentLux {
	fake := &sentLux{eventsLux: &eventsLux{run: run}}
	w.s.Lux = fake
	w.t.Cleanup(w.s.Stop)
	return fake
}

// placedAgain is lux's Run placed again in epoch 2 after epoch 1 exited.
func placedAgain() *lux.Run {
	return &lux.Run{ID: "lrun_1", State: "running", Epoch: 2,
		Placements: []lux.Placement{{Epoch: 1, State: "exited"}, {Epoch: 2, State: "running"}}}
}

// parkedForFailure: the talker's turn failed once, and it is parked for it
// and counted, with nothing held for a move.
func (w *harnessWorld) parkedForFailure() {
	w.t.Helper()
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.failed'
		AND payload->'failedTurns' = '1' AND payload->'kept' = 'true'`, w.run.ID); n != 1 {
		w.t.Errorf("%d run.failed kept with failedTurns 1, want 1", n)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND control = 'pause_graceful' AND dude_pause = $2
		AND (harness_state->>'failedTurns')::int = 1 AND NOT harness_state ? 'brokenTurn'`, w.run.ID, w.run.parkKind()); n != 1 {
		w.t.Errorf("not parked for its failed turn, counted once")
	}
}

// pausedParked: the talker is still paused, parked between turns.
func (w *harnessWorld) pausedParked() bool {
	w.t.Helper()
	return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND dude_pause = $2`, w.run.ID, w.run.parkKind()) == 1
}

// talkerWorld is newHarnessWorld's Run as a task's conductor, or a
// session's agent, mid-turn on epoch 1.
func talkerWorld(t *testing.T, role string) *harnessWorld {
	w := newHarnessWorld(t)
	switch role {
	case "conductor":
		mustOwner(t, w, `UPDATE runs SET phase = NULL, role = 'conductor', kind = 'agent' WHERE id = $1`, w.run.ID)
	case "brainstorm":
		mustOwner(t, w, `INSERT INTO sessions (id, organization_id, title) VALUES ('ssn_' || $1, $1, 'S')`, w.run.Org)
		mustOwner(t, w, `UPDATE runs SET phase = NULL, role = 'brainstorm', kind = 'agent', session_id = 'ssn_' || organization_id,
			project_id = NULL, task_id = NULL WHERE id = $1`, w.run.ID)
		w.run.SessionID, w.run.ProjectID, w.run.TaskID = "ssn_"+w.run.Org, "", ""
	}
	w.run.Phase, w.run.Role = "", role
	w.restart()
	return w
}

// turnCutOff is the agent's turn breaking with its container, on epoch 1.
func (w *harnessWorld) turnCutOff() {
	w.t.Helper()
	w.feed(processExited)
}

// A talker's turn that lux's move cuts off (a preemption, a drain, a
// migrate) is no failure: nothing is said failed or counted, the talker is
// not parked, and once lux runs the new placement the agent is told to
// carry on, through the sweep. An operator's stop, or an exit lux never
// explains, is the agent's failed turn, parked and counted.
func TestATalkersTurnLuxMovesIsCarriedOnNotParked(t *testing.T) {
	for _, role := range []string{"conductor", "brainstorm"} {
		for _, reason := range []string{"preempt", "drain", "migrate"} {
			t.Run(role+"/"+reason, func(t *testing.T) {
				w := talkerWorld(t, role)
				fake := w.withLux(nil)
				w.luxFrame(1, "state", map[string]any{"state": "stopping", "reason": reason})
				w.turnCutOff()
				w.luxFrame(1, "state", map[string]any{"state": "stopped", "reason": reason})
				w.luxFrame(2, "state", map[string]any{"state": "resuming", "reason": "auto-resume after " + reason})
				w.feed()
				if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type IN ('run.failed', 'run.parked')`, w.run.ID); n != 0 {
					t.Errorf("%d run.failed or run.parked for a move", n)
				}
				if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running' AND control = 'none'
					AND dude_pause IS NULL AND NOT harness_state ? 'failedTurns'`, w.run.ID); n != 1 {
					t.Errorf("the talker was parked or counted a failure for a move")
				}
				if n := w.count(`SELECT count(*) FROM directives WHERE run_id = $1 AND text = $2`, w.run.ID, movedNudge); n != 1 {
					t.Fatalf("%d carry-ons queued, want 1", n)
				}
				w.luxFrame(2, "state", map[string]any{"state": "running"})
				w.feed()
				if _, err := w.s.advance(context.Background(), w.swept()); err != nil {
					t.Fatal(err)
				}
				if got := fake.said(); len(got) != 1 || got[0] != movedNudge {
					t.Errorf("sent %q once lux runs it again, want the carry-on", got)
				}
			})
		}
		for _, c := range []struct{ name, stop string }{{"an operator's stop", "stop"}, {"no stop lux said", ""}} {
			t.Run(role+"/"+c.name, func(t *testing.T) {
				w := talkerWorld(t, role)
				if c.stop != "" {
					w.luxFrame(1, "state", map[string]any{"state": "stopping", "reason": c.stop})
				}
				w.turnCutOff()
				w.parkedForFailure()
				if n := w.count(`SELECT count(*) FROM directives WHERE run_id = $1`, w.run.ID); n != 0 {
					t.Errorf("%d carry-ons queued for a failure", n)
				}
			})
		}
	}
}

// parkedTalkerLuxMoved is a talker parked between turns (dude_pause
// conductor or session, its lux Run asked to stop) whose placement lux had
// already begun moving: lux placed it again itself, in epoch 2, and runs
// it. control is the Run's control: none, or a person's resume.
func parkedTalkerLuxMoved(t *testing.T, role, control string) (*harnessWorld, *sentLux) {
	w := talkerWorld(t, role)
	mustOwner(t, w, `UPDATE runs SET status = 'paused', control = $2, dude_pause = $3, lux_stop_reason = 'pause',
		lux_state = 'running', turn_done_at = NULL, agent_session_epoch = 1,
		harness_state = '{"luxStop": {"reason": "preempt", "epoch": 1}}'::jsonb WHERE id = $1`, w.run.ID, control, w.run.parkKind())
	return w, w.withLux(placedAgain())
}

// queue is a message for the talker, as Chat queues one.
func (w *harnessWorld) queue(text string) {
	w.t.Helper()
	mustOwner(w.t, w, `INSERT INTO directives (id, organization_id, task_id, run_id, text, created_at)
		VALUES ('dir_'||$1||'_'||md5($3), $1, NULLIF($2, ''), $4, $3, now() - interval '1 minute')`, w.run.Org, w.run.TaskID, text, w.run.ID)
}

// sweeps advances the Run as n sweeps would.
func (w *harnessWorld) sweeps(n int) {
	w.t.Helper()
	for range n {
		runs, err := w.s.due(context.Background())
		if err != nil {
			w.t.Fatal(err)
		}
		for _, r := range runs {
			if r.ID == w.run.ID {
				if _, err := w.s.advance(context.Background(), r); err != nil {
					w.t.Fatal(err)
				}
			}
		}
	}
}

// A parked talker lux placed again itself after a move is taken back up as
// lux runs it, and what was queued for it is delivered within two sweeps,
// in order, without lux being asked to stop or resume it: for a message
// (the talker's parked → resumed path) and for a person's Resume. With
// nothing queued and no Resume it stays parked, as any parked talker.
func TestAParkedTalkerLuxPlacedAgainHearsWhatIsQueued(t *testing.T) {
	for _, role := range []string{"conductor", "brainstorm"} {
		for _, control := range []string{"none", "resume"} {
			t.Run(role+"/control "+control, func(t *testing.T) {
				w, fake := parkedTalkerLuxMoved(t, role, control)
				w.queue("first")
				w.queue("second")
				w.sweeps(2)
				if got := fake.said(); len(got) != 2 || got[0] != "first" || got[1] != "second" {
					t.Errorf("sent %q, want first and second", got)
				}
				if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running' AND control = 'none'
					AND dude_pause IS NULL AND lux_stop_reason IS NULL`, w.run.ID); n != 1 {
					t.Errorf("not taken back up running")
				}
				if stops := fake.stopsAsked(); stops != 0 {
					t.Errorf("lux asked to stop %d times, want none", stops)
				}
			})
		}
		t.Run(role+"/nothing queued", func(t *testing.T) {
			w, fake := parkedTalkerLuxMoved(t, role, "none")
			w.sweeps(2)
			if !w.pausedParked() {
				t.Errorf("a parked talker with nothing for it was taken up")
			}
			if got := fake.said(); len(got) != 0 {
				t.Errorf("sent %q", got)
			}
		})
	}
}

// A conductor parked in that state is woken as any parked conductor: the
// wake's note is queued (WakeConductorTx) and reaches the agent, once,
// without lux being asked to stop it again. The state is reached as lux
// and the agent report it, the exit before lux's preemption.
func TestAWakeReachesAParkedConductorLuxPlacedAgain(t *testing.T) {
	w, fake := parkedTalkerPreempted(t, "conductor")
	w.s.WakeWindow = time.Second
	mustOwner(t, w, `INSERT INTO conductor_wakes (id, organization_id, task_id, kind, key, line, created_at)
		VALUES ('cwk_1', $1, $2, 'decision', 'k', 'the owner waived it', now() - interval '1 minute')`, w.run.Org, w.run.TaskID)
	if err := w.s.wakeConductors(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := w.count(`SELECT count(*) FROM directives WHERE run_id = $1 AND strpos(text, 'the owner waived it') > 0`, w.run.ID); n != 1 {
		t.Fatalf("%d wake notes queued, want 1", n)
	}
	w.sweeps(2)
	if got := fake.said(); len(got) != 1 || !strings.Contains(got[0], "the owner waived it") {
		t.Errorf("sent %q, want the wake's note", got)
	}
	if n := fake.stopsAsked(); n != 1 {
		t.Errorf("lux asked to stop %d times, want only the park's", n)
	}
}

// luxSays is lux's state event for placement epoch, applied and saved in
// one batch as the follower does.
func (w *harnessWorld) luxSays(epoch int, state, reason string) {
	w.t.Helper()
	ctx := context.Background()
	raw, _ := json.Marshal(map[string]any{"state": state, "reason": reason})
	if err := w.s.DB.InOrg(ctx, w.run.Org, func(tx pgx.Tx) error {
		if err := w.tr.apply(ctx, tx, w.s, lux.Frame{Kind: "lux", Epoch: epoch, EventType: "state", EventData: raw}); err != nil {
			return err
		}
		return w.tr.save(ctx, tx, "", 0)
	}); err != nil {
		w.t.Fatal(err)
	}
}

// parkedTalkerPreempted is a talker whose agent exited before lux said it
// was preempting the host: the exit is a failed turn, parked by the sweep
// (lux asked to stop, once); then lux reports the preemption (stopping,
// stopped), resumes the Run itself in epoch 2 and runs it.
func parkedTalkerPreempted(t *testing.T, role string) (*harnessWorld, *sentLux) {
	w := talkerWorld(t, role)
	mustOwner(t, w, `UPDATE runs SET agent_session_epoch = 1 WHERE id = $1`, w.run.ID)
	w.restart()
	fake := w.withLux(&lux.Run{ID: "lrun_1", State: "running", Epoch: 1,
		Placements: []lux.Placement{{Epoch: 1, State: "running"}}})
	w.turnCutOff()
	w.sweeps(1)
	if !w.pausedParked() {
		t.Fatalf("the failed turn did not park the talker")
	}
	w.luxSays(1, "stopping", "preempt")
	w.luxSays(1, "stopped", "preempt")
	w.luxSays(2, "resuming", "auto-resume after preempt")
	w.luxSays(2, "running", "")
	fake.setRun(placedAgain())
	return w, fake
}

// A talker re-placed that way, a run.failed behind it, hears exactly what
// was queued for it, in order: its failed turn adds no resume nudge.
func TestAPreemptedParkedTalkerHearsOnlyWhatIsQueued(t *testing.T) {
	for _, role := range []string{"conductor", "brainstorm"} {
		t.Run(role, func(t *testing.T) {
			w, fake := parkedTalkerPreempted(t, role)
			if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.failed'`, w.run.ID); n != 1 {
				t.Fatalf("%d run.failed, want 1", n)
			}
			w.queue("first")
			w.queue("second")
			w.sweeps(2)
			if got := fake.said(); len(got) != 2 || got[0] != "first" || got[1] != "second" {
				t.Errorf("sent %q, want first and second only", got)
			}
			if n := fake.stopsAsked(); n != 1 {
				t.Errorf("lux asked to stop %d times, want only the park's", n)
			}
		})
	}
}

// A talker lux moved and carried on whose agent then exits on its own in
// the next placement, lux stopping nothing, has failed its turn: parked
// and counted, not held as part of the earlier move.
func TestATalkersOwnExitAfterAMoveIsAFailure(t *testing.T) {
	for _, role := range []string{"conductor", "brainstorm"} {
		t.Run(role, func(t *testing.T) {
			w := talkerWorld(t, role)
			fake := w.withLux(nil)
			w.luxSays(1, "stopping", "preempt")
			w.turnCutOff()
			w.luxSays(1, "stopped", "preempt")
			w.luxSays(2, "resuming", "auto-resume after preempt")
			w.luxSays(2, "running", "")
			w.sweeps(1)
			if got := fake.said(); len(got) != 1 || got[0] != movedNudge {
				t.Fatalf("sent %q, want the carry-on", got)
			}
			w.feedAt(2, processExited)
			w.parkedForFailure()
		})
	}
}

// A talker parked on a person (an open question) is not taken up live for
// a note: it stays paused, sent nothing. Once the question is answered it
// resumes as any person-parked Run does: lux is asked to stop the
// placement it put up itself, and the resume follows the stop.
func TestAPersonParkedTalkerLuxPlacedAgainIsNotTakenUpLive(t *testing.T) {
	for _, status := range []string{"open", "answered"} {
		t.Run("question "+status, func(t *testing.T) {
			w, fake := parkedTalkerLuxMoved(t, "conductor", "none")
			mustOwner(t, w, `UPDATE runs SET dude_pause = 'person' WHERE id = $1`, w.run.ID)
			mustOwner(t, w, `INSERT INTO questions (id, organization_id, task_id, run_id, prompt, status)
				VALUES ('q_'||$1, $1, $2, $3, 'Sorted?', $4)`, w.run.Org, w.run.TaskID, w.run.ID, status)
			w.queue("a note")
			w.sweeps(2)
			if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND dude_pause = 'person'`, w.run.ID); n != 1 {
				t.Errorf("taken up live")
			}
			if got := fake.said(); len(got) != 0 {
				t.Errorf("sent %q", got)
			}
			// This fake's placement never records the stop, so each sweep
			// asks again.
			if n := fake.stopsAsked(); (status == "answered") != (n >= 1) {
				t.Errorf("lux asked to stop %d times, question %s", n, status)
			}
		})
	}
}
