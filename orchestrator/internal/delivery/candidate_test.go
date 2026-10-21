package delivery

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

// A replayed BeforePR integration fix keeps the candidate decision its Run
// was created with, whatever the other repository's pull request did since:
// the Run publishes to the branch it was created for.
func TestAReplayedBeforePRIntegrationKeepsItsRunsCandidate(t *testing.T) {
	for _, tc := range []struct {
		name, prState, runBranch string
		candidate                bool
	}{
		{"created on a candidate, the pull request merged since", "merged", "dude/wi_e/attempt-1-candidate", true},
		{"created on the task branch, a pull request open now", "open", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
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
					VALUES ('run_fix', $1, 'prj_e', 'wi_e', 1, 'fix', 'implementer', 'agent', 'pending', '{"target":"aaa","web":"bbb"}', 'wf_e:prfix:1', NULLIF($2, ''))`, []any{tc.runBranch}},
			} {
				if _, err := owner.Exec(ctx, s.sql, append([]any{org}, s.args...)...); err != nil {
					t.Fatal(err)
				}
			}
			st := &State{TaskID: "wi_e", ProjectID: "prj_e", Attempt: 1, Branch: "dude/wi_e/attempt-1", PullRequestIDs: []string{"pr_t"},
				Heads:       map[string]string{"target": "aaa", "web": "bbb"},
				Integration: &Integration{Repo: "web", Base: "main", Head: "bbb", BaseHead: "ccc", BeforePR: true}}
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
		})
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
