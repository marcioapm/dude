package phases

import (
	"context"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// A paused implementer lux moved (preempted in epoch 1) and placed again
// itself in epoch 2, live. A person's resume with nothing else to apply
// takes it up as lux has it. One that carries dude's own machine move
// (machine_moves unsettled) cannot be applied to a live placement: lux is
// asked to stop it, and the resume waits for the stop. A Run dude resumed
// itself since the move is not lux's placement and is left alone.
func TestAResumedRunLuxMovedDefersToAMachineMoveOfDudes(t *testing.T) {
	assigned := []lux.Placement{{Epoch: 1}, {Epoch: 2}}
	for _, c := range []struct {
		name               string
		control, pause     string
		machineMove        bool
		resumedTo          int
		placements         []lux.Placement
		wantStatus         string
		wantStops, wantDue int
	}{
		{name: "a person's resume, no move of dude's", control: "resume", placements: assigned, wantStatus: "running", wantDue: 1},
		{name: "a person's resume with a machine move pending", control: "resume", machineMove: true, placements: assigned,
			wantStatus: "paused", wantStops: 1, wantDue: 1},
		{name: "dude's machine park", control: "none", pause: "machine", machineMove: true, placements: assigned,
			wantStatus: "paused", wantStops: 1, wantDue: 1},
		{name: "dude's machine park, lux still waiting for a host", control: "none", pause: "machine", machineMove: true,
			placements: assigned[:1], wantStatus: "paused"},
		{name: "a person's resume after dude resumed it itself", control: "resume", resumedTo: 2, placements: assigned,
			wantStatus: "paused", wantDue: 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newHeldWorld(t, heldExit, 0)
			fake := w.s.Lux.(*eventsLux)
			fake.run = &lux.Run{ID: "lrun_1", State: "running", Epoch: 2, Placements: c.placements}
			if _, err := w.owner.Exec(w.ctx, `UPDATE runs SET status = 'paused', control = $2::run_control, dude_pause = NULLIF($3, ''),
				lux_state = 'resuming', harness_state = '{"luxStop": {"reason": "preempt", "epoch": 1}}' WHERE id = $1`,
				w.id, c.control, c.pause); err != nil {
				t.Fatal(err)
			}
			if c.machineMove {
				if _, err := w.owner.Exec(w.ctx, `INSERT INTO machine_moves (id, organization_id, task_id, run_id, role, kind, from_size, to_size_id, apply)
					SELECT 'mmv_1', organization_id, task_id, id, 'implementer', 'raise', '{}', NULL, 'resize' FROM runs WHERE id = $1`, w.id); err != nil {
					t.Fatal(err)
				}
			}
			if c.resumedTo > 0 {
				if _, err := w.owner.Exec(w.ctx, `INSERT INTO run_resumes (run_id, organization_id, epoch, cause)
					SELECT id, organization_id, $2, 'machine' FROM runs WHERE id = $1`, w.id, c.resumedTo); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := w.s.advance(w.ctx, w.snapshot()); err != nil {
				t.Fatal(err)
			}
			status, _, failed := w.status()
			if status != c.wantStatus || failed != 0 {
				t.Errorf("run %s, %d run.failed; want %s and none", status, failed, c.wantStatus)
			}
			fake.mu.Lock()
			stops := fake.stops
			fake.mu.Unlock()
			if stops != c.wantStops {
				t.Errorf("lux asked to stop the Run %d times, want %d", stops, c.wantStops)
			}
			runs, err := w.s.due(w.ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(runs) != c.wantDue {
				t.Errorf("the next sweep takes up %d runs, want %d", len(runs), c.wantDue)
			}
		})
	}
}

// getThen is lux answering Get after then has run: a person's Reset
// committing while dude reads lux's live placement.
type getThen struct {
	*eventsLux
	then func()
}

func (f *getThen) Get(ctx context.Context, id string) (lux.Run, error) {
	if f.then != nil {
		f.then()
		f.then = nil
	}
	return f.eventsLux.Get(ctx, id)
}

// resetPaused records a person's Reset of the Run as the API does once it
// has read the Run paused.
func (w *heldWorld) resetPaused() error {
	var org, task string
	if err := w.owner.QueryRow(w.ctx, `SELECT organization_id, task_id FROM runs WHERE id = $1`, w.id).Scan(&org, &task); err != nil {
		return err
	}
	if _, err := w.owner.Exec(w.ctx, `INSERT INTO machine_sizes (id, organization_id, name, cpus, memory_mib, disk_gib)
		VALUES ('msz_'||$1, $1, 'Reset to', 2, 8192, 20)`, org); err != nil {
		return err
	}
	return w.s.DB.InOrg(w.ctx, org, func(tx pgx.Tx) error {
		return delivery.StartMove(w.ctx, tx, delivery.MachineMove{ID: "mmv_" + org, Org: org, TaskID: task, RunID: w.id,
			Role: "implementer", Kind: "reset", ToSizeID: "msz_" + org, From: map[string]any{}}, false)
	})
}

// A person's resume of a paused Run lux placed again itself after a move,
// and a person's Reset of the same Run, in either order. Reset first —
// committed after the sweep read the Run, while dude reads lux — and the
// Run is not taken up live: it stays paused with its resume, lux is asked
// to stop the placement, and the Reset's move is applied by the resume
// that follows. Take-up first, and the Reset finds the Run running and
// parks it for its move.
func TestAResetAndATakeUpOfARunLuxMovedEndWithTheMovePending(t *testing.T) {
	for _, resetFirst := range []bool{true, false} {
		t.Run(map[bool]string{true: "reset during the take-up", false: "reset after the take-up"}[resetFirst], func(t *testing.T) {
			w := newHeldWorld(t, heldExit, 0)
			fake := w.s.Lux.(*eventsLux)
			fake.run = &lux.Run{ID: "lrun_1", State: "running", Epoch: 2, Placements: []lux.Placement{{Epoch: 1}, {Epoch: 2}}}
			if _, err := w.owner.Exec(w.ctx, `UPDATE runs SET status = 'paused', control = 'resume', lux_state = 'resuming',
				lux_stop_reason = 'pause', harness_state = '{"luxStop": {"reason": "preempt", "epoch": 1}}' WHERE id = $1`, w.id); err != nil {
				t.Fatal(err)
			}
			r := w.snapshot()
			var resetErr error
			if resetFirst {
				w.s.Lux = &getThen{eventsLux: fake, then: func() { resetErr = w.resetPaused() }}
			}
			if _, err := w.s.advance(w.ctx, r); err != nil {
				t.Fatal(err)
			}
			if !resetFirst {
				resetErr = w.resetPaused()
			}
			if resetErr != nil {
				t.Fatalf("reset: %v", resetErr)
			}
			var status, control, pause string
			var moving bool
			if err := w.owner.QueryRow(w.ctx, `SELECT status::text, control::text, COALESCE(dude_pause, ''),
					EXISTS (SELECT 1 FROM machine_moves WHERE run_id = $1 AND settled_at IS NULL)
				FROM runs WHERE id = $1`, w.id).Scan(&status, &control, &pause, &moving); err != nil {
				t.Fatal(err)
			}
			fake.mu.Lock()
			stops := fake.stops
			fake.mu.Unlock()
			want := "paused/resume/ stops=1"
			if !resetFirst {
				want = "running/pause_graceful/machine stops=0"
			}
			got := fmt.Sprintf("%s/%s/%s stops=%d", status, control, pause, stops)
			if got != want || !moving {
				t.Errorf("Run %s, move pending %v; want %s with the move pending", got, moving, want)
			}
			if _, _, failed := w.status(); failed != 0 {
				t.Errorf("%d run.failed", failed)
			}
		})
	}
}

// lux preempts a Run dude has asked to stop for its machine move: the
// broken turn is the move's, neither held nor failed, and no carry-on is
// queued, since the move's own resume tells the agent where it runs. With
// no move of dude's, the turn is held as lux's move and the carry-on
// queued once. A turn already held for lux's reason when dude parks the
// Run for a move is kept as lux's move, with no carry-on, and its warning
// says the move's resume tells the agent, not a carry-on.
func TestALuxMoveOfARunParkedForAMachineMoveQueuesNoCarryOn(t *testing.T) {
	const (
		noMove = iota
		parkedFirst
		heldFirst
	)
	const cutOff = "lux stopped the Run to move it: its spot host was reclaimed; its turn was cut off, and "
	for _, c := range []struct {
		name             string
		order            int
		nudges, warnings int
		moved, warning   string
	}{
		{"no machine move", noMove, 1, 1, "preempt", cutOff + "it is told to carry on once it runs again"},
		{"parked for a machine move", parkedFirst, 0, 0, "", ""},
		{"held, then parked for a machine move", heldFirst, 0, 1, "preempt",
			cutOff + "it is told where it runs, and to carry on, when dude resumes it for its machine move"},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newHarnessWorld(t)
			ctx := t.Context()
			exited := map[string]any{"type": "acp.turn_end", "data": map[string]any{"stopReason": "", "error": "session/prompt: process exited (0)"}}
			park := func() {
				if _, err := w.owner.Exec(ctx, `UPDATE runs SET control = 'pause_graceful', dude_pause = 'machine', control_requested_at = now()
					WHERE id = $1`, w.run.ID); err != nil {
					t.Fatal(err)
				}
				if _, err := w.owner.Exec(ctx, `INSERT INTO machine_moves (id, organization_id, task_id, run_id, role, kind, from_size, apply)
					VALUES ('mmv_1', $2, $3, $1, 'implementer', 'raise', '{}', 'resize')`, w.run.ID, w.run.Org, w.run.TaskID); err != nil {
					t.Fatal(err)
				}
			}
			switch c.order {
			case parkedFirst:
				park()
			case heldFirst:
				w.feed(exited)
				park()
			}
			w.luxFrame(1, "state", map[string]any{"state": "stopping", "reason": "preempt"})
			if c.order != heldFirst {
				w.feed(exited)
			}
			w.luxFrame(1, "state", map[string]any{"state": "stopped", "reason": "preempt"})
			w.luxFrame(2, "state", map[string]any{"state": "resuming", "reason": "auto-resume after preempt"})
			// The batch's end saves the translator's state.
			w.feed()
			var nudges, warnings int
			var moved, warning string
			if err := w.owner.QueryRow(ctx, `SELECT (SELECT count(*) FROM directives WHERE run_id = $1 AND text = $2),
					(SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'agent.warning' AND payload->>'luxStop' = 'preempt'),
					COALESCE(harness_state->'brokenTurn'->>'moved', ''),
					COALESCE((SELECT payload->>'message' FROM events WHERE run_id = $1 AND event_type = 'agent.warning'
						AND payload->>'luxStop' = 'preempt' ORDER BY cursor DESC LIMIT 1), '')
				FROM runs WHERE id = $1`, w.run.ID, movedNudge).Scan(&nudges, &warnings, &moved, &warning); err != nil {
				t.Fatal(err)
			}
			if nudges != c.nudges || warnings != c.warnings || moved != c.moved {
				t.Errorf("%d carry-ons, %d warnings, held as moved %q; want %d, %d, %q", nudges, warnings, moved, c.nudges, c.warnings, c.moved)
			}
			if warning != c.warning {
				t.Errorf("the warning says %q, want %q", warning, c.warning)
			}
			if failed := ofType(w.events(), "run.failed"); len(failed) != 0 {
				t.Errorf("run.failed %v", failed)
			}
		})
	}
}

