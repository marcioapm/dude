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
	w.syncer.DiffDelay, w.syncer.DiffEvery = 20*time.Millisecond, 20*time.Millisecond
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
	deadline := time.Now().Add(20 * time.Second)
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
		ALTER TABLE runs DROP COLUMN open_tool_calls_at, DROP COLUMN files_changed_at, DROP COLUMN stall_reported_at,
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
