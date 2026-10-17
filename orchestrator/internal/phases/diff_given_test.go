package phases

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// givenRepo is a GitHub stand-in (origin) with main at S0 and a task
// branch one commit (P) past it, and the Run's checkout of branch, made as
// lux makes one (checkout -B, so the reflog records where it started).
type givenRepo struct {
	t                 *testing.T
	origin, dir       string
	branch, startedAt string
}

func newGivenRepo(t *testing.T, branch string) *givenRepo {
	t.Helper()
	root := t.TempDir()
	g := &givenRepo{t: t, origin: filepath.Join(root, "origin"), dir: filepath.Join(root, "repos", "target"), branch: branch}
	_ = os.MkdirAll(g.origin, 0o755)
	g.in(g.origin, "init", "-q", "-b", "main")
	g.commit(g.origin, "README.md", "one\n", "S0")
	g.in(g.origin, "checkout", "-q", "-b", "task")
	g.commit(g.origin, "task.txt", "earlier phase\n", "P")
	g.in(g.origin, "checkout", "-q", "main")
	g.in(root, "clone", "-q", "--no-checkout", g.origin, g.dir)
	g.git("checkout", "-q", "-B", branch, "origin/"+branch)
	g.startedAt = g.git("rev-parse", "HEAD")
	return g
}

func (g *givenRepo) in(dir string, args ...string) string {
	g.t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@x"}, args...)...).CombinedOutput()
	if err != nil {
		g.t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (g *givenRepo) git(args ...string) string { g.t.Helper(); return g.in(g.dir, args...) }

func (g *givenRepo) write(dir, file, content string) {
	g.t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		g.t.Fatal(err)
	}
}

// commit writes file in dir and commits it there, returning the commit.
func (g *givenRepo) commit(dir, file, content, message string) string {
	g.t.Helper()
	g.write(dir, file, content)
	g.in(dir, "add", file)
	g.in(dir, "commit", "-q", "-m", message)
	return g.in(dir, "rev-parse", "HEAD")
}

// someoneCommits commits on origin's branch, as another Run or a merged
// pull request would, and lux brings it into the checkout as its syncs do
// (refs/remotes/lux/<branch>); the commit.
func (g *givenRepo) someoneCommits(branch, file, content string) string {
	g.t.Helper()
	g.in(g.origin, "checkout", "-q", branch)
	sha := g.commit(g.origin, file, content, "someone's "+file)
	g.in(g.origin, "checkout", "-q", "main")
	g.git("fetch", "-q", "origin")
	g.git("update-ref", "refs/remotes/lux/"+branch, sha)
	return sha
}

// diff runs diffScript as a live read does, with ref (the start, then any
// given commits), and returns what it printed.
func (g *givenRepo) diff(ref string, env ...string) string {
	g.t.Helper()
	cmd := exec.Command("sh", "-c", diffScript, "dude-diff", "-", "target", g.dir, ref)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		g.t.Fatalf("%v: %s", err, out)
	}
	return string(out)
}

func diffPaths(text string) []string {
	var paths []string
	for _, f := range parseRunDiff(text).Files {
		paths = append(paths, f.Path)
	}
	slices.Sort(paths)
	return paths
}

// A Run on the task branch commits A, then merges the base branch's M (a
// file it never touched): given M, its diff is A alone, against M — and
// the hook's form, the ref lux fetched M into, prints the same bytes.
// Without M it is today's diff, M's file included.
func TestABaseBranchCommitTheRunMergedIsNotItsChange(t *testing.T) {
	g := newGivenRepo(t, "task")
	g.commit(g.dir, "a.txt", "the run's\n", "A")
	m := g.someoneCommits("main", "m.txt", "someone else's\n")
	g.git("merge", "-q", "--no-edit", "lux/main")

	given := g.diff("task " + m)
	if got := diffPaths(given); !slices.Equal(got, []string{"a.txt"}) {
		t.Errorf("given M: files %v, want [a.txt]\n%s", got, given)
	}
	if base := parseRunDiff(given).Base; base != m {
		t.Errorf("given M: base %s, want M %s", base, m)
	}
	if hook := g.diff("task refs/remotes/lux/main"); hook != given {
		t.Errorf("the hook's form printed\n%s\nthe live read\n%s", hook, given)
	}
	today := g.diff("task")
	if got := diffPaths(today); !slices.Equal(got, []string{"a.txt", "m.txt"}) || parseRunDiff(today).Base != g.startedAt {
		t.Errorf("no given: files %v, base %s\n%s", got, parseRunDiff(today).Base, today)
	}
}

