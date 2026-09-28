package fakelux

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// Exec and the checkout it runs in.
//
// The fake plays agents without containers, so each Run gets a real git
// clone of its repositories, at the commits it checked out, standing in for
// the container's /workspace: made when first needed (an edit, an exec),
// so the many tests that never look at a checkout pay nothing for one.
// An exec runs its command there, with /workspace in its arguments read as
// that directory — the one liberty the fake takes.

// workspaceRoot is the container's /workspace (phases.workspaceDir).
const workspaceRoot = "/workspace"

// edit writes the agent's edits into its checkout, each reported as the
// edit tool call OpenCode makes. Callers hold s.mu.
func (s *Server) edit(run *Run, files map[string]string) {
	if len(files) == 0 {
		return
	}
	var spec map[string]any
	_ = json.Unmarshal(run.Spec, &spec)
	repos := specRepos(spec)
	if len(repos) == 0 {
		return
	}
	dir, err := s.checkout(run, spec)
	if err != nil {
		s.agent(run, map[string]any{"sessionUpdate": "agent_message_chunk",
			"content": map[string]any{"type": "text", "text": "could not check out: " + err.Error() + "\n"}})
		return
	}
	for i, path := range slices.Sorted(maps.Keys(files)) {
		id := fmt.Sprintf("edit_%d", i)
		input := map[string]any{"filePath": path, "content": files[path]}
		s.agent(run, map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": id, "title": "write", "kind": "edit",
			"status": "in_progress", "rawInput": input})
		full := filepath.Join(dir, "repos", repos[0].Name, path)
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		_ = os.WriteFile(full, []byte(files[path]), 0o644)
		s.agent(run, map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": id, "status": "completed"})
	}
}

// checkout makes the Run's /workspace, once: each repository cloned into
// repos/<name> at the commit lux checked out. Callers hold s.mu.
func (s *Server) checkout(run *Run, spec map[string]any) (string, error) {
	if run.workspace != "" {
		return run.workspace, nil
	}
	dir, err := os.MkdirTemp(s.Workspaces, "fakelux-"+run.ID+"-")
	if err != nil {
		return "", err
	}
	for _, repo := range specRepos(spec) {
		src := s.repoPath(repo.URL)
		dst := filepath.Join(dir, "repos", repo.Name)
		for _, args := range [][]string{
			{"clone", "-q", src, dst},
			{"-C", dst, "checkout", "-q", "--detach", head(src, repo.Ref)},
		} {
			if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
				return "", fmt.Errorf("git %s: %v: %s", args[0], err, out)
			}
		}
	}
	run.workspace = dir
	return dir, nil
}

// exec is lux's exec stream: refused over plain HTTP when the Run is not
// running, else a WebSocket whose first message names the command, then its
// output as {"data","ch"} messages, then {"exitCode"}.
func (s *Server) exec(w http.ResponseWriter, r *http.Request) {
	run := s.find(w, r)
	if run == nil {
		return
	}
	s.mu.Lock()
	state := run.State
	var spec map[string]any
	_ = json.Unmarshal(run.Spec, &spec)
	dir, err := "", error(nil)
	if state == "running" {
		dir, err = s.checkout(run, spec)
	}
	s.mu.Unlock()
	switch {
	case state != "running":
		writeErr(w, 409, "not_running", "run is "+state+": interactive access needs it running")
		return
	case err != nil:
		writeErr(w, 500, "internal", err.Error())
		return
	case r.Header.Get("Upgrade") == "":
		writeJSON(w, 200, map[string]string{"status": "ok"})
		return
	}
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer ws.CloseNow()
	ctx := r.Context()
	var open struct {
		Command []string `json:"command"`
	}
	if err := wsjson.Read(ctx, ws, &open); err != nil || len(open.Command) == 0 {
		_ = wsjson.Write(ctx, ws, map[string]any{"error": "exec needs a command"})
		_ = ws.Close(websocket.StatusNormalClosure, "")
		return
	}
	args := make([]string, len(open.Command))
	for i, a := range open.Command {
		args[i] = strings.ReplaceAll(a, workspaceRoot, dir)
	}
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = dir
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	code := 0
	if err := cmd.Run(); err != nil {
		code = 127
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		}
	}
	send := func(ch, text string) {
		for len(text) > 0 {
			n := min(len(text), 64<<10)
			_ = wsjson.Write(ctx, ws, map[string]any{"data": []byte(text[:n]), "ch": ch})
			text = text[n:]
		}
	}
	send("stdout", stdout.String())
	send("stderr", stderr.String())
	_ = wsjson.Write(context.WithoutCancel(ctx), ws, map[string]any{"exitCode": code})
	_ = ws.Close(websocket.StatusNormalClosure, "")
}
