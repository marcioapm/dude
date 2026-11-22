package delivery

import (
	"cmp"
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
)

// Observations: a wait's conditions on something outside dude — a pull
// request dude does not track (prEnded), a URL (url) — are read by the
// orchestrator's own observation loop (ObserveWaits), never by the
// evaluation nor the phase sweep. Each due one is selected
// (oldest reading first, waitObserveBatch a pass, read at once), read with no
// transaction open, and its reading stored on its condition
// (conditions[i].obs) in one short transaction of its organisation, which
// writes no wait that ended meanwhile. EvaluateWaitTx reads the stored
// reading, so it stays a read of the database alone.

// How often a condition is read, and how many a pass reads.
var (
	prObserveEvery  = 5 * time.Minute
	urlObserveEvery = 2 * time.Minute
)

const waitObserveBatch = 20

// Why an observation holds no reading (waitObs.Error).
const (
	obsBudget  = "budget"
	obsRefused = "refused address"
)

// WaitObservation is one condition of an open wait due a reading: the
// wait, its organisation and task, and the condition's index and value.
type WaitObservation struct {
	WaitID, Org, TaskID, ProjectID, Conductor string
	Index                                     int
	cond                                      waitCond
}

// DueWaitObservationsTx selects the conditions due a reading across
// organisations (tx is the sweeper's): never read, or a pull request's
// read prObserveEvery ago and a URL's urlObserveEvery ago; oldest reading
// first, at most waitObserveBatch.
func DueWaitObservationsTx(ctx context.Context, tx pgx.Tx) ([]WaitObservation, error) {
	rows, err := tx.Query(ctx, `SELECT w.id, w.organization_id, w.task_id, t.project_id, w.conductor_run_id, c.i - 1, c.cond
		FROM conductor_waits w JOIN tasks t ON t.id = w.task_id,
			jsonb_array_elements(w.conditions) WITH ORDINALITY AS c(cond, i)
		WHERE w.ended_at IS NULL
		  AND (c.cond->>'kind' = $1 OR c.cond->>'kind' = $2 AND COALESCE(c.cond->>'slug', '') <> '')
		  AND (c.cond->'obs' IS NULL OR (c.cond->'obs'->>'at')::timestamptz <= now() - CASE c.cond->>'kind'
			WHEN $1 THEN make_interval(secs => $3) ELSE make_interval(secs => $4) END)
		ORDER BY (c.cond->'obs'->>'at')::timestamptz NULLS FIRST, w.id, c.i
		LIMIT $5`, condURL, condPREnded, urlObserveEvery.Seconds(), prObserveEvery.Seconds(), waitObserveBatch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WaitObservation
	for rows.Next() {
		var o WaitObservation
		var raw []byte
		if err := rows.Scan(&o.WaitID, &o.Org, &o.TaskID, &o.ProjectID, &o.Conductor, &o.Index, &raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &o.cond); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// recordObservationTx stores a reading on the condition, at now() by
// Postgres's clock, in place (jsonb_set on its index), only while the wait
// is open and the condition is still the one read. Returns whether it
// wrote.
func recordObservationTx(ctx context.Context, tx pgx.Tx, o WaitObservation, obs waitObs) (bool, error) {
	raw, err := json.Marshal(obs)
	if err != nil {
		return false, err
	}
	tag, err := tx.Exec(ctx, `UPDATE conductor_waits
		SET conditions = jsonb_set(conditions, ARRAY[$2::text, 'obs'], $3::jsonb || jsonb_build_object('at', now()))
		WHERE id = $1 AND ended_at IS NULL AND conditions->$2::int->>'kind' = $4`,
		o.WaitID, o.Index, raw, o.cond.Kind)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// waitObservePass is how long one pass of ObserveWaits may take.
var waitObservePass = 20 * time.Second

// ObserveWaits is the observation loop's pass: it reads each condition due
// a reading (DueWaitObservationsTx), all of them at once (at most
// waitObserveBatch, each under its own timeout), and stores what it saw. A
// pull request's read spends the task's GitHub reads, counted before it is
// made; with them spent it is not made, and "budget" is stored.
//
// The pass has a deadline (waitObservePass). A read the pass cut short (its
// deadline, or ctx ended) stores nothing: the condition keeps its last
// reading's time, so it stays due, oldest first, and is never read as a
// timeout it was not. Only a read's own timeout or failure is stored as
// an error. Returns how many readings it stored.
func ObserveWaits(ctx context.Context, d *db.DB, forges Forges) (int, error) {
	pass, cancel := context.WithTimeout(ctx, waitObservePass)
	defer cancel()
	var due []WaitObservation
	if err := d.InSystem(pass, "wait-observations", func(tx pgx.Tx) error {
		var err error
		due, err = DueWaitObservationsTx(pass, tx)
		return err
	}); err != nil {
		return 0, err
	}
	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		stored   int
		firstErr error
	)
	for _, o := range due {
		wg.Add(1)
		go func() {
			defer wg.Done()
			wrote, err := observeOne(ctx, pass, d, forges, o)
			mu.Lock()
			defer mu.Unlock()
			if wrote {
				stored++
			}
			firstErr = cmp.Or(firstErr, err)
		}()
	}
	wg.Wait()
	return stored, firstErr
}

// observeOne reads o under the pass's context and, unless the pass cut the
// read short, stores the reading under ctx's.
func observeOne(ctx, pass context.Context, d *db.DB, forges Forges, o WaitObservation) (bool, error) {
	obs, ok, err := observe(pass, d, forges, o)
	if pass.Err() != nil {
		// Cut short by the pass, not by the read's own timeout: no reading.
		return false, nil
	}
	if err != nil || !ok {
		return false, err
	}
	wrote := false
	err = d.InOrg(ctx, o.Org, func(tx pgx.Tx) error {
		var err error
		wrote, err = recordObservationTx(ctx, tx, o, obs)
		return err
	})
	return wrote, err
}

// observe reads one condition, with no transaction open: false for a
// kind that is not read.
func observe(ctx context.Context, d *db.DB, forges Forges, o WaitObservation) (waitObs, bool, error) {
	switch o.cond.Kind {
	case condPREnded:
		read, err := reservePRReadTx(ctx, d, o)
		if err != nil {
			return waitObs{}, false, err
		}
		if !read {
			return waitObs{Error: obsBudget}, true, nil
		}
		return observePR(ctx, forges, o), true, nil
	case condURL:
		return observeURL(ctx, o.cond), true, nil
	}
	return waitObs{}, false, nil
}
