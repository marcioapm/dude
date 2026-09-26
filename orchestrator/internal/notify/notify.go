// Package notify tells people when something waits on them: a question an
// agent asked, a repository it asked for, a delivery that needs a decision.
// Each browser of the task's owner that opted in gets a Web Push
// notification, even with dude closed (docs/design/notifications.md).
//
// It reads the ledger past a stored cursor, so it needs nothing from the
// code that records an ask, and a restart neither repeats nor skips one.
// Sending is best effort: the ask is on the board whatever happens here.
package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// asks are the events a person is told of. A literal in the SQL, not a
// parameter: it is the predicate of the partial index events_asks_idx
// (migration 026), which the planner uses only when the query says the
// same.
const asks = "('question.asked', 'repository.requested', 'task.ready_to_merge')"

// Notifier sends Web Push for new asks.
type Notifier struct {
	DB  *db.DB
	Log *slog.Logger
	// Who runs this factory, for push services to reach (VAPID "sub").
	Subject string
	// VAPID keys, if configured; otherwise made once and kept in push_config.
	PublicKey, PrivateKey string
	// The push services' client (tests: a fake one).
	HTTP webpush.HTTPClient

	keysMu sync.Mutex
	ready  bool
}

// settle is how long an ask may take to commit after it was recorded: the
// watermark moves only past asks older than this, and the ones sent above
// it are remembered (push_sent). Cursors are handed out at insert and seen
// at commit, so a watermark at the newest ask read would skip one whose
// transaction was still open.
const settle = 10 * time.Minute

// Message is what the service worker shows (apps/web/public/sw.js).
type Message struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	// Replaces an earlier notification with the same tag: one per Run.
	Tag string `json:"tag"`
	// Where clicking it goes, as the web app's hash.
	URL string `json:"url"`
}

// Keys returns the VAPID key pair: configured, or kept in push_config, made
// the first time. The public half is what a browser subscribes with.
//
// It also makes push_config's row, which holds the notifier's watermark,
// configured keys or not. Read once and kept: the pair never changes.

func (n *Notifier) Keys(ctx context.Context) (public, private string, err error) {
	n.keysMu.Lock()
	defer n.keysMu.Unlock()
	if n.ready {
		return n.PublicKey, n.PrivateKey, nil
	}
	err = n.DB.InSystem(ctx, "push-keys", func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT vapid_public, vapid_private FROM push_config`).Scan(&public, &private)
		if !db.IsNotFound(err) {
			return err
		}
		if n.PublicKey != "" && n.PrivateKey != "" {
			public, private = n.PublicKey, n.PrivateKey
		} else if private, public, err = webpush.GenerateVAPIDKeys(); err != nil {
			return err
		}
		// Starting now: what was asked before notifications existed is not
		// announced. Another orchestrator racing to make the row wins.
		_, err = tx.Exec(ctx, `INSERT INTO push_config (vapid_public, vapid_private, after_cursor)
			VALUES ($1, $2, (SELECT COALESCE(max(cursor), 0) FROM events)) ON CONFLICT (id) DO NOTHING`, public, private)
		if err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT vapid_public, vapid_private FROM push_config`).Scan(&public, &private)
	})
	if err != nil {
		return "", "", err
	}
	// Configured keys win over those kept.
	if n.PublicKey == "" || n.PrivateKey == "" {
		n.PublicKey, n.PrivateKey = public, private
	}
	n.ready = true
	return n.PublicKey, n.PrivateKey, nil
}

type ask struct {
	Cursor                         int64
	Org, Type, RunID, TaskID, Task string
	Role                           string
	// The task's owner's key; empty for a task nobody owns.
	Owner   string
	Payload json.RawMessage
}

type subscription struct{ Endpoint, P256dh, Auth, Key string }

