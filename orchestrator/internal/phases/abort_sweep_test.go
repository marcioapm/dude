package phases

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
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
					w.sweepActedOnce()
					if calls.Load() != 1 {
						t.Errorf("cancel POSTs=%d, want 1", calls.Load())
					}
					if n := w.count(`SELECT count(*) FROM runs WHERE id=$1 AND status='aborted' AND ended_at IS NOT NULL
						AND control='none' AND lux_stop_reason='cancel' AND NOT keep AND control_reason='checks gate stop'`); n != 1 {
						t.Error("abort did not durably settle")
					}
					// A child's run.aborted, if any, is written by its stop request
					// (conductor stop_run or task Stop); the sweep writes none for children.
					wantEvents := 1
					if child {
						wantEvents = 0
					}
					if n := w.abortedEvents(); n != wantEvents {
						t.Errorf("sweep-written abort ledger events=%d, want %d", n, wantEvents)
					}
					if !child && w.count(`SELECT count(*) FROM events WHERE run_id=$1 AND event_type='run.aborted'
						AND payload->>'reason'='checks gate stop'`) != 1 {
						t.Error("ordinary abort event lacks its reason")
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
				firstEnded := w.endedAt()
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
				// Once only across the retries: the live Run's one transition
				// event (none for one already aborted) and its first ended_at.
				wantEvents := 0
				if status == "running" {
					wantEvents = 1
				}
				if n := w.abortedEvents(); n != wantEvents {
					t.Errorf("run.aborted events=%d, want %d", n, wantEvents)
				}
				if got := w.endedAt(); !got.Equal(firstEnded) {
					t.Errorf("ended_at moved across retries: %v -> %v", firstEnded, got)
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
				w.sweepActedOnce()
				wantReason, wantCalls := "cancel", 1
				if keep && state != "terminated" && state != "missing" {
					wantReason = "kept"
				}
				if state == "missing" {
					// No lux Run, nothing cancelled: no stop reason to stamp.
					wantReason = ""
				}
				if state == "terminated" || state == "missing" || keep && state == "stopped" {
					wantCalls = 0
				}
				var wantLux []string
				if wantCalls == 1 {
					wantLux = []string{"cancel lrun_1"}
					if wantReason == "kept" {
						wantLux = []string{"stop lrun_1"}
					}
				}
				if got := fake.Calls(); !slices.Equal(got, wantLux) {
					t.Errorf("Lux calls=%v, want %v", got, wantLux)
				}
				if w.stopReason() != wantReason {
					t.Errorf("stop reason=%s, want %s", w.stopReason(), wantReason)
				}
				var status, control string
				var ended *time.Time
				var kept bool
				if err := w.owner.QueryRow(w.ctx, `SELECT status::text, control::text, ended_at, keep FROM runs WHERE id=$1`,
					w.run.ID).Scan(&status, &control, &ended, &kept); err != nil {
					t.Fatal(err)
				}
				if status != "aborted" || control != "none" || ended == nil || kept != keep {
					t.Errorf("terminal row status=%s control=%s ended_at=%v keep=%t, want aborted none set %t",
						status, control, ended, kept, keep)
				}
				if n := w.abortedEvents(); n != 1 {
					t.Errorf("run.aborted events=%d, want 1", n)
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
			if n := w.abortedEvents(); n != 0 {
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
		if at := w.endedAt(); pass == 0 {
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

// sweepActedOnce runs 12 Sweeps: the first acts, the rest find nothing due.
func (w *resumeWorld) sweepActedOnce() {
	w.t.Helper()
	for pass := range 12 {
		want := 0
		if pass == 0 {
			want = 1
		}
		if n, err := w.s.Sweep(w.ctx); err != nil || n != want {
			w.t.Errorf("pass %d: acted=%d err=%v, want %d", pass, n, err, want)
		}
	}
}

func (w *resumeWorld) abortedEvents() int {
	w.t.Helper()
	return w.count(`SELECT count(*) FROM events WHERE run_id=$1 AND event_type='run.aborted'`)
}

func (w *resumeWorld) endedAt() time.Time {
	w.t.Helper()
	var at time.Time
	if err := w.owner.QueryRow(w.ctx, `SELECT ended_at FROM runs WHERE id=$1`, w.run.ID).Scan(&at); err != nil {
		w.t.Fatal(err)
	}
	return at
}

// An abort of a Run lux never had settles once, stamps no lux stop reason
// and leaves nothing due, so a resume submits it as a fresh Run (see
// TestAReviewerNeverStartedIsQueuedAgainOnResume). One already aborted
// writes no second run.aborted.
func TestALuxlessAbortSettlesOnceWithoutAStopReason(t *testing.T) {
	for _, tc := range []struct {
		status string
		events int
	}{{"pending", 1}, {"paused", 1}, {"aborted", 0}} {
		t.Run(tc.status, func(t *testing.T) {
			w := newResumeWorld(t)
			w.exec(`UPDATE runs SET status=$2::run_status, ended_at=CASE WHEN $2='aborted' THEN now() END,
				control='abort', control_requested_at=now(), control_reason='stop',
				lux_run_id=NULL, lux_state=NULL, lux_stop_reason=NULL, dude_pause=NULL WHERE id=$1`, w.run.ID, tc.status)
			fake := w.onCallLux(nil)
			w.sweepActedOnce()
			if got := fake.Calls(); len(got) != 0 {
				t.Errorf("Lux calls=%v, want none", got)
			}
			if w.count(`SELECT count(*) FROM runs WHERE id=$1 AND status='aborted' AND control='none'
				AND ended_at IS NOT NULL AND lux_stop_reason IS NULL`) != 1 {
				t.Errorf("Lux-less abort not settled cleanly (stop reason %q)", w.stopReason())
			}
			if n := w.abortedEvents(); n != tc.events {
				t.Errorf("run.aborted events=%d, want %d", n, tc.events)
			}
		})
	}
}

// A paused Run no resume is due for (dude_pause NULL: resumable NULL) is
// still due for its abort, once.
func TestAPausedAbortWithNoResumeIsDueOnce(t *testing.T) {
	w := newResumeWorld(t)
	w.exec(`UPDATE runs SET status='paused', dude_pause=NULL, control='abort', control_requested_at=now(),
		lux_state='stopped', lux_stop_reason=NULL WHERE id=$1`, w.run.ID)
	fake := w.onCallLux(nil)
	w.sweepActedOnce()
	if w.count(`SELECT count(*) FROM runs WHERE id=$1 AND status='aborted' AND control='none'
		AND ended_at IS NOT NULL AND lux_stop_reason='cancel' AND NOT keep`) != 1 {
		t.Errorf("paused abort not settled (stop reason %q)", w.stopReason())
	}
	if n := w.abortedEvents(); n != 1 {
		t.Errorf("run.aborted events=%d, want 1", n)
	}
	if got := fake.Calls(); !slices.Equal(got, []string{"cancel lrun_1"}) {
		t.Errorf("Lux calls=%v", got)
	}
}

// A person's Stop of an ordinary Run already aborted and kept: stopped and
// kept once, its control consumed, no second run.aborted.
func TestAKeptAbortedRunsStopIsAskedOnce(t *testing.T) {
	w := newResumeWorld(t)
	w.exec(`UPDATE runs SET status='aborted', ended_at=now(), control='abort', control_requested_at=now(),
		keep=true, kept_until=NULL, dude_pause=NULL, lux_state='running', lux_stop_reason=NULL WHERE id=$1`, w.run.ID)
	fake := w.onCallLux(nil)
	w.s.KeepFor = time.Hour
	w.sweepActedOnce()
	if got := fake.Calls(); !slices.Equal(got, []string{"stop lrun_1"}) {
		t.Errorf("Lux calls=%v, want [stop lrun_1]", got)
	}
	if w.count(`SELECT count(*) FROM runs WHERE id=$1 AND status='aborted' AND control='none'
		AND lux_stop_reason='kept' AND keep`) != 1 {
		t.Errorf("kept abort not settled (stop reason %q)", w.stopReason())
	}
	if n := w.abortedEvents(); n != 0 {
		t.Errorf("sweep-written run.aborted events=%d, want 0", n)
	}
}
