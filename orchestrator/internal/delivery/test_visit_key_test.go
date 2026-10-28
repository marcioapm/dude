package delivery

import (
	"context"
	"fmt"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

// The test and simplify steps' creation key is one per visit: a replay of
// a visit (the same state) finds the Run it created, and a later visit —
// after a decision carried out, or after a review round — creates a new
// one. Each variant of the key: plain, after a failed test's fix round, and
// in an integration.
func TestATestOrSimplifyVisitIsItsOwnRun(t *testing.T) {
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	ctx := context.Background()
	for _, sql := range []string{
		`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_k', $1, 'P', 'p', 'P')`,
		`INSERT INTO tasks (id, organization_id, project_id, number, title, goal, status)
			VALUES ('wi_k', $1, 'prj_k', 1, 'Greet', 'greet', 'running')`,
	} {
		if _, err := owner.Exec(ctx, sql, org); err != nil {
			t.Fatal(err)
		}
	}
	s := &Store{DB: app}
	sc := workflow.StepContext{WorkflowRunID: "wfr_k", OrganizationID: org}
	variants := map[string]func(*State){
		"plain":      func(*State) {},
		"after-test": func(st *State) { st.TestFixRounds = 1 },
		"integration": func(st *State) {
			st.Integration = &Integration{RunID: "run_integration"}
		},
	}
	phases := map[string]string{":test": PhaseTest, ":simplify": PhaseSimplify}
	for name, variant := range variants {
		for part, phase := range phases {
			t.Run(name+part, func(t *testing.T) {
				first := &State{TaskID: "wi_k", Attempt: 1, Decisions: 3, Iteration: 2}
				variant(first)
				create := func(st *State) string {
					t.Helper()
					id, err := s.CreatePhaseRun(ctx, org, PhaseRun{TaskID: "wi_k", Phase: phase, WorkflowID: sc.WorkflowRunID,
						Key: testRoundKey(sc, st, part), Attempt: 1, BaseRefs: map[string]string{"target": fmt.Sprint(st.Decisions, st.Iteration)}})
					if err != nil {
						t.Fatal(err)
					}
					return id
				}
				run := create(first)
				replayed := *first
				if again := create(&replayed); again != run {
					t.Fatalf("a replay of the visit created %s, want its own %s", again, run)
				}
				decided := *first
				decided.Decisions++
				afterDecision := create(&decided)
				if afterDecision == run {
					t.Fatalf("the visit after a decision got the first visit's %s (key %q)", run, testRoundKey(sc, &decided, part))
				}
				reviewed := *first
				reviewed.Iteration++
				if afterReview := create(&reviewed); afterReview == run || afterReview == afterDecision {
					t.Fatalf("the visit after a review round got an earlier visit's %s (key %q)", afterReview, testRoundKey(sc, &reviewed, part))
				}
			})
		}
	}
}
