package orchestrator_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
)

// cannotTellWorld is a dispatch bound to an in-progress run, and refuse,
// which makes every pass's settle fail (or not): a trigger refuses the
// write that stores what GitHub showed, whatever the columns' types.
func cannotTellWorld(t *testing.T, runID int64) (w *world, task, id string, refuse func(bool)) {
	t.Helper()
	w, task, _ = dispatchWorld(t)
	w.gh.Set(func(s *fakegithub.Server) { s.DispatchRunID = runID })
	id = w.dispatched(task, `{"workflow":"ci.yml"}`)
	w.gh.UpdateWorkflowRun(runID, func(r *fakegithub.WorkflowRun) { r.Status = "in_progress" })
	mustExec(t, w.owner, `CREATE FUNCTION refuse_settle() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'settle refused for the test'; END $$`)
	refuse = func(on bool) {
		t.Helper()
		mustExec(t, w.owner, `DROP TRIGGER IF EXISTS refuse_settle ON workflow_dispatches`)
		if on {
			mustExec(t, w.owner, `CREATE TRIGGER refuse_settle BEFORE UPDATE ON workflow_dispatches FOR EACH ROW
				WHEN (NEW.last_seen_at IS DISTINCT FROM OLD.last_seen_at) EXECUTE FUNCTION refuse_settle()`)
		}
	}
	return w, task, id, refuse
}

// failing is the dispatch's failed-read state: the count, whether a
// stretch is open, and whether an error is kept.
func (w *world) failing(id string) string {
	w.t.Helper()
	return w.str(`SELECT state || ' ' || failed_reads || ' ' || (failing_since IS NOT NULL)::text || ' ' || (COALESCE(last_error, '') <> '')::text
		FROM workflow_dispatches WHERE id = $1`, id)
}

func cannotSeeWakes(wakes []string) int {
	n := 0
	for _, l := range wakes {
		if strings.Contains(l, "dude cannot see this workflow run") {
			n++
		}
	}
	return n
}

// "Can't tell" is never silence (#465): a pass that fails to settle what
// it read counts as a failed read, and DispatchCannotSeeReads of them wake
// the conductor "cannot see", once.
func TestASupervisorPassThatFailsToSettleWakesCannotSee(t *testing.T) {
	w, task, id, refuse := cannotTellWorld(t, 43000003100)
	refuse(true)
	for range delivery.DispatchCannotSeeReads - 1 {
		w.supervise()
	}
	if got := w.failing(id); got != "bound 2 true true" {
		t.Fatalf("after failing passes: %s", got)
	}
	if n := len(w.workflowWakes(task)); n != 0 {
		t.Fatalf("%d wakes before the bound", n)
	}
	w.supervise()
	if wakes := w.workflowWakes(task); len(wakes) != 1 || cannotSeeWakes(wakes) != 1 {
		t.Fatalf("wakes %q", wakes)
	}
	w.supervise()
	if n := len(w.workflowWakes(task)); n != 1 {
		t.Fatalf("%d cannot-see wakes for one stretch", n)
	}
}

// A pass that settles again ends the stretch: the count is back to 0, and
// a later stretch is a new "cannot see", woken again.
func TestASettledPassEndsASettleFailureStretch(t *testing.T) {
	w, task, id, refuse := cannotTellWorld(t, 43000003200)
	refuse(true)
	for range delivery.DispatchCannotSeeReads - 1 {
		w.supervise()
	}
	// Stretches are reads apart, not within the second a wake key holds.
	mustExec(t, w.owner, `UPDATE workflow_dispatches SET failing_since = failing_since - interval '1 hour' WHERE id = $1`, id)
	w.supervise()
	refuse(false)
	w.supervise()
	if got := w.failing(id); got != "bound 0 false false" {
		t.Fatalf("after a settled pass: %s", got)
	}
	refuse(true)
	for range delivery.DispatchCannotSeeReads {
		w.supervise()
	}
	if wakes := w.workflowWakes(task); len(wakes) != 2 || cannotSeeWakes(wakes) != 2 {
		t.Fatalf("wakes %q", wakes)
	}
}

// A pass cancelled mid-read (the orchestrator stopping) learned nothing
// about GitHub: it records no failed read.
func TestACancelledSupervisorPassRecordsNoFailedRead(t *testing.T) {
	w, _, id, _ := cannotTellWorld(t, 43000003300)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.gh.Set(func(s *fakegithub.Server) {
		s.Intercept = func(r *http.Request) int {
			if strings.Contains(r.URL.Path, "/actions/runs/") {
				cancel()
			}
			return 0
		}
	})
	if _, err := w.prs.SuperviseDispatches(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if ctx.Err() == nil {
		t.Fatal("the pass read nothing to cancel")
	}
	if got := w.failing(id); got != "bound 0 false false" {
		t.Fatalf("after a cancelled pass: %s", got)
	}
}
