package orchestrator_test

// A phase Run that makes no progress (design: F6): dude reports the facts
// to the task's conductor or, on a plain delivery, its owner, who decides —
// leave it, steer it, restart it. Timestamps are back-dated; nothing sleeps
// for the windows.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// taskCall is the run-2 reviewer's hung call: OpenCode's task sub-agent.
var taskCall = [3]string{"task", "other", `{"description":"Review worker state behavior","prompt":"Read the worker and check its state"}`}

// agentPS is a container where only the agent runs, as run 2's did.
func agentPS(string) string {
	return "  PID  PPID     ELAPSED %CPU COMMAND\n    1     0       31:02  0.0 /.lux/bin/lux-shim run\n" +
		"    7     1       31:00  1.2 opencode acp\n    9     7       31:00  0.0 opencode acp\n"
}

// hangingReviews delivers a change two reviewers read (correctness and
// security); the round's two reviewers hang in an open call, and any later
// one finds nothing. The implementer commits.
func (w *world) hangingReviews(call [3]string) {
	w.t.Helper()
	scripted := w.lux.Decide
	var reviews atomic.Int32
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		labels, _ := spec["labels"].(map[string]any)
		switch labels["dude.phase"] {
		case "implement":
			return fakelux.Behaviour{Commit: map[string]string{"auth/session.go": "package auth\n"}, Message: "work"}
		case "review":
			if reviews.Add(1) <= 2 {
				return fakelux.Behaviour{Hang: true, OpenCalls: [][3]string{call}}
			}
			return fakelux.Behaviour{Reply: "Looks good."}
		}
		return scripted(spec)
	}
	w.lux.SetPS(agentPS, "")
}

// reviewersOpen waits for n reviewers with their call open, and returns them.
func (w *world) reviewersOpen(task string, n int) []string {
	w.t.Helper()
	var ids []string
	w.until("reviewers in their open call", func() bool {
		ids = w.ids(`SELECT id FROM runs WHERE task_id = $1 AND phase = 'review' AND status = 'running'
			AND open_tool_calls_at <> '{}' ORDER BY created_at`, task)
		return len(ids) == n
	})
	return ids
}

func (w *world) ids(sql string, args ...any) []string {
	w.t.Helper()
	rows, err := w.owner.Query(context.Background(), sql, args...)
	if err != nil {
		w.t.Fatal(err)
	}
	var out []string
	for rows.Next() {
		var id string
		_ = rows.Scan(&id)
		out = append(out, id)
	}
	return out
}

// openSince back-dates every open call of a Run: open since ago.
func (w *world) openSince(runID string, ago time.Duration) {
	mustExec(w.t, w.owner, `UPDATE runs SET open_tool_calls_at = (SELECT jsonb_object_agg(k, to_jsonb(now() - make_interval(secs => $2)))
		FROM jsonb_object_keys(open_tool_calls_at) k) WHERE id = $1`, runID, ago.Seconds())
}

// reportedAgo back-dates a Run's last stall report.
func (w *world) reportedAgo(runID string, ago time.Duration) {
	mustExec(w.t, w.owner, `UPDATE runs SET stall_reported_at = now() - make_interval(secs => $2) WHERE id = $1`, runID, ago.Seconds())
}

func (w *world) stalls(runID string) int {
	return w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.stalled'`, runID)
}

// stallOf is the facts of a Run's latest stall report.
func (w *world) stallOf(runID string) (stall map[string]any, text string) {
	w.t.Helper()
	var raw []byte
	_ = w.owner.QueryRow(context.Background(), `SELECT payload FROM events WHERE run_id = $1 AND event_type = 'run.stalled'
		ORDER BY cursor DESC LIMIT 1`, runID).Scan(&raw)
	var p struct {
		Stall map[string]any `json:"stall"`
		Text  string         `json:"text"`
	}
	_ = json.Unmarshal(raw, &p)
	return p.Stall, p.Text
}

