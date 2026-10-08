package notify_test

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/notify"
)

// browser is a subscribed browser: its keys, and what its push service
// received for it, decrypted as the browser would (RFC 8291, aes128gcm).
type browser struct {
	key    *ecdh.PrivateKey
	secret []byte
	mu     sync.Mutex
	got    []notify.Message
	auth   []string
	status int // what its push service answers; 201 by default
}

func newBrowser(t *testing.T) *browser {
	k, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	secret := make([]byte, 16)
	_, _ = rand.Read(secret)
	return &browser{key: k, secret: secret, status: http.StatusCreated}
}

func (b *browser) keys() (p256dh, auth string) {
	return base64.RawURLEncoding.EncodeToString(b.key.PublicKey().Bytes()), base64.RawURLEncoding.EncodeToString(b.secret)
}

func (b *browser) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.auth = append(b.auth, r.Header.Get("Authorization"))
	if b.status >= 300 {
		w.WriteHeader(b.status)
		return
	}
	plain, err := b.decrypt(body)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	var m notify.Message
	_ = json.Unmarshal(plain, &m)
	b.got = append(b.got, m)
	w.WriteHeader(http.StatusCreated)
}

// decrypt reads an aes128gcm body: salt(16) rs(4) idlen(1) keyid, then the
// record, keyed from ECDH with the sender's key and the auth secret.
func (b *browser) decrypt(body []byte) ([]byte, error) {
	salt, idlen := body[:16], int(body[20])
	senderPub, record := body[21:21+idlen], body[21+idlen:]
	sender, err := ecdh.P256().NewPublicKey(senderPub)
	if err != nil {
		return nil, err
	}
	shared, err := b.key.ECDH(sender)
	if err != nil {
		return nil, err
	}
	info := append(append([]byte("WebPush: info\x00"), b.key.PublicKey().Bytes()...), senderPub...)
	ikm, err := hkdf.Key(sha256.New, shared, b.secret, string(info), 32)
	if err != nil {
		return nil, err
	}
	cek, _ := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: aes128gcm\x00", 16)
	nonce, _ := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: nonce\x00", 12)
	block, _ := aes.NewCipher(cek)
	gcm, _ := cipher.NewGCM(block)
	plain, err := gcm.Open(nil, nonce, record, nil)
	if err != nil {
		return nil, err
	}
	// Strip the padding: the last non-zero byte is the delimiter (2).
	i := len(plain) - 1
	for i >= 0 && plain[i] == 0 {
		i--
	}
	return plain[:i], nil
}

