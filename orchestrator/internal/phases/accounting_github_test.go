package phases

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

// A publish is counted from GitHub's compares alone: start...head, what the
// trusted base T brought in (start...T), and the task's change on top of
// T (T...head). Nothing read in the checkout changes a count.

// seed commits files on top of the Run's start and makes that both
// GitHub's main and the Run's start, as if the checkout began there.
func (a *accountingRun) seed(files map[string]string) {
	a.t.Helper()
	a.commit(files, "seed more")
	git(a.t, a.work, "push", "-q", a.origin, "HEAD:refs/heads/main")
	a.start = a.head()
}

// mergeBase merges lux/main into the checkout; a conflict is resolved with
// resolved's contents.
func (a *accountingRun) mergeBase(resolved map[string]string) {
	a.t.Helper()
	if out, err := exec.Command("git", "-C", a.work, "-c", "user.name=Acme", "-c", "user.email=a@example.com",
		"merge", "-q", "--no-edit", "lux/main").CombinedOutput(); err != nil {
		if len(resolved) == 0 {
			a.t.Fatalf("merge: %v %s", err, out)
		}
		for path, content := range resolved {
			write(a.t, filepath.Join(a.work, path), content)
			git(a.t, a.work, "add", path)
		}
		git(a.t, a.work, "commit", "-q", "--no-edit")
	}
}

func TestAPublishIsCountedFromGitHubAlone(t *testing.T) {
	t.Run("a clean sync_base merge counts only the task's own work", func(t *testing.T) {
		a := newAccountingRun(t)
		a.commit(map[string]string{"mine.go": "package mine\n"}, "task work")
		a.advanceBase(map[string]string{"base.go": lines(300, "base")})
		a.syncBase()
		a.mergeBase(nil)
		acc := a.published()
		samePaths(t, acc, "mine.go")
		if acc.Lines != 1 || acc.Full != "" || acc.Binary {
			t.Fatalf("lines %d full %q binary %v, want mine.go's 1", acc.Lines, acc.Full, acc.Binary)
		}
	})
	t.Run("a resolution on a path the base changed counts", func(t *testing.T) {
		acc := conflictMerged(t).published()
		samePaths(t, acc, "mine.go", "shared.txt")
		// mine.go's line, and shared.txt's "task" line on top of the base's.
		if acc.Lines != 2 || acc.Full != "" {
			t.Fatalf("lines %d full %q, want 2", acc.Lines, acc.Full)
		}
	})
	t.Run("a base path the task leaves as the base has it is a pure import", func(t *testing.T) {
		a := newAccountingRun(t)
		a.commit(map[string]string{"mine.go": "package mine\n", "shared.txt": "task\n"}, "task work")
		a.advanceBase(map[string]string{"base.go": lines(300, "base"), "shared.txt": "base\n"})
		a.syncBase()
		a.mergeBase(map[string]string{"shared.txt": "base\n"})
		acc := a.published()
		samePaths(t, acc, "mine.go")
		if acc.Lines != 1 || acc.Full != "" {
			t.Fatalf("lines %d full %q, want mine.go's 1", acc.Lines, acc.Full)
		}
	})
	t.Run("a rename out of a path the base changed counts", func(t *testing.T) {
		// The task renames big.txt to new.go and grows it; the base puts 300
		// lines on top of big.txt, which the merge carries into new.go. On
		// top of the base, new.go is only the task's 100; from the start it
		// is 400, and new.go is no path of the base's, so 400 count.
		a := newAccountingRun(t)
		a.seed(map[string]string{"big.txt": lines(600, "big")})
		git(t, a.work, "mv", "big.txt", "new.go")
		a.commit(map[string]string{"new.go": lines(600, "big") + lines(100, "mine")}, "rename and grow")
		a.advanceBase(map[string]string{"big.txt": lines(300, "base") + lines(600, "big")})
		a.syncBase()
		a.mergeBase(nil)
		acc := a.published()
		samePaths(t, acc, "big.txt", "new.go")
		if acc.Lines != 400 || acc.Full != "" {
			t.Fatalf("lines %d full %q, want new.go's 400 from the start", acc.Lines, acc.Full)
		}
	})
	t.Run("a fetched base GitHub's main does not answer for counts in full", func(t *testing.T) {
		a := conflictMerged(t)
		a.intercept = func(r *http.Request) int {
			if strings.HasSuffix(r.URL.Path, "/compare/main..."+a.base) {
				return http.StatusNotFound
			}
			return 0
		}
		acc := a.published()
		if !strings.Contains(acc.Full, "could not compare main") || acc.Lines != 303 {
			t.Fatalf("lines %d full %q, want 303 counted in full", acc.Lines, acc.Full)
		}
	})
	t.Run("a push GitHub refuses to compare with the base counts in full", func(t *testing.T) {
		a := conflictMerged(t)
		a.intercept = func(r *http.Request) int {
			if strings.Contains(r.URL.Path, "/compare/"+a.base+"...") {
				return http.StatusNotFound
			}
			return 0
		}
		acc := a.published()
		if !strings.Contains(acc.Full, "could not compare the push") || acc.Lines != 303 {
			t.Fatalf("lines %d full %q, want 303 counted in full", acc.Lines, acc.Full)
		}
	})
	t.Run("a fetched base the push does not contain counts in full", func(t *testing.T) {
		a := newAccountingRun(t)
		a.commit(map[string]string{"mine.go": "package mine\n"}, "task work")
		a.advanceBase(map[string]string{"base.go": lines(300, "base")})
		a.syncBase()
		a.mergeBase(nil)
		a.advanceBase(map[string]string{"later.go": "later\n"})
		a.syncBase()
		acc := a.published()
		samePaths(t, acc, "base.go", "mine.go")
		if !strings.Contains(acc.Full, "does not contain") || acc.Lines != 301 {
			t.Fatalf("lines %d full %q, want 301 counted in full", acc.Lines, acc.Full)
		}
	})
	t.Run("the task's change on top of the base GitHub cut short counts in full", func(t *testing.T) {
		// The base adds 150 files, which the task removes again after
		// merging, and the task adds 150 of its own: each compare from the
		// start lists 151 files, T...head 301.
		a := newAccountingRun(t)
		base, mine := map[string]string{"shared.txt": "base\n"}, map[string]string{"shared.txt": "task\n"}
		for i := range 150 {
			base[fmt.Sprintf("gen/f%03d", i)] = fmt.Sprintf("generated %d\n", i)
			mine[fmt.Sprintf("mine/f%03d", i)] = fmt.Sprintf("mine %d\n", i)
		}
		a.commit(mine, "task work")
		a.advanceBase(base)
		a.syncBase()
		a.mergeBase(map[string]string{"shared.txt": "task\nbase\n"})
		git(t, a.work, "rm", "-q", "-r", "gen")
		a.commit(nil, "drop gen")
		acc := a.published()
		if !strings.Contains(acc.Full, "on top of main, its most") {
			t.Fatalf("lines %d full %q", acc.Lines, acc.Full)
		}
	})
}

