package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marciomartins/dude/runner/internal/client"
)

/*
The push is where phases hand work to each other, and it broke twice in ways
the unit tests could not see — so these drive real git against a real bare
remote rather than asserting on the command line.

The situation that matters is the one every phase after the first is in: a
fresh clone of the node's mirror, pushing to a branch the clone has never
tracked.
*/

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// A bare remote with one commit on main, and a clone of it to work in.
func pushFixture(t *testing.T) (remote, work string) {
	t.Helper()
	root := t.TempDir()
	remote = filepath.Join(root, "remote.git")
	gitIn(t, root, "init", "-q", "--bare", "--initial-branch=main", remote)

	seed := filepath.Join(root, "seed")
	gitIn(t, root, "clone", "-q", remote, seed)
	os.WriteFile(filepath.Join(seed, "f"), []byte("one\n"), 0o644)
	gitIn(t, seed, "add", ".")
	gitIn(t, seed, "commit", "-q", "-m", "one")
	gitIn(t, seed, "push", "-q", "origin", "HEAD:refs/heads/main")

	return remote, seed
}

// A fresh clone, as every phase after the first gets.
func freshClone(t *testing.T, remote string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "clone")
	gitIn(t, filepath.Dir(dir), "clone", "-q", remote, dir)
	return dir
}

func commitIn(t *testing.T, dir, content string) string {
	t.Helper()
	os.WriteFile(filepath.Join(dir, "f"), []byte(content), 0o644)
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "-q", "-m", content)
	return gitIn(t, dir, "rev-parse", "HEAD")
}

var noCredential = &client.PushCredential{Username: "x", Token: ""}

func TestFirstPushCreatesTheBranch(t *testing.T) {
	remote, work := pushFixture(t)
	commitIn(t, work, "implement\n")

	// Empty expectation: the implement phase is creating the branch.
	if err := pushBranch(context.Background(), work, remote, noCredential, "dude/wi/attempt-1", ""); err != nil {
		t.Fatalf("first push: %v", err)
	}
}

// The case that failed in production: a later phase, in a fresh clone that
// has never tracked the branch, building on what the previous phase pushed.
func TestLaterPhaseCanReplaceWhatItBuiltOn(t *testing.T) {
	remote, work := pushFixture(t)
	implemented := commitIn(t, work, "implement\n")
	branch := "dude/wi/attempt-1"
	if err := pushBranch(context.Background(), work, remote, noCredential, branch, ""); err != nil {
		t.Fatalf("implement push: %v", err)
	}

	fix := freshClone(t, remote)
	gitIn(t, fix, "fetch", "-q", "origin", "refs/heads/"+branch)
	gitIn(t, fix, "checkout", "-q", implemented)
	commitIn(t, fix, "fix\n")

	if err := pushBranch(context.Background(), fix, remote, noCredential, branch, implemented); err != nil {
		t.Fatalf("a phase could not push over the commit it was given: %v", err)
	}
}

// The property the lease exists for: a phase must not silently discard a
// commit someone else pushed to the branch while it was running.
func TestPhaseDoesNotOverwriteSomeoneElsesCommit(t *testing.T) {
	remote, work := pushFixture(t)
	implemented := commitIn(t, work, "implement\n")
	branch := "dude/wi/attempt-1"
	pushBranch(context.Background(), work, remote, noCredential, branch, "")

	// A person pushes to the branch after the phase started.
	human := freshClone(t, remote)
	gitIn(t, human, "fetch", "-q", "origin", "refs/heads/"+branch)
	gitIn(t, human, "checkout", "-q", "FETCH_HEAD")
	commitIn(t, human, "human\n")
	gitIn(t, human, "push", "-q", "origin", "HEAD:refs/heads/"+branch)

	// The phase, still believing the branch is where it started.
	phase := freshClone(t, remote)
	gitIn(t, phase, "fetch", "-q", "origin", "refs/heads/"+branch)
	gitIn(t, phase, "checkout", "-q", implemented)
	commitIn(t, phase, "phase\n")

	err := pushBranch(context.Background(), phase, remote, noCredential, branch, implemented)
	if err == nil {
		t.Fatal("the phase overwrote a commit it never saw")
	}
}

// Creating a branch that already exists means two work items collided, or a
// retry lost track of its state. Either way it must not overwrite.
func TestFirstPushRefusesAnExistingBranch(t *testing.T) {
	remote, work := pushFixture(t)
	commitIn(t, work, "someone\n")
	gitIn(t, work, "push", "-q", "origin", "HEAD:refs/heads/dude/wi/attempt-1")

	other := freshClone(t, remote)
	commitIn(t, other, "implement\n")
	err := pushBranch(context.Background(), other, remote, noCredential, "dude/wi/attempt-1", "")
	if err == nil {
		t.Fatal("a first push replaced a branch that already existed")
	}
}

func TestTokenIsRedactedFromErrors(t *testing.T) {
	// Git echoes the remote URL on failure, and that URL carries the token.
	msg := redact("fatal: https://x:ghp_secret@github.com/a/b.git not found", "ghp_secret")
	if strings.Contains(msg, "ghp_secret") {
		t.Errorf("token leaked into %q", msg)
	}
}
