package automations

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

type oneForge struct{ gh *forge.GitHub }

func (f oneForge) For(context.Context, string) (*forge.GitHub, error) { return f.gh, nil }

func exec(t *testing.T, owner *pgx.Conn, sql string, args ...any) {
	t.Helper()
	if _, err := owner.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func TestSubscriptionsFromTheApp(t *testing.T) {
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	ctx := context.Background()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	fake := fakegithub.NewApp(9001, "dude-acme", &key.PublicKey)
	fake.Set(func(a *fakegithub.App) { a.Events = []string{"push", "pull_request", "issues"} })
	srv := httptest.NewServer(fake.Handler())
	t.Cleanup(srv.Close)
	pemKey := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	gh := forge.NewGitHub(forge.Credential{Auth: "github_app", Secret: string(pemKey), APIBaseURL: srv.URL, AppID: "9001"})

	now := time.Now()
	got, err := ReadSubscriptions(ctx, app, oneForge{gh}, org, now)
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != "app" || !slices.Equal(got.Events, []string{"issues", "pull_request", "push"}) {
		t.Fatalf("got %+v", got)
	}
	if got.SettingsURL == nil || *got.SettingsURL != "https://github.com/settings/apps/dude-acme/permissions" {
		t.Errorf("settings URL %v", got.SettingsURL)
	}
	// Kept for an hour: a change on GitHub shows after it.
	fake.Set(func(a *fakegithub.App) { a.Events = []string{"push", "pull_request", "issues", "workflow_run"} })
	again, _ := ReadSubscriptions(ctx, app, oneForge{gh}, org, now.Add(time.Minute))
	if len(again.Events) != 3 {
		t.Errorf("read again within the hour: %v", again.Events)
	}
	later, _ := ReadSubscriptions(ctx, app, oneForge{gh}, org, now.Add(SubscriptionsTTL+time.Minute))
	if len(later.Events) != 4 {
		t.Errorf("read after the hour: %v", later.Events)
	}

	// GitHub down and nothing kept: the events seen, saying so.
	other := dbtest.Org(t, owner)
	exec(t, owner, `INSERT INTO webhook_deliveries (id, organization_id, event, payload) VALUES
		('a', $1, 'push', '{}'), ('b', $1, 'check_run', '{}'), ('c', $1, 'push', '{}'), ('d', $1, 'ping', '{}')`, other)
	fake.Set(func(a *fakegithub.App) { a.Down = http.StatusBadGateway })
	seen, err := ReadSubscriptions(ctx, app, oneForge{gh}, other, now)
	if err != nil {
		t.Fatal(err)
	}
	if seen.Source != "seen" || !slices.Equal(seen.Events, []string{"check_run", "push"}) {
		t.Errorf("got %+v", seen)
	}
}

func TestSubscriptionsFromAPATsWebhook(t *testing.T) {
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	ctx := context.Background()
	fake := fakegithub.New(t.TempDir(), "acme/web")
	fake.Set(func(s *fakegithub.Server) {
		s.Hooks = []map[string]any{{"id": 1, "events": []any{"pull_request", "push"}, "config": map[string]any{"url": "https://dude.example.com/v1/webhooks/github/" + org}}}
	})
	srv := httptest.NewServer(fake.Handler())
	t.Cleanup(srv.Close)
	gh := forge.NewGitHub(forge.Credential{Auth: "pat", Secret: "ghp_x", APIBaseURL: srv.URL})
	exec(t, owner, `INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ($1 || '_p', $1, 'W', 'w', 'W')`, org)
	exec(t, owner, `INSERT INTO repositories (id, organization_id, project_id, name, url, webhook_id, webhook_url, webhook_registered_at)
		VALUES ($1 || '_r', $1, $1 || '_p', 'web', 'https://github.com/acme/web.git', '1', 'https://dude.example.com/v1/webhooks/github/' || $1, now())`, org)
	got, err := ReadSubscriptions(ctx, app, oneForge{gh}, org, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != "webhook" || !slices.Equal(got.Events, []string{"pull_request", "push"}) || got.SettingsURL != nil {
		t.Errorf("got %+v", got)
	}
}

// A retention backlog is drained a batch per pass, each pass changing at
// most a batch of each: the old deliveries go and the old runs' payloads
// empty over several passes, then a pass changes nothing.
func TestRetainDrainsABacklogInBatches(t *testing.T) {
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	ctx := context.Background()
	now := time.Now()
	old := now.Add(-40 * 24 * time.Hour)
	exec(t, owner, `INSERT INTO webhook_deliveries (id, organization_id, event, payload, received_at, processed_at, automations_at)
		SELECT 'old-' || g, $1, 'push', '{}', $2, $2, $2 FROM generate_series(1, 5) g`, org, old)
	exec(t, owner, `INSERT INTO automations (id, organization_id, name, kind, trigger, command)
		VALUES ('aut_b', $1, 'B', 'command', '{"kind":"schedule","cron":"0 * * * *","tz":"UTC"}', 'true')`, org)
	exec(t, owner, `INSERT INTO automation_runs (id, organization_id, automation_id, status, source, payloads, created_at)
		SELECT 'arn_b' || g, $1, 'aut_b', 'succeeded', 'event', '[{"deliveryId":"x"}]', $2 FROM generate_series(1, 3) g`, org, old)
	count := func(q string) int {
		var n int
		if err := owner.QueryRow(ctx, q, org).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	var passes []int
	for range 5 {
		n, err := RetainUpTo(ctx, app, now, 2)
		if err != nil {
			t.Fatal(err)
		}
		passes = append(passes, n)
		if n == 0 {
			break
		}
	}
	// Deliveries 2+2+1, payloads 2+1: four passes, the last changing nothing.
	if !slices.Equal(passes, []int{4, 3, 1, 0}) {
		t.Errorf("rows per pass %v", passes)
	}
	if d, p := count(`SELECT count(*) FROM webhook_deliveries WHERE organization_id = $1`),
		count(`SELECT count(*) FROM automation_runs WHERE organization_id = $1 AND payloads_purged_at IS NULL`); d != 0 || p != 0 {
		t.Errorf("left: %d deliveries, %d runs with payloads", d, p)
	}
}

func TestRetainKeepsPayloadsThirtyDaysAndLogsNinety(t *testing.T) {
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	ctx := context.Background()
	now := time.Now()
	exec(t, owner, `INSERT INTO webhook_deliveries (id, organization_id, event, payload, received_at, processed_at, automations_at) VALUES
		('old', $1, 'push', '{}', $2, $2, $2), ('new', $1, 'push', '{}', $3, $3, $3), ('unread', $1, 'push', '{}', $2, NULL, NULL)`,
		org, now.Add(-31*24*time.Hour), now.Add(-29*24*time.Hour))
	exec(t, owner, `INSERT INTO automations (id, organization_id, name, kind, trigger, command)
		VALUES ('aut_r', $1, 'R', 'command', '{"kind":"schedule","cron":"0 * * * *","tz":"UTC"}', 'true')`, org)
	exec(t, owner, `INSERT INTO automation_runs (id, organization_id, automation_id, status, source, payloads, log_tail, created_at) VALUES
		('arn_91', $1, 'aut_r', 'succeeded', 'event', '[{"deliveryId":"x"}]', 'log', $2),
		('arn_31', $1, 'aut_r', 'succeeded', 'event', '[{"deliveryId":"y"}]', 'log', $3),
		('arn_1', $1, 'aut_r', 'succeeded', 'event', '[{"deliveryId":"z"}]', 'log', $4)`,
		org, now.Add(-91*24*time.Hour), now.Add(-31*24*time.Hour), now.Add(-24*time.Hour))
	if _, err := Retain(ctx, app, now); err != nil {
		t.Fatal(err)
	}
	var ids []string
	rows, _ := owner.Query(ctx, `SELECT id FROM webhook_deliveries WHERE organization_id = $1 ORDER BY id`, org)
	ids, _ = pgx.CollectRows(rows, pgx.RowTo[string])
	if !slices.Equal(ids, []string{"new", "unread"}) {
		t.Errorf("deliveries kept: %v (an unread one waits for its reader)", ids)
	}
	type kept struct {
		ID       string
		Payloads int
		Log      *string
	}
	rows, _ = owner.Query(ctx, `SELECT id, jsonb_array_length(payloads), log_tail FROM automation_runs ORDER BY created_at`)
	got, err := pgx.CollectRows(rows, pgx.RowToStructByPos[kept])
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Payloads != 0 || got[0].Log != nil || got[1].Payloads != 0 || got[1].Log == nil || got[2].Payloads != 1 || got[2].Log == nil {
		t.Errorf("runs after retention: %+v", got)
	}
}
