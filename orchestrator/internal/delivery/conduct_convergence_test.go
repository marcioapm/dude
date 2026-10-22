package delivery_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

func conductConvergenceSetup(t *testing.T, w *convergenceWorld) (string, delivery.RunRef) {
	t.Helper()
	wf := w.delivery(t, "conductorDecision", delivery.DeciderConductor, `["conductor.decision"]`)
	w.run(t, "conductor", "ABS-1", "", delivery.RoleConductor, "running")
	policy := delivery.DefaultPolicy()
	policy.MaxAttemptsPerFinding = 10
	policy.MaxReviewIterations = 10
	w.exec(t, `UPDATE workflow_runs SET state = state || jsonb_build_object(
		'policy', $2::jsonb, 'decision', jsonb_build_object('point', 'after_review', 'policy', 'reviewExit')) WHERE id = $1`, wf, policy)
	return wf, delivery.RunRef{Org: w.org, ProjectID: w.project, TaskID: w.task, RunID: "conductor"}
}

func conductConvergenceStart(w *convergenceWorld, ref delivery.RunRef, phase string, named []string) error {
	return w.app.InOrg(context.Background(), w.org, func(tx pgx.Tx) error {
		_, err := delivery.ConductStartPhase(context.Background(), tx, ref, delivery.StartPhase{Phase: phase, FindingIDs: named})
		return err
	})
}

// conductRegressionRounds is two fix rounds, each judging f fixed and
// raising a fix-caused blocker (new1, new2).
func conductRegressionRounds(t *testing.T, w *convergenceWorld) {
	t.Helper()
	for i := 1; i <= 2; i++ {
		fix, review := fmt.Sprintf("fix%d", i), fmt.Sprintf("review%d", i)
		w.phase(t, fix, "fix", "", []string{"f"}, nil, 2*i-1)
		w.phase(t, review, "review", fix, []string{"f"}, map[string]string{"f": "fixed"}, 2*i)
		w.finding(t, fmt.Sprintf("new%d", i), review, "high", true)
	}
}

// conductOpenFeedback submits one open feedback item of the people's.
func conductOpenFeedback(t *testing.T, w *convergenceWorld) {
	t.Helper()
	w.draft(t, "feedback", w.ana, "Please rename the flag")
	w.exec(t, `INSERT INTO feedback_batches (id, organization_id, task_id, attempt, author_person_id, submission_key)
		VALUES ('batch', $1, $2, 1, $3, 'k')`, w.org, w.task, w.ana)
	w.exec(t, `UPDATE feedback_items SET status = 'open', submitted_at = now(), batch_id = 'batch' WHERE id = 'feedback'`)
}

// conductTaken is the findings the conductor's taken decision sends a fix.
func conductTaken(t *testing.T, w *convergenceWorld, wf string) []string {
	t.Helper()
	var got []string
	if err := w.owner.QueryRow(context.Background(), `SELECT ARRAY(SELECT jsonb_array_elements_text(state->'decision'->'taken'->'findingIds')) FROM workflow_runs WHERE id = $1`, wf).Scan(&got); err != nil {
		t.Fatal(err)
	}
	return got
}

func conductStillRounds(t *testing.T, w *convergenceWorld) {
	t.Helper()
	for i := 1; i <= 2; i++ {
		fix := fmt.Sprintf("fix%d", i)
		w.phase(t, fix, "fix", "", []string{"f"}, nil, 2*i-1)
		w.phase(t, fmt.Sprintf("review%d", i), "review", fix, []string{"f"}, map[string]string{"f": "still"}, 2*i)
	}
}

func TestConductFixConvergenceStillGuard(t *testing.T) {
	for _, named := range [][]string{nil, {"f"}, {"other"}} {
		t.Run(fmt.Sprint(named), func(t *testing.T) {
			w := newConvergenceWorld(t)
			wf, ref := conductConvergenceSetup(t, w)
			w.finding(t, "other", "origin", "medium", false)
			conductStillRounds(t, w)
			err := conductConvergenceStart(w, ref, delivery.PhaseFix, named)
			if !delivery.IsRefusal(err) || !strings.Contains(err.Error(), "not converging") {
				t.Fatalf("fix with ordinary budget available: %v", err)
			}
			if n := w.count(t, `SELECT count(*) FROM workflow_runs WHERE id = $1 AND state->'decision'->'taken' IS NOT NULL`, wf); n != 0 {
				t.Fatal("refused fix recorded a decision")
			}
			if n := w.count(t, `SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'conductor.decided'`, w.task); n != 0 {
				t.Fatal("refused fix emitted a decision event")
			}
			if err := conductConvergenceStart(w, ref, delivery.PhaseReview, nil); err != nil {
				t.Fatalf("re-review must remain available: %v", err)
			}
		})
	}
}

