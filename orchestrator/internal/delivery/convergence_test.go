package delivery_test

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

type convergenceWorld struct{ *feedbackWorld }

func newConvergenceWorld(t *testing.T) *convergenceWorld {
	w := &convergenceWorld{newFeedbackWorld(t)}
	w.phase(t, "origin", "review", "", nil, nil, 0)
	w.finding(t, "f", "origin", "high", false)
	return w
}

func (w *convergenceWorld) phase(t *testing.T, id, phase, parent string, sent []string, verdicts map[string]string, n int) {
	t.Helper()
	w.exec(t, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, phase, role, status,
		parent_run_id, finding_ids, verdicts, category, created_at)
		VALUES ($1, $2, $3, $4, 1, $5::run_phase, 'reviewer', 'completed', NULLIF($6, ''),
		COALESCE($7::text[], '{}'), COALESCE($8::jsonb, '{}'), 'correctness', $9)`,
		id, w.org, w.project, w.task, phase, parent, sent, verdicts, time.Date(2026, 1, 1, 0, n, 0, 0, time.UTC))
}

func (w *convergenceWorld) finding(t *testing.T, id, run, severity string, caused bool) {
	t.Helper()
	w.exec(t, `INSERT INTO review_findings (id, organization_id, task_id, run_id, category, severity, title, description,
		acceptance, caused_by_fix) VALUES ($1, $2, $3, $4, 'correctness', $5::finding_severity, 'problem', 'evidence', 'observable', $6)`,
		id, w.org, w.task, run, severity, caused)
}

func (w *convergenceWorld) convergence(t *testing.T, st *delivery.State) delivery.Convergence {
	t.Helper()
	got, err := (&delivery.Store{DB: w.app}).Convergence(context.Background(), w.org, st)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func (w *convergenceWorld) state() *delivery.State {
	return &delivery.State{TaskID: w.task, Attempt: 1, Policy: delivery.DefaultPolicy()}
}

func TestConvergenceStillCountsOnlyExplicitJudgementsOfSentFindings(t *testing.T) {
	w := newConvergenceWorld(t)
	st := w.state()
	for i, verdict := range []string{"still", "", "still", "still"} {
		fix, review := fmt.Sprintf("fix%d", i), fmt.Sprintf("review%d", i)
		w.phase(t, fix, "fix", "", []string{"f"}, nil, 2*i+1)
		v := map[string]string{}
		if verdict != "" {
			v["f"] = verdict
		}
		w.phase(t, review, "review", fix, []string{"f"}, v, 2*i+2)
		want := []int{1, 0, 1, 2}[i]
		if got := w.convergence(t, st).StillRounds["f"]; got != want {
			t.Fatalf("round %d: still=%d, want %d", i, got, want)
		}
	}
	w.phase(t, "unrelated", "fix", "", nil, nil, 10)
	w.phase(t, "unrelated-review", "review", "unrelated", []string{"f"}, map[string]string{"f": "fixed"}, 11)
	if got := w.convergence(t, st).StillRounds["f"]; got != 2 {
		t.Fatalf("unrelated fix broke streak: %d", got)
	}
	findings, err := (&delivery.Store{DB: w.app}).Findings(context.Background(), w.org, st)
	if err != nil || len(findings) != 1 || !findings[0].HasCriterion || findings[0].StillRounds != 2 {
		t.Fatalf("finding annotations: %+v, %v", findings, err)
	}
	st.ConvergenceFrom = time.Date(2026, 1, 1, 0, 6, 0, 0, time.UTC)
	if got := w.convergence(t, st).StillRounds["f"]; got != 1 {
		t.Fatalf("reset: %d, want 1", got)
	}
}

func TestConvergenceExcludesInvalidReviewAndFixRows(t *testing.T) {
	for _, mutation := range []string{
		`UPDATE runs SET status = 'failed' WHERE id = 'review'`,
		`UPDATE runs SET replaced_by = 'origin' WHERE id = 'review'`,
		`UPDATE runs SET phase = 'test' WHERE id = 'review'`,
		`UPDATE runs SET attempt = 2 WHERE id = 'review'`,
		`UPDATE runs SET attempt = 2 WHERE id = 'fix'`,
		`UPDATE runs SET replaced_by = 'origin' WHERE id = 'fix'`,
		`UPDATE runs SET phase = 'simplify' WHERE id = 'fix'`,
		`UPDATE runs SET finding_ids = '{}' WHERE id = 'review'`,
		`UPDATE runs SET finding_ids = '{}' WHERE id = 'fix'`,
	} {
		t.Run(mutation, func(t *testing.T) {
			w := newConvergenceWorld(t)
			w.phase(t, "fix", "fix", "", []string{"f"}, nil, 1)
			w.phase(t, "review", "review", "fix", []string{"f"}, map[string]string{"f": "still"}, 2)
			w.exec(t, mutation)
			if got := w.convergence(t, w.state()); got.StillRounds["f"] != 0 || got.RegressionRounds != 0 {
				t.Fatalf("excluded row counted: %+v", got)
			}
		})
	}
}

func TestConvergenceRegressionRequiresSameReviewerAndParentSentBlocker(t *testing.T) {
	w := newConvergenceWorld(t)
	st := w.state()
	for i := 1; i <= 2; i++ {
		fix, review := fmt.Sprintf("fix%d", i), fmt.Sprintf("review%d", i)
		w.phase(t, fix, "fix", "", []string{"f"}, nil, i*3)
		w.phase(t, review, "review", fix, []string{"f"}, map[string]string{"f": "fixed"}, i*3+1)
		w.finding(t, fmt.Sprintf("new%d", i), review, "high", true)
	}
	got := w.convergence(t, st)
	if got.RegressionRounds != 2 || !reflect.DeepEqual(got.FindingIDs, []string{"new2"}) {
		t.Fatalf("two regressions: %+v", got)
	}
	// An earlier round's new blocker counts though it is resolved now; only
	// the newest round's still-open fix-caused blockers are named.
	w.finding(t, "new2-fixed", "review2", "high", true)
	w.exec(t, `UPDATE review_findings SET status = 'resolved' WHERE id IN ('new1', 'new2-fixed')`)
	if got := w.convergence(t, st); got.RegressionRounds != 2 || !reflect.DeepEqual(got.FindingIDs, []string{"new2"}) {
		t.Fatalf("historical or resolved newest findings: %+v", got)
	}
	// The round is the parent fix's whole fan-out: the fixed verdict and
	// the new blocker may come from different reviewers.
	w.exec(t, `UPDATE runs SET verdicts = '{}' WHERE id = 'review2'`)
	w.phase(t, "other-reviewer", "review", "fix2", []string{"f"}, map[string]string{"f": "fixed"}, 8)
	if got := w.convergence(t, st); got.RegressionRounds != 2 || !reflect.DeepEqual(got.FindingIDs, []string{"new2"}) {
		t.Fatalf("cross-reviewer round evidence not counted: %+v", got)
	}
	w.exec(t, `UPDATE runs SET verdicts = '{}' WHERE id = 'other-reviewer'`)
	if got := w.convergence(t, st); got.RegressionRounds != 0 {
		t.Fatalf("round without a fixed blocker counted: %+v", got)
	}
	w.exec(t, `UPDATE runs SET verdicts = '{"f":"fixed"}' WHERE id = 'other-reviewer'`)
	w.exec(t, `UPDATE review_findings SET caused_by_fix = false WHERE run_id = 'review2'`)
	if got := w.convergence(t, st); got.RegressionRounds != 0 {
		t.Fatalf("round without a fix-caused blocker counted: %+v", got)
	}
	w.exec(t, `UPDATE review_findings SET caused_by_fix = true WHERE run_id = 'review2'`)
	w.exec(t, `UPDATE runs SET verdicts = '{"f":"fixed"}', finding_ids = '{}' WHERE id = 'review2'`)
	w.exec(t, `UPDATE runs SET finding_ids = '{}' WHERE id = 'other-reviewer'`)
	if got := w.convergence(t, st); got.RegressionRounds != 0 {
		t.Fatalf("unshown fixed finding counted: %+v", got)
	}
	w.exec(t, `UPDATE runs SET finding_ids = '{f}' WHERE id IN ('review2', 'other-reviewer')`)
	w.exec(t, `UPDATE runs SET finding_ids = '{}' WHERE id = 'fix2'`)
	if got := w.convergence(t, st); got.RegressionRounds != 0 {
		t.Fatalf("parent-unsent fixed finding counted: %+v", got)
	}
	w.exec(t, `UPDATE runs SET finding_ids = '{f}' WHERE id = 'fix2'`)
	w.exec(t, `UPDATE runs SET status = 'failed' WHERE id = 'origin'`)
	if got := w.convergence(t, st); got.RegressionRounds != 0 {
		t.Fatalf("fixed finding of an uncompleted review counted: %+v", got)
	}
	w.exec(t, `UPDATE runs SET status = 'completed' WHERE id = 'origin'`)
	st.Policy.BlockingSeverities = []string{"medium"}
	if got := w.convergence(t, st); got.RegressionRounds != 0 {
		t.Fatalf("policy severities ignored: %+v", got)
	}
}

func TestConvergenceTxSeesUncommittedRowsAndCandidateAnchors(t *testing.T) {
	w := newConvergenceWorld(t)
	st := w.state()
	st.Candidate = &delivery.Candidate{Branch: "candidate", Base: map[string]string{"api": "base", "web": "base-web"}}
	w.phase(t, "fix", "fix", "", []string{"f"}, nil, 1)
	w.phase(t, "review", "review", "fix", []string{"f"}, nil, 2)
	w.exec(t, `UPDATE runs SET branch = 'candidate' WHERE id = 'fix'`)
	for _, id := range []string{"origin", "review"} {
		w.exec(t, `UPDATE runs SET review_range = '{"of":"pull_request_fix","base":{"api":"base"}}' WHERE id = $1`, id)
	}
	w.in(t, func(tx pgx.Tx) error {
		if _, err := tx.Exec(context.Background(), `UPDATE runs SET verdicts = '{"f":"still"}' WHERE id = 'review'`); err != nil {
			return err
		}
		got, err := delivery.ConvergenceTx(context.Background(), tx, st)
		if err == nil && got.StillRounds["f"] != 1 {
			t.Errorf("subset anchors or uncommitted verdict lost: %+v", got)
		}
		return err
	})
	// A parent fix that published elsewhere is not this candidate's round.
	w.exec(t, `UPDATE runs SET branch = 'other-candidate' WHERE id = 'fix'`)
	if got := w.convergence(t, st); got.StillRounds["f"] != 0 {
		t.Fatalf("other branch's fix counted: %+v", got)
	}
	w.exec(t, `UPDATE runs SET branch = 'candidate' WHERE id = 'fix'`)
	st.Candidate.Base["api"] = "different-round"
	if got := w.convergence(t, st); got.StillRounds["f"] != 0 {
		t.Fatalf("old candidate counted: %+v", got)
	}
}

func TestConvergenceGroupsFanoutByFixCreationOrder(t *testing.T) {
	w := newConvergenceWorld(t)
	st := w.state()
	// Review completion order differs from the fix order; the second fix
	// has two reviewers, only one of which reports a regression.
	w.phase(t, "fix1", "fix", "", []string{"f"}, nil, 1)
	w.phase(t, "fix2", "fix", "", []string{"f"}, nil, 2)
	w.phase(t, "late-review", "review", "fix1", []string{"f"}, nil, 9)
	w.phase(t, "review2", "review", "fix2", []string{"f"}, map[string]string{"f": "fixed"}, 4)
	w.phase(t, "fanout2", "review", "fix2", nil, nil, 5)
	w.finding(t, "new", "review2", "high", true)
	if got := w.convergence(t, st); got.RegressionRounds != 1 || !reflect.DeepEqual(got.FindingIDs, []string{"new"}) {
		t.Fatalf("fan-out grouping: %+v", got)
	}
	w.exec(t, `UPDATE runs SET category = NULL WHERE id = 'review2'`)
	if got := w.convergence(t, st); got.RegressionRounds != 1 {
		t.Fatalf("child correctness category: %+v", got)
	}
	// A fallback reviewer (judgedBy) judges another category's finding, and
	// a fix-caused blocker may be reported by a different fan-out reviewer.
	w.exec(t, `UPDATE runs SET category = 'security' WHERE id = 'review2'`)
	w.exec(t, `UPDATE runs SET category = 'performance' WHERE id = 'fanout2'`)
	w.exec(t, `UPDATE review_findings SET run_id = 'fanout2', category = 'performance' WHERE id = 'new'`)
	if got := w.convergence(t, st); got.RegressionRounds != 1 || !reflect.DeepEqual(got.FindingIDs, []string{"new"}) {
		t.Fatalf("fallback or cross-category round: %+v", got)
	}
	// A child reviewer has no category; recordFindingsTx stores its own
	// word as the finding's category.
	w.exec(t, `UPDATE runs SET category = NULL WHERE id = 'fanout2'`)
	w.exec(t, `UPDATE review_findings SET category = 'security' WHERE id = 'new'`)
	if got := w.convergence(t, st); got.RegressionRounds != 1 || !reflect.DeepEqual(got.FindingIDs, []string{"new"}) {
		t.Fatalf("child reviewer's own category: %+v", got)
	}
}
