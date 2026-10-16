package phases

import (
	"fmt"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

// The checkout and its container are the agent's: nothing read there is
// counted. A forged ref or merge never hides a change.

// conflictMerged is a Run that committed mine.go and shared.txt, then
// synced a base that changed base.go (300 lines) and shared.txt, and
// resolved the conflict keeping both.
func conflictMerged(t *testing.T) *accountingRun {
	a := newAccountingRun(t)
	a.commit(map[string]string{"mine.go": "package mine\n", "shared.txt": "task\n"}, "task work")
	a.advanceBase(map[string]string{"base.go": lines(300, "base"), "shared.txt": "base\n"})
	a.syncBase()
	if _, err := exec.Command("git", "-C", a.work, "merge", "-q", "--no-edit", "lux/main").CombinedOutput(); err == nil {
		t.Fatal("the fixture's merge did not conflict")
	}
	write(t, filepath.Join(a.work, "shared.txt"), "task\nbase\n")
	git(t, a.work, "add", "shared.txt")
	git(t, a.work, "commit", "-q", "--no-edit")
	return a
}

func TestAPublishCountsTheTasksOwnWorkAgainstTrustedData(t *testing.T) {
	big := lines(500, "mine")
	t.Run("a sync_base merge counts only the task's work and its resolution", func(t *testing.T) {
		acc := conflictMerged(t).published()
		samePaths(t, acc, "mine.go", "shared.txt")
		if acc.Lines > 10 || acc.Full != "" {
			t.Fatalf("lines %d (full: %q): the base's 300 were counted", acc.Lines, acc.Full)
		}
	})
	t.Run("a base ref the checkout forged hides nothing", func(t *testing.T) {
		a := newAccountingRun(t)
		a.commit(map[string]string{"big.txt": big}, "big")
		git(t, a.work, "update-ref", "refs/remotes/lux/main", "HEAD")
		acc := a.published()
		samePaths(t, acc, "big.txt")
		if acc.Lines != 500 || acc.Full == "" {
			t.Fatalf("lines %d full %q, want 500 counted in full", acc.Lines, acc.Full)
		}
	})
	t.Run("a merge of a forged base commit hides nothing", func(t *testing.T) {
		a := newAccountingRun(t)
		a.advanceBase(map[string]string{"base.go": "base\n"})
		a.syncBase()
		mine := a.commit(map[string]string{"big.txt": big}, "big")
		git(t, a.work, "checkout", "-q", "--detach", a.base)
		git(t, a.work, "merge", "-q", "--no-edit", mine)
		git(t, a.work, "update-ref", "refs/remotes/lux/main", "HEAD")
		forged := a.head()
		git(t, a.work, "checkout", "-q", "--detach", mine+"~1")
		git(t, a.work, "merge", "-q", "--no-edit", "--no-ff", forged)
		acc := a.published()
		samePaths(t, acc, "big.txt")
		if acc.Lines != 500 {
			t.Fatalf("lines %d: the forged base hid big.txt", acc.Lines)
		}
	})
	t.Run("a base lux reported that GitHub's main does not hold counts in full", func(t *testing.T) {
		a := newAccountingRun(t)
		a.advanceBase(map[string]string{"base.go": lines(30, "base")})
		a.syncBase()
		git(t, a.work, "merge", "-q", "--no-edit", "lux/main")
		a.base = a.commit(map[string]string{"big.txt": big}, "big")
		acc := a.published()
		if acc.Lines != 530 || !strings.Contains(acc.Full, "not on GitHub's main") {
			t.Fatalf("lines %d full %q, want 530 counted in full", acc.Lines, acc.Full)
		}
	})
	t.Run("no base reported counts in full", func(t *testing.T) {
		a := newAccountingRun(t)
		a.advanceBase(map[string]string{"base.go": lines(30, "base")})
		git(t, a.work, "fetch", "-q", "origin", "+refs/heads/main:refs/remotes/lux/main")
		git(t, a.work, "merge", "-q", "--no-edit", "lux/main")
		a.commit(map[string]string{"mine.go": "mine\n"}, "mine")
		acc := a.published()
		samePaths(t, acc, "base.go", "mine.go")
		if acc.Lines != 31 || !strings.Contains(acc.Full, "no fetch of main") {
			t.Fatalf("lines %d full %q", acc.Lines, acc.Full)
		}
	})
	t.Run("a compare GitHub cut short counts in full", func(t *testing.T) {
		a := newAccountingRun(t)
		files := map[string]string{}
		for i := range forge.CompareFileCap {
			files[fmt.Sprintf("gen/f%03d", i)] = "x\n"
		}
		a.advanceBase(files)
		a.syncBase()
		a.commit(map[string]string{"shared.txt": "task\n"}, "task")
		git(t, a.work, "merge", "-q", "--no-edit", "lux/main")
		acc := a.published()
		if !strings.Contains(acc.Full, "its most") || acc.Lines < forge.CompareFileCap {
			t.Fatalf("lines %d full %q", acc.Lines, acc.Full)
		}
	})
	t.Run("a compare of the task's push GitHub cut short counts in full", func(t *testing.T) {
		a := newAccountingRun(t)
		a.advanceBase(map[string]string{"shared.txt": "base\n"})
		a.syncBase()
		files := map[string]string{"shared.txt": "task\n"}
		for i := range forge.CompareFileCap + 1 {
			files[fmt.Sprintf("mine/f%03d", i)] = "x\n"
		}
		a.commit(files, "task")
		acc := a.published()
		if !strings.Contains(acc.Full, "GitHub listed 300 files, its most") {
			t.Fatalf("lines %d full %q", acc.Lines, acc.Full)
		}
	})
	t.Run("a compare of what the base brought in cut short counts in full", func(t *testing.T) {
		a := newAccountingRun(t)
		files := map[string]string{"shared.txt": "base\n"}
		for i := range forge.CompareFileCap {
			files[fmt.Sprintf("gen/f%03d", i)] = "x\n"
		}
		a.advanceBase(files)
		a.syncBase()
		a.commit(map[string]string{"shared.txt": "task\n"}, "task")
		acc := a.published()
		if !strings.Contains(acc.Full, "of what main brought in, its most") || acc.Lines != 1 {
			t.Fatalf("lines %d full %q", acc.Lines, acc.Full)
		}
	})
	t.Run("a compare of the base GitHub would not answer counts in full", func(t *testing.T) {
		a := conflictMerged(t)
		a.intercept = func(r *http.Request) int {
			if strings.HasSuffix(r.URL.Path, "..."+a.base) && strings.Contains(r.URL.Path, "/compare/"+a.start) {
				return http.StatusNotFound
			}
			return 0
		}
		acc := a.published()
		if !strings.Contains(acc.Full, "could not say what main brought in") || acc.Lines < 300 {
			t.Fatalf("lines %d full %q", acc.Lines, acc.Full)
		}
	})
}

// A GitHub failing on any compare of the trusted path is an error to retry
// on, neither a full count nor a refusal.
func TestAGitHubBlipOnATrustedCompareIsRetried(t *testing.T) {
	for name, failing := range map[string]func(a *accountingRun, path string) bool{
		"main...T": func(a *accountingRun, path string) bool {
			return strings.HasSuffix(path, "/compare/main..."+a.base)
		},
		"start...T": func(a *accountingRun, path string) bool {
			return strings.HasSuffix(path, "/compare/"+a.start+"..."+a.base)
		},
		"T...head": func(a *accountingRun, path string) bool {
			return strings.Contains(path, "/compare/"+a.base+"...")
		},
	} {
		t.Run(name, func(t *testing.T) {
			a := conflictMerged(t)
			a.intercept = func(r *http.Request) int {
				if failing(a, r.URL.Path) {
					return http.StatusBadGateway
				}
				return 0
			}
			acc, err := a.publishedErr()
			if err == nil || !forge.Transient(err) {
				t.Fatalf("err %v, lines %d full %q: want a transient error", err, acc.Lines, acc.Full)
			}
		})
	}
}
