package orchestrator_test

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakeagent"
	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
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

// fixReviewed waits, its agents done (agentsDone), for the fix on the pull
// request's branch and the task back in review.
func (w *prFixWorld) fixReviewed() {
	w.t.Helper()
	w.agentsDone(w.wi, "the fix on the pull request", func() bool {
		return w.gh.SHA(w.branch) != w.before && w.taskStatus(w.wi) == "review"
	})
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

// refreshedBeforeTheMove counts the description refreshes about heads past
// before (the candidate's) recorded before the pull request took the fix:
// a candidate's verdict in the description of a pull request without it.
func (w *prFixWorld) refreshedBeforeTheMove() int {
	return w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'pull_request.verification_updated'
		AND payload->>'head' <> $2 AND cursor < COALESCE((SELECT min(cursor) FROM events WHERE task_id = $1
		AND event_type = 'pull_request.fix_moved'), 9223372036854775807)`, w.wi, w.before)
}

// lastPrompt is the prompt of the last of specs, "" for none.
func lastPrompt(specs []lux.Spec) string {
	if len(specs) == 0 {
		return ""
	}
	return specs[len(specs)-1].Workload.Prompt
}

// findingOnTheCandidatesSimplification is a play: the candidate's second
// review, of the simplifier's diff, raises simplifierFinding, and the
// third, of its fix, clears it.
func findingOnTheCandidatesSimplification(phase string, n int, b *fakelux.Behaviour) {
	switch {
	case phase == "review" && n == 2:
		b.Reply = simplifierFinding
	case phase == "review" && n == 3:
		b.Reply = "Reviewed the fix of the simplification.\n\n" + fakeagent.Verdict
	}
}

func (w *prFixWorld) candidate() string { return w.gh.SHA(delivery.CandidateBranch(w.wi, 1)) }

// state reads the delivery's state field as JSON text.
func (w *prFixWorld) state(field string) string {
	var v string
	_ = w.owner.QueryRow(context.Background(), `SELECT COALESCE(state->>$2, '') FROM workflow_runs WHERE task_id = $1`, w.wi, field).Scan(&v)
	return v
}

// rangesNaming are the prompts of the task's range reviews of of that
// tell the reviewer repo's range from base.
func (w *world) rangesNaming(of, repo, base string) int {
	n := 0
	for _, spec := range w.rangeSpecs(of) {
		if strings.Contains(spec.Workload.Prompt, "in `"+repo+"`, `"+base+"..HEAD`") {
			n++
		}
	}
	return n
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

// Someone pushes between promotion's read of a branch and its move: the
// move is refused, the person's commit kept, and a pull request already
// moved is recorded at its new head, its description about it.
func TestAPushRacingThePromotionIsNeverOverwritten(t *testing.T) {
	t.Skip("candidate path: deleted in B2")
	w := newPRFixWorldIn(t, true)
	w.play = func(phase string, n int, b *fakelux.Behaviour) {
		if phase == "fix" && n == 1 {
			b.Commit = map[string]string{"*:" + fakeagent.FixedFile: "addressed for both"}
		}
	}
	var raced atomic.Bool
	var theirs atomic.Value
	w.web.Set(func(s *fakegithub.Server) {
		s.Intercept = func(r *http.Request) int {
			if r.Method == "PATCH" && strings.HasSuffix(r.URL.Path, "/git/refs/heads/"+w.branch) && raced.CompareAndSwap(false, true) {
				theirs.Store(w.web.CommitOnTop(w.branch, "A person's own fix"))
			}
			return 0
		}
	})
	webBody := w.web.Pull(1).Body
	w.comment("Please rename the greeting.")
	w.sync()
	w.until("a person asked", func() bool { return w.taskStatus(w.wi) == "awaiting_input" })
	if r := w.escalationReason(w.wi); r != delivery.ReasonPullRequestMoved {
		t.Fatalf("escalated %q\n%s", r, w.describeRuns())
	}
	person, _ := theirs.Load().(string)
	if at := w.web.SHA(w.branch); person == "" || at != person {
		t.Errorf("web is at %s, the person's commit %q", at, person)
	}
	var detail map[string]any
	_ = w.owner.QueryRow(context.Background(), `SELECT payload->'detail' FROM events WHERE task_id = $1
		AND event_type = 'question.asked' ORDER BY cursor DESC LIMIT 1`, w.wi).Scan(&detail)
	if detail["repo"] != "web" || detail["found"] != person || detail["expected"] != w.webBefore {
		t.Errorf("detail = %v", detail)
	}
	// target moved before the race: recorded, and described, at its head.
	moved := w.gh.SHA(w.branch)
	if moved == w.before {
		t.Fatal("target did not move before web's race")
	}
	var stored string
	_ = w.owner.QueryRow(context.Background(), `SELECT COALESCE(pr.head_sha, '') FROM pull_requests pr
		JOIN repositories r ON r.id = pr.repository_id WHERE pr.task_id = $1 AND r.name = 'target'`, w.wi).Scan(&stored)
	if stored != moved {
		t.Errorf("target's pull request is recorded at %s; GitHub has %s", stored, moved)
	}
	var heads map[string]string
	if err := json.Unmarshal([]byte(w.state("heads")), &heads); err != nil {
		t.Fatal(err)
	}
	if heads["target"] != moved {
		t.Errorf("delivery retained target at %s; GitHub has %s", heads["target"], moved)
	}
	if body := w.gh.Pull(1).Body; !strings.Contains(body, "target `"+moved[:7]+"`") {
		t.Errorf("target's Verification is not about %s:\n%s", moved[:7], body)
	}
	// web refused the candidate: its description is not about it.
	if b := w.web.Pull(1).Body; b != webBody {
		t.Errorf("web's description changed though web never took the fix:\n%s\nwas:\n%s", b, webBody)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'pull_request.fix_moved'
		AND payload->>'repo' = 'target' AND payload->>'to' = $2`, w.wi, moved); n != 1 {
		t.Errorf("%d moves of target recorded", n)
	}
}

