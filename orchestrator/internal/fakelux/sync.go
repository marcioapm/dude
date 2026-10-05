package fakelux

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/marciomartins/dude/orchestrator/internal/fakeagent"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// Sync modes, played on the Run's real checkout (checkout): fast-forward
// moves it only when nothing can be lost, fetch never moves it, and both
// leave the ref's commit at refs/remotes/lux/<ref>. move is applySync's
// own, unchanged.

// syncModeProblem is why a lux without sync modes (NoSyncModes) refuses
// a sync, "" when it takes it. Callers hold s.mu.
func (s *Server) syncModeProblem(sync []lux.SyncRef) string {
	for _, sr := range sync {
		switch sr.Mode {
		case "", lux.SyncMove:
		case lux.SyncFastForward, lux.SyncFetch:
			if s.NoSyncModes {
				return fmt.Sprintf("sync[%s]: this lux does not know mode %q", sr.Repo, sr.Mode)
			}
		default:
			return fmt.Sprintf("sync[%s]: unknown mode %q", sr.Repo, sr.Mode)
		}
	}
	return ""
}

// safeSync plays a fast-forward or fetch sync of one repository into ev,
// git.sync's payload: {mode, status, ahead, behind, dirty?, diverged?,
// from, to}. Callers hold s.mu.
func (s *Server) safeSync(run *Run, spec map[string]any, repo specRepo, sr lux.SyncRef, to string, ev map[string]any) {
	ev["mode"] = sr.Mode
	if to == "" {
		ev["status"], ev["error"] = "failed", "ref not found"
		return
	}
	dir, err := s.checkout(run, spec)
	if err != nil {
		ev["status"], ev["error"] = "failed", err.Error()
		return
	}
	work := filepath.Join(dir, "repos", repo.Name)
	git := func(args ...string) (string, error) {
		c := exec.Command("git", append([]string{"-C", work}, args...)...)
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=lux", "GIT_AUTHOR_EMAIL=lux@x", "GIT_COMMITTER_NAME=lux", "GIT_COMMITTER_EMAIL=lux@x")
		out, err := c.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	count := func(rng string) int {
		out, _ := git("rev-list", "--count", rng)
		n, _ := strconv.Atoi(out)
		return n
	}
	// As lux's bundle brings the ref's history in: every branch of the
	// repository, the ref's among them.
	if out, err := git("fetch", "-q", "origin"); err != nil {
		ev["status"], ev["error"] = "failed", out
		return
	}
	if out, err := git("update-ref", "refs/remotes/lux/"+sr.Ref, to); err != nil {
		ev["status"], ev["error"] = "failed", out
		return
	}
	from, _ := git("rev-parse", "HEAD")
	status, _ := git("status", "--porcelain", "--untracked-files=no")
	ahead, behind := count(to+"..HEAD"), count("HEAD.."+to)
	dirty, diverged := status != "", ahead > 0 && behind > 0
	// As lux: a fast-forward never switches branches. It moves HEAD only
	// when HEAD is the target branch, or detached for a sha target.
	onTarget := false
	if branch, err := git("symbolic-ref", "-q", "HEAD"); err == nil {
		onTarget = branch == "refs/heads/"+sr.Ref
	} else {
		onTarget = strings.HasPrefix(to, sr.Ref)
	}
	ev["from"], ev["to"], ev["ahead"], ev["behind"] = from, to, ahead, behind
	if dirty {
		ev["dirty"] = true
	}
	if diverged {
		ev["diverged"] = true
	}
	switch {
	case ahead == 0 && behind == 0:
		ev["status"] = "up-to-date"
	case sr.Mode == lux.SyncFetch:
		ev["status"] = "fetched"
	case dirty || diverged || !onTarget:
		ev["status"] = lux.SyncKept
	case ahead > 0:
		ev["status"] = lux.SyncAhead
	default:
		if out, err := git("merge", "-q", "--ff-only", to); err != nil {
			ev["status"], ev["error"] = "failed", out
			return
		}
		ev["status"], ev["ahead"], ev["behind"] = "fast-forward", 0, 0
		if run.at == nil {
			run.at = map[string]string{}
		}
		run.at[repo.Name] = to
	}
}

// pushWorkspace pushes the checkout's HEAD to branch in the bare
// repository, as lux pushes what the agent committed; the commit pushed,
// or base untouched when nothing was committed.
func pushWorkspace(work, bare, branch, base string) (string, error) {
	out, err := exec.Command("git", "-C", work, "rev-parse", "HEAD").Output()
	if err != nil {
		return "", err
	}
	sha := strings.TrimSpace(string(out))
	if sha == base {
		return base, nil
	}
	if out, err := exec.Command("git", "-C", work, "push", "-q", "--force", bare, "HEAD:refs/heads/"+branch).CombinedOutput(); err != nil {
		return "", fmt.Errorf("git push: %v: %s", err, out)
	}
	return sha, nil
}

// localCall is a scripted conductor's own work in its checkout
// (fakeagent.Local), reported as the bash tool call an agent makes; what
// went wrong is said in its reply. Callers hold s.mu.
func (s *Server) localCall(run *Run, tool, args string) {
	var in struct {
		Path, Content, Message string
		Args                   []string
	}
	_ = json.Unmarshal([]byte(args), &in)
	var problem string
	switch tool {
	case fakeagent.LocalWrite:
		problem = s.workspaceWrite(run, in.Path, in.Content)
	case fakeagent.LocalCommit:
		if out := s.workspaceGit(run, []string{"add", "-A"}); out != "" {
			problem = out
		}
		s.workspaceGit(run, []string{"commit", "-q", "-m", in.Message})
	case fakeagent.LocalGit:
		s.workspaceGit(run, in.Args)
	}
	run.edits++
	id := fmt.Sprintf("local_%d", run.edits)
	s.agent(run, map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": id, "title": "bash", "kind": "execute",
		"status": "in_progress", "rawInput": map[string]any{"cmd": tool + " " + args}})
	s.agent(run, map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": id, "status": "completed"})
	if problem != "" {
		s.agent(run, map[string]any{"sessionUpdate": "agent_message_chunk",
			"content": map[string]any{"type": "text", "text": tool + " failed: " + problem + "\n"}})
	}
}

// workspaceWrite writes a file in the Run's first checkout; why it could
// not, or "". workspaceGit runs git there and answers what it printed.
// Callers hold s.mu.
func (s *Server) workspaceWrite(run *Run, path, content string) string {
	work, err := s.firstCheckout(run)
	if err != nil {
		return err.Error()
	}
	full := filepath.Join(work, path)
	_ = os.MkdirAll(filepath.Dir(full), 0o755)
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		return err.Error()
	}
	return ""
}

func (s *Server) workspaceGit(run *Run, args []string) string {
	work, err := s.firstCheckout(run)
	if err != nil {
		return err.Error()
	}
	c := exec.Command("git", append([]string{"-C", work, "-c", "user.name=conductor", "-c", "user.email=c@x"}, args...)...)
	out, _ := c.CombinedOutput()
	return strings.TrimSpace(string(out))
}

// Checkout is the Run's checkout of repo, made now if it was not yet:
// what a test looks at, or commits in, as the agent would.
func (s *Server) Checkout(runID, repo string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run := s.runs[runID]
	if run == nil {
		return "", fmt.Errorf("no run %s", runID)
	}
	var spec map[string]any
	_ = json.Unmarshal(run.Spec, &spec)
	dir, err := s.checkout(run, spec)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "repos", repo), nil
}

func (s *Server) firstCheckout(run *Run) (string, error) {
	var spec map[string]any
	_ = json.Unmarshal(run.Spec, &spec)
	repos := specRepos(spec)
	if len(repos) == 0 {
		return "", fmt.Errorf("no repository checked out")
	}
	dir, err := s.checkout(run, spec)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "repos", repos[0].Name), nil
}
