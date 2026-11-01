package delivery

import (
	"fmt"
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
		{"a header that omits its count (1)", "@@ -1 +1 @@\n-a\n+b", [][2]int{{1, 1}}},
		{"two changed lines one apart are not merged",
			"@@ -1,3 +1,3 @@\n-a\n+A\n b\n-c\n+C",
			[][2]int{{1, 1}, {3, 3}}},
		{"a replacement, context, then a lone deletion",
			"@@ -1,5 +1,4 @@\n-a\n+A\n b\n c\n-d\n e",
			[][2]int{{1, 1}, {3, 4}}},
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
		{"a patch with context keeps the changed line only",
			[]forge.ComparedFile{file("a.go", "@@ -7,7 +7,7 @@\n a\n b\n c\n-d\n+D\n e\n f\n g", 1, 1)},
			[]HunkFile{{File: "a.go", New: [][2]int{{10, 10}}, Add: 1, Del: 1}}},
		{"a pure rename or an empty file, no patch and no lines, is known",
			[]forge.ComparedFile{{Filename: "new.go", PreviousFilename: "old.go"}, file("empty.txt", "", 0, 0)},
			[]HunkFile{{File: "new.go", New: [][2]int{}}, {File: "empty.txt", New: [][2]int{}}}},
		{"lines GitHub did not show are not known",
			[]forge.ComparedFile{file("big.go", "", 900, 2)},
			[]HunkFile{{File: "big.go", New: [][2]int{}, Add: 900, Del: 2, Unknown: true}}},
		{"Go, Python and TS/JS tests removed",
			[]forge.ComparedFile{
				file("a_test.go", "@@ -1,2 +0,0 @@\n-func TestGo(t *testing.T) {\n-}", 0, 2),
				file("tests/test_a.py", "@@ -1,2 +0,0 @@\n-def test_sync():\n-    async def test_async(x):", 0, 2),
				file("web/a.test.ts", "@@ -1,3 +0,0 @@\n-  it.only(\"runs once\", () => {\n-test('works', () => {\n-it(`templated`, () => {", 0, 3)},
			[]HunkFile{
				{File: "a_test.go", New: [][2]int{{1, 1}}, Del: 2, Test: true, RemovedTests: []string{"TestGo"}},
				{File: "tests/test_a.py", New: [][2]int{{1, 1}}, Del: 2, Test: true, RemovedTests: []string{"test_sync", "test_async"}},
				{File: "web/a.test.ts", New: [][2]int{{1, 1}}, Del: 3, Test: true, RemovedTests: []string{"runs once", "works", "templated"}}}},
		{"a test added again in another file is moved, not removed; one removed twice is named once",
			[]forge.ComparedFile{
				file("a_test.go", "@@ -1,3 +0,0 @@\n-func TestMoved(t *testing.T) {}\n-func TestGone(t *testing.T) {}\n-func TestGone(t *testing.T) {}", 0, 3),
				file("b_test.go", "@@ -0,0 +1,1 @@\n+func TestMoved(t *testing.T) {}", 1, 0)},
			[]HunkFile{
				{File: "a_test.go", New: [][2]int{{1, 1}}, Del: 3, Test: true, RemovedTests: []string{"TestGone"}},
				{File: "b_test.go", New: [][2]int{{1, 1}}, Add: 1, Test: true}}},
		{"a test added again in a production file is not removed",
			[]forge.ComparedFile{
				file("a_test.go", "@@ -1,1 +0,0 @@\n-func TestMoved(t *testing.T) {}", 0, 1),
				file("helpers.go", "@@ -0,0 +1,1 @@\n+func TestMoved(t *testing.T) {}", 1, 0)},
			[]HunkFile{
				{File: "a_test.go", New: [][2]int{{1, 1}}, Del: 1, Test: true},
				{File: "helpers.go", New: [][2]int{{1, 1}}, Add: 1}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := SummarizeFix(c.files, false); !reflect.DeepEqual(got, RepoHunks{Files: c.want}) {
				t.Errorf("SummarizeFix =\n %+v\nwant\n %+v", got, RepoHunks{Files: c.want})
			}
		})
	}
}

