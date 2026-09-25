// Package notify tells people when something waits on them: a question an
// agent asked, a repository it asked for, a delivery that needs a decision.
// Each browser that opted in gets a Web Push notification, even with dude
// closed (docs/design/notifications.md).
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
	"strings"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
)

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
}

// Message is what the service worker shows (apps/web/public/sw.js).
type Message struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	// Replaces an earlier notification with the same tag: one per Run.
	Tag string `json:"tag"`
	// Where clicking it goes, as the web app's hash.
	URL string `json:"url"`
}

// The events that mean something waits on a person.
var asks = []string{"question.asked", "repository.requested"}

// Keys returns the VAPID key pair: configured, or kept in push_config, made
// the first time. The public half is what a browser subscribes with.
func (n *Notifier) Keys(ctx context.Context) (public, private string, err error) {
	if n.PublicKey != "" && n.PrivateKey != "" {
		return n.PublicKey, n.PrivateKey, nil
	}
	err = n.DB.InSystem(ctx, "push-keys", func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT vapid_public, vapid_private FROM push_config`).Scan(&public, &private)
		if !db.IsNotFound(err) {
			return err
		}
		if private, public, err = webpush.GenerateVAPIDKeys(); err != nil {
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
	return public, private, err
}

type ask struct {
	Cursor                                 int64
	Org, Type, RunID, WorkItemID, WorkItem string
	Role                                   string
	Payload                                json.RawMessage
}

type subscription struct{ Endpoint, P256dh, Auth string }

// Sweep sends a notification for each ask recorded since the last sweep, to
// every subscription of its organization. Returns how many asks it handled.
func (n *Notifier) Sweep(ctx context.Context) (int, error) {
	public, private, err := n.Keys(ctx)
	if err != nil {
		return 0, err
	}
	var found []ask
	subs := map[string][]subscription{}
	if err := n.DB.InSystem(ctx, "notify", func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT e.cursor, e.organization_id, e.event_type, COALESCE(e.run_id, ''),
				COALESCE(e.work_item_id, ''), COALESCE(p.key_prefix || '-' || w.number, ''), COALESCE(r.role::text, ''), e.payload
			FROM events e
			LEFT JOIN work_items w ON w.id = e.work_item_id
			LEFT JOIN projects p ON p.id = w.project_id
			LEFT JOIN runs r ON r.id = e.run_id
			WHERE e.cursor > (SELECT after_cursor FROM push_config) AND e.event_type = ANY ($1)
			ORDER BY e.cursor LIMIT 100`, asks)
		if err != nil {
			return err
		}
		if found, err = pgx.CollectRows(rows, pgx.RowToStructByPos[ask]); err != nil || len(found) == 0 {
			return err
		}
		orgs := map[string]bool{}
		for _, a := range found {
			orgs[a.Org] = true
		}
		for org := range orgs {
			rows, err := tx.Query(ctx, `SELECT endpoint, p256dh, auth FROM push_subscriptions WHERE organization_id = $1`, org)
			if err != nil {
				return err
			}
			if subs[org], err = pgx.CollectRows(rows, pgx.RowToStructByPos[subscription]); err != nil {
				return err
			}
		}
		return nil
	}); err != nil || len(found) == 0 {
		return 0, err
	}

	for _, a := range found {
		msg, ok := messageFor(a)
		if !ok {
			continue
		}
		body, _ := json.Marshal(msg)
		for _, s := range subs[a.Org] {
			n.send(ctx, a.Org, s, body, msg.Tag, public, private)
		}
	}
	// Moved past what was looked at, sent or not: a push service that is
	// down is not a reason to announce an old ask hours later.
	last := found[len(found)-1].Cursor
	return len(found), n.DB.InSystem(ctx, "notify", func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE push_config SET after_cursor = GREATEST(after_cursor, $1)`, last)
		return err
	})
}

// send delivers one message to one browser. A subscription its push service
// says is gone (404, 410: unsubscribed, expired) is forgotten.
func (n *Notifier) send(ctx context.Context, org string, s subscription, body []byte, tag, public, private string) {
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
		return
	}
	detail, _ := io.ReadAll(io.LimitReader(res.Body, 512))
	res.Body.Close()
	switch {
	case res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusGone:
		_ = n.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `DELETE FROM push_subscriptions WHERE endpoint = $1`, s.Endpoint)
			return err
		})
	case res.StatusCode >= 300:
		n.Log.Warn("push refused", "endpoint", host(s.Endpoint), "status", res.StatusCode, "detail", strings.TrimSpace(string(detail)))
	default:
		_ = n.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE push_subscriptions SET last_sent_at = now() WHERE endpoint = $1`, s.Endpoint)
			return err
		})
	}
}

// messageFor says what an ask's notification reads, and whether it gets
// one at all.
func messageFor(a ask) (Message, bool) {
	var p map[string]any
	_ = json.Unmarshal(a.Payload, &p)
	str := func(k string) string { v, _ := p[k].(string); return v }
	who := a.WorkItem
	if role := strings.ToUpper(a.Role[:min(1, len(a.Role))]) + a.Role[min(1, len(a.Role)):]; role != "" {
		who = strings.TrimSpace(who + " · " + role)
	}
	msg := Message{Tag: "run:" + a.RunID, URL: "#/run/" + a.RunID}
	if a.RunID == "" {
		msg.Tag, msg.URL = "workItem:"+a.WorkItemID, "#/workItem/"+a.WorkItemID
	}
	switch a.Type {
	case "question.asked":
		if str("kind") == "escalation" {
			msg.Title = strings.TrimSpace(a.WorkItem + " needs a decision")
			msg.Body = strings.ReplaceAll(str("reason"), "_", " ")
			return msg, true
		}
		msg.Title, msg.Body = who+" asks", str("prompt")
	case "repository.requested":
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
	rest, _ := strings.CutPrefix(endpoint, "https://")
	h, _, _ := strings.Cut(rest, "/")
	return h
}
