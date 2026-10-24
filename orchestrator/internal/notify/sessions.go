package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// sessionAsk is a session's event that pushes to people the asks sweep does
// not resolve: the person a mention names, or every active admin for a
// question put to the owner of a session its admins own (its owner left).
type sessionAsk struct {
	Cursor     int64
	Org, Slug  string
	Type       string
	Session    string
	Payload    json.RawMessage
	Recipients []string
}

// sessionAsks reads them past the session watermark, each with who it is
// for now: a mention while it is unseen and its person may open the
// session; a question while it is open, put to nobody by name, and the
// session's admins own it. Each kind is a literal matching its partial
// index (events_session_mentioned_idx, events_asks_idx).
const sessionAsks = `WITH w AS (SELECT session_after_cursor AS after FROM push_config),
	candidates AS (
		SELECT e.* FROM events e, w WHERE e.cursor > w.after AND e.event_type = 'session.mentioned'
		UNION ALL
		SELECT e.* FROM events e, w WHERE e.cursor > w.after AND e.event_type = 'question.asked'
		  AND e.event_type IN ` + asks + ` AND e.session_id IS NOT NULL)
	SELECT e.cursor, e.organization_id, o.slug, e.event_type, e.session_id, e.payload,
		CASE WHEN e.event_type = 'session.mentioned' THEN ARRAY(
			SELECT m.person_id FROM session_mentions m WHERE m.session_id = e.session_id
			  AND m.person_id = e.payload->>'person' AND m.seen_at IS NULL
			  AND session_role(e.session_id, m.person_id) IS NOT NULL LIMIT 1)
		ELSE ARRAY(
			SELECT a.id FROM sessions s JOIN questions q ON q.id = e.payload->>'questionId'
			  JOIN people a ON a.organization_id = s.organization_id AND a.role = 'admin' AND a.removed_at IS NULL
			WHERE s.id = e.session_id AND s.owned_by_admins AND q.status = 'open' AND q.to_person IS NULL
			ORDER BY a.id) END
	FROM candidates e JOIN organizations o ON o.id = e.organization_id
	WHERE NOT EXISTS (SELECT 1 FROM push_session_sent s WHERE s.cursor = e.cursor)
	ORDER BY e.cursor LIMIT 100`

// sweepSessions sends the session asks recorded since its last sweep, and
// remembers them as the asks sweep does (push_session_sent, a watermark
// moved only past settled ones), so a restart neither repeats nor skips
// one. Returns how many it handled.
func (n *Notifier) sweepSessions(ctx context.Context, public, private string) (int, error) {
	var found []sessionAsk
	subs := map[string][]subscription{}
	if err := n.DB.InSystem(ctx, "notify", func(tx pgx.Tx) error {
		// The first sweep starts where the asks' watermark is.
		if _, err := tx.Exec(ctx, `UPDATE push_config SET session_after_cursor = after_cursor WHERE session_after_cursor IS NULL`); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, sessionAsks)
		if err != nil {
			return err
		}
		if found, err = pgx.CollectRows(rows, pgx.RowToStructByPos[sessionAsk]); err != nil || len(found) == 0 {
			return err
		}
		orgs := make([]string, 0, len(found))
		for _, a := range found {
			orgs = append(orgs, a.Org)
		}
		rows, err = tx.Query(ctx, `SELECT s.organization_id, s.endpoint, s.p256dh, s.auth, s.person_id
			FROM push_subscriptions s JOIN people p ON p.id = s.person_id AND p.organization_id = s.organization_id
			WHERE s.organization_id = ANY ($1) AND p.removed_at IS NULL`, orgs)
		if err != nil {
			return err
		}
		var org string
		var sub subscription
		_, err = pgx.ForEachRow(rows, []any{&org, &sub.Endpoint, &sub.P256dh, &sub.Auth, &sub.Person}, func() error {
			subs[org] = append(subs[org], sub)
			return nil
		})
		return err
	}); err != nil {
		return 0, err
	}
	if len(found) == 0 {
		return 0, n.advanceSessions(ctx)
	}

	var mu sync.Mutex
	var gone []string
	var wg sync.WaitGroup
	slots := make(chan struct{}, 8)
	for _, a := range found {
		msg, ok := sessionMessageFor(a)
		if !ok || len(a.Recipients) == 0 {
			continue
		}
		body, _ := json.Marshal(msg)
		for _, s := range subs[a.Org] {
			if !slices.Contains(a.Recipients, s.Person) {
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
		_, err := tx.Exec(ctx, `INSERT INTO push_session_sent (cursor) SELECT unnest($1::bigint[]) ON CONFLICT DO NOTHING`, cursors)
		return err
	})
}

// advanceSessions is advance for the session watermark: past what is sent
// and settled, never past a session ask not sent yet.
func (n *Notifier) advanceSessions(ctx context.Context) error {
	return n.DB.InSystem(ctx, "notify", func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE push_config SET session_after_cursor = GREATEST(session_after_cursor, LEAST(
				COALESCE((SELECT max(s.cursor) FROM push_session_sent s JOIN events e ON e.cursor = s.cursor
				          WHERE e.occurred_at < now() - make_interval(secs => $1)), 0),
				COALESCE((SELECT min(e.cursor) - 1 FROM events e
				          WHERE e.cursor > push_config.session_after_cursor
				            AND e.event_type = 'session.mentioned'
				            AND NOT EXISTS (SELECT 1 FROM push_session_sent s WHERE s.cursor = e.cursor)), 9223372036854775807),
				COALESCE((SELECT min(e.cursor) - 1 FROM events e
				          WHERE e.cursor > push_config.session_after_cursor
				            AND e.event_type = 'question.asked' AND e.event_type IN `+asks+` AND e.session_id IS NOT NULL
				            AND NOT EXISTS (SELECT 1 FROM push_session_sent s WHERE s.cursor = e.cursor)), 9223372036854775807)))`,
			settle.Seconds()); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM push_session_sent WHERE cursor <= (SELECT session_after_cursor FROM push_config)`)
		return err
	})
}

// sessionMessageFor is what a session ask's notification reads: a mention
// quotes its words; a question reads as any session question does.
func sessionMessageFor(a sessionAsk) (Message, bool) {
	if a.Type != delivery.EvSessionMentioned {
		return messageFor(ask{Cursor: a.Cursor, Org: a.Org, Slug: a.Slug, Type: a.Type, Session: a.Session, Payload: a.Payload})
	}
	var p map[string]any
	_ = json.Unmarshal(a.Payload, &p)
	str := func(k string) string { v, _ := p[k].(string); return v }
	title := str("title")
	if title == "" {
		title = delivery.UntitledSession
	}
	base := ask{Slug: a.Slug}
	return Message{Tag: "session:" + a.Session, URL: link(base, "sessions/"+a.Session),
		Title: fmt.Sprintf("%s mentioned you in %s", str("byName"), title), Body: str("text")}, true
}