// A conductor's checkout, on the task branch by name, is fast-forwarded
// from S to someone's T, then commits C and leaves edits uncommitted:
// given T, its diff is C and the edits. Then it merges the base branch's
// M too: given T and M, still only its own.
func TestAConductorsDiffLeavesOutTheTaskBranchItWasFastForwardedTo(t *testing.T) {
	g := newGivenRepo(t, "task")
	tip := g.someoneCommits("task", "fix.txt", "a fix run's\n")
	g.git("merge", "-q", "--ff-only", "lux/task")
	g.commit(g.dir, "c.txt", "the conductor's\n", "C")
	g.write(g.dir, "task.txt", "edited, not committed\n")
	g.write(g.dir, "new.txt", "untracked\n")

	out := g.diff("task " + tip)
	if got := diffPaths(out); !slices.Equal(got, []string{"c.txt", "new.txt", "task.txt"}) || parseRunDiff(out).Base != tip {
		t.Errorf("given T: files %v, base %s\n%s", got, parseRunDiff(out).Base, out)
	}
	if got := diffPaths(g.diff("task")); !slices.Contains(got, "fix.txt") {
		t.Errorf("no given: files %v, want fix.txt among them", got)
	}

	m := g.someoneCommits("main", "m.txt", "merged pull request\n")
	g.git("add", "-A")
	g.git("commit", "-q", "-m", "more")
	g.git("merge", "-q", "--no-edit", "lux/main")
	out = g.diff("task " + tip + " " + m)
	if got := diffPaths(out); !slices.Equal(got, []string{"c.txt", "new.txt", "task.txt"}) || parseRunDiff(out).Base != m {
		t.Errorf("given T and M: files %v, base %s\n%s", got, parseRunDiff(out).Base, out)
	}
	if hook := g.diff("task refs/remotes/lux/task refs/remotes/lux/main"); hook != out {
		t.Errorf("the hook's form printed\n%s\nthe live read\n%s", hook, out)
	}
}

func TestTheDiffIgnoresCheckoutReplacementObjects(t *testing.T) {
	g := newGivenRepo(t, "task")
	s := g.startedAt
	m := g.someoneCommits("main", "m.txt", "merged base\n")
	g.git("merge", "-q", "--no-edit", "lux/main")
	a := g.commit(g.dir, "own.txt", "the run's work\n", "A")
	tree := g.git("rev-parse", a+"^{tree}")
	r := g.in(g.dir, "commit-tree", tree, "-p", s, "-m", "replacement")
	g.git("replace", m, r)

	if got := diffPaths(g.diff("task " + m)); !slices.Equal(got, []string{"own.txt"}) {
		t.Fatalf("replacement hid the Run's work: %v", got)
	}
}

func TestTheFinalHookIgnoresCheckoutReplacementObjects(t *testing.T) {
	g := newGivenRepo(t, "task")
	s := g.startedAt
	m := g.someoneCommits("main", "m.txt", "merged base\n")
	g.git("merge", "-q", "--no-edit", "lux/main")
	a := g.commit(g.dir, "own.txt", "the run's work\n", "A")
	tree := g.git("rev-parse", a+"^{tree}")
	r := g.in(g.dir, "commit-tree", tree, "-p", s, "-m", "replacement")
	g.git("replace", m, r)

	// Stand in only for the authenticated endpoint, not its returned SHA
	// or any of the hook's Git operations.
	dir := t.TempDir()
	dude := filepath.Join(dir, "dude")
	if err := os.WriteFile(dude, []byte("#!/bin/sh\nprintf 'target "+m+"\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	script := strings.ReplaceAll(diffScript, "/usr/local/bin/dude", dude)
	artifacts := filepath.Join(dir, "artifacts")
	cmd := exec.Command("sh", "-c", script, "dude-diff", "artifacts", "target", g.dir, "task")
	cmd.Env = append(os.Environ(), "LUX_ARTIFACTS="+artifacts)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("hook: %v %s", err, out)
	}
	patch, err := os.ReadFile(filepath.Join(artifacts, finalDiffDir, "target.patch"))
	if err != nil {
		t.Fatal(err)
	}
	if got := diffPaths(string(patch)); !slices.Equal(got, []string{"own.txt"}) {
		t.Fatalf("replacement hid the Run's final work: %v\n%s", got, patch)
	}
}

// What cannot be folded in leaves the diff as it is today, byte for byte:
// a given commit the checkout lacks, or a ref it never had; one fetched but
// not merged; a merge of the start with it that conflicts; and a git
// without merge-tree.
func TestWhatCannotBeFoldedInLeavesTheDiffAgainstTheStart(t *testing.T) {
	g := newGivenRepo(t, "task")
	g.commit(g.dir, "a.txt", "the run's\n", "A")
	unmerged := g.someoneCommits("main", "m.txt", "fetched only\n")
	today := g.diff("task")
	for name, ref := range map[string]string{
		"not in the checkout": "task " + strings.Repeat("a", 40),
		"no such ref":         "task refs/remotes/lux/nowhere",
		"not merged":          "task " + unmerged,
	} {
		if out := g.diff(ref); out != today {
			t.Errorf("%s: printed\n%s\nwant today's\n%s", name, out, today)
		}
	}

	// The base changes the file the task branch changed before the Run
	// started: S and M conflict, whatever the Run's resolution.
	m := g.someoneCommits("main", "task.txt", "the base's own\n")
	if out, err := exec.Command("git", "-C", g.dir, "-c", "user.name=t", "-c", "user.email=t@x", "merge", "-q", "lux/main").CombinedOutput(); err == nil {
		t.Fatalf("the merge did not conflict: %s", out)
	}
	g.commit(g.dir, "task.txt", "resolved\n", "Merge lux/main")
	today = g.diff("task")
	if out := g.diff("task " + m); out != today {
		t.Errorf("conflicting merge: printed\n%s\nwant today's\n%s", out, today)
	}

	// git before 2.38: merge-tree --write-tree is refused. A wrapper on PATH
	// refuses it and runs the real git for everything else.
	h := newGivenRepo(t, "task")
	h.commit(h.dir, "a.txt", "the run's\n", "A")
	m = h.someoneCommits("main", "m.txt", "someone else's\n")
	h.git("merge", "-q", "--no-edit", "lux/main")
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	wrapper := "#!/bin/sh\nfor a in \"$@\"; do [ \"$a\" = merge-tree ] && { echo 'unknown option' >&2; exit 129; }; done\nexec " + real + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(wrapper), 0o755); err != nil {
		t.Fatal(err)
	}
	old := "PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH")
	if out, today := h.diff("task "+m, old), h.diff("task", old); out != today || !slices.Contains(diffPaths(out), "m.txt") {
		t.Errorf("no merge-tree: printed\n%s\nwant today's\n%s", out, today)
	}
}

