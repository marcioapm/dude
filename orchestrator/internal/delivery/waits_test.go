package delivery

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// The conductor's waits (waits.go), over newChildWorld's task: its live
// conductor run_parent, its pull request pr_life (repo#7, head
// "unchanged", checks passing) and its delivery waiting on the pull request.

const (
	headA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	headB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func newWaitWorld(t *testing.T) *childWorld {
	w := newChildWorld(t)
	w.exec(`UPDATE pull_requests SET head_sha = $1, checks = 'pending' WHERE id = 'pr_life'`, headA)
	return w
}

// phaseRun adds a live phase Run of the task.
func (w *childWorld) phaseRun(id, phase string) {
	w.t.Helper()
	w.exec(`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, role, phase)
		VALUES ($1, $2, 'prj_life', 'wi_life', 3, 'running', 'reviewer', $3)`, id, w.org, phase)
}

func (w *childWorld) waitFor(key string, ask WaitAsk) (WaitOpened, error) {
	w.t.Helper()
	ask.CallKey = key
	var out WaitOpened
	err := w.s.DB.InOrg(w.ctx, w.org, func(tx pgx.Tx) error {
		var err error
		out, err = OpenConductorWaitTx(w.ctx, tx, w.in.Parent.Run, ask)
		return err
	})
	return out, err
}

func (w *childWorld) mustWait(key string, ask WaitAsk) WaitOpened {
	w.t.Helper()
	out, err := w.waitFor(key, ask)
	if err != nil {
		w.t.Fatal(err)
	}
	return out
}

// evaluate is one evaluator pass over the task's open waits.
func (w *childWorld) evaluate() {
	w.t.Helper()
	rows, err := w.owner.Query(w.ctx, `SELECT id FROM conductor_waits WHERE ended_at IS NULL`)
	if err != nil {
		w.t.Fatal(err)
	}
	open, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		w.t.Fatal(err)
	}
	for _, id := range open {
		if err := w.s.DB.InOrg(w.ctx, w.org, func(tx pgx.Tx) error {
			_, err := EvaluateWaitTx(w.ctx, tx, w.org, "wi_life", id)
			return err
		}); err != nil {
			w.t.Fatal(err)
		}
	}
}

// waitWakes are the wait reasons recorded: their keys.
func (w *childWorld) waitWakes() []string {
	w.t.Helper()
	rows, err := w.owner.Query(w.ctx, `SELECT key FROM conductor_wakes WHERE kind = 'wait' ORDER BY created_at, id`)
	if err != nil {
		w.t.Fatal(err)
	}
	keys, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		w.t.Fatal(err)
	}
	return keys
}

// waitEnd is how a wait ended ("" while open).
func (w *childWorld) waitEnd(id string) string {
	w.t.Helper()
	var by *string
	if err := w.owner.QueryRow(w.ctx, `SELECT ended_by FROM conductor_waits WHERE id = $1`, id).Scan(&by); err != nil {
		w.t.Fatal(err)
	}
	if by == nil {
		return ""
	}
	return *by
}

func (w *childWorld) events(typ string) int {
	w.t.Helper()
	var n int
	if err := w.owner.QueryRow(w.ctx, `SELECT count(*) FROM events WHERE event_type = $1`, typ).Scan(&n); err != nil {
		w.t.Fatal(err)
	}
	return n
}

func runsEnded(ids ...string) WaitCondition { return WaitCondition{RunsEnded: ids} }

func prChecks(pr, head string) WaitCondition {
	return WaitCondition{PRChecksFinal: &PRChecksAsk{PR: pr, Head: head}}
}

