package orchestrator_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

// The review's probes of run_workflow, and the guards its mutations
// showed untested.

func (w *world) dispatchRow(id string) string {
	w.t.Helper()
	return w.str(`SELECT state || ' ' || COALESCE(run_id::text, 'none') FROM workflow_dispatches WHERE id = $1`, id)
}

func (w *world) settleDispatch(id string, read delivery.DispatchRead) {
	w.t.Helper()
	if err := w.prs.DB.InOrg(context.Background(), w.org, func(tx pgx.Tx) error {
		return delivery.SettleDispatchTx(context.Background(), tx, w.org, id, read)
	}); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) nowRFC3339() string {
	return w.str(`SELECT to_char(now() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"')`)
}

// Back-to-back staging and production dispatches of one workflow never
// bind each other's runs: with run details each is the run GitHub answered
// it with; with an older GitHub neither is bound, both unknown.
func TestReviewBackToBackDispatchesSwapRuns(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "run details", true: "204"}[legacy], func(t *testing.T) {
			w, task, branch := dispatchWorld(t)
			approve := delivery.DispatchApproval("deploy.yml", branch)
			choices := `["` + approve + `", "` + delivery.DispatchDecline + `"]`
			mustExec(t, w.owner, `INSERT INTO questions (id, organization_id, task_id, prompt, options, status, answer, answered_at, addressee)
				VALUES ('q_staging', $1, $2, 'Staging?', $3::jsonb, 'answered', $4, now(), 'person'),
				       ('q_production', $1, $2, 'Production?', $3::jsonb, 'answered', $4, now(), 'person')`, w.org, task, choices, approve)
			w.gh.Set(func(s *fakegithub.Server) { s.DispatchLegacy = legacy })
			staging := w.dispatched(task, `{"workflow":"deploy.yml","questionId":"q_staging","inputs":{"env":"staging"}}`)
			if !legacy {
				production := w.dispatched(task, `{"workflow":"deploy.yml","questionId":"q_production","inputs":{"env":"production"}}`)
				// The fake answers dispatch n with run 70000+n: staging is the
				// first dispatch, production the second.
				if got := w.dispatchRow(staging); got != "bound 43000070001" {
					t.Errorf("staging %s", got)
				}
				if got := w.dispatchRow(production); got != "bound 43000070002" {
					t.Errorf("production %s", got)
				}
				return
			}
			production := w.dispatched(task, `{"workflow":"deploy.yml","questionId":"q_production","inputs":{"env":"production"}}`)
			w.gh.SetWorkflowRun(43000000611, fakegithub.WorkflowRun{Name: "deploy.yml", Branch: branch, Event: "workflow_dispatch", Status: "queued", CreatedAt: w.nowRFC3339()})
			w.supervise()
			for _, id := range []string{staging, production} {
				if got := w.dispatchRow(id); got != "unknown none" {
					t.Errorf("%s: %s", id, got)
				}
			}
		})
	}
}

// wakesNaming is every wake of the task (its own or a wait's digest)
// whose line names the dispatch.
func (w *world) wakesNaming(task, id string) []string {
	w.t.Helper()
	rows, err := w.owner.Query(context.Background(), `SELECT line FROM conductor_wakes WHERE task_id = $1 AND line LIKE '%' || $2 || '%'
		ORDER BY created_at`, task, id)
	if err != nil {
		w.t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		w.t.Fatal(err)
	}
	return out
}

