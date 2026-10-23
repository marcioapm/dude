package delivery

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"

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
		// The task's criteria now, when not the ["it greets"] round 1 judged.
		criteria string
	}{
		{"CI only after a pass", ci, VerdictPass, nil, false, true, true, ""},
		{"docs and Markdown after a pass", map[string]string{"docs/guide.txt": "x", "api/NOTES.md": "x"}, VerdictPass, nil, false, true, true, ""},
		{"CI only after a failure", ci, VerdictFail, nil, false, false, true, ""},
		{"CI only after an unverified pass", ci, VerdictUnverified, nil, false, false, true, ""},
		{"CI only with a finding to judge", ci, VerdictPass, []string{"fnd_1"}, false, false, true, ""},
		{"a product path beside CI", map[string]string{".github/workflows/ci.yml": "x", "app/main.go": "package main"}, VerdictPass, nil, false, false, true, ""},
		{"history that does not descend", ci, VerdictPass, nil, true, false, false, ""},
		{"a criterion added after a pass", ci, VerdictPass, nil, false, false, true, `["it greets", "it waves"]`},
		{"a criterion edited after a pass", ci, VerdictPass, nil, false, false, true, `["it greets by name"]`},
		{"a task without criteria", ci, VerdictPass, nil, false, false, true, `[]`},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, st, sc, _ := newSkipFixture(t, c.files, c.previous, c.diverged, c.criteria)
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

// A change whose paths look non-product still starts a tester when it is
// not all non-product or not measured against the previous round: a
// product file renamed under docs/ (its old name is a product path), and a
// repository with a head now but no checkout recorded by the previous
// round, which is compared from its default branch and does not descend.
func TestATesterIsNotSkippedForAChangeItCannotTellIsNonProduct(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(f *verdictFixture)
		carry bool
	}{
		{"a product file renamed under docs", func(f *verdictFixture) {
			f.gh.Set(func(s *fakegithub.Server) {
				s.CompareAnswer = func(_, _ string, answer map[string]any) {
					answer["files"] = []any{map[string]any{"filename": "docs/main.go", "previous_filename": "app/main.go",
						"status": "renamed", "additions": 0, "deletions": 0}}
				}
			})
		}, true},
		{"a repository with no recorded checkout", func(f *verdictFixture) {
			if _, err := f.owner.Exec(f.ctx, `UPDATE runs SET base_refs = '{}' WHERE id = 'run_c'`); err != nil {
				f.t.Fatal(err)
			}
		}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, st, sc, _ := newSkipFixture(t, map[string]string{"docs/main.go": "package main"}, VerdictPass, false, "")
			c.setup(f)
			skip, err := f.steps.carryForward(f.ctx, sc, st, nil, false)
			if err != nil {
				t.Fatal(err)
			}
			if skip || (st.CarryFrom == "run_c") != c.carry {
				t.Fatalf("skip %v, carry from %q; want no skip, carry %v", skip, st.CarryFrom, c.carry)
			}
		})
	}
}

