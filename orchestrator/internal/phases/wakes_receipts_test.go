package phases

import (
	"context"
	"errors"
	"testing"
	"time"

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

// A failed wake note's late consumption receipt, in the conductor's
// follower batch, and a wake sweep for the same conductor, at once: the
// batch holds the conductor's Run and waits on the note's directive; the
// sweep starts and waits on the batch. Neither deadlocks: both commit, the
// note is read, its reason settled, and no retry is queued.
func TestALateWakeReceiptAndAWakeSweepBothCommit(t *testing.T) {
	w := newWakeWorld(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	a := w.wake()
	if a == "" {
		t.Fatal("no note for the reason")
	}
	w.exec(`UPDATE directives SET sent_at = now() WHERE run_id = 'run_'||$1`)
	w.receive(failedAfterAccepted(a, "the agent stopped"))
	w.exec(`UPDATE conductor_wakes SET created_at = now() - interval '1 minute' WHERE organization_id = $1`)
	if n := w.pending(); n != 1 {
		t.Fatalf("%d reasons pending after the note failed, want 1", n)
	}

	// The gate holds A's directive row only.
	gateConn, err := pgx.Connect(ctx, w.owner.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = gateConn.Close(context.Background()) })
	gate, err := gateConn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = gate.Rollback(context.Background()) })
	if _, err := gate.Exec(ctx, `SELECT 1 FROM directives WHERE id = $1 FOR UPDATE`, a); err != nil {
		t.Fatal(err)
	}
	var gatePID int
	if err := gate.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&gatePID); err != nil {
		t.Fatal(err)
	}

	// The real follower, fed the late consumption receipt.
	st := newStreamLux(&placementLux{})
	w.s.Lux = st
	followCtx, stopFollowing := context.WithCancel(ctx)
	followed := make(chan error, 1)
	go func() { followed <- w.s.followOutput(followCtx, w.tr.run) }()
	// Before the gate's rollback and the database's drop: the follower is
	// stopped, and gone, or the test says so.
	t.Cleanup(func() {
		stopFollowing()
		select {
		case <-followed:
		case <-time.After(5 * time.Second):
			t.Error("the follower did not stop")
		}
	})
	typ, data := consumed(a)
	select {
	case st.frames <- cursorFrame(record(1, typ, data), "c1"):
	case <-ctx.Done():
		t.Fatal("the follower never took the receipt")
	}
	followerPID := waiterOn(ctx, t, w.owner, gatePID, 0, "the receipt's batch")

	// The real sweep, under the task's Chat lock, on another backend.
	wakePID := make(chan int, 1)
	woke := make(chan error, 1)
	go func() {
		woke <- w.s.DB.InOrg(ctx, w.org, func(tx pgx.Tx) error {
			var pid int
			if err := tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
				return err
			}
			wakePID <- pid
			if err := delivery.LockChat(ctx, tx, "wi_"+w.org); err != nil {
				return err
			}
			_, err := delivery.WakeConductorTx(ctx, tx, w.org, "wi_"+w.org, 15)
			return err
		})
	}()
	var sweepPID int
	select {
	case sweepPID = <-wakePID:
	case err := <-woke:
		t.Fatalf("the sweep ended before it started: %v", err)
	case <-ctx.Done():
		t.Fatal("the sweep never started")
	}
	waiterOn(ctx, t, w.owner, followerPID, sweepPID, "the sweep")

	if err := gate.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-woke:
		if err != nil {
			t.Fatalf("the sweep failed: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("the sweep did not finish")
	}
	for {
		var cursor string
		if err := w.owner.QueryRow(ctx, `SELECT COALESCE(lux_cursor, '') FROM runs WHERE id = 'run_'||$1`, w.org).
			Scan(&cursor); err != nil {
			t.Fatal(err)
		}
		if cursor == "c1" {
			break
		}
		select {
		case err := <-followed:
			t.Fatalf("the receipt's batch failed: %v", err)
		case <-ctx.Done():
			t.Fatal("the receipt's batch did not commit")
		case <-time.After(10 * time.Millisecond):
		}
	}

	countOf := func(sql string) int {
		t.Helper()
		var n int
		if err := w.owner.QueryRow(ctx, sql, a).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := countOf(`SELECT count(*) FROM directives WHERE id = $1 AND delivered_at IS NOT NULL AND failed_at IS NULL`); n != 1 {
		t.Error("the note is not read")
	}
	if n := countOf(`SELECT count(*) FROM events WHERE event_type = 'run.directive.delivered'
		AND payload->>'directiveId' = $1 AND (payload->>'read')::boolean`); n != 1 {
		t.Errorf("%d read events, want 1", n)
	}
	if n := w.pending(); n != 0 {
		t.Errorf("%d reasons pending after the note was read", n)
	}
	if n := w.count(`SELECT count(*) FROM directives WHERE run_id = 'run_'||$1 AND sent_at IS NULL AND failed_at IS NULL`); n != 0 {
		t.Errorf("%d retries left to send", n)
	}
}

// waiterOn waits for a backend to wait on a lock holder holds — waiter
// itself, or with waiter 0 any backend of the test's database — and
// returns it.
func waiterOn(ctx context.Context, t *testing.T, owner *pgx.Conn, holder, waiter int, what string) int {
	t.Helper()
	for {
		var pid int
		err := owner.QueryRow(ctx, `SELECT pid FROM pg_stat_activity WHERE datname = current_database()
			AND ($2 = 0 OR pid = $2) AND $1 = ANY (pg_blocking_pids(pid)) ORDER BY pid LIMIT 1`, holder, waiter).Scan(&pid)
		switch {
		case err == nil:
			return pid
		case !errors.Is(err, pgx.ErrNoRows):
			t.Fatalf("waiting for %s: %v", what, err)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("%s never waited on backend %d", what, holder)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// briefed makes the world's Run a conductor dude started with its reason
// in its first prompt: running, the reason delivered to it as its
// briefing.
func (w *wakeWorld) briefed() {
	w.t.Helper()
	w.exec(`UPDATE runs SET status = 'running', dude_pause = NULL, turn_done_at = NULL WHERE id = 'run_'||$1`)
	w.exec(`UPDATE conductor_wakes SET delivered_at = now(), conductor_run_id = 'run_'||$1 WHERE organization_id = $1`)
	w.exec(`INSERT INTO conductor_wake_attempts (organization_id, wake_id, conductor_run_id)
		SELECT $1, id, 'run_'||$1 FROM conductor_wakes WHERE organization_id = $1`)
}

// ends ends the briefed conductor, as the syncer settles it (HandOver).
func (w *wakeWorld) ends() {
	w.t.Helper()
	w.exec(`UPDATE runs SET status = 'failed', ended_at = now() WHERE id = 'run_'||$1`)
	if err := w.s.DB.InOrg(context.Background(), w.org, func(tx pgx.Tx) error {
		_, err := delivery.HandOver(context.Background(), tx, delivery.RunRef{Org: w.org, ProjectID: "prj_" + w.org,
			TaskID: "wi_" + w.org, RunID: "run_" + w.org}, true)
		return err
	}); err != nil {
		w.t.Fatal(err)
	}
}

// A briefing lux accepted with a read receipt to follow is heard only when
// the agent reads it. Accepted and then lost — the prompt failed, or the
// conductor ended before reading it — its reason is told again; read, it
// is not, and a read after a failure wins.
func TestABriefingIsHeardWhenRead(t *testing.T) {
	for _, c := range []struct {
		name    string
		after   func(w *wakeWorld)
		pending int
	}{
		{"accepted then failed", func(w *wakeWorld) { w.receive(failedAfterAccepted("prompt", "the harness died")) }, 1},
		{"accepted then ended", func(w *wakeWorld) { w.ends() }, 1},
		{"accepted then read", func(w *wakeWorld) { w.receive(consumed("prompt")); w.ends() }, 0},
		{"failed then read", func(w *wakeWorld) {
			w.receive(failedAfterAccepted("prompt", "the harness died"))
			w.receive(consumed("prompt"))
		}, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newWakeWorld(t)
			w.briefed()
			w.receive(accepted("prompt", true, "next_step"))
			if n := w.pending(); n != 0 {
				t.Fatalf("%d reasons pending while the briefing is on its way", n)
			}
			c.after(w)
			if n := w.pending(); n != c.pending {
				t.Errorf("%d reasons pending, want %d", n, c.pending)
			}
		})
	}
	// With no receipt to follow, accepted is heard; ending the conductor
	// does not requeue its reason.
	t.Run("accepted without a receipt", func(t *testing.T) {
		w := newWakeWorld(t)
		w.briefed()
		w.receive(accepted("prompt", false, "next_step"))
		w.ends()
		if n := w.pending(); n != 0 {
			t.Errorf("%d reasons pending after a receipt-less acceptance", n)
		}
	})
}