// Sweep sends a notification for each ask recorded since the last sweep, to
// the browsers of its task's owner — they are the one who answers it — or,
// for a task nobody owns, to every browser of its organization. Returns how
// many asks it handled.
func (n *Notifier) Sweep(ctx context.Context) (int, error) {
	public, private, err := n.Keys(ctx)
	if err != nil {
		return 0, err
	}
	var found []ask
	subs := map[string][]subscription{}
	if err := n.DB.InSystem(ctx, "notify", func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT e.cursor, e.organization_id, e.event_type, COALESCE(e.run_id, ''),
				COALESCE(e.task_id, ''), COALESCE(p.key_prefix || '-' || w.number, ''), COALESCE(r.role::text, ''),
				COALESCE(k.id, ''), e.payload
			FROM events e
			LEFT JOIN tasks w ON w.id = e.task_id
			-- An owner who can no longer sign in is no owner: everyone hears.
			LEFT JOIN api_keys k ON k.id = w.owner_key_id AND k.revoked_at IS NULL
			LEFT JOIN projects p ON p.id = w.project_id
			LEFT JOIN runs r ON r.id = e.run_id
			WHERE e.cursor > (SELECT after_cursor FROM push_config)
			  AND e.event_type IN `+asks+`
			  AND NOT EXISTS (SELECT 1 FROM push_sent s WHERE s.cursor = e.cursor)
			ORDER BY e.cursor LIMIT 100`)
		if err != nil {
			return err
		}
		if found, err = pgx.CollectRows(rows, pgx.RowToStructByPos[ask]); err != nil || len(found) == 0 {
			return err
		}
		orgs := make([]string, 0, len(found))
		for _, a := range found {
			orgs = append(orgs, a.Org)
		}
		rows, err = tx.Query(ctx, `SELECT organization_id, endpoint, p256dh, auth, COALESCE(api_key_id, '')
			FROM push_subscriptions WHERE organization_id = ANY ($1)`, orgs)
		if err != nil {
			return err
		}
		var org string
		var sub subscription
		_, err = pgx.ForEachRow(rows, []any{&org, &sub.Endpoint, &sub.P256dh, &sub.Auth, &sub.Key}, func() error {
			subs[org] = append(subs[org], sub)
			return nil
		})
		return err
	}); err != nil {
		return 0, err
	}
	if len(found) == 0 {
		return 0, n.advance(ctx)
	}

	// A few at a time: one slow push service must not hold up the rest.
	var mu sync.Mutex
	var gone []string
	var wg sync.WaitGroup
	slots := make(chan struct{}, 8)
	for _, a := range found {
		msg, ok := messageFor(a)
		if !ok {
			continue
		}
		body, _ := json.Marshal(msg)
		for _, s := range subs[a.Org] {
			if a.Owner != "" && s.Key != a.Owner {
				continue
			}
			wg.Add(1)
			slots <- struct{}{}
			go func() {
				defer func() { <-slots; wg.Done() }()
				if !n.send(ctx, s, body, msg.Tag, public, private) {
					mu.Lock()
					gone = append(gone, s.Endpoint)
					mu.Unlock()
				}
			}()
		}
	}
	wg.Wait()
	// Recorded as sent, sent or not: a push service that is down is not a
	// reason to announce an old ask hours later. The watermark moves past
	// asks old enough to be settled, and what is below it is forgotten.
	cursors := make([]int64, len(found))
	for i, a := range found {
		cursors[i] = a.Cursor
	}
	return len(found), n.DB.InSystem(ctx, "notify", func(tx pgx.Tx) error {
		if len(gone) > 0 {
			if _, err := tx.Exec(ctx, `DELETE FROM push_subscriptions WHERE endpoint = ANY ($1)`, gone); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `INSERT INTO push_sent (cursor) SELECT unnest($1::bigint[]) ON CONFLICT DO NOTHING`, cursors)
		return err
	})
}

// advance moves the watermark past the asks that are sent and settled —
// old enough that no transaction can still be committing one below them —
// but never past one not sent yet; and forgets what is below it.
func (n *Notifier) advance(ctx context.Context) error {
	return n.DB.InSystem(ctx, "notify", func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE push_config SET after_cursor = GREATEST(after_cursor, LEAST(
				COALESCE((SELECT max(s.cursor) FROM push_sent s JOIN events e ON e.cursor = s.cursor
				          WHERE e.occurred_at < now() - make_interval(secs => $1)), 0),
				COALESCE((SELECT min(e.cursor) - 1 FROM events e
				          WHERE e.cursor > push_config.after_cursor
				            AND e.event_type IN `+asks+`
				            AND NOT EXISTS (SELECT 1 FROM push_sent s WHERE s.cursor = e.cursor)), 9223372036854775807)))`,
			settle.Seconds()); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM push_sent WHERE cursor <= (SELECT after_cursor FROM push_config)`)
		return err
	})
}

// send delivers one message to one browser, and says whether the
// subscription is still there: its push service answers 404 or 410 for one
// that is gone (unsubscribed, expired).
func (n *Notifier) send(ctx context.Context, s subscription, body []byte, tag, public, private string) bool {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	res, err := webpush.SendNotificationWithContext(ctx, body, &webpush.Subscription{
		Endpoint: s.Endpoint, Keys: webpush.Keys{P256dh: s.P256dh, Auth: s.Auth},
	}, &webpush.Options{
		HTTPClient: n.HTTP, Subscriber: n.Subject, VAPIDPublicKey: public, VAPIDPrivateKey: private,
		TTL: int((24 * time.Hour).Seconds()), Urgency: webpush.UrgencyHigh, Topic: topic(tag),
	})
	if err != nil {
		n.Log.Warn("push failed", "endpoint", host(s.Endpoint), "error", err)
		return true
	}
	detail, _ := io.ReadAll(io.LimitReader(res.Body, 512))
	res.Body.Close()
	switch {
	case res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusGone:
		return false
	case res.StatusCode >= 300:
		n.Log.Warn("push refused", "endpoint", host(s.Endpoint), "status", res.StatusCode, "detail", strings.TrimSpace(string(detail)))
	}
	return true
}

// messageFor says what an ask's notification reads, and whether it gets
// one at all.
func messageFor(a ask) (Message, bool) {
	var p map[string]any
	_ = json.Unmarshal(a.Payload, &p)
	str := func(k string) string { v, _ := p[k].(string); return v }
	who := a.Task
	if label := delivery.RoleLabel[a.Role]; label != "" {
		who = strings.TrimSpace(who + " · " + label)
	}
	msg := Message{Tag: "run:" + a.RunID, URL: "#/run/" + a.RunID}
	if a.RunID == "" {
		msg.Tag, msg.URL = "task:"+a.TaskID, "#/task/"+a.TaskID
	}
	switch a.Type {
	case delivery.EvQuestionAsked:
		if str("kind") == "escalation" {
			msg.Title = strings.TrimSpace(a.Task + " needs a decision")
			msg.Body = strings.ReplaceAll(str("reason"), "_", " ")
		} else {
			msg.Title, msg.Body = who+" asks", str("prompt")
		}
	case delivery.EvReadyToMerge:
		msg.Title = strings.TrimSpace(a.Task + " is ready to merge")
		msg.Body = "Approved, with its checks passing. Merging is yours."
	case delivery.EvRepositoryRequested:
		verb := "Read"
		if str("access") == "write" {
			verb = "Change"
		}
		msg.Title = who + " asks for a repository"
		msg.Body = fmt.Sprintf("%s %s? %s", verb, str("repository"), str("reason"))
	default:
		return msg, false
	}
	return msg, true
}

// topic is the push service's Topic: an earlier undelivered message with
// the same one is replaced. At most 32 URL-safe characters.
func topic(tag string) string {
	var b strings.Builder
	for _, r := range tag {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	s := b.String()
	return s[max(0, len(s)-32):]
}

func host(endpoint string) string {
	if u, err := url.Parse(endpoint); err == nil {
		return u.Host
	}
	return ""
}
