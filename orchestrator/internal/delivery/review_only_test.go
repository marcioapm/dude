package delivery

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
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
// the creation key and Continue. The others make no writer: each is named,
// by file and function, with why. An insert is found in any spelling
// (runs( or public.runs, any case and spacing), in a string literal or a
// concatenation of them, and the file's text is counted too, so none
// escapes the walk.
func TestGuardEveryRunsInsertGoesThroughTheGuard(t *testing.T) {
	root := filepath.Join("..", "..")
	unguarded := runsInsertsPastTheGuard(t, root)
	for _, e := range unguarded {
		t.Error(e)
	}
}

// insertIntoRuns is an insert into the runs table, however it is spelt.
var insertIntoRuns = regexp.MustCompile(`(?i)\binsert\s+into\s+(public\.)?runs\b`)

// notPhaseRuns are the inserts into runs that make no phase writer, keyed
// by "file:func" (the file relative to the orchestrator's root).
var notPhaseRuns = map[string]string{
	"internal/servers/previews.go:StartPreview":                  "a preview Run: no phase, no checkout to write",
	"internal/servers/branches.go:StartBranchPreview":            "a branch preview Run: no task",
	"internal/delivery/session_swap.go:RetryBrainstorm":          "a session's brainstorm: no task",
	"internal/delivery/session.go:startBrainstorm":               "a session's brainstorm: no task",
	"internal/delivery/child.go:startSessionChildTx":             "a session's child, investigator only: no task",
	"internal/delivery/lost_conductor.go:ReplaceLostConductorTx": "a conductor: writes only by publish (ConductPublish, guarded)",
	"internal/delivery/lost_conductor.go:ReaskLostConductorTx":   "a conductor",
	"internal/delivery/conductor.go:startConductor":              "a conductor",
	"internal/delivery/conductor.go:RetryConductor":              "a conductor",
}

const guardedInsert = "internal/delivery/store.go:createPhaseRunTx"

// runsInsertsPastTheGuard walks the non-test Go files under root and says
// what is wrong: an insert into runs outside createPhaseRunTx and not
// named in notPhaseRuns, one the walk did not account for in a file's
// text, a named one no longer there, or createPhaseRunTx's guard moved.
func runsInsertsPastTheGuard(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	found := map[string]bool{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, src, parser.ParseComments)
		if err != nil {
			return err
		}
		// The file's text, its comments blanked: every insert there must be
		// one the walk below found in a function.
		text := []byte(string(src))
		for _, c := range f.Comments {
			for i := fset.Position(c.Pos()).Offset; i < fset.Position(c.End()).Offset; i++ {
				text[i] = ' '
			}
		}
		inText := len(insertIntoRuns.FindAllIndex(text, -1))
		walked := 0
		for _, decl := range f.Decls {
			name := "(top level)"
			if fn, ok := decl.(*ast.FuncDecl); ok {
				name = fn.Name.Name
			}
			key := rel + ":" + name
			ast.Inspect(decl, func(n ast.Node) bool {
				e, ok := n.(ast.Expr)
				if !ok {
					return true
				}
				joined, ok := stringPieces(e)
				if !ok {
					return true
				}
				if k := len(insertIntoRuns.FindAllStringIndex(joined, -1)); k > 0 {
					walked += k
					found[key] = true
					if _, named := notPhaseRuns[key]; !named && key != guardedInsert {
						out = append(out, fmt.Sprintf("%s inserts into runs past the review-only guard: make it through createPhaseRunTx, or name it in notPhaseRuns with why", key))
					}
				}
				// A string expression is taken whole: its pieces are not counted again.
				return false
			})
			if fn, ok := decl.(*ast.FuncDecl); ok && key == guardedInsert {
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
					out = append(out, "createPhaseRunTx's first statement is not the review-only guard")
				}
			}
		}
		if inText > walked {
			out = append(out, fmt.Sprintf("%s: %d inserts into runs in its text, %d in the functions walked", rel, inText, walked))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !found[guardedInsert] {
		out = append(out, "createPhaseRunTx's insert not found: the walk saw nothing")
	}
	for key := range notPhaseRuns {
		if !found[key] {
			out = append(out, fmt.Sprintf("%s no longer inserts into runs: drop it from notPhaseRuns", key))
		}
	}
	return out
}

// stringPieces is a string expression's text: a literal, or literals
// joined with +, in parentheses or not. ok is false for anything else.
func stringPieces(e ast.Expr) (string, bool) {
	switch e := e.(type) {
	case *ast.BasicLit:
		if e.Kind != token.STRING {
			return "", false
		}
		v, err := strconv.Unquote(e.Value)
		if err != nil {
			return e.Value, true
		}
		return v, true
	case *ast.ParenExpr:
		return stringPieces(e.X)
	case *ast.BinaryExpr:
		if e.Op != token.ADD {
			return "", false
		}
		l, lok := stringPieces(e.X)
		r, rok := stringPieces(e.Y)
		return l + r, lok && rok
	}
	return "", false
}
