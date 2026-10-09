package fakelux

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
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
	for _, path := range slices.Sorted(maps.Keys(files)) {
		run.edits++
		id := fmt.Sprintf("edit_%d", run.edits)
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
		// As lux checks one out: a branch as a local branch (checkout -B,
		// which the reflog records), anything else detached.
		checkout := []string{"-C", dst, "checkout", "-q", "--detach", head(src, repo.Ref)}
		if repo.Ref == "" || exec.Command("git", "-C", src, "rev-parse", "-q", "--verify", "refs/heads/"+repo.Ref).Run() == nil {
			branch := repo.Ref
			if branch == "" {
				branch = "main"
			}
			checkout = []string{"-C", dst, "checkout", "-q", "-B", branch, "origin/" + branch}
		}
		for _, args := range [][]string{{"clone", "-q", "--no-checkout", src, dst}, checkout} {
			if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
				return "", fmt.Errorf("git %s: %v: %s", args[0], err, out)
			}
		}
	}
	run.workspace = dir
	return dir, nil
}

// beforeStop runs the spec's workload.beforeStop hook, as lux does on every
// stop it makes — dude's stop, cancel and pause, and its own timeout — in
// the Run's checkout, where it publishes with lux-shim publish, as on
// lux#77 (with LegacyArtifacts, $LUX_ARTIFACTS is a directory whose files
// are then collected). Reported in the record stream between
// lux.beforeStop start and done, as lux's shim reports it. A Run whose
// checkout was never looked at or written to has nothing to diff, and the
// fake skips the hook for it rather than clone for nothing. Callers hold
// s.mu.
func (s *Server) beforeStop(run *Run) {
	var spec struct {
		Workload struct {
			BeforeStop *lux.BeforeStop `json:"beforeStop"`
		} `json:"workload"`
	}
	_ = json.Unmarshal(run.Spec, &spec)
	hook := spec.Workload.BeforeStop
	if hook == nil || len(hook.Command) == 0 || run.workspace == "" || run.State != "running" {
		return
	}
	s.recordEvent(run, "lux.beforeStop", map[string]any{"phase": "start"})
	timeout := 10 * time.Second
	if d, err := time.ParseDuration(hook.Timeout); err == nil && d > 0 {
		timeout = d
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	args, env, collect := s.publishing(append([]string{hook.Command[0]}, s.inWorkspace(run, hook.Command[1:])...))
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = run.workspace
	out := filepath.Join(run.workspace, ".lux-artifacts")
	if s.LegacyArtifacts {
		_ = os.MkdirAll(out, 0o755)
		defer os.RemoveAll(out)
		env = append(env, "LUX_ARTIFACTS="+out)
	}
	cmd.Env = env
	code := exitCode(cmd.Run(), -1)
	s.recordEvent(run, "lux.beforeStop", map[string]any{"phase": "done", "exitCode": code, "timedOut": ctx.Err() != nil})
	collect(run)
	if !s.LegacyArtifacts {
		return
	}
	_ = filepath.WalkDir(out, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(out, path)
		if run.published == nil {
			run.published = map[string]string{}
		}
		run.published[filepath.ToSlash(rel)] = string(content)
		return nil
	})
}

// inWorkspace reads /workspace in a command's arguments as the Run's
// checkout: the one liberty the fake takes with what runs "in" it.
func (s *Server) inWorkspace(run *Run, args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = strings.ReplaceAll(a, workspaceRoot, run.workspace)
	}
	return out
}

// Edit is a working agent writing files into its checkout now, each with
// the edit tool call OpenCode reports.
func (s *Server) Edit(id string, files map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if run := s.runs[id]; run != nil && run.State == "running" {
		s.edit(run, files)
	}
}

// Timeout stops a Run as lux's own timeout would: the beforeStop hook runs,
// then the Run fails with reason "timeout".
func (s *Server) Timeout(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if run := s.runs[id]; run != nil && run.State == "running" {
		s.beforeStop(run)
		s.setStateWith(run, "failed", "timeout")
	}
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
	run.Calls = append(run.Calls, "exec")
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
	s.mu.Lock()
	args := append([]string{open.Command[0]}, s.inWorkspace(run, open.Command[1:])...)
	ps, fails := s.PS, s.ExecFails
	s.mu.Unlock()
	if args[0] == "ps" {
		// The fake has no processes: what lux's would print is the test's.
		if ps == nil || fails != "" {
			_ = wsjson.Write(ctx, ws, map[string]any{"error": cmp.Or(fails, "ps: no processes in the fake")})
			_ = ws.Close(websocket.StatusNormalClosure, "")
			return
		}
		_ = wsjson.Write(ctx, ws, map[string]any{"data": []byte(ps(run.ID)), "ch": "stdout"})
		_ = wsjson.Write(context.WithoutCancel(ctx), ws, map[string]any{"exitCode": 0})
		_ = ws.Close(websocket.StatusNormalClosure, "")
		return
	}
	args, env, collect := s.publishing(args)
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir, cmd.Env = dir, env
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	code := exitCode(cmd.Run(), 127)
	s.mu.Lock()
	collect(run)
	s.mu.Unlock()
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

// exitCode is how a command run to its end exited: its own code, or
// otherwise (it could not start, or was killed) the one given.
func exitCode(err error, otherwise int) int {
	var ee *exec.ExitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &ee):
		return ee.ExitCode()
	}
	return otherwise
}
