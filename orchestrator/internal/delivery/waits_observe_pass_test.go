package delivery

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// One pass of the observation loop (ObserveWaits): its due conditions read
// at once, under a deadline; a read the pass cut short stores nothing.

// openURLWaits opens n url conditions on u, at most WaitMaxConditions a
// wait, one wait a task, each never read; returns the waits' ids.
func (w *childWorld) openURLWaits(n int, u string, not bool) []string {
	w.t.Helper()
	var ids []string
	for i := 0; n > 0; i++ {
		k := min(n, WaitMaxConditions)
		n -= k
		conds := make([]map[string]any, 0, k)
		for range k {
			conds = append(conds, map[string]any{"kind": condURL, "url": u, "where": "here/", "status": "2xx", "not": not})
		}
		raw, _ := json.Marshal(conds)
		task := fmt.Sprintf("wi_pass%d", i)
		w.exec(`INSERT INTO tasks (id, organization_id, project_id, number, title, goal, status)
			VALUES ($1, $2, 'prj_life', $3, 'T', 'G', 'running')`, task, w.org, 50+i)
		w.exec(`INSERT INTO conductor_waits (id, organization_id, task_id, conductor_run_id, attempt, conditions, wake_all, deadline_at, since_cursor, call_key)
			VALUES ($1, $2, $3, 'run_parent', 3, $4::jsonb, false, now() + interval '1 hour', 0, $1)`, "cwt_"+task, w.org, task, raw)
		ids = append(ids, "cwt_"+task)
	}
	return ids
}

// 20 due URLs whose server never answers: one pass reads them all at once,
// so it takes about one read's timeout, not twenty; each stores its own
// timeout, once.
func TestAPassReadsItsDueConditionsAtOnce(t *testing.T) {
	timeout := urlTimeout
	urlTimeout = 300 * time.Millisecond
	t.Cleanup(func() { urlTimeout = timeout })
	w := newWaitWorld(t)
	s := newStatusServer(t, 200)
	s.sleep.Store(int64(time.Minute))
	ids := w.openURLWaits(waitObserveBatch, s.srv.URL+"/tarpit", false)
	start := time.Now()
	n, err := ObserveWaits(w.ctx, w.s.DB, nil)
	took := time.Since(start)
	t.Logf("one pass over %d hanging URLs took %s (urlTimeout %s)", waitObserveBatch, took, urlTimeout)
	if err != nil {
		t.Fatal(err)
	}
	if took >= time.Second {
		t.Fatalf("the pass took %s: its reads were not made at once", took)
	}
	if n != waitObserveBatch || s.hits.Load() != waitObserveBatch {
		t.Fatalf("%d stored, %d requests, want %d of each", n, s.hits.Load(), waitObserveBatch)
	}
	var timeouts, read int
	if err := w.owner.QueryRow(w.ctx, `SELECT count(*) FILTER (WHERE c->'obs'->>'error' = 'timeout'), count(*) FILTER (WHERE c ? 'obs')
		FROM conductor_waits, jsonb_array_elements(conditions) c WHERE id = ANY($1)`, ids).Scan(&timeouts, &read); err != nil {
		t.Fatal(err)
	}
	if timeouts != waitObserveBatch || read != waitObserveBatch {
		t.Fatalf("%d timeouts stored over %d readings, want %d", timeouts, read, waitObserveBatch)
	}
	// Each was written once: none is due again, so a second pass reads none.
	if n, err := ObserveWaits(w.ctx, w.s.DB, nil); err != nil || n != 0 || s.hits.Load() != waitObserveBatch {
		t.Fatalf("a second pass: %d stored, %d requests, %v", n, s.hits.Load(), err)
	}
}

// A read the pass cut short — its deadline, or the loop's context ended —
// stores nothing: the condition keeps its last reading and its time, so it
// stays due, and a not-2xx wait is not held by a "timeout" it never had.
func TestAReadThePassCutShortStoresNothing(t *testing.T) {
	for _, how := range []string{"deadline", "shutdown"} {
		t.Run(how, func(t *testing.T) {
			w := newWaitWorld(t)
			s := newStatusServer(t, 200)
			s.sleep.Store(int64(time.Minute))
			ids := w.openURLWaits(1, s.srv.URL+"/slow", true)
			// Read before, 200: due again (an old reading).
			w.exec(`UPDATE conductor_waits SET conditions = jsonb_set(conditions, '{0,obs}',
				jsonb_build_object('status', 200, 'at', now() - interval '10 minutes')) WHERE id = $1`, ids[0])
			before := w.obs(ids[0], 0)
			ctx := w.ctx
			switch how {
			case "deadline":
				pass := waitObservePass
				waitObservePass = 300 * time.Millisecond
				t.Cleanup(func() { waitObservePass = pass })
			case "shutdown":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(w.ctx)
				defer cancel()
				go func() {
					for s.hits.Load() == 0 {
						time.Sleep(5 * time.Millisecond)
					}
					cancel()
				}()
			}
			start := time.Now()
			n, err := ObserveWaits(ctx, w.s.DB, nil)
			if err != nil || n != 0 {
				t.Fatalf("%d stored, %v", n, err)
			}
			if took := time.Since(start); took > 5*time.Second {
				t.Fatalf("the pass took %s", took)
			}
			if s.hits.Load() != 1 {
				t.Fatalf("%d requests, want the one cut short", s.hits.Load())
			}
			if after := w.obs(ids[0], 0); after != before {
				t.Fatalf("a read cut short was stored: %s, was %s", after, before)
			}
			w.mustStillWait(ids[0], "200 is 2xx; the cut-short read is no reading")
		})
	}
}
