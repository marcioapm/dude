package phases

import (
	"context"
	"io"
	"log/slog"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// A conductor's wait is evaluated in the sweep's beforeDue, before the
// wakes: an open wait is work for the probe by itself, and the reason it
// records goes in a note like any other, once.
func TestTheSweepEvaluatesOpenWaitsAndWakesTheConductorOnce(t *testing.T) {
	w := &wakeWorld{newReceiptWorld(t)}
	w.exec(`UPDATE runs SET role = 'conductor', kind = 'agent', status = 'paused', dude_pause = 'conductor', turn_done_at = now()
		WHERE id = 'run_'||$1`)
	w.exec(`UPDATE runs SET status = 'running', phase = 'review', role = 'reviewer' WHERE id = 'other_'||$1`)
	w.exec(`DELETE FROM directives WHERE organization_id = $1`)
	w.s.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	w.s.WakeWindow = time.Millisecond
	ctx := context.Background()
	var waitID string
	if err := w.s.DB.InOrg(ctx, w.org, func(tx pgx.Tx) error {
		ref := delivery.RunRef{Org: w.org, ProjectID: "prj_" + w.org, TaskID: "wi_" + w.org, RunID: "run_" + w.org}
		opened, err := delivery.OpenConductorWaitTx(ctx, tx, ref, delivery.WaitAsk{
			Conditions: []delivery.WaitCondition{{RunsEnded: []string{"other_" + w.org}}}, Deadline: "1h", CallKey: "c1"})
		waitID = opened.WaitID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if waitID == "" {
		t.Fatal("no wait opened")
	}
	sweep := func() {
		t.Helper()
		if err := w.s.beforeDue(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if !w.s.pending(ctx).waits {
		t.Fatal("the probe does not see the open wait")
	}
	sweep()
	if n := w.count(`SELECT count(*) FROM conductor_wakes WHERE organization_id = $1`); n != 0 {
		t.Fatalf("%d reasons while the Run runs", n)
	}
	w.exec(`UPDATE runs SET status = 'completed' WHERE id = 'other_'||$1`)
	sweep()
	time.Sleep(20 * time.Millisecond)
	sweep()
	if n := w.count(`SELECT count(*) FROM conductor_wakes WHERE organization_id = $1 AND kind = 'wait' AND delivered_at IS NOT NULL`); n != 1 {
		t.Fatalf("%d wait reasons delivered, want 1", n)
	}
	if n := w.count(`SELECT count(*) FROM conductor_waits WHERE organization_id = $1 AND ended_by = 'held'`); n != 1 {
		t.Fatal("the wait did not end held")
	}
	woken := func() int {
		return w.count(`SELECT count(*) FROM events WHERE organization_id = $1 AND event_type = 'conductor.woken'`)
	}
	if woken() != 1 {
		t.Fatalf("%d notes, want 1", woken())
	}
	if w.s.pending(ctx).waits {
		t.Fatal("the probe still sees an ended wait")
	}
	sweep()
	sweep()
	if woken() != 1 {
		t.Fatalf("a later sweep sent another note: %d", woken())
	}
}

// More open waits than one sweep took up before (wakeBatch): the newest,
// its deadline passed, ends in the first sweep all the same.
func TestTheSweepEvaluatesEveryOpenWait(t *testing.T) {
	w := &wakeWorld{newReceiptWorld(t)}
	w.s.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	n := wakeBatch + 5
	for _, q := range []string{
		`INSERT INTO tasks (id, organization_id, project_id, number, title, goal)
			SELECT 'wi_'||$1||'_'||i, $1, 'prj_'||$1, 100 + i, 'T', 'G' FROM generate_series(1, ` + itoa(n) + `) i`,
		`INSERT INTO runs (id, organization_id, project_id, task_id, attempt)
			SELECT 'run_'||$1||'_'||i, $1, 'prj_'||$1, 'wi_'||$1||'_'||i, 1 FROM generate_series(1, ` + itoa(n) + `) i`,
		`INSERT INTO conductor_waits (id, organization_id, task_id, conductor_run_id, attempt, conditions, deadline_at, since_cursor, call_key, created_at)
			SELECT 'cwt_'||i, $1, 'wi_'||$1||'_'||i, 'run_'||$1||'_'||i, 1, '[]', now() + interval '1 hour', 0, 'c', now() - interval '1 hour'
			FROM generate_series(1, ` + itoa(n) + `) i`,
		`INSERT INTO conductor_waits (id, organization_id, task_id, conductor_run_id, attempt, conditions, deadline_at, since_cursor, call_key)
			VALUES ('cwt_newest', $1, 'wi_'||$1, 'run_'||$1, 1, '[]', now() - interval '1 second', 0, 'c')`,
	} {
		w.exec(q)
	}
	if err := w.s.evaluateWaits(context.Background()); err != nil {
		t.Fatal(err)
	}
	if by := w.count(`SELECT count(*) FROM conductor_waits WHERE organization_id = $1 AND id = 'cwt_newest' AND ended_by = 'deadline'`); by != 1 {
		t.Fatal("the newest wait, its deadline passed, was not evaluated")
	}
	if open := w.count(`SELECT count(*) FROM conductor_waits WHERE organization_id = $1 AND ended_at IS NULL`); open != n {
		t.Fatalf("%d waits open, want %d", open, n)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }
