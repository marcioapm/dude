package fakelux

import (
	"os"
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