// One run.failed per end, its error lux's reason first: a move lux ends
// the Run at (a resumePolicy that does not resume it) says the move, the
// agent's broken turn and lux's own end, naming no start stage (park); a
// start lux stops at its time limit names lux's reason and, from the
// start's stages, the stage it failed in.
func TestARunLuxEndsSaysLuxsReasonFirstAndItsStartStageOnce(t *testing.T) {
	const exited = "session/prompt: process exited (0)"
	for _, c := range []struct {
		name              string
		run               func(w *harnessWorld)
		want, stop, stage string
	}{
		{"a drain its resumePolicy fails", func(w *harnessWorld) {
			w.luxFrame(1, "state", map[string]any{"state": "stopping", "reason": "drain"})
			w.luxFrame(1, "stage", map[string]any{"stage": "stopping", "reason": "drain", "epoch": 1, "since": "2026-10-12T10:00:00Z"})
			w.feed(map[string]any{"type": "acp.turn_end", "data": map[string]any{"stopReason": "", "error": exited}})
			w.luxFrame(1, "state", map[string]any{"state": "failed", "reason": "drain: not resumed (resumePolicy manual)"})
		}, "lux stopped the Run to move it: its host was drained; the agent's turn failed: " + exited +
			"; then its run ended: drain: not resumed (resumePolicy manual)", "drain", ""},
		{"a start stopped at its time limit", func(w *harnessWorld) {
			if _, err := w.owner.Exec(t.Context(), `UPDATE runs SET lux_state = 'starting', status = 'scheduled' WHERE id = $1`, w.run.ID); err != nil {
				t.Fatal(err)
			}
			w.stages(1, "waiting", "image")
			w.luxFrame(1, "state", map[string]any{"state": "failed", "reason": "timeout"})
		}, "lux stopped the Run at its time limit: 4h of running", "timeout", "image"},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newHarnessWorld(t)
			w.s.Agent.Timeout = "4h"
			c.run(w)
			p := w.failedWith()
			if p["error"] != c.want || p["luxStop"] != c.stop {
				t.Errorf("run.failed %v; want error %q and luxStop %s", p, c.want, c.stop)
			}
			if stage, _ := p["stage"].(string); stage != c.stage {
				t.Errorf("run.failed names stage %q, want %q", stage, c.stage)
			}
			var errText string
			if err := w.owner.QueryRow(t.Context(), `SELECT COALESCE(error, '') FROM runs WHERE id = $1`, w.run.ID).Scan(&errText); err != nil {
				t.Fatal(err)
			}
			if errText != c.want {
				t.Errorf("the Run's error %q, want %q", errText, c.want)
			}
		})
	}
}
