package orchestrator_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

// T-F1: the author pushes after the verdict claimed its review and before
// it read the diff (/pulls/files has no ref): the step re-reads the head,
// abandons, and posts nothing.
func TestAHeadMovingBetweenTheClaimAndTheDiffPostsNothing(t *testing.T) {
	w := newWorld(t)
	w.gh.SetChecks("success")
	pushed := false
	var wi string
	n := w.gh.OpenAsPerson("alice", "feature/greet", "main", "Greet")
	w.gh.Set(func(s *fakegithub.Server) {
		s.Intercept = func(r *http.Request) int {
			if !pushed && wi != "" && r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/pulls/1") &&
				w.events(wi, delivery.EvReviewClaimed) > 0 {
				pushed = true
				w.gh.CommitFiles("feature/greet", "alice's later change", map[string]string{"later.md": "later\n"})
			}
			return 0
		}
	})
	wi = w.adopted("pr", n, "feature/greet", "main", delivery.ModeReviewOnly)
	w.cleanLenses()
	w.startDelivery(wi)
	w.agentsDone(wi, "abandoned", func() bool {
		return w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = $2 AND payload->>'claimId' IS NOT NULL`,
			wi, delivery.EvReviewAbandoned) > 0 && w.waitingOnPR(wi)
	})
	if !pushed {
		t.Fatal("never pushed between the claim and the diff")
	}
	if got := w.dudeReviews(wi, n); len(got) != 0 {
		t.Fatalf("a review was posted at a head the pull request no longer shows: %+v", got)
	}
}

// T-F2: a lens's agent crashes: no escalation to a person; dude comments
// that the review could not complete, naming the lens, and never approves.
func TestAFailedLensCommentsNotApproves(t *testing.T) {
	w := newWorld(t)
	w.gh.SetChecks("success")
	n := w.gh.OpenAsPerson("alice", "feature/greet", "main", "Greet")
	wi := w.adopted("pr", n, "feature/greet", "main", delivery.ModeReviewOnly)
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] == "review" {
			return fakelux.Behaviour{Crash: true}
		}
		return fakelux.Behaviour{Reply: "ok"}
	}
	w.startDelivery(wi)
	// The lenses run on the fake lux (pump's sweep), where they crash.
	w.until("dude's review", func() bool {
		w.sync()
		w.advanced()
		w.quietConductorDecides(wi)
		return len(w.dudeReviews(wi, n)) == 1 && w.waitingOnPR(wi)
	})
	if e := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type LIKE '%escalat%'`, wi); e != 0 {
		t.Fatalf("%d escalations for a failed lens on a review-only task", e)
	}
	got := w.dudeReviews(wi, n)
	if got[0].State != "COMMENTED" || !strings.Contains(got[0].Body, "did not finish") {
		t.Fatalf("after a crashed lens: %+v", got)
	}
	if c := w.writers(wi); c != 0 {
		t.Fatalf("%d writers", c)
	}
}

// T-F2, the case lensOutcomesTx exists for: a lens's earlier Run at H
// completed, its latest failed. The gate reads completed Runs only;
// the verdict comments naming the lens, never approves.
func TestALensWhoseLatestRunFailedIsNamedNotApproved(t *testing.T) {
	w := newWorld(t)
	w.gh.SetChecks("success")
	n := w.gh.OpenAsPerson("alice", "feature/greet", "main", "Greet")
	wi := w.adopted("pr", n, "feature/greet", "main", delivery.ModeReviewOnly)
	w.cleanLenses()
	// As a lens's Run completes, a newer Run of the same lens at the same
	// head fails (as a retried lens would), before the verdict reads them.
	_, err := w.owner.Exec(t.Context(), `CREATE FUNCTION dude_test_fail_later() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.phase = 'review' AND NEW.status = 'completed' AND OLD.status <> 'completed' AND NEW.category IS NOT NULL
				AND NOT EXISTS (SELECT 1 FROM runs r WHERE r.task_id = NEW.task_id AND r.id = NEW.id || '_again') THEN
				INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, kind, category, base_refs,
					created_at, ended_at)
				VALUES (NEW.id || '_again', NEW.organization_id, NEW.project_id, NEW.task_id, NEW.attempt, 'failed', NEW.phase,
					NEW.role, NEW.kind, NEW.category, NEW.base_refs, NEW.created_at + interval '1 second', now());
			END IF;
			RETURN NEW;
		END $$`)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = w.owner.Exec(t.Context(), `DROP FUNCTION IF EXISTS dude_test_fail_later() CASCADE`) })
	if _, err := w.owner.Exec(t.Context(), `CREATE TRIGGER dude_test_fail_later AFTER UPDATE ON runs
		FOR EACH ROW WHEN (NEW.task_id = '`+wi+`') EXECUTE FUNCTION dude_test_fail_later()`); err != nil {
		t.Fatal(err)
	}
	w.startDelivery(wi)
	w.agentsDone(wi, "dude's review", func() bool { return len(w.dudeReviews(wi, n)) == 1 && w.waitingOnPR(wi) })
	_, _ = w.owner.Exec(t.Context(), `DROP TRIGGER IF EXISTS dude_test_fail_later ON runs`)
	var lens string
	_ = w.owner.QueryRow(t.Context(), `SELECT category FROM runs WHERE task_id = $1 AND id LIKE '%\_again' LIMIT 1`, wi).Scan(&lens)
	if lens == "" {
		t.Fatal("no lens Run failed after a completed one")
	}
	got := w.dudeReviews(wi, n)
	if got[0].State != "COMMENTED" || !strings.Contains(got[0].Body, lens+" did not finish") {
		t.Fatalf("a lens whose latest Run failed (%s): %+v", lens, got)
	}
}

