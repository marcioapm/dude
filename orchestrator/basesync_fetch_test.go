package orchestrator_test

// An integration fixer and lux's report of its fetch: a report that comes
// after sync_base's bound, and a fixer that publishes no merge at all.

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/agenttools"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

// syncBaseAnswers are the statuses sync_base answered a task's integration
// Runs, in order.
func (w *world) syncBaseAnswers(wi string) []string {
	w.t.Helper()
	rows, err := w.owner.Query(context.Background(), `SELECT e.payload->'result'->'repositories'->0->>'status'
		FROM events e JOIN runs r ON r.id = e.run_id
		WHERE r.task_id = $1 AND r.pr_feedback::text LIKE '%Base integration%'
		  AND e.event_type = 'agent.tool.dude' AND e.payload->>'tool' = 'sync_base' ORDER BY e.cursor`, wi)
	if err != nil {
		w.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			w.t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

// syncBaseNext is what sync_base's first answer of status told a task's
// integration Run to do next.
func (w *world) syncBaseNext(wi, status string) string {
	w.t.Helper()
	var next string
	if err := w.owner.QueryRow(context.Background(), `SELECT e.payload->'result'->'repositories'->0->>'next'
		FROM events e JOIN runs r ON r.id = e.run_id
		WHERE r.task_id = $1 AND r.pr_feedback::text LIKE '%Base integration%' AND e.event_type = 'agent.tool.dude'
		  AND e.payload->>'tool' = 'sync_base' AND e.payload->'result'->'repositories'->0->>'status' = $2
		ORDER BY e.cursor LIMIT 1`, wi, status).Scan(&next); err != nil {
		w.t.Fatal(err)
	}
	return next
}

// conflictOnFactory moves main so that it conflicts with the task's
// FACTORY.md, as another task's merge does; GitHub works the conflict out
// from git.
func (w *world) conflictOnFactory() string {
	w.t.Helper()
	return w.commitFileIn(w.gh.Repo, "main", "FACTORY.md", "base intent\n", "Another task's change")
}

// syncBaseLine is the first repository's line of a sync_base answer.
func syncBaseLine(t *testing.T, body string) agenttools.BaseSync {
	t.Helper()
	var out struct{ Repositories []agenttools.BaseSync }
	if err := json.Unmarshal([]byte(body), &out); err != nil || len(out.Repositories) != 1 {
		t.Fatalf("sync_base answered %s (%v)", body, err)
	}
	return out.Repositories[0]
}

// Every report comes after its own call's bound, as on a slow but healthy
// lux. A later call counts the fetch an earlier call asked for once lux
// has reported it, and of several such reports the newest: the base as
// lux last fetched it, not as it first did.
func TestSyncBaseCountsTheNewestReportOfAnEarlierCall(t *testing.T) {
	w := newWorld(t)
	w.lux.HoldSyncs = true
	tools := httptest.NewServer((&agenttools.Server{DB: w.app, Log: quiet, Lux: w.syncer.Lux,
		SyncBase: w.syncer.SyncBase, SyncBaseWait: 50 * time.Millisecond}).Handler())
	t.Cleanup(tools.Close)
	w.syncer.Agent.ToolsURL = tools.URL
	w.implementerAtWork()
	spec := string(w.lux.Runs()[0].Spec)
	if _, err := w.lux.Checkout(w.lux.Runs()[0].ID, "target"); err != nil {
		t.Fatal(err)
	}
	call := func() agenttools.BaseSync {
		t.Helper()
		status, body := w.callTool(tools.URL, spec, "sync_base", `{}`)
		if status != 200 {
			t.Fatalf("sync_base: %d %s", status, body)
		}
		return syncBaseLine(t, body)
	}
	w.gh.AdvanceBase("main", "Someone else's work")
	if got := call(); got.Status != "asked" {
		t.Fatalf("first call, nothing reported: %+v", got)
	}
	w.lux.ReleaseSyncs()
	w.gh.AdvanceBase("main", "More of someone else's work")
	if got := call(); got.Status != "fetched" || got.Behind != 1 {
		t.Fatalf("second call, the first call's fetch reported: %+v, want fetched 1 behind", got)
	}
	w.lux.ReleaseSyncs()
	if got := call(); got.Status != "fetched" || got.Behind != 2 {
		t.Fatalf("third call, both earlier fetches reported: %+v, want the newer, fetched 2 behind", got)
	}
}

// lux reports every fetch sync_base asks for only after that call's bound
// has passed, as a slow but healthy runner does: the scripted integration
// fixer is told the fetch is not reported yet ("asked") and calls again.
// The reports held so far are released once, after the first "asked";
// every later call's own report stays held. A later call answers fetched
// from an earlier call's released report, and the fixer merges the moved
// base and publishes the merge.
func TestAnIntegrationWhoseFetchIsReportedLateMergesTheMovedBase(t *testing.T) {
	w := newWorld(t)
	tools := httptest.NewServer((&agenttools.Server{DB: w.app, Log: quiet, Lux: w.syncer.Lux,
		SyncBase: w.syncer.SyncBase, SyncBaseWait: 50 * time.Millisecond}).Handler())
	t.Cleanup(tools.Close)
	w.syncer.Agent.ToolsURL, w.syncer.Agent.ToolsService = tools.URL, true
	wi := w.reviewing()
	branch := w.gh.Pull(1).Head
	w.lux.HoldSyncs = true
	base := w.conflictOnFactory()
	w.until("sync_base answered that the fetch is not reported yet", func() bool {
		w.sync()
		if r := w.escalationReason(wi); r != "" {
			t.Fatalf("escalated %s before lux reported the fetch; sync_base answered %v", r, w.syncBaseAnswers(wi))
		}
		return slices.Contains(w.syncBaseAnswers(wi), "asked")
	})
	// The fixer is told the way out its brief gives: the recorded head on
	// lux/main, or finishing without a merge.
	next := w.syncBaseNext(wi, "asked")
	if !strings.Contains(next, "git merge-base --is-ancestor "+base+" lux/main") ||
		!strings.Contains(next, "finish without merging") {
		t.Fatalf("an integration's pending answer: %s", next)
	}
	var brief string
	if err := w.owner.QueryRow(context.Background(), `SELECT pr_feedback::text FROM runs WHERE task_id = $1
		AND pr_feedback::text LIKE '%Base integration%'`, wi).Scan(&brief); err != nil {
		t.Fatal(err)
	}
	if _, when, _ := strings.Cut(next, "call sync_base again in a moment. "); when == "" ||
		!strings.Contains(brief, strings.SplitN(when, ".", 2)[0]) {
		t.Fatalf("the brief and sync_base word the merge differently:\nbrief %s\nnext %s", brief, next)
	}
	w.lux.ReleaseSyncs()
	w.until("the base merged into the task branch", func() bool {
		w.sync()
		if r := w.escalationReason(wi); r != "" {
			raw, _ := w.escalationOf(wi)
			t.Fatalf("escalated %s %s; sync_base answered %v; branch log %v", r, raw, w.syncBaseAnswers(wi), w.gh.Log(branch))
		}
		return gitOut(t, w.gh.Repo, "rev-list", "--count", branch+".."+base) == "0"
	})
	if log := w.gh.Log(branch); !slices.ContainsFunc(log, func(m string) bool {
		return strings.HasPrefix(m, "Merge remote-tracking branch 'lux/main'")
	}) {
		t.Fatalf("no merge of lux/main on %s: %v", branch, log)
	}
	if answers := w.syncBaseAnswers(wi); answers[len(answers)-1] != "fetched" {
		t.Fatalf("sync_base answered %v, want the last fetched", answers)
	}
	if n := w.count(integrationRuns, wi); n != 1 {
		t.Fatalf("integration Runs %d", n)
	}
}

// lux never reports the fixer's fetch within its bound, and the checkout's
// lux/main holds an older base that conflicts as well (an earlier fetch).
// The scripted fixer merges nothing once its bound runs out: the Run
// publishes nothing and escalates no_merge, with no merge on the branch.
func TestAnIntegrationWhoseFetchNeverLandsPublishesNoMerge(t *testing.T) {
	w := newWorld(t)
	tools := httptest.NewServer((&agenttools.Server{DB: w.app, Log: quiet, Lux: w.syncer.Lux,
		SyncBase: w.syncer.SyncBase, SyncBaseWait: 50 * time.Millisecond}).Handler())
	t.Cleanup(tools.Close)
	w.syncer.Agent.ToolsURL, w.syncer.Agent.ToolsService = tools.URL, true
	w.lux.SyncBaseRetryFor = 5 * time.Second
	wi := w.reviewing()
	branch := w.gh.Pull(1).Head
	before := w.gh.SHA(branch)
	w.lux.HoldSyncs = true
	stale := w.conflictOnFactory()
	w.commitFileIn(w.gh.Repo, "main", "FACTORY.md", "later base intent\n", "Yet another task's change")
	w.until("sync_base answered that the fetch is not reported yet", func() bool {
		w.sync()
		return slices.Contains(w.syncBaseAnswers(wi), "asked")
	})
	// While the fixer calls again (the fake releases its lock between
	// calls): its checkout's lux/main at the earlier base.
	var fixer string
	if err := w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE task_id = $1
		AND pr_feedback::text LIKE '%Base integration%'`, wi).Scan(&fixer); err != nil {
		t.Fatal(err)
	}
	work, err := w.lux.Checkout(w.luxRunOf(fixer), "target")
	if err != nil {
		t.Fatal(err)
	}
	gitOut(t, work, "update-ref", "refs/remotes/lux/main", stale)
	w.until("the integration escalation", func() bool { w.sync(); return w.escalationReason(wi) == "pull_request_conflict" })
	raw, _ := w.escalationOf(wi)
	if !strings.Contains(string(raw), `"cause": "no_merge"`) {
		t.Fatalf("escalation %s; sync_base answered %v; branch log %v", raw, w.syncBaseAnswers(wi), w.gh.Log(branch))
	}
	if head := w.gh.SHA(branch); head != before {
		t.Fatalf("the branch moved to %s: %v", head, w.gh.Log(branch))
	}
	if answers := w.syncBaseAnswers(wi); len(answers) < 2 || slices.ContainsFunc(answers, func(s string) bool { return s != "asked" }) {
		t.Fatalf("sync_base answered %v, want only asked, called again within the bound", answers)
	}
}

// The fake lux plays the fixer's turn as soon as the Run is submitted, and
// dude records it running only when a later pass of the phase sweep
// follows its output. Here no pass comes until the fixer has called
// sync_base again, as on a loaded runner: sync_base refuses ("your
// container is not running") until the Run is recorded running, and the
// fixer, as told, calls again rather than merging a lux/<base> nobody
// fetched.
func TestAnIntegrationWhoseRunIsRecordedRunningLateMergesTheMovedBase(t *testing.T) {
	w := newWorld(t)
	var calls atomic.Int32
	tools := httptest.NewServer((&agenttools.Server{DB: w.app, Log: quiet, Lux: w.syncer.Lux, SyncBase: w.syncer.SyncBase,
		BeforeCall: func(tool string) {
			if tool == "sync_base" {
				calls.Add(1)
			}
		}}).Handler())
	t.Cleanup(tools.Close)
	w.syncer.Agent.ToolsURL, w.syncer.Agent.ToolsService = tools.URL, true
	wi := w.reviewing()
	branch := w.gh.Pull(1).Head
	base := w.conflictOnFactory()
	w.until("the integration Run submitted to lux", func() bool {
		w.sync()
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND pr_feedback::text LIKE '%Base integration%'
			AND lux_run_id IS NOT NULL`, wi) == 1
	})
	// No pump, so no sweep: the fixer's first call is refused, and it calls
	// again.
	for deadline := time.Now().Add(20 * time.Second); calls.Load() < 2; time.Sleep(untilPoll) {
		if time.Now().After(deadline) {
			t.Fatalf("sync_base called %d times while the Run was not recorded running, want a call again", calls.Load())
		}
	}
	w.until("the base merged into the task branch", func() bool {
		w.sync()
		if r := w.escalationReason(wi); r != "" {
			raw, _ := w.escalationOf(wi)
			t.Fatalf("escalated %s %s; sync_base answered %v; branch log %v", r, raw, w.syncBaseAnswers(wi), w.gh.Log(branch))
		}
		return gitOut(t, w.gh.Repo, "rev-list", "--count", branch+".."+base) == "0"
	})
	if answers := w.syncBaseAnswers(wi); len(answers) != 1 || answers[0] != "fetched" {
		t.Fatalf("sync_base answered %v, want one fetch", answers)
	}
}

