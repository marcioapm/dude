package agenttools_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/agenttools"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// localLux runs a Run's execs with sh here, /workspace read as root: its
// container, its checkouts real git repositories.
type localLux struct {
	lux.Client
	root string
}

func (l *localLux) Exec(ctx context.Context, _ string, command []string) (lux.ExecResult, error) {
	args := make([]string, len(command))
	for i, a := range command {
		args[i] = strings.ReplaceAll(a, "/workspace", l.root)
	}
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	code := 0
	var ee *exec.ExitError
	if err := cmd.Run(); errors.As(err, &ee) {
		code = ee.ExitCode()
	} else if err != nil {
		return lux.ExecResult{}, err
	}
	return lux.ExecResult{Stdout: []byte(stdout.String()), Stderr: []byte(stderr.String()), ExitCode: code}, nil
}

// runCallsWorld is a conductor and a running implementer Run whose
// checkout "api" has two commits: first, made an hour ago, then head, made
// now; the Run made calls before first, between the two and after head.
type runCallsWorld struct {
	*fixture
	token       string
	first, head string
}

func newRunCallsWorld(t *testing.T) *runCallsWorld {
	t.Helper()
	root := t.TempDir()
	f := setupWith(t, &agenttools.Server{Lux: &localLux{root: root}})
	w := &runCallsWorld{fixture: f}
	dir := filepath.Join(root, "repos", "api")
	git := func(env []string, args ...string) string {
		t.Helper()
		c := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=a", "-c", "user.email=a@x"}, args...)...)
		c.Env = append(os.Environ(), env...)
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	git(nil, "init", "-q", "-b", "main")
	hourAgo := time.Now().Add(-time.Hour).Format(time.RFC3339)
	git([]string{"GIT_COMMITTER_DATE=" + hourAgo}, "commit", "-q", "--allow-empty", "-m", "first")
	w.first = git(nil, "rev-parse", "HEAD")
	git(nil, "commit", "-q", "--allow-empty", "-m", "head")
	w.head = git(nil, "rev-parse", "HEAD")

	w.token = f.run(t, "run_conductor", "conductor", "running")
	mustExec(t, f.owner, `UPDATE runs SET phase = NULL WHERE id = 'run_conductor'`)
	f.run(t, "run_writer", "implementer", "running")
	mustExec(t, f.owner, `UPDATE runs SET lux_run_id = 'lrun_w', lux_state = 'running', lux_pushes = ARRAY['api'] WHERE id = 'run_writer'`)
	at := map[string]time.Duration{"before first": -2 * time.Hour, "between": -30 * time.Minute, "after head": time.Minute}
	for i, c := range []struct{ when, command string }{
		{"before first", "make test # before first"},
		{"between", "make test # between"},
		{"after head", "make lint"},
		{"after head", "npm test --silent"},
	} {
		id := "call_" + string(rune('a'+i))
		mustExec(t, f.owner, `INSERT INTO events (id, organization_id, event_type, occurred_at, run_id, task_id, actor_type, actor_id, source, payload) VALUES
			($1 || '_c', $2, 'agent.tool.called', now() + $3::interval, 'run_writer', $4, 'agent', 'run_writer', 'agent', jsonb_build_object('callId', $1::text, 'input', jsonb_build_object('command', $5::text))),
			($1 || '_d', $2, 'agent.tool.completed', now() + $3::interval, 'run_writer', $4, 'agent', 'run_writer', 'agent', jsonb_build_object('callId', $1::text, 'exitCode', 0))`,
			id, f.org, at[c.when].String(), f.item, c.command)
	}
	return w
}

// calls are run_calls' answer's commands, unquoted, and its commits.
func (w *runCallsWorld) calls(t *testing.T, body string) (commands []string, commits []string, clean any) {
	t.Helper()
	status, out := w.post(t, w.token, "run_calls", body)
	if status != 200 {
		t.Fatalf("run_calls %s: %d %v", body, status, out)
	}
	list, _ := out["calls"].([]any)
	for _, c := range list {
		m := c.(map[string]any)
		commands = append(commands, delivery.Unquoted(m["command"].(string)))
	}
	cs, _ := out["commits"].([]any)
	for _, c := range cs {
		commits = append(commits, c.(map[string]any)["sha"].(string))
	}
	return commands, commits, out["treeClean"]
}

// By default run_calls reads the latest writer Run's HEAD: the calls
// started after it was made, newest first, and whether its tree is clean.
func TestRunCallsDefaultsToTheWritersHead(t *testing.T) {
	w := newRunCallsWorld(t)
	got, commits, clean := w.calls(t, `{}`)
	if strings.Join(got, "|") != "npm test --silent|make lint" || len(commits) != 1 || commits[0] != w.head || clean != true {
		t.Fatalf("calls %q, commits %v, clean %v", got, commits, clean)
	}
}

// An explicit commit lists the calls started after it was made, the Run
// named or not.
func TestRunCallsAtAnExplicitCommit(t *testing.T) {
	w := newRunCallsWorld(t)
	got, commits, clean := w.calls(t, `{"run":"run_writer","commit":"`+w.first[:12]+`"}`)
	if strings.Join(got, "|") != "npm test --silent|make lint|make test # between" || len(commits) != 1 || commits[0] != w.first || clean != nil {
		t.Fatalf("calls %q, commits %v, clean %v", got, commits, clean)
	}
	if status, out := w.post(t, w.token, "run_calls", `{"commit":"`+strings.Repeat("e", 40)+`"}`); status == 200 {
		t.Fatalf("a commit the checkout lacks: %v", out)
	}
}

// filter keeps the calls whose command holds it, any case.
func TestRunCallsFilter(t *testing.T) {
	w := newRunCallsWorld(t)
	if got, _, _ := w.calls(t, `{"filter":"NPM"}`); strings.Join(got, "|") != "npm test --silent" {
		t.Fatalf("calls %q", got)
	}
	if got, _, _ := w.calls(t, `{"commit":"`+w.first+`","filter":"make test"}`); strings.Join(got, "|") != "make test # between" {
		t.Fatalf("calls %q", got)
	}
}

// Only a writer Run of the task's attempt, and only the conductor asks.
func TestRunCallsRefusesOthers(t *testing.T) {
	w := newRunCallsWorld(t)
	if status, _ := w.post(t, w.token, "run_calls", `{"run":"run_conductor"}`); status == 200 {
		t.Fatal("listed the conductor's own Run")
	}
	writer := w.tokenForRun(t, "run_writer")
	if status, _ := w.post(t, writer, "run_calls", `{}`); status != 404 {
		t.Fatalf("a writer's run_calls=%d", status)
	}
}
