package delivery

import (
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

// skips counts the task's test.skipped events.
func (f *verdictFixture) skips() int {
	f.t.Helper()
	var n int
	if err := f.owner.QueryRow(f.ctx, `SELECT count(*) FROM events WHERE task_id = 'wi_c' AND event_type = $1`, EvTestSkipped).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

// Each carryForward condition still holds for a clean merge of other
// files: a fix of a failed test due, a directed test, a finding to judge,
// a previous round that did not pass, criteria edited since, and a
// previous round at heads other than the merge's first parent each retest.
func TestACleanMergeIsTestedWhenAnyOtherConditionFails(t *testing.T) {
	other := map[string]string{"lib/other.go": "package lib"}
	for _, c := range []struct {
		name     string
		set      func(f *verdictFixture, st *State)
		judge    []string
		directed bool
	}{
		{"none (the skip happens)", nil, nil, false},
		{"a fix of a failed test due", func(_ *verdictFixture, st *State) { st.RetestDue = true }, nil, false},
		{"a directed test", nil, nil, true},
		{"a finding to judge", nil, []string{"fnd_1"}, false},
		{"a previous round that failed", func(f *verdictFixture, _ *State) {
			f.exec(`UPDATE acceptance_verdicts SET status = 'fail' WHERE run_id = 'run_c' AND organization_id = $1`)
		}, nil, false},
		{"a criterion edited since", func(f *verdictFixture, _ *State) {
			f.exec(`UPDATE tasks SET acceptance_criteria = '["it greets by name"]' WHERE id = 'wi_c' AND organization_id = $1`)
		}, nil, false},
		{"a previous round at other heads", func(f *verdictFixture, _ *State) {
			f.exec(`UPDATE runs SET base_refs = '{"repo": "0000000000000000000000000000000000000000"}' WHERE id = 'run_c' AND organization_id = $1`)
		}, nil, false},
		// The same merge, not known as MergeBase's answer: a head adopted, or
		// a delivery's state from before ServerMerge was recorded.
		{"a merge not answered by MergeBase", func(_ *verdictFixture, st *State) { st.Integration.ServerMerge = false }, nil, false},
		{"an agent's integration", func(_ *verdictFixture, st *State) { st.Integration.RunID = "run_integration" }, nil, false},
		// Each of the merge's other marks, alone: one after the pull request,
		// one not published (a prFix's), one on an open pull request.
		{"an integration not before the pull request", func(_ *verdictFixture, st *State) { st.Integration.BeforePR = false }, nil, false},
		{"an integration not published", func(_ *verdictFixture, st *State) { st.Integration.Published = false }, nil, false},
		{"an integration on pull request 7", func(_ *verdictFixture, st *State) { st.Integration.Number = 7 }, nil, false},
		// The person's Draft at the heads before the merge; then a commit
		// lands on the merged branch (a publish while the step waited).
		{"the merged repo's head moved since the merge", func(f *verdictFixture, st *State) {
			f.setGate(map[string]string{"repo": st.Integration.Head}, true)
			st.Heads["repo"] = f.gh.CommitFiles("feature", "A fix", map[string]string{"app/greet.go": "package app // fixed"})
		}, nil, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, st, sc := mergeFixture(t, other)
			f.merged(st, sc)
			if c.set != nil {
				c.set(f, st)
			}
			gate := f.gate()
			skip, err := f.steps.carryForward(f.ctx, sc, st, c.judge, c.directed)
			want := strings.HasPrefix(c.name, "none")
			if err != nil || skip != want || (f.skips() == 1) != want {
				t.Fatalf("skip %v (%v), %d skips; want skip %v", skip, err, f.skips(), want)
			}
			if after := f.gate(); !want && !reflect.DeepEqual(after, gate) {
				t.Fatalf("gate moved from %+v to %+v without a skip", gate, after)
			}
		})
	}
}

// setGate records the person's Open or Draft at heads in the delivery's row.
func (f *verdictFixture) setGate(heads map[string]string, draft bool) {
	f.t.Helper()
	raw, _ := json.Marshal(GateAt{Heads: heads, Draft: draft})
	if _, err := f.owner.Exec(f.ctx, `UPDATE workflow_runs SET state = jsonb_set(state, '{gateAt}', $1::jsonb) WHERE id = 'wf_c'`, raw); err != nil {
		f.t.Fatal(err)
	}
}

// gate is the row's gateAt, nil when none is given.
func (f *verdictFixture) gate() *GateAt {
	f.t.Helper()
	var raw []byte
	if err := f.owner.QueryRow(f.ctx, `SELECT state->'gateAt' FROM workflow_runs WHERE id = 'wf_c'`).Scan(&raw); err != nil {
		f.t.Fatal(err)
	}
	var at *GateAt
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &at); err != nil {
			f.t.Fatal(err)
		}
	}
	return at
}