// A CI-only change after a pass still starts a tester when the conductor
// directed this test, when the round is an integration's, or when a fix of
// a failed test is due: each is a test someone asked for, not a round to
// carry. Driven through the test step, so the step's own guard is tested.
func TestATesterIsNotSkippedWhenTheTestWasAskedFor(t *testing.T) {
	for _, c := range []struct {
		name string
		set  func(*State)
	}{
		{"none (the skip happens)", func(*State) {}},
		{"a conductor-directed test", func(st *State) { st.Directed = &Directed{Phase: PhaseTest, By: "run_conductor"} }},
		{"an integration round", func(st *State) { st.Integration = &Integration{Repo: "repo", Number: 1} }},
		{"a fix of a failed test due", func(st *State) { st.RetestDue = true }},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, st, sc, _ := newSkipFixture(t, map[string]string{".github/workflows/ci.yml": "on: [push]"}, VerdictPass, false, "")
			st.Policy.Test = true
			c.set(st)
			sc.Step, sc.State = "test", mustJSON(t, st)
			res, err := f.steps.test(f.ctx, sc)
			var skips int
			if err := f.owner.QueryRow(f.ctx, `SELECT count(*) FROM events WHERE task_id = 'wi_c' AND event_type = $1`, EvTestSkipped).Scan(&skips); err != nil {
				t.Fatal(err)
			}
			skipped := err == nil && res.Next == "testVerdict"
			if want := strings.HasPrefix(c.name, "none"); skipped != want || (skips == 1) != want {
				t.Fatalf("next %q (%v), %d skips; want skipped %v", res.Next, err, skips, want)
			}
		})
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// newSkipFixture is a task whose test round run_c judged its one criterion
// "it greets" previous at the feature branch's head (or, diverged, at a
// commit the branch lost), followed by a commit of files; criteria, when
// set, is the task's criteria now. st is the delivery at that commit;
// the counter, GitHub's compares.
func newSkipFixture(t *testing.T, files map[string]string, previous string, diverged bool, criteria string) (*verdictFixture, *State, workflow.StepContext, *atomic.Int64) {
	t.Helper()
	f := newVerdictFixture(t)
	compares := f.withRepo()
	from := f.gh.SHA("feature")
	if diverged {
		from = f.gh.Branch("rewritten", "feature", "A commit the branch lost")
	}
	head := f.gh.CommitFiles("feature", "Change", files)
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO task_repositories (task_id, repository_id, organization_id) VALUES ('wi_c', 'repo_c', $1)`, nil},
		{`INSERT INTO workflow_runs (id, organization_id, workflow_type, idempotency_key, status, step, state, task_id)
			VALUES ('wf_c', $1, 'task.delivery', 'c', 'running', 'test', '{}', 'wi_c')`, nil},
		{`UPDATE runs SET base_refs = jsonb_build_object('repo', $1::text) WHERE id = 'run_c'`, []any{from}},
		{`INSERT INTO acceptance_verdicts (organization_id, task_id, run_id, criterion, text, status, evidence)
			VALUES ($1, 'wi_c', 'run_c', 1, 'it greets', $2, '{greet.webm}')`, []any{f.org, previous}},
		{`UPDATE tasks SET acceptance_criteria = COALESCE(NULLIF($1, '')::jsonb, acceptance_criteria) WHERE id = 'wi_c'`,
			[]any{criteria}},
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
	return f, st, workflow.StepContext{WorkflowRunID: "wf_c", OrganizationID: f.org}, compares
}

// A failed criterion a person accepted, carried by the next round after a
// product fix, keeps the acceptance: the round raises no new finding for
// it, and the gate passes.
func TestACarriedAcceptanceOfAFailureKeepsTheGatePassing(t *testing.T) {
	d := newTestRounds(t, "a", `["it greets", "it says goodbye"]`)
	ctx, org, s, owner := d.ctx, d.org, d.s, d.owner
	d.testRun("run_a1", "", "```yaml\ncriterion: 1\nstatus: pass\nevidence: greet.webm\nobserved: It greets.\n"+
		"---\ncriterion: 2\nstatus: fail\nobserved: It says bye, not goodbye.\n```\n")
	d.exec(`INSERT INTO artifacts (id, organization_id, run_id, kind, name, storage_key, sha256)
		VALUES ('art_a1', $1, 'run_a1', 'published', 'greet.webm', 'k', 'x')`)
	st := &State{TaskID: "wi_a", ProjectID: "prj_a", Attempt: 1, TestRunID: "run_a1", Policy: DefaultPolicy()}
	if _, err := s.RecordVerdict(ctx, org, st, "run_a1"); err != nil {
		t.Fatal(err)
	}
	// Round 1 fails criterion 2, dude raises its finding, and the person accepts it.
	in, err := s.gateInput(ctx, org, st, "run_a1")
	if err != nil {
		t.Fatal(err)
	}
	g := judgeGate(st.Policy, in)
	if g.Outcome != "fail" || !slices.Equal(g.Criteria, []int{2}) {
		t.Fatalf("round 1 gate = %+v, want criterion 2 failing", g)
	}
	e := &Escalation{Reason: ReasonTestFailedCriteria, Detail: map[string]any{"runId": "run_a1", "findingIds": g.Findings}}
	if err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error { return SettleVerdictTx(ctx, tx, e, "accept", "", "Bye is fine.") }); err != nil {
		t.Fatal(err)
	}
	// Round 2 resumes the tester after a product fix: it retests 1 and carries 2.
	d.testRun("run_a2", "resume", "```yaml\ncriterion: 1\nstatus: pass\nevidence: greet2.webm\nobserved: It greets.\n"+
		"---\ncriterion: 2\nstatus: carried\nfrom_round: run_a1\nobserved: The fix changed the greeting only.\n```\n")
	d.exec(`INSERT INTO artifacts (id, organization_id, run_id, kind, name, storage_key, sha256)
		VALUES ('art_a2', $1, 'run_a2', 'published', 'greet2.webm', 'k2', 'x')`)
	st = &State{TaskID: "wi_a", ProjectID: "prj_a", Attempt: 1, TestRunID: "run_a2", Policy: DefaultPolicy(), CarryFrom: "run_a1"}
	if _, err := s.RecordVerdict(ctx, org, st, "run_a2"); err != nil {
		t.Fatal(err)
	}
	var line string
	if err := owner.QueryRow(ctx, `SELECT status || ' ' || COALESCE(decision, '') || ' ' || COALESCE(carried_from, '')
		FROM acceptance_verdicts WHERE run_id = 'run_a2' AND criterion = 2`).Scan(&line); err != nil || line != "fail accepted run_a1" {
		t.Fatalf("carried criterion 2 = %q (%v), want fail accepted run_a1", line, err)
	}
	var raised int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM review_findings WHERE run_id = 'run_a2'`).Scan(&raised); err != nil || raised != 0 {
		t.Errorf("round 2 raised %d findings (%v), want none", raised, err)
	}
	if in, err = s.gateInput(ctx, org, st, "run_a2"); err != nil {
		t.Fatal(err)
	}
	if g := judgeGate(st.Policy, in); g.Outcome != "pass" {
		t.Errorf("round 2 gate = %+v, want pass", g)
	}
}

// testRounds is task wi_<key> of project prj_<key>, with criteria, in a
// database of its own; exec runs as the owner with the organization as $1.
type testRounds struct {
	t     *testing.T
	ctx   context.Context
	org   string
	key   string
	s     *Store
	owner *pgx.Conn
}

func newTestRounds(t *testing.T, key, criteria string) *testRounds {
	app, owner := dbtest.Open(t)
	d := &testRounds{t: t, ctx: context.Background(), org: dbtest.Org(t, owner), key: key, s: &Store{DB: app}, owner: owner}
	d.exec(`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_'||$2, $1, 'P', 'p', 'P')`, key)
	d.exec(`INSERT INTO tasks (id, organization_id, project_id, number, title, goal, acceptance_criteria, status)
		VALUES ('wi_'||$2, $1, 'prj_'||$2, 1, 'Greet', 'greet', $3::jsonb, 'running')`, key, criteria)
	return d
}