// conductedReview is a conducted task at its first review round.
func (w *world) conductedReview(task string) {
	w.t.Helper()
	w.talk(task)
	w.must(task, "start_phase", `{"phase":"implement"}`)
	w.until("after implement", func() bool {
		return w.decisionAt(task) != "" && w.phaseRuns(task, "implement") == 1 &&
			w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'implement' AND status = 'completed'`, task) == 1
	})
	w.must(task, "start_phase", `{"phase":"review"}`)
}

// Two reviewers of a conducted task, each in a call open for 30 minutes:
// the conductor is woken once, the note carrying both with their facts.
func TestStalledReviewersAreOneWakeWithTheirFacts(t *testing.T) {
	w := conducting(t)
	w.hangingReviews(taskCall)
	task := w.task()
	w.conductedReview(task)
	runs := w.reviewersOpen(task, 2)
	for _, id := range runs {
		w.lux.SetUsage(w.luxRunOf(id), lux.Usage{CPUSeconds: 18})
	}
	// 29 minutes: not yet.
	for _, id := range runs {
		w.openSince(id, 29*time.Minute)
	}
	w.sweep()
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'run.stalled'`, task); n != 0 {
		t.Fatalf("%d stall reports before 30 minutes", n)
	}
	for _, id := range runs {
		w.openSince(id, 31*time.Minute)
	}
	note := w.wokenWith(task, "made no progress")
	for _, id := range runs {
		if w.stalls(id) != 1 || !strings.Contains(note, id) {
			t.Errorf("reviewer %s: %d reports; the note names it: %v", id, w.stalls(id), strings.Contains(note, id))
		}
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'conductor.woken'
		AND payload->>'text' LIKE '%made no progress%'`, task); n != 1 {
		t.Errorf("%d wakes for two stalled reviewers, want one", n)
	}
	for _, want := range []string{"review Run", "round 1", "has made no progress for 30 min", "It has 1 tool call open: `task` \"Review worker state behavior\", for 31 min",
		"runs inside the agent: no separate process is expected", "Processes: opencode acp (1.2 % CPU", "CPU over", "18 s; network: 0 B",
		"Leave it, steer it (interrupt to stop its turn), or restart_run"} {
		if !strings.Contains(note, want) {
			t.Errorf("the note lacks %q:\n%s", want, note)
		}
	}
	if strings.Contains(note, "lux-shim") || strings.Count(note, "opencode acp") != 2 {
		t.Errorf("the processes are not trimmed (shim dropped, the agent once per reviewer):\n%s", note)
	}
	// The badge: a stalled Run is one until it makes progress.
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND run_stalled(runs)`, task); n != 2 {
		t.Errorf("%d runs read as stalled, want 2", n)
	}
}

// A Run whose facts have not changed is reported again only after 60
// minutes; one whose facts changed, after 30.
func TestAStalledRunIsReportedAgainAfterAnHourUnlessItsFactsChanged(t *testing.T) {
	w := conducting(t)
	w.hangingReviews(taskCall)
	task := w.task()
	w.conductedReview(task)
	runs := w.reviewersOpen(task, 2)
	id := runs[0]
	mustExec(t, w.owner, `UPDATE runs SET open_tool_calls_at = '{}' WHERE id = $1`, runs[1])
	w.openSince(id, 31*time.Minute)
	w.sweep()
	if w.stalls(id) != 1 {
		t.Fatalf("%d reports, want 1", w.stalls(id))
	}
	w.reportedAgo(id, 31*time.Minute)
	w.sweep()
	if n := w.stalls(id); n != 1 {
		t.Errorf("the same facts reported again at 30 minutes (%d reports)", n)
	}
	w.reportedAgo(id, 61*time.Minute)
	w.sweep()
	if n := w.stalls(id); n != 2 {
		t.Errorf("the same facts not reported at 60 minutes (%d reports)", n)
	}
	// Another call opened since: changed facts, told at 30 minutes.
	mustExec(t, w.owner, `UPDATE runs SET open_tool_calls_at = open_tool_calls_at || jsonb_build_object('call_new', now() - interval '40 minutes')
		WHERE id = $1`, id)
	w.reportedAgo(id, 31*time.Minute)
	w.sweep()
	if n := w.stalls(id); n != 3 {
		t.Errorf("changed facts not reported at 30 minutes (%d reports)", n)
	}
}

// An implementer whose files have not changed for its window, with reads
// and no edits, is reported with what it did; one whose diff changes is not.
func TestAnImplementerWhoseFilesDoNotChangeIsReported(t *testing.T) {
	w := newWorld(t)
	w.lux.Workspaces = t.TempDir()
	w.syncer.DiffDelay, w.syncer.DiffEvery = 10*time.Millisecond, time.Hour
	mustExec(t, w.owner, `UPDATE projects SET agent_models = jsonb_set(agent_models, '{implementer,timeLimitMinutes}', '30') WHERE id = $1`, w.project)
	w.lux.Decide = func(map[string]any) fakelux.Behaviour {
		return fakelux.Behaviour{Hang: true, Tools: []string{"todowrite", "read", "read", "grep"}, Reply: "Looking at the parser."}
	}
	w.lux.SetPS(agentPS, "")
	wi := w.task()
	w.deliver(wi)
	var runID, luxID string
	w.until("the implementer working", func() bool {
		_ = w.owner.QueryRow(context.Background(), `SELECT id, COALESCE(lux_run_id, '') FROM runs WHERE task_id = $1
			AND phase = 'implement' AND files_changed_at IS NOT NULL AND agent_busy_at IS NOT NULL`, wi).Scan(&runID, &luxID)
		return luxID != "" && w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'agent.tool.called'`, runID) >= 3
	})
	unchanged := func() {
		mustExec(t, w.owner, `UPDATE runs SET files_changed_at = now() - interval '31 minutes' WHERE id = $1`, runID)
	}
	// Its diff changes: progress. The live diff is read by the Run's
	// follower, not the sweep, so it is waited for without sweeping.
	unchanged()
	w.lux.Edit(luxID, map[string]string{"parser.go": "package parser\n"})
	for deadline := time.Now().Add(10 * time.Second); w.count(`SELECT count(*) FROM run_diffs WHERE run_id = $1
		AND files @> '[{"path": "parser.go"}]'`, runID) == 0; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the new diff was never read")
		}
	}
	w.sweep()
	if n := w.stalls(runID); n != 0 {
		t.Fatalf("an implementer whose diff changed was reported (%d)", n)
	}
	// Its files no longer change.
	unchanged()
	w.sweep()
	if n := w.stalls(runID); n != 1 {
		t.Fatalf("%d reports of an implementer with no change for 30 minutes, want 1", n)
	}
	stall, text := w.stallOf(runID)
	if reasons, _ := json.Marshal(stall["reasons"]); string(reasons) != `["files"]` {
		t.Errorf("reasons %s", reasons)
	}
	for _, want := range []string{"tool calls in the window: 2 read, 1 edit, 1 grep, 0 bash.", "Its files last changed at", "Its plan: do it [in_progress]"} {
		if !strings.Contains(text, want) {
			t.Errorf("the report lacks %q:\n%s", want, text)
		}
	}
}

// A plain delivery: nothing at 30 minutes; at the role's 120 its owner is
// told once, and after Leave it never again for that Run.
func TestAPlainDeliverysOwnerIsToldOnceAndLeaveItHolds(t *testing.T) {
	w := newWorld(t)
	ana := w.person("Ana")
	w.hangingReviews(taskCall)
	wi := w.task()
	w.deliver(wi)
	w.assignOwner(wi, ana)
	runs := w.reviewersOpen(wi, 2)
	id := runs[0]
	mustExec(t, w.owner, `UPDATE runs SET open_tool_calls_at = '{}' WHERE id = $1`, runs[1])
	w.openSince(id, 31*time.Minute)
	w.pump()
	if n := w.stalls(id); n != 0 {
		t.Fatalf("a plain delivery's owner told at 30 minutes")
	}
	w.openSince(id, 121*time.Minute)
	w.pump()
	if n := w.stalls(id); n != 1 {
		t.Fatalf("%d reports at 120 minutes, want 1", n)
	}
	_, text := w.stallOf(id)
	if !strings.Contains(text, "no progress for 2.0 h") || strings.Contains(text, "restart_run") {
		t.Errorf("the owner's report: %s", text)
	}
	if n := w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1`, wi); n != 0 {
		t.Errorf("a plain delivery woke a conductor")
	}
	// Once per Run.
	w.reportedAgo(id, 5*time.Hour)
	w.pump()
	if n := w.stalls(id); n != 1 {
		t.Errorf("the owner was told %d times", n)
	}
	bo := w.person("Bo")
	if status, _ := w.callAs(bo, "/internal/runs/"+id+"/leave", map[string]any{}); status != 403 {
		t.Errorf("someone else left it: %d", status)
	}
	if status, body := w.callAs(ana, "/internal/runs/"+id+"/leave", map[string]any{}); status != 200 {
		t.Fatalf("leave: %d %v", status, body)
	}
	mustExec(t, w.owner, `UPDATE runs SET stall_reported_at = NULL WHERE id = $1`, id)
	w.pump()
	if n := w.stalls(id); n != 1 {
		t.Errorf("a Run left as it is was reported again (%d)", n)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.stall_left' AND actor_id = $2`, id, ana); n != 1 {
		t.Errorf("leaving it is not recorded as Ana's")
	}
}

// The processes say what they can: a command's process alive or gone; a
// call inside the agent expects none; an exec that fails is named and the
// report still goes out.
func TestAStallReportSaysWhatTheProcessesShow(t *testing.T) {
	bash := [3]string{"bash", "execute", `{"command":"npm test -- --watch"}`}
	cases := []struct {
		name  string
		call  [3]string
		ps    func(string) string
		fails string
		want  []string
	}{
		{"a command still running", bash, func(string) string {
			return "PID PPID ELAPSED %CPU COMMAND\n 7 1 2:00:00 0.5 opencode acp\n 40 7 1:59:00 97.0 npm test -- --watch\n"
		}, "", []string{"`bash`'s command is running", "npm test -- --watch (97.0 % CPU"}},
		{"a command gone", bash, agentPS, "", []string{"no process runs `bash`'s command"}},
		{"a call inside the agent", taskCall, agentPS, "", []string{"runs inside the agent: no separate process is expected"}},
		{"exec failing", taskCall, nil, "host unreachable", []string{"Processes: could not be read", "host unreachable"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			w.hangingReviews(c.call)
			w.lux.SetPS(c.ps, c.fails)
			wi := w.task()
			w.deliver(wi)
			runs := w.reviewersOpen(wi, 2)
			mustExec(t, w.owner, `UPDATE runs SET open_tool_calls_at = '{}' WHERE id = $1`, runs[1])
			w.openSince(runs[0], 3*time.Hour)
			w.pump()
			if n := w.stalls(runs[0]); n != 1 {
				t.Fatalf("%d reports, want 1", n)
			}
			_, text := w.stallOf(runs[0])
			for _, want := range c.want {
				if !strings.Contains(text, want) {
					t.Errorf("the report lacks %q:\n%s", want, text)
				}
			}
		})
	}
}

// The facts are gathered outside any transaction. A Run that, while they
// are, closes its call and asks a person (or is otherwise no longer
// stalled) is not reported.
func TestF6StallRechecksPersonWaitProbe(t *testing.T) {
	cases := []struct {
		name, change string
		// Reports after the next sweep, which reads the facts as they are.
		then int
	}{
		{"its call closes and it asks a person", `open_tool_calls = '{}', open_tool_calls_at = '{}', waiting_since = now()`, 0},
		{"its turn ends", `turn_done_at = now()`, 0},
		{"a pause is asked for", `control = 'pause_graceful', control_requested_at = now()`, 0},
		{"another call opens", `open_tool_calls_at = open_tool_calls_at || jsonb_build_object('call_late', now() - interval '3 hours')`, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			w.quickDiffs()
			w.hangingReviews(taskCall)
			wi := w.task()
			w.deliver(wi)
			runs := w.reviewersOpen(wi, 2)
			w.diffsSettled(runs)
			mustExec(t, w.owner, `UPDATE runs SET open_tool_calls_at = '{}' WHERE id = $1`, runs[1])
			w.openSince(runs[0], 3*time.Hour)
			var changed atomic.Bool
			w.lux.SetPS(func(string) string {
				if changed.CompareAndSwap(false, true) {
					mustExec(t, w.owner, `UPDATE runs SET `+c.change+` WHERE id = $1`, runs[0])
				}
				return agentPS("")
			}, "")
			w.sweep()
			if !changed.Load() {
				t.Fatal("the report read no processes")
			}
			if n := w.stalls(runs[0]); n != 0 {
				t.Fatalf("%d reports from facts that changed while they were gathered", n)
			}
			w.sweep()
			if n := w.stalls(runs[0]); n != c.then {
				t.Errorf("%d reports after the next sweep, want %d", n, c.then)
			}
		})
	}
}

// luxFactsDown is the syncer's lux whose Exec and Get, once down, go to
// a lux client (lux.New) whose host accepts requests and never answers;
// every other call reaches the fake lux.
type luxFactsDown struct {
	lux.Client
	hung lux.Client
	down atomic.Bool
}

func (l *luxFactsDown) Exec(ctx context.Context, runID string, cmd []string) (lux.ExecResult, error) {
	if l.down.Load() {
		return l.hung.Exec(ctx, runID, cmd)
	}
	return l.Client.Exec(ctx, runID, cmd)
}

func (l *luxFactsDown) Get(ctx context.Context, runID string) (lux.Run, error) {
	if l.down.Load() {
		return l.hung.Get(ctx, runID)
	}
	return l.Client.Get(ctx, runID)
}

// quickDiffs has each Run's live diff read within moments of its start, so
// a test can wait for it (diffsSettled) instead of it landing mid-sweep.
// Before any Run starts: a follower takes the timings it starts with.
func (w *world) quickDiffs() {
	// DiffSlow too: a read that fails on a loaded host counts towards the
	// watcher's back-off, which would otherwise wait a minute between reads.
	w.syncer.DiffDelay, w.syncer.DiffEvery, w.syncer.DiffSlow = 20*time.Millisecond, 20*time.Millisecond, 20*time.Millisecond
}

// diffsSettled waits for each Run's initial live diff to be recorded and
// to stay as it is across several reads: a checksum arriving later would
// change the facts a sweep fingerprints, and its recheck would rightly
// drop the report.
func (w *world) diffsSettled(runs []string) {
	w.t.Helper()
	sums := func() string {
		var s string
		_ = w.owner.QueryRow(context.Background(), `SELECT string_agg(run_id || '=' || checksum, ',' ORDER BY run_id)
			FROM run_diffs WHERE run_id = ANY($1)`, runs).Scan(&s)
		return s
	}
	deadline := time.Now().Add(60 * time.Second)
	for {
		before := sums()
		if n := w.count(`SELECT count(*) FROM run_diffs WHERE run_id = ANY($1)`, runs); n == len(runs) {
			time.Sleep(300 * time.Millisecond)
			if sums() == before {
				return
			}
		}
		if time.Now().After(deadline) {
			w.t.Fatalf("the live diffs never settled: %s", sums())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A lux host that never answers the facts' calls, with six stalled Runs
// (a conducted task's two reviewers and two plain deliveries' two each):
// the sweep returns within the facts' budget, still submits a pending Run
// and wakes the conductor once, and each stalled Run is reported once,
// saying lux did not answer.
func TestStallFactsFromALuxThatNeverAnswersDoNotHoldUpTheSweep(t *testing.T) {
	const budget, margin = 15 * time.Second, 5 * time.Second
	w := conducting(t)
	stop := make(chan struct{})
	never := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-stop:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(never.Close)
	t.Cleanup(func() { close(stop) })
	facts := &luxFactsDown{Client: w.syncer.Lux, hung: lux.New(never.URL, "lux-key")}
	w.syncer.Lux = facts
	w.quickDiffs()

	w.hangingReviews(taskCall)
	conducted := w.task()
	w.conductedReview(conducted)
	stalled := w.reviewersOpen(conducted, 2)
	for range 2 {
		// hangingReviews counts reviewers per world: two hang, then reply.
		w.hangingReviews(taskCall)
		wi := w.task()
		w.deliver(wi)
		stalled = append(stalled, w.reviewersOpen(wi, 2)...)
	}
	w.diffsSettled(stalled)
	for _, id := range stalled {
		w.openSince(id, 3*time.Hour)
	}
	// A delivery whose implementer is pending, not yet submitted.
	pendingTask := w.task()
	w.deliver(pendingTask)
	for range 5 {
		if _, err := w.runtime.Tick(context.Background(), 10); err != nil {
			t.Fatal(err)
		}
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'implement' AND status = 'pending'
		AND lux_run_id IS NULL`, pendingTask); n != 1 {
		t.Fatalf("%d pending implementers", n)
	}

	facts.down.Store(true)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	start := time.Now()
	go func() { _, err := w.syncer.Sweep(ctx); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(budget + margin):
		t.Fatalf("the sweep still runs after %s, past the facts' budget of %s", time.Since(start).Round(time.Second), budget)
	}
	took := time.Since(start)
	facts.down.Store(false)
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'implement' AND lux_run_id IS NOT NULL`,
		pendingTask); n != 1 {
		t.Errorf("the pending implementer was not submitted in the sweep (took %s)", took)
	}
	for _, id := range stalled {
		if n := w.stalls(id); n != 1 {
			t.Errorf("run %s: %d reports, want 1", id, n)
			continue
		}
		if _, text := w.stallOf(id); text != "" && !strings.Contains(text, "lux did not answer in time") {
			t.Errorf("run %s's report does not say lux did not answer:\n%s", id, text)
		}
	}
	if n := w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1 AND kind = 'stalled'
		AND line LIKE '%lux did not answer in time%'`, conducted); n != 2 {
		t.Errorf("%d stalled wake reasons for the conducted task's two reviewers, want 2", n)
	}
	note := w.wokenWith(conducted, "made no progress")
	if !strings.Contains(note, stalled[0]) || !strings.Contains(note, stalled[1]) {
		t.Errorf("the conductor's note does not carry both reviewers:\n%s", note)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'conductor.woken'
		AND payload->>'text' LIKE '%made no progress%'`, conducted); n != 1 {
		t.Errorf("%d wakes for the conducted task's two stalled reviewers, want one", n)
	}
	// Once per window: the next sweeps say nothing more.
	w.sweep()
	for _, id := range stalled {
		if n := w.stalls(id); n != 1 {
			t.Errorf("run %s: reported again (%d)", id, n)
		}
	}
}

// luxSlowPS is the syncer's lux whose ps execs (a report's processes) each
// take delay, counting how many are in flight at once; every other call,
// the live diff's reads among them, reaches the fake lux as it is.
type luxSlowPS struct {
	lux.Client
	delay          time.Duration
	inFlight, peak atomic.Int32
	calls          atomic.Int32
}

func (l *luxSlowPS) Exec(ctx context.Context, runID string, cmd []string) (lux.ExecResult, error) {
	if len(cmd) == 0 || cmd[0] != "ps" {
		return l.Client.Exec(ctx, runID, cmd)
	}
	l.calls.Add(1)
	n := l.inFlight.Add(1)
	defer l.inFlight.Add(-1)
	for p := l.peak.Load(); n > p && !l.peak.CompareAndSwap(p, n); p = l.peak.Load() {
	}
	select {
	case <-time.After(l.delay):
	case <-ctx.Done():
		return lux.ExecResult{}, ctx.Err()
	}
	return l.Client.Exec(ctx, runID, cmd)
}

// A lux that answers, slowly, with four stalled Runs due: their facts are
// asked for several at a time, and every report carries them.
func TestStallFactsFromASlowLuxAreGatheredSeveralAtATime(t *testing.T) {
	w := newWorld(t)
	slow := &luxSlowPS{Client: w.syncer.Lux, delay: 200 * time.Millisecond}
	w.syncer.Lux = slow
	w.quickDiffs()
	var stalled []string
	for range 2 {
		w.hangingReviews(taskCall)
		wi := w.task()
		w.deliver(wi)
		stalled = append(stalled, w.reviewersOpen(wi, 2)...)
	}
	w.diffsSettled(stalled)
	for _, id := range stalled {
		w.openSince(id, 3*time.Hour)
	}
	w.sweep()
	if n := slow.calls.Load(); n != int32(len(stalled)) {
		t.Errorf("%d process reads for %d stalled Runs", n, len(stalled))
	}
	if p := slow.peak.Load(); p < 2 {
		t.Errorf("peak simultaneous process reads %d; want at least 2", p)
	}
	for _, id := range stalled {
		if n := w.stalls(id); n != 1 {
			t.Errorf("run %s: %d reports, want 1", id, n)
			continue
		}
		if _, text := w.stallOf(id); strings.Contains(text, "lux did not answer in time") ||
			!strings.Contains(text, "Processes: opencode acp") {
			t.Errorf("run %s's report lacks lux's facts:\n%s", id, text)
		}
	}
}

// A Run not held up by itself is not reported, however old its open call
// or its files: one waiting on a person, one whose turn is over, one
// paused or with a pause asked for; and a reviewer, which changes no code,
// whose files are old and whose call is not. Each beside a reviewer whose
// call is as old, which is reported in the same sweep.
func TestARunNotHeldUpByItselfIsNotReported(t *testing.T) {
	cases := []struct{ name, set string }{
		{"waiting on a person", `waiting_since = now() - interval '3 hours'`},
		{"its turn done", `turn_done_at = now() - interval '3 hours'`},
		{"paused", `status = 'paused'`},
		{"a pause asked for", `control = 'pause_graceful', control_requested_at = now()`},
		{"a reviewer whose files are old, its call not",
			`open_tool_calls_at = (SELECT jsonb_object_agg(k, to_jsonb(now())) FROM jsonb_object_keys(open_tool_calls_at) k),
			 files_changed_at = now() - interval '3 hours'`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			w.quickDiffs()
			w.hangingReviews(taskCall)
			wi := w.task()
			w.deliver(wi)
			runs := w.reviewersOpen(wi, 2)
			w.diffsSettled(runs)
			held, stalled := runs[0], runs[1]
			w.openSince(held, 3*time.Hour)
			w.openSince(stalled, 3*time.Hour)
			mustExec(t, w.owner, `UPDATE runs SET `+c.set+` WHERE id = $1`, held)
			w.sweep()
			if n := w.stalls(stalled); n != 1 {
				t.Fatalf("the reviewer beside it, stalled: %d reports, want 1", n)
			}
			if n := w.stalls(held); n != 0 {
				t.Errorf("a reviewer %s was reported (%d)", c.name, n)
			}
		})
	}
}

// Under a conductor, a Run whose facts changed is told again only once
// the window has passed since its last report.
func TestChangedFactsAreNotToldAgainBeforeTheWindow(t *testing.T) {
	w := conducting(t)
	w.quickDiffs()
	w.hangingReviews(taskCall)
	task := w.task()
	w.conductedReview(task)
	runs := w.reviewersOpen(task, 2)
	w.diffsSettled(runs)
	id := runs[0]
	mustExec(t, w.owner, `UPDATE runs SET open_tool_calls_at = '{}' WHERE id = $1`, runs[1])
	w.openSince(id, 31*time.Minute)
	w.sweep()
	if w.stalls(id) != 1 {
		t.Fatalf("%d reports, want 1", w.stalls(id))
	}
	mustExec(t, w.owner, `UPDATE runs SET open_tool_calls_at = open_tool_calls_at || jsonb_build_object('call_new', now() - interval '40 minutes')
		WHERE id = $1`, id)
	w.reportedAgo(id, 20*time.Minute)
	w.sweep()
	if n := w.stalls(id); n != 1 {
		t.Errorf("changed facts told again 20 minutes after the last report (%d reports)", n)
	}
	w.reportedAgo(id, 31*time.Minute)
	w.sweep()
	if n := w.stalls(id); n != 2 {
		t.Errorf("changed facts not told at 30 minutes (%d reports)", n)
	}
}

// A Run's diff whose checksum changed is changed facts, its open calls the
// same: told again at 30 minutes, not 60.
func TestAChangedDiffIsChangedFacts(t *testing.T) {
	w := conducting(t)
	w.quickDiffs()
	w.hangingReviews(taskCall)
	task := w.task()
	w.conductedReview(task)
	runs := w.reviewersOpen(task, 2)
	// The live reader has recorded its checksum and skips an unchanged
	// read, so the checksums written below stay until the next.
	w.diffsSettled(runs)
	id := runs[0]
	mustExec(t, w.owner, `UPDATE runs SET open_tool_calls_at = '{}' WHERE id = $1`, runs[1])
	diff := func(checksum string) {
		mustExec(t, w.owner, `INSERT INTO run_diffs (run_id, organization_id, base, checksum) VALUES ($1, $2, 'base', $3)
			ON CONFLICT (run_id) DO UPDATE SET checksum = EXCLUDED.checksum`, id, w.org, checksum)
	}
	diff("sum-1")
	w.openSince(id, 31*time.Minute)
	w.sweep()
	if w.stalls(id) != 1 {
		t.Fatalf("%d reports, want 1", w.stalls(id))
	}
	w.reportedAgo(id, 31*time.Minute)
	w.sweep()
	if n := w.stalls(id); n != 1 {
		t.Fatalf("the same facts told again at 30 minutes (%d reports)", n)
	}
	diff("sum-2")
	w.sweep()
	if n := w.stalls(id); n != 2 {
		t.Errorf("a changed diff not told at 30 minutes (%d reports)", n)
	}
}

// migrate085 applies migration 085 to the world's database as a deploy
// finds it: what it adds is dropped (every row otherwise as it is), then
// the file runs as the migrator runs it, in one transaction.
func (w *world) migrate085() {
	w.t.Helper()
	sql, err := os.ReadFile("../migrations/085_stalled_runs.sql")
	if err != nil {
		w.t.Fatal(err)
	}
	ctx := context.Background()
	tx, err := w.owner.Begin(ctx)
	if err != nil {
		w.t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DROP FUNCTION run_stalled(runs);
		ALTER TABLE runs DROP COLUMN open_tool_calls_at, DROP COLUMN files_changed_at, DROP COLUMN left_running_at, DROP COLUMN stall_reported_at,
			DROP COLUMN stall_reasons, DROP COLUMN stall_fingerprint, DROP COLUMN stall_usage, DROP COLUMN stall_left_at,
			DROP COLUMN restart_note, DROP COLUMN tier_override, DROP COLUMN replaced_by`); err != nil {
		w.t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, string(sql)); err != nil {
		w.t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		w.t.Fatal(err)
	}
}

// Two reviewers already hung in an open call when 085 deploys, which write
// nothing more: the one whose call the ledger shows open for 3 hours is
// reported; the one started 3 hours ago whose call opened just now is not.
func TestF6UpgradeHungCallProbe(t *testing.T) {
	w := newWorld(t)
	w.quickDiffs()
	w.hangingReviews(taskCall)
	wi := w.task()
	w.deliver(wi)
	runs := w.reviewersOpen(wi, 2)
	w.diffsSettled(runs)
	mustExec(t, w.owner, `UPDATE runs SET started_at = now() - interval '3 hours' WHERE id = ANY($1)`, runs)
	mustExec(t, w.owner, `UPDATE events SET occurred_at = now() - interval '3 hours' WHERE run_id = $1
		AND event_type = 'agent.tool.called'`, runs[0])
	w.migrate085()
	if n := w.count(`SELECT count(*) FROM runs WHERE id = ANY($1) AND open_tool_calls_at <> '{}' AND files_changed_at IS NOT NULL`,
		runs); n != 2 {
		t.Errorf("%d of the 2 live reviewers have their calls and files dated", n)
	}
	w.sweep()
	if a, b := w.stalls(runs[0]), w.stalls(runs[1]); a != 1 || b != 0 {
		t.Fatalf("reports: %d for the call open 3 hours (want 1), %d for the one open now (want 0)", a, b)
	}
	if stall, _ := w.stallOf(runs[0]); stall["calls"] == nil {
		t.Errorf("the report names no call: %v", stall)
	} else if calls, _ := json.Marshal(stall["calls"]); !strings.Contains(string(calls), `"openSecs":108`) {
		t.Errorf("the call is not open since its ledger event, 3 hours: %s", calls)
	}
}

// silentReviews delivers a change two reviewers read; the round's two
// reviewers start their turn and then say and do nothing, with no call
// open, as run 3's reviewer hung inside its first model call. Any later
// reviewer finds nothing.
func (w *world) silentReviews() {
	w.t.Helper()
	scripted := w.lux.Decide
	var reviews atomic.Int32
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		labels, _ := spec["labels"].(map[string]any)
		switch labels["dude.phase"] {
		case "implement":
			return fakelux.Behaviour{Commit: map[string]string{"auth/session.go": "package auth\n"}, Message: "work"}
		case "review":
			if reviews.Add(1) <= 2 {
				return fakelux.Behaviour{Hang: true}
			}
			return fakelux.Behaviour{Reply: "Looks good."}
		}
		return scripted(spec)
	}
	w.lux.SetPS(agentPS, "")
}

// reviewersSilent waits for n reviewers whose turn has started, with no
// call open, and returns them.
func (w *world) reviewersSilent(task string, n int) []string {
	w.t.Helper()
	var ids []string
	w.until("reviewers silent in their turn", func() bool {
		ids = w.ids(`SELECT id FROM runs WHERE task_id = $1 AND phase = 'review' AND status = 'running'
			AND lux_state = 'running' AND agent_active_at IS NOT NULL AND open_tool_calls_at = '{}' ORDER BY created_at`, task)
		return len(ids) == n
	})
	return ids
}

func (w *world) conductedSilentReview() (string, []string) {
	w.t.Helper()
	w.quickDiffs()
	w.silentReviews()
	task := w.task()
	w.conductedReview(task)
	runs := w.reviewersSilent(task, 2)
	w.diffsSettled(runs)
	return task, runs
}

// silentFor back-dates when a Run's agent last did anything: ago.
func (w *world) silentFor(runID string, ago time.Duration) {
	mustExec(w.t, w.owner, `UPDATE runs SET agent_active_at = now() - make_interval(secs => $2) WHERE id = $1`, runID, ago.Seconds())
}

func (w *world) reasonsOf(runID string) string {
	stall, _ := w.stallOf(runID)
	reasons, _ := json.Marshal(stall["reasons"])
	return string(reasons)
}

// A conducted task's reviewer whose agent has done nothing for 34 minutes,
// no call open: the conductor is woken once, the reason silent, with the
// processes and CPU and network that tell a hang from a long think. The
// other reviewer, active, is not reported.
func TestASilentReviewerIsReported(t *testing.T) {
	w := conducting(t)
	task, runs := w.conductedSilentReview()
	id := runs[0]
	w.lux.SetUsage(w.luxRunOf(id), lux.Usage{CPUSeconds: 2})
	w.silentFor(id, 29*time.Minute)
	w.sweep()
	if n := w.stalls(id); n != 0 {
		t.Fatalf("%d reports of a reviewer silent for 29 minutes", n)
	}
	w.silentFor(id, 34*time.Minute)
	note := w.wokenWith(task, "made no progress")
	if n := w.stalls(id); n != 1 || !strings.Contains(note, id) {
		t.Fatalf("%d reports; the note names it: %v", n, strings.Contains(note, id))
	}
	if n := w.stalls(runs[1]); n != 0 {
		t.Errorf("the active reviewer was reported (%d)", n)
	}
	if r := w.reasonsOf(id); r != `["silent"]` {
		t.Errorf("reasons %s", r)
	}
	for _, want := range []string{"review Run", "has made no progress for 30 min", "Its agent has done nothing for 34 min: no output, no tool call.",
		"Processes: opencode acp (1.2 % CPU", "CPU over", "2 s; network: 0 B", "restart_run"} {
		if !strings.Contains(note, want) {
			t.Errorf("the note lacks %q:\n%s", want, note)
		}
	}
	if strings.Contains(note, "tool call open") {
		t.Errorf("the note names an open call:\n%s", note)
	}
	if n := w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1 AND kind = 'stalled'`, task); n != 1 {
		t.Errorf("%d stalled wake reasons, want 1", n)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND run_stalled(runs)`, task); n != 1 {
		t.Errorf("%d runs read as stalled, want 1", n)
	}
	w.sweep()
	if n := w.stalls(id); n != 1 {
		t.Errorf("reported again in the next sweep (%d)", n)
	}
}

// A plain delivery's reviewer silent past its role's 2 hours: its owner is
// told once, the Run reads as stalled, and after Leave it is not told again.
func TestASilentPlainDeliveryRunIsToldToItsOwnerOnce(t *testing.T) {
	w := newWorld(t)
	w.quickDiffs()
	ana := w.person("Ana")
	w.silentReviews()
	wi := w.task()
	w.deliver(wi)
	w.assignOwner(wi, ana)
	runs := w.reviewersSilent(wi, 2)
	w.diffsSettled(runs)
	id := runs[0]
	w.silentFor(id, 31*time.Minute)
	w.sweep()
	if n := w.stalls(id); n != 0 {
		t.Fatalf("a plain delivery's owner told at 30 minutes")
	}
	w.silentFor(id, 121*time.Minute)
	w.sweep()
	if n := w.stalls(id); n != 1 {
		t.Fatalf("%d reports at 120 minutes, want 1", n)
	}
	if r := w.reasonsOf(id); r != `["silent"]` {
		t.Errorf("reasons %s", r)
	}
	if _, text := w.stallOf(id); !strings.Contains(text, "no progress for 2.0 h") ||
		!strings.Contains(text, "Its agent has done nothing for 2.0 h: no output, no tool call.") || strings.Contains(text, "restart_run") {
		t.Errorf("the owner's report: %s", text)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND run_stalled(runs)`, id); n != 1 {
		t.Errorf("the silent Run does not read as stalled")
	}
	w.reportedAgo(id, 5*time.Hour)
	w.sweep()
	if n := w.stalls(id); n != 1 {
		t.Errorf("the owner was told %d times", n)
	}
	if status, body := w.callAs(ana, "/internal/runs/"+id+"/leave", map[string]any{}); status != 200 {
		t.Fatalf("leave: %d %v", status, body)
	}
	mustExec(t, w.owner, `UPDATE runs SET stall_reported_at = NULL WHERE id = $1`, id)
	w.sweep()
	if n := w.stalls(id); n != 1 {
		t.Errorf("a Run left as it is was reported again (%d)", n)
	}
}

// A Run not silent, or not held up by itself, is not reported as silent:
// one active inside the window, waiting on a person, whose turn is done,
// paused, or with a pause asked for. Each beside a reviewer silent as
// long, which is reported in the same sweep.
func TestARunNotSilentIsNotReportedAsSilent(t *testing.T) {
	cases := []struct{ name, set string }{
		{"active inside the window", `agent_active_at = now() - interval '10 minutes'`},
		{"waiting on a person", `waiting_since = now() - interval '3 hours'`},
		{"its turn done", `turn_done_at = now() - interval '3 hours'`},
		{"paused", `status = 'paused'`},
		{"a pause asked for", `control = 'pause_graceful', control_requested_at = now()`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			w.quickDiffs()
			w.silentReviews()
			wi := w.task()
			w.deliver(wi)
			runs := w.reviewersSilent(wi, 2)
			w.diffsSettled(runs)
			held, silent := runs[0], runs[1]
			w.silentFor(held, 3*time.Hour)
			w.silentFor(silent, 3*time.Hour)
			mustExec(t, w.owner, `UPDATE runs SET `+c.set+` WHERE id = $1`, held)
			w.sweep()
			if n := w.stalls(silent); n != 1 {
				t.Fatalf("the reviewer beside it, silent: %d reports, want 1", n)
			}
			if n := w.stalls(held); n != 0 {
				t.Errorf("a reviewer %s was reported (%d)", c.name, n)
			}
		})
	}
}

// A reviewer with a call open, however long it has been silent, is the
// call's: reported for the call alone, once its call has been open for the
// window, and not before.
func TestARunWithAnOpenCallIsNeverSilent(t *testing.T) {
	w := newWorld(t)
	w.quickDiffs()
	w.hangingReviews(taskCall)
	wi := w.task()
	w.deliver(wi)
	runs := w.reviewersOpen(wi, 2)
	w.diffsSettled(runs)
	id := runs[0]
	w.silentFor(id, 5*time.Hour)
	w.openSince(id, 10*time.Minute)
	mustExec(t, w.owner, `UPDATE runs SET open_tool_calls_at = '{}' WHERE id = $1`, runs[1])
	w.silentFor(runs[1], 10*time.Minute)
	w.sweep()
	if n := w.stalls(id); n != 0 {
		t.Fatalf("a reviewer whose call opened 10 minutes ago was reported (%d)", n)
	}
	w.openSince(id, 3*time.Hour)
	w.sweep()
	if n := w.stalls(id); n != 1 {
		t.Fatalf("%d reports, want 1", n)
	}
	if r := w.reasonsOf(id); r != `["call"]` {
		t.Errorf("reasons %s, want only the call", r)
	}
	if _, text := w.stallOf(id); strings.Contains(text, "done nothing") {
		t.Errorf("the report calls it silent:\n%s", text)
	}
}

// A silent reviewer that becomes active after its report no longer reads
// as stalled, and is not told again; silent for a window again, it is told
// again at 30 minutes, its facts changed.
func TestASilentRunThatComesBackIsNotToldAgainUntilSilentAgain(t *testing.T) {
	w := conducting(t)
	_, runs := w.conductedSilentReview()
	id := runs[0]
	w.silentFor(id, 31*time.Minute)
	w.sweep()
	if n := w.stalls(id); n != 1 {
		t.Fatalf("%d reports, want 1", n)
	}
	stalled := func() bool { return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND run_stalled(runs)`, id) == 1 }
	if !stalled() {
		t.Fatal("the silent Run does not read as stalled")
	}
	// Active 5 minutes ago, after its report 40 minutes ago.
	w.reportedAgo(id, 40*time.Minute)
	w.silentFor(id, 5*time.Minute)
	if stalled() {
		t.Error("a Run active since its report still reads as stalled")
	}
	w.sweep()
	if n := w.stalls(id); n != 1 {
		t.Errorf("a Run active 5 minutes ago was told again (%d reports)", n)
	}
	// Silent again for the window since that activity, still after the
	// report: changed facts, told again before the 60 minutes.
	w.silentFor(id, 31*time.Minute)
	w.sweep()
	if n := w.stalls(id); n != 2 {
		t.Errorf("a Run silent again for the window was not told again (%d reports)", n)
	}
	if !stalled() {
		t.Error("the Run silent again does not read as stalled")
	}
}

// The facts are gathered outside any transaction. A silent Run whose
// agent says something while they are is not reported, nor at the next
// sweep.
func TestF6SilentStallRechecksActivityProbe(t *testing.T) {
	w := newWorld(t)
	w.quickDiffs()
	w.silentReviews()
	wi := w.task()
	w.deliver(wi)
	runs := w.reviewersSilent(wi, 2)
	w.diffsSettled(runs)
	id := runs[0]
	w.silentFor(id, 3*time.Hour)
	var changed atomic.Bool
	w.lux.SetPS(func(string) string {
		if changed.CompareAndSwap(false, true) {
			mustExec(t, w.owner, `UPDATE runs SET agent_active_at = now() WHERE id = $1`, id)
		}
		return agentPS("")
	}, "")
	w.sweep()
	if !changed.Load() {
		t.Fatal("the report read no processes")
	}
	if n := w.stalls(id); n != 0 {
		t.Fatalf("%d reports of a Run active while its facts were gathered", n)
	}
	w.sweep()
	if n := w.stalls(id); n != 0 {
		t.Errorf("%d reports after the next sweep, want 0", n)
	}
}

// An agent that never said or did anything, its Run running for 3 hours:
// silent since the Run went running, and reported.
func TestARunWhoseAgentNeverDidAnythingIsReported(t *testing.T) {
	w := newWorld(t)
	w.quickDiffs()
	w.silentReviews()
	wi := w.task()
	w.deliver(wi)
	runs := w.reviewersSilent(wi, 2)
	w.diffsSettled(runs)
	id := runs[0]
	mustExec(t, w.owner, `UPDATE runs SET agent_active_at = NULL, started_at = now() - interval '3 hours',
		files_changed_at = now() - interval '3 hours' WHERE id = $1`, id)
	w.sweep()
	if n := w.stalls(id); n != 1 {
		t.Fatalf("%d reports, want 1", n)
	}
	if r := w.reasonsOf(id); r != `["silent"]` {
		t.Errorf("reasons %s", r)
	}
	if _, text := w.stallOf(id); !strings.Contains(text, "Its agent has done nothing for 3.0 h") {
		t.Errorf("the report: %s", text)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND run_stalled(runs)`, id); n != 1 {
		t.Errorf("the Run does not read as stalled")
	}
}

// movedAndHeld has lux move the running Run to another host, as a drain or
// an operator's migrate does (lux.Recorded: resuming, not an end), and
// holds its new placement before it runs until release is called. It
// returns once dude has recorded the Run resuming.
func (w *world) movedAndHeld(runID string) (release func()) {
	w.t.Helper()
	luxID := w.luxRunOf(runID)
	gate := make(chan struct{})
	var once sync.Once
	release = func() {
		once.Do(func() {
			w.lux.BeforeStart(nil)
			close(gate)
		})
	}
	w.t.Cleanup(release)
	w.lux.BeforeStart(func(id string) {
		if id == luxID {
			<-gate
		}
	})
	w.lux.Migrate(luxID)
	w.until("the Run resuming on another host", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND lux_state = 'resuming'`, runID) == 1
	})
	return release
}

// runningAgain releases a held placement and waits for dude to record the
// Run running on it.
func (w *world) runningAgain(runID string, release func()) {
	w.t.Helper()
	release()
	w.until("the Run running again", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND lux_state = 'running'`, runID) == 1
	})
}