// TestFixHunksChange is what a round wake says of a fix's kept change.
func TestFixHunksChange(t *testing.T) {
	one := func(files ...HunkFile) FixHunks { return FixHunks{"target": {Files: files}} }
	prod := func(name string) HunkFile { return HunkFile{File: name, New: [][2]int{}, Add: 1} }
	for _, c := range []struct {
		name      string
		h         FixHunks
		published []string
		b         brief
		want      roundChange
	}{
		{"a directory named without a trailing slash names its files",
			one(prod("internal/delivery/x.go"), prod("other.go")), []string{"target"},
			brief{Notes: []string{"Keep it small; see internal/delivery."}},
			roundChange{Delta: &lineDelta{2, 0, 0, 0}, Outside: []string{"other.go"}}},
		{"a plain word names no directory, nor the repository",
			FixHunks{"api": {Files: []HunkFile{prod("x.go"), prod("app/y.go"), prod("cmd/z.go")}}}, []string{"api"},
			brief{Findings: []briefPlace{{"", "x.go"}}, Notes: []string{"In the api repo, fix the nil check; the app crashes."}},
			roundChange{Delta: &lineDelta{3, 0, 0, 0}, Outside: []string{"app/y.go", "cmd/z.go"}}},
		{"a quoted path with its :line after the quote",
			one(prod("docs/x.md"), prod("other.go")), []string{"target"},
			brief{Notes: []string{"Fix `docs/x.md`:1 only."}},
			roundChange{Delta: &lineDelta{2, 0, 0, 0}, Outside: []string{"other.go"}}},
		{"a partial segment names nothing",
			one(prod("internal/delivery/x.go"), prod("other.go")), []string{"target"},
			brief{Notes: []string{"see internal/del and other.go"}},
			roundChange{Delta: &lineDelta{2, 0, 0, 0}, Outside: []string{"internal/delivery/x.go"}}},
		{"two repositories: outside carries the repository",
			FixHunks{"api": {Files: []HunkFile{prod("x.go")}}, "web": {Files: []HunkFile{prod("y.go")}}}, []string{"api", "web"},
			brief{Findings: []briefPlace{{"api", "x.go"}}},
			roundChange{Delta: &lineDelta{2, 0, 0, 0}, Outside: []string{"web/y.go"}}},
		{"a finding names its repository's file, not another's of that name",
			FixHunks{"api": {Files: []HunkFile{prod("x.go")}}, "web": {Files: []HunkFile{prod("x.go")}}}, []string{"api", "web"},
			brief{Findings: []briefPlace{{"api", "./x.go"}}},
			roundChange{Delta: &lineDelta{2, 0, 0, 0}, Outside: []string{"web/x.go"}}},
		{"a note names repo/path",
			FixHunks{"api": {Files: []HunkFile{prod("x.go")}}, "web": {Files: []HunkFile{prod("x.go")}}}, []string{"api", "web"},
			brief{Notes: []string{"Change `web/x.go:3` only."}},
			roundChange{Delta: &lineDelta{2, 0, 0, 0}, Outside: []string{"api/x.go"}}},
		{"past the cap, the rest counted",
			one(HunkFile{File: "a_test.go", New: [][2]int{}, Test: true, RemovedTests: []string{"T1", "T2", "T3", "T4", "T5"}},
				prod("a.go"), prod("b.go"), prod("c.go"), prod("d.go")), []string{"target"},
			brief{Findings: []briefPlace{{"", "a.go"}}},
			roundChange{Delta: &lineDelta{4, 0, 0, 0}, Removed: []string{"T1", "T2", "T3"}, RemovedMore: 2,
				Outside: []string{"a_test.go", "b.go", "c.go"}, OutsideMore: 1}},
		{"a recognised test file is in the total and in the test share",
			one(HunkFile{File: "FACTORY.md", New: [][2]int{}, Add: 3, Del: 1}, HunkFile{File: "factory_test.go", New: [][2]int{}, Add: 2, Test: true}), []string{"target"},
			brief{},
			roundChange{Delta: &lineDelta{5, 1, 2, 0}}},
		{"no test file: the total, no test share",
			one(HunkFile{File: "FACTORY.md", New: [][2]int{}, Add: 3, Del: 1}), []string{"target"},
			brief{},
			roundChange{Delta: &lineDelta{3, 1, 0, 0}}},
		{"a test file not recognised is in the total",
			one(HunkFile{File: "FACTORY.md", New: [][2]int{}, Add: 3, Del: 1}, HunkFile{File: "spec/PlayerSpec.js", New: [][2]int{}, Add: 4, Del: 2}), []string{"target"},
			brief{},
			roundChange{Delta: &lineDelta{7, 3, 0, 0}}},
		{"a file not known leaves the delta and removals unknown",
			one(HunkFile{File: "a_test.go", New: [][2]int{}, Test: true, RemovedTests: []string{"T1"}}, HunkFile{File: "big.go", Unknown: true}), []string{"target"},
			brief{Findings: []briefPlace{{"", "a_test.go"}}},
			roundChange{Outside: []string{"big.go"}}},
		{"a published repository not kept leaves it all unknown",
			one(prod("a.go")), []string{"target", "other"}, brief{Findings: []briefPlace{{"", "x.go"}}}, roundChange{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := c.h.change(c.published, c.b); !reflect.DeepEqual(got, c.want) {
				t.Errorf("change =\n %s\nwant\n %s", show(got), show(c.want))
			}
		})
	}
}

