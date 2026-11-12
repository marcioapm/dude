package delivery

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// reviewOnlyWorld is the child world's task made a review-only one of its
// pull request #7.
func reviewOnlyWorld(t *testing.T) *childWorld {
	w := newChildWorld(t)
	w.exec(`INSERT INTO task_sources (task_id, organization_id, repository_id, kind, number, head_branch, base_branch, mode)
		VALUES ('wi_life', $1, 'repo_life', 'pr', 7, 'work', 'main', 'review_only')`, w.org)
	return w
}

func (w *childWorld) phaseRuns(phase string) int {
	w.t.Helper()
	var n int
	if err := w.owner.QueryRow(w.ctx, `SELECT count(*) FROM runs WHERE task_id = 'wi_life' AND phase::text = $1`, phase).Scan(&n); err != nil {
		w.t.Fatal(err)
	}
	return n
}

func refusedAsReviewOnly(t *testing.T, what string, err error) {
	t.Helper()
	if !IsRefusal(err) || !strings.Contains(err.Error(), "reviews pull request #7 and changes no code") {
		t.Fatalf("%s on a review-only task: %v, want the review-only refusal", what, err)
	}
}

// G1: no writer Run is made for a review-only task, whatever asks for one:
// the workflow's steps (CreatePhaseRun), a conductor's start_phase, its
// start_agent writer child, a publish. Review and test are made.
func TestGuardNoWriterRunForAReviewOnlyTask(t *testing.T) {
	w := reviewOnlyWorld(t)
	for _, phase := range []string{PhaseImplement, PhaseFix, PhaseSimplify} {
		if !Publishes[phase] {
			t.Fatalf("%s is not a writer", phase)
		}
		_, err := w.s.CreatePhaseRun(w.ctx, w.org, PhaseRun{TaskID: "wi_life", Phase: phase, WorkflowID: "wf_life", Attempt: 3})
		refusedAsReviewOnly(t, "the workflow's "+phase, err)
		_, err = w.s.CreatePhaseRun(w.ctx, w.org, PhaseRun{TaskID: "wi_life", Phase: phase, WorkflowID: "wf_life", Attempt: 3,
			Key: "keyed-" + phase, Continue: &Continue{Predecessor: "run_parent"}})
		refusedAsReviewOnly(t, "a continued "+phase, err)
		if n := w.phaseRuns(phase); n != 0 {
			t.Fatalf("%d %s Runs made", n, phase)
		}
	}
	for _, profile := range []string{"implementer", "fixer", "simplifier"} {
		in := w.in
		in.Profile, in.Capabilities.PublishCode, in.Key = profile, true, "child-"+profile
		_, err := w.s.StartChild(w.ctx, in)
		refusedAsReviewOnly(t, "a start_agent "+profile, err)
	}
	ref := w.in.Parent.Run
	for _, phase := range []string{PhaseImplement, PhaseFix, PhaseSimplify} {
		err := w.s.DB.InOrg(w.ctx, w.org, func(tx pgx.Tx) error {
			_, err := ConductStartPhase(w.ctx, tx, ref, StartPhase{Phase: phase, Note: "fix it"})
			return err
		})
		refusedAsReviewOnly(t, "start_phase "+phase, err)
	}
	w.exec(`UPDATE runs SET kind = 'agent' WHERE id = 'run_parent'`)
	err := w.s.DB.InOrg(w.ctx, w.org, func(tx pgx.Tx) error {
		_, err := ConductPublish(w.ctx, tx, ref, "publish it")
		return err
	})
	refusedAsReviewOnly(t, "publish", err)
	if w.count("conductor_publishes") != 0 {
		t.Fatal("a publish was recorded")
	}
	for _, action := range []string{ActionRequestReview, ActionMarkReady, ActionResolveThread} {
		err := w.s.DB.InOrg(w.ctx, w.org, func(tx pgx.Tx) error {
			_, err := CheckPRActionTx(w.ctx, tx, ref, nil, PRAction{Action: action, PR: "7", Logins: []string{"ana"}, Thread: "PRRT_1"})
			return err
		})
		if !IsRefusal(err) || !strings.Contains(err.Error(), "does not change it") {
			t.Fatalf("%s on a review-only task: %v", action, err)
		}
	}
	for _, phase := range []string{PhaseImplement, PhaseFix, PhaseSimplify} {
		if n := w.phaseRuns(phase); n != 0 {
			t.Fatalf("%d %s Runs made", n, phase)
		}
	}
	// What reads is made: a review and a test, as Runs and as a child.
	for _, phase := range []string{PhaseReview, PhaseTest} {
		if _, err := w.s.CreatePhaseRun(w.ctx, w.org, PhaseRun{TaskID: "wi_life", Phase: phase, WorkflowID: "wf_life", Attempt: 3}); err != nil {
			t.Fatalf("%s on a review-only task: %v", phase, err)
		}
	}
	in := w.in
	in.Profile, in.Key = "reviewer", "child-reviewer"
	if _, err := w.s.StartChild(w.ctx, in); err != nil {
		t.Fatalf("a reviewer child: %v", err)
	}
}