// A reviewer started 3 hours ago whose current placement entered running
// 10 minutes ago, its agent not yet active there: silent for 10 minutes,
// not 3 hours, and not reported.
func TestASilentRunCountsFromItsPlacementNotItsFirstStart(t *testing.T) {
	w := newWorld(t)
	w.quickDiffs()
	w.silentReviews()
	wi := w.task()
	w.deliver(wi)
	runs := w.reviewersSilent(wi, 2)
	w.diffsSettled(runs)
	id := runs[0]
	release := w.movedAndHeld(id)
	// As a resume lux accepted leaves the row (whilePaused).
	mustExec(t, w.owner, `UPDATE runs SET agent_active_at = NULL, agent_busy_at = NULL WHERE id = $1`, id)
	w.runningAgain(id, release)
	mustExec(t, w.owner, `UPDATE runs SET started_at = now() - interval '3 hours',
		files_changed_at = now() - interval '10 minutes' WHERE id = $1`, id)
	w.silentFor(runs[1], 3*time.Hour)
	w.sweep()
	if n := w.stalls(runs[1]); n != 1 {
		t.Fatalf("the reviewer beside it, silent 3 hours: %d reports, want 1", n)
	}
	if n := w.stalls(id); n != 0 {
		t.Errorf("running again 10 minutes ago, but %d reports", n)
	}
	// The same Run, its placement entered running 3 hours ago: reported.
	mustExec(t, w.owner, `UPDATE runs SET files_changed_at = now() - interval '3 hours' WHERE id = $1`, id)
	w.sweep()
	if n := w.stalls(id); n != 1 {
		t.Errorf("running again 3 hours ago, silent since: %d reports, want 1", n)
	}
}

