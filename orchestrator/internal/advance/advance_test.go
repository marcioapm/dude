package advance

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

// advancing is a task branch on a fake GitHub, at start, and a writer's
// commit on start.
type advancing struct {
	t             *testing.T
	fake          *fakegithub.Server
	gh            *forge.GitHub
	start, commit string
}

func newAdvancing(t *testing.T) *advancing {
	t.Helper()
	dir := t.TempDir()
	origin := filepath.Join(dir, "origin.git")
	git(t, dir, "init", "-q", "--bare", "-b", "main", origin)
	fake := fakegithub.New(origin, "acme/target")
	srv := httptest.NewServer(fake.Handler())
	t.Cleanup(srv.Close)
	a := &advancing{t: t, fake: fake, gh: forge.NewGitHub(forge.Credential{Auth: "pat", Secret: "ghp_x", APIBaseURL: srv.URL})}
	work := filepath.Join(dir, "work")
	git(t, dir, "init", "-q", "-b", "main", work)
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("# target\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, work, "add", ".")
	git(t, work, "commit", "-qm", "seed")
	git(t, work, "push", "-q", origin, "main", "main:task")
	a.start = fake.SHA("task")
	a.commit = fake.CommitFiles("task", "the writer's", map[string]string{"MINE.md": "mine\n"})
	// The writer's commit, pushed to a branch of its own; the task branch
	// back at start.
	git(t, origin, "update-ref", "refs/heads/run", a.commit)
	git(t, origin, "update-ref", "refs/heads/task", a.start)
	return a
}

func (a *advancing) advance() (Result, error) {
	return Branch(context.Background(), a.gh, "acme/target", "task", "main", a.start, a.commit, func() (bool, error) { return true, nil })
}

func TestAWriterIsMergedInOnlyOverMergesOfTheBase(t *testing.T) {
	t.Run("one base merge: the writer is merged in on top of it", func(t *testing.T) {
		a := newAdvancing(t)
		a.fake.CommitFiles("main", "base", map[string]string{"BASE.md": "base\n"})
		x := a.fake.MergeInto("task", "main", nil)
		adv, err := a.advance()
		if err != nil {
			t.Fatal(err)
		}
		if got := a.fake.Parents(adv.Head); adv.Head != a.fake.SHA("task") || !slices.Equal(got, []string{x, a.commit}) {
			t.Fatalf("head %.8s with parents %v, want the merge of %.8s and %.8s", adv.Head, got, x, a.commit)
		}
		if adv.Commit != a.commit || adv.MergedInto != x {
			t.Errorf("advanced %+v", adv)
		}
	})
	t.Run("two base merges in a row: merged in", func(t *testing.T) {
		a := newAdvancing(t)
		a.fake.CommitFiles("main", "base", map[string]string{"BASE.md": "base\n"})
		a.fake.MergeInto("task", "main", nil)
		a.fake.CommitFiles("main", "base again", map[string]string{"BASE2.md": "base\n"})
		x := a.fake.MergeInto("task", "main", nil)
		if adv, err := a.advance(); err != nil || adv.MergedInto != x {
			t.Fatalf("%+v %v, want merged into %.8s", adv, err, x)
		}
	})
	t.Run("an already merged writer: its merge is the head, sent again nothing", func(t *testing.T) {
		a := newAdvancing(t)
		a.fake.CommitFiles("main", "base", map[string]string{"BASE.md": "base\n"})
		x := a.fake.MergeInto("task", "main", nil)
		first, err := a.advance()
		if err != nil {
			t.Fatal(err)
		}
		again, err := a.advance()
		if err != nil || again.Head != first.Head || again.MergedInto != x {
			t.Fatalf("again %+v %v, want %.8s over %.8s", again, err, first.Head, x)
		}
	})
	refused := func(t *testing.T, a *advancing) {
		t.Helper()
		before := a.fake.SHA("task")
		_, err := a.advance()
		var fe *forge.Error
		if !errors.As(err, &fe) || !NotFastForward(err) {
			t.Fatalf("%v, want the fast-forward's refusal", err)
		}
		if got := a.fake.SHA("task"); got != before {
			t.Errorf("the task branch moved to %.8s", got)
		}
	}
	t.Run("a person's commit: refused", func(t *testing.T) {
		a := newAdvancing(t)
		a.fake.CommitFiles("task", "a person's", map[string]string{"PERSON.md": "theirs\n"})
		refused(t, a)
	})
	t.Run("a person's commit under a base merge: refused", func(t *testing.T) {
		a := newAdvancing(t)
		a.fake.CommitFiles("task", "a person's", map[string]string{"PERSON.md": "theirs\n"})
		a.fake.CommitFiles("main", "base", map[string]string{"BASE.md": "base\n"})
		a.fake.MergeInto("task", "main", nil)
		refused(t, a)
	})
	t.Run("a merge of a branch that is not the base: refused", func(t *testing.T) {
		a := newAdvancing(t)
		a.fake.Branch("other", "main", "someone's other work")
		a.fake.MergeInto("task", "other", nil)
		refused(t, a)
	})
	t.Run("the branch fast-forwarded to the base, no merge: refused", func(t *testing.T) {
		a := newAdvancing(t)
		a.fake.CommitFiles("main", "base", map[string]string{"BASE.md": "base\n"})
		git(t, a.fake.Repo, "update-ref", "refs/heads/task", a.fake.SHA("main"))
		refused(t, a)
	})
	t.Run("the branch rewritten, start no longer in it: refused", func(t *testing.T) {
		a := newAdvancing(t)
		tree := git(t, a.fake.Repo, "rev-parse", a.start+"^{tree}")
		git(t, a.fake.Repo, "update-ref", "refs/heads/task", git(t, a.fake.Repo, "commit-tree", tree, "-m", "rewritten"))
		refused(t, a)
	})
	t.Run("the writer conflicting with the base merge: refused naming it", func(t *testing.T) {
		a := newAdvancing(t)
		a.fake.CommitFiles("main", "base", map[string]string{"MINE.md": "the base's\n"})
		x := a.fake.MergeInto("task", "main", map[string]string{"MINE.md": "the base's\n"})
		_, err := a.advance()
		var conflict *ErrBaseMergeConflict
		if !errors.As(err, &conflict) || a.fake.SHA("task") != x {
			t.Fatalf("%v, branch at %.8s; want a base-merge conflict, the branch at %.8s", err, a.fake.SHA("task"), x)
		}
	})
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=Acme", "-c", "user.email=a@example.com"}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// notes is a ten-line file whose first and last lines are given.
func notes(first, last string) string {
	return first + "\n2\n3\n4\n5\n6\n7\n8\n9\n" + last + "\n"
}

// bothSides is a task branch whose writer's start edited files, and the
// base edited them too; the task branch's head before any merge.
func bothSides(t *testing.T, files map[string]string, task, base map[string]string) (*advancing, string) {
	t.Helper()
	a := newAdvancing(t)
	a.fake.CommitFiles("main", "shared", files)
	a.fake.MergeInto("task", "main", nil)
	if task != nil {
		a.fake.CommitFiles("task", "the task's", task)
	}
	if base != nil {
		a.fake.CommitFiles("main", "the base's", base)
	}
	return a, a.fake.SHA("task")
}

// pure judges the task branch's head, a merge of main onto first.
func (a *advancing) pure(first string) (bool, error) {
	x := a.fake.SHA("task")
	ps := a.fake.Parents(x)
	if len(ps) != 2 || ps[0] != first {
		a.t.Fatalf("the merge %.8s has parents %v, want %.8s first", x, ps, first)
	}
	return pureMerge(context.Background(), a.gh, "acme/target", ps[0], ps[1], x, ps[0])
}

func TestABaseMergeOfAFileBothSidesEditedIsPureOnlyAsGitMergesIt(t *testing.T) {
	shared := map[string]string{"NOTES.md": notes("1", "10")}
	ours := map[string]string{"NOTES.md": notes("1", "ten, the task's")}
	theirs := map[string]string{"NOTES.md": notes("one, the base's", "10")}
	t.Run("merged cleanly by GitHub: pure", func(t *testing.T) {
		a, first := bothSides(t, shared, ours, theirs)
		x := a.fake.MergeInto("task", "main", nil)
		if got := git(t, a.fake.Repo, "show", x+":NOTES.md") + "\n"; got != notes("one, the base's", "ten, the task's") {
			t.Fatalf("the merge's NOTES.md %q, want both edits", got)
		}
		if pure, err := a.pure(first); err != nil || !pure {
			t.Fatalf("%v %v, want pure", pure, err)
		}
	})
	t.Run("the writer merged in over it", func(t *testing.T) {
		a, _ := bothSides(t, shared, ours, theirs)
		// The writer's start is the task branch with the task's edit.
		a.start = a.fake.SHA("task")
		a.commit = a.fake.CommitFiles("task", "the writer's", map[string]string{"MINE.md": "mine\n"})
		git(t, a.fake.Repo, "update-ref", "refs/heads/task", a.start)
		x := a.fake.MergeInto("task", "main", nil)
		adv, err := a.advance()
		if err != nil || adv.MergedInto != x || !slices.Equal(a.fake.Parents(adv.Head), []string{x, a.commit}) {
			t.Fatalf("%+v %v, want the writer merged in over %.8s", adv, err, x)
		}
		// And its own merge, found again, adopted (the writer second).
		again, err := a.advance()
		if err != nil || again.Head != adv.Head {
			t.Fatalf("again %+v %v, want %.8s adopted", again, err, adv.Head)
		}
	})
	t.Run("the merge's file carries an edit by hand: not pure", func(t *testing.T) {
		a, first := bothSides(t, shared, ours, theirs)
		a.fake.MergeInto("task", "main", map[string]string{"NOTES.md": notes("one, the base's", "ten, the task's") + "and a person's\n"})
		if pure, err := a.pure(first); err != nil || pure {
			t.Fatalf("%v %v, want not pure", pure, err)
		}
	})
	t.Run("a conflict the merge resolved: not pure", func(t *testing.T) {
		a, first := bothSides(t, shared, map[string]string{"NOTES.md": notes("one, the task's", "10")}, theirs)
		a.fake.MergeInto("task", "main", map[string]string{"NOTES.md": notes("one, the base's", "10")})
		if pure, err := a.pure(first); err != nil || pure {
			t.Fatalf("%v %v, want not pure", pure, err)
		}
	})
	t.Run("a conflict resolved as git's conflicted text: not pure", func(t *testing.T) {
		a, first := bothSides(t, shared, map[string]string{"NOTES.md": notes("one, the task's", "10")}, theirs)
		dir := t.TempDir()
		for name, content := range map[string]string{"o": notes("one, the task's", "10"), "b": notes("1", "10"), "t": notes("one, the base's", "10")} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		out, _ := exec.Command("git", "-C", dir, "merge-file", "-p", "--diff-algorithm=histogram", "-L", "ours", "-L", "base", "-L", "theirs", "o", "b", "t").Output()
		if !strings.Contains(string(out), "<<<<<<<") {
			t.Fatalf("no conflict: %q", out)
		}
		a.fake.MergeInto("task", "main", map[string]string{"NOTES.md": string(out)})
		if pure, err := a.pure(first); err != nil || pure {
			t.Fatalf("%v %v, want not pure", pure, err)
		}
	})
	t.Run("renamed by the task: not pure", func(t *testing.T) {
		a, _ := bothSides(t, map[string]string{"NOTES.md": notes("1", "10") + "11\n12\n13\n14\n15\n"}, nil, nil)
		work := filepath.Join(t.TempDir(), "w")
		git(t, filepath.Dir(work), "clone", "-q", a.fake.Repo, work)
		git(t, work, "checkout", "-q", "task")
		git(t, work, "mv", "NOTES.md", "RENAMED.md")
		if err := os.WriteFile(filepath.Join(work, "RENAMED.md"), []byte(notes("1", "ten, the task's")+"11\n12\n13\n14\n15\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		git(t, work, "commit", "-qam", "renamed")
		git(t, work, "push", "-q", "origin", "task")
		first := a.fake.SHA("task")
		a.fake.CommitFiles("main", "the base's", map[string]string{"NOTES.md": notes("one, the base's", "10") + "11\n12\n13\n14\n15\n"})
		x := a.fake.MergeInto("task", "main", nil)
		if got := git(t, a.fake.Repo, "show", x+":RENAMED.md") + "\n"; got != notes("one, the base's", "ten, the task's")+"11\n12\n13\n14\n15\n" {
			t.Fatalf("the merge's RENAMED.md %q, want both edits", got)
		}
		if pure, err := a.pure(first); err != nil || pure {
			t.Fatalf("%v %v, want not pure", pure, err)
		}
	})
	t.Run("added on both sides: not pure", func(t *testing.T) {
		a, first := bothSides(t, map[string]string{"OTHER.md": "x\n"}, map[string]string{"NEW.md": notes("1", "ten, the task's")}, map[string]string{"NEW.md": notes("one, the base's", "10")})
		a.fake.MergeInto("task", "main", map[string]string{"NEW.md": notes("one, the base's", "ten, the task's")})
		if pure, err := a.pure(first); err != nil || pure {
			t.Fatalf("%v %v, want not pure", pure, err)
		}
	})
	t.Run("more files than the bound: not pure; at it: pure", func(t *testing.T) {
		for _, n := range []int{maxRemerged, maxRemerged + 1} {
			files, task, base := map[string]string{}, map[string]string{}, map[string]string{}
			for i := range n {
				name := "N" + strconv.Itoa(i) + ".md"
				files[name], task[name], base[name] = shared["NOTES.md"], ours["NOTES.md"], theirs["NOTES.md"]
			}
			a, first := bothSides(t, files, task, base)
			if a.fake.MergeInto("task", "main", nil) == "" {
				t.Fatal("no merge")
			}
			if pure, err := a.pure(first); err != nil || pure != (n <= maxRemerged) {
				t.Fatalf("%d files: %v %v", n, pure, err)
			}
		}
	})
	t.Run("binary: not pure", func(t *testing.T) {
		bin := func(first, last string) string { return "\x00" + notes(first, last) }
		a, first := bothSides(t, map[string]string{"BIN": bin("1", "10")}, map[string]string{"BIN": bin("1", "ten")}, map[string]string{"BIN": bin("one", "10")})
		a.fake.MergeInto("task", "main", map[string]string{"BIN": bin("one", "ten")})
		if pure, err := a.pure(first); err != nil || pure {
			t.Fatalf("%v %v, want not pure", pure, err)
		}
	})
	t.Run("a version over the size bound: not pure", func(t *testing.T) {
		big := strings.Repeat("filler\n", maxRemergedBytes/7+1)
		a, first := bothSides(t, map[string]string{"BIG.md": notes("1", "10") + big}, map[string]string{"BIG.md": notes("1", "ten") + big}, map[string]string{"BIG.md": notes("one", "10") + big})
		a.fake.MergeInto("task", "main", nil)
		if pure, err := a.pure(first); err != nil || pure {
			t.Fatalf("%v %v, want not pure", pure, err)
		}
	})
	t.Run("no git to merge with: not pure, and the merge says why", func(t *testing.T) {
		a, first := bothSides(t, shared, ours, theirs)
		a.fake.MergeInto("task", "main", nil)
		defer func(was string) { gitBinary = was }(gitBinary)
		gitBinary = filepath.Join(t.TempDir(), "no-such-git")
		if pure, err := a.pure(first); err != nil || pure {
			t.Fatalf("%v %v, want not pure", pure, err)
		}
		_, clean, err := mergeFile(context.Background(), t.TempDir(), []byte("a\n"), []byte("a\n"), []byte("a\n"))
		var local *remergeError
		if clean || !errors.As(err, &local) {
			t.Fatalf("clean %v, %v; want an error and not clean", clean, err)
		}
	})
}

// A task branch and a base that each merged the other's two branches
// have two merge bases; a clean merge against either alone is not
// GitHub's merge of them, so neither is pure.
func TestABaseMergeOverTwoMergeBasesIsNotPure(t *testing.T) {
	f := func(two, ten string) string { return "1\n" + two + "\n3\n4\n5\n6\n7\n8\n9\n" + ten + "\n" }
	for _, resolved := range []string{f("a", "ten"), f("b", "ten")} {
		t.Run("resolved as "+strings.ReplaceAll(resolved, "\n", " "), func(t *testing.T) {
			a := newAdvancing(t)
			repo := a.fake.Repo
			a.fake.CommitFiles("main", "F", map[string]string{"F.md": f("x", "10")})
			git(t, repo, "update-ref", "refs/heads/b1", "refs/heads/main")
			git(t, repo, "update-ref", "refs/heads/b2", "refs/heads/main")
			a.fake.CommitFiles("b1", "a", map[string]string{"F.md": f("a", "10")})
			a.fake.CommitFiles("b2", "b", map[string]string{"F.md": f("b", "10")})
			git(t, repo, "update-ref", "refs/heads/task", "refs/heads/b1")
			a.fake.MergeInto("task", "b2", map[string]string{"F.md": f("a", "10")})
			first := a.fake.CommitFiles("task", "the task's", map[string]string{"F.md": f("a", "ten")})
			git(t, repo, "update-ref", "refs/heads/main", "refs/heads/b2")
			a.fake.MergeInto("main", "b1", map[string]string{"F.md": f("b", "10")})
			if bases := strings.Fields(git(t, repo, "merge-base", "--all", "task", "main")); len(bases) != 2 {
				t.Fatalf("merge bases %v, want two", bases)
			}
			if err := exec.Command("git", "-C", repo, "merge-tree", "--write-tree", "task", "main").Run(); err == nil {
				t.Fatal("merge-tree merged F.md cleanly, want a conflict")
			}
			a.fake.MergeInto("task", "main", map[string]string{"F.md": resolved})
			if pure, err := a.pure(first); err != nil || pure {
				t.Fatalf("%v %v, want not pure", pure, err)
			}
		})
	}
}

// Each refusal of a merge that might otherwise be judged pure.
func TestEachRefusalOfABaseMergeHolds(t *testing.T) {
	shared := map[string]string{"NOTES.md": notes("1", "10")}
	ours := map[string]string{"NOTES.md": notes("1", "ten, the task's")}
	theirs := map[string]string{"NOTES.md": notes("one, the base's", "10")}
	ctx := context.Background()
	notPure := func(t *testing.T, pure bool, err error) {
		t.Helper()
		if err != nil || pure {
			t.Fatalf("%v %v, want not pure", pure, err)
		}
	}
	t.Run("a file first added that the merge replaced with another: not pure", func(t *testing.T) {
		a, _ := bothSides(t, map[string]string{"OTHER.md": "x\n"}, map[string]string{"A.md": "a\n"}, map[string]string{"BASE.md": "base\n"})
		first, second := a.fake.SHA("task"), a.fake.SHA("main")
		merged := strings.Fields(git(t, a.fake.Repo, "merge-tree", "--write-tree", first, second))[0]
		// The merged tree with A.md's entry named B.md instead.
		listing := strings.Replace(git(t, a.fake.Repo, "ls-tree", merged), "\tA.md", "\tB.md", 1)
		tree := strings.TrimSpace(gitIn(t, a.fake.Repo, listing+"\n", "mktree"))
		if git(t, a.fake.Repo, "ls-tree", "--name-only", tree) != "B.md\nBASE.md\nOTHER.md\nREADME.md" {
			t.Fatalf("tree %s", git(t, a.fake.Repo, "ls-tree", tree))
		}
		x := gitAsGitHub(t, a.fake.Repo, "commit-tree", tree, "-p", first, "-p", second, "-m", "Merge branch 'main' into task")
		git(t, a.fake.Repo, "update-ref", "refs/heads/task", x)
		pure, err := a.pure(first)
		notPure(t, pure, err)
	})
	t.Run("a file of another status on one side, all three versions present: not pure", func(t *testing.T) {
		a, first := bothSides(t, shared, ours, theirs)
		x := a.fake.MergeInto("task", "main", nil)
		second := a.fake.Parents(x)[1]
		a.fake.Set(func(s *fakegithub.Server) {
			s.CompareAnswer = func(base, head string, answer map[string]any) {
				if base == second && head == first {
					for _, f := range answer["files"].([]any) {
						f.(map[string]any)["status"] = "changed"
					}
				}
			}
		})
		pure, err := a.pure(first)
		notPure(t, pure, err)
	})
	t.Run("re-merged directly, a file absent at the base: not pure", func(t *testing.T) {
		a, _ := bothSides(t, shared, nil, nil)
		base := a.fake.SHA("task")
		first := a.fake.CommitFiles("task", "the task's", map[string]string{"NEW.md": notes("1", "ten")})
		second := a.fake.CommitFiles("main", "the base's", map[string]string{"NEW.md": notes("one", "10")})
		blob := strings.TrimSpace(gitIn(t, a.fake.Repo, notes("one", "ten"), "hash-object", "-w", "--stdin"))
		pure, err := remerged(ctx, a.gh, "acme/target", base, first, second, []forge.ComparedFile{{Filename: "NEW.md", Status: "modified", SHA: blob}})
		notPure(t, pure, err)
	})
	t.Run("no merge base in the compare: not pure", func(t *testing.T) {
		a, first := bothSides(t, shared, ours, theirs)
		a.fake.MergeInto("task", "main", nil)
		a.fake.Set(func(s *fakegithub.Server) {
			s.CompareAnswer = func(base, head string, answer map[string]any) {
				answer["merge_base_commit"] = map[string]any{"sha": ""}
			}
		})
		pure, err := a.pure(first)
		notPure(t, pure, err)
		x := a.fake.SHA("task")
		single, err := singleMergeBase(ctx, a.gh, "acme/target", "", first, a.fake.Parents(x)[1])
		if err != nil || single {
			t.Fatalf("no merge base: %v %v, want not single", single, err)
		}
	})
	t.Run("a commit list from the merge base cut short: not pure", func(t *testing.T) {
		a, first := bothSides(t, shared, ours, theirs)
		x := a.fake.MergeInto("task", "main", nil)
		base := git(t, a.fake.Repo, "merge-base", first, a.fake.Parents(x)[1])
		a.fake.Set(func(s *fakegithub.Server) {
			s.CompareAnswer = func(from, head string, answer map[string]any) {
				if from == base {
					answer["total_commits"] = len(answer["commits"].([]any)) + 1
				}
			}
		})
		pure, err := a.pure(first)
		notPure(t, pure, err)
	})
	t.Run("cancelled while git merges: the cancellation, not a verdict", func(t *testing.T) {
		a, _ := bothSides(t, shared, ours, theirs)
		x := a.fake.MergeInto("task", "main", nil)
		ps := a.fake.Parents(x)
		dir := t.TempDir()
		started := filepath.Join(dir, "started")
		script := filepath.Join(dir, "git")
		if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch '"+started+"'\nexec sleep 30\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		defer func(was string) { gitBinary = was }(gitBinary)
		gitBinary = script
		cancelled, cancel := context.WithCancel(ctx)
		defer cancel()
		go func() {
			for {
				if _, err := os.Stat(started); err == nil {
					cancel()
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
		}()
		pure, err := pureMerge(cancelled, a.gh, "acme/target", ps[0], ps[1], x, ps[0])
		if pure || !errors.Is(err, context.Canceled) {
			t.Fatalf("%v %v, want context.Canceled", pure, err)
		}
		_, _, err = mergeFile(cancelled, t.TempDir(), []byte("a\n"), []byte("a\n"), []byte("a\n"))
		var local *remergeError
		if !errors.Is(err, context.Canceled) || errors.As(err, &local) {
			t.Fatalf("mergeFile %v, want context.Canceled and not a local failure", err)
		}
	})
	t.Run("a NUL past git's binary sniff: not pure", func(t *testing.T) {
		text := strings.Repeat("filler line\n", 8192/12+1)
		bin := func(first, last string) string { return notes(first, "10") + text + "\x00" + last + "\n" }
		a, first := bothSides(t, map[string]string{"LATE.md": bin("1", "end")}, map[string]string{"LATE.md": bin("1", "the end")}, map[string]string{"LATE.md": bin("one", "end")})
		a.fake.MergeInto("task", "main", map[string]string{"LATE.md": bin("one", "the end")})
		pure, err := a.pure(first)
		notPure(t, pure, err)
	})
	t.Run("a version exactly at the size bound: pure; one byte over: not", func(t *testing.T) {
		for _, size := range []int{maxRemergedBytes, maxRemergedBytes + 1} {
			// Every version, the merge's too, is the same length.
			pad := strings.Repeat("f", size-len(notes("a", "b"))-1) + "\n"
			sized := func(first, last string) string { return notes(first, last) + pad }
			a, first := bothSides(t, map[string]string{"SIZED.md": sized("a", "b")}, map[string]string{"SIZED.md": sized("a", "B")}, map[string]string{"SIZED.md": sized("A", "b")})
			x := a.fake.MergeInto("task", "main", nil)
			for _, ref := range []string{first, a.fake.Parents(x)[1], x} {
				if n := git(t, a.fake.Repo, "cat-file", "-s", ref+":SIZED.md"); n != strconv.Itoa(size) {
					t.Fatalf("%s bytes at %.8s, want %d", n, ref, size)
				}
			}
			if pure, err := a.pure(first); err != nil || pure != (size <= maxRemergedBytes) {
				t.Fatalf("%d bytes: %v %v", size, pure, err)
			}
		}
	})
}

func gitIn(t *testing.T, dir, stdin string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return string(out)
}

// gitAsGitHub runs git committing as the fake's GitHub, whose commits it
// reports as web-flow's, verified.
func gitAsGitHub(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=GitHub", "GIT_AUTHOR_EMAIL=noreply@github.com",
		"GIT_COMMITTER_NAME=GitHub", "GIT_COMMITTER_EMAIL=noreply@github.com")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

// git's clean three-way merge of two sides is the same whichever is ours:
// edits on both sides of one line, and insertions at neighbouring lines.
func TestACleanMergeOfAFileIsTheSameWhicheverSideIsOurs(t *testing.T) {
	for _, c := range []struct{ base, a, b string }{
		{notes("1", "10"), notes("one", "10"), notes("1", "ten")},
		{"1\n2\n3\n", "1\nA\n2\n3\n", "1\n2\nB\n3\n"},
		{"1\n2\n3\n4\n", "1\n3\n4\n", "1\n2\n3\nB\n4\n"},
		{"x\ny\n", "a\nx\ny\n", "x\ny\nb\n"},
	} {
		ab, cleanAB, err := mergeFile(context.Background(), t.TempDir(), []byte(c.a), []byte(c.base), []byte(c.b))
		if err != nil || !cleanAB {
			t.Fatalf("%q: %v %v", c, cleanAB, err)
		}
		ba, cleanBA, err := mergeFile(context.Background(), t.TempDir(), []byte(c.b), []byte(c.base), []byte(c.a))
		if err != nil || !cleanBA || string(ab) != string(ba) {
			t.Fatalf("%q: %q then %q", c, ab, ba)
		}
	}
}
