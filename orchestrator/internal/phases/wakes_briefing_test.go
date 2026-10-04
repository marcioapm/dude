package phases

import (
	"context"
	"testing"
)

// Briefed conductor A ends before its hearing is recorded: its reason goes
// back to pending, and conductor B is queued a retry, not yet sent. A's
// late handoff (an older lux's prompt receipt) is A's own report that it
// heard the reason: the reason is settled, and B's retry withdrawn, never
// sent. A mere acceptance arriving late does not override A's failure.
func TestALateBriefingHandoffWithdrawsTheUnsentRetry(t *testing.T) {
	for _, c := range []struct {
		name    string
		late    func(w *wakeWorld)
		settled bool
	}{
		{"handoff", func(w *wakeWorld) { w.receive(legacyHandoff("prompt")) }, true},
		{"read", func(w *wakeWorld) { w.receive(consumed("prompt")) }, true},
		{"accepted", func(w *wakeWorld) { w.receive(accepted("prompt", false, "next_step")) }, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newWakeWorld(t)
			w.briefed()
			w.ends()
			if n := w.pending(); n != 1 {
				t.Fatalf("%d reasons pending after A ended unheard, want 1", n)
			}
			w.exec(`UPDATE runs SET role = 'conductor', kind = 'agent', status = 'paused', dude_pause = 'conductor', turn_done_at = now()
				WHERE id = 'other_'||$1`)
			b := w.wake()
			if b == "" {
				t.Fatal("no retry queued for B")
			}
			c.late(w)
			var withdrawn bool
			_ = w.owner.QueryRow(context.Background(), `SELECT failed_at IS NOT NULL FROM directives WHERE id = $1`, b).Scan(&withdrawn)
			if withdrawn != c.settled {
				t.Errorf("B's unsent retry withdrawn %v, want %v", withdrawn, c.settled)
			}
			want := 0
			if !c.settled {
				want = 1
			}
			if n := w.count(`SELECT count(*) FROM conductor_wakes WHERE organization_id = $1 AND delivered_at IS NULL`); n != 0 {
				t.Errorf("%d reasons pending, want 0: the retry on its way holds the reason, or it is settled", n)
			}
			if n := w.count(`SELECT count(*) FROM directives WHERE run_id = 'other_'||$1 AND sent_at IS NULL AND failed_at IS NULL`); n != want {
				t.Errorf("%d notes left to send to B, want %d", n, want)
			}
			if b2 := w.wake(); b2 != "" && b2 != b {
				t.Errorf("a third note %s", b2)
			}
		})
	}
}
