package notify_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/notify"
)

// pushWorld is an organisation with people, each with a browser that counts
// what reaches it, and a notifier starting at the ledger's end.
type pushWorld struct {
	t          *testing.T
	org        string
	owner      *pgx.Conn
	exec       func(string, ...any)
	mu         sync.Mutex
	recipients map[string]int
	browsers   map[string]*browser
	notifier   func() *notify.Notifier
}

// newPushWorld makes the people named (id "per_<name>_<org>", role admin
// when the name starts with "admin"), each with a browser.
func newPushWorld(t *testing.T, names ...string) (*pushWorld, map[string]string) {
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	ctx := context.Background()
	w := &pushWorld{t: t, org: org, owner: owner, recipients: map[string]int{}, browsers: map[string]*browser{}}
	w.exec = func(sql string, args ...any) {
		t.Helper()
		if _, err := owner.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	ids := map[string]string{}
	mux := http.NewServeMux()
	for _, name := range names {
		person := "per_" + name + "_" + org
		ids[name] = person
		role := "member"
		if strings.HasPrefix(name, "admin") {
			role = "admin"
		}
		w.exec(`INSERT INTO people (id, organization_id, name, role) VALUES ($1, $2, $3, $4)`, person, org, name, role)
		b := newBrowser(t)
		w.browsers[person] = b
		mux.Handle("/"+person, http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			w.mu.Lock()
			w.recipients[person]++
			w.mu.Unlock()
			b.ServeHTTP(rw, r)
		}))
	}
	push := httptest.NewServer(mux)
	t.Cleanup(push.Close)
	for _, person := range ids {
		p256dh, auth := w.browsers[person].keys()
		w.exec(`INSERT INTO push_subscriptions (endpoint, organization_id, person_id, p256dh, auth) VALUES ($1, $2, $3, $4, $5)`,
			push.URL+"/"+person, org, person, p256dh, auth)
	}
	// A fresh notifier each call: what an orchestrator restart starts with.
	w.notifier = func() *notify.Notifier {
		n := &notify.Notifier{DB: app, Log: slog.New(slog.DiscardHandler), Subject: "ops@example.com", HTTP: push.Client()}
		if _, _, err := n.Keys(ctx); err != nil {
			t.Fatal(err)
		}
		return n
	}
	w.notifier()
	w.exec(`UPDATE push_config SET after_cursor = (SELECT COALESCE(max(cursor), 0) FROM events),
		session_after_cursor = (SELECT COALESCE(max(cursor), 0) FROM events)`)
	return w, ids
}

func (w *pushWorld) sweep(n *notify.Notifier, times int) {
	w.t.Helper()
	for range times {
		if _, err := n.Sweep(context.Background()); err != nil {
			w.t.Fatal(err)
		}
	}
}

func (w *pushWorld) got() map[string]int {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := map[string]int{}
	for k, v := range w.recipients {
		out[k] = v
	}
	return out
}

// A mention is pushed once across a restart, and one recorded after it is
// pushed too: the session watermark is kept in the database, and is not
// moved past a mention not sent yet even when the asks' watermark is.
func TestAMentionIsPushedOnceAcrossARestart(t *testing.T) {
	w, ids := newPushWorld(t, "gus", "ana")
	gus, ana, org := ids["gus"], ids["ana"], w.org
	id := "ssn_r_" + org
	w.exec(`INSERT INTO sessions (id, organization_id, title, access) VALUES ($1, $2, 'Q4', 'organisation')`, id, org)
	w.exec(`INSERT INTO session_people (session_id, person_id, organization_id, role, accepted_at) VALUES ($1, $2, $3, 'owner', now())`, id, ana, org)
	mention := func(evt string) {
		w.exec(`INSERT INTO session_mentions (organization_id, session_id, person_id, by_person, text) VALUES ($1, $2, $3, $4, 'look')`, org, id, gus, ana)
		w.exec(`INSERT INTO events (id, organization_id, event_type, session_id, actor_type, actor_id, source, payload)
			VALUES ($1, $2, 'session.mentioned', $3, 'human', $4, 'orchestrator', jsonb_build_object('person', $5::text, 'byName', 'Ana', 'text', 'look'))`,
			evt+org, org, id, ana, gus)
	}
	mention("evt_m1_")
	w.sweep(w.notifier(), 3)
	if got := w.got(); got[gus] != 1 {
		t.Fatalf("first mention: %v, want Gus once", got)
	}
	// Restarted: nothing repeats.
	w.sweep(w.notifier(), 3)
	if got := w.got(); got[gus] != 1 {
		t.Fatalf("after a restart: %v, want Gus still once", got)
	}
	// The asks' watermark jumps past the next mention (a settled ask sent
	// beyond it): the mention is still pushed, once.
	w.exec(`UPDATE session_mentions SET seen_at = now() WHERE person_id = $1`, gus)
	mention("evt_m2_")
	w.exec(`UPDATE push_config SET after_cursor = (SELECT max(cursor) + 1000 FROM events)`)
	w.sweep(w.notifier(), 3)
	if got := w.got(); got[gus] != 2 {
		t.Fatalf("a mention past the asks' watermark: %v, want Gus twice", got)
	}
}