// An integration Run that finishes without publishing anything to the
// conflicting repository merged nothing: it escalates at once as such,
// with retry offered, and no review is spent on a merge that is not there.
func TestAnIntegrationRunThatPublishesNoMergeEscalatesWithoutAReview(t *testing.T) {
	w := newWorld(t)
	w.toolsWithSync()
	wi := w.reviewing()
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		labels, _ := spec["labels"].(map[string]any)
		if labels["dude.integration"] == "true" {
			return fakelux.Behaviour{Reply: "I looked; nothing to merge."}
		}
		return fakelux.Behaviour{Reply: "No findings."}
	}
	w.conflictOnFactory()
	w.until("the integration escalation", func() bool { w.sync(); return w.escalationReason(wi) == "pull_request_conflict" })
	raw, e := w.escalationOf(wi)
	if !strings.Contains(string(raw), `"cause": "no_merge"`) || strings.Join(e.Actions(), " ") != "retry wait stop" {
		t.Fatalf("escalation %s, actions %v", raw, e.Actions())
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id=$1 AND phase='review' AND pr_feedback::text LIKE '%remerge-diff%'`, wi); n != 0 {
		t.Fatalf("%d reviews of a merge that was never published", n)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id=$1 AND event_type='pull_request.action'
		AND payload->>'action'='integration-refused' AND payload->>'cause'='no_merge'`, wi); n != 1 {
		t.Fatalf("integration-refused lines %d", n)
	}
	if code, body := w.decide(w.person("owner"), wi, "retry", ""); code != 200 {
		t.Fatal(code, body)
	}
	w.until("a second integration Run", func() bool { w.sync(); return w.count(integrationRuns, wi) == 2 })
}

