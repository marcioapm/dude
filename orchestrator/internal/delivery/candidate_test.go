package delivery

import (
	"context"
	"encoding/json"
	"maps"
	"reflect"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

// A replayed BeforePR integration fix keeps the candidate decision its Run
// was created with, whatever the other repository's pull request did since:
// the Run publishes to the branch it was created for. A replayed pull
// request fix, with no integration, keeps its candidate too.
func TestAReplayedBeforePRIntegrationKeepsItsRunsCandidate(t *testing.T) {
	beforePR := &Integration{Repo: "web", Base: "main", Head: "bbb", BaseHead: "ccc", BeforePR: true}
	for _, tc := range []struct {
		name, prState string
		integration   *Integration
		// onCandidate creates the Run on the delivery's candidate branch,
		// else on the task branch (runs.branch NULL).
		onCandidate, candidate bool
	}{
		{"created on a candidate, the pull request merged since", "merged", beforePR, true, true},
		{"created on the task branch, a pull request open now", "open", beforePR, false, false},
		{"a pull request fix, not an integration, created on a candidate", "open", nil, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := &State{TaskID: "wi_e", ProjectID: "prj_e", Attempt: 1, Branch: "dude/wi_e/attempt-1", PullRequestIDs: []string{"pr_t"},
				Heads: map[string]string{"target": "aaa", "web": "bbb"}, Integration: tc.integration,
				// A pull request fix is charged its round: one is left.
				Policy: Policy{MaxPRFixIterations: 1}}
			var runBranch string
			if tc.onCandidate {
				runBranch = st.candidateBranch()
			}
			app, owner := dbtest.Open(t)
			org := dbtest.Org(t, owner)
			ctx := context.Background()
			for _, s := range []struct {
				sql  string
				args []any
			}{
				{`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_e', $1, 'P', 'p', 'P')`, nil},
				{`INSERT INTO tasks (id, organization_id, project_id, number, title, goal, status) VALUES ('wi_e', $1, 'prj_e', 1, 'Greet', 'greet', 'running')`, nil},
				{`INSERT INTO repositories (id, organization_id, project_id, name, url) VALUES ('repo_t', $1, 'prj_e', 'target', 'https://github.com/acme/target')`, nil},
				{`INSERT INTO pull_requests (id, organization_id, project_id, task_id, repository_id, number, url, head_branch, base_branch, head_sha, title, state)
					VALUES ('pr_t', $1, 'prj_e', 'wi_e', 'repo_t', 1, 'https://github.com/acme/target/pull/1', 'dude/wi_e/attempt-1', 'main', 'aaa', 'Greet', $2::pull_request_state)`, []any{tc.prState}},
				{`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, phase, role, kind, status, base_refs, creation_key, branch)
					VALUES ('run_fix', $1, 'prj_e', 'wi_e', 1, 'fix', 'implementer', 'agent', 'pending', '{"target":"aaa","web":"bbb"}', 'wf_e:prfix:1', NULLIF($2, ''))`, []any{runBranch}},
			} {
				if _, err := owner.Exec(ctx, s.sql, append([]any{org}, s.args...)...); err != nil {
					t.Fatal(err)
				}
			}
			raw, err := json.Marshal(st)
			if err != nil {
				t.Fatal(err)
			}
			w := &steps{s: &Store{DB: app}, forges: forgeOf{}}
			res, err := w.prFix(ctx, workflow.StepContext{WorkflowRunID: "wf_e", OrganizationID: org, State: raw})
			if err != nil {
				t.Fatal(err)
			}
			got := res.State.(*State)
			if len(got.PendingRunIDs) != 1 || got.PendingRunIDs[0] != "run_fix" {
				t.Fatalf("waits on %v, want the existing run_fix", got.PendingRunIDs)
			}
			if (got.Candidate != nil) != tc.candidate {
				t.Fatalf("candidate %+v, want one: %v", got.Candidate, tc.candidate)
			}
			if got.Candidate != nil && got.Candidate.Branch != runBranch {
				t.Fatalf("candidate on %q, want the Run's %q", got.Candidate.Branch, runBranch)
			}
		})
	}
}