// An ask reaches its task's owner's browsers, once; a colleague's browser
// hears only of a task nobody owns, and another organization's of nothing.
func TestAnAskReachesItsOwnersBrowsersOnce(t *testing.T) {
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	other := dbtest.Org(t, owner)
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := owner.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	// Two people: the task's owner, and a colleague.
	for _, k := range []string{"key_me_" + org, "key_colleague_" + org} {
		exec(`INSERT INTO api_keys (id, organization_id, name, key_hash, key_prefix) VALUES ($1, $2, $1, $1, 'dude_sk_')`, k, org)
	}
	exec(`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ($1, $2, 'P', $1, 'TEXT')`, "prj_"+org, org)
	exec(`INSERT INTO tasks (id, organization_id, project_id, number, title, owner_key_id) VALUES ($1, $2, $3, 19, 'Count sentences', $4)`,
		"wi_"+org, org, "prj_"+org, "key_me_"+org)
	exec(`INSERT INTO task_people (task_id, organization_id, person_id, position)
		SELECT $1, $2, person_id, 0 FROM api_keys WHERE id = $3`, "wi_"+org, org, "key_me_"+org)
	exec(`INSERT INTO people (id, organization_id, name) VALUES ($1, $2, 'Other organization')`, "per_theirs_"+other, other)
	exec(`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role)
		VALUES ($1, $2, $3, $4, 1, 'running', 'implement', 'implementer')`, "run_"+org, org, "prj_"+org, "wi_"+org)
	// And one from before there were owners.
	exec(`INSERT INTO tasks (id, organization_id, project_id, number, title) VALUES ($1, $2, $3, 20, 'Count words')`,
		"wi_legacy_"+org, org, "prj_"+org)

	mine, stale, colleague, theirs := newBrowser(t), newBrowser(t), newBrowser(t), newBrowser(t)
	stale.status = http.StatusGone
	mux := http.NewServeMux()
	mux.Handle("/mine", mine)
	mux.Handle("/stale", stale)
	mux.Handle("/colleague", colleague)
	mux.Handle("/theirs", theirs)
	push := httptest.NewServer(mux)
	t.Cleanup(push.Close)
	for _, s := range []struct {
		path, org, key string
		b              *browser
	}{
		{"/mine", org, "key_me_" + org, mine}, {"/stale", org, "key_me_" + org, stale},
		{"/colleague", org, "key_colleague_" + org, colleague}, {"/theirs", other, "", theirs},
	} {
		p256dh, auth := s.b.keys()
		exec(`INSERT INTO push_subscriptions (endpoint, organization_id, api_key_id, person_id, p256dh, auth)
			VALUES ($1, $2, NULLIF($3, ''), COALESCE((SELECT person_id FROM api_keys WHERE id = $3), $6), $4, $5)`,
			push.URL+s.path, s.org, s.key, p256dh, auth, "per_theirs_"+other)
	}

	// The owner can sign in without a key; revocation leaves browser ownership intact.
	exec(`UPDATE api_keys SET revoked_at = now() WHERE id = $1`, "key_me_"+org)
	exec(`UPDATE push_subscriptions SET api_key_id = NULL WHERE endpoint = $1`, push.URL+"/mine")
	n := &notify.Notifier{DB: app, Log: slog.New(slog.DiscardHandler), Subject: "ops@example.com", HTTP: push.Client()}
	if _, _, err := n.Keys(ctx); err != nil {
		t.Fatal(err)
	}
	// Only what is asked from now on: other tests share the database.
	exec(`UPDATE push_config SET after_cursor = (SELECT COALESCE(max(cursor), 0) FROM events)`)
	exec(`INSERT INTO events (id, organization_id, event_type, project_id, task_id, run_id, actor_type, actor_id, source, payload)
		VALUES ($1, $2, 'question.asked', $3, $4, $5, 'agent', $5, 'orchestrator', $6)`,
		"evt_q_"+org, org, "prj_"+org, "wi_"+org, "run_"+org, `{"kind":"agent","prompt":"Does an ellipsis end a sentence?"}`)
	exec(`INSERT INTO events (id, organization_id, event_type, project_id, task_id, run_id, actor_type, actor_id, source, payload)
		VALUES ($1, $2, 'agent.message', $3, $4, $5, 'agent', $5, 'runner', '{"text":"thinking"}')`,
		"evt_m_"+org, org, "prj_"+org, "wi_"+org, "run_"+org)
	exec(`INSERT INTO events (id, organization_id, event_type, project_id, task_id, actor_type, actor_id, source, payload)
		VALUES ($1, $2, 'task.ready_to_merge', $3, $4, 'system', 'orchestrator', 'orchestrator', '{}')`,
		"evt_r_"+org, org, "prj_"+org, "wi_legacy_"+org)
	// A Run of the owner's that made no progress: told on a plain delivery,
	// not where the conductor is told instead.
	exec(`INSERT INTO events (id, organization_id, event_type, project_id, task_id, run_id, actor_type, actor_id, source, payload)
		VALUES ($1, $2, 'run.stalled', $3, $4, $5, 'system', $5, 'orchestrator', $6),
		       ($7, $2, 'run.stalled', $3, $4, $5, 'system', $5, 'orchestrator', '{"conducted":true}')`,
		"evt_s_"+org, org, "prj_"+org, "wi_"+org, "run_"+org, `{"conducted":false,"text":"Your implement Run has made no progress for 2.0 h."}`,
		"evt_sc_"+org)

	// Other tests' asks may be swept too; ours must be among them.
	for range 5 {
		if _, err := n.Sweep(ctx); err != nil {
			t.Fatal(err)
		}
	}
	mine.mu.Lock()
	defer mine.mu.Unlock()
	if len(mine.got) != 3 {
		t.Fatalf("the owner's browser got %d notifications, want 3: %+v", len(mine.got), mine.got)
	}
	// Sent concurrently, so in any order.
	var m, stalled notify.Message
	for _, g := range mine.got {
		switch g.Title {
		case "TEXT-19 · Implementer asks":
			m = g
		case "TEXT-19 · Implementer has made no progress":
			stalled = g
		}
	}
	if stalled.Body != "Your implement Run has made no progress for 2.0 h." || stalled.URL != "#/task/wi_"+org {
		t.Errorf("the stall's notification = %+v", stalled)
	}
	if m.Title != "TEXT-19 · Implementer asks" || m.Body != "Does an ellipsis end a sentence?" || m.URL != "#/run/run_"+org {
		t.Errorf("notification = %+v", m)
	}
	if !strings.HasPrefix(mine.auth[0], "vapid t=") {
		t.Errorf("not signed with VAPID: %q", mine.auth[0])
	}
	colleague.mu.Lock()
	defer colleague.mu.Unlock()
	if len(colleague.got) != 1 || colleague.got[0].Title != "TEXT-20 is ready to merge" {
		t.Errorf("a colleague's browser got %+v, want only the task nobody owns", colleague.got)
	}
	if len(theirs.got) != 0 {
		t.Errorf("another organization's browser was told")
	}
	var gone bool
	if err := owner.QueryRow(ctx, `SELECT NOT EXISTS (SELECT 1 FROM push_subscriptions WHERE endpoint = $1)`, push.URL+"/stale").Scan(&gone); err != nil || !gone {
		t.Errorf("a subscription its push service says is gone was kept")
	}
}

