package delivery

import "testing"

// The pull request loop ended (its pull request merged) keeps nothing of
// a pull request fix it was in: not the fix's range, nor its simplifier's
// one retry spent.
func TestEndingThePullRequestLoopForgetsTheFixLoop(t *testing.T) {
	r := newLaterRound(t)
	r.exec(`INSERT INTO pull_requests (id, organization_id, project_id, task_id, repository_id, number, url, head_branch, base_branch, head_sha, title, state)
		VALUES ('pr_rr', $1, 'prj_rr', 'wi_rr', 'repo_rr', 1, 'https://github.com/acme/repo/pull/1', 'dude/wi_rr/attempt-1', 'main', 'bbb', 'T', 'merged')`)
	r.st.PullRequestIDs = []string{"pr_rr"}
	r.st.FixRange = map[string]string{"repo": "aaa"}
	r.st.SimplifyRetried = true
	states, err := r.steps.s.PullRequestStates(r.ctx, r.org, r.st.PullRequestIDs)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.steps.endLoop(r.ctx, r.sc("awaitPullRequest"), r.st, states); err != nil {
		t.Fatal(err)
	}
	if r.st.FixRange != nil || r.st.SimplifyRetried {
		t.Errorf("after the loop ended: FixRange %v, SimplifyRetried %v; want neither", r.st.FixRange, r.st.SimplifyRetried)
	}
}
