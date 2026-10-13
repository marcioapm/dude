package delivery_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// The conductor's digest is recorded as data on the conductor's Run, item
// by item, where its Chat shows it; nothing is recorded when there was
// nothing to tell, or with no Run to tell.
func TestTheDigestIsRecordedOnTheConductorsRun(t *testing.T) {
	w := newEpicWorld(t)
	ctx := context.Background()
	w.run(t, "run_cond", "ABS-2", "", "conductor", "paused")
	w.exec(t, `UPDATE runs SET created_at = now() - interval '1 hour' WHERE id = 'run_cond'`)
	one := w.run(t, "run_one", "ABS-1", "implement", "implementer", "running")
	digest := func(runID string) string {
		var s string
		w.in(t, func(tx pgx.Tx) error {
			var err error
			s, err = delivery.ConductorDigestTx(ctx, tx, w.org, w.tasks["ABS-2"], runID, "briefing")
			return err
		})
		return s
	}
	if digest("run_cond") != "" || w.count(t, `SELECT count(*) FROM events WHERE event_type = 'conductor.digest'`) != 0 {
		t.Fatal("a digest with nothing in it was recorded")
	}
	q := w.send(t, one, "ABS-2", "Which body?", "")
	w.in(t, func(tx pgx.Tx) error {
		_, err := delivery.PinNoteTx(ctx, tx, w.org, delivery.PinInput{EpicID: w.epic, Text: "Body is {metric}", Run: one})
		return err
	})
	if digest("run_cond") == "" {
		t.Fatal("no digest")
	}
	var payload map[string]any
	if err := w.owner.QueryRow(ctx, `SELECT payload FROM events WHERE run_id = 'run_cond' AND event_type = 'conductor.digest'`).
		Scan(&payload); err != nil {
		t.Fatal(err)
	}
	msgs, _ := payload["messages"].([]any)
	notes, _ := payload["notes"].([]any)
	if payload["where"] != "briefing" || payload["task"] != "ABS-2" || len(msgs) != 1 || len(notes) != 1 {
		t.Fatalf("the digest's payload: %v", payload)
	}
	m, _ := msgs[0].(map[string]any)
	n, _ := notes[0].(map[string]any)
	if m["message"] != q.ID || m["from"] != "ABS-1" || m["to"] != "ABS-2" || m["text"] != "Which body?" {
		t.Errorf("the message item: %v", m)
	}
	if n["n"] != float64(1) || n["change"] != "added" {
		t.Errorf("the note item: %v", n)
	}
	w.send(t, one, "ABS-2", "Another?", "")
	digest("")
	if n := w.count(t, `SELECT count(*) FROM events WHERE event_type = 'conductor.digest'`); n != 1 {
		t.Errorf("a digest with no Run to tell was recorded: %d", n)
	}
}
