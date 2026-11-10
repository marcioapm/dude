package forge

import (
	"context"
	"net/http/httptest"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
)

// readinessFake is a fake GitHub over a bare repository whose main has one
// commit, with a pull request from feature into main.
func readinessFake(t *testing.T) (*fakegithub.Server, *GitHub, int) {
	t.Helper()
	bare := t.TempDir()
	git := func(args ...string) string {
		out, err := exec.Command("git", append([]string{"-C", bare}, args...)...).Output()
		if err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q", "--bare", "--initial-branch=main")
	git("update-ref", "refs/heads/main", git("-c", "user.name=t", "-c", "user.email=t@t", "commit-tree", git("mktree"), "-m", "Initial"))
	fake := fakegithub.New(bare, "acme/repo")
	n := fake.OpenAsPerson("alice", "feature", "main", "Greet")
	srv := httptest.NewServer(fake.Handler())
	t.Cleanup(srv.Close)
	return fake, NewGitHub(Credential{Secret: "x", APIBaseURL: srv.URL}), n
}

// Readiness reads GitHub's own mergeable_state, the checks at the head,
// whether the head has workflow files when nothing reported, and what
// protection on the base requires.
func TestReadinessReadsWhatTheGateJudges(t *testing.T) {
	ctx := context.Background()
	fake, gh, n := readinessFake(t)
	fake.SetChecks("none")
	f, err := gh.Readiness(ctx, "acme/repo", n)
	if err != nil {
		t.Fatal(err)
	}
	if f.MergeableState != "clean" || f.Checks != ChecksUnknown || !f.WorkflowsKnown || len(f.Workflows) != 0 ||
		!f.ProtectionKnown || len(f.RequiredChecks) != 0 || f.HeadSHA != fake.SHA("feature") {
		t.Fatalf("a repository with no CI: %+v", f)
	}

	fake.CommitFiles("feature", "CI", map[string]string{".github/workflows/ci.yml": "on: push\n", ".github/workflows/README": "x"})
	fake.Set(func(s *fakegithub.Server) {
		s.RequiredChecks = map[string][]string{"main": {"build"}}
		s.RequiredApprovals = map[string]int{"main": 2}
	})
	fake.Review(n, "bob", "APPROVED")
	if f, err = gh.Readiness(ctx, "acme/repo", n); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.Workflows, []string{"ci.yml"}) || !slices.Equal(f.RequiredChecks, []string{"build"}) ||
		f.RequiredApprovals != 2 || f.Approvals != 1 || f.MergeableState != "blocked" {
		t.Fatalf("workflows and protection: %+v", f)
	}

	fake.SetChecks("run:success")
	fake.Set(func(s *fakegithub.Server) { s.MergeableStates = map[int]string{n: "behind"} })
	if f, err = gh.Readiness(ctx, "acme/repo", n); err != nil {
		t.Fatal(err)
	}
	if f.Checks != ChecksPassing || f.Workflows != nil || f.MergeableState != "behind" {
		t.Fatalf("checks reported, behind: %+v", f)
	}
	fake.AdvanceBase("main", "Other work")
	if f, err = gh.Readiness(ctx, "acme/repo", n); err != nil {
		t.Fatal(err)
	}
	if f.BehindBy != 1 {
		t.Fatalf("behind by %d, want 1", f.BehindBy)
	}
}

// A commit's parents, first parent first.
func TestCommitParentsReadsAMergesParentsInOrder(t *testing.T) {
	fake, gh, _ := readinessFake(t)
	fake.CommitFiles("feature", "Work", map[string]string{"a.txt": "a"})
	fake.AdvanceBase("main", "Other")
	head, base := fake.SHA("feature"), fake.SHA("main")
	merge := fake.MergeInto("feature", "main", nil)
	got, err := gh.CommitParents(context.Background(), "acme/repo", merge)
	if err != nil || !slices.Equal(got, []string{head, base}) {
		t.Fatalf("parents %v (%v), want [%s %s]", got, err, head, base)
	}
}
