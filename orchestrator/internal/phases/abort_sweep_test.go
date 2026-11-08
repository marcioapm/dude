package phases

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

func TestAbortSweepSettlesLiveRunsOnce(t *testing.T) {
	for _, status := range []string{"pending", "scheduled", "starting", "running", "paused"} {
		for _, child := range []bool{false, true} {
			for _, code := range []int{204, 409} {
				t.Run(fmt.Sprintf("%s/child=%t/http=%d", status, child, code), func(t *testing.T) {
					w := newResumeWorld(t)
					w.exec(`UPDATE runs SET status=$2::run_status, control='abort', control_requested_at=now(),
						control_reason='checks gate stop', lux_state='running', lux_stop_reason=NULL,
						next_attempt_at=now()+interval '1 hour', keep=$3 WHERE id=$1`, w.run.ID, status, child)
					if child {
						w.exec(`UPDATE runs SET source_kind='task_conductor', parent_run_id=id,
							child_profile='investigator', child_capabilities='{}' WHERE id=$1`, w.run.ID)
					}
					var calls atomic.Int32
					server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
						if r.Method != "POST" || r.URL.Path != "/v1/runs/lrun_1/cancel" {
							t.Errorf("unexpected Lux request %s %s", r.Method, r.URL.Path)
						}
						calls.Add(1)
						rw.WriteHeader(code)
					}))
					defer server.Close()
					w.s.Lux = lux.New(server.URL, "test")
					for pass := range 12 {
						n, err := w.s.Sweep(w.ctx)
						want := 0
						if pass == 0 {
							want = 1
						}
						if err != nil || n != want {
							t.Errorf("pass %d: acted=%d err=%v, want %d", pass, n, err, want)
						}
					}
					if calls.Load() != 1 {
						t.Errorf("cancel POSTs=%d, want 1", calls.Load())
					}
					if n := w.count(`SELECT count(*) FROM runs WHERE id=$1 AND status='aborted' AND ended_at IS NOT NULL
						AND control='none' AND lux_stop_reason='cancel' AND NOT keep AND control_reason='checks gate stop'`); n != 1 {
						t.Error("abort did not durably settle")
					}
					if n := w.count(`SELECT count(*) FROM events WHERE run_id=$1 AND event_type='run.aborted'
						AND payload->>'reason'='checks gate stop'`); n != 1 {
						t.Errorf("abort ledger events=%d, want 1", n)
					}
				})
			}
		}
	}
}

func TestAbortSweepHonorsCleanupBackoff(t *testing.T) {
	for _, status := range []string{"running", "aborted"} {
		for _, failure := range []string{"503", "transport", "context"} {
			t.Run(status+"/"+failure, func(t *testing.T) {
				w := newResumeWorld(t)
				w.exec(`UPDATE runs SET status=$2::run_status, control='abort', control_requested_at=now(),
					lux_state='running', lux_stop_reason=NULL, next_attempt_at=now()+interval '1 hour',
					retry_due_at=now()+interval '1 hour', retry_failures=4 WHERE id=$1`, w.run.ID, status)
				var calls atomic.Int32
				var succeed atomic.Bool
				server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					if !succeed.Load() {
						if failure == "transport" {
							conn, _, err := rw.(http.Hijacker).Hijack()
							if err != nil {
								t.Error(err)
								return
							}
							_ = conn.Close()
							return
						}
						rw.WriteHeader(http.StatusServiceUnavailable)
						return
					}
					rw.WriteHeader(http.StatusNoContent)
				}))
				defer server.Close()
				w.s.Lux = lux.New(server.URL, "test")
				if failure == "context" {
					w.s.Lux = &abortContextLux{Client: w.s.Lux, fail: true}
				}
				if n, err := w.s.Sweep(w.ctx); err != nil || n != 0 {
					t.Fatalf("transient cleanup acted=%d err=%v", n, err)
				}
				if n := w.count(`SELECT count(*) FROM runs WHERE id=$1 AND status='aborted' AND control='none'
					AND retry_due_at=next_attempt_at AND next_attempt_at>now() AND lux_stop_reason IS NULL`); n != 1 {
					t.Fatal("cleanup did not schedule its backoff")
				}
				for range 12 {
					if n, err := w.s.Sweep(w.ctx); err != nil || n != 0 {
						t.Fatalf("backed-off sweep acted=%d err=%v", n, err)
					}
				}
				want := int32(1)
				if failure == "context" {
					want = 0
					w.s.Lux.(*abortContextLux).fail = false
				}
				if calls.Load() != want {
					t.Fatalf("POSTs during backoff=%d, want %d", calls.Load(), want)
				}
				succeed.Store(true)
				w.s.RetryAhead = time.Hour
				if n, err := w.s.Sweep(w.ctx); err != nil || n != 1 {
					t.Fatalf("due cleanup acted=%d err=%v", n, err)
				}
				if n, err := w.s.Sweep(w.ctx); err != nil || n != 0 {
					t.Fatalf("settled cleanup acted=%d err=%v", n, err)
				}
				if calls.Load() != want+1 || w.stopReason() != "cancel" {
					t.Fatalf("cleanup not settled: POSTs=%d reason=%q", calls.Load(), w.stopReason())
				}
			})
		}
	}
}