// G1 the other way: the guard is inert for a task in a writing mode.
func TestGuardLeavesAWritingModeAlone(t *testing.T) {
	for _, mode := range []string{ModeReview, ModeContinue} {
		w := newChildWorld(t)
		w.exec(`INSERT INTO task_sources (task_id, organization_id, repository_id, kind, number, head_branch, base_branch, mode)
			VALUES ('wi_life', $1, 'repo_life', 'pr', 7, 'work', 'main', $2)`, w.org, mode)
		if err := w.s.DB.InOrg(w.ctx, w.org, func(tx pgx.Tx) error {
			return refuseReviewOnlyWriterTx(w.ctx, tx, "wi_life")
		}); err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		_, err := w.s.CreatePhaseRun(w.ctx, w.org, PhaseRun{TaskID: "wi_life", Phase: PhaseFix, WorkflowID: "wf_life", Attempt: 3})
		if err != nil && strings.Contains(err.Error(), "changes no code") {
			t.Fatalf("%s refused as review only: %v", mode, err)
		}
	}
}

// G1b, the structure: every INSERT INTO runs that can make a task's phase
// Run is createPhaseRunTx's, and the guard is its first statement, before
// the creation key and Continue. The others make no writer: each is named
// with why.
func TestGuardEveryRunsInsertGoesThroughTheGuard(t *testing.T) {
	notPhaseRuns := map[string]string{
		"StartPreview":           "a preview Run: no phase, no checkout to write",
		"StartBranchPreview":     "a branch preview Run: no task",
		"RetryBrainstorm":        "a session's brainstorm: no task",
		"startBrainstorm":        "a session's brainstorm: no task",
		"startSessionChildTx":    "a session's child, investigator only: no task",
		"ReplaceLostConductorTx": "a conductor: writes only by publish (ConductPublish, guarded)",
		"ReaskLostConductorTx":   "a conductor",
		"startConductor":         "a conductor",
		"RetryConductor":         "a conductor",
	}
	root := filepath.Join("..", "..")
	found := map[string]bool{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING || !strings.Contains(strings.Join(strings.Fields(lit.Value), " "), "INSERT INTO runs ") {
					return true
				}
				found[fn.Name.Name] = true
				if _, ok := notPhaseRuns[fn.Name.Name]; !ok && fn.Name.Name != "createPhaseRunTx" {
					t.Errorf("%s: %s inserts into runs past the review-only guard: make it through createPhaseRunTx, or name it here with why",
						path, fn.Name.Name)
				}
				return true
			})
			if fn.Name.Name == "createPhaseRunTx" && filepath.Base(path) == "store.go" {
				first, ok := fn.Body.List[0].(*ast.IfStmt)
				var calls []string
				if ok {
					ast.Inspect(first, func(n ast.Node) bool {
						if c, ok := n.(*ast.CallExpr); ok {
							if id, ok := c.Fun.(*ast.Ident); ok {
								calls = append(calls, id.Name)
							}
						}
						return true
					})
				}
				if !slices.Contains(calls, "refuseReviewOnlyWriterTx") {
					t.Errorf("createPhaseRunTx's first statement is not the review-only guard")
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !found["createPhaseRunTx"] {
		t.Fatal("createPhaseRunTx's insert not found: the walk saw nothing")
	}
	for name := range notPhaseRuns {
		if !found[name] {
			t.Errorf("%s no longer inserts into runs: drop it from the list", name)
		}
	}
}