// A refused loop candidate's round (round 2 here) reopens the findings its
// Runs resolved, except one naming a repository the candidate did move to;
// a Run that replaced one of the round's (restart_run, replaceRound) has no
// creation key and is the round's too. Another round's resolutions, and a
// keyless Run's outside the round, stay. The round opened with a fix.
func TestDiscardingALoopCandidateReopensWhatItsRoundResolved(t *testing.T) {
	const roundReview = "wf_d:review:1:correctness:candidate:2"
	const otherRound = "wf_d:review:1:correctness:candidate:12"
	for _, tc := range []struct {
		name string
		// repo is the finding's; "" is NULL.
		repo  string
		moved []string
		// resolver is the creation key of the Run that resolved the finding,
		// "" for none; replaced, when set, is the key of the Run it replaced.
		resolver, replaced string
		want               string
	}{
		{"a repository's, nothing moved (nil)", "target", nil, roundReview, "", "open"},
		{"a repository's, nothing moved (empty)", "target", []string{}, roundReview, "", "open"},
		{"a repository's, nothing moved (sorted empty map)", "target", slices.Sorted(maps.Keys(map[string]string{})), roundReview, "", "open"},
		{"naming no repository, one moved", "", []string{"target"}, roundReview, "", "open"},
		{"a repository's, another moved", "target", []string{"web"}, roundReview, "", "open"},
		{"a repository's, that one moved", "target", []string{"target"}, roundReview, "", "resolved"},
		{"resolved by another round's Run", "target", nil, otherRound, "", "resolved"},
		{"resolved by the replacement of a round Run", "target", nil, "", roundReview, "open"},
		{"resolved by the replacement of another round's Run", "target", nil, "", otherRound, "resolved"},
		{"resolved by a keyless Run outside the round", "target", nil, "", "", "resolved"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app, owner := dbtest.Open(t)
			org := dbtest.Org(t, owner)
			ctx := context.Background()
			for _, q := range []struct {
				sql  string
				args []any
			}{
				{`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_d', $1, 'P', 'p', 'P')`, nil},
				{`INSERT INTO tasks (id, organization_id, project_id, number, title, goal, status) VALUES ('wi_d', $1, 'prj_d', 1, 'Greet', 'greet', 'running')`, nil},
				{`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, phase, role, kind, status, creation_key, created_at, ended_at)
				 VALUES ('run_fix', $1, 'prj_d', 'wi_d', 1, 'fix', 'implementer', 'agent', 'completed', 'wf_d:fix:0:candidate:2', now() - interval '3 minutes', now()),
				        ('run_review', $1, 'prj_d', 'wi_d', 1, 'review', 'reviewer', 'agent', 'completed', NULLIF($2, ''), now() - interval '1 minute', now())`, []any{tc.resolver}},
				{`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, phase, role, kind, status, creation_key, replaced_by, created_at, ended_at)
				 SELECT 'run_replaced', $1, 'prj_d', 'wi_d', 1, 'review', 'reviewer', 'agent', 'failed', $2, 'run_review', now() - interval '2 minutes', now()
				 WHERE $2 <> ''`, []any{tc.replaced}},
				{`INSERT INTO review_findings (id, organization_id, task_id, run_id, category, severity, status, repo, title, resolved_by_run_id)
				 VALUES ('fnd_d', $1, 'wi_d', 'run_review', 'correctness', 'blocking', 'resolved', NULLIF($2, ''), 'Greets nobody', 'run_review')`, []any{tc.repo}},
			} {
				if _, err := owner.Exec(ctx, q.sql, append([]any{org}, q.args...)...); err != nil {
					t.Fatal(err)
				}
			}
			var phase string
			if err := app.InOrg(ctx, org, func(tx pgx.Tx) error {
				var err error
				phase, err = discardLoopCandidateTx(ctx, tx, "wi_d", "wf_d", 1, 2, tc.moved)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if phase != PhaseFix {
				t.Errorf("the round's first writer = %q, want fix", phase)
			}
			var status string
			if err := owner.QueryRow(ctx, `SELECT status::text FROM review_findings WHERE id = 'fnd_d'`).Scan(&status); err != nil {
				t.Fatal(err)
			}
			if status != tc.want {
				t.Errorf("finding %s, want %s", status, tc.want)
			}
		})
	}
}

// A loop writer's step recovers the Run it saved under either key, the
// task branch's or the candidate's, whichever way the pull request went
// since. A conductor-directed writer's is recovered too, and the step still
// spends the direction; with no saved Run, the Run it creates carries the
// conductor's mark.
func TestLoopWritersReplayTheirSavedBranchAcrossPRStateChanges(t *testing.T) {
	type variant struct {
		candidate, directed, saved bool
	}
	var variants []variant
	for _, directed := range []bool{false, true} {
		for _, candidate := range []bool{false, true} {
			variants = append(variants, variant{candidate, directed, true})
		}
	}
	variants = append(variants, variant{directed: true})
	for _, phase := range []string{PhaseImplement, PhaseFix, PhaseSimplify} {
		for _, v := range variants {
			candidate := v.candidate
			name := phase + map[bool]string{false: "/closed-to-open", true: "/open-to-closed"}[candidate]
			if !v.saved {
				name = phase + "/no-saved-run"
			}
			if v.directed {
				name += "/directed"
			}
			t.Run(name, func(t *testing.T) {
				app, owner := dbtest.Open(t)
				org := dbtest.Org(t, owner)
				ctx := context.Background()
				st := &State{TaskID: "wi_e", ProjectID: "prj_e", Attempt: 1, Branch: "dude/wi_e/attempt-1",
					PullRequestIDs: []string{"pr_t"}, Heads: map[string]string{"target": "stale"}, Iteration: 2,
					PRFixKey: 3, PRIteration: 1, PRFixes: map[string]int{"target": 1}}
				if v.directed {
					st.Directed, st.Decisions = &Directed{Phase: phase, By: "run_conductor", Note: "n"}, 2
				}
				saved := *st
				branch, prState := "", "open"
				if candidate {
					saved.Candidate = &Candidate{Round: 4}
					branch, prState = st.candidateBranch(), "closed"
				}
				if !v.saved {
					// Closed: the step creates its Run on the task branch.
					prState = "closed"
				}
				sc := workflow.StepContext{WorkflowRunID: "wf_e", OrganizationID: org}
				seed := []struct {
					sql  string
					args []any
				}{
					{`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_e', $1, 'P', 'p', 'P')`, nil},
					{`INSERT INTO tasks (id, organization_id, project_id, number, title, goal, status) VALUES ('wi_e', $1, 'prj_e', 1, 'Greet', 'greet', 'running')`, nil},
					{`INSERT INTO repositories (id, organization_id, project_id, name, url) VALUES ('repo_t', $1, 'prj_e', 'target', 'https://github.com/acme/target')`, nil},
					{`INSERT INTO pull_requests (id, organization_id, project_id, task_id, repository_id, number, url, head_branch, base_branch, head_sha, title, state)
					 VALUES ('pr_t', $1, 'prj_e', 'wi_e', 'repo_t', 1, 'https://github.com/acme/target/pull/1', 'dude/wi_e/attempt-1', 'main', 'person', 'Greet', $2::pull_request_state)`, []any{prState}},
					{`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, role, kind, status)
					 VALUES ('run_conductor', $1, 'prj_e', 'wi_e', 1, 'conductor', 'agent', 'running')`, nil},
					// The live delivery an implementer is created for.
					{`INSERT INTO workflow_runs (id, organization_id, workflow_type, idempotency_key, task_id, status, step, state)
					 VALUES ('wf_e', $1, $2, 'e', 'wi_e', 'running', 'implementRun', '{}')`, []any{WorkflowType}},
				}
				if v.saved {
					seed = append(seed, struct {
						sql  string
						args []any
					}{`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, phase, role, kind, status, base_refs, creation_key, branch)
					 VALUES ('run_saved', $1, 'prj_e', 'wi_e', 1, $2, 'implementer', 'agent', 'pending', '{"target":"saved"}', $3, NULLIF($4, ''))`, []any{phase, writerKey(sc, &saved, phase), branch}})
				}
				for _, q := range seed {
					if _, err := owner.Exec(ctx, q.sql, append([]any{org}, q.args...)...); err != nil {
						t.Fatal(err)
					}
				}
				sc.State, _ = json.Marshal(st)
				w := &steps{s: &Store{DB: app}, forges: forgeOf{}}
				step := map[string]func(context.Context, workflow.StepContext) (workflow.Result, error){PhaseImplement: w.implementRun, PhaseFix: w.fix, PhaseSimplify: w.simplify}[phase]
				res, err := step(ctx, sc)
				if err != nil {
					t.Fatal(err)
				}
				got := res.State.(*State)
				if got.Directed != nil {
					t.Errorf("the step did not spend the conductor's direction: %+v", got.Directed)
				}
				if !v.saved {
					var runs int
					var conductor, note string
					if err := owner.QueryRow(ctx, `SELECT count(*) OVER (), COALESCE(conductor_run_id, ''), COALESCE(conductor_note, '')
						FROM runs WHERE task_id = 'wi_e' AND phase = $1::run_phase`, phase).Scan(&runs, &conductor, &note); err != nil {
						t.Fatalf("%v; the step went to %s waiting on %v", err, res.Next, got.PendingRunIDs)
					}
					if runs != 1 || conductor != "run_conductor" || note != "n" {
						t.Fatalf("%d %s Runs, conductor %q note %q; want 1 with the conductor's mark", runs, phase, conductor, note)
					}
					return
				}
				if !reflect.DeepEqual(got.PendingRunIDs, []string{"run_saved"}) {
					t.Fatalf("replayed Runs = %v", got.PendingRunIDs)
				}
				if !reflect.DeepEqual(got.Heads, map[string]string{"target": "saved"}) {
					t.Fatalf("base refs = %v", got.Heads)
				}
				if (got.Candidate != nil) != candidate {
					t.Fatalf("candidate = %+v", got.Candidate)
				}
				wantRound := 3
				if candidate {
					wantRound = 4
					if got.Candidate.Branch != branch || !reflect.DeepEqual(got.Candidate.From, got.Heads) {
						t.Fatalf("candidate = %+v", got.Candidate)
					}
				}
				if got.PRFixKey != wantRound || got.PRIteration != 1 || !reflect.DeepEqual(got.PRFixes, st.PRFixes) || got.PRFixCharged != nil {
					t.Fatalf("replay changed budgets: %+v", got)
				}
				if err := app.InOrg(ctx, org, func(tx pgx.Tx) error {
					var count int
					if err := tx.QueryRow(ctx, `SELECT count(*) FROM runs WHERE task_id = 'wi_e' AND phase IS NOT NULL`).Scan(&count); err != nil {
						return err
					}
					if count != 1 {
						t.Errorf("Runs = %d, want 1", count)
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

// A pull request's fix is checked on a branch of its own beside the task
// branch, which a run's own branch name can never be.
func TestTheCandidateBranchIsBesideTheTaskBranch(t *testing.T) {
	if got := CandidateBranch("t1", 2); got != "dude/t1/attempt-2-candidate" {
		t.Errorf("CandidateBranch = %q", got)
	}
	st := &State{TaskID: "t1", Branch: "dude/t1/attempt-3"}
	if got := st.candidateBranch(); got != "dude/t1/attempt-3-candidate" {
		t.Errorf("a delivery's candidate = %q, want beside its own branch", got)
	}
	if got := (&State{TaskID: "t1"}).candidateBranch(); got != "dude/t1/attempt-1-candidate" {
		t.Errorf("a delivery naming no branch: %q", got)
	}
	adopted := &State{TaskID: "t1", Attempt: 2, Branch: "feature/half", Source: &Source{Kind: SourcePR, HeadBranch: "feature/half"}}
	if got := adopted.candidateBranch(); got != "dude/t1/attempt-2-candidate" {
		t.Errorf("an adopted source's candidate = %q, want under dude/, never beside the person's branch", got)
	}
}

// Refreshing a pull request's description after a promotion replaces
// dude's marked Review and Verification sections and keeps everything
// else, a person's edits and an unmarked heading of theirs included; a
// marked section the body lacks is appended.
func TestAPullRequestsSectionsAreReplacedAndTheRestKept(t *testing.T) {
	body := "Say hello\n\nA person's note.\n\n## Review\nTheir own review notes.\n\n" + markedReview("## Review\nold review") +
		"\n\n" + markedVerification("## Verification\nTested at `aaa`.") + "\n\n" + signOff + "\n\nOne of 2 pull requests."
	got := replaceVerification(replaceReview(body, "## Review\nnew review"), "## Verification\nTested at `bbb`.")
	want := "Say hello\n\nA person's note.\n\n## Review\nTheir own review notes.\n\n" + markedReview("## Review\nnew review") +
		"\n\n" + markedVerification("## Verification\nTested at `bbb`.") + "\n\n" + signOff + "\n\nOne of 2 pull requests."
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	// A description that had no marked Review gets one, after the rest.
	plain := "Say hello\n\n" + signOff
	if got := replaceReview(plain, "## Review\n1 finding."); got != plain+"\n\n"+markedReview("## Review\n1 finding.") {
		t.Errorf("added: %q", got)
	}
	// A description opened before Review was marked keeps its unmarked one.
	legacy := "Say hello\n\n## Review\nTheir own review notes.\n\n" + signOff
	if got := replaceReview(legacy, "## Review\n1 finding."); got != legacy+"\n\n"+markedReview("## Review\n1 finding.") {
		t.Errorf("an unmarked Review was not kept: %q", got)
	}
	// The body dude opens with carries both sections marked, before the sign-off.
	opened := prBody("Say hello", nil, "## Review\nr", "## Verification\nv")
	if want := "Say hello\n\n" + markedReview("## Review\nr") + "\n\n" + markedVerification("## Verification\nv") + "\n\n" + signOff; opened != want {
		t.Errorf("opened:\n%q\nwant:\n%q", opened, want)
	}
	if again := replaceVerification(replaceReview(opened, "## Review\nr"), "## Verification\nv"); again != opened {
		t.Errorf("a refresh with the same sections changed it:\n%q", again)
	}
}
