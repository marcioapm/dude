package delivery

import (
	"slices"
	"testing"
)

// largeRound is a later review round whose change since run_rr1's round is
// over MaxResumeDeltaLines, with the policy resuming rounds: its reviewer
// is made fresh (large_delta).
func largeRound(t *testing.T) *laterRound {
	r := newLaterRound(t, "app/greet.go").firstRound()
	r.files[0]["additions"] = MaxResumeDeltaLines
	r.policy(`{"resumeRounds":true,"requiredReviewers":["correctness"]}`)
	return r
}

// reviewRangeOf is the review_range->>'of' of the round's correctness
// reviewer, "none" for NULL, and the fresh reason its run.created records.
func (r *laterRound) reviewRangeOf() (of, fresh string) {
	r.t.Helper()
	if err := r.owner.QueryRow(r.ctx, `SELECT COALESCE(x.review_range->>'of', 'none'), COALESCE(e.payload->>'fresh', '')
		FROM runs x JOIN events e ON e.run_id = x.id AND e.event_type = 'run.created'
		WHERE x.task_id = 'wi_rr' AND x.id <> 'run_rr1' AND x.category = 'correctness'`).Scan(&of, &fresh); err != nil {
		r.t.Fatal(err)
	}
	return of, fresh
}

// A fresh reviewer for a large change keeps the range of a pull request's
// fix loop on its candidate (the fix's, or its simplifier's diff's): the
// loop's fix is sent only that range's findings. Off a candidate the range
// (a simplifier's diff) is dropped for the whole branch, as before.
func TestAFreshLargeDeltaReviewerKeepsTheCandidateLoopsRange(t *testing.T) {
	for _, c := range []struct {
		name      string
		candidate bool
		simplify  bool
		want      string
	}{
		{"the fix on a candidate", true, false, RangePullRequestFix},
		{"its simplifier's diff on a candidate", true, true, RangeSimplify},
		{"a simplifier's diff with no pull request", false, true, "none"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := largeRound(t)
			if c.candidate {
				r.onCandidate()
			}
			if c.simplify {
				r.st.SimplifyBase = map[string]string{"repo": "aaa"}
			}
			r.review()
			if of, fresh := r.reviewRangeOf(); of != c.want || fresh != "large_delta" {
				t.Fatalf("range %q, fresh %q; want %q, large_delta", of, fresh, c.want)
			}
		})
	}
}

// A pull request's fix loop on its candidate weighs only the findings it
// would send a fix: an open blocking finding of a review outside its range
// (run_rr1, of the whole branch) does not keep the loop going. One of the
// loop's own does. The same holds for reviewExit, which a person's accept of
// a not_converging escalation on the candidate goes to.
func TestACandidateLoopsExitWeighsOnlyTheFindingsItsFixIsSent(t *testing.T) {
	type exitCase struct {
		name string
		step string
		of   string
		want string
	}
	var cases []exitCase
	for _, step := range []string{"awaitReview", "reviewExit"} {
		cases = append(cases,
			exitCase{step + " outside the candidate's range", step, "", "simplify"},
			exitCase{step + " of the candidate's fix", step, RangePullRequestFix, "fix"})
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newLaterRound(t, "app/greet.go").firstRound()
			r.onCandidate()
			r.exec(`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, kind, category,
					base_refs, review_range, ended_at)
				VALUES ('run_rr2', $1, 'prj_rr', 'wi_rr', 1, 'completed', 'review', 'reviewer', 'agent', 'correctness',
					'{"repo":"bbb"}', '{"of":"pull_request_fix","base":{"repo":"aaa"}}', now())`)
			from := "run_rr1"
			if c.of != "" {
				from = "run_rr2"
			}
			r.exec(`INSERT INTO review_findings (id, organization_id, task_id, run_id, severity, category, title, description, status)
				VALUES ('fnd_b', $1, 'wi_rr', $2, 'blocking', 'correctness', 'T', 'd', 'open')`, from)
			r.st.ReviewRunIDs = []string{"run_rr2"}
			exit := r.steps.awaitReview
			if c.step == "reviewExit" {
				exit = r.steps.reviewExit
			}
			res, err := exit(r.ctx, r.sc(c.step))
			if err != nil {
				t.Fatal(err)
			}
			if res.Next != c.want {
				t.Fatalf("after the round: %q, want %q", res.Next, c.want)
			}
			if c.want != "fix" {
				return
			}
			fix, err := r.steps.fix(r.ctx, r.sc("fix"))
			if err != nil {
				t.Fatal(err)
			}
			var sent []string
			if err := r.owner.QueryRow(r.ctx, `SELECT finding_ids FROM runs WHERE task_id = 'wi_rr' AND phase = 'fix'`).Scan(&sent); err != nil {
				t.Fatalf("no fix Run (%+v): %v", fix, err)
			}
			if !slices.Equal(sent, []string{"fnd_b"}) {
				t.Errorf("the fix was sent %v, want the loop's finding", sent)
			}
		})
	}
}
