package orchestrator_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// toldEvent is a run.told's payload.
type toldEvent struct {
	Epoch  int    `json:"epoch"`
	MoveID string `json:"moveId"`
	Kind   string `json:"kind"`
	Text   string `json:"text"`
}

// toldOf is every run.told of the Run, in order.
func (w *world) toldOf(runID string) []toldEvent {
	w.t.Helper()
	rows, err := w.owner.Query(context.Background(), `SELECT payload FROM events WHERE run_id = $1 AND event_type = 'run.told' ORDER BY cursor`, runID)
	if err != nil {
		w.t.Fatal(err)
	}
	defer rows.Close()
	var out []toldEvent
	for rows.Next() {
		var raw []byte
		var e toldEvent
		if err := rows.Scan(&raw); err != nil {
			w.t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &e); err != nil {
			w.t.Fatal(err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		w.t.Fatal(err)
	}
	return out
}

// luxEpoch is the fake lux Run's current epoch.
func (w *world) luxEpoch(id string) int {
	for _, r := range w.lux.Runs() {
		if r.ID == id {
			return r.Epoch
		}
	}
	w.t.Fatalf("no lux Run %s", id)
	return 0
}

// An agent's raise: one run.told, "moved", for the epoch lux resumed it
// into, holding the resume's input byte for byte as lux received it.
func TestARaiseRecordsWhatTheAgentWasToldOnce(t *testing.T) {
	w := newWorld(t)
	_, runID, live := w.liveImplementer()
	mustExec(t, w.owner, `INSERT INTO machine_sizes (id, organization_id, name, cpus, memory_mib, disk_gib) VALUES ('msz_big', $1, 'Bigger', 4, 16384, 40)`, w.org)
	if status, body := w.requestMachine(runID, `{"size":"Bigger","reason":"jest killed, exit 137"}`); status != 200 {
		t.Fatalf("request: %d %s", status, body)
	}
	w.until("the move settled and the agent running", func() bool {
		return w.count(`SELECT count(*) FROM machine_moves WHERE run_id=$1 AND settled_at IS NOT NULL`, runID) == 1 &&
			w.count(`SELECT count(*) FROM runs WHERE id=$1 AND status='running'`, runID) == 1 && w.lux.State(live) == "running"
	})
	for range 3 {
		w.pump()
	}
	inputs := w.lux.ResumeInputs(live)
	told := w.toldOf(runID)
	if len(inputs) != 1 || len(told) != 1 {
		t.Fatalf("resume inputs %q, run.told %+v; want one of each", inputs, told)
	}
	move := w.str(`SELECT id FROM machine_moves WHERE run_id=$1`, runID)
	if got := told[0]; got.Kind != "moved" || got.MoveID != move || got.Text != inputs[0] || got.Epoch != w.luxEpoch(live) || got.Epoch != 2 {
		t.Fatalf("run.told %+v; want moved, %s, epoch %d, text %q", got, move, w.luxEpoch(live), inputs[0])
	}
	if !strings.HasPrefix(told[0].Text, "You now run on Bigger (4 CPUs · 16 GiB · 40 GiB). Start again any server or watcher you had running. Carry on.") {
		t.Fatalf("text %q", told[0].Text)
	}
	if w.count(`SELECT count(*) FROM events WHERE run_id=$1 AND event_type='run.resized' AND (payload->'machine'->>'sinceEpoch')::int=$2`, runID, told[0].Epoch) != 1 {
		t.Fatal("run.told's epoch is not the one run.resized records")
	}
}

// A person's Reset with a note: the agent is told it moved back, by whom
// and the note, first in the resume's input.
func TestAResetWithANoteRecordsItsWords(t *testing.T) {
	w := newWorld(t)
	task, runID, live := w.raisedImplementer()
	const note = "The suite fits in 8 GiB with --maxWorkers 2; keep it at 2."
	if status, out := w.callAs(w.person("Ana Ribeiro"), "/internal/agent-machines/reset", map[string]any{"taskId": task, "role": "implementer", "note": note}); status != 200 {
		t.Fatalf("reset: %d %v", status, out)
	}
	w.until("the Reset settled", func() bool {
		return w.count(`SELECT count(*) FROM machine_moves WHERE run_id=$1 AND kind='reset' AND settled_at IS NOT NULL`, runID) == 1
	})
	told := w.toldOf(runID)
	inputs := w.lux.ResumeInputs(live)
	if len(told) != 1 || len(inputs) == 0 || told[0].Text != inputs[len(inputs)-1] || told[0].Kind != "moved" {
		t.Fatalf("run.told %+v, inputs %q", told, inputs)
	}
	if want := "Moved back to Standard by Ana Ribeiro: " + note; !strings.HasPrefix(told[0].Text, want) {
		t.Fatalf("text %q, want it to start %q", told[0].Text, want)
	}
}

// lux refuses the real resume (the pool went after the dry run): the Run
// resumes where it was, and run.told is "kept", with keptInput's words as
// lux took them.
func TestARefusedMoveRecordsTheKeptWords(t *testing.T) {
	w := newWorld(t)
	_, runID, live := w.liveImplementer()
	mustExec(t, w.owner, `INSERT INTO machine_sizes (id, organization_id, name, cpus, memory_mib, disk_gib, pool_id) VALUES ('msz_big', $1, 'Bigger', 4, 16384, 40, $2)`, w.org, bigPool)
	if status, body := w.requestMachine(runID, `{"size":"Bigger","reason":"duckdb out of memory"}`); status != 200 {
		t.Fatalf("request: %d %s", status, body)
	}
	w.lux.Pools = slices.DeleteFunc(fakelux.DefaultPools(), func(p lux.Pool) bool { return p.ID == bigPool })
	w.until("settled refused", func() bool {
		return w.count(`SELECT count(*) FROM machine_moves WHERE run_id=$1 AND settled_at IS NOT NULL AND refused='unknown_pool'`, runID) == 1
	})
	told := w.toldOf(runID)
	inputs := w.lux.ResumeInputs(live)
	if len(told) != 1 || len(inputs) != 1 {
		t.Fatalf("run.told %+v, inputs %q; want one of each", told, inputs)
	}
	const kept = "You asked for Bigger; lux kept you on Standard: Bigger's pool has no host it fits; ask for another size\n\n"
	if got := told[0]; got.Kind != "kept" || got.Text != inputs[0] || !strings.HasPrefix(got.Text, kept) || got.Epoch != w.luxEpoch(live) {
		t.Fatalf("run.told %+v; want kept, epoch %d, text %q", got, w.luxEpoch(live), inputs[0])
	}
}

// lux took the sized resume and its answer was lost, so nothing was
// recorded; before the retry the size's numbers changed, so lux refuses the
// retry's resources 409 (resuming already with others) and takes it without
// them. That retry's input carries none of the move's words, and records
// nothing.
func TestAResumeRetriedAfterA409RecordsNothingNew(t *testing.T) {
	w := newWorld(t)
	_, runID, live := w.liveImplementer()
	mustExec(t, w.owner, `INSERT INTO machine_sizes (id, organization_id, name, cpus, memory_mib, disk_gib) VALUES ('msz_big', $1, 'Bigger', 4, 16384, 40)`, w.org)
	if status, body := w.requestMachine(runID, `{"size":"Bigger","reason":"jest killed, exit 137"}`); status != 200 {
		t.Fatalf("request: %d %s", status, body)
	}
	w.until("parked", func() bool { return w.count(`SELECT count(*) FROM runs WHERE id=$1 AND status='paused'`, runID) == 1 })
	// The resumed placement waits for a host until the retry is answered,
	// so lux is still resuming when it comes.
	hold := make(chan struct{})
	w.lux.BeforeAssign(func(id string) {
		if id == live {
			<-hold
		}
	})
	defer close(hold)
	lost := &lostAnswer{Client: w.syncer.Lux}
	lost.left.Store(1)
	w.syncer.Lux = lost
	w.until("lux took the first resume", func() bool { return len(w.lux.ResumeInputs(live)) == 1 })
	mustExec(t, w.owner, `UPDATE machine_sizes SET cpus = 6 WHERE id = 'msz_big'`)
	mustExec(t, w.owner, `UPDATE runs SET next_attempt_at = NULL WHERE id = $1`, runID)
	w.until("the move settled", func() bool {
		return w.count(`SELECT count(*) FROM machine_moves WHERE run_id=$1 AND settled_at IS NOT NULL`, runID) == 1
	})
	if got := w.resumeResources(live); len(got) != 1 || got[0] == nil || got[0].CPUs != 4 {
		t.Fatalf("lux took %v, want only the first sized resume", got)
	}
	if told := w.toldOf(runID); len(told) != 0 {
		t.Fatalf("run.told %+v after a 409's retry, want none", told)
	}
}

// The resume lux accepted is recorded, then settling the move fails (lux's
// reads fail) and the resume is retried while lux is still resuming: lux
// answers the retry as it did the first, and nothing new is recorded for
// that epoch.
func TestARetryIntoTheSameEpochRecordsNoSecondTold(t *testing.T) {
	w := newWorld(t)
	_, runID, live := w.liveImplementer()
	mustExec(t, w.owner, `INSERT INTO machine_sizes (id, organization_id, name, cpus, memory_mib, disk_gib) VALUES ('msz_big', $1, 'Bigger', 4, 16384, 40)`, w.org)
	if status, body := w.requestMachine(runID, `{"size":"Bigger","reason":"jest killed, exit 137"}`); status != 200 {
		t.Fatalf("request: %d %s", status, body)
	}
	w.until("parked", func() bool { return w.count(`SELECT count(*) FROM runs WHERE id=$1 AND status='paused'`, runID) == 1 })
	hold := make(chan struct{})
	w.lux.BeforeAssign(func(id string) {
		if id == live {
			<-hold
		}
	})
	defer close(hold)
	unread := &unreadAfterResume{Client: w.syncer.Lux}
	w.syncer.Lux = unread
	w.until("the first resume recorded, its settlement failed", func() bool {
		return len(w.toldOf(runID)) == 1 && w.count(`SELECT count(*) FROM runs WHERE id=$1 AND status='paused' AND next_attempt_at > now()`, runID) == 1
	})
	if w.count(`SELECT count(*) FROM machine_moves WHERE run_id=$1 AND settled_at IS NULL`, runID) != 1 {
		t.Fatal("the move settled without lux's reads")
	}
	unread.failing.Store(false)
	mustExec(t, w.owner, `UPDATE runs SET next_attempt_at = NULL WHERE id = $1`, runID)
	w.until("the move settled on the retry", func() bool {
		return w.count(`SELECT count(*) FROM machine_moves WHERE run_id=$1 AND settled_at IS NOT NULL`, runID) == 1
	})
	if n := w.luxCalls(live, "resume"); n != 1 {
		t.Fatalf("lux resumed %d times, want 1", n)
	}
	told := w.toldOf(runID)
	inputs := w.lux.ResumeInputs(live)
	if len(told) != 1 || len(inputs) != 1 || told[0].Text != inputs[0] || told[0].Epoch != 2 {
		t.Fatalf("run.told %+v, inputs %q; want one, epoch 2, of the input lux took", told, inputs)
	}
}

// unreadAfterResume answers the first real resume without the Run's
// resources and fails every read of lux after it until failing is cleared,
// so that resume's settlement fails.
type unreadAfterResume struct {
	lux.Client
	armed, failing atomic.Bool
}

func (u *unreadAfterResume) Resume(ctx context.Context, id string, in lux.ResumeInput) (lux.Run, error) {
	res, err := u.Client.Resume(ctx, id, in)
	if err == nil && !in.ValidateOnly && u.armed.CompareAndSwap(false, true) {
		res.Resize, res.Spec.Resources = nil, nil
		u.failing.Store(true)
	}
	return res, err
}

func (u *unreadAfterResume) Get(ctx context.Context, id string) (lux.Run, error) {
	if u.failing.Load() {
		return lux.Run{}, errors.New("lux unreachable")
	}
	return u.Client.Get(ctx, id)
}

// A resume with no machine move (a person's pause and Resume) tells the
// agent nothing of a move and records no run.told.
func TestAResumeWithoutAMoveRecordsNoTold(t *testing.T) {
	w := newWorld(t)
	_, runID, live := w.liveImplementer()
	if status, out := w.call("/internal/runs/"+runID+"/pause", map[string]any{}); status != 200 {
		t.Fatalf("pause: %d %v", status, out)
	}
	w.until("paused", func() bool { return w.count(`SELECT count(*) FROM runs WHERE id=$1 AND status='paused'`, runID) == 1 })
	w.resumeAgent(runID, w.lux.Runs()[0])
	w.until("running again", func() bool { return w.count(`SELECT count(*) FROM runs WHERE id=$1 AND status='running'`, runID) == 1 })
	if inputs := w.lux.ResumeInputs(live); len(inputs) != 1 {
		t.Fatalf("resume inputs %q", inputs)
	}
	if told := w.toldOf(runID); len(told) != 0 {
		t.Fatalf("run.told %+v without a move", told)
	}
}
