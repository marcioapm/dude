package automations

// The GitHub events an organisation is subscribed to, for the event picker,
// and how long what GitHub sent is kept.

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

// Subscriptions is the picker's list and where it came from: the App's
// settings (app), the repository webhooks dude registered (webhook), or,
// when GitHub would not say, the events that have arrived (seen).
type Subscriptions struct {
	Events      []string `json:"events"`
	Source      string   `json:"source"`
	SettingsURL *string  `json:"settingsUrl"`
}

// SubscriptionsTTL is how long a read of GitHub's answer is kept.
const SubscriptionsTTL = time.Hour

// Forges finds an organisation's GitHub client (forge.Resolver).
type Forges interface {
	For(ctx context.Context, org string) (*forge.GitHub, error)
}

// ReadSubscriptions answers from the kept read when it is fresh, else asks
// GitHub (as the App: GET /app; with a PAT: one registered repository's
// webhook) and keeps the answer. When GitHub cannot be read, the events
// seen in the last 30 days, saying so.
func ReadSubscriptions(ctx context.Context, d *db.DB, forges Forges, org string, now time.Time) (Subscriptions, error) {
	var kept Subscriptions
	var readAt time.Time
	err := d.InOrg(ctx, org, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT source, events, read_at FROM forge_event_subscriptions`).Scan(&kept.Source, &kept.Events, &readAt)
	})
	if err != nil && !db.IsNotFound(err) {
		return Subscriptions{}, err
	}
	if err == nil && now.Sub(readAt) < SubscriptionsTTL {
		return withSettingsURL(ctx, forges, org, kept), nil
	}
	if read, ok := askGitHub(ctx, d, forges, org); ok {
		slices.Sort(read.Events)
		if err := d.InOrg(ctx, org, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `INSERT INTO forge_event_subscriptions (organization_id, source, events, read_at)
				VALUES ($1, $2, $3, $4) ON CONFLICT (organization_id) DO UPDATE
				SET source = EXCLUDED.source, events = EXCLUDED.events, read_at = EXCLUDED.read_at`, org, read.Source, read.Events, now)
			return err
		}); err != nil {
			return Subscriptions{}, err
		}
		return read, nil
	}
	seen := Subscriptions{Source: "seen", Events: []string{}}
	err = d.InOrg(ctx, org, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT DISTINCT event FROM webhook_deliveries
			WHERE received_at > $1::timestamptz - interval '30 days' AND event NOT IN ('ping', 'github_app_authorization')
			ORDER BY event`, now)
		if err != nil {
			return err
		}
		seen.Events, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	})
	return seen, err
}

func withSettingsURL(ctx context.Context, forges Forges, org string, s Subscriptions) Subscriptions {
	if s.Source != "app" || forges == nil {
		return s
	}
	gh, err := forges.For(ctx, org)
	if err != nil || gh == nil {
		return s
	}
	if sub, err := gh.AppEvents(ctx); err == nil && sub.SettingsURL != "" {
		s.SettingsURL = &sub.SettingsURL
	}
	return s
}