// T-F3: CI fails at H and the conductor judges it related: it starts a
// review with a note, a lens files the finding, and dude requests changes
// at H, the failing checks and the finding's title in the review.
func TestRelatedRedCIRequestsChanges(t *testing.T) {
	w := newWorld(t)
	w.withTools()
	w.gh.SetChecks("failure")
	n := w.gh.OpenAsPerson("alice", "feature/greet", "main", "Greet")
	wi := w.adopted("pr", n, "feature/greet", "main", delivery.ModeReviewOnly)
	// The first round's lenses find nothing; the conductor's finds the failure.
	clean, scripted := true, w.lux.Decide
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] == "review" && clean {
			return fakelux.Behaviour{Reply: "No findings."}
		}
		return scripted(spec)
	}
	w.startDelivery(wi)
	w.until("the conductor asked about the failing checks", func() bool {
		w.sync()
		w.advanced()
		for _, id := range w.pendingPhaseRuns(wi) {
			w.finished(id)
		}
		return w.decisionAt(wi) == delivery.PointPRFeedback
	})
	if got := w.dudeReviews(wi, n); len(got) != 0 {
		t.Fatalf("posted before the conductor looked: %+v", got)
	}
	clean = false
	w.must(wi, "start_phase", `{"phase":"review","note":"the build check fails on the change's own code: file it"}`)
	w.agentsDone(wi, "dude's review", func() bool { return len(w.dudeReviews(wi, n)) == 1 && w.waitingOnPR(wi) })
	got := w.dudeReviews(wi, n)
	if got[0].State != "CHANGES_REQUESTED" || got[0].CommitID != w.gh.SHA("feature/greet") {
		t.Fatalf("with related red CI: %+v", got)
	}
	body := got[0].Body
	for _, l := range w.gh.Pull(n).LineComments {
		body += "\n" + l.Body
	}
	// The finding as the review places it (its title bold, its words), not
	// only the gate's headline, which names the title too.
	for _, want := range []string{"failing", "**FACTORY.md does not record the fix**", "The change is missing a record that the review was addressed."} {
		if !strings.Contains(body, want) {
			t.Errorf("the review lacks %q:\n%s", want, body)
		}
	}
	if c := w.writers(wi); c != 0 {
		t.Fatalf("%d writers", c)
	}
}
