package delivery

import (
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

// A review-only task's tester failing (or blocked): never a writer. The
// failure goes to the verdict, which never approves past it and names it
// in the review; the status comment is the verdict's, not edited by the
// test's verdict comment (C-M3, C-L5).
func TestAReviewOnlyTestFailureGoesToTheVerdictNotAFixer(t *testing.T) {
	for _, c := range []struct{ name, reply, observed string }{
		{"failed", "```yaml\n---\ncriterion: 1\nstatus: fail\nobserved: No greeting.\n```\n", "No greeting."},
		{"blocked", "```yaml\n---\ncriterion: 1\nstatus: blocked\nobserved: It did not start.\n```\n", "It did not start."},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newVerdictFixture(t)
			if _, err := f.owner.Exec(f.ctx, `UPDATE events SET payload = jsonb_build_object('text', $1::text) WHERE id = 'ev_c'`, c.reply); err != nil {
				t.Fatal(err)
			}
			st := &State{TaskID: "wi_c", ProjectID: "prj_c", Attempt: 1, TestRunID: "run_c", Policy: DefaultPolicy(),
				PullRequestIDs: []string{"pr_c"}, Heads: map[string]string{"repo": "abc1234"},
				Source: &Source{Kind: SourcePR, Mode: ModeReviewOnly, RepositoryID: "repo_c", Repository: "repo", Number: f.number,
					HeadBranch: "feature", BaseBranch: "main"}}
			res, err := f.verdictStep(st)
			if err != nil {
				t.Fatal(err)
			}
			if res.Next != "verdict" {
				t.Fatalf("a review-only task's %s test goes to %q, want the verdict (never a writer)", c.name, res.Next)
			}
			got := res.State.(*State)
			if got.Verdict == nil || got.Verdict.TestHead != "abc1234" || got.Verdict.Test == "" || len(got.Verdict.TestNotes) != 1 {
				t.Fatalf("the verdict was not told of the test: %+v", got.Verdict)
			}
			if cs := f.gh.Pull(f.number).Comments; len(cs) != 0 {
				t.Fatalf("the test's verdict comment was posted on a review-only task's pull request (the status comment's marker): %+v", cs)
			}
			// And the verdict never approves past it.
			d := decideVerdict(verdictInput{Gate: gateAt(), Test: got.Verdict.Test, TestNotes: got.Verdict.TestNotes})
			if d.Event != forge.EventComment || d.Reason != reasonTestFailed {
				t.Fatalf("with the test %s: %+v", c.name, d)
			}
			// L-B: the review names the criterion and what the tester
			// observed, not an internal run id and a bare number.
			body, _, _ := verdictReviewOf(d, "abc1234", "main", nil, nil, "")
			for _, want := range []string{"it greets", c.observed} {
				if !strings.Contains(body, want) {
					t.Errorf("the review lacks %q:\n%s", want, body)
				}
			}
			if strings.Contains(body, "run_c") {
				t.Errorf("the review names the test by its run id:\n%s", body)
			}
		})
	}
}