func TestConductFixConvergenceRegressionGuard(t *testing.T) {
	w := newConvergenceWorld(t)
	_, ref := conductConvergenceSetup(t, w)
	conductRegressionRounds(t, w)
	w.exec(t, `UPDATE review_findings SET status = 'resolved' WHERE id IN ('f', 'new1')`)
	err := conductConvergenceStart(w, ref, delivery.PhaseFix, []string{"new2"})
	if !delivery.IsRefusal(err) || !strings.Contains(err.Error(), "not converging") {
		t.Fatalf("regression with ordinary budget available: %v", err)
	}
}

// Two regression rounds stand, but every reviewer blocker is resolved: the
// tester's open blocker is the next test's to judge, not the review loop's,
// so it neither trips the guard nor is held by it.
func TestConductFixConvergenceRegressionIgnoresTesterBlocker(t *testing.T) {
	w := newConvergenceWorld(t)
	wf, ref := conductConvergenceSetup(t, w)
	conductRegressionRounds(t, w)
	w.exec(t, `UPDATE review_findings SET status = 'resolved' WHERE id IN ('f', 'new1', 'new2')`)
	w.phase(t, "tested", "test", "", nil, nil, 5)
	w.finding(t, "tester", "tested", "blocking", false)
	if got := w.convergence(t, w.state()); got.RegressionRounds != 2 {
		t.Fatalf("fixture lost its regression rounds: %+v", got)
	}
	if err := conductConvergenceStart(w, ref, delivery.PhaseFix, nil); err != nil {
		t.Fatalf("tester-only fix refused: %v", err)
	}
	got := conductTaken(t, w, wf)
	if !slices.Equal(got, []string{"tester"}) {
		t.Fatalf("selected findings %v, want the tester's", got)
	}
}

// A regression whose fix-caused blockers are all closed names nothing to
// direct: an unrelated open blocker goes to the next fix, and the loop does
// not escalate with an empty list.
func TestARegressionWithItsBlockersClosedDoesNotEscalate(t *testing.T) {
	w := newConvergenceWorld(t)
	wf, ref := conductConvergenceSetup(t, w)
	conductRegressionRounds(t, w)
	w.finding(t, "g", "origin", "high", false)
	w.exec(t, `UPDATE review_findings SET status = 'resolved' WHERE id IN ('f', 'new1')`)
	w.exec(t, `UPDATE review_findings SET status = 'accepted' WHERE id = 'new2'`)
	st := w.state()
	findings, conv, err := (&delivery.Store{DB: w.app}).ConvergingFindings(context.Background(), w.org, st)
	if err != nil {
		t.Fatal(err)
	}
	if conv.RegressionRounds != 2 || len(conv.FindingIDs) != 0 {
		t.Fatalf("fixture: %+v", conv)
	}
	if exit := delivery.Exit(st.Policy, findings, 1, conv); exit != nil {
		t.Fatalf("exit = %+v, want another fix", exit)
	}
	if err := conductConvergenceStart(w, ref, delivery.PhaseFix, nil); err != nil {
		t.Fatalf("fix of the unrelated blocker refused: %v", err)
	}
	got := conductTaken(t, w, wf)
	if !slices.Equal(got, []string{"g"}) {
		t.Fatalf("selected findings %v, want [g]", got)
	}
}

func TestConductFixConvergenceAllowsUnstalledFindings(t *testing.T) {
	for _, scenario := range []string{"disabled", "blank criterion", "nonblocking", "tester", "reset", "previous attempt", "named subset"} {
		t.Run(scenario, func(t *testing.T) {
			w := newConvergenceWorld(t)
			wf, ref := conductConvergenceSetup(t, w)
			conductStillRounds(t, w)
			named, want := []string(nil), []string{"f"}
			switch scenario {
			case "disabled":
				w.exec(t, `UPDATE workflow_runs SET state = jsonb_set(state, '{policy,maxStillRounds}', '0') WHERE id = $1`, wf)
			case "blank criterion":
				w.exec(t, `UPDATE review_findings SET acceptance = '   ' WHERE id = 'f'`)
			case "nonblocking":
				w.exec(t, `UPDATE review_findings SET severity = 'medium' WHERE id = 'f'`)
			case "tester":
				w.exec(t, `UPDATE runs SET phase = 'test' WHERE id = 'origin'`)
			case "reset":
				w.exec(t, `UPDATE workflow_runs SET state = state || '{"convergenceFrom":"2026-01-01T00:03:00Z"}'::jsonb WHERE id = $1`, wf)
			case "previous attempt":
				w.exec(t, `UPDATE runs SET attempt = 2 WHERE id = 'review2'`)
			case "named subset":
				w.exec(t, `UPDATE runs SET verdicts = '{}' WHERE id = 'review2'`)
				w.finding(t, "other", "origin", "high", false)
				named = []string{"other"}
				want = named
			}
			if err := conductConvergenceStart(w, ref, delivery.PhaseFix, named); err != nil {
				t.Fatal(err)
			}
			got := conductTaken(t, w, wf)
			if !slices.Equal(got, want) {
				t.Fatalf("selected findings %v, want %v", got, want)
			}
		})
	}
}

