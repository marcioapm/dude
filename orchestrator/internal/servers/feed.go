package servers

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
	"github.com/marciomartins/dude/orchestrator/internal/phases"
)

// Feed follows lux's tenant event feed (GET /v1/events) for the events of
// wakeable previews' servers. Every orchestrator runs one: each event is
// applied in a transaction keyed on durable columns, so two followers, or a
// replay after a restart, apply it once.
//
// The cursor (lux_feed) is a settled watermark, not the last id received:
// lux takes an event's id when it is written, not when its transaction
// commits, so a lower id can become visible after a higher one, and
// Last-Event-ID resumes strictly after the id given. lux's own feed moves
// its watermark only past events whose time (when their transaction began)
// is older than its feedSettle (10s); dude stores the highest id received
// whose time is older than Settle (15s, a margin for the two clocks), so a
// restart replays at most that much (applied once) and misses nothing an id
// still to commit would carry.
type Feed struct {
	DB   *db.DB
	Lux  lux.Servers
	Log  *slog.Logger
	Kick func()
	// How old an event's time must be before nothing below its id can
	// still appear; zero is 15s.
	Settle time.Duration
	// Waits before reconnecting; zero is 2s.
	Retry time.Duration
	// The cursor is written at most this often; zero is 1s.
	CursorEvery time.Duration

	mu sync.Mutex
	// Events received and not yet settled, by their lux time.
	young []seen
	// The highest settled id, and the one last written and when.
	settled, written int64
	writtenAt        time.Time
}

type seen struct {
	id int64
	at time.Time
}

const feedKey = "default"

func (f *Feed) settle() time.Duration {
	if f.Settle > 0 {
		return f.Settle
	}
	return 15 * time.Second
}

func (f *Feed) cursorEvery() time.Duration {
	if f.CursorEvery > 0 {
		return f.CursorEvery
	}
	return time.Second
}

// Run follows the feed until ctx ends, reconnecting after each failure.
func (f *Feed) Run(ctx context.Context) {
	for ctx.Err() == nil {
		err := f.Once(ctx)
		if ctx.Err() != nil {
			return
		}
		f.Log.Warn("lux's event feed: reconnecting", "error", err)
		retry := f.Retry
		if retry <= 0 {
			retry = 2 * time.Second
		}
		select {
		case <-ctx.Done():
		case <-time.After(retry):
		}
	}
}

// Once follows the feed from the stored cursor until it ends. A first
// follower (no cursor) starts at lux's latest event, so the cursor is
// stored from the first connection on. While it follows, the cursor is
// also settled on a ticker, so a quiet feed's last events are stored.
func (f *Feed) Once(ctx context.Context) error {
	after, err := f.cursor(ctx)
	if err != nil {
		return err
	}
	f.mu.Lock()
	f.young, f.settled, f.written, f.writtenAt = nil, after, after, time.Time{}
	f.mu.Unlock()
	tickCtx, stop := context.WithCancel(ctx)
	var ticking sync.WaitGroup
	ticking.Add(1)
	defer func() { stop(); ticking.Wait() }()
	go func() {
		defer ticking.Done()
		tick := time.NewTicker(f.cursorEvery())
		defer tick.Stop()
		for {
			select {
			case <-tickCtx.Done():
				return
			case <-tick.C:
				if err := f.store(tickCtx); err != nil && tickCtx.Err() == nil {
					f.Log.Warn("storing lux's feed cursor", "error", err)
				}
			}
		}
	}()
	return f.Lux.Feed(ctx, after, func(e lux.FeedEvent) error {
		if err := f.Apply(ctx, e); err != nil {
			return err
		}
		at := e.Time
		if at.IsZero() {
			at = time.Now()
		}
		f.mu.Lock()
		f.young = append(f.young, seen{e.ID, at})
		f.mu.Unlock()
		return f.store(ctx)
	})
}

