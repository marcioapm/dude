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
	"delivery/to_person.go:ToPersonTx":  true,
	"delivery/to_person.go:askPersonTx": true,
	// A conductor-held reviewer question given to the person.
	"delivery/to_person.go:askReviewQuestionTx": true,
}

// toPersonAllowed reach a person some other way, for good: a brainstorm
// session's question has no task and no conductor.
var toPersonAllowed = map[string]string{
	"agenttools/brainstorm.go:sessionAsk": "a session's question: no task, no conductor",
}

// toPersonReaders name "awaiting_input" or EvQuestionAsked only to read
// them, in a shape isRead does not know. Each says why; keep it short.
var toPersonReaders = map[string]string{
	"delivery/replay.go:replayTypes":  "the event types the replay reads and renders",
	"delivery/relations.go:stateWord": "a blocker's state in words, keyed by task status",
}

// toPersonOffenders still reach a person without ToPersonTx, or through it
// for a reason conductor-first takes away (WhyWriter, WhyEscalation): each
// with the PR that removes it, which its code names in a
// TODO(conductor-first PRn). This list only shrinks.
var toPersonOffenders = map[string]string{
	// A12: the idle park sets awaiting_input.
	"phases/sync.go:requestPause": "PR4b",
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
//
// What the scan flags (reachesPerson), in function bodies and in file-level
// const and var values alike:
//   - SQL that inserts an open question or sets status = 'awaiting_input';
//   - any reference to EvQuestionAsked, or the literal "question.asked";
//   - any "awaiting_input" literal, wherever it goes (a local, a var, a
//     helper's argument, a parameterised SQL's argument), unless it is a
//     read (isRead);
//   - a ToPersonTx call for a transitional reason.
//
// testdata/to_person_bypasses holds the known ways round this;
// TestTheScanSeesEveryKnownBypass keeps the scan seeing them.
func TestConductorQuestionInsertCannotReachAPerson(t *testing.T) {
	if !strings.HasSuffix(ConductorQuestionInsertSQL, "'conductor')") || !strings.Contains(ConductorQuestionInsertSQL, "items,addressee)") {
		t.Fatal("conductor question insert must explicitly address its row to conductor")
	}
}

func TestNothingReachesAPersonButThroughToPersonTx(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	type parsed struct {
		f   *ast.File
		rel string
		dir string
	}
	var files []parsed
	consts := map[string]map[string]string{} // package dir → its string consts
	fset := token.NewFileSet()
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() && d.Name() == "testdata" {
			return filepath.SkipDir
		}
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		dir := filepath.Dir(path)
		if consts[dir] == nil {
			consts[dir] = map[string]string{}
		}
		files = append(files, parsed{f, filepath.ToSlash(rel), dir})
		return nil
	})
	if walkErr != nil {
		t.Fatal(walkErr)
	}
	for _, p := range files {
		stringConsts(p.f, consts[p.dir])
	}
	// A const built from one declared after it, or in a later file.
	for _, p := range files {
		stringConsts(p.f, consts[p.dir])
	}
	found := map[string][]string{}
	todos := map[string]string{}
	for _, p := range files {
		for site, whats := range scanPersonReach(fset, p.f, p.rel, todos, consts[p.dir]) {
			found[site] = append(found[site], whats...)
		}
	}
	if len(found) == 0 {
		t.Fatal("the scan found nothing: not even ToPersonTx")
	}
	for site, whats := range found {
		switch {
		case toPersonInternals[site]:
		case toPersonAllowed[site] != "":
		case toPersonReaders[site] != "":
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
	for site := range toPersonAllowed {
		if found[site] == nil {
			t.Errorf("%s: allowed, but the scan no longer sees it: take it off toPersonAllowed", site)
		}
	}
	for site := range toPersonReaders {
		if found[site] == nil {
			t.Errorf("%s: a reader the scan no longer sees: take it off toPersonReaders", site)
		}
	}
}

// stringConsts adds a file's package-level string consts to into, so a
// query held in one (tx.QueryRow(ctx, someSQL, ...)) is read as its text. A
// const built from another not yet seen is skipped: call it again.
func stringConsts(f *ast.File, into map[string]string) {
	for _, decl := range f.Decls {
		g, ok := decl.(*ast.GenDecl)
		if !ok || g.Tok != token.CONST {
			continue
		}
		for _, spec := range g.Specs {
			vs := spec.(*ast.ValueSpec)
			for i, name := range vs.Names {
				if i < len(vs.Values) {
					if s, ok := sqlText(vs.Values[i], into); ok {
						into[name.Name] = s
					}
				}
			}
		}
	}
}

// sqlText is a string expression's text: literals, the consts it names, and
// their concatenation. ok is false when any part of it is not known.
func sqlText(e ast.Expr, consts map[string]string) (string, bool) {
	switch e := e.(type) {
	case *ast.BasicLit:
		if e.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(e.Value)
		return s, err == nil
	case *ast.Ident:
		s, ok := consts[e.Name]
		return s, ok
	case *ast.ParenExpr:
		return sqlText(e.X, consts)
	case *ast.BinaryExpr:
		if e.Op != token.ADD {
			return "", false
		}
		l, ok := sqlText(e.X, consts)
		if !ok {
			return "", false
		}
		r, ok := sqlText(e.Y, consts)
		return l + r, ok
	}
	return "", false
}

// scanPersonReach lists, per site, what in a parsed file reaches a person:
// a function's site is file:func, a file-level const or var's file:name. It
// records each function's TODO(conductor-first PRn) in todos. consts are the
// package's string consts (stringConsts).
func scanPersonReach(fset *token.FileSet, f *ast.File, rel string, todos map[string]string, consts map[string]string) map[string][]string {
	found := map[string][]string{}
	for _, decl := range f.Decls {
		switch decl := decl.(type) {
		case *ast.FuncDecl:
			if decl.Body == nil {
				continue
			}
			site := rel + ":" + decl.Name.Name
			for _, c := range f.Comments {
				if c.Pos() >= decl.Pos() && c.End() <= decl.End() {
					if m := todoPR.FindStringSubmatch(c.Text()); m != nil {
						todos[site] = m[1]
					}
				}
			}
			for _, why := range reachesPerson(decl.Body, consts) {
				found[site] = append(found[site], fset.Position(why.pos).String()+": "+why.what)
			}
		case *ast.GenDecl:
			for _, spec := range decl.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				site := rel + ":" + vs.Names[0].Name
				for i, v := range vs.Values {
					// The event type's own definition.
					if i < len(vs.Names) && vs.Names[i].Name == "EvQuestionAsked" {
						continue
					}
					for _, why := range reachesPerson(v, consts) {
						found[site] = append(found[site], fset.Position(why.pos).String()+": "+why.what)
					}
				}
			}
		}
	}
	return found
}

