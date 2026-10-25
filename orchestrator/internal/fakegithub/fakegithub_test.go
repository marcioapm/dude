package fakegithub_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

func contractGit(t *testing.T, repo string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	cmd.Env = append(cmd.Environ(), "GIT_AUTHOR_NAME=Alice", "GIT_AUTHOR_EMAIL=alice@example.com",
		"GIT_COMMITTER_NAME=Alice", "GIT_COMMITTER_EMAIL=alice@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func branchContract(t *testing.T) (*fakegithub.Server, *forge.GitHub, int) {
	t.Helper()
	repo := t.TempDir()
	contractGit(t, repo, "init", "--bare", "--initial-branch=main")
	tree := contractGit(t, repo, "mktree")
	initial := contractGit(t, repo, "commit-tree", tree, "-m", "Initial")
	contractGit(t, repo, "update-ref", "refs/heads/main", initial)
	fake := fakegithub.New(repo, "acme/repo")
	number := fake.OpenAsPerson("alice", "topic", "main", "Topic")
	if fake.SHA("topic") == "" {
		t.Fatal("topic branch was not created")
	}
	srv := httptest.NewServer(fake.Handler())
	t.Cleanup(srv.Close)
	return fake, forge.NewGitHub(forge.Credential{APIBaseURL: srv.URL}), number
}

func contractCommit(t *testing.T, fake *fakegithub.Server, branch string, files map[string]string) string {
	t.Helper()
	sha := fake.CommitFiles(branch, "Change "+branch, files)
	if sha == "" {
		t.Fatalf("commit files on %s failed", branch)
	}
	return sha
}

func contractParents(t *testing.T, fake *fakegithub.Server, sha string, want []string) {
	t.Helper()
	if got := fake.Parents(sha); !slices.Equal(got, want) {
		t.Fatalf("parents = %v, want %v", got, want)
	}
}

func contractStatus(t *testing.T, err error, status int) {
	t.Helper()
	var apiErr *forge.Error
	if !errors.As(err, &apiErr) || apiErr.Status != status {
		t.Fatalf("error = %v, want HTTP %d", err, status)
	}
}

func TestUpdateBranchExpectedHeadContract(t *testing.T) {
	for _, guarded := range []bool{true, false} {
		name := "without_expected_head"
		if guarded {
			name = "matching_expected_head"
		}
		t.Run(name, func(t *testing.T) {
			fake, client, number := branchContract(t)
			head := contractCommit(t, fake, "topic", map[string]string{"topic.txt": "topic change\n"})
			base := contractCommit(t, fake, "main", map[string]string{"base.txt": "base change\n"})
			expected := ""
			if guarded {
				expected = head
			}
			if err := client.UpdateBranch(context.Background(), fake.Slug, number, expected); err != nil {
				t.Fatal(err)
			}
			merged := fake.SHA("topic")
			contractParents(t, fake, merged, []string{head, base})
			for path, want := range map[string]string{"topic.txt": "topic change", "base.txt": "base change"} {
				if got := contractGit(t, fake.Repo, "show", merged+":"+path); got != want {
					t.Fatalf("%s = %q, want %q", path, got, want)
				}
			}
			if got := fake.SHA("main"); got != base {
				t.Fatalf("base moved to %s, want %s", got, base)
			}
		})
	}
	t.Run("stale_expected_head", func(t *testing.T) {
		fake, client, number := branchContract(t)
		stale := fake.SHA("topic")
		head := contractCommit(t, fake, "topic", map[string]string{"topic.txt": "new head\n"})
		base := contractCommit(t, fake, "main", map[string]string{"base.txt": "base change\n"})
		contractStatus(t, client.UpdateBranch(context.Background(), fake.Slug, number, stale), http.StatusUnprocessableEntity)
		if got := fake.SHA("topic"); got != head {
			t.Fatalf("stale update moved topic to %s, want %s", got, head)
		}
		if got := fake.SHA("main"); got != base {
			t.Fatalf("stale update moved main to %s, want %s", got, base)
		}
	})
}

func TestUpdateBranchTreeConflictContract(t *testing.T) {
	fake, client, number := branchContract(t)
	head := contractCommit(t, fake, "topic", map[string]string{"shared.txt": "topic version\n"})
	base := contractCommit(t, fake, "main", map[string]string{"shared.txt": "base version\n"})
	// No Conflicting override: git must detect the incompatible file additions.
	contractStatus(t, client.UpdateBranch(context.Background(), fake.Slug, number, head), http.StatusUnprocessableEntity)
	if got := fake.SHA("topic"); got != head {
		t.Fatalf("conflicting update moved topic to %s, want %s", got, head)
	}
	if got := fake.SHA("main"); got != base {
		t.Fatalf("conflicting update moved main to %s, want %s", got, base)
	}
}

func TestMergesParentOrderContract(t *testing.T) {
	fake, client, _ := branchContract(t)
	branch := contractCommit(t, fake, "topic", map[string]string{"topic.txt": "topic change\n"})
	incoming := contractCommit(t, fake, "main", map[string]string{"base.txt": "base change\n"})
	merged, err := client.MergeCommit(context.Background(), fake.Slug, "topic", incoming)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{branch, incoming}
	if !slices.Equal(merged.Parents, want) {
		t.Fatalf("response parents = %v, want %v", merged.Parents, want)
	}
	if got := fake.SHA("topic"); got != merged.SHA || merged.SHA == "" {
		t.Fatalf("topic = %s, response SHA = %s", got, merged.SHA)
	}
	contractParents(t, fake, merged.SHA, want)
	// GitHub commits a /merges merge as web-flow, signed and verified.
	if p, err := client.CommitProvenance(context.Background(), fake.Slug, merged.SHA); err != nil || p.Login != "web-flow" || !p.Verified {
		t.Fatalf("merge provenance = %+v (%v), want web-flow, verified", p, err)
	}
	for path, want := range map[string]string{"topic.txt": "topic change", "base.txt": "base change"} {
		if got := contractGit(t, fake.Repo, "show", merged.SHA+":"+path); got != want {
			t.Fatalf("%s = %q, want %q", path, got, want)
		}
	}
	if got := fake.SHA("main"); got != incoming {
		t.Fatalf("incoming branch moved to %s, want %s", got, incoming)
	}
}

func TestAFileIsReadRawAtACommit(t *testing.T) {
	fake, client, _ := branchContract(t)
	ctx := context.Background()
	first := contractCommit(t, fake, "topic", map[string]string{"docs/a note #1.md": "one\n"})
	second := contractCommit(t, fake, "topic", map[string]string{"docs/a note #1.md": "two\n"})
	for ref, want := range map[string]string{first: "one\n", second: "two\n"} {
		got, err := client.FileAt(ctx, "acme/repo", "docs/a note #1.md", ref, 1<<20)
		if err != nil || string(got) != want {
			t.Fatalf("at %.8s: %q %v, want %q", ref, got, err, want)
		}
	}
	_, err := client.FileAt(ctx, "acme/repo", "docs/absent.md", second, 1<<20)
	contractStatus(t, err, 404)
	_, err = client.FileAt(ctx, "acme/repo", "docs/a note #1.md", fake.SHA("main"), 1<<20)
	contractStatus(t, err, 404)
	_, err = client.FileAt(ctx, "acme/repo", "docs/a note #1.md", second, 3)
	var apiErr *forge.Error
	if !errors.As(err, &apiErr) || apiErr.Refusal != forge.RefusalTooLarge {
		t.Fatalf("%v, want refused as too large", err)
	}
}