// A reviewer resumed after a 3-hour wait for a host, started 3 hours ago,
// its agent not yet active on the new placement: its silence counts from
// when it ran again, not from the resume's acceptance nor its first start.
// Silent for the window after that, it is reported.
func TestAResumedRunIsNotSilentForItsWaitToRun(t *testing.T) {
	w := newWorld(t)
	w.quickDiffs()
	w.silentReviews()
	wi := w.task()
	w.deliver(wi)
	runs := w.reviewersSilent(wi, 2)
	w.diffsSettled(runs)
	id := runs[0]
	release := w.movedAndHeld(id)
	// As a resume lux accepted leaves the row (whilePaused): no activity,
	// no turn, files dated at the acceptance — 3 hours ago — and away from
	// running since then.
	mustExec(t, w.owner, `UPDATE runs SET agent_active_at = NULL, agent_busy_at = NULL, started_at = now() - interval '3 hours',
		files_changed_at = now() - interval '3 hours', left_running_at = now() - interval '3 hours' WHERE id = $1`, id)
	w.sweep()
	if n := w.stalls(id); n != 0 {
		t.Fatalf("%d reports while waiting for a host", n)
	}
	w.runningAgain(id, release)
	w.sweep()
	if n := w.stalls(id); n != 0 {
		t.Fatalf("%d silent reports on reaching running; the wait counted as silence", n)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND agent_active_at IS NULL`, id); n != 1 {
		t.Fatal("the resumed agent was active: the case is not the one under test")
	}
	mustExec(t, w.owner, `UPDATE runs SET files_changed_at = now() - interval '3 hours' WHERE id = $1`, id)
	w.sweep()
	if n := w.stalls(id); n != 1 {
		t.Fatalf("%d reports of a Run silent for 3 hours since it ran again, want 1", n)
	}
	if r := w.reasonsOf(id); r != `["silent"]` {
		t.Errorf("reasons %s", r)
	}
}

// hangingImplementer delivers a task whose implementer reads and then
// works on without ending its turn or changing a file, and returns it once
// its live diff has settled.
func (w *world) hangingImplementer() string {
	w.t.Helper()
	w.quickDiffs()
	w.lux.Decide = func(map[string]any) fakelux.Behaviour {
		return fakelux.Behaviour{Hang: true, Tools: []string{"todowrite", "read"}}
	}
	w.lux.SetPS(agentPS, "")
	wi := w.task()
	w.deliver(wi)
	var id string
	w.until("the implementer working", func() bool {
		_ = w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE task_id = $1 AND phase = 'implement'
			AND status = 'running' AND lux_state = 'running' AND agent_busy_at IS NOT NULL`, wi).Scan(&id)
		return id != ""
	})
	w.diffsSettled([]string{id})
	return id
}

