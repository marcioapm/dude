package fakelux

import (
	"errors"
	"os/exec"

	"github.com/marciomartins/dude/orchestrator/internal/fakegit"
)

// The repository reads the fake makes on the repositories Runs check out,
// in process (fakegit) where it can answer as git would; each caller runs
// its git command otherwise. Ref updates, clones, fetches, pushes and
// anything in a Run's checkout stay git's.

// resolved is name's sha in repo as `git rev-parse --verify` gives it, ""
// for a name naming nothing; ok false when fakegit cannot say.
func resolved(repo, name string) (sha string, ok bool) {
	r, opened := fakegit.Open(repo)
	if !opened {
		return "", false
	}
	defer r.Close()
	h, err := r.Resolve(name)
	switch {
	case err == nil:
		return h.String(), true
	case errors.Is(err, fakegit.ErrNotFound):
		return "", true
	}
	return "", false
}

// hasPath is whether `git cat-file -e ref:path` in repo succeeds; ok false
// when fakegit cannot say.
func hasPath(repo, ref, path string) (there, ok bool) {
	r, opened := fakegit.Open(repo)
	if !opened {
		return false, false
	}
	defer r.Close()
	there, err := r.HasPath(ref, path)
	return there, err == nil
}

// commitInProcess is commit's tree and commit, files over base's tree, as
// the agent (agent <a@x>); ok false when fakegit cannot make them.
func commitInProcess(repo, base string, files map[string]string, message string) (string, bool) {
	r, opened := fakegit.Open(repo)
	if !opened {
		return "", false
	}
	defer r.Close()
	tree, err := r.TreeWith(base, files)
	if err != nil {
		return "", false
	}
	sha, err := r.CommitTree(tree.String(), []string{base}, "agent", "a@x", message)
	if err != nil {
		return "", false
	}
	return sha.String(), true
}

// isBranch is whether ref is a branch of repo.
func isBranch(repo, ref string) bool {
	if sha, ok := resolved(repo, "refs/heads/"+ref); ok {
		return sha != ""
	}
	return exec.Command("git", "-C", repo, "rev-parse", "-q", "--verify", "refs/heads/"+ref).Run() == nil
}