func TestConductFixConvergencePreservesFeedbackOnly(t *testing.T) {
	w := newConvergenceWorld(t)
	_, ref := conductConvergenceSetup(t, w)
	conductStillRounds(t, w)
	w.exec(t, `UPDATE review_findings SET status = 'resolved' WHERE id = 'f'`)
	conductOpenFeedback(t, w)
	if err := conductConvergenceStart(w, ref, delivery.PhaseFix, nil); err != nil {
		t.Fatalf("feedback-only fix: %v", err)
	}
}

func TestConductFixConvergenceKeepsNamedValidation(t *testing.T) {
	for _, scenario := range []string{"unknown", "resolved", "attempt bound", "no open items"} {
		t.Run(scenario, func(t *testing.T) {
			w := newConvergenceWorld(t)
			_, ref := conductConvergenceSetup(t, w)
			named := []string{"f"}
			switch scenario {
			case "unknown":
				named = []string{"missing"}
			case "resolved":
				w.exec(t, `UPDATE review_findings SET status = 'resolved' WHERE id = 'f'`)
			case "attempt bound":
				w.exec(t, `UPDATE review_findings SET fix_attempts = 10 WHERE id = 'f'`)
			case "no open items":
				w.exec(t, `UPDATE review_findings SET status = 'resolved' WHERE id = 'f'`)
				named = nil
			}
			if err := conductConvergenceStart(w, ref, delivery.PhaseFix, named); !delivery.IsRefusal(err) {
				t.Fatalf("expected refusal: %v", err)
			}
		})
	}
}

// An escalation's wake longer than the table admits is clipped to its bound,
// not refused: escalate records it in the step's own transaction.
func TestAnEscalationWakePastItsBoundIsClippedToTheTablesBound(t *testing.T) {
	w := newConvergenceWorld(t)
	ctx := context.Background()
	line := strings.Repeat("é", 1500) + " Findings: f."
	err := w.app.InOrg(ctx, w.org, func(tx pgx.Tx) error {
		_, err := delivery.RecordWakeTx(ctx, tx, w.org, w.task, "escalation", "escalation:long", line)
		return err
	})
	if err != nil {
		t.Fatalf("recording a long escalation wake: %v", err)
	}
	var stored string
	var chars int
	if err := w.owner.QueryRow(ctx, `SELECT line, length(line) FROM conductor_wakes WHERE key = 'escalation:long'`).Scan(&stored, &chars); err != nil {
		t.Fatal(err)
	}
	if chars != 1200 || !strings.HasSuffix(stored, "é…") {
		t.Fatalf("stored %d characters ending %q, want 1200 ending in …", chars, stored[len(stored)-8:])
	}
}

// A fix of the people's feedback alone never reads the review loop's
// convergence: a review history it could not read holds no such fix, and
// does hold a fix that names a review finding.
func TestConductFixOfFeedbackAloneReadsNoConvergence(t *testing.T) {
	for _, named := range [][]string{nil, {"f"}} {
		t.Run(fmt.Sprint(named), func(t *testing.T) {
			w := newConvergenceWorld(t)
			_, ref := conductConvergenceSetup(t, w)
			w.phase(t, "fix1", "fix", "", []string{"f"}, nil, 1)
			w.phase(t, "review1", "review", "fix1", []string{"f"}, nil, 2)
			// Not a verdict map ConvergenceTx can scan.
			w.exec(t, `UPDATE runs SET verdicts = '{"f": 1}' WHERE id = 'review1'`)
			if named == nil {
				w.exec(t, `UPDATE review_findings SET status = 'resolved' WHERE id = 'f'`)
			}
			conductOpenFeedback(t, w)
			err := conductConvergenceStart(w, ref, delivery.PhaseFix, named)
			if named == nil && err != nil {
				t.Fatalf("feedback-only fix: %v", err)
			}
			// The unreadable history fails the read itself, not as a refusal.
			if named != nil && (err == nil || delivery.IsRefusal(err)) {
				t.Fatalf("a fix of a review finding did not read the convergence: %v", err)
			}
		})
	}
}