// An implementer resumed after a 3-hour wait for a host, its files as the
// acceptance dated them: no change in its files counts from when it ran
// again. Unchanged for the window after that, it is reported for its files.
func TestAResumedImplementerIsNotReportedForItsWaitToRun(t *testing.T) {
	w := newWorld(t)
	id := w.hangingImplementer()
	release := w.movedAndHeld(id)
	// As a resume lux accepted 3 hours ago leaves the row (whilePaused).
	mustExec(t, w.owner, `UPDATE runs SET agent_busy_at = NULL, files_changed_at = now() - interval '3 hours',
		left_running_at = now() - interval '3 hours' WHERE id = $1`, id)
	w.sweep()
	if n := w.stalls(id); n != 0 {
		t.Fatalf("%d reports while waiting for a host", n)
	}
	w.runningAgain(id, release)
	// Its agent at work again since.
	w.silentFor(id, 0)
	w.sweep()
	if n := w.stalls(id); n != 0 {
		t.Fatalf("%d reports for files on reaching running; the wait counted as no change", n)
	}
	mustExec(t, w.owner, `UPDATE runs SET files_changed_at = now() - interval '3 hours' WHERE id = $1`, id)
	w.silentFor(id, 0)
	w.sweep()
	if n := w.stalls(id); n != 1 {
		t.Fatalf("%d reports of files unchanged for 3 hours since it ran again, want 1", n)
	}
	if r := w.reasonsOf(id); r != `["files"]` {
		t.Errorf("reasons %s", r)
	}
}