func (d *testRounds) exec(sql string, args ...any) {
	d.t.Helper()
	if _, err := d.owner.Exec(d.ctx, sql, append([]any{d.org}, args...)...); err != nil {
		d.t.Fatal(err)
	}
}

// testRun is a finished test Run of attempt 1 that replied reply ("" for
// no reply); how, when set, is how it continues the task's first test Run,
// <key>1.
func (d *testRounds) testRun(id, how, reply string) {
	d.t.Helper()
	d.exec(`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, kind, ended_at,
			continues_run_id, continues_how)
		VALUES ($2, $1, 'prj_'||$3, 'wi_'||$3, 1, 'completed', 'test', 'qa_browser', 'agent', now(),
			CASE WHEN $4 <> '' THEN 'run_'||$3||'1' END, NULLIF($4, ''))`, id, d.key, how)
	if reply != "" {
		d.exec(`INSERT INTO events (id, organization_id, event_type, run_id, task_id, actor_type, actor_id, source, payload)
			VALUES ('ev_'||$2, $1, 'agent.message', $2, 'wi_'||$3, 'agent', $2, 'test', jsonb_build_object('text', $4::text))`,
			id, d.key, reply)
	}
}

// withRepo gives the fixture's GitHub a git repository of its own, a
// feature branch from main on it, so its compare answers from real commits.
// It returns how many compares GitHub has answered.
func (f *verdictFixture) withRepo() *atomic.Int64 {
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
	compares := &atomic.Int64{}
	gh := f.gh.Handler()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/compare/") {
			compares.Add(1)
		}
		gh.ServeHTTP(w, r)
	}))
	f.t.Cleanup(srv.Close)
	f.steps.forges = forgeOf{forge.NewGitHub(forge.Credential{Auth: "pat", Secret: "ghp", APIBaseURL: srv.URL})}
	return compares
}

