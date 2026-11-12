package delivery

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// However long the shared paths, the line fits a wake's and ends with what
// to do: a path that does not fit is counted, never cut.
func TestEpicMergeLineFitsAndKeepsItsHint(t *testing.T) {
	const hint = ": bring `main` in (sync_base, or start_phase fix while a pull request is open) before you go on."
	for _, n := range []int{1, 2, 5} {
		for size := 10; size <= 400; size += 15 {
			files := make([]string, n)
			for i := range files {
				files[i] = strings.Repeat("d", size) + "/" + string(rune('a'+i)) + ".go"
			}
			line := epicMergeLine(goldenEpicMerge, epicOverlap{Known: true, SameBase: true, Files: files})
			if utf8.RuneCountInString(oneLine(line)) > wakeLineChars || !strings.HasSuffix(line, hint) {
				t.Fatalf("%d paths of %d: %d chars, %q", n, size, utf8.RuneCountInString(line), line)
			}
		}
	}
}

// The overlap matches a path, or a rename's previous one, on either side;
// a side that may miss a rename's previous path, with nothing shared, is
// unknown, never "none".
func TestEpicMergeOverlapRenames(t *testing.T) {
	peer := func(partial bool, files ...string) epicPeer {
		return epicPeer{TaskID: "wi_p", Writes: true, Known: true, Partial: partial, SameBase: true, Files: files}
	}
	for _, c := range []struct {
		name          string
		p             epicPeer
		merged        []string
		mergedPartial bool
		want          epicOverlap
	}{
		{"the merge renamed what the peer edits", peer(false, "billing/old.go"), []string{"billing/new.go", "billing/old.go"}, false,
			epicOverlap{Known: true, SameBase: true, Files: []string{"billing/old.go"}}},
		{"the peer renamed what the merge edits", peer(false, "billing/new.go", "billing/old.go"), []string{"billing/old.go"}, false,
			epicOverlap{Known: true, SameBase: true, Files: []string{"billing/old.go"}}},
		{"nothing shared", peer(false, "a.go"), []string{"b.go"}, false, epicOverlap{Known: true, SameBase: true}},
		{"the merge's rename has no previous name", peer(false, "billing/old.go"), []string{"billing/new.go"}, true, epicOverlap{}},
		{"the peer's rename has no previous name", peer(true, "billing/new.go"), []string{"billing/old.go"}, false, epicOverlap{}},
		{"shared all the same", peer(true, "a.go"), []string{"a.go"}, true, epicOverlap{Known: true, SameBase: true, Files: []string{"a.go"}}},
		{"a read-only peer", epicPeer{TaskID: "wi_p", Known: true, Files: []string{"a.go"}}, []string{"a.go"}, false, epicOverlap{Known: true}},
	} {
		got := overlapOf(c.p, c.merged, true, c.mergedPartial)
		if got.Known != c.want.Known || got.SameBase != c.want.SameBase || strings.Join(got.Files, ",") != strings.Join(c.want.Files, ",") {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
}
