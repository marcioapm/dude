package phases

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// The checks gate cannot reach a judgement tier: no file of the phases
// package (tests aside) imports the judge package, and the Syncer has no
// field through which one could be handed to it. The judgement tier routes
// chat (internal/chat); checks are the conductor's (askConductor) or, with
// no conductor, a person's (noConductor).
func TestTheChecksGateCannotReachAJudge(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	read := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, name, src, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		read++
		for _, imp := range f.Imports {
			if path, _ := strconv.Unquote(imp.Path.Value); strings.HasSuffix(path, "/internal/judge") {
				t.Errorf("%s imports %s", name, path)
			}
		}
	}
	if read < 10 {
		t.Fatalf("read %d files of the package, not the package", read)
	}
	typ := reflect.TypeOf(Syncer{})
	for i := range typ.NumField() {
		f := typ.Field(i)
		if strings.Contains(strings.ToLower(f.Name), "judge") || strings.HasSuffix(f.Type.PkgPath(), "/internal/judge") ||
			f.Type.Kind() == reflect.Pointer && strings.HasSuffix(f.Type.Elem().PkgPath(), "/internal/judge") {
			t.Errorf("Syncer.%s (%s) could hand the checks gate a judge", f.Name, f.Type)
		}
	}
}
