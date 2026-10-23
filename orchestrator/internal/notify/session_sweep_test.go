package notify_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/notify"
)

// pushWorld is an organisation with people, each with a browser that counts
// what reaches it, and a notifier starting at the ledger's end.
type pushWorld struct {
	t          *testing.T
	org        string
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
	w := &pushWorld{t: t, org: org, recipients: map[string]int{}, browsers: map[string]*browser{}}
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
