package orchestrator_test

import (
	"strings"
	"testing"
	"time"
)

const overflow = "session/prompt: Internal error: prompt is too long: 212000 tokens > 200000 maximum (-32603)"

// failedTurn has the talker's next turn fail, sends text, and waits for
// the failure to be recorded on run.
func (tk *talking) failedTurn(run, text string) {
	tk.t.Helper()
	before := tk.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.failed'`, run)
	tk.lux.FailTurns(tk.luxRunOf(run), 1, overflow)
	if status, reached := tk.write(text); status != 200 || reached != run {
		tk.t.Fatalf("%q: %d reached %q, want %s", text, status, reached, run)
	}
	tk.until("the failed turn recorded", func() bool {
		return tk.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.failed'`, run) > before
	})
}

// parkedAfterFailure waits for the talker to be parked after a failed turn,
// its lux Run stopped and kept.
func (tk *talking) parkedAfterFailure(run string) {
	tk.t.Helper()
	tk.until("parked after its failed turn", func() bool {
		return tk.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND dude_pause = $2`, run, tk.park) == 1
	})
	if n := tk.count(`SELECT count(*) FROM runs WHERE id = $1 AND lux_stop_reason = 'cancel'`, run); n != 0 {
		tk.t.Errorf("the lux Run of a parked talker was cancelled")
	}
}

// A talker whose turn fails is parked, not ended: the person sees the
// failure, the lux Run is kept, and the next message resumes it, which
// answers. A third failure in a row, after two resumes, ends it as before
// and the next message gets a new Run; a turn that works in between
// starts the count again.
func TestATalkersFailedTurnParksItBoundedByTwoResumes(t *testing.T) {
	for kind, start := range talkers(t) {
		t.Run(kind+"/parks and resumes", func(t *testing.T) {
			tk, run := start(t)
			tk.failedTurn(run, "first try")
			if n := tk.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.failed'
				AND payload->>'error' LIKE '%prompt is too long%'`, run); n != 1 {
				t.Errorf("%d run.failed carrying the agent's error, want 1", n)
			}
			tk.parkedAfterFailure(run)
			// What the person reads (run.failed kept: "Its turn failed";
			// run.parked failedTurns: "Parked after its turn failed").
			if n := tk.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.failed'
				AND payload->'kept' = 'true' AND payload->'failedTurns' = '1'`, run); n != 1 {
				t.Errorf("%d run.failed kept with failedTurns 1, want 1", n)
			}
			if n := tk.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.parked'
				AND payload->'failedTurns' = '1' AND payload->>'reason' = $2`, run, tk.park); n != 1 {
				t.Errorf("%d run.parked with failedTurns 1, want 1", n)
			}
			if status, reached := tk.write("try again"); status != 200 || reached != run {
				t.Fatalf("after the failure: %d reached %q", status, reached)
			}
			tk.resumedAnswering(run, "try again")
		})
		t.Run(kind+"/bounded", func(t *testing.T) {
			tk, run := start(t)
			tk.failedTurn(run, "one")
			tk.parkedAfterFailure(run)
			tk.failedTurn(run, "two")
			tk.parkedAfterFailure(run)
			if n := tk.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.parked'
				AND payload->'failedTurns' = '2'`, run); n != 1 {
				t.Errorf("%d run.parked with failedTurns 2, want 1", n)
			}
			tk.failedTurn(run, "three")
			tk.until("the third failure to end it", func() bool {
				return tk.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'failed' AND lux_stop_reason = 'cancel'`, run) == 1
			})
			status, next := tk.write("anyone?")
			if status != 201 || next == run {
				t.Fatalf("after the end: %d reached %q, want a new Run", status, next)
			}
			tk.until("the new Run's answer", func() bool {
				said := tk.said(next)
				return len(said) == 1 && strings.Contains(said[0], "anyone?")
			})
		})
		t.Run(kind+"/a good turn resets the count", func(t *testing.T) {
			tk, run := start(t)
			tk.failedTurn(run, "one")
			tk.parkedAfterFailure(run)
			tk.failedTurn(run, "two")
			tk.parkedAfterFailure(run)
			tk.write("works now")
			tk.resumedAnswering(run, "works now")
			tk.failedTurn(run, "three")
			tk.parkedAfterFailure(run)
			if n := tk.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'failed'`, run); n != 0 {
				t.Errorf("ended after a good turn reset the count:\n%s", tk.describeRuns())
			}
		})
	}
}

// A turn that fails while a person's pause is pending is still told — a
// run.failed, kept — and the pause parks the Run as the person asked. It
// was not parked for the failure, so it does not count towards the bound.
func TestATalkersTurnFailingUnderAPendingPauseIsStillTold(t *testing.T) {
	for kind, start := range talkers(t) {
		t.Run(kind, func(t *testing.T) {
			tk, run := start(t)
			tk.lux.FailTurns(tk.luxRunOf(run), 1, overflow)
			tk.lux.InputGate = make(chan struct{})
			if status, reached := tk.write("one more thing"); status != 200 || reached != run {
				t.Fatalf("the message: %d reached %q", status, reached)
			}
			tk.until("the message sent to lux", func() bool {
				return tk.count(`SELECT count(*) FROM directives WHERE run_id = $1 AND sent_at IS NOT NULL
					AND strpos(text, 'one more thing') > 0`, run) == 1
			})
			if kind == "conductor" {
				if status, body := tk.call("/internal/runs/"+run+"/pause", map[string]any{}); status != 200 {
					t.Fatalf("pause: %d %v", status, body)
				}
			} else {
				// A session's Run has no person's pause route: the control
				// the conductor's route writes, pending as it would be.
				mustExec(t, tk.owner, `UPDATE runs SET control = 'pause_graceful', control_requested_at = now() WHERE id = $1`, run)
			}
			close(tk.lux.InputGate)
			tk.lux.InputGate = nil
			// Without sweeping, so the pause waits on the failed turn's record.
			deadline := time.Now().Add(10 * time.Second)
			for tk.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.failed'`, run) == 0 {
				if time.Now().After(deadline) {
					t.Fatalf("the failed turn under a pending pause was never told:\n%s", tk.describeRuns())
				}
				time.Sleep(20 * time.Millisecond)
			}
			var kept bool
			var failedTurns *string
			if err := tk.owner.QueryRow(t0(), `SELECT (payload->>'kept')::boolean, payload->>'failedTurns' FROM events
				WHERE run_id = $1 AND event_type = 'run.failed'`, run).Scan(&kept, &failedTurns); err != nil {
				t.Fatal(err)
			}
			if !kept || failedTurns != nil {
				t.Errorf("run.failed kept %v failedTurns %v, want kept and not counted", kept, failedTurns)
			}
			tk.until("the person's pause to park it", func() bool {
				return tk.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND dude_pause IS NULL`, run) == 1
			})
			if n := tk.count(`SELECT count(*) FROM runs WHERE id = $1 AND harness_state ? 'failedTurns'`, run); n != 0 {
				t.Errorf("a failure not parked for was counted")
			}
		})
	}
}
