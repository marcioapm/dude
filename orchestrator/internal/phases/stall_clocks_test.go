package phases

import (
	"context"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// The stall clocks' time away from running (left_running_at), as its
// production writers set it: a person's resume (whilePaused) and lux's
// state frames (luxEvent).

// runningAnswerLux answers a resume as a lux that has the Run running
// already.
type runningAnswerLux struct {
	*streamLux
	base time.Time
}

func (f *runningAnswerLux) Resume(context.Context, string, lux.ResumeInput) (lux.Run, error) {
	f.placementLux.set(runningAgain(f.base, "host-a"), nil)
	return lux.Run{ID: "lrun_1", State: "running", Epoch: 2}, nil
}

// clock reads one of w's Run's timestamps.
func (w *resumeWorld) clock(col string) *time.Time {
	w.t.Helper()
	var at *time.Time
	if err := w.owner.QueryRow(w.ctx, `SELECT `+col+` FROM runs WHERE id = $1`, w.run.ID).Scan(&at); err != nil {
		w.t.Fatal(err)
	}
	return at
}

// A person's resume lux accepts as resuming, the Run never away before:
// its time away starts at the acceptance, with its fresh files.
func TestAPersonsResumeStartsItsTimeAwayAtTheAcceptance(t *testing.T) {
	w := newResumeWorld(t)
	base := time.Now().Add(-time.Minute).UTC()
	w.resumable()
	w.lux.set(stoppedOnHost1(base), nil)
	w.s.Lux = &stoppedEpochLux{streamLux: w.following(), base: base}
	w.exec(`UPDATE runs SET left_running_at = NULL WHERE id = $1`, w.run.ID)
	w.whilePaused()
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running' AND lux_state = 'resuming'`); n != 1 {
		t.Fatal("the resume was not accepted as resuming: the case is not the one under test")
	}
	away, files := w.clock("left_running_at"), w.clock("files_changed_at")
	if away == nil || files == nil || !away.Equal(*files) {
		t.Fatalf("time away from %v, files from %v: want both at the acceptance", away, files)
	}
}

// A person's resume lux answers already running: never away since, so an
// old time away is cleared, and the next running frame moves nothing.
func TestAResumeLuxAnswersRunningClearsItsTimeAway(t *testing.T) {
	w := newResumeWorld(t)
	base := time.Now().Add(-time.Minute).UTC()
	w.resumable()
	w.lux.set(stoppedOnHost1(base), nil)
	w.s.Lux = &runningAnswerLux{streamLux: w.following(), base: base}
	w.exec(`UPDATE runs SET left_running_at = now() - interval '1 hour' WHERE id = $1`, w.run.ID)
	w.whilePaused()
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running' AND lux_state = 'running'`); n != 1 {
		t.Fatal("the resume was not accepted as running: the case is not the one under test")
	}
	if away := w.clock("left_running_at"); away != nil {
		t.Fatalf("a Run lux answered running is away from running since %v", away)
	}
}

// Leaving running is stamped once: later frames on the way to running
// again (stopping, then resuming, each in its own batch) keep the first
// departure, so the whole wait is time away. Entering and leaving resuming
// each tell the Run page to read its waiting reason again.
func TestFramesOnTheWayBackKeepTheFirstDeparture(t *testing.T) {
	w := newResumeWorld(t)
	w.exec(`UPDATE runs SET status = 'running', lux_state = 'running', left_running_at = NULL WHERE id = $1`, w.run.ID)
	const changed = `SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'servers.changed'`
	lastChange := func() string {
		t.Helper()
		var state string
		if err := w.owner.QueryRow(w.ctx, `SELECT payload->>'luxState' FROM events
			WHERE run_id = $1 AND event_type = 'servers.changed' ORDER BY cursor DESC LIMIT 1`, w.run.ID).Scan(&state); err != nil {
			t.Fatal(err)
		}
		return state
	}
	w.follow(luxState(1, "stopping"))
	first := w.clock("left_running_at")
	if first == nil {
		t.Fatal("leaving running was not stamped")
	}
	time.Sleep(30 * time.Millisecond)
	before := w.count(changed)
	w.follow(luxState(1, "resuming"))
	if last := w.clock("left_running_at"); last == nil || !last.Equal(*first) {
		t.Fatalf("the departure moved from %v to %v on a later frame away from running", first, last)
	}
	if after := w.count(changed); after != before+1 {
		t.Fatalf("resuming published %d servers.changed, want 1", after-before)
	}
	if state := lastChange(); state != "resuming" {
		t.Fatalf("resuming's servers.changed says lux state %q", state)
	}
	before = w.count(changed)
	w.follow(running(2))
	if after := w.count(changed); after != before+1 {
		t.Fatalf("running again published %d servers.changed, want 1 to clear the waiting notice", after-before)
	}
	if state := lastChange(); state != "running" {
		t.Fatalf("running again's servers.changed says lux state %q", state)
	}
}

