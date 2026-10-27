package delivery

import (
	"slices"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

// mergeFixture is a task whose test round run_c passed its one criterion
// at the feature branch's head, the task's own change app/greet.go on it;
// main then gains a commit of mainFiles. st is the delivery at the gate,
// before syncBeforePR.
func mergeFixture(t *testing.T, mainFiles map[string]string) (*verdictFixture, *State, workflow.StepContext) {
	t.Helper()
	f := newVerdictFixture(t)
	f.withRepo()
	head := f.gh.CommitFiles("feature", "Greet", map[string]string{"app/greet.go": "package app"})
	f.gh.CommitFiles("main", "Other work", mainFiles)
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`DELETE FROM pull_requests WHERE task_id = 'wi_c' AND organization_id = $1`, nil},
		{`INSERT INTO task_repositories (task_id, repository_id, organization_id) VALUES ('wi_c', 'repo_c', $1)`, nil},
		{`INSERT INTO workflow_runs (id, organization_id, workflow_type, idempotency_key, status, step, state, task_id)
			VALUES ('wf_c', $1, 'task.delivery', 'c', 'running', 'openPullRequest', '{}', 'wi_c')`, nil},
		{`UPDATE runs SET base_refs = jsonb_build_object('repo', $1::text) WHERE id = 'run_c'`, []any{head}},
		{`INSERT INTO acceptance_verdicts (organization_id, task_id, run_id, criterion, text, status, evidence)
			VALUES ($1, 'wi_c', 'run_c', 1, 'it greets', 'pass', '{greet.webm}')`, nil},
	} {
		args := q.args
		if args == nil {
			args = []any{f.org}
		}
		if _, err := f.owner.Exec(f.ctx, q.sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	p := DefaultPolicy()
	p.Test = true
	st := &State{TaskID: "wi_c", ProjectID: "prj_c", Attempt: 1, Policy: p, Branch: "feature", Draft: true,
		Heads: map[string]string{"repo": head}}
	return f, st, workflow.StepContext{WorkflowRunID: "wf_c", OrganizationID: f.org}
}

// merged runs syncBeforePR: GitHub merges main into the task branch, and
// the step goes to the test with the merge as its integration.
func (f *verdictFixture) merged(st *State, sc workflow.StepContext) {
	f.t.Helper()
	res, diverted, err := f.steps.syncBeforePR(f.ctx, sc, st)
	if err != nil || !diverted || res.Next != "test" || st.Integration == nil || !st.Integration.ServerMerge {
		f.t.Fatalf("syncBeforePR: %+v %v %v, integration %+v; want GitHub's merge, then the test", res, diverted, err, st.Integration)
	}
	if st.Heads["repo"] != f.gh.SHA("feature") {
		f.t.Fatalf("head %s, branch at %s", st.Heads["repo"], f.gh.SHA("feature"))
	}
}

// A clean merge of main before the pull request, of files the task did not
// change, after a passing test: the test step starts no tester, records
// the skip with the merge's paths and why, and the gate weighs the previous
// round. One that touches the task's file is tested.
func TestACleanMergeOfOtherFilesBeforeThePullRequestIsNotTestedAgain(t *testing.T) {
	for _, c := range []struct {
		name  string
		files map[string]string
		skip  bool
	}{
		{"other files", map[string]string{"lib/other.go": "package lib"}, true},
		// The same content: git merges it cleanly, and the path is the task's.
		{"the task's file", map[string]string{"app/greet.go": "package app"}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, st, sc := mergeFixture(t, c.files)
			f.merged(st, sc)
			var runs int
			_ = f.owner.QueryRow(f.ctx, `SELECT count(*) FROM runs WHERE task_id = 'wi_c'`).Scan(&runs)
			skip, err := f.steps.carryForward(f.ctx, sc, st, nil, false)
			if err != nil || skip != c.skip {
				t.Fatalf("skip %v (%v), want %v", skip, err, c.skip)
			}
			var paths []string
			var why string
			n := 0
			rows, err := f.owner.Query(f.ctx, `SELECT payload->>'why', payload->'paths' FROM events
				WHERE task_id = 'wi_c' AND event_type = $1 AND payload->>'fromRunId' = 'run_c'
				AND payload->'heads'->>'repo' = $2`, EvTestSkipped, st.Heads["repo"])
			if err != nil {
				t.Fatal(err)
			}
			for rows.Next() {
				n++
				if err := rows.Scan(&why, &paths); err != nil {
					t.Fatal(err)
				}
			}
			if !c.skip {
				if n != 0 {
					t.Fatalf("%d skips of a merge that touched the task's file", n)
				}
				return
			}
			if n != 1 || why != MergeUntouchedWhy || !slices.Equal(paths, []string{"repo/lib/other.go"}) || st.TestRunID != "run_c" {
				t.Fatalf("%d skips, why %q, paths %v, gate on %q", n, why, paths, st.TestRunID)
			}
			var after int
			_ = f.owner.QueryRow(f.ctx, `SELECT count(*) FROM runs WHERE task_id = 'wi_c'`).Scan(&after)
			if after != runs {
				t.Errorf("%d Runs created by a skip", after-runs)
			}
		})
	}
}