// TestFixHunksTouches places a finding on the fix's lines, or not.
func TestFixHunksTouches(t *testing.T) {
	file := HunkFile{File: "x.go", New: [][2]int{{5, 6}}, Add: 2}
	two := FixHunks{"api": {Files: []HunkFile{file}}, "web": {Files: []HunkFile{file}}}
	for _, c := range []struct {
		name       string
		h          FixHunks
		published  []string
		repo, file string
		line       int
		want       bool
	}{
		{"on its lines", two, []string{"api", "web"}, "api", "x.go", 5, true},
		{"past them", two, []string{"api", "web"}, "api", "x.go", 7, false},
		{"no line", two, []string{"api", "web"}, "api", "x.go", 0, false},
		{"another file", two, []string{"api", "web"}, "api", "y.go", 5, false},
		{"./path", two, []string{"api", "web"}, "api", "./x.go", 6, true},
		{"no repository, one published", two, []string{"api"}, "", "x.go", 5, true},
		{"no repository, two published", two, []string{"api", "web"}, "", "x.go", 5, false},
		{"a repository it did not publish", two, []string{"api"}, "web", "x.go", 5, false},
		{"a published repository not kept", FixHunks{"api": {Files: []HunkFile{file}}}, []string{"api", "web"}, "web", "x.go", 5, false},
		{"a truncated repository", FixHunks{"api": {Truncated: true, Files: []HunkFile{file}}}, []string{"api"}, "api", "x.go", 5, false},
		{"a file not known", FixHunks{"api": {Files: []HunkFile{{File: "x.go", New: [][2]int{{5, 6}}, Unknown: true}}}}, []string{"api"}, "api", "x.go", 5, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := c.h.Touches(c.published, c.repo, c.file, c.line); got != c.want {
				t.Errorf("Touches(%v, %q, %q, %d) = %v, want %v", c.published, c.repo, c.file, c.line, got, c.want)
			}
		})
	}
}

func show(c roundChange) string {
	d := "nil"
	if c.Delta != nil {
		d = fmt.Sprint(*c.Delta)
	}
	return fmt.Sprintf("delta %s removed %q +%d outside %q +%d", d, c.Removed, c.RemovedMore, c.Outside, c.OutsideMore)
}

// IsTestPath knows the common test layouts, by whole path segment.
func TestIsTestPath(t *testing.T) {
	for p, want := range map[string]bool{
		"a/b_test.go": true, "x/test_a.py": true, "x/a_test.py": true, "app/tests.py": true,
		"web/a.test.ts": true, "web/a.spec.tsx": true, "web/a.test.mjs": true,
		"tests/a.go": true, "x/testdata/a.json": true, "web/__tests__/a.ts": true,
		"test/api.js": true, "spec/models/user_spec.rb": true, "lib/user_spec.rb": true,
		"src/test/java/a/FooTest.java": true, "src/test/kotlin/a/FooTests.kt": true,
		"src/main/java/x/ABTest.java": false, "src/a/FooTests.java": false, "src/a/FooTest.kt": false, "api/spec/openapi.yaml": false,
		"a.go": false, "contest.go": false, "latest.ts": false, "testing/helper.go": false,
		"src/contest/x.go": false, "latest/x.go": false, "spectrum/x.go": false, "a/Test.java": false,
		"a/Contest.java": false, "a/test.py": false, "a/spec.rb": false,
	} {
		if got := IsTestPath(p); got != want {
			t.Errorf("IsTestPath(%q) = %v, want %v", p, got, want)
		}
	}
}