// A test round that is not skipped compares each changed repository once:
// the skip check and the resumed tester's Continue share the measurement,
// and the step entered again (a wait, a retry) asks GitHub nothing new.
func TestATestRoundComparesEachChangedRepositoryOnce(t *testing.T) {
	f, st, sc, compares := newSkipFixture(t, map[string]string{"app/main.go": "package main"}, VerdictPass, false, "")
	// A second repository of the task, unchanged since the previous round.
	for _, q := range []string{
		`INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch)
			VALUES ('repo_d', $1, 'prj_c', 'other', 'https://github.com/acme/other.git', 'main')`,
		`INSERT INTO task_repositories (task_id, repository_id, organization_id) VALUES ('wi_c', 'repo_d', $1)`,
		`UPDATE runs SET base_refs = base_refs || '{"other": "ddd444"}' WHERE id = 'run_c' AND organization_id = $1`,
	} {
		if _, err := f.owner.Exec(f.ctx, q, f.org); err != nil {
			t.Fatal(err)
		}
	}
	st.Heads["other"] = "ddd444"
	for entry := range 2 {
		at := *st
		skip, err := f.steps.carryForward(f.ctx, sc, &at, nil, false)
		if err != nil || skip || at.CarryFrom != "run_c" {
			t.Fatalf("entry %d: skip %v, carry from %q, %v; want a resumed tester carrying from run_c", entry, skip, at.CarryFrom, err)
		}
		c, err := f.steps.goesOn(f.ctx, sc, &at, PhaseTest)("")
		if err != nil || c == nil || c.Predecessor != "run_c" || c.DeltaLines != 1 {
			t.Fatalf("entry %d: continue %+v, %v; want run_c over 1 line", entry, c, err)
		}
	}
	if n := compares.Load(); n != 1 {
		t.Errorf("%d compares for one changed repository over two entries of the step, want 1", n)
	}
}

// A resumed tester's carried criterion is recorded as the previous round's
// pass, with that round's evidence and carried_from naming it; the same
// carry of a criterion that failed there, or naming another run, is
// recorded blocked. A Run that resumed no round (fresh after a large
// change, or a replay), or resumed one its heads do not descend from,
// carries nothing.
func TestACarriedVerdictIsRecordedFromThePreviousRound(t *testing.T) {
	d := newTestRounds(t, "k", `["it greets", "it says goodbye", "it waves"]`)
	ctx, org, s, owner := d.ctx, d.org, d.s, d.owner
	d.testRun("run_k1", "", "")
	d.exec(`INSERT INTO acceptance_verdicts (organization_id, task_id, run_id, criterion, text, status, evidence, observed)
		VALUES ($1, 'wi_k', 'run_k1', 1, 'it greets', 'pass', '{greet.webm}', 'It greets.'),
		       ($1, 'wi_k', 'run_k1', 2, 'it says goodbye', 'fail', '{bye.png}', 'No goodbye.'),
		       ($1, 'wi_k', 'run_k1', 3, 'it waves', 'pass', '{wave.png}', 'It waves.')`)
	reply := "```yaml\ncriterion: 1\nstatus: carried\nfrom_round: run_k1\nobserved: Only CI changed.\n" +
		"---\ncriterion: 2\nstatus: carried\nfrom_round: run_k1\nobserved: Only CI changed.\n" +
		"---\ncriterion: 3\nstatus: carried\nfrom_round: run_other\nobserved: Only CI changed.\n```\n"
	nothing := []string{"1 blocked  ", "2 blocked  ", "3 blocked  "}
	afresh := "Carried, but this round tests every criterion afresh: nothing carries into it."
	for _, c := range []struct {
		run, how string
		// st.CarryFrom: the previous round when this round's heads descend
		// from its checkouts, "" when they do not.
		from string
		want []string
		// Why criterion 1, which run_k1 passed, is refused, when it is.
		refused string
	}{
		{"run_k2", "resume", "run_k1", []string{"1 pass run_k1 greet.webm", "2 blocked  ", "3 blocked  "}, ""},
		{"run_k3", "", "run_k1", nothing, afresh},
		// A replay after the round's time budget starts the tester afresh.
		{"run_k4", "replay", "run_k1", nothing, afresh},
		// Resumed, but the branch was rewritten since run_k1.
		{"run_k5", "resume", "", nothing,
			"Carried, but this round's commits do not descend from those run_k1 tested: nothing it judged carries."},
	} {
		d.testRun(c.run, c.how, reply)
		st := &State{TaskID: "wi_k", ProjectID: "prj_k", Attempt: 1, TestRunID: c.run, Policy: DefaultPolicy(), CarryFrom: c.from}
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
		if c.refused != "" && (len(problems) == 0 || problems[0] != c.refused) {
			t.Errorf("%s's criterion 1 problem = %q, want %q", c.run, problems, c.refused)
		}
		for i, line := range got {
			if line[2:9] == "blocked" && problems[i] == "" {
				t.Errorf("%s criterion %d is blocked without saying why", c.run, i+1)
			}
		}
	}
}
