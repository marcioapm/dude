package phases

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// historyRepo is a checkout on main, with origin at its first commit, for
// historyScript to read from "main" (its reflog's first entry).
func historyRepo(t *testing.T) (dir string, git func(args ...string) string) {
	t.Helper()
	root := t.TempDir()
	origin, dir := filepath.Join(root, "origin"), filepath.Join(root, "repo")
	run := func(in string, args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", in, "-c", "user.name=t", "-c", "user.email=t@x"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return string(out)
	}
	_ = os.MkdirAll(origin, 0o755)
	run(origin, "init", "-q", "-b", "main")
	run(origin, "commit", "-q", "--allow-empty", "-m", "base")
	run(root, "clone", "-q", "--no-checkout", origin, dir)
	run(dir, "checkout", "-q", "-B", "main", "origin/main")
	return dir, func(args ...string) string { return run(dir, args...) }
}

// runHistory runs historyScript over repos (name, dir, ref triples) with
// tmp as its TMPDIR and every file it or its children write limited to
// fileLimit bytes (0: none): a write past it kills the writer (SIGXFSZ).
func runHistory(t *testing.T, tmp string, fileLimit int, repos ...string) (string, int) {
	t.Helper()
	limit := "unlimited"
	if fileLimit > 0 {
		// dash counts 512-byte blocks.
		limit = strconv.Itoa(fileLimit / 512)
	}
	args := append([]string{"-c", `ulimit -f ` + limit + ` && exec sh -c "$0" "$@"`, historyScript, "dude-history"}, repos...)
	cmd := exec.Command("sh", args...)
	cmd.Env = append(os.Environ(), "TMPDIR="+tmp)
	var stdout strings.Builder
	cmd.Stdout = &stdout
	err := cmd.Run()
	return stdout.String(), exitCode(err)
}

func exitCode(err error) int {
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode()
	}
	if err != nil {
		return -1
	}
	return 0
}

// A history over historyLimit is cut while git writes it: at most
// historyLimit+1 bytes reach the output, the script exits 5 (over the
// limit), and no file it writes ever grows past the limit, which the
// file-size limit would have killed the writer for (making it exit 4).
func TestAHistoryOverTheLimitIsCutAsItIsRead(t *testing.T) {
	dir, git := historyRepo(t)
	line := strings.Repeat("x", 99) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "big.txt"), []byte(strings.Repeat(line, (2*historyLimit)/len(line))), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "big.txt")
	git("commit", "-q", "-m", "big")
	tmp := t.TempDir()
	out, code := runHistory(t, tmp, historyLimit, "target", dir, "main")
	if code != 5 {
		t.Errorf("exit %d, want 5 (over the limit)", code)
	}
	if len(out) != historyLimit+1 {
		t.Errorf("printed %d bytes, want historyLimit+1 = %d", len(out), historyLimit+1)
	}
	if left, _ := os.ReadDir(tmp); len(left) != 0 {
		t.Errorf("%d files left in TMPDIR", len(left))
	}
}

// Within the limit, every repository's history is printed whole and the
// script exits 0; a checkout, base or log git cannot read exits 4, with
// the readable repositories still printed.
func TestAHistoryWithinTheLimitIsReadWholeAndAnUnreadableOneFails(t *testing.T) {
	dir, git := historyRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "a.txt")
	git("commit", "-q", "-m", "add a")
	tmp := t.TempDir()

	out, code := runHistory(t, tmp, historyLimit, "a", dir, "main", "b", dir, "main")
	if code != 0 || strings.Count(out, "# dude-history ") != 2 || strings.Count(out, "\n+hello") != 2 {
		t.Errorf("exit %d, printed %q", code, out)
	}
	if missing := missingHistory(out, map[string]string{"a": "main", "b": "main"}); len(missing) != 0 {
		t.Errorf("sections missing for %v", missing)
	}

	for name, repos := range map[string][]string{
		"no checkout": {"a", dir, "main", "ghost", filepath.Join(tmp, "absent"), "main"},
		"no base":     {"a", dir, "main", "b", dir, "no-such-ref"},
	} {
		out, code := runHistory(t, tmp, 0, repos...)
		if code != 4 || !strings.Contains(out, "# dude-history a ") {
			t.Errorf("%s: exit %d, printed %q; want 4 and a's section", name, code, out)
		}
	}

	// The log itself failing: the new commit's blob is gone.
	blob := strings.TrimSpace(git("rev-parse", "HEAD:a.txt"))
	if err := os.Remove(filepath.Join(dir, ".git", "objects", blob[:2], blob[2:])); err != nil {
		t.Fatal(err)
	}
	if _, code := runHistory(t, tmp, 0, "a", dir, "main"); code != 4 {
		t.Errorf("a log git cannot read: exit %d, want 4", code)
	}
	if left, _ := os.ReadDir(tmp); len(left) != 0 {
		t.Errorf("%d files left in TMPDIR", len(left))
	}
}
