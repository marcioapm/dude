package orchestrator_test

// A replacement for a removed secret, decided by a sweep that then stalls
// while another moves the preview on: the stale sweep must not cancel,
// retire or resubmit the Run the other has resumed since.

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/servers"
)

// secretsReadGate holds a connection once it has read a resume's secret
// values (the statement filtering by the declared names), until release
// is closed: the boundary between deciding a replacement and acting on it.
type secretsReadGate struct {
	once             sync.Once
	reached, release chan struct{}
}

func (g *secretsReadGate) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.Contains(data.SQL, "FROM project_secrets") && strings.Contains(data.SQL, "ANY(") {
		return context.WithValue(ctx, secretsReadGate{}, true)
	}
	return ctx
}

func (g *secretsReadGate) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryEndData) {
	if ctx.Value(secretsReadGate{}) != nil {
		g.once.Do(func() {
			close(g.reached)
			<-g.release
		})
	}
}

// staleSweeper is a second orchestrator whose resume stalls after reading
// the secrets: it is started on its own, and the test lets it go on.
func (w *world) staleSweeper() (gate *secretsReadGate, sweep func() <-chan error) {
	w.t.Helper()
	gate = &secretsReadGate{reached: make(chan struct{}), release: make(chan struct{})}
	cfg := w.app.Pool.Config()
	cfg.ConnConfig.Tracer = gate
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		w.t.Fatal(err)
	}
	stale := &servers.Previews{Service: &servers.Service{DB: &db.DB{Pool: pool}, Lux: w.previews.Lux, Log: quiet,
		PreviewDomain: previewDomain}, Forges: w.previews.Forges, DefaultImage: "default:img", Minute: time.Hour}
	returned := make(chan struct{})
	w.t.Cleanup(func() {
		if !isClosed(gate.release) {
			close(gate.release)
		}
		select {
		case <-returned:
		case <-time.After(30 * time.Second):
			w.t.Error("the stale sweep did not return")
		}
		stale.Stop()
		pool.Close()
	})
	return gate, func() <-chan error {
		swept := make(chan error, 1)
		go func() {
			defer close(returned)
			_, err := stale.Sweep(context.Background())
			swept <- err
		}()
		return swept
	}
}

// A wake whose resume read a declared secret as removed stalls past its
// claim; the secret is added again and another orchestrator takes the wake
// over and resumes the Run. The stale one then neither cancels that Run
// nor submits another.
func TestAStaleWakeDoesNotReplaceTheRunAnotherWoke(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.secret("ORIGINAL_KEY", seedKey)
	runID, web := w.asleepPreview()
	old := w.luxRuns()[0]
	w.removeSecret("ORIGINAL_KEY")
	mustExec(t, w.owner, `UPDATE runs SET wake_wanted_at = clock_timestamp() WHERE id = $1`, runID)
	gate, sweep := w.staleSweeper()
	swept := sweep()
	wait(t, gate.reached, "the stale wake to read the secrets")

	w.secret("ORIGINAL_KEY", seedKeyNew)
	mustExec(t, w.owner, `UPDATE runs SET wake_claimed_at = now() - interval '3 minutes' WHERE id = $1`, runID)
	if _, err := w.previews.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the wake taken over to serve", func() bool { return w.lux.RequestServer(web, "/") })
	close(gate.release)
	if err := <-swept; err != nil {
		t.Fatal(err)
	}
	if slices.Contains(w.lux.CallsOf(old.ID), "cancel") || len(w.luxRuns()) != 1 || old.Resumed != 1 {
		t.Errorf("the stale wake replaced the woken Run: calls %v, %d lux runs, resumed %d\n%s",
			w.lux.CallsOf(old.ID), len(w.luxRuns()), old.Resumed, w.preview(runID))
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND lux_run_id = $2 AND preview_secrets = '{ORIGINAL_KEY}'`, runID, old.ID); n != 1 {
		t.Errorf("the preview no longer holds its woken Run:\n%s", w.preview(runID))
	}
}

// The eager path: a sweep about to replace a parked preview whose secret
// was removed stalls; the secret is added again and another sweep resumes
// the Run. The stale one then neither cancels it nor puts the preview back
// to pending.
func TestAStaleSweepDoesNotReplaceAParkedPreviewAnotherResumed(t *testing.T) {
	w := newWorld(t)
	w.previews.Minute = time.Hour
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	w.secret("ORIGINAL_KEY", seedKey)
	_, out := w.do("POST", "/internal/tasks/"+w.task()+"/preview", nil)
	runID := out["run"].(map[string]any)["id"].(string)
	w.until("web ready", func() bool {
		return len(w.luxRuns()) == 1 && w.lux.ServerStates(w.luxRuns()[0].ID)["web"] == "ready"
	})
	old := w.luxRuns()[0]
	mustExec(t, w.owner, `UPDATE runs SET active_since = now() - interval '1 day' WHERE id = $1`, runID)
	w.previews.Minute = time.Millisecond
	w.until("parked", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND lux_state = 'stopped'`, runID) == 1
	})
	w.previews.Minute = time.Hour
	w.removeSecret("ORIGINAL_KEY")
	mustExec(t, w.owner, `UPDATE runs SET pending_starts = ARRAY['web'] WHERE id = $1`, runID)
	gate, sweep := w.staleSweeper()
	swept := sweep()
	wait(t, gate.reached, "the stale sweep to read the secrets")

	w.secret("ORIGINAL_KEY", seedKeyNew)
	w.until("resumed by the other sweep", func() bool {
		return old.Resumed == 1 && w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running'`, runID) == 1
	})
	close(gate.release)
	if err := <-swept; err != nil {
		t.Fatal(err)
	}
	if slices.Contains(w.lux.CallsOf(old.ID), "cancel") || len(w.luxRuns()) != 1 {
		t.Errorf("the stale sweep replaced the resumed Run: calls %v, %d lux runs\n%s",
			w.lux.CallsOf(old.ID), len(w.luxRuns()), w.preview(runID))
	}
	w.until("web ready on the resumed Run", func() bool { return w.lux.ServerStates(old.ID)["web"] == "ready" })
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running' AND lux_run_id = $2`, runID, old.ID); n != 1 {
		t.Errorf("the preview no longer runs on its resumed Run:\n%s", w.preview(runID))
	}
}
