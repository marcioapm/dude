package orchestrator_test

// A phase Run that makes no progress (design: F6): dude reports the facts
// to the task's conductor or, on a plain delivery, its owner, who decides —
// leave it, steer it, restart it. Timestamps are back-dated; nothing sleeps
// for the windows.

import (
	"context"
	"encoding/json"
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
