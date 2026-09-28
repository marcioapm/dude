package orchestrator_test

// The live diff and machine cost, through the orchestrator's real code: the
// syncer reads a working agent's checkout through the fake lux's exec —
// real git, in a real clone — and records what it finds.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
	"github.com/marciomartins/dude/orchestrator/internal/phases"
)

func TestAWorkingAgentsUncommittedEditsAreItsLiveDiff(t *testing.T) {
	w := newWorld(t)
	w.syncer.DiffDelay, w.syncer.DiffEvery = 10*time.Millisecond, 20*time.Millisecond
	w.lux.Workspaces = t.TempDir()
	w.lux.Decide = func(map[string]any) fakelux.Behaviour {
		return fakelux.Behaviour{Hang: true, Edits: map[string]string{"src/new.go": "package src\n", "notes.md": "one\ntwo\n"}}
	}
	wi := w.task()
	w.deliver(wi)
	var runID string
	w.until("the edits to be the Run's diff", func() bool {
		_ = w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE task_id = $1`, wi).Scan(&runID)
		return w.count(`SELECT count(*) FROM run_diffs WHERE run_id = $1 AND jsonb_array_length(files) = 2`, runID) == 1
	})

	var base, head string
	var raw []byte
	_ = w.owner.QueryRow(context.Background(), `SELECT d.base, d.files, r.base_shas->>'target' FROM run_diffs d JOIN runs r ON r.id = d.run_id
		WHERE d.run_id = $1`, runID).Scan(&base, &raw, &head)
	if base == "" || base != head {
		t.Errorf("diffed against %q, the checkout started from %q", base, head)
	}
	var files []phases.DiffFile
	_ = json.Unmarshal(raw, &files)
	byPath := map[string]phases.DiffFile{}
	for _, f := range files {
		byPath[f.Path] = f
	}
	// Untracked, so new; with their lines.
	if f := byPath["notes.md"]; f.Status != "A" || f.Additions != 2 || f.Hunks[0].Lines[1].Text != "two" {
		t.Errorf("notes.md = %+v", f)
	}
	if f := byPath["src/new.go"]; f.Status != "A" || f.Additions != 1 {
		t.Errorf("src/new.go = %+v", f)
	}
	// Announced once, with the same diff; a quiet agent read again is not
	// announced again.
	w.until("the event", func() bool {
		return w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.diff.updated'
			AND jsonb_array_length(payload->'files') = 2 AND payload->>'base' = $2`, runID, base) == 1
	})
	// Read every 20ms meanwhile.
	time.Sleep(200 * time.Millisecond)
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.diff.updated'`, runID); n != 1 {
		t.Errorf("an unchanged diff was announced again: %d events", n)
	}

	// Once the Run ends, the last diff stays.
	if status, body := w.call("/internal/runs/"+runID+"/abort", map[string]any{}); status != 200 {
		t.Fatalf("abort: %d %v", status, body)
	}
	w.until("the Run to end", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'aborted'`, runID) == 1
	})
	if n := w.count(`SELECT count(*) FROM run_diffs WHERE run_id = $1 AND jsonb_array_length(files) = 2`, runID); n != 1 {
		t.Error("the last diff was not kept")
	}
}

func TestAnotherOrganizationCannotReadARunsDiff(t *testing.T) {
	w := newWorld(t)
	w.syncer.DiffDelay = 10 * time.Millisecond
	w.lux.Workspaces = t.TempDir()
	w.lux.Decide = func(map[string]any) fakelux.Behaviour {
		return fakelux.Behaviour{Hang: true, Edits: map[string]string{"a.md": "a\n"}}
	}
	wi := w.task()
	w.deliver(wi)
	w.until("a diff", func() bool {
		return w.count(`SELECT count(*) FROM run_diffs d JOIN runs r ON r.id = d.run_id WHERE r.task_id = $1`, wi) == 1
	})
	var n int
	if err := w.app.InOrg(context.Background(), "org_other", func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `SELECT count(*) FROM run_diffs`).Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("another organization sees %d diffs", n)
	}
}

func TestARunsMachineTimeIsPricedAtTheRateItWasSubmittedWith(t *testing.T) {
	w := newWorld(t)
	w.syncer.MachineUSDPerHour = 0.36
	w.lux.Decide = func(map[string]any) fakelux.Behaviour { return fakelux.Behaviour{Hang: true} }
	wi := w.task()
	w.deliver(wi)
	var runID string
	w.until("the Run to be submitted", func() bool {
		_ = w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE task_id = $1 AND lux_run_id IS NOT NULL`, wi).Scan(&runID)
		return runID != ""
	})
	// Ten minutes on a host, as if it had started then and ended now; a
	// later change of rate leaves it priced as it was.
	mustExec(t, w.owner, `UPDATE runs SET started_at = now() - interval '10 minutes', ended_at = now() WHERE id = $1`, runID)
	w.syncer.MachineUSDPerHour = 5
	var machine, tokens float64
	if err := w.owner.QueryRow(context.Background(), `SELECT machine_usd, cost_usd FROM run_metrics($1)`, runID).Scan(&machine, &tokens); err != nil {
		t.Fatal(err)
	}
	if machine < 0.0599 || machine > 0.0601 {
		t.Errorf("10 minutes at $0.36/h = %v", machine)
	}
	var task float64
	_ = w.owner.QueryRow(context.Background(), `SELECT machine_usd FROM task_metrics($1)`, wi).Scan(&task)
	if task != machine {
		t.Errorf("the task's machine cost %v is not its Run's %v", task, machine)
	}
	// Before machine cost was recorded, a Run's is unknown: nothing, not a
	// guess.
	mustExec(t, w.owner, `UPDATE runs SET machine_usd_per_hour = NULL WHERE id = $1`, runID)
	_ = w.owner.QueryRow(context.Background(), `SELECT machine_usd FROM run_metrics($1)`, runID).Scan(&machine)
	if machine != 0 {
		t.Errorf("an unpriced Run costs %v", machine)
	}
}
