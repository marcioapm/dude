package fakelux

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// A Run's checkpoints, as lux keeps them: at every end of a placement,
// per repository of its spec, the checkout's committed HEAD, whether it
// had uncommitted work (which is not kept), and the branch it was on.
// "Uploaded" here is the commit fetched into the repository's bare
// repository under refs/checkpoints/<run>/<repo>, so a Run cloned from it
// has its objects. They outlive the Run's terminate, as lux's do.

// checkpoint is one repository's, with where it came from.
type checkpoint struct {
	lux.Checkpoint
	// The source repository's clone URL in the Run's spec.
	url string
}

// CheckpointNotUploaded makes the Run's checkpoints, from now on, recorded
// but never uploaded: listed with uploaded false, and not cloned from.
func (s *Server) CheckpointNotUploaded(runID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.notUploaded == nil {
		s.notUploaded = map[string]bool{}
	}
	s.notUploaded[runID] = true
}

// recordCheckpoints keeps the Run's checkpoint of each repository its spec
// names, as its placement ends; nothing with NoCheckpoints. A checkout
// never made is where its clone left it. Callers hold s.mu.
func (s *Server) recordCheckpoints(run *Run) {
	if s.NoCheckpoints {
		return
	}
	var spec map[string]any
	_ = json.Unmarshal(run.Spec, &spec)
	for _, repo := range specRepos(spec) {
		cp := checkpoint{Checkpoint: lux.Checkpoint{Repo: repo.Name}, url: repo.URL}
		bare := s.repoPath(repo.URL)
		work := ""
		if run.workspace != "" {
			work = filepath.Join(run.workspace, "repos", repo.Name)
		}
		if work != "" {
			git := func(args ...string) string {
				out, _ := exec.Command("git", append([]string{"-C", work}, args...)...).Output()
				return strings.TrimSpace(string(out))
			}
			cp.Head = git("rev-parse", "HEAD")
			cp.Dirty = git("status", "--porcelain") != ""
			cp.Branch = git("symbolic-ref", "-q", "--short", "HEAD")
		}
		if cp.Head == "" {
			cp.Head = run.at[repo.Name]
		}
		if cp.Head == "" {
			continue
		}
		if !s.notUploaded[run.ID] {
			ref := "refs/checkpoints/" + run.ID + "/" + repo.Name
			var err error
			if work != "" {
				_, err = exec.Command("git", "-C", work, "push", "-q", "--force", bare, "HEAD:"+ref).CombinedOutput()
			} else {
				_, err = exec.Command("git", "-C", bare, "update-ref", ref, cp.Head).CombinedOutput()
			}
			if err == nil {
				cp.Uploaded = true
			}
		}
		if run.checkpoints == nil {
			run.checkpoints = map[string]checkpoint{}
		}
		run.checkpoints[repo.Name] = cp
	}
}

// listCheckpoints is GET /v1/runs/{id}/checkpoints.
func (s *Server) listCheckpoints(w http.ResponseWriter, r *http.Request) {
	run := s.find(w, r)
	if run == nil {
		return
	}
	s.mu.Lock()
	var spec map[string]any
	_ = json.Unmarshal(run.Spec, &spec)
	list := []lux.Checkpoint{}
	for _, repo := range specRepos(spec) {
		if cp, ok := run.checkpoints[repo.Name]; ok {
			list = append(list, cp.Checkpoint)
		}
	}
	s.mu.Unlock()
	writeJSON(w, 200, map[string]any{"checkpoints": list})
}

// fromCheckpoints fills in, for each repository of a submitted spec that
// is cloned from a checkpoint, the URL and commit lux clones it at, as
// lux resolves them: refused when the Run or its checkpoint is unknown or
// was never uploaded. Callers hold s.mu.
func (s *Server) fromCheckpoints(raw json.RawMessage) (json.RawMessage, string) {
	var spec map[string]any
	if json.Unmarshal(raw, &spec) != nil {
		return raw, ""
	}
	git, _ := spec["git"].(map[string]any)
	list, _ := git["repositories"].([]any)
	changed := false
	for i, item := range list {
		repo, _ := item.(map[string]any)
		from, _ := repo["from"].(map[string]any)
		if from == nil {
			continue
		}
		runID, _ := from["run"].(string)
		name, _ := from["repo"].(string)
		src := s.runs[runID]
		if src == nil {
			return raw, fmt.Sprintf("git.repositories[%d].from: no run %q", i, runID)
		}
		cp, ok := src.checkpoints[name]
		if !ok {
			return raw, fmt.Sprintf("git.repositories[%d].from: run %s has no checkpoint of %q", i, runID, name)
		}
		if !cp.Uploaded {
			return raw, fmt.Sprintf("git.repositories[%d].from: the checkpoint of %q is not uploaded", i, name)
		}
		repo["url"], repo["ref"] = cp.url, cp.Head
		changed = true
	}
	if !changed {
		return raw, ""
	}
	out, err := json.Marshal(spec)
	if err != nil {
		return raw, err.Error()
	}
	return out, ""
}