// A question the agent puts to nobody by name goes to the owner; while the
// session's admins own it (its owner left), to every active admin instead,
// and never to an ordinary member or a removed admin. Once an admin owns
// it, that admin alone, through the ordinary ask.
func TestAnOwnerQuestionOfAnAdminOwnedSessionReachesTheAdmins(t *testing.T) {
	w, ids := newPushWorld(t, "admin1", "admin2", "mem", "admingone", "lea")
	org := w.org
	a1, a2, mem, gone, lea := ids["admin1"], ids["admin2"], ids["mem"], ids["admingone"], ids["lea"]
	w.exec(`UPDATE people SET removed_at = now() WHERE id IN ($1, $2)`, gone, lea)
	id, run := "ssn_q_"+org, "run_q_"+org
	w.exec(`INSERT INTO sessions (id, organization_id, title, owned_by_admins) VALUES ($1, $2, 'Billing', true)`, id, org)
	w.exec(`INSERT INTO session_people (session_id, person_id, organization_id, role, accepted_at)
		VALUES ($1, $2, $4, 'owner', now()), ($1, $3, $4, 'chat', now())`, id, lea, mem, org)
	w.exec(`INSERT INTO runs (id, organization_id, session_id, attempt, role, kind, status) VALUES ($1, $2, $3, 1, 'brainstorm', 'agent', 'running')`, run, org, id)
	ask := func(q string) {
		w.exec(`INSERT INTO questions (id, organization_id, run_id, prompt) VALUES ($1, $2, $3, 'Which plan?')`, q+org, org, run)
		w.exec(`INSERT INTO events (id, organization_id, event_type, session_id, run_id, actor_type, actor_id, source, payload)
			VALUES ($1, $2, 'question.asked', $3, $4, 'agent', $4, 'orchestrator', jsonb_build_object('questionId', $5::text, 'prompt', 'Which plan?'))`,
			"evt_"+q+org, org, id, run, q+org)
	}
	ask("q1_")
	w.sweep(w.notifier(), 3)
	if got := w.got(); len(got) != 2 || got[a1] != 1 || got[a2] != 1 {
		t.Fatalf("an owner question of an admin-owned session reached %v, want each active admin once", got)
	}
	msg := w.browsers[a1].got[0]
	if msg.Title != "The brainstorm asks you" || msg.URL != "#/o/"+org+"/sessions/"+id {
		t.Errorf("the admin's notification: %+v", msg)
	}

	// A1 takes it over: the next question is A1's alone.
	w.exec(`UPDATE sessions SET owned_by_admins = false WHERE id = $1`, id)
	w.exec(`WITH gone AS (DELETE FROM session_people WHERE session_id = $1 AND person_id = $2 RETURNING session_id)
		INSERT INTO session_people (session_id, person_id, organization_id, role, accepted_at) SELECT $1, $3, $4, 'owner', now() FROM gone`, id, lea, a1, org)
	ask("q2_")
	w.sweep(w.notifier(), 3)
	if got := w.got(); len(got) != 2 || got[a1] != 2 || got[a2] != 1 {
		t.Fatalf("after A1 took it over: %v, want A1 twice and A2 once", got)
	}
}

