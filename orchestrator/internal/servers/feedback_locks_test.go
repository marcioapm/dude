package servers

import (
	"context"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

type feedbackLockLux struct{ lux.Client }

func (feedbackLockLux) ListServers(context.Context, string, ...string) ([]lux.TenantServer, error) {
	now := time.Now()
	return []lux.TenantServer{{ID: "srv_feedback", State: lux.SrvReady, LastRequestAt: &now}}, nil
}

func TestFeedbackCompetitorsLockRunBeforeServer(t *testing.T) {
	for _, path := range []string{"feed ready", "feed deleted", "non-idle park"} {
		t.Run(path, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			app, owner := dbtest.Open(t)
			org := dbtest.Org(t, owner)
			exec := func(q string, args ...any) {
				t.Helper()
				if _, err := owner.Exec(ctx, q, args...); err != nil {
					t.Fatal(err)
				}
			}
			exec(`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_feedback', $1, 'Acme', 'acme', 'ACME')`, org)
			exec(`INSERT INTO runs (id, organization_id, project_id, preview_branch, attempt, status, kind, wakeable)
				VALUES ('run_feedback', $1, 'prj_feedback', 'feature/feedback', 1, 'running', 'preview', true)`, org)
			exec(`INSERT INTO preview_servers (run_id, organization_id, name, lux_server_id, hostname, url, idle_at)
				VALUES ('run_feedback', $1, 'web', 'srv_feedback', 'feedback', 'https://feedback.example.test', now())`, org)
			exec(`INSERT INTO people (id, organization_id, name) VALUES ('per_feedback', $1, 'Ana Example')`, org)
			// This is the API's held Run SHARE, followed by its server SHARE.
			tx, err := owner.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(context.Background())
			if _, err := tx.Exec(ctx, `SELECT id FROM runs WHERE id = 'run_feedback' FOR SHARE`); err != nil {
				t.Fatal(err)
			}
			pid := owner.PgConn().PID()
			probe, err := pgx.Connect(ctx, owner.Config().ConnString())
			if err != nil {
				t.Fatal(err)
			}
			defer probe.Close(context.Background())
			result := make(chan error, 1)
			go func() {
				if path == "non-idle park" {
					p := &Previews{Service: &Service{DB: app, Lux: feedbackLockLux{}}}
					_, err := p.parkIfIdle(ctx, wakeRun{previewRun: previewRun{ID: "run_feedback", Org: org}})
					result <- err
				} else {
					typ := "server.state"
					if path == "feed deleted" {
						typ = "server.deleted"
					}
					result <- (&Feed{DB: app}).Apply(ctx, lux.FeedEvent{ID: 10, Type: typ, ServerID: "srv_feedback", Data: map[string]any{"state": lux.ServerReady}})
				}
			}()
			// Inspect PostgreSQL's actual wait, not a timing assumption. A server-first
			// mutant either waits at UPDATE runs (park) or SELECT runs after updating
			// the server (feed); the NOWAIT probe below catches both inversions.
			var query string
			for {
				err := probe.QueryRow(ctx, `SELECT query FROM pg_stat_activity WHERE $1::int = ANY(pg_blocking_pids(pid)) LIMIT 1`, pid).Scan(&query)
				if err == nil {
					break
				}
				if ctx.Err() != nil {
					t.Fatalf("no observed lock barrier: %v", err)
				}
				runtime.Gosched()
			}
			if !strings.Contains(query, "runs") {
				t.Fatalf("competitor blocked at wrong lock: %s", query)
			}
			if _, err := tx.Exec(ctx, `SELECT name FROM preview_servers WHERE run_id = 'run_feedback' FOR SHARE NOWAIT`); err != nil {
				t.Fatalf("competitor locked server before Run: %v", err)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO preview_feedback
				(id, organization_id, project_id, preview_run_id, preview_server_name, author_person_id, submission_key, body, anchor)
				VALUES ('fbi_feedback', $1, 'prj_feedback', 'run_feedback', 'web', 'per_feedback', 'example', 'Example feedback',
				'{"kind":"preview","url":"https://feedback.example.test"}')`, org); err != nil {
				t.Fatalf("held feedback submission failed: %v", err)
			}
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if err := <-result; err != nil {
				t.Fatalf("real competitor failed: %v", err)
			}
			var count int
			if err := owner.QueryRow(ctx, `SELECT count(*) FROM runs WHERE id = 'run_feedback' AND status = 'running'`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if path == "feed deleted" && count != 0 {
				t.Fatal("deleted preview resurrected")
			}
			if path != "feed deleted" && count != 1 {
				t.Fatal("live preview ended")
			}
			if err := owner.QueryRow(ctx, `SELECT count(*) FROM preview_feedback WHERE preview_run_id = 'run_feedback'`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			want := 1
			if path == "feed deleted" {
				want = 0
			}
			if count != want {
				t.Fatalf("feedback count %d after competitor, want %d", count, want)
			}
		})
	}
}

func TestUnsupportedFeedEventsDoNotLockRun(t *testing.T) {
	for _, e := range []lux.FeedEvent{
		{Type: "server.unknown", ServerID: "srv_feedback"},
		{Type: "server.state", ServerID: "srv_feedback", Data: map[string]any{"state": "starting"}},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		app, owner := dbtest.Open(t)
		org := dbtest.Org(t, owner)
		for _, query := range []string{
			`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_feedback', $1, 'Acme', 'acme', 'ACME')`,
			`INSERT INTO runs (id, organization_id, project_id, preview_branch, attempt, status, kind, wakeable)
			 VALUES ('run_feedback', $1, 'prj_feedback', 'feature/feedback', 1, 'running', 'preview', true)`,
			`INSERT INTO preview_servers (run_id, organization_id, name, lux_server_id, hostname, url)
			 VALUES ('run_feedback', $1, 'web', 'srv_feedback', 'feedback', 'https://feedback.example.test')`,
		} {
			if _, err := owner.Exec(ctx, query, org); err != nil {
				t.Fatal(err)
			}
		}
		tx, err := owner.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `SELECT id FROM runs WHERE id = 'run_feedback' FOR UPDATE`); err != nil {
			t.Fatal(err)
		}
		// A real owned server, with its Run locked: irrelevant events must finish
		// before that lock is released, not merely return for an unknown server.
		if err := (&Feed{DB: app}).Apply(ctx, e); err != nil {
			t.Fatal(err)
		}
		if err := tx.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
		cancel()
	}
}
