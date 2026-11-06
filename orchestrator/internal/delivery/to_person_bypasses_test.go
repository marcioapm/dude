package delivery

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestTheScanSeesEveryKnownBypass: Test 1's detector
// (scanPersonReach) flags each known way round ToPersonTx
// (testdata/to_person_bypasses/bypass*.go), and none of the reads in
// reads_only.go.
func TestTheScanSeesEveryKnownBypass(t *testing.T) {
	want := map[string][]string{
		// 1: the SQL in file-level consts and vars.
		"bypass1_const_sql.go": {"zzAskSQL", "zzParkSQL"},
		// 2: the status through a local and through a package var.
		"bypass2_status_var.go": {"zzParkLocal", "zzParked"},
		// 3: a helper wrapping SetTaskStatusTx: its caller.
		"bypass3_helper.go": {"zzPark"},
		// 4: question.asked's type through a local, to emitTx and to ledger.Append.
		"bypass4_event_var.go": {"zzEmit", "zzAppend"},
		// 5: awaiting_input as a parameterised SQL's argument.
		"bypass5_param_sql.go": {"zzParkParam"},
		// 6: an existing question addressed to a person (a hand-over).
		"bypass6_handover_sql.go": {"zzHandOver"},
		"reads_only.go":           nil,
	}
	files, err := filepath.Glob("testdata/to_person_bypasses/*.go")
	if err != nil || len(files) != len(want) {
		t.Fatalf("fixtures %v (%v): want the %d in this test", files, err, len(want))
	}
	fset := token.NewFileSet()
	for _, path := range files {
		f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			t.Fatal(err)
		}
		base := filepath.Base(path)
		consts := map[string]string{}
		stringConsts(f, consts)
		found := scanPersonReach(fset, f, base, map[string]string{}, consts)
		var sites []string
		for site := range found {
			sites = append(sites, strings.TrimPrefix(site, base+":"))
		}
		slices.Sort(sites)
		exp := slices.Clone(want[base])
		slices.Sort(exp)
		if !slices.Equal(sites, exp) {
			t.Errorf("%s: the scan flags %v, want %v\n%v", base, sites, exp, found)
		}
	}
}