// Two moves, each 10 minutes away, and lux saying running twice after
// each: entering running clears the time away and moves the clocks on by
// it once; the repeated frame moves nothing.
func TestEachEntryIntoRunningMovesTheClocksOnce(t *testing.T) {
	w := newResumeWorld(t)
	w.exec(`UPDATE runs SET status = 'running', lux_state = 'running', files_changed_at = now() - interval '1 hour',
		agent_active_at = now() - interval '1 hour', left_running_at = NULL WHERE id = $1`, w.run.ID)
	for epoch := 2; epoch <= 3; epoch++ {
		w.follow(luxState(epoch-1, "resuming"))
		w.exec(`UPDATE runs SET left_running_at = now() - interval '10 minutes' WHERE id = $1`, w.run.ID)
		files, active := w.clock("files_changed_at"), w.clock("agent_active_at")
		w.follow(running(epoch))
		if away := w.clock("left_running_at"); away != nil {
			t.Errorf("move %d: still away from running since %v once running", epoch-1, away)
		}
		movedFiles, movedActive := w.clock("files_changed_at"), w.clock("agent_active_at")
		if d := movedFiles.Sub(*files); d < 10*time.Minute || d > 11*time.Minute {
			t.Fatalf("move %d: files moved on by %v, want 10 minutes", epoch-1, d)
		}
		if d := movedActive.Sub(*active); d < 10*time.Minute || d > 11*time.Minute {
			t.Fatalf("move %d: activity moved on by %v, want 10 minutes", epoch-1, d)
		}
		w.follow(running(epoch))
		if again := w.clock("files_changed_at"); !again.Equal(*movedFiles) {
			t.Errorf("move %d: a repeated running frame moved the files again, from %v to %v", epoch-1, movedFiles, again)
		}
		if again := w.clock("agent_active_at"); !again.Equal(*movedActive) {
			t.Errorf("move %d: a repeated running frame moved the activity again, from %v to %v", epoch-1, movedActive, again)
		}
	}
}

func TestOpenCallClocksExcludeHostMoveTime(t *testing.T) {
	w := newResumeWorld(t)
	w.exec(`UPDATE runs SET status = 'running', lux_state = 'resuming', left_running_at = now() - interval '30 minutes',
		open_tool_calls = ARRAY['old', 'recent'],
		open_tool_calls_at = jsonb_build_object('old', now() - interval '39 minutes', 'recent', now() - interval '1 minute')
		WHERE id = $1`, w.run.ID)
	w.follow(running(2))
	old, recent := w.clock("(open_tool_calls_at->>'old')::timestamptz"), w.clock("(open_tool_calls_at->>'recent')::timestamptz")
	if old == nil || time.Since(*old) < 8*time.Minute || time.Since(*old) > 10*time.Minute {
		t.Fatalf("old call clock = %v, want 9 running minutes ago", old)
	}
	if recent == nil || time.Since(*recent) > time.Minute {
		t.Fatalf("recent call clock = %v, want clamped at arrival", recent)
	}
	w.follow(running(2))
	if again := w.clock("(open_tool_calls_at->>'old')::timestamptz"); again == nil || !again.Equal(*old) {
		t.Fatalf("repeated running shifted open call from %v to %v", old, again)
	}
}

func TestBrainstormHostMoveDoesNotConsumeItsCallLimit(t *testing.T) {
	w := newResumeWorld(t)
	w.exec(`INSERT INTO sessions (id, organization_id, title) VALUES ('ssn_' || $1, $1, 'Moving brainstorm')`, w.run.Org)
	w.exec(`UPDATE runs SET role = 'brainstorm', phase = NULL, session_id = 'ssn_' || organization_id,
		project_id = NULL, task_id = NULL, status = 'running', lux_state = 'resuming',
		left_running_at = now() - interval '30 minutes', open_tool_calls = ARRAY['old'],
		open_tool_calls_at = jsonb_build_object('old', now() - interval '39 minutes') WHERE id = $1`, w.run.ID)
	w.follow(running(2))
	if err := w.s.interruptBrainstorms(w.ctx); err != nil {
		t.Fatal(err)
	}
	if n := w.count(`SELECT count(*) FROM directives WHERE run_id = $1 AND interrupt`); n != 0 {
		t.Fatalf("host move queued %d interrupts for nine running minutes, want none", n)
	}
}
