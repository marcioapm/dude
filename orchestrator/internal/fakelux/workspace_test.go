package fakelux

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// Only a path's leading /workspace is the container's, and a path in one of
// the caller's own directories is the caller's, even under /workspace.
func TestRewriteWorkspaceReadsOnlyAPathsLeadingWorkspace(t *testing.T) {
	const dir = "/tmp/run-1"
	// An E2E run on a lux Run: its checkout, and so its log directory, is
	// under /workspace.
	logs := "/workspace/repos/dude/.taskdocs/e2e-logs"
	for _, c := range []struct{ in, want string }{
		// The intended rewrite: the container's paths, wherever a command has them.
		{"/workspace", dir},
		{"/workspace/", dir + "/"},
		{"/workspace/repos/x/.taskdocs/notes.md", dir + "/repos/x/.taskdocs/notes.md"},
		{"cd /workspace && ls /workspace/repos", "cd " + dir + " && ls " + dir + "/repos"},
		{"cat > /workspace/f.tmp <<'X'", "cat > " + dir + "/f.tmp <<'X'"},
		{"echo hi >/workspace/out", "echo hi >" + dir + "/out"},
		{"--root=/workspace/repos", "--root=" + dir + "/repos"},
		{"PATH=/usr/bin:/workspace/bin", "PATH=/usr/bin:" + dir + "/bin"},
		{`"/workspace/a b" '/workspace/c'`, `"` + dir + `/a b" '` + dir + `/c'`},
		{"$(ls /workspace)", "$(ls " + dir + ")"},
		// Not the container's /workspace: another path that has it inside.
		{"/srv/workspace/x", "/srv/workspace/x"},
		{"repos/workspace/x", "repos/workspace/x"},
		{"/workspaces/x", "/workspaces/x"},
		{"/workspace.old/x", "/workspace.old/x"},
		{"/workspace-2", "/workspace-2"},
		{"~/workspace", "~/workspace"},
		// The case that broke: the suite's log directory, the host's own.
		{"printenv K > " + logs + "/secrets-1/first.env; echo exit=$? >> " + logs + "/secrets-1/first.env # fakelux-run",
			"printenv K > " + logs + "/secrets-1/first.env; echo exit=$? >> " + logs + "/secrets-1/first.env # fakelux-run"},
		{logs, logs},
		{"/workspace/repos/dude/tests/../.taskdocs/e2e-logs/x", "/workspace/repos/dude/tests/../.taskdocs/e2e-logs/x"},
		// Beside it, the same repository's other paths are still the Run's.
		{"/workspace/repos/dude/.taskdocs/e2e-logsX/y", dir + "/repos/dude/.taskdocs/e2e-logsX/y"},
		{"/workspace/repos/dude/src/main.go > " + logs + "/out",
			dir + "/repos/dude/src/main.go > " + logs + "/out"},
	} {
		if got := rewriteWorkspace(c.in, dir, []string{"", logs + "/"}); got != c.want {
			t.Errorf("rewriteWorkspace(%q)\n got %q\nwant %q", c.in, got, c.want)
		}
	}
	// With no host directories, every leading /workspace is the Run's, as before.
	if got := rewriteWorkspace(logs+"/f", dir, nil); got != dir+"/repos/dude/.taskdocs/e2e-logs/f" {
		t.Errorf("without host directories: %q", got)
	}
}

// A server command run through sh (fakelux-run) writes its /workspace path
// in the Run's checkout, and the caller's files where they are: one whose
// path has workspace inside it, which every occurrence of /workspace being
// rewritten broke, and, where the host has a writable /workspace (a lux
// Run), one in a host directory under /workspace itself.
func TestAServerCommandWritesInTheCheckoutAndInTheHostsDirectories(t *testing.T) {
	inside := filepath.Join(t.TempDir(), "workspace", "logs")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	hostFiles := map[string]string{filepath.Join(inside, "f"): "inside\n"}
	fake := New("", "k", func(map[string]any) Behaviour { return Behaviour{Hang: true} })
	if under, err := os.MkdirTemp(workspaceRoot, ".fakelux-host-"); err == nil {
		t.Cleanup(func() { os.RemoveAll(under) })
		fake.HostDirs = []string{under}
		hostFiles[filepath.Join(under, "f")] = "under\n"
	} else {
		t.Logf("no writable %s here (%v): only the path with workspace inside it", workspaceRoot, err)
	}
	t.Cleanup(fake.Close)
	client := c0(t, fake)
	ctx := context.Background()
	run, err := client.Submit(ctx, lux.Spec{Image: lux.Image{Ref: "agent:1"},
		Workload: lux.Workload{Adapter: "generic", Command: []string{"true"}}}, "")
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, client, run.ID, "running")
	line := "echo run > /workspace/in-run"
	for path, text := range hostFiles {
		line += "; printf '" + text + "' > " + path
	}
	if _, err := client.AddServer(ctx, run.ID, lux.ServerInput{Name: "probe", Port: 4100,
		Command: []string{"sh", "-c", line + " # fakelux-run"}}); err != nil {
		t.Fatal(err)
	}
	read := func(path string) string { b, _ := os.ReadFile(path); return string(b) }
	inRun := func() string { return filepath.Join(fake.Workspace(run.ID), "in-run") }
	written := func() bool {
		for path := range hostFiles {
			if read(path) == "" {
				return false
			}
		}
		return read(inRun()) != ""
	}
	for deadline := time.Now().Add(5 * time.Second); !written() && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
	}
	if got := read(inRun()); got != "run\n" {
		t.Errorf("the checkout's file has %q", got)
	}
	for path, want := range hostFiles {
		if got := read(path); got != want {
			t.Errorf("%s has %q, want %q", path, got, want)
		}
	}
}

// An init script's commands get the same rewrite as an exec's arguments: a
// leading /workspace is the Run's, an embedded one and the caller's own
// directories are left alone, and a here-document's body stays data.
func TestAnInitScriptReadsOnlyALeadingWorkspaceOutsideTheHostsDirectories(t *testing.T) {
	logs := "/workspace/repos/dude/.taskdocs/logs"
	script := "printf x > " + logs + "/out; cat /srv/workspace/x > /workspace/out\n" +
		"cat <<EOF > /workspace/f\n/workspace/data\nEOF\n"
	want := "printf x > " + logs + "/out; cat /srv/workspace/x > /tmp/run/out\n" +
		"cat <<EOF > /tmp/run/f\n/workspace/data\nEOF\n"
	if got := inWorkspaceScript(script, "/tmp/run", []string{logs}); got != want {
		t.Fatalf("inWorkspaceScript:\n got %q\nwant %q", got, want)
	}
}
