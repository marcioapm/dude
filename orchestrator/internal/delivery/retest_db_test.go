package delivery

import (
	"context"
	"net/http/httptest"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

// The test step starts no tester after a passing round when the change
// since is all non-product paths and the tester has no finding to judge;
// a previous round that did not pass, a finding to judge, a product path
// or a head the previous checkout is not an ancestor of each starts one.
// This round may carry from the previous one only when its heads descend
// from that round's checkouts.
func TestATesterIsSkippedOnlyForANonProductChangeAfterAPass(t *testing.T) {
	ci := map[string]string{".github/workflows/ci.yml": "on: [push, pull_request]"}
	for _, c := range []struct {
		name     string
		files    map[string]string
		previous string
		judge    []string
		diverged bool
		skip     bool
		carry    bool
	}{
		{"CI only after a pass", ci, VerdictPass, nil, false, true, true},
		{"docs and Markdown after a pass", map[string]string{"docs/guide.txt": "x", "api/NOTES.md": "x"}, VerdictPass, nil, false, true, true},
		{"CI only after a failure", ci, VerdictFail, nil, false, false, true},
		{"CI only after an unverified pass", ci, VerdictUnverified, nil, false, false, true},
		{"CI only with a finding to judge", ci, VerdictPass, []string{"fnd_1"}, false, false, true},
		{"a product path beside CI", map[string]string{".github/workflows/ci.yml": "x", "app/main.go": "package main"}, VerdictPass, nil, false, false, true},
		{"history that does not descend", ci, VerdictPass, nil, true, false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newVerdictFixture(t)
			f.withRepo()
			from := f.gh.SHA("feature")
			if c.diverged {
				// The previous round tested a commit the branch does not hold.
				from = f.gh.Branch("rewritten", "feature", "A commit the branch lost")
			}
			head := f.gh.CommitFiles("feature", "Change", c.files)
			for _, q := range []struct {
				sql  string
				args []any
			}{
				{`INSERT INTO task_repositories (task_id, repository_id, organization_id) VALUES ('wi_c', 'repo_c', $1)`, nil},
				{`UPDATE runs SET base_refs = jsonb_build_object('repo', $1::text) WHERE id = 'run_c'`, []any{from}},
				{`INSERT INTO acceptance_verdicts (organization_id, task_id, run_id, criterion, text, status, evidence)
					VALUES ($1, 'wi_c', 'run_c', 1, 'it greets', $2, '{greet.webm}')`, []any{f.org, c.previous}},
			} {
				args := q.args
				if args == nil {
					args = []any{f.org}
				}
				if _, err := f.owner.Exec(f.ctx, q.sql, args...); err != nil {
					t.Fatal(err)
				}
			}
			st := &State{TaskID: "wi_c", ProjectID: "prj_c", Attempt: 1, Policy: DefaultPolicy(), Heads: map[string]string{"repo": head}}
			sc := workflow.StepContext{WorkflowRunID: "wf_c", OrganizationID: f.org}
			skip, err := f.steps.carryForward(f.ctx, sc, st, c.judge, false)
			if err != nil {
				t.Fatal(err)
			}
			if skip != c.skip || (st.CarryFrom == "run_c") != c.carry {
				t.Fatalf("skip %v, carry from %q; want skip %v, carry %v", skip, st.CarryFrom, c.skip, c.carry)
			}
			var events int
			_ = f.owner.QueryRow(f.ctx, `SELECT count(*) FROM events WHERE task_id = 'wi_c' AND event_type = $1
				AND payload->>'fromRunId' = 'run_c' AND payload->'criteria'->0->>'status' = 'pass'`, EvTestSkipped).Scan(&events)
			if want := map[bool]int{true: 1}[c.skip]; events != want || c.skip && st.TestRunID != "run_c" {
				t.Errorf("%d skip events, gate on %q; want %d and the previous round", events, st.TestRunID, want)
			}
		})
	}
}

