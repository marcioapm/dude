package orchestrator_test

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
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

// prFixWorld delivers a task with the tester on to an open pull request,
// and arms a watch: once armed, every phase Run lux is asked for after it
// records the pull request branch's head as it was then. tester plays the
// candidate's tests (after arm), "" for the scripted pass.
type prFixWorld struct {
	*world
	wi, branch, before string
	// web's pull request head before the comment, with two repositories.
	webBefore      string
	armed          atomic.Bool
	mu             chan struct{}
	seen           []string // the PR branch's head at each Run lux started after arm
	seenWeb        []string // web's, the same way
	phases         []string
	candidateTests atomic.Int32
	// The candidate's tests, in order: the scripted tester's model each
	// plays; past the end, a pass.
	testers []string
	// Called as the candidate's first test starts.
	onTest func()
	// Changes what an armed Run does, by phase and its index among the
	// armed Runs of that phase (from 1).
	play func(phase string, n int, b *fakelux.Behaviour)
}

func newPRFixWorld(t *testing.T, testers ...string) *prFixWorld {
	return newPRFixWorldIn(t, false, testers...)
}

// newPRFixWorldIn is newPRFixWorld, across target and web (both writable,
// a pull request in each) when web is set.
func newPRFixWorldIn(t *testing.T, web bool, testers ...string) *prFixWorld {
	return newPRFixWorldAt(t, prAt{test: true, web: web}, testers...)
}

// newPRFixWorldAt is newPRFixWorld with the pull request opened at at.
func newPRFixWorldAt(t *testing.T, at prAt, testers ...string) *prFixWorld {
	w := &prFixWorld{world: newWorld(t), mu: make(chan struct{}, 1), testers: testers}
	scripted := w.lux.Decide
	count := map[string]int{}
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		labels, _ := spec["labels"].(map[string]any)
		phase, _ := labels["dude.phase"].(string)
		if !w.armed.Load() {
			return scripted(spec)
		}
		w.mu <- struct{}{}
		w.seen = append(w.seen, w.gh.SHA(w.branch))
		w.seenWeb = append(w.seenWeb, w.web.SHA(w.branch))
		w.phases = append(w.phases, phase)
		count[phase]++
		n := count[phase]
		<-w.mu
		b := scripted(spec)
		if phase == "test" {
			n := int(w.candidateTests.Add(1))
			if n == 1 && w.onTest != nil {
				w.onTest()
			}
			if n <= len(w.testers) {
				criteria, _ := strconv.Atoi(labels["dude.criteria"].(string))
				b.Reply = fakeagent.Tester(w.testers[n-1], criteria, 0).Reply
			}
		}
		if w.play != nil {
			w.play(phase, n, &b)
		}
		return b
	}
	// A reviewer the fake lux plays as the scripted one, whose spec carries
	// dude's own prompt.
	w.onModel("reviewer", "llm-reviewer")
	w.onModel("qa_browser", "fake/scripted")
	w.wi = w.task()
	mustExec(t, w.owner, `UPDATE tasks SET acceptance_criteria = '["it greets", "it says goodbye"]' WHERE id = $1`, w.wi)
	w.prOpen(w.wi, at)
	w.branch = w.gh.Pull(1).Head
	w.before = w.gh.SHA(w.branch)
	w.webBefore = w.web.SHA(w.branch)
	return w
}

// testingAcross is testing on a task that changes target and web.
func (w *world) testingAcross(model string) string {
	w.t.Helper()
	w.onModel("qa_browser", model)
	wi := w.task()
	mustExec(w.t, w.owner, `UPDATE tasks SET acceptance_criteria = '["it greets", "it says goodbye"]' WHERE id = $1`, wi)
	w.addWeb(wi, "write")
	p := scriptedPolicy()
	p.Test = true
	w.start(wi, p)
	return wi
}

// comment arms the watch and has a collaborator ask for a change.
func (w *prFixWorld) comment(body string) {
	w.armed.Store(true)
	w.gh.Comment(1, "alice", body)
}

