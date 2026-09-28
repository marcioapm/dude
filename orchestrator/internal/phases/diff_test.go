package phases

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

const sampleDiff = `diff --git a/src/app.ts b/src/app.ts
index 1111111..2222222 100644
--- a/src/app.ts
+++ b/src/app.ts
@@ -1,4 +1,5 @@
 import x from "x";
-const a = 1;
+const a = 2;
+const b = 3;

 export { a };
@@ -10,2 +11,2 @@ function f() {
-  return 1;
+  return 2;
 }
diff --git a/old.md b/old.md
deleted file mode 100644
index 3333333..0000000
--- a/old.md
+++ /dev/null
@@ -1,2 +0,0 @@
-gone
--- this line starts with dashes
diff --git a/a b/c.txt b/a b/c.txt
similarity 90%
rename from a b/c.txt
rename to a b/d.txt
diff --git a/img.png b/img.png
index 4444444..5555555 100644
Binary files a/img.png and b/img.png differ
diff --git a/dev/null b/NEW.md
new file mode 100644
index 0000000..6666666
--- /dev/null
+++ b/NEW.md
@@ -0,0 +1 @@
+hello
\ No newline at end of file
`

func TestADiffIsReadIntoFilesHunksAndNumberedLines(t *testing.T) {
	files := ParseDiff(sampleDiff)
	if len(files) != 5 {
		t.Fatalf("got %d files: %+v", len(files), files)
	}
	app := files[0]
	if app.Path != "src/app.ts" || app.Status != "M" || app.Additions != 3 || app.Deletions != 2 || len(app.Hunks) != 2 {
		t.Errorf("app.ts = %+v", app)
	}
	first := app.Hunks[0]
	if first.Header != "@@ -1,4 +1,5 @@" || len(first.Lines) != 6 {
		t.Fatalf("hunk = %+v", first)
	}
	// Context has both numbers, a removal only the old, an addition only
	// the new; a blank context line keeps its place.
	want := []struct {
		kind     string
		old, new int
	}{{" ", 1, 1}, {"-", 2, 0}, {"+", 0, 2}, {"+", 0, 3}, {" ", 3, 4}, {" ", 4, 5}}
	for i, w := range want {
		l := first.Lines[i]
		if l.Kind != w.kind || deref(l.Old) != w.old || deref(l.New) != w.new {
			t.Errorf("line %d = %s %v %v %q, want %+v", i, l.Kind, deref(l.Old), deref(l.New), l.Text, w)
		}
	}
	if first.Lines[2].Text != "const a = 2;" {
		t.Errorf("text = %q", first.Lines[2].Text)
	}
	if app.Hunks[1].Lines[0].Old == nil || *app.Hunks[1].Lines[0].Old != 10 {
		t.Errorf("second hunk starts at %+v", app.Hunks[1].Lines[0])
	}

	// A removed line that looks like a header is still a removed line.
	old := files[1]
	if old.Path != "old.md" || old.Status != "D" || old.Deletions != 2 || old.Hunks[0].Lines[1].Text != "-- this line starts with dashes" {
		t.Errorf("old.md = %+v", old)
	}
	if r := files[2]; r.Path != "a b/d.txt" || r.Status != "R" || len(r.Hunks) != 0 || r.Hunks == nil {
		t.Errorf("rename = %+v", r)
	}
	if b := files[3]; b.Path != "img.png" || !b.Binary || b.Status != "M" {
		t.Errorf("binary = %+v", b)
	}
	// An untracked file, diffed against /dev/null, is new.
	if n := files[4]; n.Path != "NEW.md" || n.Status != "A" || n.Additions != 1 || len(n.Hunks[0].Lines) != 1 {
		t.Errorf("new = %+v", n)
	}
}

func TestADiffKeepsTheWireShapeTheAPIPromises(t *testing.T) {
	raw, _ := json.Marshal(ParseDiff(sampleDiff)[0])
	var got map[string]any
	_ = json.Unmarshal(raw, &got)
	line := got["hunks"].([]any)[0].(map[string]any)["lines"].([]any)[1].(map[string]any)
	// A missing number is null, not absent: the contract's `old: number | null`.
	if _, ok := line["new"]; !ok || line["new"] != nil || line["kind"] != "-" {
		t.Errorf("line = %v", line)
	}
	for _, k := range []string{"path", "status", "additions", "deletions", "hunks"} {
		if _, ok := got[k]; !ok {
			t.Errorf("missing %s in %v", k, got)
		}
	}
}

