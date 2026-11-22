package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"testing"
)

// Conductors' waits on things outside dude are observed by a loop of their
// own, every second, apart from the phase sweep (whose Sweep no longer
// reads them): main's loops table holds {"wait-observations", time.Second,
// syncer.ObserveWaits}.
func TestWaitObservationsHaveTheirOwnLoop(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok || len(lit.Elts) != 3 {
			return true
		}
		if id, ok := lit.Type.(*ast.Ident); lit.Type != nil && (!ok || id.Name != "loop") {
			return true
		}
		name, ok := lit.Elts[0].(*ast.BasicLit)
		if !ok || name.Kind != token.STRING {
			return true
		}
		if s, _ := strconv.Unquote(name.Value); s != "wait-observations" {
			return true
		}
		every, ok := lit.Elts[1].(*ast.SelectorExpr)
		if !ok || every.Sel.Name != "Second" {
			t.Errorf("the wait-observations loop's interval is not time.Second")
		}
		sweep, ok := lit.Elts[2].(*ast.SelectorExpr)
		if !ok || sweep.Sel.Name != "ObserveWaits" {
			t.Errorf("the wait-observations loop does not run ObserveWaits")
		} else if x, ok := sweep.X.(*ast.Ident); !ok || x.Name != "syncer" {
			t.Errorf("the wait-observations loop runs ObserveWaits on something other than the syncer")
		}
		found++
		return true
	})
	if found != 1 {
		t.Fatalf("%d wait-observations loops in main.go, want 1", found)
	}
}
