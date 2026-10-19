package orchestrator_test

import (
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakeagent"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
	"github.com/marciomartins/dude/orchestrator/internal/ids"
)

// finished on a pending review Run whose reviewer replies with one finding:
// the Run is completed with its run.completed, and the finding is open. The
// finding must be recorded (review.completed) before the Run completes: the
// delivery reads a review's findings once it is told the Run completed
// (NotifyFinished), so a completed review without its findings would be
// read as a clean one.
func TestFinishedRecordsAReviewsFindingsBeforeCompletingIt(t *testing.T) {
	w := newWorld(t)
	wi := w.reviewing()
	// A pending re-review of prOpen's first review round: its task, attempt,
	// category and starting point, nothing shown to judge.
	review := ids.New(ids.Run)
	mustExec(t, w.owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role,
			parent_run_id, base_refs, category, blocking_severities)
		SELECT $1, organization_id, project_id, task_id, attempt, 'pending', phase, role,
			parent_run_id, base_refs, category, blocking_severities
		FROM runs WHERE task_id = $2 AND phase = 'review' AND review_range IS NULL ORDER BY created_at LIMIT 1`, review, wi)
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'pending'`, review); n != 1 {
		t.Fatalf("no pending review Run\n%s", w.describeRuns())
	}
	scripted := w.lux.Decide
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] == delivery.PhaseReview {
			return fakelux.Behaviour{Reply: "One problem:\n\n```yaml\n" + fakeagent.Finding + "```\n"}
		}
		return scripted(spec)
	}

	w.finished(review)

	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'completed'`, review); n != 1 {
		t.Errorf("the review Run is not completed\n%s", w.describeRuns())
	}
	if n := w.count(`SELECT count(*) FROM review_findings WHERE run_id = $1 AND status = 'open'
		AND severity = 'blocking' AND file = 'FACTORY.md' AND line = 1 AND title = 'FACTORY.md does not record the fix'`, review); n != 1 {
		t.Errorf("%d open findings of the review, want its one", n)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.completed'`, review); n != 1 {
		t.Errorf("%d run.completed events, want 1", n)
	}
	if n := w.count(`SELECT count(*) FROM events f JOIN events c ON c.run_id = f.run_id AND c.event_type = 'run.completed'
		WHERE f.run_id = $1 AND f.event_type = $2 AND (f.payload->>'count')::int = 1 AND f.cursor < c.cursor`,
		review, delivery.EvReviewCompleted); n != 1 {
		t.Errorf("the review's finding was not recorded before the Run completed")
	}
	// Nothing has told the delivery yet: what it would read when told is the above.
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND phase_notified_at IS NULL`, review); n != 1 {
		t.Errorf("the delivery was told of the review before the test read it")
	}
}
