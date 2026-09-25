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
	"encoding/binary"
	"encoding/json"
	"io"
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
	_ = binary.BigEndian
	return plain[:i], nil
}

func TestAnAskReachesEveryBrowserOfItsOrganizationOnce(t *testing.T) {
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
	exec(`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ($1, $2, 'P', $1, 'TEXT')`, "prj_"+org, org)
	exec(`INSERT INTO work_items (id, organization_id, project_id, number, title) VALUES ($1, $2, $3, 19, 'Count sentences')`,
		"wi_"+org, org, "prj_"+org)
	exec(`INSERT INTO runs (id, organization_id, project_id, work_item_id, attempt, status, phase, role)
		VALUES ($1, $2, $3, $4, 1, 'running', 'implement', 'implementer')`, "run_"+org, org, "prj_"+org, "wi_"+org)

	mine, stale, theirs := newBrowser(t), newBrowser(t), newBrowser(t)
	stale.status = http.StatusGone
	mux := http.NewServeMux()
	mux.Handle("/mine", mine)
	mux.Handle("/stale", stale)
	mux.Handle("/theirs", theirs)
	push := httptest.NewServer(mux)
	t.Cleanup(push.Close)
	for org, b := range map[string][]struct {
		path string
		b    *browser
	}{org: {{"/mine", mine}, {"/stale", stale}}, other: {{"/theirs", theirs}}} {
		for _, s := range b {
			p256dh, auth := s.b.keys()
			exec(`INSERT INTO push_subscriptions (endpoint, organization_id, p256dh, auth) VALUES ($1, $2, $3, $4)`,
				push.URL+s.path, org, p256dh, auth)
		}
	}

	n := &notify.Notifier{DB: app, Log: slog.New(slog.DiscardHandler), Subject: "ops@example.com", HTTP: push.Client()}
	if _, _, err := n.Keys(ctx); err != nil {
		t.Fatal(err)
	}
	// Only what is asked from now on: other tests share the database.
	exec(`UPDATE push_config SET after_cursor = (SELECT COALESCE(max(cursor), 0) FROM events)`)
	exec(`INSERT INTO events (id, organization_id, event_type, project_id, work_item_id, run_id, actor_type, actor_id, source, payload)
		VALUES ($1, $2, 'question.asked', $3, $4, $5, 'agent', $5, 'orchestrator', $6)`,
		"evt_q_"+org, org, "prj_"+org, "wi_"+org, "run_"+org, `{"kind":"agent","prompt":"Does an ellipsis end a sentence?"}`)
	exec(`INSERT INTO events (id, organization_id, event_type, project_id, work_item_id, run_id, actor_type, actor_id, source, payload)
		VALUES ($1, $2, 'agent.message', $3, $4, $5, 'agent', $5, 'runner', '{"text":"thinking"}')`,
		"evt_m_"+org, org, "prj_"+org, "wi_"+org, "run_"+org)

	// Other tests' asks may be swept too; ours must be among them.
	for range 5 {
		if _, err := n.Sweep(ctx); err != nil {
			t.Fatal(err)
		}
	}
	mine.mu.Lock()
	defer mine.mu.Unlock()
	if len(mine.got) != 1 {
		t.Fatalf("the browser got %d notifications, want 1: %+v", len(mine.got), mine.got)
	}
	m := mine.got[0]
	if m.Title != "TEXT-19 · Implementer asks" || m.Body != "Does an ellipsis end a sentence?" || m.URL != "#/run/run_"+org {
		t.Errorf("notification = %+v", m)
	}
	if !strings.HasPrefix(mine.auth[0], "vapid t=") {
		t.Errorf("not signed with VAPID: %q", mine.auth[0])
	}
	if len(theirs.got) != 0 {
		t.Errorf("another organization's browser was told")
	}
	var gone bool
	if err := owner.QueryRow(ctx, `SELECT NOT EXISTS (SELECT 1 FROM push_subscriptions WHERE endpoint = $1)`, push.URL+"/stale").Scan(&gone); err != nil || !gone {
		t.Errorf("a subscription its push service says is gone was kept")
	}
	_ = pgx.ErrNoRows
}
