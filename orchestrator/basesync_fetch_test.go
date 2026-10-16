package orchestrator_test

// An integration fixer and lux's report of its fetch: a report that comes
// after sync_base's bound, and a fixer that publishes no merge at all.

import (
	"context"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/agenttools"
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

// conflictOnFactory moves main so that it conflicts with the task's
// FACTORY.md, as another task's merge does; GitHub works the conflict out
// from git.
func (w *world) conflictOnFactory() string {
	w.t.Helper()
	return w.commitFileIn(w.gh.Repo, "main", "FACTORY.md", "base intent\n", "Another task's change")
}

// lux reports the fetch sync_base asked for only after the call's bound
// has passed, as a loaded runner does: the scripted integration fixer is
// told the fetch is not reported yet ("asked"), and, as its brief and that
// answer tell it, calls again until lux has fetched, then merges the moved
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
	w.lux.HoldSyncs = false
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

// The fake lux plays the fixer's turn as soon as the Run is submitted, and
// dude records it running only when a later pass of the phase sweep
// follows its output. Here that pass comes 6 s late, as on a loaded
// runner: sync_base refuses ("your container is not running") until it
// does, and the fixer, as told, calls again rather than merging a
// lux/<base> nobody fetched.
func TestAnIntegrationWhoseRunIsRecordedRunningLateMergesTheMovedBase(t *testing.T) {
	w := newWorld(t)
	w.toolsWithSync()
	wi := w.reviewing()
	branch := w.gh.Pull(1).Head
	base := w.conflictOnFactory()
	w.until("the integration Run submitted to lux", func() bool {
		w.sync()
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND pr_feedback::text LIKE '%Base integration%'
			AND lux_run_id IS NOT NULL`, wi) == 1
	})
	time.Sleep(6 * time.Second)
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