type abortContextLux struct {
	lux.Client
	fail bool
}

func (c *abortContextLux) Cancel(ctx context.Context, id string) error {
	if c.fail {
		return context.DeadlineExceeded
	}
	return c.Client.Cancel(ctx, id)
}

func TestAbortSweepPreservesKeepAndMissingLux(t *testing.T) {
	for _, state := range []string{"running", "stopped", "terminated", "missing"} {
		for _, keep := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/keep=%t", state, keep), func(t *testing.T) {
				w := newResumeWorld(t)
				w.exec(`UPDATE runs SET status='pending', control='abort', control_requested_at=now(),
					lux_state=$2, lux_stop_reason=NULL, keep=$3 WHERE id=$1`, w.run.ID, state, keep)
				if state == "missing" {
					w.exec(`UPDATE runs SET lux_run_id=NULL WHERE id=$1`, w.run.ID)
				}
				fake := w.onCallLux(nil)
				w.s.KeepFor = time.Hour
				for pass := range 12 {
					want := 0
					if pass == 0 {
						want = 1
					}
					if n, err := w.s.Sweep(w.ctx); err != nil || n != want {
						t.Fatalf("pass %d: acted=%d err=%v, want %d", pass, n, err, want)
					}
				}
				wantReason, wantCalls := "cancel", 1
				if keep && state != "terminated" && state != "missing" {
					wantReason = "kept"
				}
				if state == "terminated" || state == "missing" || keep && state == "stopped" {
					wantCalls = 0
				}
				if got := fake.Calls(); len(got) != wantCalls || wantCalls == 1 && keep && got[0] != "stop lrun_1" {
					t.Errorf("Lux calls=%v, want %d (%s)", got, wantCalls, wantReason)
				}
				if w.stopReason() != wantReason {
					t.Errorf("stop reason=%s, want %s", w.stopReason(), wantReason)
				}
			})
		}
	}
}

func TestAbortAdvanceRechecksStatusAndControl(t *testing.T) {
	for _, fresh := range []string{"completed", "failed", "resume"} {
		t.Run(fresh, func(t *testing.T) {
			w := newResumeWorld(t)
			r := w.run
			r.Status, r.Control = "running", "abort"
			w.exec(`UPDATE runs SET status='running', control='abort', ended_at=NULL WHERE id=$1`, r.ID)
			if fresh == "resume" {
				w.exec(`UPDATE runs SET control='resume' WHERE id=$1`, r.ID)
			} else {
				w.exec(`UPDATE runs SET status=$2::run_status, ended_at=now()-interval '1 hour' WHERE id=$1`, r.ID, fresh)
			}
			fake := w.onCallLux(nil)
			if _, err := w.s.advance(w.ctx, r); err != nil {
				t.Fatal(err)
			}
			if fresh != "failed" && len(fake.Calls()) != 0 {
				t.Errorf("stale abort asked Lux %v", fake.Calls())
			}
			if n := w.count(`SELECT count(*) FROM events WHERE run_id=$1 AND event_type='run.aborted'`); n != 0 {
				t.Errorf("stale abort emitted %d events", n)
			}
			if fresh != "resume" && w.count(`SELECT count(*) FROM runs WHERE id=$1 AND ended_at<now()-interval '30 minutes'`) != 1 {
				t.Error("terminal timestamp overwritten")
			}
			wantStatus := fresh
			if fresh == "resume" {
				wantStatus = "running"
			}
			var status string
			if err := w.owner.QueryRow(w.ctx, `SELECT status::text FROM runs WHERE id=$1`, r.ID).Scan(&status); err != nil || status != wantStatus {
				t.Errorf("fresh status=%s err=%v, want %s", status, err, wantStatus)
			}
		})
	}
}