// The fixed hook arguments contain no agent-writable sync refs.
func TestTheHookDoesNotTrustCheckoutSyncRefs(t *testing.T) {
	spec := buildSpec(AgentConfig{}, specInput{Phase: "", Role: "conductor", Model: "m", Repos: []specRepo{
		{Name: "api", Ref: "dude/wi_1", Synced: []string{"dude/wi_1", "main"}}, {Name: "web", Ref: "abc123"}}})
	got := strings.Join(spec.Workload.BeforeStop.Command[5:], "|")
	if got != "api|/workspace/repos/api|dude/wi_1|web|/workspace/repos/web|abc123" {
		t.Errorf("args %q", got)
	}
}

// The command passes each repository's given commits after its start, in
// order, and none when there are none: what the push scan runs is today's.
func TestTheDiffCommandCarriesEachRepositorysGivenCommits(t *testing.T) {
	cmd := diffCommand("-", map[string]string{"api": "s1", "web": "s2"}, map[string][]string{"api": {"t1", "m1"}})
	if got := strings.Join(cmd[5:], "|"); got != "api|/workspace/repos/api|s1 t1 m1|web|/workspace/repos/web|s2" {
		t.Errorf("args %q", got)
	}
	if got := strings.Join(diffCommand("-", map[string]string{"api": "s1"}, nil)[5:], "|"); got != "api|/workspace/repos/api|s1" {
		t.Errorf("no given: args %q", got)
	}
}

func TestDiffFallbackIsIsolatedPerRepository(t *testing.T) {
	bad := newGivenRepo(t, "task")
	m1 := bad.someoneCommits("main", "task.txt", "conflicting base\n")
	if exec.Command("git", "-C", bad.dir, "merge", "-q", "lux/main").Run() == nil {
		t.Fatal("expected conflicting merge")
	}
	bad.commit(bad.dir, "task.txt", "resolved\n", "Resolve")
	good := newGivenRepo(t, "task")
	good.commit(good.dir, "own.txt", "own\n", "Own")
	m2 := good.someoneCommits("main", "imported.txt", "imported\n")
	good.git("merge", "-q", "--no-edit", "lux/main")
	out, err := exec.Command("sh", "-c", diffScript, "dude-diff", "-",
		"bad", bad.dir, "task "+m1, "good", good.dir, "task "+m2).CombinedOutput()
	if err != nil {
		t.Fatalf("diff: %v %s", err, out)
	}
	if got := diffPaths(string(out)); !slices.Equal(got, []string{"bad/task.txt", "good/own.txt"}) {
		t.Fatalf("repository isolation: %v\n%s", got, out)
	}
}