// The facts are gathered outside any transaction. A writer reported only
// for its files, whose agent is busy while they are, is still reported:
// its activity is not a change in what it was reported for.
func TestABusyWriterReportedForItsFilesIsStillReported(t *testing.T) {
	w := newWorld(t)
	id := w.hangingImplementer()
	mustExec(t, w.owner, `UPDATE runs SET files_changed_at = now() - interval '3 hours' WHERE id = $1`, id)
	w.silentFor(id, 10*time.Minute)
	var changed atomic.Bool
	w.lux.SetPS(func(string) string {
		if changed.CompareAndSwap(false, true) {
			mustExec(t, w.owner, `UPDATE runs SET agent_active_at = now() WHERE id = $1`, id)
		}
		return agentPS("")
	}, "")
	w.sweep()
	if !changed.Load() {
		t.Fatal("the report read no processes")
	}
	if n := w.stalls(id); n != 1 {
		t.Fatalf("busy files-only writer: %d reports, want 1", n)
	}
	if r := w.reasonsOf(id); r != `["files"]` {
		t.Errorf("reasons %s", r)
	}
}

// Silence is any phase's: a test Run and an investigator, silent for 3
// hours with no call open, are each reported as silent.
func TestASilentRunOfAnyPhaseIsReported(t *testing.T) {
	w := newWorld(t)
	w.quickDiffs()
	w.silentReviews()
	wi := w.task()
	w.deliver(wi)
	runs := w.reviewersSilent(wi, 2)
	w.diffsSettled(runs)
	phases := map[string]string{runs[0]: "test", runs[1]: "investigate"}
	for id, phase := range phases {
		mustExec(t, w.owner, `UPDATE runs SET phase = $2 WHERE id = $1`, id, phase)
		w.silentFor(id, 3*time.Hour)
	}
	w.sweep()
	for id, phase := range phases {
		if n := w.stalls(id); n != 1 {
			t.Errorf("silent %s-phase Run: %d reports, want 1", phase, n)
			continue
		}
		if r := w.reasonsOf(id); r != `["silent"]` {
			t.Errorf("%s-phase Run: reasons %s", phase, r)
		}
	}
}