func TestAbortSweepLiveReproductionAndNotifier(t *testing.T) {
	w := newResumeWorld(t)
	w.exec(`UPDATE runs SET status='running', control='abort', control_requested_at=now(),
		control_reason='checks gate stop', lux_state='running', lux_stop_reason=NULL WHERE id=$1`, w.run.ID)
	w.exec(`INSERT INTO workflow_runs(id,organization_id,task_id,workflow_type,idempotency_key,status,step,state)
		VALUES('wf_abort',$1,$2,'task.delivery','abort','waiting','awaitPullRequest','{"attempt":1}')`, w.run.Org, w.run.TaskID)
	w.exec(`INSERT INTO directives(id,organization_id,task_id,run_id,text)
		VALUES('dir_abort',$1,$2,$3,'unread')`, w.run.Org, w.run.TaskID, w.run.ID)
	var posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		posts.Add(1)
		rw.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	w.s.Lux = lux.New(server.URL, "test")
	acted := 0
	var ended time.Time
	for pass := range 12 {
		n, err := w.s.Sweep(w.ctx)
		if err != nil {
			t.Fatal(err)
		}
		acted += n
		var at time.Time
		if err := w.owner.QueryRow(w.ctx, `SELECT ended_at FROM runs WHERE id=$1`, w.run.ID).Scan(&at); err != nil {
			t.Error(err)
		} else if pass == 0 {
			ended = at
		} else if !at.Equal(ended) {
			t.Error("repeated sweep changed ended_at")
		}
	}
	t.Logf("12 real Sweeps: cancel POSTs=%d acted=%d", posts.Load(), acted)
	if posts.Load() != 1 || acted != 1 {
		t.Errorf("want exactly one cancel and one acted Sweep")
	}
	if w.count(`SELECT count(*) FROM directives WHERE run_id=$1 AND failed_at IS NOT NULL
		AND error='the run was aborted before the agent read it'`) != 1 {
		t.Error("unread directive not settled")
	}
	if w.count(`SELECT count(*) FROM events WHERE run_id=$1 AND event_type='run.directive.failed'`) != 1 {
		t.Error("directive failure ledger event not exactly once")
	}
	// Artifacts are collected independently; let the notifier observe that boundary.
	w.exec(`UPDATE runs SET artifacts_due_at=NULL WHERE id=$1`, w.run.ID)
	signals := 0
	for pass := range 2 {
		n, err := NotifyFinished(w.ctx, w.s.DB, func(_ context.Context, org, workflow, run, status, key string) error {
			signals++
			if org != w.run.Org || workflow != "wf_abort" || run != w.run.ID || status != "aborted" || key != "phase-finished:"+w.run.ID {
				t.Errorf("unexpected phase notification: %s %s %s %s %s", org, workflow, run, status, key)
			}
			return nil
		})
		if err != nil || n != 1-pass {
			t.Errorf("notifier pass %d: n=%d err=%v", pass, n, err)
		}
	}
	if signals != 1 {
		t.Errorf("phase signals=%d, want 1", signals)
	}
}

func TestAFreshAbortDuringCleanupBackoffIsConsumedOnce(t *testing.T) {
	w := newResumeWorld(t)
	w.exec(`UPDATE runs SET status='aborted', ended_at=now()-interval '1 hour', control='none',
		lux_state='running', lux_stop_reason=NULL, next_attempt_at=now()+interval '1 hour',
		retry_due_at=now()+interval '1 hour', retry_failures=3 WHERE id=$1`, w.run.ID)
	fake := w.onCallLux(map[string]error{"lrun_1": context.DeadlineExceeded})
	if n, err := w.s.Sweep(w.ctx); err != nil || n != 0 || len(fake.Calls()) != 0 {
		t.Fatalf("old cleanup bypassed backoff: %d %v %v", n, err, fake.Calls())
	}
	w.exec(`UPDATE runs SET control='abort', control_requested_at=now(), control_reason='fresh stop' WHERE id=$1`, w.run.ID)
	if n, err := w.s.Sweep(w.ctx); err != nil || n != 0 || len(fake.Calls()) != 1 {
		t.Fatalf("fresh abort not immediate: %d %v %v", n, err, fake.Calls())
	}
	for range 12 {
		if n, err := w.s.Sweep(w.ctx); err != nil || n != 0 {
			t.Fatalf("cleanup ignored fresh backoff: %d %v", n, err)
		}
	}
	if len(fake.Calls()) != 1 || w.count(`SELECT count(*) FROM runs WHERE id=$1 AND control='none'
		AND control_reason='fresh stop' AND ended_at<now()-interval '30 minutes' AND retry_failures=1`) != 1 {
		t.Fatal("fresh control was not consumed once with original terminal timestamp")
	}
}