func TestWaitForRunsEndedHoldsOnceAllEnd(t *testing.T) {
	w := newWaitWorld(t)
	w.phaseRun("run_a", "review")
	w.phaseRun("run_b", "review")
	opened := w.mustWait("c1", WaitAsk{Conditions: []WaitCondition{runsEnded("run_a", "run_b")}, Deadline: "45m"})
	if opened.WaitID == "" || opened.Holds || w.events(EvConductorWaiting) != 1 {
		t.Fatalf("opened %+v, waiting events %d", opened, w.events(EvConductorWaiting))
	}
	w.exec(`UPDATE runs SET status = 'completed' WHERE id = 'run_a'`)
	w.evaluate()
	if got := w.waitWakes(); len(got) != 0 || w.waitEnd(opened.WaitID) != "" {
		t.Fatalf("one of two ended: wakes %v, ended %q", got, w.waitEnd(opened.WaitID))
	}
	w.exec(`UPDATE runs SET status = 'failed', error = 'tests failed' WHERE id = 'run_b'`)
	w.evaluate()
	if got := w.waitWakes(); len(got) != 1 || got[0] != "wait:"+opened.WaitID || w.waitEnd(opened.WaitID) != WaitHeld {
		t.Fatalf("both ended: wakes %v, ended %q", got, w.waitEnd(opened.WaitID))
	}
	if w.events(EvConductorWaitEnded) != 1 {
		t.Fatalf("wait_ended events %d", w.events(EvConductorWaitEnded))
	}
	// Worst first: the failed Run before the completed one.
	var ev waitEval
	if err := w.s.DB.InOrg(w.ctx, w.org, func(tx pgx.Tx) error {
		var err error
		ev, err = evaluateConditionsTx(w.ctx, tx, []waitCond{{Kind: condRunsEnded, Runs: []string{"run_a", "run_b"}}}, true)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(ev.items) != 2 || ev.items[0].RunID != "run_b" || ev.items[1].RunID != "run_a" {
		t.Fatalf("items %+v", ev.items)
	}
	// The wake is recorded with its digest, and the wait keeps it.
	var line, digest string
	if err := w.owner.QueryRow(w.ctx, `SELECT c.line, w.digest FROM conductor_wakes c JOIN conductor_waits w ON c.key = 'wait:' || w.id`).
		Scan(&line, &digest); err != nil || line != digest || line == "" {
		t.Fatalf("line %q digest %q: %v", line, digest, err)
	}
}

func TestWaitForFollowsARestartedRun(t *testing.T) {
	w := newWaitWorld(t)
	w.phaseRun("run_old", "implement")
	opened := w.mustWait("c1", WaitAsk{Conditions: []WaitCondition{runsEnded("run_old")}, Deadline: "1h"})
	// restart_run's swap: the old Run aborted, replaced by a new one.
	w.phaseRun("run_new", "implement")
	w.exec(`UPDATE runs SET status = 'aborted', replaced_by = 'run_new' WHERE id = 'run_old'`)
	w.evaluate()
	if got := w.waitWakes(); len(got) != 0 {
		t.Fatalf("the replaced Run's abort held the wait: %v", got)
	}
	w.exec(`UPDATE runs SET status = 'completed' WHERE id = 'run_new'`)
	w.evaluate()
	if got := w.waitWakes(); len(got) != 1 || w.waitEnd(opened.WaitID) != WaitHeld {
		t.Fatalf("the replacement's end: wakes %v", got)
	}
}

func TestWaitForPRChecksFinalHoldsOnFinalOrMovedHead(t *testing.T) {
	for _, tc := range []struct {
		name, set string
		holds     bool
	}{
		{"pending", `checks = 'pending'`, false},
		{"unknown is not final", `checks = 'unknown'`, false},
		{"passing", `checks = 'passing'`, true},
		{"failing", `checks = 'failing'`, true},
		{"head moved", `head_sha = '` + headB + `'`, true},
		{"merged", `state = 'merged'`, true},
		{"closed", `state = 'closed'`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWaitWorld(t)
			opened := w.mustWait("c1", WaitAsk{Conditions: []WaitCondition{prChecks("repo#7", "")}, Deadline: "30m"})
			w.exec(`UPDATE pull_requests SET ` + tc.set + ` WHERE id = 'pr_life'`)
			w.evaluate()
			if got := len(w.waitWakes()) == 1; got != tc.holds || (w.waitEnd(opened.WaitID) == WaitHeld) != tc.holds {
				t.Fatalf("holds %v, want %v (ended %q)", got, tc.holds, w.waitEnd(opened.WaitID))
			}
		})
	}
}

func TestWaitForPinsTheHeadItWasAskedAt(t *testing.T) {
	w := newWaitWorld(t)
	// Asked at an explicit head that is not the pull request's: the head
	// has moved from it already, so it holds at once.
	opened := w.mustWait("c1", WaitAsk{Conditions: []WaitCondition{prChecks("7", headB)}, Deadline: "30m"})
	if !opened.Holds || opened.WaitID != "" || w.count("conductor_waits") != 0 {
		t.Fatalf("opened %+v", opened)
	}
}

func TestWaitForDeadlineWakesWithWhatHeldAndWhatDidNot(t *testing.T) {
	w := newWaitWorld(t)
	w.phaseRun("run_a", "review")
	w.phaseRun("run_b", "review")
	opened := w.mustWait("c1", WaitAsk{Conditions: []WaitCondition{runsEnded("run_a"), runsEnded("run_b")}, Deadline: "2h", Note: "then decide"})
	w.exec(`UPDATE runs SET status = 'completed' WHERE id = 'run_a'`)
	w.evaluate()
	if len(w.waitWakes()) != 0 {
		t.Fatal("woken before the deadline with one of two")
	}
	w.exec(`UPDATE conductor_waits SET deadline_at = now() - interval '1 second'`)
	w.evaluate()
	if got := w.waitWakes(); len(got) != 1 || w.waitEnd(opened.WaitID) != WaitDeadline {
		t.Fatalf("deadline: wakes %v, ended %q", got, w.waitEnd(opened.WaitID))
	}
	var held, of int
	if err := w.owner.QueryRow(w.ctx, `SELECT (payload->>'held')::int, (payload->>'of')::int FROM events WHERE event_type = $1`,
		EvConductorWaitEnded).Scan(&held, &of); err != nil || held != 1 || of != 2 {
		t.Fatalf("held %d of %d: %v", held, of, err)
	}
}

func TestAWaitWithNoConditionsIsATimer(t *testing.T) {
	w := newWaitWorld(t)
	opened := w.mustWait("c1", WaitAsk{Deadline: "10m"})
	w.evaluate()
	if len(w.waitWakes()) != 0 {
		t.Fatal("a timer woke before its time")
	}
	w.exec(`UPDATE conductor_waits SET deadline_at = now() - interval '1 second'`)
	w.evaluate()
	if len(w.waitWakes()) != 1 || w.waitEnd(opened.WaitID) != WaitDeadline {
		t.Fatalf("timer: %v %q", w.waitWakes(), w.waitEnd(opened.WaitID))
	}
}

func TestWaitForAnyHoldsOnTheFirst(t *testing.T) {
	w := newWaitWorld(t)
	w.phaseRun("run_a", "review")
	opened := w.mustWait("c1", WaitAsk{Conditions: []WaitCondition{runsEnded("run_a"), prChecks("7", "")}, Wake: "any", Deadline: "1h"})
	w.evaluate()
	if len(w.waitWakes()) != 0 {
		t.Fatal("held with nothing ended")
	}
	w.exec(`UPDATE pull_requests SET checks = 'passing' WHERE id = 'pr_life'`)
	w.evaluate()
	if len(w.waitWakes()) != 1 || w.waitEnd(opened.WaitID) != WaitHeld {
		t.Fatalf("any: %v %q", w.waitWakes(), w.waitEnd(opened.WaitID))
	}
}

func TestWaitForThatAlreadyHoldsOpensNoWait(t *testing.T) {
	w := newWaitWorld(t)
	w.phaseRun("run_a", "review")
	w.exec(`UPDATE runs SET status = 'completed' WHERE id = 'run_a'`)
	first := w.mustWait("c0", WaitAsk{Deadline: "1h"})
	opened := w.mustWait("c1", WaitAsk{Conditions: []WaitCondition{runsEnded("run_a")}, Deadline: "1h"})
	if !opened.Holds || opened.Digest == "" || opened.WaitID != "" {
		t.Fatalf("opened %+v", opened)
	}
	// Nothing opened, nothing replaced, nothing to wake.
	if w.count("conductor_waits") != 1 || w.waitEnd(first.WaitID) != "" || len(w.waitWakes()) != 0 {
		t.Fatalf("waits %d, first ended %q", w.count("conductor_waits"), w.waitEnd(first.WaitID))
	}
}

func TestANewWaitReplacesTheOpenOne(t *testing.T) {
	w := newWaitWorld(t)
	w.phaseRun("run_a", "review")
	old := w.mustWait("c1", WaitAsk{Conditions: []WaitCondition{runsEnded("run_a")}, Deadline: "1h"})
	next := w.mustWait("c2", WaitAsk{Deadline: "2h"})
	if old.WaitID == next.WaitID || w.waitEnd(old.WaitID) != WaitReplaced || w.waitEnd(next.WaitID) != "" {
		t.Fatalf("old %q next %q", w.waitEnd(old.WaitID), w.waitEnd(next.WaitID))
	}
	w.exec(`UPDATE runs SET status = 'completed' WHERE id = 'run_a'`)
	w.evaluate()
	if len(w.waitWakes()) != 0 {
		t.Fatal("the replaced wait woke")
	}
}

func TestWaitForRetriedCallIsTheSameWait(t *testing.T) {
	w := newWaitWorld(t)
	first := w.mustWait("c1", WaitAsk{Deadline: "1h"})
	again := w.mustWait("c1", WaitAsk{Deadline: "1h"})
	if first.WaitID != again.WaitID || !first.DeadlineAt.Equal(again.DeadlineAt) || w.count("conductor_waits") != 1 ||
		w.events(EvConductorWaiting) != 1 {
		t.Fatalf("first %+v again %+v, waits %d", first, again, w.count("conductor_waits"))
	}
}

// wait_for says the decision the delivery waits on from the conductor at
// the call, for a new wait and for a retried call alike; none when it
// waits on none.
func TestWaitForSaysTheDecisionWaitedOn(t *testing.T) {
	w := newWaitWorld(t)
	if out := w.mustWait("c0", WaitAsk{Deadline: "1h"}); out.DecisionPoint != "" {
		t.Fatalf("no decision: %q", out.DecisionPoint)
	}
	w.exec(`UPDATE workflow_runs SET status = 'waiting', step = 'conductorDecision',
		state = state || jsonb_build_object('decision', jsonb_build_object('point', $1::text, 'policy', 'review'))
		WHERE id = 'wf_life'`, PointImplemented)
	opened := w.mustWait("c1", WaitAsk{Deadline: "1h"})
	again := w.mustWait("c1", WaitAsk{Deadline: "1h"})
	if opened.WaitID == "" || again.WaitID != opened.WaitID {
		t.Fatalf("opened %+v, again %+v", opened, again)
	}
	if opened.DecisionPoint != PointImplemented || again.DecisionPoint != PointImplemented {
		t.Fatalf("decision point: new %q, retried %q", opened.DecisionPoint, again.DecisionPoint)
	}
}

func TestWaitForRefusals(t *testing.T) {
	w := newWaitWorld(t)
	w.exec(`INSERT INTO tasks (id, organization_id, project_id, number, title, goal, status)
		VALUES ('wi_other', $1, 'prj_life', 2, 'Other', 'Other', 'running')`, w.org)
	w.exec(`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, role, phase)
		VALUES ('run_other', $1, 'prj_life', 'wi_other', 1, 'running', 'reviewer', 'review')`, w.org)
	w.exec(`INSERT INTO pull_requests (id, organization_id, project_id, task_id, repository_id, run_id, number, url,
		head_branch, base_branch, head_sha, title, checks, review)
		VALUES ('pr_other', $1, 'prj_life', 'wi_other', 'repo_life', 'run_other', 8, 'https://github.com/test/repo/pull/8',
		'other', 'main', $2, 'PR', 'pending', 'pending')`, w.org, headA)
	nine := make([]WaitCondition, 9)
	for i := range nine {
		nine[i] = prChecks("7", "")
	}
	for name, ask := range map[string]WaitAsk{
		"no deadline":             {},
		"unreadable deadline":     {Deadline: "soon"},
		"deadline under a minute": {Deadline: "30s"},
		"deadline over a day":     {Deadline: "25h"},
		"deadline in the past":    {Deadline: time.Now().Add(-time.Hour).Format(time.RFC3339)},
		"wake neither":            {Deadline: "1h", Wake: "some"},
		"too many conditions":     {Deadline: "1h", Conditions: nine},
		"empty condition":         {Deadline: "1h", Conditions: []WaitCondition{{}}},
		"two kinds in one":        {Deadline: "1h", Conditions: []WaitCondition{{RunsEnded: []string{"run_parent"}, PRChecksFinal: &PRChecksAsk{PR: "7"}}}},
		"too many runs":           {Deadline: "1h", Conditions: []WaitCondition{runsEnded("a", "b", "c", "d", "e", "f", "g", "h", "i")}},
		"another task's run":      {Deadline: "1h", Conditions: []WaitCondition{runsEnded("run_other")}},
		"another task's pr":       {Deadline: "1h", Conditions: []WaitCondition{prChecks("8", "")}},
		"short head":              {Deadline: "1h", Conditions: []WaitCondition{prChecks("7", "abc1234")}},
		"long note":               {Deadline: "1h", Note: string(make([]byte, 501))},
	} {
		if _, err := w.waitFor("c-"+name, ask); !IsRefusal(err) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if w.count("conductor_waits") != 0 || w.events(EvConductorWaiting) != 0 {
		t.Fatal("a refused call wrote")
	}
}

func TestASupersededConductorOpensNoWait(t *testing.T) {
	w := newWaitWorld(t)
	w.exec(`UPDATE runs SET status = 'completed' WHERE id = 'run_parent'`)
	if _, err := w.waitFor("c1", WaitAsk{Deadline: "1h"}); !IsRefusal(err) || w.count("conductor_waits") != 0 {
		t.Fatalf("superseded: %v", err)
	}
}

func TestEvaluatingTwiceRecordsOneWake(t *testing.T) {
	w := newWaitWorld(t)
	opened := w.mustWait("c1", WaitAsk{Deadline: "1h"})
	w.exec(`UPDATE conductor_waits SET deadline_at = now() - interval '1 second'`)
	// A crash before the commit: nothing recorded, the wait still open.
	crash := errors.New("crash")
	if err := w.s.DB.InOrg(w.ctx, w.org, func(tx pgx.Tx) error {
		if ended, err := EvaluateWaitTx(w.ctx, tx, w.org, "wi_life", opened.WaitID); err != nil || !ended {
			t.Fatalf("ended %v: %v", ended, err)
		}
		return crash
	}); !errors.Is(err, crash) {
		t.Fatal(err)
	}
	if len(w.waitWakes()) != 0 || w.waitEnd(opened.WaitID) != "" {
		t.Fatal("a rolled back evaluation left a wake or an end")
	}
	for range 2 {
		if err := w.s.DB.InOrg(w.ctx, w.org, func(tx pgx.Tx) error {
			_, err := EvaluateWaitTx(w.ctx, tx, w.org, "wi_life", opened.WaitID)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	if len(w.waitWakes()) != 1 || w.events(EvConductorWaitEnded) != 1 {
		t.Fatalf("wakes %v, ended events %d", w.waitWakes(), w.events(EvConductorWaitEnded))
	}
}

func TestTwoEvaluatorsRacingOnOneWaitRecordOneWake(t *testing.T) {
	w := newWaitWorld(t)
	opened := w.mustWait("c1", WaitAsk{Deadline: "1h"})
	w.exec(`UPDATE conductor_waits SET deadline_at = now() - interval '1 second'`)
	var barrier sync.WaitGroup
	barrier.Add(2)
	errs := make(chan error, 2)
	ended := make(chan bool, 2)
	for range 2 {
		go func() {
			errs <- w.s.DB.InOrg(w.ctx, w.org, func(tx pgx.Tx) error {
				// Both transactions are open before either evaluates.
				if _, err := tx.Exec(w.ctx, `SELECT 1`); err != nil {
					return err
				}
				barrier.Done()
				barrier.Wait()
				e, err := EvaluateWaitTx(w.ctx, tx, w.org, "wi_life", opened.WaitID)
				ended <- e
				return err
			})
		}()
	}
	wins := 0
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
		if <-ended {
			wins++
		}
	}
	if wins != 1 || len(w.waitWakes()) != 1 || w.events(EvConductorWaitEnded) != 1 {
		t.Fatalf("wins %d, wakes %v, ended events %d", wins, w.waitWakes(), w.events(EvConductorWaitEnded))
	}
}

// An open wait that neither holds nor is due is read, not locked: its
// evaluation does not wait on the task's Chat lock or on its row.
func TestEvaluatingAnIdleWaitTakesNoLock(t *testing.T) {
	w := newWaitWorld(t)
	w.phaseRun("run_a", "review")
	opened := w.mustWait("c1", WaitAsk{Conditions: []WaitCondition{runsEnded("run_a")}, Deadline: "1h"})
	holder, err := w.owner.Begin(w.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Rollback(w.ctx) }()
	if err := LockChat(w.ctx, holder, "wi_life"); err != nil {
		t.Fatal(err)
	}
	if _, err := holder.Exec(w.ctx, `SELECT 1 FROM conductor_waits WHERE id = $1 FOR UPDATE`, opened.WaitID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(w.ctx, 3*time.Second)
	defer cancel()
	if err := w.s.DB.InOrg(ctx, w.org, func(tx pgx.Tx) error {
		ended, err := EvaluateWaitTx(ctx, tx, w.org, "wi_life", opened.WaitID)
		if ended {
			t.Error("an idle wait ended")
		}
		return err
	}); err != nil {
		t.Fatalf("an idle evaluation waited on a lock: %v", err)
	}
}

// "Meanwhile" counts people's Chat messages, a signed-in person's and an
// API key's alike, and pull request comments; an agent's message is not
// counted.
func TestAWaitsDigestCountsWhatPeopleDidMeanwhile(t *testing.T) {
	w := newWaitWorld(t)
	opened := w.mustWait("c1", WaitAsk{Deadline: "1h"})
	ref := RunRef{Org: w.org, ProjectID: "prj_life", TaskID: "wi_life", RunID: "run_parent"}
	if err := w.s.DB.InOrg(w.ctx, w.org, func(tx pgx.Tx) error {
		for _, e := range []struct{ typ, actor string }{
			{EvChatMessage, "person"}, {EvChatMessage, ledger.ActorHuman}, {EvChatMessage, ledger.ActorAgent},
			{EvPullRequestCommented, ledger.ActorIntegration},
		} {
			if _, err := ledger.Append(w.ctx, tx, ref.Event(e.typ, e.actor, map[string]any{"text": "x"})); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	w.exec(`UPDATE conductor_waits SET deadline_at = now() - interval '1 second'`)
	w.evaluate()
	var digest string
	if err := w.owner.QueryRow(w.ctx, `SELECT COALESCE(digest, '') FROM conductor_waits WHERE id = $1`, opened.WaitID).Scan(&digest); err != nil {
		t.Fatal(err)
	}
	want := "Your wait " + opened.WaitID + " reached its deadline: it was a timer. Meanwhile: 1 pull request comment, 2 messages from people in Chat."
	if digest != want {
		t.Fatalf("digest\n %q\nwant\n %q", digest, want)
	}
}

func TestEndWakesEndsOpenWaits(t *testing.T) {
	w := newWaitWorld(t)
	opened := w.mustWait("c1", WaitAsk{Deadline: "1h"})
	if err := w.s.DB.InOrg(w.ctx, w.org, func(tx pgx.Tx) error { return EndWakesTx(w.ctx, tx, "wi_life") }); err != nil {
		t.Fatal(err)
	}
	if w.waitEnd(opened.WaitID) != WaitTaskEnded {
		t.Fatalf("ended %q", w.waitEnd(opened.WaitID))
	}
	w.exec(`UPDATE conductor_waits SET deadline_at = now() - interval '1 second'`)
	w.evaluate()
	if len(w.waitWakes()) != 0 {
		t.Fatal("an ended wait woke")
	}
}

func TestAWaitOnAClosedTaskEndsWithNoWake(t *testing.T) {
	w := newWaitWorld(t)
	opened := w.mustWait("c1", WaitAsk{Deadline: "1h"})
	w.exec(`UPDATE tasks SET status = 'done' WHERE id = 'wi_life'`)
	w.exec(`UPDATE conductor_waits SET deadline_at = now() - interval '1 second'`)
	w.evaluate()
	if len(w.waitWakes()) != 0 || w.waitEnd(opened.WaitID) != WaitTaskEnded {
		t.Fatalf("wakes %v ended %q", w.waitWakes(), w.waitEnd(opened.WaitID))
	}
}

func TestAWaitOnAStoppedTaskStaysForThePickUp(t *testing.T) {
	w := newWaitWorld(t)
	opened := w.mustWait("c1", WaitAsk{Deadline: "1h"})
	w.exec(`UPDATE tasks SET status = 'aborted' WHERE id = 'wi_life'`)
	w.exec(`UPDATE workflow_runs SET state = state || '{"halt":{"conductor":"run_parent"}}' WHERE id = 'wf_life'`)
	w.exec(`UPDATE conductor_waits SET deadline_at = now() - interval '1 second'`)
	w.evaluate()
	// Its reason is recorded; WakeConductorTx holds it until the pick-up.
	if len(w.waitWakes()) != 1 || w.waitEnd(opened.WaitID) != WaitDeadline {
		t.Fatalf("wakes %v ended %q", w.waitWakes(), w.waitEnd(opened.WaitID))
	}
	var woken string
	if err := w.s.DB.InOrg(w.ctx, w.org, func(tx pgx.Tx) error {
		if err := LockChat(w.ctx, tx, "wi_life"); err != nil {
			return err
		}
		var err error
		woken, err = WakeConductorTx(context.Background(), tx, w.org, "wi_life", 0)
		return err
	}); err != nil || woken != "" {
		t.Fatalf("woken %q: %v", woken, err)
	}
}
