package phases

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/marciomartins/dude/orchestrator/internal/db"
)

// A broken probe connection must not prevent failed-Run wakes or stall
// reports, even when the probe has already received its first answer.
func TestASweepWhoseProbeBreaksStillTakesUpEveryStep(t *testing.T) {
	for _, c := range []struct {
		name string
		mode faultMode
	}{{"before any answer", failOnSend}, {"after the first answer", failAfterFirstResult}} {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			w := newWakeSweepWorld(t)
			org, exec, count := w.org, w.exec, w.count
			exec(`INSERT INTO tasks (id, organization_id, project_id, number, title, goal) VALUES
				('wi_f', $1, 'prj_'||$1, 1, 'T', 'G'), ('wi_s', $1, 'prj_'||$1, 2, 'T', 'G')`, org)
			exec(`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, role, kind, status, dude_pause, turn_done_at) VALUES
				('run_cond_f', $1, 'prj_'||$1, 'wi_f', 1, 'conductor', 'agent', 'paused', 'conductor', now()),
				('run_cond_s', $1, 'prj_'||$1, 'wi_s', 1, 'conductor', 'agent', 'paused', 'conductor', now())`, org)
			exec(`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, phase, status, conductor_run_id, error, ended_at)
				VALUES ('run_impl', $1, 'prj_'||$1, 'wi_f', 1, 'implement', 'failed', 'run_cond_f', 'the build broke', now())`, org)
			// Silent for an hour: past a conducted task's window (StallWindow).
			exec(`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, phase, status, lux_state, started_at)
				VALUES ('run_rev', $1, 'prj_'||$1, 'wi_s', 1, 'review', 'running', 'running', now() - interval '1 hour')`, org)

			f := faultOnFirstBatch(t, w.s.DB, c.mode)
			w.s.DB = f.db
			w.s.WakeWindow = time.Nanosecond
			if _, err := w.s.Sweep(ctx); err != nil {
				t.Fatal(err)
			}
			if !f.fired.Load() {
				t.Fatal("the injected fault never fired: the probe's batch was not sent on a faulted connection")
			}
			if n := count(`SELECT count(*) FROM conductor_wakes WHERE task_id = 'wi_f' AND key = 'failed:run_impl'`); n != 1 {
				t.Errorf("%d reasons recorded for the failed Run, want 1", n)
			}
			if n := count(`SELECT count(*) FROM conductor_wakes WHERE task_id = 'wi_s' AND kind = 'stalled'`); n != 1 {
				t.Errorf("%d stall reports recorded for the stalled Run, want 1", n)
			}
		})
	}
}

type faultMode int

const (
	failOnSend faultMode = iota + 1
	failAfterFirstResult
)

var errInjected = errors.New("injected connection fault")

type batchFault struct {
	db    *db.DB
	mode  faultMode
	armed atomic.Bool
	fired atomic.Bool
}

// Only the first batch is faulted; subsequent step transactions stay healthy.
func faultOnFirstBatch(t *testing.T, app *db.DB, mode faultMode) *batchFault {
	t.Helper()
	f := &batchFault{mode: mode}
	cfg := app.Pool.Config()
	// Plain TCP, so the wrapper sees protocol messages rather than TLS records.
	cfg.ConnConfig.TLSConfig, cfg.ConnConfig.Fallbacks = nil, nil
	dial := cfg.ConnConfig.DialFunc
	cfg.ConnConfig.DialFunc = func(ctx context.Context, network, addr string) (net.Conn, error) {
		c, err := dial(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		return &faultConn{Conn: c, fired: &f.fired}, nil
	}
	cfg.ConnConfig.Tracer = f
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	f.db = &db.DB{Pool: pool}
	return f
}

func (f *batchFault) TraceBatchStart(ctx context.Context, conn *pgx.Conn, _ pgx.TraceBatchStartData) context.Context {
	if fc, ok := conn.PgConn().Conn().(*faultConn); ok && f.armed.CompareAndSwap(false, true) {
		fc.arm(f.mode)
	}
	return ctx
}

func (*batchFault) TraceBatchQuery(context.Context, *pgx.Conn, pgx.TraceBatchQueryData) {}
func (*batchFault) TraceBatchEnd(context.Context, *pgx.Conn, pgx.TraceBatchEndData)     {}
func (*batchFault) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	return ctx
}
func (*batchFault) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// failAfterFirstResult cuts reads after CommandComplete ('C'), before pgx
// can buffer later results. Framing starts at connection startup and tracks
// the type byte and int32 length (including the length itself) across reads.
type faultConn struct {
	net.Conn
	fired *atomic.Bool

	mu            sync.Mutex
	mode          faultMode
	broken        bool
	header        []byte
	messageType   byte
	bodyRemaining int
}

func (c *faultConn) arm(m faultMode) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.mode = m
}

func (c *faultConn) breakLocked() {
	c.broken = true
	c.fired.Store(true)
	c.Conn.Close()
}

func (c *faultConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.broken {
		return 0, errInjected
	}
	if c.mode == failOnSend {
		c.breakLocked()
		return 0, errInjected
	}
	return c.Conn.Write(p)
}

func (c *faultConn) Read(p []byte) (int, error) {
	c.mu.Lock()
	if c.broken {
		c.mu.Unlock()
		return 0, errInjected
	}
	c.mu.Unlock()
	n, err := c.Conn.Read(p)
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := 0; i < n; {
		if c.bodyRemaining > 0 {
			k := min(c.bodyRemaining, n-i)
			c.bodyRemaining -= k
			i += k
		} else {
			c.header = append(c.header, p[i])
			i++
			if len(c.header) == 5 {
				c.messageType = c.header[0]
				c.bodyRemaining = int(binary.BigEndian.Uint32(c.header[1:])) - 4
				c.header = c.header[:0]
			}
		}
		if c.bodyRemaining == 0 && len(c.header) == 0 && c.messageType == 'C' && c.mode == failAfterFirstResult {
			c.breakLocked()
			return i, nil
		}
	}
	return n, err
}
