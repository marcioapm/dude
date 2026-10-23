package notify_test

import (
	"context"
	"testing"
)

// ownerEventAt records a session.owner_left of session by person at cursor
// (0: the next one): a removal that took its cursor earlier and commits now.
func (w *pushWorld) ownerEventAt(session, person string, cursor int64) {
	w.t.Helper()
	w.exec(`INSERT INTO events (cursor, id, organization_id, event_type, session_id, actor_type, actor_id, source, payload)
		OVERRIDING SYSTEM VALUE
		VALUES (CASE WHEN $4::bigint = 0 THEN nextval(pg_get_serial_sequence('events', 'cursor')) ELSE $4::bigint END,
			'evt_' || md5(random()::text), $1, 'session.owner_left', $2, 'human', 'per_admin', 'control-plane',
			jsonb_build_object('owner', $3::text, 'ownerName', 'Ana'))`,
		w.org, session, person, cursor)
}

// The owner sweep marks each change done once, across restarts; its
// watermark moves past a change only once it is done and settled, and
// stops short of one committed late below a done one until that is done.
func TestTheOwnerSweepWatermarkNeverSkipsALateChange(t *testing.T) {
	w, ids := newPushWorld(t, "ana")
	org, ana := w.org, ids["ana"]
	one := func(sql string, args ...any) int64 {
		t.Helper()
		var v int64
		if err := w.owner.QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	id := "ssn_o_" + org
	w.exec(`INSERT INTO sessions (id, organization_id, title, owned_by_admins) VALUES ($1, $2, 'Q4', true)`, id, org)
	w.exec(`INSERT INTO session_people (session_id, person_id, organization_id, role, accepted_at) VALUES ($1, $2, $3, 'owner', now())`, id, ana, org)
	w.exec(`UPDATE people SET removed_at = now() WHERE id = $1`, ana)
	w.exec(`UPDATE session_owner_sweep SET after_cursor = (SELECT COALESCE(max(cursor), 0) FROM events)`)
	start := one(`SELECT after_cursor FROM session_owner_sweep`)

	w.ownerEventAt(id, ana, 0)
	done := one(`SELECT max(cursor) FROM events`)
	w.sweep(w.notifier(), 1)
	w.sweep(w.notifier(), 1)
	if n := one(`SELECT count(*) FROM session_owner_done WHERE cursor = $1`, done); n != 1 {
		t.Fatalf("the change is not marked done (%d)", n)
	}
	if c := one(`SELECT after_cursor FROM session_owner_sweep`); c != start {
		t.Fatalf("the watermark moved to %d past a change not settled", c)
	}
	w.exec(`UPDATE events SET occurred_at = now() - interval '11 minutes' WHERE cursor = $1`, done)
	w.sweep(w.notifier(), 1)
	if c, left := one(`SELECT after_cursor FROM session_owner_sweep`), one(`SELECT count(*) FROM session_owner_done`); c != done || left != 0 {
		t.Fatalf("after a settled change: watermark %d (want %d), %d done rows left", c, done, left)
	}

	// One done far ahead and settled, one committed late below it not done
	// yet: the watermark stops short of the late one, then passes both.
	ahead, late := done+1000, done+500
	w.ownerEventAt(id, ana, ahead)
	w.sweep(w.notifier(), 1)
	w.ownerEventAt(id, ana, late)
	w.exec(`UPDATE events SET occurred_at = now() - interval '11 minutes' WHERE cursor IN ($1, $2)`, ahead, late)
	if err := w.notifier().AdvanceOwners(context.Background()); err != nil {
		t.Fatal(err)
	}
	if c := one(`SELECT after_cursor FROM session_owner_sweep`); c != late-1 {
		t.Fatalf("the watermark is %d with a change not done at %d", c, late)
	}
	w.sweep(w.notifier(), 2)
	if n := one(`SELECT count(*) FROM session_owner_done`); n != 0 {
		t.Errorf("%d done rows left below the watermark", n)
	}
	if c := one(`SELECT after_cursor FROM session_owner_sweep`); c != ahead {
		t.Errorf("once both are done and settled the watermark is %d, want %d", c, ahead)
	}
}
