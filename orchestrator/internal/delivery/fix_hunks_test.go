package delivery

import (
	"reflect"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

// newRanges keeps the lines a patch changed — its + lines, and the lines
// either side of a run of - lines — not GitHub's context around them.
func TestNewRanges(t *testing.T) {
	for _, c := range []struct {
		name  string
		patch string
		want  [][2]int
	}{
		{"an addition", "@@ -1,1 +1,3 @@\n-x\n+one\n+two\n+three", [][2]int{{1, 3}}},
		{"a pure deletion", "@@ -2,3 +1,0 @@\n-a\n-b\n-c", [][2]int{{1, 2}}},
		{"a deletion at the top", "@@ -1,1 +0,0 @@\n-a", [][2]int{{1, 1}}},
		{"one line in context", "@@ -7,7 +7,7 @@\n a\n b\n c\n-d\n+D\n e\n f\n g", [][2]int{{10, 10}}},
		{"two changes in one hunk, context between",
			"@@ -7,13 +7,13 @@\n a\n b\n c\n-d\n+D\n e\n f\n g\n h\n i\n j\n-k\n+K\n l\n m\n n",
			[][2]int{{10, 10}, {17, 17}}},
		{"a deletion run mid-hunk",
			"@@ -7,8 +7,6 @@\n a\n b\n c\n-d\n-e\n f\n g\n h",
			[][2]int{{9, 10}}},
		{"the newline marker",
			"@@ -1,3 +1,3 @@\n a\n b\n-c\n\\ No newline at end of file\n+c\n\\ No newline at end of file",
			[][2]int{{3, 3}}},
		{"a deletion at the end", "@@ -1,3 +1,2 @@\n a\n b\n-c", [][2]int{{2, 3}}},
		{"two hunks",
			"@@ -1,4 +1,4 @@\n a\n-b\n+B\n c\n d\n@@ -20,3 +20,4 @@\n t\n+u\n v\n w",
			[][2]int{{2, 2}, {21, 21}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := newRanges(c.patch); !reflect.DeepEqual(got, c.want) {
				t.Errorf("newRanges = %v, want %v", got, c.want)
			}
		})
	}
}

// SummarizeFix keeps each file's changed lines, counts and the test
// declarations it removed and did not add again.
func TestSummarizeFix(t *testing.T) {
	file := func(name, patch string, add, del int) forge.ComparedFile {
		return forge.ComparedFile{Filename: name, Patch: patch, Additions: add, Deletions: del}
	}
	for _, c := range []struct {
		name  string
		files []forge.ComparedFile
		want  []HunkFile
	}{
		{"a test-like declaration in a production file is not a removed test",
			[]forge.ComparedFile{file("app/db.py", "@@ -1,2 +0,0 @@\n-def test_connection(db):\n-    pass", 0, 2)},
			[]HunkFile{{File: "app/db.py", New: [][2]int{{1, 1}}, Del: 2}}},
		{"a test file's removed test is",
			[]forge.ComparedFile{file("db_test.go", "@@ -1,2 +0,0 @@\n-func TestConnection(t *testing.T) {\n-}", 0, 2)},
			[]HunkFile{{File: "db_test.go", New: [][2]int{{1, 1}}, Del: 2, Test: true, RemovedTests: []string{"TestConnection"}}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := SummarizeFix(c.files, false); !reflect.DeepEqual(got, RepoHunks{Files: c.want}) {
				t.Errorf("SummarizeFix =\n %+v\nwant\n %+v", got, RepoHunks{Files: c.want})
			}
		})
	}
}
