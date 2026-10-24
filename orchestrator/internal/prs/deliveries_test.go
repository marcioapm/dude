package prs

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

type noForges struct{}

func (noForges) For(context.Context, string) (*forge.GitHub, error) { return nil, nil }

// Every event is stored now (automations trigger on any). The pull request
// sync marks the ones that cannot change a pull request done without work,
// and still picks up a pull request's events behind a backlog of others.
func TestProcessDeliveriesMarksEventsThatAreNotAboutPullRequestsDone(t *testing.T) {
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	ctx := context.Background()
	// More pushes than one batch holds, received before the pull request's.
	for i := range 30 {
		if _, err := owner.Exec(ctx, `INSERT INTO webhook_deliveries (id, organization_id, event, payload, received_at)
			VALUES ($1, $2, 'push', '{"ref":"refs/heads/main"}', now() - interval '1 minute' + make_interval(secs => $3))`,
			"push-"+string(rune('a'+i)), org, i); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := owner.Exec(ctx, `INSERT INTO webhook_deliveries (id, organization_id, event, payload) VALUES
		('pr-1', $1, 'pull_request', '{"action":"opened","pull_request":{"number":9},"repository":{"full_name":"acme/web"}}'),
		('issue-1', $1, 'issues', '{"action":"opened"}')`, org); err != nil {
		t.Fatal(err)
	}
	s := &Syncer{DB: app, Forges: noForges{}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	n, err := s.ProcessDeliveries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("synced %d deliveries, want the pull request's alone", n)
	}
	rows, err := owner.Query(ctx, `SELECT event, count(*) FILTER (WHERE processed_at IS NULL) FROM webhook_deliveries
		WHERE organization_id = $1 GROUP BY event ORDER BY event`, org)
	if err != nil {
		t.Fatal(err)
	}
	type left struct {
		Event string
		Open  int
	}
	got, err := pgx.CollectRows(rows, pgx.RowToStructByPos[left])
	if err != nil {
		t.Fatal(err)
	}
	want := []left{{"issues", 0}, {"pull_request", 0}, {"push", 0}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("unprocessed per event: %v, want %v", got, want)
	}
	// Stored and marked done, not deleted: automations read them.
	var kept int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM webhook_deliveries WHERE organization_id = $1`, org).Scan(&kept); err != nil {
		t.Fatal(err)
	}
	if kept != 32 {
		t.Errorf("%d deliveries kept, want 32", kept)
	}
}