func (f *Feed) cursor(ctx context.Context) (int64, error) {
	after := int64(-1)
	err := f.DB.InSystem(ctx, "lux-feed", func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT after_event_id FROM lux_feed WHERE id = $1`, feedKey).Scan(&after)
		if db.IsNotFound(err) {
			return nil
		}
		return err
	})
	return after, err
}

// store moves the stored cursor to the highest settled id: only forward,
// and at most once per CursorEvery. Held under f.mu throughout, so the
// ticker's and an event's writes do not race, and a write that failed is
// tried again.
func (f *Feed) store(ctx context.Context) error {
	now := time.Now()
	f.mu.Lock()
	defer f.mu.Unlock()
	keep := f.young[:0]
	for _, s := range f.young {
		if now.Sub(s.at) >= f.settle() {
			f.settled = max(f.settled, s.id)
		} else {
			keep = append(keep, s)
		}
	}
	f.young = keep
	mark := f.settled
	if mark <= f.written || now.Sub(f.writtenAt) < f.cursorEvery() {
		return nil
	}
	err := f.DB.InSystem(ctx, "lux-feed", func(tx pgx.Tx) error {
		// Forward only: two followers each move it to what they have
		// settled; one behind leaves the row, and its lock, alone.
		_, err := tx.Exec(ctx, `INSERT INTO lux_feed (id, after_event_id) VALUES ($1, $2)
			ON CONFLICT (id) DO UPDATE SET after_event_id = EXCLUDED.after_event_id, updated_at = now()
			WHERE lux_feed.after_event_id < EXCLUDED.after_event_id`,
			feedKey, mark)
		return err
	})
	if err == nil {
		f.written, f.writtenAt = mark, now
	}
	return err
}

// Apply applies one feed event to the wakeable preview whose server it is
// about; others (a server dude does not own, a Run's events) change
// nothing.
func (f *Feed) Apply(ctx context.Context, e lux.FeedEvent) error {
	if e.ServerID == "" {
		return nil
	}
	var org, runID, projectID, taskID string
	err := f.DB.InSystem(ctx, "lux-feed", func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT s.organization_id, s.run_id, r.project_id, r.task_id
			FROM preview_servers s JOIN runs r ON r.id = s.run_id WHERE s.lux_server_id = $1`, e.ServerID).
			Scan(&org, &runID, &projectID, &taskID)
	})
	if db.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	kick := false
	err = f.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		switch e.Type {
		case "server.wake_requested":
			// One wake per event, however often it is seen: wake_event_id
			// only moves forward, and only a live preview wakes.
			tag, err := tx.Exec(ctx, `UPDATE runs r SET wake_event_id = $2, wake_wanted_at = COALESCE(wake_wanted_at, now())
				WHERE r.id = $1 AND r.wake_event_id < $2 AND `+livePreview, runID, e.ID)
			if err != nil || tag.RowsAffected() == 0 {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE preview_servers SET last_woken_at = now(), idle_at = NULL, idle_last_request_at = NULL
				WHERE lux_server_id = $1`, e.ServerID); err != nil {
				return err
			}
			kick = true
			return phases.ServersChanged(ctx, tx, org, projectID, taskID, runID, map[string]any{"change": "waking", "server": e.Data["name"]})
		case "server.idle":
			last := timeOf(e.Data["lastRequestAt"])
			tag, err := tx.Exec(ctx, `UPDATE preview_servers SET idle_event_id = $2, idle_at = now(), idle_last_request_at = $3
				WHERE lux_server_id = $1 AND idle_event_id < $2 AND deleted_at IS NULL`, e.ServerID, e.ID, last)
			kick = err == nil && tag.RowsAffected() > 0
			return err
		case "server.state":
			// Its Run's stream says it to the browser (followEvents). Ready
			// again is a new idle period.
			if e.Data["state"] != lux.ServerReady {
				return nil
			}
			if _, err := tx.Exec(ctx, `UPDATE preview_servers SET idle_at = NULL, idle_last_request_at = NULL
				WHERE lux_server_id = $1 AND idle_event_id < $2`, e.ServerID, e.ID); err != nil {
				return err
			}
			return nil
		case "server.deleted", "server.expired":
			// Gone in lux, by dude's own delete or lux's expiry: a preview
			// still live ends with it (its Run cancelled by the sweep).
			tag, err := tx.Exec(ctx, `UPDATE preview_servers SET deleted_at = COALESCE(deleted_at, now())
				WHERE lux_server_id = $1 AND deleted_at IS NULL`, e.ServerID)
			if err != nil || tag.RowsAffected() == 0 {
				return err
			}
			why := map[string]string{"server.deleted": "deleted", "server.expired": "expired"}[e.Type]
			tag, err = tx.Exec(ctx, `UPDATE runs r SET status = 'completed', ended_at = now(), wake_wanted_at = NULL,
				error = 'its preview server was ' || $2 || ' in lux'
				WHERE r.id = $1 AND `+livePreview, runID, why)
			if err != nil || tag.RowsAffected() == 0 {
				return err
			}
			if _, err := ledger.Append(ctx, tx, ledger.Event{Type: "run.completed", OrganizationID: org, ProjectID: projectID,
				TaskID: taskID, RunID: runID, ActorType: ledger.ActorSystem, ActorID: runID, Source: ledger.SourceOrchestrator,
				CorrelationID: taskID, Payload: map[string]any{"status": "completed", "kind": KindPreview, "reason": why}}); err != nil {
				return err
			}
			kick = true
			return phases.ServersChanged(ctx, tx, org, projectID, taskID, runID, map[string]any{"change": "stopped"})
		}
		return nil
	})
	if err == nil && kick && f.Kick != nil {
		f.Kick()
	}
	return err
}

func timeOf(v any) *time.Time {
	s, _ := v.(string)
	if s == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return nil
	}
	return &t
}
