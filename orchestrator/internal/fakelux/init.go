package fakelux

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
)

// workspaceDir is the directory standing in for the Run's /workspace,
// made once and kept across its placements, as lux's state volume is.
// Callers hold s.mu.
func (s *Server) workspaceDir(run *Run) (string, error) {
	if run.root != "" {
		return run.root, nil
	}
	dir, err := os.MkdirTemp(s.Workspaces, "fakelux-"+run.ID+"-")
	if err != nil {
		return "", err
	}
	run.root = dir
	return dir, nil
}

// runInit runs the spec's init.script with /bin/sh -c before the workload,
// on every placement, as lux's shim does (internal/shim/shim.go runInit),
// reported as lux.init start and done. False: it exited non-zero, and the
// placement failed with reason init-failed. Callers hold s.mu.
func (s *Server) runInit(run *Run) bool {
	var spec struct {
		Init *struct {
			Script string `json:"script"`
		} `json:"init"`
	}
	_ = json.Unmarshal(run.Spec, &spec)
	if spec.Init == nil || strings.TrimSpace(spec.Init.Script) == "" {
		return true
	}
	s.recordEvent(run, "lux.init", map[string]any{"phase": "start"})
	dir, err := s.workspaceDir(run)
	code := 1
	if err == nil {
		cmd := exec.Command("/bin/sh", "-c", inWorkspaceScript(spec.Init.Script, dir))
		cmd.Dir = dir
		code = exitCode(cmd.Run(), 1)
	}
	run.inits++
	s.recordEvent(run, "lux.init", map[string]any{"phase": "done", "exitCode": code})
	if code != 0 {
		s.setStateWith(run, "failed", "init-failed")
		return false
	}
	return true
}

// inWorkspaceScript reads /workspace in a script's commands as the Run's
// directory, as inWorkspace does for an exec's arguments; a here-document's
// body is data, and is left exactly as written.
func inWorkspaceScript(script, dir string) string {
	lines := strings.SplitAfter(script, "\n")
	end := ""
	for i, line := range lines {
		bare := strings.TrimRight(line, "\n")
		if end != "" {
			if bare == end {
				end = ""
			}
			continue
		}
		lines[i] = strings.ReplaceAll(line, workspaceRoot, dir)
		if _, word, ok := strings.Cut(bare, "<<"); ok {
			word = strings.TrimPrefix(strings.TrimSpace(word), "-")
			word, _, _ = strings.Cut(strings.TrimSpace(word), " ")
			end = strings.Trim(word, `'"`)
		}
	}
	return strings.Join(lines, "")
}

// Workspace is the directory standing in for a Run's /workspace; "" when
// nothing has made it yet.
func (s *Server) Workspace(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if run := s.runs[id]; run != nil {
		return run.root
	}
	return ""
}

// InitsOf is how many placements of a Run have run its init.
func (s *Server) InitsOf(id string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if run := s.runs[id]; run != nil {
		return run.inits
	}
	return 0
}