// The open wait naming a dispatch carries its outcome only if it holds on
// it. Replaced (here in the same transaction as the settle, before any
// evaluation could hold it), the outcome is owed, and the next pass wakes
// the conductor with it: exactly once.
func TestReviewReplacedWaitLosesOutcome(t *testing.T) {
	w, task, _ := dispatchWorld(t)
	w.gh.Set(func(s *fakegithub.Server) { s.DispatchRunID = 43000001960 })
	id := w.dispatched(task, `{"workflow":"ci.yml"}`)
	w.must(task, "wait_for", `{"conditions":[{"workflowEnded":"`+id+`"}],"deadline":"30m"}`)
	done := forge.RunState{ID: 43000001960, Status: "completed", Conclusion: "failure", Attempt: 1}
	if err := w.prs.DB.InOrg(context.Background(), w.org, func(tx pgx.Tx) error {
		if err := delivery.SettleDispatchTx(context.Background(), tx, w.org, id, delivery.DispatchRead{Run: &done}); err != nil {
			return err
		}
		var delivered string
		if err := tx.QueryRow(context.Background(), `SELECT COALESCE(woken_outcome, 'none') FROM workflow_dispatches WHERE id = $1`, id).
			Scan(&delivered); err != nil {
			return err
		}
		if delivered != "none" {
			t.Errorf("delivered %s while the wait was only open", delivered)
		}
		_, err := tx.Exec(context.Background(), `UPDATE conductor_waits SET ended_at = now(), ended_by = $2 WHERE task_id = $1 AND ended_at IS NULL`,
			task, delivery.WaitReplaced)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if n := len(w.workflowWakes(task)); n != 0 {
		t.Fatalf("%d wakes beside the open wait's", n)
	}
	for range 3 {
		mustExec(t, w.owner, `UPDATE workflow_dispatches SET last_read_at = NULL WHERE id = $1`, id)
		w.supervise()
	}
	wakes := w.wakesNaming(task, id)
	if len(wakes) != 1 || !strings.Contains(wakes[0], "completed: failure") {
		t.Fatalf("wakes %q", wakes)
	}
}

// The conductor ending ends its wait with no wake: the outcome that wait
// would have carried is owed, and woken for once.
func TestAnOutcomeOwedWhenTheConductorEndsIsWoken(t *testing.T) {
	w, task, _ := dispatchWorld(t)
	w.gh.Set(func(s *fakegithub.Server) { s.DispatchRunID = 43000001970 })
	id := w.dispatched(task, `{"workflow":"ci.yml"}`)
	w.must(task, "wait_for", `{"conditions":[{"workflowEnded":"`+id+`"}],"deadline":"30m"}`)
	done := forge.RunState{ID: 43000001970, Status: "completed", Conclusion: "failure", Attempt: 1}
	if err := w.prs.DB.InOrg(context.Background(), w.org, func(tx pgx.Tx) error {
		if err := delivery.SettleDispatchTx(context.Background(), tx, w.org, id, delivery.DispatchRead{Run: &done}); err != nil {
			return err
		}
		return delivery.EndWaitsTx(context.Background(), tx, task, delivery.WaitConductorEnded)
	}); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		mustExec(t, w.owner, `UPDATE workflow_dispatches SET last_read_at = NULL WHERE id = $1`, id)
		w.supervise()
	}
	wakes := w.wakesNaming(task, id)
	if len(wakes) != 1 || !strings.Contains(wakes[0], "completed: failure") {
		t.Fatalf("wakes %q", wakes)
	}
}

// A wait that held told the outcome in its digest; should its conductor
// end before that digest was told, the outcome is owed again and woken
// for once.
func TestAHeldDigestNotToldIsOwedAgain(t *testing.T) {
	w, task, _ := dispatchWorld(t)
	w.gh.Set(func(s *fakegithub.Server) { s.DispatchRunID = 43000001980 })
	id := w.dispatched(task, `{"workflow":"ci.yml"}`)
	out := w.must(task, "wait_for", `{"conditions":[{"workflowEnded":"`+id+`"}],"deadline":"30m"}`)
	waitID, _ := out["waitId"].(string)
	w.gh.UpdateWorkflowRun(43000001980, func(r *fakegithub.WorkflowRun) { r.Status, r.Conclusion = "completed", "failure" })
	w.supervise()
	if err := w.prs.DB.InOrg(context.Background(), w.org, func(tx pgx.Tx) error {
		_, err := delivery.EvaluateWaitTx(context.Background(), tx, w.org, task, waitID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if got := w.str(`SELECT COALESCE(woken_outcome, 'none') || ' ' || COALESCE(woken_wait, '') FROM workflow_dispatches WHERE id = $1`, id); got != "completed "+waitID {
		t.Fatalf("the held digest did not deliver it: %s", got)
	}
	// Its digest not yet told, the conductor ends.
	mustExec(t, w.owner, `UPDATE conductor_wakes SET delivered_at = NULL WHERE task_id = $1 AND key = $2`, task, "wait:"+waitID)
	mustExec(t, w.owner, `UPDATE conductor_waits SET ended_at = NULL, ended_by = NULL WHERE id = $1`, waitID)
	if err := w.prs.DB.InOrg(context.Background(), w.org, func(tx pgx.Tx) error {
		return delivery.EndWaitsTx(context.Background(), tx, task, delivery.WaitConductorEnded)
	}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		mustExec(t, w.owner, `UPDATE workflow_dispatches SET last_read_at = NULL WHERE id = $1`, id)
		w.supervise()
	}
	if wakes := w.workflowWakes(task); len(wakes) != 1 || !strings.Contains(wakes[0], "completed: failure") {
		t.Fatalf("wakes %q", wakes)
	}
}

// Each stretch of failed reads is its own "cannot see": seen again, then
// failing again, wakes again (M5: the key names the stretch; M6: seen
// again clears what was delivered).
func TestASecondStretchOfFailedReadsWakesAgain(t *testing.T) {
	w, task, _ := dispatchWorld(t)
	w.gh.Set(func(s *fakegithub.Server) { s.DispatchRunID = 43000001990 })
	id := w.dispatched(task, `{"workflow":"ci.yml"}`)
	failing := func(on bool) {
		w.gh.Set(func(s *fakegithub.Server) {
			s.Intercept = nil
			if on {
				s.Intercept = func(r *http.Request) int {
					if strings.Contains(r.URL.Path, "/actions/runs/43000001990") {
						return http.StatusBadGateway
					}
					return 0
				}
			}
		})
	}
	stretch := func() {
		failing(true)
		for range delivery.DispatchCannotSeeReads {
			w.supervise()
		}
		failing(false)
	}
	stretch()
	if n := len(w.workflowWakes(task)); n != 1 {
		t.Fatalf("%d wakes after one stretch", n)
	}
	w.supervise()
	if got := w.str(`SELECT COALESCE(outcome, 'none') || ' ' || failed_reads FROM workflow_dispatches WHERE id = $1`, id); got != "none 0" {
		t.Fatalf("seen again: %s", got)
	}
	time.Sleep(1100 * time.Millisecond)
	stretch()
	if wakes := w.workflowWakes(task); len(wakes) != 2 || !strings.Contains(wakes[1], "dude cannot see this workflow run") {
		t.Fatalf("wakes %q", wakes)
	}
}

// Two passes settling one dispatch at once are serialized by its row lock:
// two failed reads count two (M7).
func TestRacingSettlesCountEveryFailedRead(t *testing.T) {
	w, task, _ := dispatchWorld(t)
	id := w.dispatched(task, `{"workflow":"ci.yml"}`)
	read := delivery.DispatchRead{Err: &forge.Error{Status: 502, Message: "bad gateway"}}
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 2)
	go func() {
		done <- w.prs.DB.InOrg(context.Background(), w.org, func(tx pgx.Tx) error {
			if err := delivery.SettleDispatchTx(context.Background(), tx, w.org, id, read); err != nil {
				return err
			}
			close(started)
			<-release
			return nil
		})
	}()
	<-started
	go func() {
		done <- w.prs.DB.InOrg(context.Background(), w.org, func(tx pgx.Tx) error {
			return delivery.SettleDispatchTx(context.Background(), tx, w.org, id, read)
		})
	}()
	time.Sleep(300 * time.Millisecond)
	close(release)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if n := w.count(`SELECT failed_reads FROM workflow_dispatches WHERE id = $1`, id); n != 2 {
		t.Fatalf("two racing failed reads counted %d", n)
	}
	_ = task
}

// A task that ended is no longer supervised: its dispatches are not read
// and wake no one (M10).
func TestAnEndedTasksDispatchesAreNotSupervised(t *testing.T) {
	w, task, _ := dispatchWorld(t)
	w.gh.Set(func(s *fakegithub.Server) { s.DispatchRunID = 43000002010 })
	id := w.dispatched(task, `{"workflow":"ci.yml"}`)
	mustExec(t, w.owner, `UPDATE tasks SET status = 'done' WHERE id = $1`, task)
	w.gh.UpdateWorkflowRun(43000002010, func(r *fakegithub.WorkflowRun) { r.Status, r.Conclusion = "completed", "success" })
	w.supervise()
	if got := w.str(`SELECT state || ' ' || (last_read_at IS NULL)::text FROM workflow_dispatches WHERE id = $1`, id); got != "bound true" {
		t.Fatalf("an ended task's dispatch was read: %s", got)
	}
	if n := len(w.workflowWakes(task)); n != 0 {
		t.Fatalf("%d wakes for an ended task", n)
	}
}

// A rate-limited read's Retry-After is honoured: no read before it.
func TestARateLimitedReadWaitsForRetryAfter(t *testing.T) {
	w, task, _ := dispatchWorld(t)
	w.gh.Set(func(s *fakegithub.Server) { s.DispatchRunID = 43000002020 })
	id := w.dispatched(task, `{"workflow":"ci.yml"}`)
	w.settleDispatch(id, delivery.DispatchRead{Err: &forge.Error{Status: 429, Message: "rate limit", RetryAfter: time.Hour}})
	w.gh.UpdateWorkflowRun(43000002020, func(r *fakegithub.WorkflowRun) { r.Status, r.Conclusion = "completed", "success" })
	w.supervise()
	if got := w.str(`SELECT state || ' ' || failed_reads FROM workflow_dispatches WHERE id = $1`, id); got != "bound 1" {
		t.Fatalf("read before Retry-After: %s", got)
	}
	mustExec(t, w.owner, `UPDATE workflow_dispatches SET read_after = now() - interval '1 second', last_read_at = NULL WHERE id = $1`, id)
	w.supervise()
	if got := w.str(`SELECT state || ' ' || failed_reads FROM workflow_dispatches WHERE id = $1`, id); got != "completed 0" {
		t.Fatalf("after Retry-After: %s", got)
	}
}

// A full batch has the supervisor's loop run again at once.
func TestAFullBatchOfDispatchesRunsAgain(t *testing.T) {
	w, task, _ := dispatchWorld(t)
	w.gh.Set(func(s *fakegithub.Server) { s.DispatchRunID = 43000002100 })
	id := w.dispatched(task, `{"workflow":"ci.yml"}`)
	mustExec(t, w.owner, `INSERT INTO workflow_dispatches (id, organization_id, task_id, repository_id, workflow, ref, state, run_id)
		SELECT 'wfd_n' || n, organization_id, task_id, repository_id, workflow, 'r' || n, 'bound', 3000 + n FROM workflow_dispatches, generate_series(1, 24) n
		WHERE id = $1`, id)
	n, err := w.prs.SuperviseDispatches(context.Background(), 0)
	if err != nil || n != 20 {
		t.Fatalf("a full batch: %d %v", n, err)
	}
	if n, err = w.prs.SuperviseDispatches(context.Background(), time.Hour); err != nil || n != 0 {
		t.Fatalf("the rest: %d %v", n, err)
	}
}