// An integration Run that publishes to another repository of the task (one
// with no pull request) but not to the conflicting one is no merge; what it
// pushed is still where that repository's work stands, so the next Run
// starts from it.
func TestANoMergeIntegrationKeepsWhatItPublishedElsewhere(t *testing.T) {
	w := newWorld(t)
	w.toolsWithSync()
	scripted := w.lux.Decide
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		b := scripted(spec)
		labels, _ := spec["labels"].(map[string]any)
		switch {
		case labels["dude.integration"] == "true":
			b.CallTools, b.Commit, b.Reply = nil, map[string]string{"web:NOTE.md": "note\n"}, "Touched web only."
		case labels["dude.phase"] == "implement":
			b.Commit = map[string]string{"target:FACTORY.md": "task intent\n"}
		}
		return b
	}
	wi := w.task()
	w.addWeb(wi, "write")
	w.deliver(wi)
	w.until("review", func() bool { return len(w.gh.Pulls()) == 1 && w.taskStatus(wi) == "review" })
	if n := len(w.web.Pulls()); n != 0 {
		t.Fatalf("web pull requests %d, want none", n)
	}
	w.conflictOnFactory()
	w.until("the integration escalation", func() bool { w.sync(); return w.escalationReason(wi) == "pull_request_conflict" })
	raw, _ := w.escalationOf(wi)
	if !strings.Contains(string(raw), `"cause": "no_merge"`) {
		t.Fatalf("escalation %s", raw)
	}
	pushed := w.web.SHA(delivery.BranchFor(wi, 1))
	if pushed == "" {
		t.Fatal("the integration Run pushed nothing to web")
	}
	var head string
	if err := w.owner.QueryRow(context.Background(), `SELECT COALESCE(state->'heads'->>'web', '') FROM workflow_runs
		WHERE task_id = $1`, wi).Scan(&head); err != nil {
		t.Fatal(err)
	}
	if head != pushed {
		t.Fatalf("the delivery's web head %q, want the integration Run's push %s", head, pushed)
	}
}