// A person's push after the promotion's GET but before its PATCH returns
// 422: the escalation names their SHA, not the candidate's or the old head.
func TestAPushBeforeTheRefPatchEscalatesWithThePersonsHead(t *testing.T) {
	t.Skip("candidate path: deleted in B2")
	w := newPRFixWorld(t)
	var raced atomic.Bool
	var theirs atomic.Value
	w.gh.Set(func(s *fakegithub.Server) {
		s.Intercept = func(r *http.Request) int {
			if r.Method == "PATCH" && strings.HasSuffix(r.URL.Path, "/git/refs/heads/"+w.branch) && raced.CompareAndSwap(false, true) {
				theirs.Store(w.gh.CommitOnTop(w.branch, "A person's racing push"))
			}
			return 0
		}
	})
	w.comment("Please rename the greeting.")
	w.personAsked()
	person, _ := theirs.Load().(string)
	if r := w.escalationReason(w.wi); r != delivery.ReasonPullRequestMoved {
		t.Fatalf("escalated %q\n%s", r, w.describeRuns())
	}
	var detail map[string]any
	if err := w.owner.QueryRow(context.Background(), `SELECT payload->'detail' FROM events WHERE task_id = $1
		AND event_type = 'question.asked' ORDER BY cursor DESC LIMIT 1`, w.wi).Scan(&detail); err != nil {
		t.Fatal(err)
	}
	if person == "" || w.gh.SHA(w.branch) != person || detail["found"] != person || detail["expected"] != w.before {
		t.Errorf("person's head %q, branch %s, escalation %v", person, w.gh.SHA(w.branch), detail)
	}
}

func TestADeletionBeforeTheRefPatchKeepsTheCheckedCandidate(t *testing.T) {
	t.Skip("candidate path: deleted in B2")
	for _, status := range []int{http.StatusUnprocessableEntity, http.StatusNotFound} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			w := newPRFixWorld(t)
			var raced atomic.Bool
			var creates atomic.Int32
			var checked string
			// The pull request stays open: one merged meanwhile ends the
			// delivery before any promotion (pr_ended_test.go).
			w.onTest = func() { checked = w.candidate() }
			w.gh.Set(func(s *fakegithub.Server) {
				s.Intercept = func(r *http.Request) int {
					if raced.Load() && r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/git/refs") {
						creates.Add(1)
					}
					if r.Method == "PATCH" && strings.HasSuffix(r.URL.Path, "/git/refs/heads/"+w.branch) && raced.CompareAndSwap(false, true) {
						if err := w.gh.DeleteBranch(w.branch); err != nil {
							t.Errorf("delete source branch: %v", err)
						}
						if status == http.StatusNotFound {
							return status
						}
					}
					return 0
				}
			})
			w.comment("Please rename the greeting.")
			w.agentsDone(w.wi, "promotion settled", func() bool {
				return raced.Load() && (w.taskStatus(w.wi) == "awaiting_input" || len(w.gh.Pulls()) > 1)
			})
			if at := w.gh.SHA(w.branch); at != "" {
				t.Errorf("deleted source branch recreated at %s", at)
			}
			if n := creates.Load(); n != 0 {
				t.Errorf("%d create-ref fallbacks during promotion", n)
			}
			if at := w.candidate(); checked == "" || at != checked {
				t.Errorf("checked candidate %q was not retained: %q", checked, at)
			}
			if pulls := w.gh.Pulls(); len(pulls) != 1 {
				t.Errorf("created a follow-up after source deletion: %v", pulls)
			}
			if p := w.gh.Pull(1); p.State != "open" {
				t.Errorf("the pull request is %s, want open: %+v", p.State, p)
			}
			if r := w.escalationReason(w.wi); r != delivery.ReasonPullRequestMoved {
				t.Fatalf("escalated %q\n%s", r, w.describeRuns())
			}
			var detail map[string]any
			if err := w.owner.QueryRow(context.Background(), `SELECT payload->'detail' FROM events WHERE task_id = $1
				AND event_type = 'question.asked' ORDER BY cursor DESC LIMIT 1`, w.wi).Scan(&detail); err != nil {
				t.Fatal(err)
			}
			if detail["found"] != "" || detail["expected"] != w.before || detail["candidate"] != checked ||
				detail["branch"] != delivery.CandidateBranch(w.wi, 1) || detail["repo"] != "target" {
				t.Errorf("missing-ref refusal = %v", detail)
			}
		})
	}
}