// withRepo gives the fixture's GitHub a git repository of its own, a
// feature branch from main on it, so its compare answers from real commits.
func (f *verdictFixture) withRepo() {
	f.t.Helper()
	bare := f.t.TempDir()
	git := func(args ...string) string {
		out, err := exec.Command("git", append([]string{"-C", bare}, args...)...).Output()
		if err != nil {
			f.t.Fatalf("git %v: %v", args, err)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q", "--bare", "--initial-branch=main")
	initial := git("-c", "user.name=t", "-c", "user.email=t@t", "commit-tree", git("mktree"), "-m", "Initial")
	git("update-ref", "refs/heads/main", initial)
	f.gh = fakegithub.New(bare, "acme/repo")
	f.gh.Branch("feature", "main", "Start")
	srv := httptest.NewServer(f.gh.Handler())
	f.t.Cleanup(srv.Close)
	f.steps.forges = forgeOf{forge.NewGitHub(forge.Credential{Auth: "pat", Secret: "ghp", APIBaseURL: srv.URL})}
}

// A resumed tester's carried criterion is recorded as the previous round's
// pass, with that round's evidence and carried_from naming it; the same
// carry of a criterion that failed there, or naming another run, is
// recorded blocked. A Run that resumed no round (fresh after a large
// change) carries nothing.
func TestACarriedVerdictIsRecordedFromThePreviousRound(t *testing.T) {
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := owner.Exec(ctx, sql, append([]any{org}, args...)...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_k', $1, 'P', 'p', 'P')`)
	exec(`INSERT INTO tasks (id, organization_id, project_id, number, title, goal, acceptance_criteria, status)
		VALUES ('wi_k', $1, 'prj_k', 1, 'Greet', 'greet', '["it greets", "it says goodbye", "it waves"]', 'running')`)
	exec(`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, kind, ended_at)
		VALUES ('run_k1', $1, 'prj_k', 'wi_k', 1, 'completed', 'test', 'qa_browser', 'agent', now() - interval '1 hour')`)
	exec(`INSERT INTO acceptance_verdicts (organization_id, task_id, run_id, criterion, text, status, evidence, observed)
		VALUES ($1, 'wi_k', 'run_k1', 1, 'it greets', 'pass', '{greet.webm}', 'It greets.'),
		       ($1, 'wi_k', 'run_k1', 2, 'it says goodbye', 'fail', '{bye.png}', 'No goodbye.'),
		       ($1, 'wi_k', 'run_k1', 3, 'it waves', 'pass', '{wave.png}', 'It waves.')`)
	reply := "```yaml\ncriterion: 1\nstatus: carried\nfrom_round: run_k1\nobserved: Only CI changed.\n" +
		"---\ncriterion: 2\nstatus: carried\nfrom_round: run_k1\nobserved: Only CI changed.\n" +
		"---\ncriterion: 3\nstatus: carried\nfrom_round: run_other\nobserved: Only CI changed.\n```\n"
	s := &Store{DB: app}
	for _, c := range []struct {
		run, how string
		want     []string
	}{
		{"run_k2", "resume", []string{"1 pass run_k1 greet.webm", "2 blocked  ", "3 blocked  "}},
		{"run_k3", "", []string{"1 blocked  ", "2 blocked  ", "3 blocked  "}},
	} {
		exec(`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, kind, ended_at,
				continues_run_id, continues_how)
			VALUES ($2, $1, 'prj_k', 'wi_k', 1, 'completed', 'test', 'qa_browser', 'agent', now(),
				CASE WHEN $3 <> '' THEN 'run_k1' END, NULLIF($3, ''))`, c.run, c.how)
		exec(`INSERT INTO events (id, organization_id, event_type, run_id, task_id, actor_type, actor_id, source, payload)
			VALUES ('ev_'||$2, $1, 'agent.message', $2, 'wi_k', 'agent', $2, 'test', jsonb_build_object('text', $3::text))`, c.run, reply)
		st := &State{TaskID: "wi_k", ProjectID: "prj_k", Attempt: 1, TestRunID: c.run, Policy: DefaultPolicy(), CarryFrom: "run_k1"}
		if _, err := s.RecordVerdict(ctx, org, st, c.run); err != nil {
			t.Fatal(err)
		}
		rows, err := owner.Query(ctx, `SELECT criterion || ' ' || status || ' ' || COALESCE(carried_from, '') || ' ' ||
				array_to_string(evidence, ','), problem
			FROM acceptance_verdicts WHERE run_id = $1 ORDER BY criterion`, c.run)
		if err != nil {
			t.Fatal(err)
		}
		var got, problems []string
		for rows.Next() {
			var line, problem string
			if err := rows.Scan(&line, &problem); err != nil {
				t.Fatal(err)
			}
			got, problems = append(got, line), append(problems, problem)
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("%s's verdicts = %q, want %q", c.run, got, c.want)
		}
		for i, line := range got {
			if line[2:9] == "blocked" && problems[i] == "" {
				t.Errorf("%s criterion %d is blocked without saying why", c.run, i+1)
			}
		}
	}
}
