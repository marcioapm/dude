package phases

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// wakeWorld is a receipt world whose Run is a task's conductor, paused
// between turns, with one reason to wake it: each sweep's note is a
// directive the translator gets receipts for.
type wakeWorld struct{ *receiptWorld }

func newWakeWorld(t *testing.T) *wakeWorld {
	w := &wakeWorld{newReceiptWorld(t)}
	w.exec(`UPDATE runs SET role = 'conductor', kind = 'agent', status = 'paused', dude_pause = 'conductor', turn_done_at = now()
		WHERE id = 'run_'||$1`)
	w.exec(`UPDATE runs SET status = 'completed' WHERE id = 'other_'||$1`)
	w.exec(`DELETE FROM directives WHERE id = 'dir_1' AND organization_id = $1`)
	w.tr.run.Role = delivery.RoleConductor
	if err := w.s.DB.InOrg(context.Background(), w.org, func(tx pgx.Tx) error {
		_, err := delivery.RecordWakeTx(context.Background(), tx, w.org, "wi_"+w.org, "decision", "k", "the decision waits")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return w
}

func (w *wakeWorld) exec(sql string) {
	w.t.Helper()
	if _, err := w.owner.Exec(context.Background(), sql, w.org); err != nil {
		w.t.Fatal(err)
	}
}

func (w *wakeWorld) count(sql string) int {
	w.t.Helper()
	var n int
	if err := w.owner.QueryRow(context.Background(), sql, w.org).Scan(&n); err != nil {
		w.t.Fatal(err)
	}
	return n
}

// wake ages the pending reasons and delivers them: the note's directive,
// "" for none.
func (w *wakeWorld) wake() string {
	w.t.Helper()
	w.exec(`UPDATE conductor_wakes SET created_at = now() - interval '1 minute' WHERE organization_id = $1`)
	var id string
	if err := w.s.DB.InOrg(context.Background(), w.org, func(tx pgx.Tx) error {
		if _, err := delivery.WakeConductorTx(context.Background(), tx, w.org, "wi_"+w.org, 15); err != nil {
			return err
		}
		return tx.QueryRow(context.Background(), `SELECT COALESCE(max(id), '') FROM directives WHERE sent_at IS NULL AND failed_at IS NULL`).Scan(&id)
	}); err != nil {
		w.t.Fatal(err)
	}
	return id
}

func (w *wakeWorld) pending() int {
	return w.count(`SELECT count(*) FROM conductor_wakes WHERE organization_id = $1 AND delivered_at IS NULL`)
}

// A failed wake note's late consumption receipt wins: once its retry is
// queued and not yet sent, the receipt settles the reason and withdraws
// the retry, which is never sent.
func TestALateReceiptForAFailedWakeWithdrawsItsUnsentRetry(t *testing.T) {
	w := newWakeWorld(t)
	a := w.wake()
	if a == "" {
		t.Fatal("no note for the reason")
	}
	w.exec(`UPDATE directives SET sent_at = now() WHERE run_id = 'run_'||$1`)
	w.receive(failedAfterAccepted(a, "the agent stopped"))
	if n := w.pending(); n != 1 {
		t.Fatalf("%d reasons pending after the note failed, want 1", n)
	}
	b := w.wake()
	if b == "" || b == a {
		t.Fatalf("no retry queued (%q after %q)", b, a)
	}
	w.receive(consumed(a))
	if n := w.pending(); n != 0 {
		t.Errorf("%d reasons pending after the first note was read", n)
	}
	var failed bool
	_ = w.owner.QueryRow(context.Background(), `SELECT failed_at IS NOT NULL FROM directives WHERE id = $1`, b).Scan(&failed)
	if !failed {
		t.Errorf("the unsent retry %s is still to be sent", b)
	}
	if n := w.count(`SELECT count(*) FROM directives WHERE run_id = 'run_'||$1 AND sent_at IS NULL AND failed_at IS NULL`); n != 0 {
		t.Errorf("%d notes left to send", n)
	}
	if b2 := w.wake(); b2 != "" {
		t.Errorf("a third note %s", b2)
	}
}

// The opposite order to a read reported failed: the note fails first, its
// reason goes back to pending, then its consumption receipt arrives before
// any retry: the reason is settled, and nothing is told again.
func TestAFailedWakeReadAfterAllIsNotToldAgain(t *testing.T) {
	w := newWakeWorld(t)
	a := w.wake()
	w.exec(`UPDATE directives SET sent_at = now() WHERE run_id = 'run_'||$1`)
	w.receive(failedAfterAccepted(a, "the agent stopped"))
	if n := w.pending(); n != 1 {
		t.Fatalf("%d reasons pending after the note failed, want 1", n)
	}
	w.receive(consumed(a))
	if n := w.pending(); n != 0 {
		t.Errorf("%d reasons pending after the failed note was read", n)
	}
	if b := w.wake(); b != "" {
		t.Errorf("told again: %s", b)
	}
}