// A file GitHub shows no patch for, with no lines counted, is one it
// cannot count in lines, whatever its status; an empty file is not.
func TestABinaryChangeIsBinaryWhateverItsStatus(t *testing.T) {
	blob := strings.Repeat("\x00\x01binary\n", 400)
	for name, change := range map[string]func(a *accountingRun){
		"renamed": func(a *accountingRun) {
			git(a.t, a.work, "mv", "a.bin", "c.bin")
			a.commit(map[string]string{"c.bin": blob + "\x00more"}, "rename")
		},
		// a.bin emptied (the empty blob, not binary) and copied to b.bin.
		"copied": func(a *accountingRun) {
			a.commit(map[string]string{"b.bin": blob}, "copy")
			if err := os.WriteFile(filepath.Join(a.work, "a.bin"), nil, 0o644); err != nil {
				a.t.Fatal(err)
			}
			a.commit(nil, "empty")
		},
		"changed to a link": func(a *accountingRun) {
			path := filepath.Join(a.work, "a.bin")
			if err := os.Remove(path); err != nil {
				a.t.Fatal(err)
			}
			if err := os.Symlink("README.md", path); err != nil {
				a.t.Fatal(err)
			}
			a.commit(nil, "link")
		},
		"removed": func(a *accountingRun) {
			a.commit(map[string]string{"a.bin": ""}, "remove")
		},
	} {
		t.Run(name, func(t *testing.T) {
			a := newAccountingRun(t)
			a.seed(map[string]string{"a.bin": blob})
			change(a)
			if acc := a.published(); !acc.Binary {
				t.Fatalf("paths %q lines %d: not binary", acc.Paths, acc.Lines)
			}
		})
	}
	t.Run("an empty file is not binary", func(t *testing.T) {
		a := newAccountingRun(t)
		if err := os.WriteFile(filepath.Join(a.work, "empty.txt"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		a.commit(nil, "empty")
		acc := a.published()
		samePaths(t, acc, "empty.txt")
		if acc.Binary || acc.Lines != 0 {
			t.Fatalf("lines %d binary %v", acc.Lines, acc.Binary)
		}
	})
	// GitHub lists a diff too large to show with its numbers and no patch;
	// the fake never does, so this one is GitHub's entry as it sends it.
	t.Run("a diff GitHub would not show is past the line limit", func(t *testing.T) {
		acc := counted([]forge.ComparedFile{{Filename: "huge.sql", Status: "modified", Additions: 20000, SHA: "4b825dc642cb6eb9a060e54bf8d69288fbee4904"}})
		if !acc.Binary {
			t.Fatalf("lines %d: not past the limit", acc.Lines)
		}
	})
}
