package phases

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// secretLookups counts the statements that read a Run's secret values.
type secretLookups struct{ n atomic.Int64 }

func (c *secretLookups) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	if strings.Contains(d.SQL, "run_secret_values") {
		c.n.Add(1)
	}
	return ctx
}

func (*secretLookups) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// A batch of a Run's frames, each writing an event, reads the Run's secret
// values once for the batch; for a Run given none, never. Its events are
// still redacted with them.
func TestABatchReadsItsRunsSecretsOnceAndARunWithNoneNever(t *testing.T) {
	ctx := context.Background()
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	counter := &secretLookups{}
	cfg, err := pgxpool.ParseConfig(app.Pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.Tracer = counter
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	s := &Syncer{DB: &db.DB{Pool: pool}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	const value = "batch-test-not-a-real-token-1"
	for _, q := range []string{
		`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_'||$1, $1, 'P', 'prj_'||$1, 'P')`,
		`INSERT INTO tasks (id, organization_id, project_id, number, title, goal) VALUES ('wi_'||$1, $1, 'prj_'||$1, 1, 'T', 'G')`,
		`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, phase, status, lux_run_id, lux_state, project_secret_names)
			VALUES ('run_with_'||$1, $1, 'prj_'||$1, 'wi_'||$1, 1, 'implement', 'running', 'lrun_1', 'running', '{TOKEN}')`,
		`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, phase, status, lux_run_id, lux_state)
			VALUES ('run_none_'||$1, $1, 'prj_'||$1, 'wi_'||$1, 1, 'implement', 'running', 'lrun_2', 'running')`,
		`INSERT INTO run_project_secrets (run_id, organization_id, name, value) VALUES ('run_with_'||$1, $1, 'TOKEN', '` + value + `')`,
	} {
		if _, err := owner.Exec(ctx, q, org); err != nil {
			t.Fatal(err)
		}
	}
	const frames = 10
	for _, c := range []struct {
		run  string
		want int64
	}{{"run_none_" + org, 0}, {"run_with_" + org, 1}} {
		r := phaseRun{ID: c.run, Org: org, ProjectID: "prj_" + org, TaskID: "wi_" + org, Phase: "implement", Status: statusRunning}
		tr := &translator{run: r}
		if err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error { return tr.load(ctx, tx) }); err != nil {
			t.Fatal(err)
		}
		var batch []lux.Frame
		for i := range frames {
			batch = append(batch, record(1, "lux.warning", map[string]any{"message": fmt.Sprintf("warning %d: %s", i, value)}))
		}
		before := counter.n.Load()
		if err := s.applyBatch(ctx, tr, batch); err != nil {
			t.Fatal(err)
		}
		if got := counter.n.Load() - before; got != c.want {
			t.Errorf("%s: %d secret lookups for a batch of %d frames, want %d", c.run, got, frames, c.want)
		}
	}
	var leaked, marked int
	_ = owner.QueryRow(ctx, `SELECT count(*) FILTER (WHERE payload::text LIKE '%'||$2||'%'),
		count(*) FILTER (WHERE payload->>'message' LIKE '%[REDACTED:TOKEN]')
		FROM events WHERE run_id = $1 AND event_type = $3`, "run_with_"+org, value, evAgentWarning).Scan(&leaked, &marked)
	if leaked != 0 || marked != frames {
		t.Errorf("the Run given TOKEN: %d events hold it, %d of %d show the marker", leaked, marked, frames)
	}
}