// An ask on a task nobody owns reaches every active person's browser, a
// person whose only key is revoked included, and never the browser a
// removed person left behind.
func TestAnUnownedAskSkipsRemovedPeople(t *testing.T) {
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := owner.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	active, revoked, removed := "per_active_"+org, "per_revoked_"+org, "per_removed_"+org
	exec(`INSERT INTO people (id, organization_id, name) VALUES ($1, $4, 'Active'), ($2, $4, 'Revoked'), ($3, $4, 'Removed')`,
		active, revoked, removed, org)
	exec(`INSERT INTO api_keys (id, organization_id, person_id, name, key_hash, key_prefix) VALUES ($1, $2, $3, $1, $1, 'dude_sk_')`,
		"key_revoked_"+org, org, revoked)
	exec(`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ($1, $2, 'P', $1, 'UNOWN')`, "prj_"+org, org)
	exec(`INSERT INTO tasks (id, organization_id, project_id, number, title) VALUES ($1, $2, $3, 7, 'Nobody owns this')`,
		"wi_"+org, org, "prj_"+org)

	// Every browser has valid keys, so a delivery to the removed one would
	// be received and decrypted like any other.
	browsers := map[string]*browser{active: newBrowser(t), revoked: newBrowser(t), removed: newBrowser(t)}
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
		exec(`INSERT INTO push_subscriptions (endpoint, organization_id, person_id, api_key_id, p256dh, auth)
			VALUES ($1, $2, $3, (SELECT id FROM api_keys WHERE person_id = $3), $4, $5)`,
			push.URL+"/"+person, org, person, p256dh, auth)
	}
	// Revoking a key keeps its person and their browser; removal keeps the
	// subscription row here, as a row a removal left behind would be.
	exec(`UPDATE api_keys SET revoked_at = now() WHERE id = $1`, "key_revoked_"+org)
	exec(`UPDATE people SET removed_at = now() WHERE id = $1`, removed)
	var kept int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM push_subscriptions WHERE organization_id = $1`, org).Scan(&kept); err != nil || kept != 3 {
		t.Fatalf("subscriptions before the sweep = %d, %v; want all 3", kept, err)
	}

	n := &notify.Notifier{DB: app, Log: slog.New(slog.DiscardHandler), Subject: "ops@example.com", HTTP: push.Client()}
	if _, _, err := n.Keys(ctx); err != nil {
		t.Fatal(err)
	}
	exec(`UPDATE push_config SET after_cursor = (SELECT COALESCE(max(cursor), 0) FROM events)`)
	exec(`INSERT INTO events (id, organization_id, event_type, project_id, task_id, actor_type, actor_id, source, payload)
		VALUES ($1, $2, 'question.asked', $3, $4, 'system', 'orchestrator', 'orchestrator', '{"kind":"agent","prompt":"Which locale?"}')`,
		"evt_q_"+org, org, "prj_"+org, "wi_"+org)
	for range 5 {
		if _, err := n.Sweep(ctx); err != nil {
			t.Fatal(err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	want := map[string]int{active: 1, revoked: 1}
	if len(recipients) != len(want) || recipients[active] != 1 || recipients[revoked] != 1 {
		t.Fatalf("push deliveries by person = %v, want %v", recipients, want)
	}
	for _, person := range []string{active, revoked} {
		b := browsers[person]
		b.mu.Lock()
		got := b.got
		b.mu.Unlock()
		if len(got) != 1 || got[0].Title != "UNOWN-7 asks" || got[0].Body != "Which locale?" {
			t.Errorf("%s's browser got %+v", person, got)
		}
	}
}
