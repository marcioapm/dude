package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// The judgement tier routes chat and nothing else: in the composition root
// the judge service is built once and handed to chat.Service alone, never
// to the checks gate (phases.Syncer) or anything else, under any field
// name. Every other mention of it, or of the judge package, fails this.
func TestTheJudgeServiceIsHandedOnlyToChat(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var built, toChat, judgePkg int
	var other []string
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		judgeName := ""
		for _, imp := range f.Imports {
			if strings.HasSuffix(strings.Trim(imp.Path.Value, `"`), "/internal/judge") {
				judgeName = "judge"
				if imp.Name != nil {
					judgeName = imp.Name.Name
				}
			}
		}
		if judgeName == "" {
			continue
		}
		// The judge package's one use: the service's literal.
		ast.Inspect(f, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok {
				if id, ok := sel.X.(*ast.Ident); ok && id.Name == judgeName {
					judgePkg++
					if sel.Sel.Name != "Service" {
						other = append(other, fset.Position(sel.Pos()).String()+": judge."+sel.Sel.Name)
					}
				}
			}
			return true
		})
		// The service's variable: one assignment, one use as chat's Judge.
		var service string
		ast.Inspect(f, func(n ast.Node) bool {
			as, ok := n.(*ast.AssignStmt)
			if !ok || len(as.Lhs) != 1 || len(as.Rhs) != 1 {
				return true
			}
			u, ok := as.Rhs[0].(*ast.UnaryExpr)
			if !ok {
				return true
			}
			if lit, ok := u.X.(*ast.CompositeLit); ok {
				if sel, ok := lit.Type.(*ast.SelectorExpr); ok && sel.Sel.Name == "Service" {
					if id, ok := sel.X.(*ast.Ident); ok && id.Name == judgeName {
						service = as.Lhs[0].(*ast.Ident).Name
						built++
					}
				}
			}
			return true
		})
		if service == "" {
			continue
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			sel, ok := lit.Type.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "chat" && sel.Sel.Name == "Service" {
				for _, el := range lit.Elts {
					if kv, ok := el.(*ast.KeyValueExpr); ok {
						k, _ := kv.Key.(*ast.Ident)
						v, _ := kv.Value.(*ast.Ident)
						if k != nil && v != nil && k.Name == "Judge" && v.Name == service {
							toChat++
						}
					}
				}
			}
			return true
		})
		uses := 0
		ast.Inspect(f, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && id.Name == service {
				uses++
			}
			return true
		})
		// Its definition and chat's Judge field, nothing else.
		if uses != 2 {
			other = append(other, name+": the judge service ("+service+") is used beyond chat.Service's Judge")
		}
	}
	if built != 1 || toChat != 1 || judgePkg != 1 {
		t.Errorf("judge services built %d, handed to chat.Service %d, judge package mentions %d; want 1 each", built, toChat, judgePkg)
	}
	for _, o := range other {
		t.Error(o)
	}
}
