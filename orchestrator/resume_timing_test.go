package orchestrator_test

// Every resume of a Run is timed, end to end (phases/resumes.go): one
// run_resumes row per lux resume, its cause and when it became due, lux's
// placements, and when the agent was back, took its input and said
// something; then one run.resume.timed.

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

// resumeRow is a run_resumes row, as the tests read it.
type resumeRow struct {
	Epoch                                                                        int
	Cause                                                                        string
	Woken, Requested, Assigned, ImageReady, Restored, ContainerStarted, Workload *time.Time
	HostName, StoppedHost                                                        *string
	StopRequested, Exited, SnapshotDone, Uploaded                                *time.Time
	SnapshotBytes                                                                *int64
	Moved                                                                        *bool
	Running, Busy, FirstOutput                                                   *time.Time
}

func (w *world) resumes(runID string) []resumeRow {
	w.t.Helper()
	rows, err := w.owner.Query(context.Background(), `SELECT epoch, cause, woken_at, requested_at, assigned_at,
		image_ready_at, volumes_restored_at, container_started_at, workload_started_at, host_name, stopped_host_name,
		stop_requested_at, exited_at, snapshot_done_at, uploaded_at, snapshot_bytes, moved, running_at, busy_at, first_output_at
		FROM run_resumes WHERE run_id = $1 ORDER BY epoch`, runID)
	if err != nil {
		w.t.Fatal(err)
	}
	defer rows.Close()
	var out []resumeRow
	for rows.Next() {
		var r resumeRow
		if err := rows.Scan(&r.Epoch, &r.Cause, &r.Woken, &r.Requested, &r.Assigned, &r.ImageReady, &r.Restored,
			&r.ContainerStarted, &r.Workload, &r.HostName, &r.StoppedHost, &r.StopRequested, &r.Exited, &r.SnapshotDone,
			&r.Uploaded, &r.SnapshotBytes, &r.Moved, &r.Running, &r.Busy, &r.FirstOutput); err != nil {
			w.t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

// timedEvents are the Run's run.resume.timed payloads, in order.
func (w *world) timedEvents(runID string) []map[string]any {
	w.t.Helper()
	rows, err := w.owner.Query(context.Background(), `SELECT payload FROM events
		WHERE run_id = $1 AND event_type = 'run.resume.timed' ORDER BY cursor`, runID)
	if err != nil {
		w.t.Fatal(err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var raw []byte
		var p map[string]any
		if err := rows.Scan(&raw); err != nil {
			w.t.Fatal(err)
		}
		_ = json.Unmarshal(raw, &p)
		out = append(out, p)
	}
	return out
}

// oneTimedResume checks the Run was resumed once, with cause, due at
// woken, every stamp present and each clock's in order, and timed once.
// dude's stamps are Postgres's clock and lux's the fake's: each sequence
// is compared within its own clock. A stamp out of order says whether
// Postgres's wall clock was stepped back meanwhile (a VM's time sync
// does), which puts any two stamps out of order.
func (w *world) oneTimedResume(runID, cause string, woken time.Time, moved bool) resumeRow {
	w.t.Helper()
	steps := w.watchClock()
	w.until("the resume to be timed", func() bool { return len(w.timedEvents(runID)) > 0 })
	rows := w.resumes(runID)
	if len(rows) != 1 {
		w.t.Fatalf("%d resumes recorded, want 1", len(rows))
	}
	r := rows[0]
	if r.Epoch != 2 || r.Cause != cause {
		w.t.Errorf("resume: epoch %d cause %q, want 2 %q", r.Epoch, r.Cause, cause)
	}
	if r.Woken == nil || !r.Woken.Equal(woken) {
		w.t.Errorf("woken_at %v, want %v", r.Woken, woken)
	}
	inOrder := func(clock string, stamps ...*time.Time) {
		for i, s := range stamps {
			if s == nil {
				w.t.Errorf("%s stamp %d missing: %+v", clock, i, r)
				return
			}
			if i > 0 && s.Before(*stamps[i-1]) {
				w.t.Errorf("%s stamps out of order at %d: %v before %v; Postgres's clock stepped back meanwhile: %v",
					clock, i, s, stamps[i-1], steps())
			}
		}
	}
	inOrder("dude", r.Woken, r.Requested, r.Running, r.Busy, r.FirstOutput)
	inOrder("lux, new placement", r.Assigned, r.ImageReady, r.Restored, r.ContainerStarted, r.Workload)
	inOrder("lux, stopped placement", r.StopRequested, r.Exited, r.SnapshotDone, r.Uploaded)
	if r.HostName == nil || r.StoppedHost == nil || r.SnapshotBytes == nil || *r.SnapshotBytes <= 0 {
		w.t.Errorf("hosts or snapshot size missing: %+v", r)
	}
	if r.Moved == nil || *r.Moved != moved {
		w.t.Errorf("moved = %v, want %v", r.Moved, moved)
	}
	events := w.timedEvents(runID)
	if len(events) != 1 {
		w.t.Fatalf("%d run.resume.timed, want 1", len(events))
	}
	e := events[0]
	phases, _ := e["phases"].(map[string]any)
	if e["cause"] != cause || e["epoch"] != 2.0 || e["moved"] != moved || e["hostName"] != *r.HostName ||
		e["totalMs"] == nil || e["untilBusyMs"] == nil || len(phases) != 8 {
		w.t.Errorf("run.resume.timed = %v", e)
	}
	return r
}

// watchClock reads Postgres's clock_timestamp() every few milliseconds
// until the test ends, on a connection of its own; the returned func
// lists each step back seen so far.
func (w *world) watchClock() func() []string {
	w.t.Helper()
	conn, err := pgx.Connect(context.Background(), w.owner.Config().ConnString())
	if err != nil {
		w.t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var mu sync.Mutex
	var steps []string
	done := make(chan struct{})
	go func() {
		defer close(done)
		var prev time.Time
		for ctx.Err() == nil {
			var now time.Time
			if conn.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now) != nil {
				return
			}
			if now.Before(prev) {
				mu.Lock()
				steps = append(steps, fmt.Sprintf("%s back at %s", prev.Sub(now), now.Format("15:04:05.000000")))
				mu.Unlock()
			}
			prev = now
			time.Sleep(2 * time.Millisecond)
		}
	}()
	w.t.Cleanup(func() { cancel(); <-done; conn.Close(context.Background()) })
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(steps)
	}
}

func (w *world) stamp(sql string, args ...any) time.Time {
	w.t.Helper()
	var at time.Time
	if err := w.owner.QueryRow(context.Background(), sql, args...).Scan(&at); err != nil {
		w.t.Fatal(err)
	}
	return at
}

// An answer that wakes a parked Run: due when it was answered.
func TestAResumeAnAnswerWokeIsTimedFromTheAnswer(t *testing.T) {
	w := newWorld(t)
	w.syncer.ParkAfter = 300 * time.Millisecond
	wi, runID := w.asking()
	w.until("the run to be parked and stopped", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND dude_pause = 'person'`, runID) == 1 &&
			w.lux.Runs()[0].State == "stopped"
	})
	qid := w.questionID(wi)
	if status, body := w.call("/internal/questions/"+qid+"/answer", map[string]any{"text": "yes"}); status != 200 {
		t.Fatalf("answer: %d %v", status, body)
	}
	answered := w.stamp(`SELECT answered_at FROM questions WHERE id = $1`, qid)
	w.oneTimedResume(runID, "answer", answered, false)
	w.until("the implementer to finish", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'completed'`, runID) == 1
	})
}

// A repository approved while the agent works: dude pauses it to bring the
// repository, and the resume is due from the approval.
func TestAResumeForAnApprovedRepositoryIsTimedFromTheApproval(t *testing.T) {
	w := newWorld(t)
	w.withTools()
	mustExec(t, w.owner, `INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch)
		VALUES ($1, $2, $3, 'web', 'git://127.0.0.1/acme/web.git', 'main')`, "repo_web_"+w.org, w.org, w.project)
	w.lux.Decide = func(map[string]any) fakelux.Behaviour {
		return fakelux.Behaviour{Hang: true, Reply: "Done.", Commit: map[string]string{"A.md": "a\n"}}
	}
	wi := w.task()
	w.names(wi, w.repoID)
	w.deliver(wi)
	w.until("the agent to be working", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND status = 'running' AND lux_state = 'running'`, wi) == 1
	})
	var runID string
	_ = w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE task_id = $1`, wi).Scan(&runID)
	_, body := w.callTool(w.syncer.Agent.ToolsURL, string(w.lux.Runs()[0].Spec), "request_repository",
		`{"repository":"web","reason":"the client"}`)
	var req struct{ RequestID string }
	_ = json.Unmarshal([]byte(body), &req)
	if status, body := w.call("/internal/repository-requests/"+req.RequestID+"/decide", map[string]any{"approve": true}); status != 200 {
		t.Fatalf("approve: %d %v", status, body)
	}
	approved := w.stamp(`SELECT decided_at FROM repository_requests WHERE id = $1`, req.RequestID)
	w.oneTimedResume(runID, "repository", approved, false)
}

// A person's Pause and Resume: due when they asked to resume.
func TestAPersonsResumeIsTimedFromTheirResume(t *testing.T) {
	w := newWorld(t)
	w.lux.Decide = func(map[string]any) fakelux.Behaviour {
		return fakelux.Behaviour{Hang: true, Reply: "Done after resume.", Commit: map[string]string{"A.md": "a\n"}}
	}
	wi := w.task()
	w.deliver(wi)
	w.until("the agent to be working", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND status = 'running' AND lux_state = 'running'`, wi) == 1
	})
	var runID string
	_ = w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE task_id = $1`, wi).Scan(&runID)
	if status, body := w.call("/internal/runs/"+runID+"/pause", map[string]any{}); status != 200 {
		t.Fatalf("pause: %d %v", status, body)
	}
	w.until("lux to report it stopped", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND lux_state = 'stopped'`, runID) == 1
	})
	if status, body := w.call("/internal/runs/"+runID+"/resume", map[string]any{}); status != 200 {
		t.Fatalf("resume: %d %v", status, body)
	}
	asked := w.stamp(`SELECT control_requested_at FROM runs WHERE id = $1`, runID)
	w.oneTimedResume(runID, "person", asked, false)
}

// A person resuming an idle park: its own cause, due from their Resume;
// and a resume lux places on another host says it moved.
func TestAPersonsResumeOfAnIdleParkIsTimedAndAMoveIsSaid(t *testing.T) {
	w := newWorld(t)
	w.lux.MoveOnResume = true
	w.syncer.IdleAfter = 300 * time.Millisecond
	w.lux.Decide = func(map[string]any) fakelux.Behaviour {
		return fakelux.Behaviour{Hang: true, Reply: "Back at it.", Commit: map[string]string{"A.md": "a\n"}}
	}
	wi := w.task()
	w.deliver(wi)
	var runID string
	w.until("the run to be parked as idle", func() bool {
		_ = w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE task_id = $1 AND status = 'paused'
			AND dude_pause = 'idle' AND lux_state = 'stopped'`, wi).Scan(&runID)
		return runID != ""
	})
	if status, body := w.call("/internal/runs/"+runID+"/resume", map[string]any{}); status != 200 {
		t.Fatalf("resume: %d %v", status, body)
	}
	asked := w.stamp(`SELECT control_requested_at FROM runs WHERE id = $1`, runID)
	r := w.oneTimedResume(runID, "idle", asked, true)
	if *r.HostName == *r.StoppedHost {
		t.Errorf("moved, but on %s both times", *r.HostName)
	}
}
