package fakeagent

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const integrationBrief = "Base integration: this branch conflicts with `main` in target. Call sync_base, then " +
	"`git merge lux/main`. Resolve every conflict keeping both sides' intent."

// The script a real lux runs for an integration fixer, line by line as
// lux-fake would: sync_base through the dude service, then, in the
// repository, a merge of lux/<base> resolved keeping both sides. Its sh
// line is run here against a real conflict, as lux-fake's `sh` runs it.
func TestTheIntegrationScriptMergesTheBaseKeepingBothSides(t *testing.T) {
	if s := IntegrationScript("Fix the typo.", "/workspace/repos/target"); s != "" {
		t.Fatalf("a fix with no integration got a script: %q", s)
	}
	script := IntegrationScript(integrationBrief, "/workspace/repos/target")
	lines := strings.Split(script, "\n")
	if len(lines) != 4 || lines[0] != "http dude POST /tools/sync_base {}" || lines[1] != "cd /workspace/repos/target" ||
		!strings.HasPrefix(lines[2], "sh ") || lines[3] != "echo "+IntegrationReply {
		t.Fatalf("script:\n%s", script)
	}

	work := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", work, "-c", "user.name=Acme", "-c", "user.email=a@example.com"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	write := func(content string) {
		if err := os.WriteFile(filepath.Join(work, "FACTORY.md"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q", "-b", "main")
	write("original\n")
	git("add", ".")
	git("commit", "-qm", "seed")
	git("switch", "-qc", "task")
	write("task intent\n")
	git("commit", "-qam", "task")
	git("switch", "-q", "main")
	write("base intent\n")
	git("commit", "-qam", "base")
	git("update-ref", "refs/remotes/lux/main", "main")
	git("switch", "-q", "task")
	taskHead := git("rev-parse", "HEAD")

	sh := exec.Command("sh", "-c", strings.TrimPrefix(lines[2], "sh "))
	sh.Dir = work
	sh.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Acme", "GIT_AUTHOR_EMAIL=a@example.com",
		"GIT_COMMITTER_NAME=Acme", "GIT_COMMITTER_EMAIL=a@example.com")
	if out, err := sh.CombinedOutput(); err != nil {
		t.Fatalf("resolution: %v %s", err, out)
	}
	content, err := os.ReadFile(filepath.Join(work, "FACTORY.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "task intent") || !strings.Contains(string(content), "base intent") ||
		strings.Contains(string(content), "<<<<<<<") {
		t.Fatalf("resolved file: %q", content)
	}
	if parents := strings.Fields(git("log", "-1", "--format=%P")); len(parents) != 2 || parents[0] != taskHead ||
		parents[1] != git("rev-parse", "lux/main") {
		t.Fatalf("not a merge of lux/main into the task: parents %v", parents)
	}
	if msg := git("log", "-1", "--format=%s"); msg != "Merge remote-tracking branch 'lux/main' into task" {
		t.Fatalf("merge message %q, want git's default", msg)
	}
	if status := git("status", "--porcelain"); status != "" {
		t.Fatalf("left uncommitted: %s", status)
	}
}
