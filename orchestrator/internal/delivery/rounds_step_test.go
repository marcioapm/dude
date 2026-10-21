package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

func TestRoundCompareFailuresAndCaching(t *testing.T) {
	for _, status := range []int{200, 429, 403, 502, 404, 422, 401} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			f := newVerdictFixture(t)
			exec := func(q string, args ...any) {
				t.Helper()
				if _, err := f.owner.Exec(f.ctx, q, args...); err != nil {
					t.Fatal(err)
				}
			}
			exec(`INSERT INTO workflow_runs (id, organization_id, workflow_type, idempotency_key, status, step, state, task_id) VALUES ('wfr_delta', $1, 'task.delivery', 'delta', 'running', 'review', '{}', 'wi_c')`, f.org)
			exec(`INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch) VALUES ('repo_second', $1, 'prj_c', 'second', 'https://github.com/acme/second.git', 'main')`, f.org)
			exec(`INSERT INTO task_repositories (task_id, repository_id, organization_id) VALUES ('wi_c', 'repo_c', $1), ('wi_c', 'repo_second', $1)`, f.org)
			for _, cat := range []string{"correctness", "security", "quality"} {
				exec(`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, category, base_refs, ended_at) VALUES ($1, $2, 'prj_c', 'wi_c', 1, 'completed', 'review', 'reviewer', $3, '{"repo":"aaa","second":"aaa"}', now())`, "run_"+cat, f.org, cat)
			}
			calls := 0
			answerStatus := status
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.Contains(r.URL.Path, "/compare/") {
					t.Errorf("unexpected request %s", r.URL.Path)
				}
				calls++
				w.Header().Set("Retry-After", "60")
				w.WriteHeader(answerStatus)
				if answerStatus == 200 {
					_ = json.NewEncoder(w).Encode(map[string]any{"files": []any{map[string]any{"filename": "a.go", "additions": 10, "deletions": 0}}})
				} else {
					_ = json.NewEncoder(w).Encode(map[string]string{"message": "rate limit exceeded"})
				}
			}))
			defer srv.Close()
			f.steps.forges = forgeOf{forge.NewGitHub(forge.Credential{Auth: "pat", Secret: "token", APIBaseURL: srv.URL})}
			st := &State{TaskID: "wi_c", Attempt: 1, Policy: DefaultPolicy(), Heads: map[string]string{"repo": "bbb", "second": "bbb"}}
			sc := workflow.StepContext{WorkflowRunID: "wfr_delta", OrganizationID: f.org}
			if status == 200 {
				lines, err := f.steps.s.DeltaLines(f.ctx, f.org, "wi_c", nil, map[string]string{"repo": "aaa"}, st.Heads)
				if err != nil || lines != MaxResumeDeltaLines+1 {
					t.Fatalf("nil credential delta=%d error=%v", lines, err)
				}
			}
			c, err := f.steps.goesOn(context.Background(), sc, st, PhaseReview)("correctness")
			if status == 429 || status == 403 || status == 502 {
				var wait workflow.Wait
				if !errors.As(err, &wait) || wait.After != time.Minute {
					t.Fatalf("compare error = %v, want 60s workflow wait", err)
				}
				answerStatus = 200
				calls = 0
			} else if err != nil {
				t.Fatal(err)
			} else if status != 200 && c.DeltaLines != MaxResumeDeltaLines+1 {
				t.Fatalf("permanent error delta = %d", c.DeltaLines)
			}
			if status != 200 && status != 429 && status != 403 && status != 502 {
				return
			}
			for retry := 0; retry < 3; retry++ {
				goesOn := f.steps.goesOn(context.Background(), sc, st, PhaseReview)
				for _, cat := range []string{"correctness", "security", "quality"} {
					c, err = goesOn(cat)
					if err != nil || c == nil || c.DeltaLines != 20 {
						t.Fatalf("decision = %+v, %v", c, err)
					}
				}
			}
			if calls != 2 {
				t.Fatalf("%d compares for 2 repos, 3 categories and 3 retries; want 2", calls)
			}
			st.Heads["repo"] = "ccc"
			if _, err := f.steps.goesOn(f.ctx, sc, st, PhaseReview)("correctness"); err != nil {
				t.Fatal(err)
			}
			if calls != 4 {
				t.Fatalf("changed heads did not invalidate measurement: %d calls", calls)
			}
		})
	}
}

type forgeErr struct{ err error }

func (f forgeErr) For(context.Context, string) (*forge.GitHub, error) { return nil, f.err }

// A credential lookup that failed for another reason than the forge's
// answer (the database) is the step's error, retried: it is not taken as
// an unmeasurable change, which would let the kept predecessor go.
func TestARoundsCredentialLookupFailureIsRetried(t *testing.T) {
	f := newVerdictFixture(t)
	for _, q := range []string{
		`INSERT INTO workflow_runs (id, organization_id, workflow_type, idempotency_key, status, step, state, task_id) VALUES ('wfr_delta', $1, 'task.delivery', 'delta', 'running', 'review', '{}', 'wi_c')`,
		`INSERT INTO task_repositories (task_id, repository_id, organization_id) VALUES ('wi_c', 'repo_c', $1)`,
		`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, category, base_refs, ended_at) VALUES ('run_correctness', $1, 'prj_c', 'wi_c', 1, 'completed', 'review', 'reviewer', 'correctness', '{"repo":"aaa"}', now())`,
	} {
		if _, err := f.owner.Exec(f.ctx, q, f.org); err != nil {
			t.Fatal(err)
		}
	}
	down := errors.New("load forge credential: connection reset")
	f.steps.forges = forgeErr{down}
	st := &State{TaskID: "wi_c", Attempt: 1, Policy: DefaultPolicy(), Heads: map[string]string{"repo": "bbb"}}
	sc := workflow.StepContext{WorkflowRunID: "wfr_delta", OrganizationID: f.org}
	c, err := f.steps.goesOn(f.ctx, sc, st, PhaseReview)("correctness")
	if !errors.Is(err, down) {
		t.Fatalf("decision = %+v, %v; want the lookup's error", c, err)
	}
	var cached int
	if err := f.owner.QueryRow(f.ctx, `SELECT count(*) FROM workflow_runs, jsonb_object_keys(COALESCE(state->'roundDelta', '{}')) WHERE id = 'wfr_delta'`).Scan(&cached); err != nil || cached != 0 {
		t.Fatalf("%d measurements cached (%v) after a failed lookup, want 0", cached, err)
	}
	// A refusal from GitHub itself is the forge's answer: the round goes fresh.
	f.steps.forges = forgeErr{&forge.Error{Status: 401, Message: "Bad credentials"}}
	if c, err := f.steps.goesOn(f.ctx, sc, st, PhaseReview)("correctness"); err != nil || c == nil || c.DeltaLines != MaxResumeDeltaLines+1 {
		t.Fatalf("refused lookup decision = %+v, %v; want delta %d", c, err, MaxResumeDeltaLines+1)
	}
}

func TestIntegrationRoundsStayFresh(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, integration := range []bool{false, true} {
			for _, phase := range []string{PhaseReview, PhaseTest, PhaseImplement, PhaseFix, PhaseSimplify} {
				p := DefaultPolicy()
				p.ResumeRounds = enabled
				if got, want := RoundContinues(p, phase, integration), enabled && !integration && (phase == PhaseReview || phase == PhaseTest || phase == PhaseFix); got != want {
					t.Fatalf("%s enabled=%v integration=%v: %v", phase, enabled, integration, got)
				}
			}
		}
	}
}