// While its admins own the session, a question put by name to a member is
// that member's (the asks sweep's), and one answered before the sweep is
// nobody's: neither reaches an admin.
func TestAQuestionByNameOrAnsweredDoesNotReachTheAdmins(t *testing.T) {
	w, ids := newPushWorld(t, "admin1", "mem", "lea")
	org := w.org
	a1, mem, lea := ids["admin1"], ids["mem"], ids["lea"]
	w.exec(`UPDATE people SET removed_at = now() WHERE id = $1`, lea)
	id, run := "ssn_n_"+org, "run_n_"+org
	w.exec(`INSERT INTO sessions (id, organization_id, title, owned_by_admins) VALUES ($1, $2, 'Billing', true)`, id, org)
	w.exec(`INSERT INTO session_people (session_id, person_id, organization_id, role, accepted_at)
		VALUES ($1, $2, $4, 'owner', now()), ($1, $3, $4, 'chat', now())`, id, lea, mem, org)
	w.exec(`INSERT INTO runs (id, organization_id, session_id, attempt, role, kind, status) VALUES ($1, $2, $3, 1, 'brainstorm', 'agent', 'running')`, run, org, id)
	for _, q := range []struct{ id, to, status string }{{"q_named_", mem, "open"}, {"q_done_", "", "answered"}} {
		w.exec(`INSERT INTO questions (id, organization_id, run_id, prompt, to_person, status) VALUES ($1, $2, $3, 'Which plan?', NULLIF($4, ''), $5)`,
			q.id+org, org, run, q.to, q.status)
		w.exec(`INSERT INTO events (id, organization_id, event_type, session_id, run_id, actor_type, actor_id, source, payload)
			VALUES ($1, $2, 'question.asked', $3, $4, 'agent', $4, 'orchestrator', jsonb_build_object('questionId', $5::text, 'prompt', 'Which plan?'))`,
			"evt_"+q.id+org, org, id, run, q.id+org)
	}
	w.sweep(w.notifier(), 3)
	if got := w.got(); got[a1] != 0 || got[mem] != 1 {
		t.Fatalf("pushes %v: want the named member once and no admin", got)
	}
}

// The session sweep failing (here its sent set's grant is gone) is
// reported, and the asks sweep still sends: an ask on a task nobody owns
// reaches the organisation.
func TestASessionSweepFailingStillSendsTheAsks(t *testing.T) {
	w, ids := newPushWorld(t, "ana")
	org := w.org
	w.exec(`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ($1, $2, 'P', $1, 'TEXT')`, "prj_"+org, org)
	w.exec(`INSERT INTO tasks (id, organization_id, project_id, number, title) VALUES ($1, $2, $3, 20, 'Count words')`, "wi_"+org, org, "prj_"+org)
	w.exec(`INSERT INTO events (id, organization_id, event_type, project_id, task_id, actor_type, actor_id, source, payload)
		VALUES ($1, $2, 'task.ready_to_merge', $3, $4, 'system', 'orchestrator', 'orchestrator', '{}')`, "evt_r_"+org, org, "prj_"+org, "wi_"+org)
	w.exec(`REVOKE SELECT ON push_session_sent FROM dude_sweeper`)
	if _, err := w.notifier().Sweep(context.Background()); err == nil {
		t.Errorf("the session sweep's failure was not reported")
	}
	if got := w.got(); got[ids["ana"]] != 1 {
		t.Fatalf("with the session sweep failing the ask reached %v, want Ana once", got)
	}
}

// sessionCursor is the session sweep's watermark, -1 while unset.
func (w *pushWorld) sessionCursor(owner func(string, ...any) int64) int64 {
	return owner(`SELECT COALESCE(session_after_cursor, -1) FROM push_config`)
}

