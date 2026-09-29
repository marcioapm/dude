package servers

import (
	"context"
	"io"
	"log/slog"
	"slices"
	"sync"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// cancelLux is lux as far as ending previews goes: it records what is
// cancelled. Anything else the sweep asks of it is a test failure (the
// embedded nil Client panics).
type cancelLux struct {
	lux.Client
	mu        sync.Mutex
	cancelled []string
}

func (c *cancelLux) Cancel(_ context.Context, runID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cancelled = append(c.cancelled, runID)
	return nil
}

func (c *cancelLux) Cancelled() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Sorted(slices.Values(c.cancelled))
}

// A preview ends with its task — live or parked — as a person's DELETE
// would end it; and a lost one is cancelled too, since lux keeps a lost
// Run to resume.
func TestAPreviewEndsWithItsTaskAndALostOneIsCancelled(t *testing.T) {
	ctx := context.Background()
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := owner.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_'||$1, $1, 'P', 'prj_'||$1, 'P')`, org)
	task := func(n int, status string) string {
		id := "wi_" + org + "_" + status
		exec(`INSERT INTO tasks (id, organization_id, project_id, number, title, goal, status) VALUES ($1, $2, 'prj_'||$2, $3, 'T', 'G', $4::text::task_status)`,
			id, org, n, status)
		return id
	}
	preview := func(id, taskID, status, luxState string) {
		exec(`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, kind, status, lux_run_id, lux_state, lux_stop_reason, dude_pause)
			VALUES ($1, $2, 'prj_'||$2, $3, 1, 'preview', $4::text::run_status, 'lux_'||$1, $5,
			        CASE WHEN $4::text = 'paused' THEN 'pause' END, CASE WHEN $4::text = 'paused' THEN 'unused' END)`,
			id, org, taskID, status, luxState)
	}
	preview("run_done", task(1, "done"), "running", "running")
	preview("run_aborted", task(2, "aborted"), "paused", "stopped")
	preview("run_lost", task(3, "review"), "completed", "lost")
	preview("run_live", task(4, "running"), "paused", "stopped")

	fake := &cancelLux{}
	p := &Previews{Service: &Service{DB: app, Lux: fake, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}}
	if _, err := p.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if got, want := fake.Cancelled(), []string{"lux_run_aborted", "lux_run_done", "lux_run_lost"}; !slices.Equal(got, want) {
		t.Errorf("cancelled %v, want %v", got, want)
	}
	for id, want := range map[string]string{"run_done": "completed/cancel", "run_aborted": "completed/cancel",
		"run_lost": "completed/cancel", "run_live": "paused/pause"} {
		var got string
		if err := owner.QueryRow(ctx, `SELECT status::text || '/' || COALESCE(lux_stop_reason, '') FROM runs WHERE id = $1`, id).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("%s is %s, want %s", id, got, want)
		}
	}
	var stopped int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM events WHERE event_type = 'servers.changed' AND payload->>'change' = 'stopped'`).Scan(&stopped); err != nil {
		t.Fatal(err)
	}
	if stopped != 2 {
		t.Errorf("%d servers.changed stopped, want 2 (one per preview its task ended)", stopped)
	}
	// Done with: a second sweep asks nothing more.
	if _, err := p.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(fake.Cancelled()); n != 3 {
		t.Errorf("a second sweep cancelled again: %v", fake.Cancelled())
	}
}

// readLux answers the reads a task's servers take: no servers, the Run as
// running.
type readLux struct{ lux.Client }

func (readLux) Get(_ context.Context, id string) (lux.Run, error) {
	return lux.Run{ID: id, State: "running"}, nil
}
func (readLux) Servers(context.Context, string) ([]lux.Server, error) { return nil, nil }

// A live preview behind a live agent run is still said, so it can be
// stopped; with the preview shown, it is not said twice.
func TestAPreviewBehindAnAgentRunIsStillSaid(t *testing.T) {
	ctx := context.Background()
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	for _, q := range []string{
		`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_'||$1, $1, 'P', 'prj_'||$1, 'P')`,
		`INSERT INTO tasks (id, organization_id, project_id, number, title, goal) VALUES ('wi_'||$1, $1, 'prj_'||$1, 1, 'T', 'G')`,
		`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, kind, status, lux_run_id)
			VALUES ('run_p'||$1, $1, 'prj_'||$1, 'wi_'||$1, 1, 'preview', 'paused', 'lux_p')`,
	} {
		if _, err := owner.Exec(ctx, q, org); err != nil {
			t.Fatal(err)
		}
	}
	s := &Service{DB: app, Lux: readLux{}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	alone, err := s.ForTask(ctx, org, "wi_"+org)
	if err != nil {
		t.Fatal(err)
	}
	if alone.Run == nil || alone.Run.Kind != KindPreview || alone.Preview != nil {
		t.Fatalf("preview alone: run %+v, preview %+v", alone.Run, alone.Preview)
	}
	if _, err := owner.Exec(ctx, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, phase, status, lux_run_id)
		VALUES ('run_a'||$1, $1, 'prj_'||$1, 'wi_'||$1, 1, 'implement', 'running', 'lux_a')`, org); err != nil {
		t.Fatal(err)
	}
	behind, err := s.ForTask(ctx, org, "wi_"+org)
	if err != nil {
		t.Fatal(err)
	}
	if behind.Run == nil || behind.Run.Kind != "agent" {
		t.Fatalf("shown %+v, want the agent's run", behind.Run)
	}
	if p := behind.Preview; p == nil || p.ID != "run_p"+org || p.LuxRunID != "lux_p" || p.State != "paused" {
		t.Errorf("preview = %+v", p)
	}
}
