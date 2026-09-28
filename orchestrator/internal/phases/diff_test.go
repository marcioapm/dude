package phases

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
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

// The script, against a real checkout made as lux makes one (a branch
// checked out with -B): tracked changes against where it started, a
// commit since included; untracked files as new, binary ones too; ignored
// files left out; and the agent's index untouched. Run both ways: printed,
// for the live read, and saved into $LUX_ARTIFACTS, for the beforeStop hook.
func TestTheDiffScriptSeesTrackedAndUntrackedWork(t *testing.T) {
	dir := t.TempDir()
	origin, repo := filepath.Join(dir, "origin"), filepath.Join(dir, "repos", "target")
	git := func(in string, args ...string) string {
		out, err := exec.Command("git", append([]string{"-C", in, "-c", "user.name=t", "-c", "user.email=t@x"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return string(out)
	}
	write := func(name, content string) {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(repo, name)), 0o755)
		if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.MkdirAll(origin, 0o755)
	git(origin, "init", "-q", "-b", "main")
	_ = os.WriteFile(filepath.Join(origin, "README.md"), []byte("one\n"), 0o644)
	_ = os.WriteFile(filepath.Join(origin, ".gitignore"), []byte("build/\n"), 0o644)
	git(origin, "add", ".")
	git(origin, "commit", "-q", "-m", "base")
	base := git(origin, "rev-parse", "HEAD")[:40]
	git(dir, "clone", "-q", "--no-checkout", origin, repo)
	git(repo, "checkout", "-q", "-B", "main", "origin/main")

	// A commit since the base, an uncommitted change, a new text file and
	// a new binary one, and build output that is ignored.
	write("README.md", "two\n")
	git(repo, "commit", "-q", "-am", "agent's commit")
	write("README.md", "three\n")
	write("src/new file.go", "package x\n")
	write("logo.bin", "\x00\x01")
	write("build/out.js", "junk\n")

	run := func(dest string, env ...string) string {
		cmd := exec.Command("sh", "-c", diffScript, "dude-diff", dest, "target", repo, "main")
		cmd.Env = append(os.Environ(), env...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		return string(out)
	}
	printed := run("-")
	diff := parseRunDiff(printed)
	got := map[string]string{}
	for _, f := range diff.Files {
		got[f.Path] = f.Status
	}
	if diff.Base != base || len(got) != 3 || got["README.md"] != "M" || got["src/new file.go"] != "A" || got["logo.bin"] != "A" {
		t.Errorf("base %s (want %s), files %v\n%s", diff.Base, base, got, printed)
	}
	if status := git(repo, "status", "--porcelain"); status != " M README.md\n?? logo.bin\n?? src/\n" {
		t.Errorf("the index changed: %q", status)
	}

	// The hook's way: the same diff, in $LUX_ARTIFACTS, printing nothing.
	artifacts := filepath.Join(dir, "artifacts")
	if out := run("artifacts", "LUX_ARTIFACTS="+artifacts); out != "" {
		t.Errorf("the hook printed %q", out)
	}
	saved, err := os.ReadFile(filepath.Join(artifacts, finalDiffDir, "target.patch"))
	if err != nil || string(saved) != printed {
		t.Errorf("saved %q (%v), printed %q", saved, err, printed)
	}
	if diffChecksum(saved) != diff.Checksum {
		t.Error("the same diff has another checksum")
	}

	// Several repositories: a patch each, which in name order are the live
	// read, so an unchanged checkout has one checksum either way.
	both := func(dest string) string {
		cmd := exec.Command("sh", "-c", diffScript, "dude-diff", dest, "a", repo, "main", "b", repo, "main")
		cmd.Env = append(os.Environ(), "LUX_ARTIFACTS="+artifacts)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		return string(out)
	}
	printed = both("-")
	both("artifacts")
	a, _ := os.ReadFile(filepath.Join(artifacts, finalDiffDir, "a.patch"))
	b, _ := os.ReadFile(filepath.Join(artifacts, finalDiffDir, "b.patch"))
	if string(a)+string(b) != printed || !strings.HasPrefix(string(b), "# dude-diff b ") {
		t.Errorf("patches %q + %q, printed %q", a, b, printed)
	}
}

func TestSeveralRepositoriesAreOneDiffWithTheirNames(t *testing.T) {
	text := "# dude-diff api aaaa\n" + sampleDiff + "# dude-diff web bbbb\ndiff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+b\n"
	d := parseRunDiff(text)
	if d.Base != "aaaa" || len(d.Files) != 6 || d.Files[0].Path != "api/src/app.ts" || d.Files[5].Path != "web/x" {
		t.Errorf("got base %s, %d files, %s … %s", d.Base, len(d.Files), d.Files[0].Path, d.Files[len(d.Files)-1].Path)
	}
	if parseRunDiff("").Files == nil {
		t.Error("no changes is an empty list, not null")
	}
}

func TestEveryPhaseLeavesItsFinalDiffOnStop(t *testing.T) {
	spec := buildSpec(AgentConfig{}, specInput{Phase: "implement", Model: "m", Repos: []specRepo{{Name: "api", Ref: "main"}, {Name: "web", Ref: "abc123"}}})
	hook := spec.Workload.BeforeStop
	if hook == nil || hook.Timeout != FinalDiffTimeout {
		t.Fatalf("hook = %+v", hook)
	}
	args := hook.Command[4:]
	if hook.Command[0] != "sh" || hook.Command[4] != "artifacts" ||
		strings.Join(args[1:], " ") != "api /workspace/repos/api main web /workspace/repos/web abc123" {
		t.Errorf("command = %q", hook.Command)
	}
	if spec := buildSpec(AgentConfig{}, specInput{Phase: "implement", Model: "m"}); spec.Workload.BeforeStop != nil {
		t.Error("work on no repository has no diff to leave")
	}
}

func TestTheDiffIsReadSoonAfterAnEditOnceForABurstAndEverySoOften(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var edits atomic.Int32
	poke := make(chan struct{}, 1)
	go watchDiff(ctx, poke, 30*time.Millisecond, time.Hour, time.Hour, func(_ context.Context, periodic bool) bool {
		if !periodic {
			edits.Add(1)
		}
		return true
	})

	// A burst of edits is one read, a moment after the first.
	for range 5 {
		select {
		case poke <- struct{}{}:
		default:
		}
		time.Sleep(2 * time.Millisecond)
	}
	if edits.Load() != 0 {
		t.Fatal("read before the delay")
	}
	waitFor(t, func() bool { return edits.Load() == 1 })
	time.Sleep(60 * time.Millisecond)
	if n := edits.Load(); n != 1 {
		t.Fatalf("a burst was read %d times", n)
	}
	// A later edit is read again.
	poke <- struct{}{}
	waitFor(t, func() bool { return edits.Load() == 2 })
}

func TestReadsThatFindNothingNewSlowDownUntilTheAgentEditsAgain(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var ticks, edits atomic.Int32
	poke := make(chan struct{}, 1)
	go watchDiff(ctx, poke, time.Millisecond, 10*time.Millisecond, time.Hour, func(_ context.Context, periodic bool) bool {
		if !periodic {
			edits.Add(1)
			return true
		}
		ticks.Add(1)
		return false // unchanged, or skipped: the agent is idle
	})
	// Every 10ms, until four found nothing; then the slow pace (an hour).
	waitFor(t, func() bool { return ticks.Load() == diffBackoffAfter })
	time.Sleep(80 * time.Millisecond)
	if n := ticks.Load(); n != diffBackoffAfter {
		t.Fatalf("%d periodic reads, want them to slow after %d", n, diffBackoffAfter)
	}
	// An edit brings the pace back.
	poke <- struct{}{}
	waitFor(t, func() bool { return edits.Load() == 1 })
	waitFor(t, func() bool { return ticks.Load() > diffBackoffAfter })

	// And nothing after it is told to stop.
	cancel()
	time.Sleep(20 * time.Millisecond)
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

func TestOnlyTheHooksPatchesAreTheFinalDiff(t *testing.T) {
	for path, want := range map[string]string{
		FinalDiffPrefix + "api.patch":     "api",
		FinalDiffPrefix + ".api.tmp":      "",
		FinalDiffPrefix + "x/api.patch":   "",
		FinalDiffPrefix + ".patch":        "",
		lux.PublishedPrefix + "api.patch": "",
	} {
		if got, ok := finalDiffRepo(path); got != want || ok != (want != "") {
			t.Errorf("%s: %q %v", path, got, ok)
		}
	}
}
