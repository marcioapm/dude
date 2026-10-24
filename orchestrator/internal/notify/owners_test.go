package notify_test

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
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

	// A removal (nothing put to Ana) is done once swept, and passed once settled.
	w.exec(`INSERT INTO events (id, organization_id, event_type, actor_type, actor_id, source, payload)
		VALUES ('evt_' || md5(random()::text), $1, 'person.removed', 'human', 'per_admin', 'control-plane', jsonb_build_object('personId', $2::text))`,
		org, ana)
	removed := one(`SELECT max(cursor) FROM events`)
	w.sweep(w.notifier(), 1)
	if n := one(`SELECT count(*) FROM session_owner_done WHERE cursor = $1`, removed); n != 1 {
		t.Fatalf("the person.removed is not marked done (%d)", n)
	}
	w.exec(`UPDATE events SET occurred_at = now() - interval '11 minutes' WHERE cursor = $1`, removed)
	w.sweep(w.notifier(), 1)
	if c := one(`SELECT after_cursor FROM session_owner_sweep`); c != removed {
		t.Fatalf("after a settled removal: watermark %d, want %d", c, removed)
	}
	done = removed

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

// A change whose work fails every time is given up, and logged, once it
// has failed five times and first failed over ten minutes ago (neither
// alone suffices): the watermark passes it, and a later change is told.
func TestTheOwnerSweepGivesUpAChangeThatAlwaysFails(t *testing.T) {
	w, ids := newPushWorld(t, "ana")
	org, ana := w.org, ids["ana"]
	ctx := context.Background()
	one := func(sql string, args ...any) int64 {
		t.Helper()
		var v int64
		if err := w.owner.QueryRow(ctx, sql, args...).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	stuck, later := "ssn_stuck_"+org, "ssn_later_"+org
	for _, id := range []string{stuck, later} {
		w.exec(`INSERT INTO sessions (id, organization_id, title, owned_by_admins) VALUES ($1, $2, 'Q4', true)`, id, org)
		w.exec(`INSERT INTO session_people (session_id, person_id, organization_id, role, accepted_at) VALUES ($1, $2, $3, 'owner', now())`, id, ana, org)
		w.exec(`INSERT INTO runs (id, organization_id, session_id, attempt, role, kind, status) VALUES ($1, $2, $3, 1, 'brainstorm', 'agent', 'running')`,
			"run_"+id, org, id)
	}
	// Telling the stuck session's agent fails every time.
	w.exec(`CREATE FUNCTION stuck_fails() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN IF NEW.run_id = 'run_` + stuck + `' THEN RAISE EXCEPTION 'the directive cannot be queued'; END IF; RETURN NEW; END $$`)
	w.exec(`CREATE TRIGGER stuck_fails BEFORE INSERT ON directives FOR EACH ROW EXECUTE FUNCTION stuck_fails()`)
	w.exec(`UPDATE session_owner_sweep SET after_cursor = (SELECT COALESCE(max(cursor), 0) FROM events)`)

	w.ownerEventAt(stuck, ana, 0)
	failing := one(`SELECT max(cursor) FROM events`)
	var logged bytes.Buffer
	n := w.notifier()
	n.Log = slog.New(slog.NewTextHandler(&logged, nil))
	for attempt := 1; attempt < 5; attempt++ {
		// The fourth fails long after the first: still short of five, it is not given up.
		if attempt == 4 {
			w.exec(`UPDATE session_owner_attempts SET first_failed_at = now() - interval '11 minutes' WHERE cursor = $1`, failing)
		}
		if _, err := n.Sweep(ctx); err == nil || !strings.Contains(err.Error(), "the directive cannot be queued") {
			t.Fatalf("attempt %d: %v, want the change's error", attempt, err)
		}
		if d := one(`SELECT count(*) FROM session_owner_done WHERE cursor = $1`, failing); d != 0 {
			t.Fatalf("attempt %d: given up early", attempt)
		}
	}
	// Five failures within ten minutes of the first (an outage of a few sweeps) give nothing up.
	w.exec(`UPDATE session_owner_attempts SET first_failed_at = now() - interval '9 minutes' WHERE cursor = $1`, failing)
	if _, err := n.Sweep(ctx); err == nil || !strings.Contains(err.Error(), "the directive cannot be queued") {
		t.Fatalf("five quick attempts: %v, want the change's error", err)
	}
	if d := one(`SELECT count(*) FROM session_owner_done WHERE cursor = $1`, failing); d != 0 {
		t.Fatalf("given up after five failures within ten minutes")
	}
	w.exec(`UPDATE session_owner_attempts SET attempts = 4, first_failed_at = now() - interval '11 minutes' WHERE cursor = $1`, failing)
	if _, err := n.Sweep(ctx); err != nil {
		t.Fatalf("the fifth attempt, the first over ten minutes ago: %v, want it given up", err)
	}
	if d := one(`SELECT count(*) FROM session_owner_done WHERE cursor = $1`, failing); d != 1 {
		t.Fatalf("not marked done after five attempts")
	}
	if got := logged.String(); !strings.Contains(got, "given up") || !strings.Contains(got, fmt.Sprintf("cursor=%d", failing)) ||
		!strings.Contains(got, "the directive cannot be queued") {
		t.Errorf("the give-up is not logged with its cursor and error: %q", got)
	}

	w.ownerEventAt(later, ana, 0)
	told := one(`SELECT max(cursor) FROM events`)
	w.sweep(w.notifier(), 1)
	if c := one(`SELECT count(*) FROM events WHERE session_id = $1 AND event_type = 'session.told'`, later); c != 1 {
		t.Errorf("the later change was told %d times, want once", c)
	}
	if c := one(`SELECT count(*) FROM events WHERE session_id = $1 AND event_type = 'session.told'`, stuck); c != 0 {
		t.Errorf("the change given up was told")
	}
	w.exec(`UPDATE events SET occurred_at = now() - interval '11 minutes' WHERE cursor IN ($1, $2)`, failing, told)
	w.sweep(w.notifier(), 1)
	if c := one(`SELECT after_cursor FROM session_owner_sweep`); c < told {
		t.Errorf("the watermark is %d, short of the later change at %d", c, told)
	}
	if left := one(`SELECT count(*) FROM session_owner_attempts`); left != 0 {
		t.Errorf("%d attempt rows left below the watermark", left)
	}
}
