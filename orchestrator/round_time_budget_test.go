package orchestrator_test

import (
	"context"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakeagent"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

// testRow is a test Run as these tests read it.
type testRow struct{ ID, LuxRun, How, Continues, Status, ReplacedBy string }

func (w *world) testRows(wi string) []testRow {
	w.t.Helper()
	rows, err := w.owner.Query(context.Background(), `SELECT id, COALESCE(
			(SELECT payload->>'luxRunId' FROM events e WHERE e.run_id = r.id AND e.event_type = 'run.lease.acquired' LIMIT 1), lux_run_id, ''),
			COALESCE(continues_how, ''), COALESCE(continues_run_id, ''), status::text, COALESCE(replaced_by, '')
		FROM runs r WHERE task_id = $1 AND phase = 'test' ORDER BY created_at, id`, wi)
	if err != nil {
		w.t.Fatal(err)
	}
	defer rows.Close()
	var out []testRow
	for rows.Next() {
		var r testRow
		if err := rows.Scan(&r.ID, &r.LuxRun, &r.How, &r.Continues, &r.Status, &r.ReplacedBy); err != nil {
			w.t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

// luxOf is the lux Run a test row runs on now, "" before it has one.
func (w *world) luxOf(id string) string {
	var lux string
	_ = w.owner.QueryRow(context.Background(), `SELECT COALESCE(lux_run_id, '') FROM runs WHERE id = $1 AND lux_state = 'running'`, id).Scan(&lux)
	return lux
}

// A re-test resumed the first tester's lux Run, and lux stopped it at its
// time limit, which its predecessor had spent most of: the round is
// replayed in its slot on a lux Run of its own, given the first tester's
// conversation, and nobody is asked. The replay is no continuation: when
// lux stops it at its time limit too, it fails (test_failed), never a
// second replay. A person's Try again then starts a tester on a lux Run of
// its own; neither timed-out lux Run is resumed again.
func TestAResumedRoundLuxTimedOutIsReplayedOnceThenTryAgainStartsFresh(t *testing.T) {
	w := resuming(t)
	w.onModel("qa_browser", fakeagent.TestFailOnceModel)
	var tests atomic.Int32
	scripted := w.lux.Decide
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		labels, _ := spec["labels"].(map[string]any)
		if labels["dude.phase"] == "test" && tests.Add(1) > 1 {
			// Every tester after the first runs until lux stops it.
			return fakelux.Behaviour{Hang: true, NeverWake: true}
		}
		return scripted(spec)
	}
	wi := w.task()
	mustExec(t, w.owner, `UPDATE tasks SET acceptance_criteria = '["it greets", "it says goodbye"]' WHERE id = $1`, wi)
	w.names(wi, w.repoID)
	owner, _, _ := w.people(wi)
	p := resumingPolicy()
	p.Test = true
	w.start(wi, p)

	var retest, luxID string
	w.until("the re-test to be running on the first tester's lux Run", func() bool {
		_ = w.owner.QueryRow(context.Background(), `SELECT id, lux_run_id FROM runs WHERE task_id = $1 AND phase = 'test'
			AND continues_how = 'resume' AND lux_state = 'running' AND lux_run_id IS NOT NULL`, wi).Scan(&retest, &luxID)
		return luxID != ""
	})
	var replay testRow
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("test rows: %+v", w.testRows(wi))
		}
	})
	// lux's state events trail: the fake's Run may not be running yet when
	// dude has it so, and Timeout stops only a running one.
	w.until("the re-test replayed in its slot", func() bool {
		w.lux.Timeout(luxID)
		rows := w.testRows(wi)
		if len(rows) < 3 {
			return false
		}
		replay = rows[2]
		return w.luxOf(replay.ID) != ""
	})
	rows := w.testRows(wi)
	first := rows[0]
	if rows[1].ID != retest || rows[1].Status != "aborted" || rows[1].ReplacedBy != replay.ID {
		t.Fatalf("the timed-out re-test: %+v, want aborted, replaced by %s", rows[1], replay.ID)
	}
	replayLux := w.luxOf(replay.ID)
	if replay.How != delivery.ContinuesReplay || replay.Continues != first.ID || replayLux == luxID {
		t.Fatalf("the replay: %+v on %s; want a replay of %s on a lux Run other than %s", replay, replayLux, first.ID, luxID)
	}
	if got := w.escalationReason(wi); got != "" {
		t.Fatalf("escalated for %q, want the round replayed with nobody asked", got)
	}

	w.until("the delivery to stop for a person", func() bool {
		w.lux.Timeout(replayLux)
		return w.taskStatus(wi) == "awaiting_input"
	})
	if got := w.escalationReason(wi); got != "test_failed" {
		t.Fatalf("escalated for %q, want test_failed", got)
	}
	if rows := w.testRows(wi); len(rows) != 3 || rows[2].Status != "failed" {
		t.Fatalf("after the replay timed out: %+v; want it failed, with no second replay", rows)
	}
	resumes := func(id string) int {
		return len(slices.DeleteFunc(w.lux.CallsOf(id), func(c string) bool { return c != "resume" }))
	}
	before, beforeReplay := resumes(luxID), resumes(replayLux)
	// Not kept: its running time is spent, so Resume is not offered.
	if status, body := w.decide(owner, wi, "resume", ""); status < 400 {
		t.Fatalf("resuming the timed-out tester: %d %v, want refused", status, body)
	}

	if status, body := w.decide(owner, wi, "retry", "Test it again."); status != 200 {
		t.Fatalf("retry: %d %v", status, body)
	}
	var fourth testRow
	w.until("a tester after Try again on lux", func() bool {
		rows := w.testRows(wi)
		if len(rows) < 4 {
			return false
		}
		fourth = rows[3]
		return w.luxOf(fourth.ID) != ""
	})
	// The latest completed round is the first tester, whose lux Run went
	// to the re-test: replayed, not taken over.
	if l := w.luxOf(fourth.ID); l == luxID || l == replayLux || fourth.How != delivery.ContinuesReplay || fourth.Continues != first.ID {
		t.Fatalf("the tester after Try again: %+v on %s; want a replay of %s on a lux Run of its own", fourth, l, first.ID)
	}
	if resumes(luxID) != before || resumes(replayLux) != beforeReplay {
		t.Error("a timed-out lux Run was resumed after Try again")
	}
	if luxTests := w.luxRunsOf("test"); len(luxTests) != 3 {
		t.Errorf("%d testers submitted to lux, want the first, the replay and the one after Try again", len(luxTests))
	}
}
