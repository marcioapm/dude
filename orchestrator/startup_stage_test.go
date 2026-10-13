package orchestrator_test

// While an agent's Run starts, dude records where it is from lux's stage
// events (phases/stage.go): one run.stage per change, while it starts, for
// a task's Run, a session's brainstorm (fresh and resumed) and a
// conductor; a move's stop as park; a start that fails names its stage;
// an older lux, with no stages, gives none.

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

type stageEvent struct {
	Stage string
	Epoch int
	Since *time.Time
}

// stages are the Run's run.stage events, in order.
func (w *world) stages(runID string) []stageEvent {
	w.t.Helper()
	rows, err := w.owner.Query(context.Background(), `SELECT payload->>'stage', (payload->>'epoch')::int, (payload->>'since')::timestamptz
		FROM events WHERE run_id = $1 AND event_type = 'run.stage' ORDER BY cursor`, runID)
	if err != nil {
		w.t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByPos[stageEvent])
	if err != nil {
		w.t.Fatal(err)
	}
	return out
}

// startedThrough checks epoch's stages are want, in order, each with its since, not going back in time.
func (w *world) startedThrough(runID string, epoch int, want ...string) {
	w.t.Helper()
	var got []string
	var last time.Time
	for _, e := range w.stages(runID) {
		if e.Epoch != epoch {
			continue
		}
		got = append(got, e.Stage)
		if e.Since == nil || e.Since.Before(last) {
			w.t.Errorf("stage %s since %v, after %v", e.Stage, e.Since, last)
		} else {
			last = *e.Since
		}
	}
	if len(got) != len(want) {
		w.t.Fatalf("epoch %d stages %v, want %v", epoch, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			w.t.Fatalf("epoch %d stages %v, want %v", epoch, got, want)
		}
	}
}

var freshStages = []string{"waiting", "image", "volumes", "repositories", "container", "running"}

func TestATaskRunRecordsEachStageOfItsStartOnce(t *testing.T) {
	w := newWorld(t)
	w.lux.Decide = func(map[string]any) fakelux.Behaviour {
		return fakelux.Behaviour{Hang: true, Reply: "Working."}
	}
	wi := w.task()
	w.deliver(wi)
	runID := w.working(wi)
	w.startedThrough(runID, 1, freshStages...)
	// Running: nothing more, and the API says no stage.
	time.Sleep(200 * time.Millisecond)
	if n := len(w.stages(runID)); n != len(freshStages) {
		t.Errorf("%d run.stage once running, want %d", n, len(freshStages))
	}
}

func TestABrainstormRecordsItsStagesFreshAndResumedAndTheSessionSaysWhereItIs(t *testing.T) {
	s := newSessionWorld(t)
	s.syncer.ConductorWarm = time.Hour
	next := s.holdStages()
	id := s.session()
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "where would metering live?"})
	close(next(1, "waiting").release)
	held := next(1, "image")
	var run map[string]any
	s.until("the session to say its agent is getting the image ready", func() bool {
		run, _ = s.ok(s.marcio, "GET", "/internal/sessions/"+id, nil)["session"].(map[string]any)["run"].(map[string]any)
		st, _ := run["stage"].(map[string]any)
		return st != nil && st["stage"] == "image"
	})
	if st := run["stage"].(map[string]any); st["epoch"] != 1.0 || st["since"] == nil {
		t.Errorf("session run stage %v", st)
	}
	close(held.release)
	for _, stage := range []string{"volumes", "repositories", "container", "running"} {
		close(next(1, stage).release)
	}
	runID := s.started(id)
	s.startedThrough(runID, 1, freshStages...)
	if st := s.ok(s.marcio, "GET", "/internal/sessions/"+id, nil)["session"].(map[string]any)["run"].(map[string]any)["stage"]; st != nil {
		t.Errorf("a running agent's session says stage %v", st)
	}

	// Parked, then woken by a message: the resume's stages, under its epoch.
	s.lux.StopOnItsOwn(s.luxRunOf(runID))
	s.until("the Run parked", func() bool {
		return s.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused'`, runID) == 1
	})
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "and the tests?"})
	for _, stage := range freshStages {
		close(next(2, stage).release)
	}
	s.until("the resumed agent running", func() bool {
		return s.count(`SELECT count(*) FROM runs WHERE id = $1 AND lux_state = 'running' AND status = 'running'`, runID) == 1
	})
	// The resume's stages follow the first start's. Its waiting still
	// names the placement it left (lux moves the epoch at assignment); the
	// rest name the new one.
	all := s.stages(runID)
	var resumed []string
	for _, e := range all[len(freshStages):] {
		resumed = append(resumed, fmt.Sprintf("%s@%d", e.Stage, e.Epoch))
	}
	if want := "[waiting@1 image@2 volumes@2 repositories@2 container@2 running@2]"; fmt.Sprint(resumed) != want {
		t.Errorf("the resume's stages %v, want %s", resumed, want)
	}
}

func TestAConductorRecordsItsStagesAsItWakes(t *testing.T) {
	tk := newTalking(t, "conductor", nil)
	var run string
	tk.until("the conductor answering", func() bool {
		run = tk.latest()
		return run != "" && len(tk.said(run)) == 1
	})
	tk.startedThrough(run, 1, freshStages...)
}

func TestAStartThatFailsNamesTheStageItFailedInAndLuxsReason(t *testing.T) {
	w := newWorld(t)
	w.lux.FailStarts("dude.role=implementer", 1)
	wi := w.task()
	w.deliver(wi)
	var payload map[string]any
	w.until("the implementer's start to fail", func() bool {
		return w.owner.QueryRow(context.Background(), `SELECT e.payload FROM events e JOIN runs r ON r.id = e.run_id
			WHERE r.task_id = $1 AND e.event_type = 'run.failed' ORDER BY e.cursor LIMIT 1`, wi).Scan(&payload) == nil
	})
	if payload["stage"] != "running" || payload["reason"] != "start-failed" {
		t.Errorf("run.failed = %v, want stage running (the agent coming up) and lux's reason start-failed", payload)
	}
}

func TestAMoveRecordsItsStopAsParkThenItsStages(t *testing.T) {
	w := newWorld(t)
	w.lux.Decide = func(map[string]any) fakelux.Behaviour {
		return fakelux.Behaviour{Hang: true, Reply: "Working."}
	}
	wi := w.task()
	w.deliver(wi)
	runID := w.working(wi)
	w.lux.Migrate(w.luxRunOf(runID))
	w.until("the moved Run running again", func() bool {
		var n int
		for _, e := range w.stages(runID) {
			if e.Epoch == 2 && e.Stage == "running" {
				n++
			}
		}
		return n == 1
	})
	var moved []string
	for _, e := range w.stages(runID)[len(freshStages):] {
		moved = append(moved, e.Stage)
	}
	want := append([]string{"park"}, freshStages...)
	if fmt.Sprint(moved) != fmt.Sprint(want) {
		t.Errorf("the move's stages %v, want %v", moved, want)
	}
}

func TestAnOlderLuxWithNoStagesRecordsNoneAndTheRunStillFinishes(t *testing.T) {
	w := newWorld(t)
	w.lux.LegacyStages = true
	wi := w.task()
	w.deliver(wi)
	w.until("the implementer to finish", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'implement' AND status = 'completed'`, wi) == 1
	})
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'run.stage'`, wi); n != 0 {
		t.Errorf("%d run.stage from a lux with no stages", n)
	}
}