func TestAHugeFileIsCutButCountedWhole(t *testing.T) {
	text := "diff --git a/big b/big\n--- a/big\n+++ b/big\n@@ -0,0 +1,3000 @@\n"
	for range 3000 {
		text += "+x\n"
	}
	f := ParseDiff(text)[0]
	if f.Additions != 3000 || !f.Truncated || len(f.Hunks[0].Lines) != maxFileLines {
		t.Errorf("additions %d truncated %v lines %d", f.Additions, f.Truncated, len(f.Hunks[0].Lines))
	}
}

func TestQuotedPathsAreUnquoted(t *testing.T) {
	text := "diff --git \"a/tab\\there\" \"b/tab\\there\"\nnew file mode 100644\n--- /dev/null\n+++ \"b/tab\\there\"\n@@ -0,0 +1 @@\n+x\n"
	if f := ParseDiff(text)[0]; f.Path != "tab\there" || f.Status != "A" {
		t.Errorf("got %+v", f)
	}
}

// The script the orchestrator runs through exec, against a real checkout:
// tracked changes against the base, untracked files as new, ignored files
// left out, and the agent's index untouched.
func TestTheDiffScriptSeesTrackedAndUntrackedWork(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) string {
		out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@x"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return string(out)
	}
	git("init", "-q", "-b", "main")
	write := func(name, content string) {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755)
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("README.md", "one\n")
	write(".gitignore", "build/\n")
	git("add", ".")
	git("commit", "-q", "-m", "base")
	base := git("rev-parse", "HEAD")[:40]
	// A commit since the base, an uncommitted change, a new file, and build
	// output that is ignored.
	write("README.md", "two\n")
	git("commit", "-q", "-am", "agent's commit")
	write("README.md", "three\n")
	write("src/new file.go", "package x\n")
	write("build/out.js", "junk\n")

	cmd := diffCommand(dir, base)
	out, err := exec.Command(cmd[0], cmd[1:]...).CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	files := ParseDiff(string(out))
	got := map[string]string{}
	for _, f := range files {
		got[f.Path] = f.Status
	}
	if len(got) != 2 || got["README.md"] != "M" || got["src/new file.go"] != "A" {
		t.Errorf("files = %v\n%s", got, out)
	}
	if status := git("status", "--porcelain"); status != " M README.md\n?? src/\n" {
		t.Errorf("the index changed: %q", status)
	}
}

func TestTheDiffIsReadSoonAfterAnEditOnceForABurstAndEverySoOften(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var reads atomic.Int32
	poke := make(chan struct{}, 1)
	go watchDiff(ctx, poke, 30*time.Millisecond, time.Hour, func(context.Context) { reads.Add(1) })

	// A burst of edits is one read, a moment after the first.
	for range 5 {
		select {
		case poke <- struct{}{}:
		default:
		}
		time.Sleep(2 * time.Millisecond)
	}
	if reads.Load() != 0 {
		t.Fatal("read before the delay")
	}
	waitFor(t, func() bool { return reads.Load() == 1 })
	time.Sleep(60 * time.Millisecond)
	if n := reads.Load(); n != 1 {
		t.Fatalf("a burst was read %d times", n)
	}
	// A later edit is read again.
	poke <- struct{}{}
	waitFor(t, func() bool { return reads.Load() == 2 })

	// Without edits, the slow tick still reads.
	var ticks atomic.Int32
	go watchDiff(ctx, make(chan struct{}), time.Hour, 20*time.Millisecond, func(context.Context) { ticks.Add(1) })
	waitFor(t, func() bool { return ticks.Load() >= 2 })

	// And nothing after it is told to stop.
	cancel()
	time.Sleep(10 * time.Millisecond)
	n := ticks.Load()
	time.Sleep(60 * time.Millisecond)
	if ticks.Load() != n {
		t.Error("read after it was stopped")
	}
}

func TestEditToolsAreKnownByName(t *testing.T) {
	for _, name := range []string{"edit", "Write", "patch", "multiedit"} {
		if !isEdit(name) {
			t.Errorf("%s is an edit", name)
		}
	}
	for _, name := range []string{"read", "bash", "todowrite", "execute"} {
		if isEdit(name) {
			t.Errorf("%s is not an edit", name)
		}
	}
}

func deref(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
