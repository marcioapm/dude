package phases

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
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
	return advanceBranch(context.Background(), a.gh, "acme/target", "task", "main", a.start, a.commit, func() (bool, error) { return true, nil })
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

// Two Runs of one task: Run A recorded attempting /merges for commit C in
// the target repository. Run B publishing the same C finds no attempt of
// its own, so a merge of C on the branch is not B's to adopt.
func TestARunDoesNotTakeAnotherRunsMergeAttemptAsItsOwn(t *testing.T) {
	ctx := context.Background()
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	for _, q := range []string{
		`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_'||$1, $1, 'P', 'prj_'||$1, 'P')`,
		`INSERT INTO tasks (id, organization_id, project_id, number, title, goal) VALUES ('wi_'||$1, $1, 'prj_'||$1, 1, 'T', 'G')`,
		`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, phase, status)
			VALUES ('run_a_'||$1, $1, 'prj_'||$1, 'wi_'||$1, 1, 'fix', 'failed'),
			       ('run_b_'||$1, $1, 'prj_'||$1, 'wi_'||$1, 2, 'fix', 'running')`,
	} {
		if _, err := owner.Exec(ctx, q, org); err != nil {
			t.Fatal(err)
		}
	}
	s := &Syncer{DB: app, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	runOf := func(id string) phaseRun {
		return phaseRun{ID: id + org, Org: org, ProjectID: "prj_" + org, TaskID: "wi_" + org, Phase: "fix"}
	}
	a, b := runOf("run_a_"), runOf("run_b_")
	const commit = "c0ffee0000000000000000000000000000000000"
	if err := app.InOrg(ctx, org, func(tx pgx.Tx) error {
		return s.event(ctx, tx, a, delivery.EvGitMergeAttempted, ledger.ActorAgent, map[string]any{"repo": "target", "commit": commit})
	}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		run  phaseRun
		want bool
	}{{a, true}, {b, false}} {
		got, err := s.mergeAttempted(ctx, c.run, "target", commit)
		if err != nil {
			t.Fatal(err)
		}
		if got != c.want {
			t.Errorf("Run %s attempted the merge of %.8s: %v, want %v", c.run.ID, commit, got, c.want)
		}
	}
}
