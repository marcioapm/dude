package phases

import (
	"context"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"testing"

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
	write(t, filepath.Join(work, "README.md"), "# target\n")
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

func (a *advancing) advance() (advanced, error) {
	return advanceBranch(context.Background(), a.gh, "acme/target", "task", "main", a.start, a.commit)
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
		if !errors.As(err, &fe) || !notFastForward(err) {
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
		var conflict *errBaseMergeConflict
		if !errors.As(err, &conflict) || a.fake.SHA("task") != x {
			t.Fatalf("%v, branch at %.8s; want a base-merge conflict, the branch at %.8s", err, a.fake.SHA("task"), x)
		}
	})
}
