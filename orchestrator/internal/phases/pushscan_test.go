package phases

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/redact"
)

// historyRepo is a checkout on main, with origin at its first commit, for
// historyScript to read from "main" (its reflog's first entry).
func historyRepo(t *testing.T) (dir string, git func(args ...string) string) {
	t.Helper()
	dir, _, git, _ = historyRepoWithOrigin(t)
	return dir, git
}

// historyRepoWithOrigin is historyRepo with its origin, the base branch on
// GitHub, and git run there.
func historyRepoWithOrigin(t *testing.T) (dir, originDir string, git, inOrigin func(args ...string) string) {
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
	return dir, origin, func(args ...string) string { return run(dir, args...) },
		func(args ...string) string { return run(origin, args...) }
}

// runHistory runs historyScript over repos (name, dir, ref, trusted base
// quadruples) with tmp as its TMPDIR and every file it or its children
// write limited to fileLimit bytes (0: none): a write past it kills the
// writer (SIGXFSZ).
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

// writeCommit writes content to file in dir and commits it with git.
func writeCommit(t *testing.T, dir string, git func(args ...string) string, file, content, message string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", file)
	git("commit", "-q", "-m", message)
}

// A history over historyLimit is cut while git writes it: at most
// historyLimit+1 bytes reach the output, the script exits 5 (over the
// limit), and no file it writes ever grows past the limit, which the
// file-size limit would have killed the writer for (making it exit 4).
func TestAHistoryOverTheLimitIsCutAsItIsRead(t *testing.T) {
	dir, git := historyRepo(t)
	line := strings.Repeat("x", 99) + "\n"
	writeCommit(t, dir, git, "big.txt", strings.Repeat(line, (2*historyLimit)/len(line)), "big")
	tmp := t.TempDir()
	out, code := runHistory(t, tmp, historyLimit, "target", dir, "main", "")
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
	writeCommit(t, dir, git, "a.txt", "hello\n", "add a")
	tmp := t.TempDir()

	out, code := runHistory(t, tmp, historyLimit, "a", dir, "main", "", "b", dir, "main", "")
	if code != 0 || strings.Count(out, "# dude-history ") != 2 || strings.Count(out, "\n+hello") != 2 {
		t.Errorf("exit %d, printed %q", code, out)
	}
	if missing := missingHistory(out, map[string]string{"a": "main", "b": "main"}); len(missing) != 0 {
		t.Errorf("sections missing for %v", missing)
	}

	for name, repos := range map[string][]string{
		"no checkout": {"a", dir, "main", "", "ghost", filepath.Join(tmp, "absent"), "main", ""},
		"no base":     {"a", dir, "main", "", "b", dir, "no-such-ref", ""},
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
	if _, code := runHistory(t, tmp, 0, "a", dir, "main", ""); code != 4 {
		t.Errorf("a log git cannot read: exit %d, want 4", code)
	}
	if left, _ := os.ReadDir(tmp); len(left) != 0 {
		t.Errorf("%d files left in TMPDIR", len(left))
	}
}

// scanToken is the secret the history tests look for.
const scanToken = "tok_9f2c41d7a8e3b6055cd1"

// scannedHistory is what scanHistory finds of scanToken in out.
func scannedHistory(out string) []SecretInWork {
	var found hits
	found.scanHistory(redact.New(map[string]string{"TOKEN": scanToken}), out)
	return found
}

// mergedBase is a checkout whose task work merged in a base delta larger
// than historyLimit, fetched as lux fetches it (refs/remotes/lux/main), the
// merge resolving a conflict on conflict.txt to resolution. before is a
// task commit made before the merge; trusted, the base lux reported
// fetching.
func mergedBase(t *testing.T, before, resolution string) (dir, trusted string, git func(args ...string) string) {
	t.Helper()
	dir, origin, git, inOrigin := historyRepoWithOrigin(t)
	writeCommit(t, dir, git, "conflict.txt", "task side\n", "Task side")
	writeCommit(t, dir, git, "task.txt", before, "Task work")
	line := strings.Repeat("b", 99) + "\n"
	writeCommit(t, origin, inOrigin, "base.txt", strings.Repeat(line, (historyLimit+historyLimit/2)/len(line)), "Base delta")
	writeCommit(t, origin, inOrigin, "conflict.txt", "base side\n", "Base side")
	trusted = fetchAsLux(git, inOrigin)
	if out, err := exec.Command("git", "-C", dir, "-c", "user.name=t", "-c", "user.email=t@x", "merge", "-q", "lux/main").CombinedOutput(); err == nil {
		t.Fatalf("the merge did not conflict: %s", out)
	}
	writeCommit(t, dir, git, "conflict.txt", resolution, "Merge lux/main")
	return dir, trusted, git
}

// fetchAsLux fetches origin's main into refs/remotes/lux/main, as lux
// does, and returns the commit fetched: the trusted base.
func fetchAsLux(git, inOrigin func(args ...string) string) string {
	trusted := strings.TrimSpace(inOrigin("rev-parse", "HEAD"))
	git("fetch", "-q", "origin", "+main:refs/remotes/lux/main")
	return trusted
}

// A base delta larger than historyLimit, merged under a trusted base, is
// not read: the scan covers the task's commits and the merge's resolution
// only, and exits 0.
func TestABaseDeltaOverTheLimitMergedUnderATrustedBaseIsLeftOut(t *testing.T) {
	dir, trusted, _ := mergedBase(t, "task\n", "task side\nbase side\n")
	out, code := runHistory(t, t.TempDir(), 0, "target", dir, "main", trusted)
	if code != 0 {
		t.Fatalf("exit %d, want 0; printed %d bytes", code, len(out))
	}
	if len(out) > 64<<10 {
		t.Errorf("printed %d bytes: the base delta was read", len(out))
	}
	if strings.Contains(out, "+++ b/base.txt") {
		t.Error("base.txt, the base's own file, was read")
	}
	if !strings.Contains(out, "+task") || strings.Count(out, "\x00commit ") != 3 {
		t.Errorf("want the task's two commits and the merge: %q", out)
	}
	if found := scannedHistory(out); len(found) != 0 {
		t.Errorf("found %v in clean work", found)
	}
}

// A secret the merge's resolution adds, which neither parent has, is found
// under a trusted base, named by its file.
func TestASecretInAMergesResolutionIsFoundUnderATrustedBase(t *testing.T) {
	dir, trusted, _ := mergedBase(t, "task\n", "task side\npassword = "+scanToken+"\n")
	out, code := runHistory(t, t.TempDir(), 0, "target", dir, "main", trusted)
	if code != 0 {
		t.Fatalf("exit %d, want 0; printed %d bytes", code, len(out))
	}
	if found := scannedHistory(out); !slices.Contains(found, SecretInWork{"TOKEN", "conflict.txt"}) {
		t.Errorf("found %v, want TOKEN in conflict.txt", found)
	}
}

// A secret in a task commit made before the merge is found under a
// trusted base, even when the agent moves lux/main, the ref it fetched
// into, to its own HEAD: only the base lux reported is left out.
func TestASecretInATaskCommitBeforeTheMergeIsFoundUnderATrustedBase(t *testing.T) {
	dir, trusted, git := mergedBase(t, "password = "+scanToken+"\n", "task side\nbase side\n")
	git("update-ref", "refs/remotes/lux/main", "HEAD")
	out, code := runHistory(t, t.TempDir(), 0, "target", dir, "main", trusted)
	if code != 0 {
		t.Fatalf("exit %d, want 0; printed %d bytes", code, len(out))
	}
	if found := scannedHistory(out); !slices.Contains(found, SecretInWork{"TOKEN", "task.txt"}) {
		t.Errorf("found %v, want TOKEN in task.txt", found)
	}
}

// remerge-diff skips an octopus merge: a secret one adds is still found,
// read against each parent.
func TestASecretInAnOctopusMergeIsFoundUnderATrustedBase(t *testing.T) {
	dir, origin, git, inOrigin := historyRepoWithOrigin(t)
	writeCommit(t, origin, inOrigin, "base.txt", "base\n", "Base")
	trusted := fetchAsLux(git, inOrigin)
	git("checkout", "-q", "-b", "side")
	writeCommit(t, dir, git, "side.txt", "side\n", "Side")
	git("checkout", "-q", "main")
	writeCommit(t, dir, git, "task.txt", "task\n", "Task")
	git("merge", "-q", "--no-commit", "lux/main", "side")
	writeCommit(t, dir, git, "octopus.txt", "password = "+scanToken+"\n", "Octopus")
	out, code := runHistory(t, t.TempDir(), 0, "target", dir, "main", trusted)
	if code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}
	if found := scannedHistory(out); !slices.Contains(found, SecretInWork{"TOKEN", "octopus.txt"}) {
		t.Errorf("found %v, want TOKEN in octopus.txt", found)
	}
}

// With no trusted base, or one the checkout does not hold, the whole of
// base..HEAD is read as before: the merged base delta is over the limit
// and the read exits 5, refusing rather than reading less.
func TestWithoutATrustedBaseTheWholeHistoryIsRead(t *testing.T) {
	dir, _, _ := mergedBase(t, "task\n", "task side\nbase side\n")
	for name, trusted := range map[string]string{
		"none":            "",
		"not in checkout": strings.Repeat("a", 40),
	} {
		out, code := runHistory(t, t.TempDir(), 0, "target", dir, "main", trusted)
		if code != 5 || len(out) != historyLimit+1 {
			t.Errorf("%s: exit %d, printed %d bytes; want 5 and historyLimit+1", name, code, len(out))
		}
	}
}