// A conducted reviewer silent for 10 seconds past its 30-minute window is
// reported.
func TestASilentConductedRunIsReportedJustPastItsWindow(t *testing.T) {
	w := conducting(t)
	_, runs := w.conductedSilentReview()
	id := runs[0]
	w.silentFor(id, 30*time.Minute+10*time.Second)
	w.sweep()
	if n := w.stalls(id); n != 1 {
		t.Fatalf("silent for 30 min 10 s: %d reports, want 1", n)
	}
}

// movedAfter has lux move the Run, as movedAndHeld does, and dates its
// departure from running away ago, so the move's wait for a host is away
// long. The old turn's busy marker is cleared while it is held, so the new
// placement's first idle does not end a turn it never took.
func (w *world) movedAfter(runID string, away time.Duration) (release func()) {
	w.t.Helper()
	release = w.movedAndHeld(runID)
	w.idleWhileHeld(runID)
	mustExec(w.t, w.owner, `UPDATE runs SET left_running_at = now() - make_interval(secs => $2) WHERE id = $1`, runID, away.Seconds())
	return release
}

// idleWhileHeld clears a held Run's old busy marker, so the new
// placement's first idle does not end a turn it never took.
func (w *world) idleWhileHeld(runID string) {
	mustExec(w.t, w.owner, `UPDATE runs SET agent_busy_at = NULL WHERE id = $1`, runID)
}

// filesUnchangedFor dates a Run's last change of files ago.
func (w *world) filesUnchangedFor(runID string, ago time.Duration) {
	mustExec(w.t, w.owner, `UPDATE runs SET files_changed_at = now() - make_interval(secs => $2) WHERE id = $1`, runID, ago.Seconds())
}

// thirtyMinuteWriters sets the implementer's window to 30 minutes.
func (w *world) thirtyMinuteWriters() {
	mustExec(w.t, w.owner, `UPDATE projects SET agent_models = jsonb_set(agent_models, '{implementer,timeLimitMinutes}', '30')
		WHERE id = $1`, w.project)
}

