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

// The connection carrying the sweep's probe breaks, either before the
// probe's first answer arrives or just after it: the pass still records the
// failed Run's reason to wake and the stalled Run's report, as if every
// probe had found work.
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

// faultOnFirstBatch returns a pool on app's database whose first pgx.Batch
// breaks its connection as mode says. Every other statement, and every
// connection opened afterwards, is healthy.
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

// faultConn passes a connection through until armed. failOnSend fails the
// next write; failAfterFirstResult delivers the server's bytes up to and
// including the next CommandComplete ('C'), then fails every read. Server
// messages are framed (type byte, int32 length including itself) from the
// first byte so the cut lands on a message boundary.
type faultConn struct {
	net.Conn
	fired *atomic.Bool

	mu     sync.Mutex
	mode   faultMode
	broken bool
	hdr    []byte
	typ    byte
	body   int
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
		if c.body > 0 {
			k := min(c.body, n-i)
			c.body -= k
			i += k
		} else {
			c.hdr = append(c.hdr, p[i])
			i++
			if len(c.hdr) == 5 {
				c.typ, c.body = c.hdr[0], int(binary.BigEndian.Uint32(c.hdr[1:]))-4
				c.hdr = c.hdr[:0]
			}
		}
		if c.body == 0 && len(c.hdr) == 0 && c.typ == 'C' && c.mode == failAfterFirstResult {
			c.breakLocked()
			return i, nil
		}
	}
	return n, err
}
