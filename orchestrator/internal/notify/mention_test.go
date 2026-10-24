package notify_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/notify"
)

// A session.mentioned reaches the browsers of the person mentioned alone,
// linking to the session, while their mention is unseen and they may open
// the session; one they already saw, and one in a session they can no
// longer open, sends nothing.
func TestAMentionReachesOnlyThePersonMentioned(t *testing.T) {
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := owner.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	gus, eli, ana := "per_gus_"+org, "per_eli_"+org, "per_ana_"+org
	exec(`INSERT INTO people (id, organization_id, name) VALUES ($1, $4, 'Gus'), ($2, $4, 'Eli'), ($3, $4, 'Ana')`, gus, eli, ana, org)
	open, closed := "ssn_open_"+org, "ssn_closed_"+org
	exec(`INSERT INTO sessions (id, organization_id, title, access) VALUES ($1, $3, 'Q4 cleanup', 'organisation'), ($2, $3, 'Private notes', 'private')`,
		open, closed, org)
	exec(`INSERT INTO session_people (session_id, person_id, organization_id, role, accepted_at)
		VALUES ($1, $3, $4, 'owner', now()), ($2, $3, $4, 'owner', now())`, open, closed, ana, org)
	// Gus is mentioned in the open session (not one of its people); Eli in
	// the private one, which she cannot open; Gus's earlier mention was seen.
	exec(`INSERT INTO session_mentions (organization_id, session_id, person_id, by_person, text, seen_at)
		VALUES ($1, $2, $3, $5, '@Gus can you look', NULL), ($1, $4, $6, $5, '@Eli', NULL), ($1, $2, $3, $5, 'old', now())`,
		org, open, gus, closed, ana, eli)

	browsers := map[string]*browser{gus: newBrowser(t), eli: newBrowser(t), ana: newBrowser(t)}
	var mu sync.Mutex
	recipients := map[string]int{}
	mux := http.NewServeMux()
	for person, b := range browsers {
		mux.Handle("/"+person, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			recipients[person]++
			mu.Unlock()
			b.ServeHTTP(w, r)
		}))
	}
	push := httptest.NewServer(mux)
	t.Cleanup(push.Close)
	for person, b := range browsers {
		p256dh, auth := b.keys()
		exec(`INSERT INTO push_subscriptions (endpoint, organization_id, person_id, p256dh, auth) VALUES ($1, $2, $3, $4, $5)`,
			push.URL+"/"+person, org, person, p256dh, auth)
	}
	n := &notify.Notifier{DB: app, Log: slog.New(slog.DiscardHandler), Subject: "ops@example.com", HTTP: push.Client()}
	if _, _, err := n.Keys(ctx); err != nil {
		t.Fatal(err)
	}
	exec(`UPDATE push_config SET after_cursor = (SELECT COALESCE(max(cursor), 0) FROM events),
		session_after_cursor = (SELECT COALESCE(max(cursor), 0) FROM events)`)
	exec(`INSERT INTO events (id, organization_id, event_type, session_id, actor_type, actor_id, source, payload) VALUES
		($1, $2, 'session.mentioned', $3, 'human', $5, 'orchestrator', jsonb_build_object('person', $6::text, 'byName', 'Ana', 'title', 'Q4 cleanup', 'text', '@Gus can you look')),
		($7, $2, 'session.mentioned', $4, 'human', $5, 'orchestrator', jsonb_build_object('person', $8::text, 'byName', 'Ana', 'title', 'Private notes', 'text', '@Eli'))`,
		"evt_mg_"+org, org, open, closed, ana, gus, "evt_me_"+org, eli)
	for range 5 {
		if _, err := n.Sweep(ctx); err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(recipients) != 1 || recipients[gus] != 1 {
		t.Fatalf("push deliveries by person = %v, want only Gus once", recipients)
	}
	got := browsers[gus].got
	if len(got) != 1 || got[0].Title != "Ana mentioned you in Q4 cleanup" || got[0].Body != "@Gus can you look" ||
		got[0].URL != "#/o/"+org+"/sessions/"+open {
		t.Errorf("Gus's browser got %+v", got)
	}

	// Seen before the sweep: nothing more is sent for a new mention event.
	exec(`UPDATE session_mentions SET seen_at = now() WHERE person_id = $1`, gus)
	exec(`INSERT INTO events (id, organization_id, event_type, session_id, actor_type, actor_id, source, payload) VALUES
		($1, $2, 'session.mentioned', $3, 'human', $4, 'orchestrator', jsonb_build_object('person', $5::text, 'byName', 'Ana', 'text', 'again'))`,
		"evt_mg2_"+org, org, open, ana, gus)
	mu.Unlock()
	for range 3 {
		if _, err := n.Sweep(ctx); err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	if recipients[gus] != 1 {
		t.Errorf("a seen mention was pushed: %v", recipients)
	}
}