// A writer whose files have not changed for 3 hours, moved by lux to
// another host, its agent at work throughout: the move is not a change of
// its files, and it is reported for them once running again. Its departure
// from running is the one dude stamped.
func TestAMovedWriterIsStillReportedForItsUnchangedFiles(t *testing.T) {
	w := newWorld(t)
	id := w.hangingImplementer()
	w.filesUnchangedFor(id, 3*time.Hour)
	release := w.movedAndHeld(id)
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND left_running_at IS NOT NULL`, id); n != 1 {
		t.Fatal("leaving running was not stamped")
	}
	w.idleWhileHeld(id)
	w.runningAgain(id, release)
	w.silentFor(id, 0)
	w.sweep()
	if n := w.stalls(id); n != 1 {
		t.Fatalf("writer unchanged for 3 hours across a host move: %d reports, want 1", n)
	}
	if r := w.reasonsOf(id); r != `["files"]` {
		t.Errorf("reasons %s", r)
	}
}

// A writer unchanged for 20 minutes of running, then moved with a
// 40-minute wait for a host: 20 minutes on its 30-minute window when it
// runs again, not 60 and not 0. 10 more minutes running, it is reported.
func TestAMovedWriterKeepsItsRunningTimeButNotItsWait(t *testing.T) {
	w := newWorld(t)
	w.thirtyMinuteWriters()
	id := w.hangingImplementer()
	release := w.movedAfter(id, 40*time.Minute)
	w.filesUnchangedFor(id, 60*time.Minute)
	w.runningAgain(id, release)
	w.silentFor(id, 0)
	w.sweep()
	if n := w.stalls(id); n != 0 {
		t.Fatalf("%d reports on reaching running: the wait counted as no change", n)
	}
	mustExec(t, w.owner, `UPDATE runs SET files_changed_at = files_changed_at - interval '10 minutes 10 seconds' WHERE id = $1`, id)
	w.silentFor(id, 0)
	w.sweep()
	if n := w.stalls(id); n != 1 {
		t.Fatalf("%d reports after 30 minutes of running unchanged, want 1: the running before the move was lost", n)
	}
	if r := w.reasonsOf(id); r != `["files"]` {
		t.Errorf("reasons %s", r)
	}
}

// A conducted reviewer silent for 20 minutes, then moved with a 40-minute
// wait for a host: silent 20 minutes on its 30-minute window when it runs
// again, and reported once 10 more silent minutes pass.
func TestAMovedRunsSilenceKeepsItsRunningTimeButNotItsWait(t *testing.T) {
	w := conducting(t)
	_, runs := w.conductedSilentReview()
	id := runs[0]
	release := w.movedAfter(id, 40*time.Minute)
	w.silentFor(id, 60*time.Minute)
	w.runningAgain(id, release)
	w.sweep()
	if n := w.stalls(id); n != 0 {
		t.Fatalf("%d reports on reaching running: the wait counted as silence", n)
	}
	mustExec(t, w.owner, `UPDATE runs SET agent_active_at = agent_active_at - interval '10 minutes 10 seconds' WHERE id = $1`, id)
	w.sweep()
	if n := w.stalls(id); n != 1 {
		t.Fatalf("%d reports after 30 minutes of running silent, want 1: the silence before the move was lost", n)
	}
	if r := w.reasonsOf(id); r != `["silent"]` {
		t.Errorf("reasons %s", r)
	}
}

// A conducted reviewer whose agent did something while it waited 40
// minutes for a host (its records reach dude before lux reports it
// running): silent from its arrival at the latest, so silent for its
// window 30 minutes after it, and reported.
func TestActivityBeforeAMovedRunRunsAgainCountsFromItsArrival(t *testing.T) {
	w := conducting(t)
	_, runs := w.conductedSilentReview()
	id := runs[0]
	release := w.movedAfter(id, 40*time.Minute)
	w.silentFor(id, 0)
	w.runningAgain(id, release)
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND agent_active_at <= now()`, id); n != 1 {
		t.Fatal("its last activity is dated after now")
	}
	mustExec(t, w.owner, `UPDATE runs SET agent_active_at = agent_active_at - interval '30 minutes 10 seconds' WHERE id = $1`, id)
	w.sweep()
	if n := w.stalls(id); n != 1 {
		t.Fatalf("%d reports 30 minutes after its arrival, silent since, want 1", n)
	}
}

// A conducted reviewer reported as silent and silent since: 31 running
// minutes after the report it is not told again, its facts unchanged. Nor
// after a move: reported 71 minutes ago, 40 of them waiting for a host, it
// has been running 31 minutes since, silent throughout, and a move is not
// a change of facts that would tell it again before 60.
func TestReviewAMoveDoesNotChangeSilentFacts(t *testing.T) {
	w := conducting(t)
	_, runs := w.conductedSilentReview()
	id := runs[0]
	w.silentFor(id, 2*time.Hour)
	w.sweep()
	if n := w.stalls(id); n != 1 {
		t.Fatalf("initial reports %d, want 1", n)
	}
	w.reportedAgo(id, 31*time.Minute)
	w.sweep()
	if n := w.stalls(id); n != 1 {
		t.Fatalf("unchanged silence without a move: %d reports, want 1", n)
	}
	w.reportedAgo(id, 71*time.Minute)
	w.runningAgain(id, w.movedAfter(id, 40*time.Minute))
	w.sweep()
	if n := w.stalls(id); n != 1 {
		t.Fatalf("unchanged silence after 31 running minutes since report: %d reports, want 1 until 60 minutes", n)
	}
	// Still the same silence at 60 running minutes: told again.
	w.reportedAgo(id, 60*time.Minute+10*time.Second)
	w.sweep()
	if n := w.stalls(id); n != 2 {
		t.Fatalf("unchanged silence 60 running minutes after its report: %d reports, want 2", n)
	}
}

// A conducted reviewer reported as silent 31 minutes ago, silent since,
// moved by lux on dude's own departure stamp: however short its wait, the
// move is no change of facts, and it is not told again before 60 minutes.
func TestASilentRunMovedOnItsOwnDepartureIsNotToldAgain(t *testing.T) {
	w := conducting(t)
	_, runs := w.conductedSilentReview()
	id := runs[0]
	w.silentFor(id, 2*time.Hour)
	w.sweep()
	if n := w.stalls(id); n != 1 {
		t.Fatalf("initial reports %d, want 1", n)
	}
	w.reportedAgo(id, 31*time.Minute)
	release := w.movedAndHeld(id)
	w.idleWhileHeld(id)
	w.runningAgain(id, release)
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND agent_active_at < now() - interval '119 minutes'`, id); n != 1 {
		t.Fatal("the agent was active on the new placement: the case is not the one under test")
	}
	w.sweep()
	if n := w.stalls(id); n != 1 {
		t.Fatalf("unchanged silence 31 minutes after its report, across a move: %d reports, want 1", n)
	}
}

// A conducted reviewer reported as silent, active again after its report
// and silent since, then moved with a 40-minute wait: its new silence is a
// change of facts the move keeps, and 31 running minutes into it, 55 after
// the report, it is told again.
func TestAMovedRunSilentAgainSinceItsReportIsToldAgain(t *testing.T) {
	w := conducting(t)
	_, runs := w.conductedSilentReview()
	id := runs[0]
	w.silentFor(id, 2*time.Hour)
	w.sweep()
	if n := w.stalls(id); n != 1 {
		t.Fatalf("initial reports %d, want 1", n)
	}
	w.reportedAgo(id, 95*time.Minute)
	w.silentFor(id, 71*time.Minute)
	w.runningAgain(id, w.movedAfter(id, 40*time.Minute))
	w.sweep()
	if n := w.stalls(id); n != 2 {
		t.Fatalf("silent again for 31 running minutes since activity after its report: %d reports, want 2", n)
	}
}

// A writer reported for its unchanged files, then moved by lux: still
// stalled once it runs again, as its files have not changed. Reported 10
// minutes after they last did, and moved 40 minutes later, so the move's
// wait carries its files' date past the report unless the report moves
// with it.
func TestAMovedWritersFilesReportStillStands(t *testing.T) {
	w := newWorld(t)
	id := w.hangingImplementer()
	w.filesUnchangedFor(id, 3*time.Hour)
	w.silentFor(id, 0)
	w.sweep()
	stalled := func() bool { return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND run_stalled(runs)`, id) == 1 }
	if n := w.stalls(id); n != 1 || !stalled() {
		t.Fatalf("%d reports, stalled %v: want 1, true", n, stalled())
	}
	w.reportedAgo(id, 2*time.Hour+50*time.Minute)
	w.runningAgain(id, w.movedAfter(id, 40*time.Minute))
	w.silentFor(id, 0)
	if !stalled() {
		t.Fatal("a writer reported for its files no longer reads as stalled after a host move")
	}
}

// A reviewer in an open call moved by lux: the agent's process starts
// again on the new host, and its first idle closes the old placement's
// calls. None survives into the new placement to be timed.
func TestAMovedRunsOpenCallsDoNotSurviveTheMove(t *testing.T) {
	w := newWorld(t)
	w.quickDiffs()
	w.hangingReviews(taskCall)
	wi := w.task()
	w.deliver(wi)
	runs := w.reviewersOpen(wi, 2)
	w.diffsSettled(runs)
	id := runs[0]
	w.openSince(id, 20*time.Minute)
	release := w.movedAndHeld(id)
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND open_tool_calls_at <> '{}'`, id); n != 1 {
		t.Fatal("the call closed before the new placement ran: the case is not the one under test")
	}
	w.runningAgain(id, release)
	w.until("the old placement's calls closed", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND open_tool_calls_at = '{}' AND open_tool_calls = '{}'`, id) == 1
	})
}
