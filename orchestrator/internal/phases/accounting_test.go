package phases

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

type accountingLux struct {
	lux.Client
	dir    string
	stdout string
	code   int
}

func (l accountingLux) Exec(ctx context.Context, _ string, command []string) (lux.ExecResult, error) {
	if l.stdout != "" || l.code != 0 {
		return lux.ExecResult{Stdout: []byte(l.stdout), ExitCode: l.code}, nil
	}
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	for i, arg := range cmd.Args {
		cmd.Args[i] = strings.Replace(arg, "/workspace/repos", l.dir, 1)
	}
	cmd.Dir = l.dir
	out, err := cmd.Output()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		return lux.ExecResult{}, err
	}
	return lux.ExecResult{Stdout: out, ExitCode: code}, nil
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=Acme", "-c", "user.email=a@example.com"}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPublicationAccountingExcludesImportedBaseButKeepsTaskEdits(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "target")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "init", "-q", "-b", "main")
	write(t, filepath.Join(repo, "old.txt"), "old\n")
	write(t, filepath.Join(repo, "tool.sh"), "echo tool\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-qm", "seed")
	start := git(t, repo, "rev-parse", "HEAD")
	git(t, repo, "branch", "base")
	git(t, repo, "mv", "old.txt", "renamed.txt")
	write(t, filepath.Join(repo, "image.bin"), "\x00\x01")
	if err := os.Chmod(filepath.Join(repo, "tool.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", "-A")
	git(t, repo, "commit", "-qm", "task edits")
	git(t, repo, "switch", "-q", "base")
	var large strings.Builder
	for i := range 500 {
		large.WriteString(strings.Repeat("x", i%10) + "\n")
	}
	write(t, filepath.Join(repo, "base.txt"), large.String())
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-qm", "base import")
	git(t, repo, "switch", "-q", "main")
	git(t, repo, "merge", "--no-edit", "base")
	head := git(t, repo, "rev-parse", "HEAD")
	base := git(t, repo, "rev-parse", "base")
	paths, lines, err := publicationAccounting(context.Background(), accountingLux{dir: root}, "run", start, head, base, "target")
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(paths, ",")
	if strings.Contains(got, "base.txt") || !strings.Contains(got, "renamed.txt") || !strings.Contains(got, "image.bin") ||
		!strings.Contains(got, "tool.sh") || lines > 10 {
		t.Fatalf("paths %v lines %d", paths, lines)
	}
}

func TestPublicationAccountingFailsClosed(t *testing.T) {
	for name, l := range map[string]accountingLux{
		"missing evidence": {stdout: "\n"},
		"truncated":        {stdout: "DUDE_ACCOUNTING\ta\tb\tc\n1\t0\tfile\n"},
		"bad line":         {stdout: "DUDE_ACCOUNTING\ta\tb\tc\n1\tfile\nDUDE_END\tb\n"},
		"failed exec":      {stdout: "partial", code: 1},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := publicationAccounting(context.Background(), l, "run", "a", "b", "c", "target"); err == nil {
				t.Fatal("unverified evidence accepted")
			}
		})
	}
}