var todoPR = regexp.MustCompile(`TODO\(conductor-first (PR\w+)\)`)

type personReach struct {
	pos  token.Pos
	what string
}

var setSQL = regexp.MustCompile(`(?i)\bSET\b`)
var writeSQL = regexp.MustCompile(`(?i)\b(INSERT|UPDATE|DELETE|MERGE)\b`)

// reachesPerson lists what under n reaches a person directly, or through
// ToPersonTx for a transitional reason.
func reachesPerson(n ast.Node, consts map[string]string) []personReach {
	var out []personReach
	var stack []ast.Node
	ast.Inspect(n, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		var parent ast.Node
		if len(stack) > 0 {
			parent = stack[len(stack)-1]
		}
		stack = append(stack, n)
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
			if openQuestionInsert.MatchString(s) && !strings.Contains(s, "'answered'") && s != ConductorQuestionInsertSQL {
				out = append(out, personReach{n.Pos(), "INSERT INTO questions"})
			}
			if awaitingInputSQL.MatchString(s) && setSQL.MatchString(s) {
				out = append(out, personReach{n.Pos(), "SET status = 'awaiting_input'"})
			}
			if s == "question.asked" {
				out = append(out, personReach{n.Pos(), `"question.asked"`})
			}
			if s == "awaiting_input" && !isRead(n, parent, consts) {
				out = append(out, personReach{n.Pos(), `"awaiting_input", not read`})
			}
		case *ast.Ident:
			// A selector's name is judged with the selector (below).
			if sel, ok := parent.(*ast.SelectorExpr); !ok || sel.Sel != n {
				out = append(out, namesPerson(n, n.Name, parent, consts)...)
			}
		case *ast.SelectorExpr:
			// delivery.EvQuestionAsked: judged as a whole, with its own parent.
			out = append(out, namesPerson(n, n.Sel.Name, parent, consts)...)
		}
		return true
	})
	return out
}

// namesPerson: e, named name, is a ToPersonTx call's transitional reason
// however it is passed, or the event type however it travels to an emitter.
func namesPerson(e ast.Expr, name string, parent ast.Node, consts map[string]string) []personReach {
	var out []personReach
	if slices.Contains(transitionalWhys, name) {
		out = append(out, personReach{e.Pos(), "ToPersonTx for " + name})
	}
	if name == "EvQuestionAsked" && !isRead(e, parent, consts) {
		out = append(out, personReach{e.Pos(), "EvQuestionAsked, not read"})
	}
	return out
}

// isRead: e (an "awaiting_input" literal or EvQuestionAsked), with its
// parent, only reads a status or an event type. The rule is narrow on
// purpose, and anything else is a write:
//   - an operand of == or !=;
//   - a case of a switch;
//   - SetTaskStatusTx's from (argument 5): the status moved away from;
//   - an argument, after the SQL, of Query or QueryRow whose SQL's text is
//     known (literals and package consts) and has no INSERT, UPDATE, DELETE
//     or MERGE: a predicate's parameter.
func isRead(e ast.Expr, parent ast.Node, consts map[string]string) bool {
	switch p := parent.(type) {
	case *ast.BinaryExpr:
		return p.Op == token.EQL || p.Op == token.NEQ
	case *ast.CaseClause:
		return slices.Contains(p.List, e)
	case *ast.CallExpr:
		at := slices.Index(p.Args, e)
		switch callName(p.Fun) {
		case "SetTaskStatusTx":
			return at == 5
		case "Query", "QueryRow":
			if at < 2 {
				return false
			}
			sql, ok := sqlText(p.Args[1], consts)
			return ok && !writeSQL.MatchString(sql)
		}
	}
	return false
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
