package branchdiff

import (
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

// The four relations, decided by ancestry and the merge base alone.
func TestClassify(t *testing.T) {
	for _, c := range []struct {
		name        string
		hasPrevious bool
		prevMB, mb  string
		descends    bool
		want        string
	}{
		{"the first snapshot", false, "", "b1", false, Initial},
		{"new commits on the branch", true, "b1", "b1", true, FastForward},
		{"the target merged in: the base moved", true, "b1", "b2", true, MergedTarget},
		{"a rebase: the head does not descend and the base moved", true, "b1", "b2", false, Rewritten},
		{"a force push over the same base", true, "b1", "b1", false, Rewritten},
	} {
		if got := Classify(c.hasPrevious, c.prevMB, c.mb, c.descends); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}

const patch = "@@ -1,3 +1,4 @@\n line one\n-line two\n+line 2\n+line 2b\n line three"

// GitHub's per-file patches become a Run diff's files, with the lines'
// numbers on each side; what GitHub did not show is said, never shown as
// no change.
func TestFilesSayWhatTheyCouldNotShow(t *testing.T) {
	files, st := Files([]forge.ComparedFile{
		{Filename: "src/a.ts", Status: "modified", Additions: 2, Deletions: 1, SHA: "aa", Patch: patch},
		{Filename: "huge.lock", Status: "modified", Additions: 9000, Deletions: 12, SHA: "bb"},
		{Filename: "logo.png", Status: "added", SHA: "cc"},
		{Filename: "empty.txt", Status: "added", SHA: emptyBlob},
		{Filename: "moved.ts", Status: "renamed", PreviousFilename: "old.ts", SHA: "dd"},
		{Filename: "gone.ts", Status: "removed", Deletions: 1, SHA: "ee", Patch: "@@ -1 +0,0 @@\n-x"},
	})
	if len(files) != 6 {
		t.Fatalf("%d files, want every one listed", len(files))
	}
	a := files[0]
	if a.Path != "src/a.ts" || a.Status != "M" || a.Truncated || a.Binary || len(a.Hunks) != 1 || len(a.Hunks[0].Lines) != 5 {
		t.Fatalf("the patched file: %+v", a)
	}
	if l := a.Hunks[0].Lines[2]; l.Kind != "+" || l.Old != nil || *l.New != 2 || l.Text != "line 2" {
		t.Errorf("an added line: %+v", l)
	}
	if l := a.Hunks[0].Lines[4]; l.Kind != " " || *l.Old != 3 || *l.New != 4 {
		t.Errorf("a context line after it: %+v", l)
	}
	if f := files[1]; !f.Truncated || f.Binary || f.Additions != 9000 || len(f.Hunks) != 0 {
		t.Errorf("a file too large to show: %+v", f)
	}
	if f := files[2]; !f.Binary || f.Truncated || f.Status != "A" {
		t.Errorf("a binary file: %+v", f)
	}
	if f := files[3]; f.Binary || f.Truncated {
		t.Errorf("an empty file is neither binary nor truncated: %+v", f)
	}
	if f := files[4]; f.Status != "R" || f.Binary || f.Truncated {
		t.Errorf("a rename with no lines: %+v", f)
	}
	if f := files[5]; f.Status != "D" || f.Truncated || len(f.Hunks) != 1 {
		t.Errorf("a removed file: %+v", f)
	}
	if st != (Stats{Files: 6, Additions: 9002, Deletions: 14, TruncatedFiles: 1, BinaryFiles: 1}) {
		t.Errorf("stats %+v", st)
	}
}

// A patch whose lines do not account for GitHub's counts was cut: truncated.
func TestAPatchShorterThanItsCountsIsTruncated(t *testing.T) {
	files, st := Files([]forge.ComparedFile{{Filename: "a.go", Status: "modified", Additions: 40, Deletions: 1, SHA: "aa", Patch: patch}})
	if !files[0].Truncated || st.TruncatedFiles != 1 {
		t.Errorf("%+v %+v", files[0], st)
	}
}

// At GitHub's file cap the list may be cut: the snapshot says so.
func TestTheFileCapIsSaid(t *testing.T) {
	many := make([]forge.ComparedFile, forge.CompareFileCap)
	for i := range many {
		many[i] = forge.ComparedFile{Filename: "f" + strings.Repeat("x", i%7) + string(rune('a'+i%26)), Status: "added", SHA: emptyBlob}
	}
	_, st := Files(many)
	if !st.FileCapReached {
		t.Error("300 files listed, the cap not said")
	}
	if _, st := Files(many[:10]); st.FileCapReached {
		t.Error("10 files, the cap said")
	}
	if r := partialReason(300); !strings.Contains(r, "300") {
		t.Errorf("reason %q", r)
	}
}

// The checksum names the diff: another head, base or file is another one.
func TestChecksum(t *testing.T) {
	files, _ := Files([]forge.ComparedFile{{Filename: "a", Status: "modified", Additions: 2, Deletions: 1, Patch: patch}})
	a := Checksum("b", "h", files)
	if a != Checksum("b", "h", files) || a == Checksum("b2", "h", files) || a == Checksum("b", "h2", files) || a == Checksum("b", "h", nil) {
		t.Error("the checksum does not follow the diff")
	}
}