func (f *verdictFixture) exec(sql string) {
	f.t.Helper()
	if _, err := f.owner.Exec(f.ctx, sql, f.org); err != nil {
		f.t.Fatal(err)
	}
}

// GitHub failing to compare the merge: a transient failure waits and asks
// again, starting nothing; a refusal retests. The comparison GitHub
// answered is cached: the step entered again asks nothing new.
func TestACleanMergeGitHubCannotCompareIsWaitedOnOrTested(t *testing.T) {
	for _, c := range []struct {
		name   string
		status int
		wait   bool
	}{{"transient", http.StatusBadGateway, true}, {"refused", http.StatusUnprocessableEntity, false}} {
		t.Run(c.name, func(t *testing.T) {
			f, st, sc := mergeFixture(t, map[string]string{"lib/other.go": "package lib"})
			f.merged(st, sc)
			// The task's side (target branch...task's head): the merge's side
			// alone would leave nothing to skip for.
			taskSide := "/compare/" + st.Integration.BaseHead + "..."
			f.gh.Set(func(s *fakegithub.Server) {
				s.Intercept = func(r *http.Request) int {
					if strings.Contains(r.URL.Path, taskSide) {
						return c.status
					}
					return 0
				}
			})
			skip, err := f.steps.carryForward(f.ctx, sc, st, nil, false)
			var wait workflow.Wait
			if skip || errors.As(err, &wait) != c.wait || !c.wait && err != nil || f.skips() != 0 {
				t.Fatalf("skip %v, %v; want a wait %v and no skip", skip, err, c.wait)
			}
			if !c.wait {
				return
			}
			// GitHub answers again: the skip happens.
			f.gh.Set(func(s *fakegithub.Server) { s.Intercept = nil })
			if skip, err := f.steps.carryForward(f.ctx, sc, st, nil, false); err != nil || !skip {
				t.Fatalf("after GitHub answers: skip %v, %v", skip, err)
			}
		})
	}
}

// A branch found moved before the pull request (GitHub merged, its answer
// lost, or a person pushed) is adopted and tested again, whatever it holds.
func TestAnAdoptedMovedHeadBeforeThePullRequestIsTested(t *testing.T) {
	f, st, sc := mergeFixture(t, map[string]string{"lib/other.go": "package lib"})
	if f.gh.MergeInto("feature", "main", nil) == "" {
		t.Fatal("merging main on GitHub")
	}
	res, diverted, err := f.steps.syncBeforePR(f.ctx, sc, st)
	if err != nil || !diverted || res.Next != "test" || st.Integration == nil || st.Integration.ServerMerge {
		t.Fatalf("syncBeforePR: %+v %v %v %+v; want the moved head adopted", res, diverted, err, st.Integration)
	}
	if skip, err := f.steps.carryForward(f.ctx, sc, st, nil, false); err != nil || skip || f.skips() != 0 {
		t.Fatalf("skip %v (%v): an adopted head must be tested", skip, err)
	}
}

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
