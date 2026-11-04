package delivery

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// toPersonInternals are ToPersonTx and the functions it is made of: the
// only code that may ask a person on a task.
var toPersonInternals = map[string]bool{
	"delivery/to_person.go:ToPersonTx":         true,
	"delivery/to_person.go:askPersonTx":        true,
	"delivery/to_person.go:escalateToPersonTx": true,
}

// toPersonAllowed reach a person some other way, for good: a brainstorm
// session's question has no task and no conductor.
var toPersonAllowed = map[string]string{
	"agenttools/brainstorm.go:sessionAsk": "a session's question: no task, no conductor",
}

// toPersonOffenders still reach a person without ToPersonTx, or through it
// for a reason conductor-first takes away (WhyWriter, WhyEscalation): each
// with the PR that removes it, which its code names in a
// TODO(conductor-first PRn). This list only shrinks.
var toPersonOffenders = map[string]string{
	// A10: reviewer questions set awaiting_input whoever decides.
	"delivery/review_questions.go:holdForQuestions": "PR4b",
	// A12: the idle park sets awaiting_input.
	"phases/sync.go:requestPause": "PR4b",
	// A1: a writer's ask_person goes to the person.
	"agenttools/tools.go:askPerson": "PR4a",
	// A6: dude's escalations go to the person first.
	"delivery/workflow.go:escalate": "PR2",
	// A7: the checks gate's bound goes to the person first.
	"delivery/checks_escalation.go:EscalateChecksTx": "PR2",
	// A9: a promotion stopped at boot goes to the person first.
	"delivery/convert_candidates.go:stopMidPromotionTx": "PR2",
}

// transitionalWhys are ToPersonTx's reasons conductor-first removes: a call
// naming one is an offender.
var transitionalWhys = []string{"WhyWriter", "WhyEscalation"}

var openQuestionInsert = regexp.MustCompile(`(?is)INSERT\s+INTO\s+questions\b`)
var awaitingInputSQL = regexp.MustCompile(`(?i)status\s*=\s*'awaiting_input'`)

// TestNothingReachesAPersonButThroughToPersonTx: nothing creates an open
// question for a person, emits question.asked, or moves a task to
// awaiting_input except ToPersonTx, the allowed, and the offenders listed,
// each still marked in its code with the PR that removes it. A listed
// offender that no longer offends fails too: take it off the list.
func TestNothingReachesAPersonButThroughToPersonTx(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	found := map[string][]string{}
	todos := map[string]string{}
	fset := token.NewFileSet()
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			site := rel + ":" + fn.Name.Name
			for _, c := range f.Comments {
				if c.Pos() >= fn.Pos() && c.End() <= fn.End() {
					if m := regexp.MustCompile(`TODO\(conductor-first (PR\w+)\)`).FindStringSubmatch(c.Text()); m != nil {
						todos[site] = m[1]
					}
				}
			}
			for _, why := range reachesPerson(fn.Body) {
				found[site] = append(found[site], fset.Position(why.pos).String()+": "+why.what)
			}
		}
		return nil
	})
	if walkErr != nil {
		t.Fatal(walkErr)
	}
	if len(found) == 0 {
		t.Fatal("the scan found nothing: not even ToPersonTx")
	}
	for site, whats := range found {
		switch {
		case toPersonInternals[site]:
		case toPersonAllowed[site] != "":
		case toPersonOffenders[site] != "":
			if todos[site] != toPersonOffenders[site] {
				t.Errorf("%s is an offender removed in %s, but its code says TODO(conductor-first %s)",
					site, toPersonOffenders[site], todos[site])
			}
		default:
			t.Errorf("%s reaches a person without delivery.ToPersonTx:\n\t%s", site, strings.Join(whats, "\n\t"))
		}
	}
	for site := range toPersonOffenders {
		if found[site] == nil {
			t.Errorf("%s no longer reaches a person but through ToPersonTx: take it off toPersonOffenders", site)
		}
	}
	for site := range toPersonInternals {
		if found[site] == nil {
			t.Errorf("%s: a ToPersonTx internal the scan no longer sees", site)
		}
	}
}

type personReach struct {
	pos  token.Pos
	what string
}

// reachesPerson lists what in a function body reaches a person directly,
// or through ToPersonTx for a transitional reason.
func reachesPerson(body *ast.BlockStmt) []personReach {
	var out []personReach
	ast.Inspect(body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.BasicLit:
			if n.Kind != token.STRING {
				return true
			}
			s, err := strconv.Unquote(n.Value)
			if err != nil {
				return true
			}
			// An answered row (RecordDecisionTx) asks nobody.
			if openQuestionInsert.MatchString(s) && !strings.Contains(s, "'answered'") {
				out = append(out, personReach{n.Pos(), "INSERT INTO questions"})
			}
			if awaitingInputSQL.MatchString(s) && regexp.MustCompile(`(?i)\bSET\b`).MatchString(s) {
				out = append(out, personReach{n.Pos(), "SET status = 'awaiting_input'"})
			}
			if s == "question.asked" {
				out = append(out, personReach{n.Pos(), `"question.asked"`})
			}
		case *ast.CallExpr:
			name := callName(n.Fun)
			switch name {
			case "SetTaskStatusTx", "SetTaskStatus":
				// The status moved to: SetTaskStatusTx(ctx, tx, org, project,
				// task, from, status, reason); Store.SetTaskStatus(ctx, org, st,
				// status, reason).
				at := 6
				if name == "SetTaskStatus" {
					at = 3
				}
				if at < len(n.Args) && isLiteral(n.Args[at], "awaiting_input") {
					out = append(out, personReach{n.Pos(), name + ` to "awaiting_input"`})
				}
			}
			for _, a := range n.Args {
				if callName(a) == "EvQuestionAsked" && emits(name) {
					out = append(out, personReach{n.Pos(), name + "(EvQuestionAsked)"})
				}
			}
		case *ast.Ident:
			// A ToPersonTx call's reason, however it is passed.
			if slices.Contains(transitionalWhys, n.Name) {
				out = append(out, personReach{n.Pos(), "ToPersonTx for " + n.Name})
			}
		case *ast.KeyValueExpr:
			if k, ok := n.Key.(*ast.Ident); ok && k.Name == "Type" && callName(n.Value) == "EvQuestionAsked" {
				out = append(out, personReach{n.Pos(), "Type: EvQuestionAsked"})
			}
		}
		return true
	})
	return out
}

// emits: a call that writes an event of the type it is given.
func emits(name string) bool {
	return slices.Contains([]string{"emitTx", "Emit", "Event", "event", "Append", "appendEvent", "humanEvent"}, name)
}

func callName(e ast.Expr) string {
	switch e := e.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		return e.Sel.Name
	}
	return ""
}

func isLiteral(e ast.Expr, want string) bool {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return false
	}
	s, err := strconv.Unquote(lit.Value)
	return err == nil && s == want
}
