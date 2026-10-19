package fakegit

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Each answer is checked against git's own on the same repository: a
// history with merges, commits sharing a second, an annotated tag, nested
// directories, an executable and a symlink, part of it packed (objects and
// refs).

func run(t *testing.T, dir string, env []string, stdin string, args ...string) (string, error) {
	t.Helper()
	c := exec.Command("git", append([]string{"-C", dir}, args...)...)
	c.Env = append(c.Environ(), env...)
	c.Stdin = strings.NewReader(stdin)
	out, err := c.Output()
	return strings.TrimSpace(string(out)), err
}

func must(t *testing.T, dir string, env []string, args ...string) string {
	t.Helper()
	out, err := run(t, dir, env, "", args...)
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return out
}

// history is a bare repository and every name the tests ask about.
func history(t *testing.T) (string, []string) {
	t.Helper()
	dir := t.TempDir()
	bare, work := filepath.Join(dir, "r.git"), filepath.Join(dir, "w")
	must(t, dir, nil, "init", "-q", "--bare", "-b", "main", bare)
	must(t, dir, nil, "init", "-q", "-b", "main", work)
	at := 1_700_000_000
	commit := func(msg string, files map[string]string) {
		for p, c := range files {
			full := filepath.Join(work, p)
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		must(t, work, nil, "add", "-A")
		// Two commits a second: ties in git's date order.
		at++
		date := fmt.Sprintf("@%d +0000", at/2)
		must(t, work, []string{"GIT_AUTHOR_DATE=" + date, "GIT_COMMITTER_DATE=" + date},
			"-c", "user.name=t", "-c", "user.email=t@x", "commit", "-q", "--allow-empty", "-m", msg)
	}
	commit("root", map[string]string{"a.txt": "a\n", "dir/sub/b.txt": "b\n", "dir/c.txt": "c\n"})
	if err := os.WriteFile(filepath.Join(work, "run.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("a.txt", filepath.Join(work, "link")); err != nil {
		t.Fatal(err)
	}
	commit("exec and link", nil)
	must(t, work, []string{"GIT_COMMITTER_DATE=" + fmt.Sprintf("@%d +0000", at/2)},
		"-c", "user.name=t", "-c", "user.email=t@x", "tag", "-a", "-m", "v1", "v1")
	must(t, work, nil, "tag", "light")
	must(t, work, nil, "checkout", "-q", "-b", "side")
	commit("side 1", map[string]string{"dir/sub/b.txt": "side\n"})
	commit("side 2", map[string]string{"s.txt": "s\n"})
	must(t, work, nil, "checkout", "-q", "main")
	commit("main 1", map[string]string{"a.txt": "main\n"})
	commit("main 2", nil)
	must(t, work, nil, "checkout", "-q", "-b", "other", "side")
	commit("other 1", map[string]string{"o.txt": "o\n"})
	must(t, work, nil, "checkout", "-q", "main")
	at++
	date := fmt.Sprintf("@%d +0000", at/2)
	must(t, work, []string{"GIT_AUTHOR_DATE=" + date, "GIT_COMMITTER_DATE=" + date},
		"-c", "user.name=t", "-c", "user.email=t@x", "merge", "-q", "--no-ff", "-m", "merge side", "side")
	commit("main 3", map[string]string{"dir/d.txt": "d\n"})
	must(t, work, nil, "checkout", "-q", "-b", "dude/wi_1/attempt-1", "other")
	commit("attempt", map[string]string{"FIXED.md": "fixed\n"})
	must(t, work, nil, "push", "-q", bare, "--all")
	must(t, work, nil, "push", "-q", bare, "--tags")
	// Part packed: what is there now is in a pack and packed-refs, what
	// follows loose.
	must(t, bare, nil, "repack", "-q", "-a", "-d")
	must(t, bare, nil, "pack-refs", "--all")
	commit("loose", map[string]string{"l.txt": "l\n"})
	must(t, work, nil, "push", "-q", bare, "main")
	must(t, bare, nil, "symbolic-ref", "refs/heads/alias", "refs/heads/side")
	// A name that is a branch and a tag both, at different commits.
	must(t, bare, nil, "update-ref", "refs/tags/both", must(t, bare, nil, "rev-parse", "main~1"))
	must(t, bare, nil, "update-ref", "refs/heads/both", must(t, bare, nil, "rev-parse", "side"))

	names := []string{"main", "side", "other", "dude/wi_1/attempt-1", "refs/heads/dude/wi_1/attempt-1", "HEAD",
		"v1", "light", "refs/tags/v1", "refs/heads/main", "heads/main", "tags/v1", "both", "refs/heads/both", "alias",
		"refs/heads/alias", "missing", "refs/heads/missing", "dude/wi_1", "dude", "refs/heads", "config", "objects",
		"FETCH_HEAD", "main~1", "main^2", "main^{tree}", "main:a.txt", "@", "", "main..side", "-q",
		strings.Repeat("0", 40), strings.Repeat("a", 40), "abcd", "deadbeef0", "x-g1234"}
	for _, n := range []string{"main", "side", "other", "v1", "main~1", "main~2", "main~3", "side~1", "dude/wi_1/attempt-1"} {
		sha := must(t, bare, nil, "rev-parse", n)
		names = append(names, sha, sha[:7], strings.ToUpper(sha))
	}
	return bare, names
}

func open(t *testing.T, dir string) *Repo {
	t.Helper()
	r, ok := Open(dir)
	if !ok {
		t.Fatal("not opened")
	}
	t.Cleanup(r.Close)
	return r
}

func TestResolveAnswersAsRevParseDoesOrLeavesItToGit(t *testing.T) {
	bare, names := history(t)
	r := open(t, bare)
	answered := 0
	for _, n := range names {
		want, gitErr := run(t, bare, nil, "", "rev-parse", "--verify", "-q", n)
		got, err := r.Resolve(n)
		switch {
		case err == ErrUnknown:
			continue
		case err == ErrNotFound:
			if gitErr == nil {
				t.Errorf("%q: not found, git says %s", n, want)
			}
		case err != nil:
			t.Errorf("%q: %v", n, err)
		case gitErr != nil:
			t.Errorf("%q: %s, git finds none", n, got)
		case got.String() != want:
			t.Errorf("%q: %s, git says %s", n, got, want)
		}
		answered++
	}
	if answered < len(names)/2 {
		t.Errorf("answered %d of %d names", answered, len(names))
	}
}

func TestTreeOfIsRevParsesTree(t *testing.T) {
	bare, names := history(t)
	r := open(t, bare)
	for _, n := range names {
		want, gitErr := run(t, bare, nil, "", "rev-parse", "--verify", "-q", n+"^{tree}")
		got, err := r.TreeOf(n)
		if err != nil {
			continue
		}
		if gitErr != nil || got.String() != want {
			t.Errorf("%q^{tree}: %s, git says %q (%v)", n, got, want, gitErr)
		}
	}
}

// The peel paths, each against git: a tag on a tag, a tag on a tree, a
// blob, a missing object, and a chain of tags longer than the ten hops
// fakegit follows. Where fakegit answers it must be git's answer; past
// the hop limit it must leave the name to git (ErrUnknown), never answer.
func TestPeelFollowsTagsAsGitDoesAndLeavesLongChainsToGit(t *testing.T) {
	bare, _ := history(t)
	tag := func(name, target string) {
		must(t, bare, []string{"GIT_COMMITTER_DATE=@1700000000 +0000"},
			"-c", "user.name=t", "-c", "user.email=t@x", "tag", "-a", "-m", name, name, target)
	}
	tag("onv1", "v1")            // tag -> tag -> commit
	tag("ontree", "main^{tree}") // tag -> tree
	tag("onblob", "main:a.txt")  // tag -> blob
	prev := "main"
	for i := range 12 { // tag(12) -> ... -> tag(1) -> commit: past ten hops
		name := fmt.Sprintf("chain%d", i+1)
		tag(name, prev)
		prev = name
	}
	r := open(t, bare)
	cases := []struct {
		name   string
		commit bool // git peels it to a commit
		tree   bool // git peels it to a tree
		long   bool // past fakegit's hop limit for both: must be ErrUnknown
	}{
		{"onv1", true, true, false},
		{"ontree", false, true, false},
		{"onblob", false, false, false},
		{"chain9", true, false, false}, // nine tags reach the commit; its tree is an eleventh hop
		{"chain12", true, true, true},
		{must(t, bare, nil, "rev-parse", "main:a.txt"), false, false, false},
		{strings.Repeat("e", 40), false, false, false},
	}
	for _, tc := range cases {
		wantC, errC := run(t, bare, nil, "", "rev-parse", "--verify", "-q", tc.name+"^{commit}")
		if (errC == nil) != tc.commit {
			t.Fatalf("%q^{commit}: fixture expects %v, git says %q (%v)", tc.name, tc.commit, wantC, errC)
		}
		c, err := r.Commit(tc.name)
		switch {
		case tc.long:
			if err != ErrUnknown {
				t.Errorf("%q: past the hop limit, Commit = %v, %v; want ErrUnknown", tc.name, c, err)
			}
		case err == ErrUnknown || err == ErrNotFound:
			if tc.commit {
				t.Errorf("%q: git peels it to %s, fakegit left it (%v)", tc.name, wantC, err)
			}
		case err != nil:
			t.Errorf("%q: %v", tc.name, err)
		case !tc.commit:
			t.Errorf("%q: Commit = %s, git has no commit there", tc.name, c.Hash)
		case c.Hash.String() != wantC:
			t.Errorf("%q: Commit = %s, git says %s", tc.name, c.Hash, wantC)
		}
		// tc.tree is whether fakegit should answer the tree. git always can
		// for a commit-ish; fakegit stops at ten objects, so a tree one hop
		// past that is left to git.
		wantT, errT := run(t, bare, nil, "", "rev-parse", "--verify", "-q", tc.name+"^{tree}")
		if gitTree := errT == nil; gitTree != (tc.tree || tc.commit) {
			t.Fatalf("%q^{tree}: fixture expects git to find one = %v, git says %q (%v)", tc.name, tc.tree || tc.commit, wantT, errT)
		}
		h, err := r.TreeOf(tc.name)
		switch {
		case tc.long || (tc.commit && !tc.tree):
			if err != ErrUnknown {
				t.Errorf("%q: past the hop limit, TreeOf = %s, %v; want ErrUnknown", tc.name, h, err)
			}
		case err == ErrUnknown || err == ErrNotFound:
			if tc.tree {
				t.Errorf("%q^{tree}: git says %s, fakegit left it (%v)", tc.name, wantT, err)
			}
		case err != nil:
			t.Errorf("%q^{tree}: %v", tc.name, err)
		case !tc.tree:
			t.Errorf("%q: TreeOf = %s, git has no tree there", tc.name, h)
		case h.String() != wantT:
			t.Errorf("%q^{tree}: %s, git says %s", tc.name, h, wantT)
		}
	}
}

// commits are the names of the history's commits (and the annotated tag).
var commits = []string{"main", "side", "other", "v1", "light", "both", "refs/tags/both", "dude/wi_1/attempt-1", "alias"}

func TestIsAncestorIsMergeBases(t *testing.T) {
	bare, _ := history(t)
	r := open(t, bare)
	for _, a := range commits {
		for _, b := range commits {
			_, gitErr := run(t, bare, nil, "", "merge-base", "--is-ancestor", a, b)
			got, err := r.IsAncestor(a, b)
			if err != nil {
				t.Errorf("%s, %s: %v", a, b, err)
				continue
			}
			if got != (gitErr == nil) {
				t.Errorf("%s is an ancestor of %s: %v, git says %v", a, b, got, gitErr == nil)
			}
		}
	}
	if _, err := r.IsAncestor("main", strings.Repeat("a", 40)); err == nil {
		t.Error("an ancestor of a commit that is not there")
	}
}

func TestRangeIsRevListsInItsOrder(t *testing.T) {
	bare, _ := history(t)
	r := open(t, bare)
	for _, a := range commits {
		for _, b := range commits {
			want := must(t, bare, nil, "rev-list", "--reverse", "--parents", a+".."+b)
			cs, err := r.Range(a, b)
			if err != nil {
				t.Fatalf("%s..%s: %v", a, b, err)
			}
			var lines []string
			for _, c := range cs {
				line := c.Hash.String()
				for _, p := range c.ParentHashes {
					line += " " + p.String()
				}
				lines = append(lines, line)
			}
			if got := strings.Join(lines, "\n"); got != want {
				t.Errorf("%s..%s:\n%s\ngit:\n%s", a, b, got, want)
			}
			n, err := r.Count(a, b)
			if want := must(t, bare, nil, "rev-list", "--count", a+".."+b); err != nil || strconv.Itoa(n) != want {
				t.Errorf("count %s..%s: %d (%v), git says %s", a, b, n, err, want)
			}
		}
	}
}

func TestHasPathIsCatFileE(t *testing.T) {
	bare, _ := history(t)
	r := open(t, bare)
	for _, n := range commits {
		for _, p := range []string{"FIXED.md", "a.txt", "dir", "dir/sub", "dir/sub/b.txt", "dir/nope", "a.txt/x", "link", "run.sh", "s.txt"} {
			_, gitErr := run(t, bare, nil, "", "cat-file", "-e", n+":"+p)
			got, err := r.HasPath(n, p)
			if err != nil {
				t.Errorf("%s:%s: %v", n, p, err)
				continue
			}
			if got != (gitErr == nil) {
				t.Errorf("%s:%s there: %v, git says %v", n, p, got, gitErr == nil)
			}
		}
	}
}

func TestWriteBlobIsHashObject(t *testing.T) {
	bare, _ := history(t)
	r := open(t, bare)
	for _, content := range []string{"", "x", "line\n", "a\r\nb\n", "\x00binary\xff"} {
		got, err := r.WriteBlob(content)
		if err != nil {
			t.Fatal(err)
		}
		want, _ := run(t, bare, nil, content, "hash-object", "--stdin")
		if got.String() != want {
			t.Errorf("%q: %s, git says %s", content, got, want)
		}
		if out, _ := run(t, bare, nil, "", "cat-file", "blob", got.String()); out != strings.TrimSpace(content) {
			t.Errorf("%q: git reads %q", content, out)
		}
	}
}

// gitTreeWith is the fakes' git version: a scratch index.
func gitTreeWith(t *testing.T, bare, treeish string, files map[string]string) (string, error) {
	index := filepath.Join(t.TempDir(), "index")
	env := []string{"GIT_INDEX_FILE=" + index}
	if _, err := run(t, bare, env, "", "read-tree", treeish); err != nil {
		return "", err
	}
	for p, c := range files {
		blob, err := run(t, bare, nil, c, "hash-object", "-w", "--stdin")
		if err != nil {
			return "", err
		}
		if _, err := run(t, bare, env, "", "update-index", "--add", "--cacheinfo", "100644,"+blob+","+p); err != nil {
			return "", err
		}
	}
	return run(t, bare, env, "", "write-tree")
}

func TestTreeWithIsAScratchIndexsTree(t *testing.T) {
	bare, _ := history(t)
	r := open(t, bare)
	cases := []map[string]string{
		nil,
		{"a.txt": "changed\n"},
		{"new.txt": "n\n"},
		{"dir/sub/b.txt": "deep\n", "dir/e.txt": "e\n", "z/y/x.txt": "x\n"},
		{"run.sh": "now plain\n", "link": "no longer a link\n"},
		{"dir.txt": "sorts before dir/\n", "dir-a": "and after\n", "dir/0": "0"},
		{"FIXED.md": "fixed\n"},
		// git refuses these, or the scratch index would; left to git.
		{"dir": "a file over a directory\n"},
		{"a.txt/x": "a directory under a file\n"},
		{"../out": "x"},
		{"dir//y": "x"},
	}
	for _, base := range []string{"main", "side", "v1", must(t, bare, nil, "rev-parse", "main^{tree}")} {
		for _, files := range cases {
			want, gitErr := gitTreeWith(t, bare, base, files)
			got, err := r.TreeWith(base, files)
			switch {
			case err == ErrUnknown:
				if gitErr == nil && !strings.Contains(fmt.Sprint(files), "dir//") {
					t.Logf("%s %v: left to git, which makes %s", base, files, want)
				}
			case err != nil:
				t.Errorf("%s %v: %v", base, files, err)
			case gitErr != nil:
				t.Errorf("%s %v: %s, git refuses", base, files, got)
			case got.String() != want:
				t.Errorf("%s %v: %s, git says %s", base, files, got, want)
			}
		}
	}
}

func TestCommitTreeIsACommitGitReads(t *testing.T) {
	bare, _ := history(t)
	r := open(t, bare)
	tree := must(t, bare, nil, "rev-parse", "main^{tree}")
	for _, c := range []struct {
		parents []string
		message string
	}{
		{[]string{"main"}, "one"},
		{[]string{"main", "side"}, "Merge branch 'side' into main"},
		{nil, "multi\n\nline\n"},
		{[]string{"v1"}, ""},
	} {
		sha, err := r.CommitTree(tree, c.parents, "Alice", "alice@example.com", c.message)
		if err != nil {
			t.Fatal(err)
		}
		var parents []string
		for _, p := range c.parents {
			parents = append(parents, "-p", must(t, bare, nil, "rev-parse", p+"^{commit}"))
		}
		// git's own, at the same second, as its environment sets it.
		date := must(t, bare, nil, "log", "-1", "--format=%ad", "--date=raw", sha.String())
		env := []string{"GIT_AUTHOR_NAME=Alice", "GIT_AUTHOR_EMAIL=alice@example.com", "GIT_COMMITTER_NAME=Alice",
			"GIT_COMMITTER_EMAIL=alice@example.com", "GIT_AUTHOR_DATE=" + date, "GIT_COMMITTER_DATE=" + date}
		want, err := run(t, bare, env, "", append(append([]string{"commit-tree", tree}, parents...), "-m", c.message)...)
		if err != nil {
			t.Fatal(err)
		}
		if sha.String() != want {
			got, _ := run(t, bare, nil, "", "cat-file", "-p", sha.String())
			wantBody, _ := run(t, bare, nil, "", "cat-file", "-p", want)
			t.Errorf("%v %q: %s\n%s\ngit's %s\n%s", c.parents, c.message, sha, got, want, wantBody)
		}
	}
	if out, err := run(t, bare, nil, "", "fsck", "--strict", "--no-dangling"); err != nil {
		t.Errorf("fsck: %v %s", err, out)
	}
}