func askGitHub(ctx context.Context, d *db.DB, forges Forges, org string) (Subscriptions, bool) {
	if forges == nil {
		return Subscriptions{}, false
	}
	gh, err := forges.For(ctx, org)
	if err != nil || gh == nil {
		return Subscriptions{}, false
	}
	if gh.IsApp() {
		sub, err := gh.AppEvents(ctx)
		if err != nil {
			return Subscriptions{}, false
		}
		out := Subscriptions{Source: "app", Events: append([]string{}, sub.Events...)}
		if sub.SettingsURL != "" {
			out.SettingsURL = &sub.SettingsURL
		}
		return out, true
	}
	var url, target string
	if err := d.InOrg(ctx, org, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT r.url, r.webhook_url FROM repositories r
			WHERE r.webhook_id IS NOT NULL AND r.webhook_url IS NOT NULL ORDER BY r.webhook_registered_at DESC NULLS LAST LIMIT 1`).
			Scan(&url, &target)
	}); err != nil {
		return Subscriptions{}, false
	}
	slug := forge.SlugFromURL(url)
	if slug == "" {
		return Subscriptions{}, false
	}
	events, err := gh.HookEvents(ctx, slug, target)
	if err != nil || events == nil {
		return Subscriptions{}, false
	}
	return Subscriptions{Source: "webhook", Events: events}, true
}

// Retention: payloads 30 days, run logs 90 (Q4); a refused incoming
// request's row 7 days (it never had a body).
const (
	PayloadRetention = 30 * 24 * time.Hour
	LogRetention     = 90 * 24 * time.Hour
	RefusedRetention = 7 * 24 * time.Hour
)

// RetainBatch is how many rows one retention statement changes, each batch
// its own transaction: a backlog (the first pass after a deploy or an
// outage) is drained in short steps that never hold a table's rows long.
const RetainBatch = 5000

// Retain deletes stored deliveries past their 30 days, empties automation
// runs' payloads at 30 days and their logs at 90 (the run rows stay, so the
// Runs tab still says what ran), deletes refused incoming requests at 7 days
// and empties the other incoming deliveries' bodies at 30: one batch of each. Returns how many rows
// it changed; the loop calls it again at once while that is not 0.
func Retain(ctx context.Context, d *db.DB, now time.Time) (int, error) {
	return RetainUpTo(ctx, d, now, RetainBatch)
}

// RetainUpTo is Retain with batches of limit rows.
func RetainUpTo(ctx context.Context, d *db.DB, now time.Time, limit int) (int, error) {
	n := int64(0)
	for _, step := range []struct {
		sql  string
		args []any
	}{
		{`DELETE FROM webhook_deliveries WHERE id IN (SELECT id FROM webhook_deliveries WHERE received_at < $1
			AND (processed_at IS NOT NULL OR attempts >= 10) AND automations_at IS NOT NULL ORDER BY received_at LIMIT $2)`,
			[]any{now.Add(-PayloadRetention), limit}},
		{`UPDATE automation_runs SET payloads = '[]', payloads_purged_at = $3 WHERE id IN (SELECT id FROM automation_runs
			WHERE created_at < $1 AND payloads_purged_at IS NULL ORDER BY created_at LIMIT $2)`,
			[]any{now.Add(-PayloadRetention), limit, now}},
		// A run's activity goes with its log: the events first, then
		// log_tail once the purge marked the run empty (216).
		{`SELECT purge_automation_run_events($1, $2)`, []any{now.Add(-LogRetention), limit}},
		{`UPDATE automation_runs SET log_tail = NULL WHERE id IN (SELECT r.id FROM automation_runs r
			WHERE r.created_at < $1 AND r.log_tail IS NOT NULL AND r.activity_purged_at IS NOT NULL ORDER BY r.created_at LIMIT $2)`,
			[]any{now.Add(-LogRetention), limit}},
		{`DELETE FROM incoming_deliveries WHERE id IN (SELECT id FROM incoming_deliveries
			WHERE verdict = 'refused' AND received_at < $1 ORDER BY received_at LIMIT $2)`,
			[]any{now.Add(-RefusedRetention), limit}},
		{`DELETE FROM incoming_refusals WHERE (automation_id, hour, code) IN (SELECT automation_id, hour, code FROM incoming_refusals
			WHERE hour < $1 ORDER BY hour LIMIT $2)`,
			[]any{now.Add(-RefusedRetention), limit}},
		// A decided delivery no run holds (skipped, or its run deleted) goes at 30 days; one a run holds keeps its row for the run's page.
		{`DELETE FROM incoming_deliveries WHERE id IN (SELECT id FROM incoming_deliveries
			WHERE run_id IS NULL AND verdict IN ('started', 'sent_to_run', 'skipped') AND received_at < $1 ORDER BY received_at LIMIT $2)`,
			[]any{now.Add(-PayloadRetention), limit}},
		{`UPDATE incoming_deliveries SET payload = NULL, headers = '{}', payload_purged_at = $3 WHERE id IN (SELECT id FROM incoming_deliveries
			WHERE payload IS NOT NULL AND verdict IS NOT NULL AND received_at < $1 ORDER BY received_at LIMIT $2)`,
			[]any{now.Add(-PayloadRetention), limit, now}},
	} {
		if err := d.InSystem(ctx, "automations-retention", func(tx pgx.Tx) error {
			if strings.HasPrefix(step.sql, "SELECT") {
				var purged int64
				err := tx.QueryRow(ctx, step.sql, step.args...).Scan(&purged)
				n += purged
				return err
			}
			tag, err := tx.Exec(ctx, step.sql, step.args...)
			n += tag.RowsAffected()
			return err
		}); err != nil {
			return int(n), err
		}
	}
	return int(n), nil
}
