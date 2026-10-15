package orchestrator_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakeagent"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// simplifierFinding is what the review of the simplifier's diff raises,
// once, in the scripted reviewer's format.
const simplifierFinding = "One problem:\n\n```yaml\n---\nseverity: blocking\ncategory: correctness\nfile: SIMPLE.md\nline: 1\n" +
	"title: The simplification drops the greeting's fallback\n" +
	"description: \"Traced: SIMPLE.md:1 removes the default the greeting used.\"\nsuggested_fix: Put the fallback back.\n```\n"

// rangeSpecs are the lux specs of the task's reviews limited to a range
// of, in the order they were submitted.
func (w *world) rangeSpecs(of string) []lux.Spec {
	var out []lux.Spec
	for _, r := range w.lux.Runs() {
		var spec lux.Spec
		_ = json.Unmarshal(r.Spec, &spec)
		if spec.Labels["dude.phase"] == "review" && spec.Labels["dude.review_range"] == of {
			out = append(out, spec)
		}
	}
	return out
}

// The simplifier changes code nobody reviewed: a review of its diff alone,
// by the policy's required reviewers, told the range from the head before
// it to HEAD. Its finding goes to the fixer; the fix is reviewed (over the
// same range), the simplifier does not run again, and the test then the
// pull request follow.
func TestTheSimplifiersDiffIsReviewedAndItsFindingFixedBeforeTheTest(t *testing.T) {
	w := newWorld(t)
	// A reviewer on a model the fake lux plays as the scripted one, whose
	// spec carries dude's own prompt rather than a script.
	w.onModel("reviewer", "llm-reviewer")
	scripted := w.lux.Decide
	var simplifyReviews atomic.Int32
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		if rangeReview(spec) == delivery.RangeSimplify {
			if simplifyReviews.Add(1) == 1 {
				return fakelux.Behaviour{Reply: simplifierFinding}
			}
			return fakelux.Behaviour{Reply: "Reviewed the fix of the simplification.\n\n" + fakeagent.Verdict}
		}
		return scripted(spec)
	}
	wi := w.testing("fake/scripted")
	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })

	if n := w.phaseRuns(wi, "simplify"); n != 1 {
		t.Errorf("%d simplifications, want one: never again after its diff's fix", n)
	}
	specs := w.rangeSpecs(delivery.RangeSimplify)
	if len(specs) != 2 {
		t.Fatalf("%d reviews of the simplifier's diff, want its round and the fix's re-review\n%s", len(specs), w.describeRuns())
	}
	// From the head before the simplifier: the review loop's fix.
	var before, simplified string
	_ = w.owner.QueryRow(context.Background(), `SELECT heads->'target'->>'sha' FROM runs WHERE task_id = $1 AND phase = 'fix'
		ORDER BY created_at LIMIT 1`, wi).Scan(&before)
	_ = w.owner.QueryRow(context.Background(), `SELECT heads->'target'->>'sha' FROM runs WHERE task_id = $1 AND phase = 'simplify'`,
		wi).Scan(&simplified)
	for i, spec := range specs {
		for _, want := range []string{"Review `" + before + "..HEAD`:", "behaviour-preservation check of the simplifier's diff"} {
			if !strings.Contains(spec.Workload.Prompt, want) {
				t.Errorf("review %d of the simplifier's diff is not told %q:\n%s", i+1, want, spec.Workload.Prompt)
			}
		}
		if strings.Contains(spec.Workload.Prompt, "`git merge-base`") {
			t.Errorf("review %d of the simplifier's diff was sent to the whole branch", i+1)
		}
	}
	if specs[0].Labels["dude.review_range"] != delivery.RangeSimplify {
		t.Errorf("labels = %v", specs[0].Labels)
	}
	// The first saw the simplifier's commit; its finding went to the fixer,
	// and the re-review judged it fixed.
	var status, fixedBy string
	_ = w.owner.QueryRow(context.Background(), `SELECT f.status::text, COALESCE((SELECT r.id FROM runs r WHERE r.task_id = f.task_id
			AND r.phase = 'fix' AND f.id = ANY(r.finding_ids)), '')
		FROM review_findings f WHERE f.task_id = $1 AND f.title LIKE 'The simplification drops%'`, wi).Scan(&status, &fixedBy)
	if status != "resolved" || fixedBy == "" {
		t.Errorf("the simplifier's finding is %q, sent to fix %q; want resolved after a fix", status, fixedBy)
	}
	if n := w.phaseRuns(wi, "fix"); n != 2 {
		t.Errorf("%d fixes, want the review loop's and the simplifier's diff's", n)
	}
	// Then the tester, on what the pull request opened with.
	var tested string
	_ = w.owner.QueryRow(context.Background(), `SELECT base_refs->>'target' FROM runs WHERE task_id = $1 AND phase = 'test'`, wi).Scan(&tested)
	if head := w.branchHead(wi); tested != head || tested == simplified {
		t.Errorf("tested %s; the branch is at %s, the simplifier left %s: want the fix of its diff tested", tested, head, simplified)
	}
	if order := w.phaseOrder(wi); !slices.Equal(order, []string{"implement", "review", "fix", "review", "simplify", "review", "fix", "review", "test"}) {
		t.Errorf("phases = %v", order)
	}
}

// A simplifier that changes nothing adds no review: straight to the test.
func TestASimplifierThatChangesNothingAddsNoReview(t *testing.T) {
	w := newWorld(t)
	scripted := w.lux.Decide
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] == "simplify" {
			return fakelux.Behaviour{Reply: "Simple enough already."}
		}
		return scripted(spec)
	}
	wi := w.testing("fake/scripted")
	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	if order := w.phaseOrder(wi); !slices.Equal(order, []string{"implement", "review", "fix", "review", "simplify", "test"}) {
		t.Errorf("phases = %v", order)
	}
	if n := len(w.rangeSpecs(delivery.RangeSimplify)); n != 0 {
		t.Errorf("%d reviews of a simplification that changed nothing", n)
	}
}

// phaseOrder is the task's phase Runs, oldest first.
func (w *world) phaseOrder(wi string) []string {
	w.t.Helper()
	rows, err := w.owner.Query(context.Background(), `SELECT phase::text FROM runs WHERE task_id = $1 AND phase IS NOT NULL
		AND NOT (phase = 'review' AND category IS DISTINCT FROM 'correctness') ORDER BY created_at, id`, wi)
	if err != nil {
		w.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		_ = rows.Scan(&p)
		out = append(out, p)
	}
	return out
}