// mentionAt records a mention of person, as a message would, with the
// event at cursor (0: the next one): a transaction that took its cursor
// earlier and commits now.
func (w *pushWorld) mentionAt(session, person, by string, cursor int64) {
	w.t.Helper()
	w.exec(`INSERT INTO session_mentions (organization_id, session_id, person_id, by_person, text) VALUES ($1, $2, $3, $4, 'look')`,
		w.org, session, person, by)
	w.exec(`INSERT INTO events (cursor, id, organization_id, event_type, session_id, actor_type, actor_id, source, payload)
		OVERRIDING SYSTEM VALUE
		VALUES (CASE WHEN $5::bigint = 0 THEN nextval(pg_get_serial_sequence('events', 'cursor')) ELSE $5::bigint END,
			'evt_' || md5(random()::text), $1, 'session.mentioned', $2, 'human', $3, 'orchestrator',
			jsonb_build_object('person', $4::text, 'byName', 'Ana', 'text', 'look'))`,
		w.org, session, by, person, cursor)
}

// A fresh deploy (no session watermark yet) starts at the asks'
// watermark: a mention below it is history and never pushed, one above it
// is, and the watermark is set. Once a sent mention is settled the
// watermark moves to it and its sent row goes. A mention committed late,
// below a sent one, holds the watermark below it until it is sent: pushed
// once, never skipped.
func TestTheSessionWatermarkStartsAtTheAsksAndNeverSkipsALateMention(t *testing.T) {
	w, ids := newPushWorld(t, "ana", "old", "gus", "late")
	org, ana := w.org, ids["ana"]
	owner := w.owner
	one := func(sql string, args ...any) int64 {
		t.Helper()
		var v int64
		if err := owner.QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	id := "ssn_w_" + org
	w.exec(`INSERT INTO sessions (id, organization_id, title, access) VALUES ($1, $2, 'Q4', 'organisation')`, id, org)
	w.exec(`INSERT INTO session_people (session_id, person_id, organization_id, role, accepted_at) VALUES ($1, $2, $3, 'owner', now())`, id, ana, org)

	w.mentionAt(id, ids["old"], ana, 0)
	w.exec(`UPDATE push_config SET after_cursor = (SELECT max(cursor) FROM events), session_after_cursor = NULL`)
	w.mentionAt(id, ids["gus"], ana, 0)
	sent := one(`SELECT max(cursor) FROM events`)
	w.sweep(w.notifier(), 1)
	if got := w.got(); got[ids["gus"]] != 1 || got[ids["old"]] != 0 {
		t.Fatalf("a fresh deploy's first sweep pushed %v: want the new mention only", got)
	}
	if c := w.sessionCursor(one); c < 0 {
		t.Fatalf("the session watermark is still unset")
	}

	w.exec(`UPDATE events SET occurred_at = now() - interval '11 minutes' WHERE cursor = $1`, sent)
	w.sweep(w.notifier(), 1)
	if c, left := w.sessionCursor(one), one(`SELECT count(*) FROM push_session_sent`); c != sent || left != 0 {
		t.Fatalf("after a settled mention: watermark %d (want %d), %d sent rows left", c, sent, left)
	}

	// A mention sent far ahead, not settled yet: the watermark stays.
	ahead := sent + 1000
	w.mentionAt(id, ids["gus"], ana, ahead)
	w.sweep(w.notifier(), 1)
	if err := w.notifier().AdvanceSessions(context.Background()); err != nil {
		t.Fatal(err)
	}
	if c := w.sessionCursor(one); c != sent {
		t.Fatalf("the watermark moved to %d past a sent mention not settled (want %d)", c, sent)
	}
	// One committed late below it, unsent, while the one ahead settles:
	// the watermark stops short of the late one.
	late := sent + 500
	w.mentionAt(id, ids["late"], ana, late)
	w.exec(`UPDATE events SET occurred_at = now() - interval '11 minutes' WHERE cursor = $1`, ahead)
	if err := w.notifier().AdvanceSessions(context.Background()); err != nil {
		t.Fatal(err)
	}
	if c := w.sessionCursor(one); c != late-1 {
		t.Fatalf("the watermark is %d with an unsent mention at %d", c, late)
	}
	w.sweep(w.notifier(), 3)
	if got := w.got(); got[ids["late"]] != 1 {
		t.Fatalf("the late mention: %v, want it pushed once", got)
	}
	if c := w.sessionCursor(one); c != ahead {
		t.Errorf("once both are sent and settled the watermark is %d, want %d", c, ahead)
	}
}