// personAsked waits, its agents done (agentsDone), for the delivery to stop
// for a person.
func (w *prFixWorld) personAsked() {
	w.t.Helper()
	w.agentsDone(w.wi, "a person asked", func() bool { return w.taskStatus(w.wi) == "awaiting_input" })
}

// shownBeforeTheGate is every head the pull request's branch had while a
// Run of the fix's check started, other than where it was before the fix.
func (w *prFixWorld) shownBeforeTheGate() []string {
	w.mu <- struct{}{}
	defer func() { <-w.mu }()
	var out []string
	for i, sha := range w.seen {
		if sha != w.before {
			out = append(out, w.phases[i]+"@"+sha)
		}
	}
	return out
}

func (w *prFixWorld) candidate() string { return w.gh.SHA(delivery.CandidateBranch(w.wi, 1)) }

// state reads the delivery's state field as JSON text.
func (w *prFixWorld) state(field string) string {
	var v string
	_ = w.owner.QueryRow(context.Background(), `SELECT COALESCE(state->>$2, '') FROM workflow_runs WHERE task_id = $1`, w.wi, field).Scan(&v)
	return v
}

// The fix of the simplifier's diff changes a repository the simplifier did
// not: that repository joins the range its re-review is told, from where
// it stood before the fix.
func TestARepositoryAFixOfTheSimplifiersDiffChangesFirstIsReviewed(t *testing.T) {
	w := newWorld(t)
	w.onModel("reviewer", "llm-reviewer")
	scripted := w.lux.Decide
	var simplifyReviews, fixes atomic.Int32
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		labels, _ := spec["labels"].(map[string]any)
		if rangeReview(spec) == delivery.RangeSimplify {
			if simplifyReviews.Add(1) == 1 {
				return fakelux.Behaviour{Reply: simplifierFinding}
			}
			return fakelux.Behaviour{Reply: "Reviewed the fix of the simplification.\n\n" + fakeagent.Verdict}
		}
		b := scripted(spec)
		if labels["dude.phase"] == "fix" && fixes.Add(1) == 2 {
			b.Commit = map[string]string{"web:UNREVIEWED.md": "the fix of the simplification\n"}
		}
		return b
	}
	wi := w.testingAcross("fake/scripted")
	w.until("a pull request in each repository", func() bool { return len(w.gh.Pulls()) == 1 && len(w.web.Pulls()) == 1 })

	var webBefore, webFixed string
	_ = w.owner.QueryRow(context.Background(), `SELECT base_refs->>'web', heads->'web'->>'sha' FROM runs
		WHERE task_id = $1 AND phase = 'fix' ORDER BY created_at DESC LIMIT 1`, wi).Scan(&webBefore, &webFixed)
	if webBefore == "" || webFixed == "" || webFixed == webBefore {
		t.Fatalf("the simplifier's fix did not change web: from %q to %q\n%s", webBefore, webFixed, w.describeRuns())
	}
	specs := w.rangeSpecs(delivery.RangeSimplify)
	if len(specs) != 2 {
		t.Fatalf("%d reviews of the simplifier's diff", len(specs))
	}
	if !strings.Contains(specs[1].Workload.Prompt, "in `web`, `"+webBefore+"..HEAD`") {
		t.Errorf("the re-review is not told web's range from %s:\n%s", webBefore, specs[1].Workload.Prompt)
	}
	if !strings.Contains(specs[0].Workload.Prompt, "Review in `target`, `") {
		t.Errorf("a range among two checkouts names no repository:\n%s", specs[0].Workload.Prompt)
	}
	// web's pull request opened at the fix the re-review checked out.
	if at := w.web.SHA(delivery.BranchFor(wi, 1)); at != webFixed {
		t.Errorf("web's pull request is at %s, the reviewed fix %s", at, webFixed)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review' AND status = 'completed'
		AND review_range->>'of' = $2 AND review_range->'base'->>'web' = $3 AND base_refs->>'web' = $4`,
		wi, delivery.RangeSimplify, webBefore, webFixed); n != 1 {
		t.Errorf("%d reviews of web's fix told its range", n)
	}
}
