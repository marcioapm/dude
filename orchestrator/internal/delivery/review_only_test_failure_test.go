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
	for _, c := range []struct{ name, reply string }{
		{"failed", "```yaml\n---\ncriterion: 1\nstatus: fail\nobserved: No greeting.\n```\n"},
		{"blocked", "```yaml\n---\ncriterion: 1\nstatus: blocked\nobserved: It did not start.\n```\n"},
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
			if got.Verdict == nil || got.Verdict.TestHead != "abc1234" || !strings.Contains(got.Verdict.Test, "run_c") {
				t.Fatalf("the verdict was not told of the test: %+v", got.Verdict)
			}
			if cs := f.gh.Pull(f.number).Comments; len(cs) != 0 {
				t.Fatalf("the test's verdict comment was posted on a review-only task's pull request (the status comment's marker): %+v", cs)
			}
			// And the verdict never approves past it.
			d := decideVerdict(verdictInput{Gate: gateAt(), Test: got.Verdict.Test})
			if d.Event != forge.EventComment || !strings.Contains(d.Headline, "run_c") {
				t.Fatalf("with the test %s: %+v", c.name, d)
			}
		})
	}
}
