package phases

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// setupWorkspace is a Run's /workspace as the composed init sees it: real
// git checkouts under repos/, the Run's environment (its command-scope
// include of dude's git-config, which names a copy of dude's hook
// directory), and a HOME of its own.
type setupWorkspace struct {
	t     *testing.T
	root  string
	hooks string // dude's hook directory
	env   []string
}

func newSetupWorkspace(t *testing.T) *setupWorkspace {
	t.Helper()
	hooks, include := dudeHooks(t)
	w := &setupWorkspace{t: t, root: t.TempDir(), hooks: hooks}
	home := t.TempDir()
	runEnv := map[string]string{}
	workloadGitConfig(runEnv)
	w.env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + filepath.Join(home, ".gitconfig"), "GIT_AUTHOR_NAME=a", "GIT_AUTHOR_EMAIL=a@x",
		"GIT_COMMITTER_NAME=a", "GIT_COMMITTER_EMAIL=a@x"}
	for k, v := range runEnv {
		if v == DudeGitConfig {
			v = include
		}
		w.env = append(w.env, k+"="+v)
	}
	return w
}

// dudeHooks is a copy of dude's hook directory, linking the runtime
// image's dude-hook under the client hooks the tests commit through, and
// a git-config naming it as core.hooksPath, as the image's does.
func dudeHooks(t *testing.T) (hooks, include string) {
	t.Helper()
	hooks = t.TempDir()
	hook, err := filepath.Abs("../../../images/runtime/git-hooks/dude-hook")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"pre-commit", "prepare-commit-msg", "commit-msg"} {
		if err := os.Symlink(hook, filepath.Join(hooks, name)); err != nil {
			t.Fatal(err)
		}
	}
	include = filepath.Join(t.TempDir(), "git-config")
	if err := os.WriteFile(include, []byte("[core]\n\thooksPath = "+hooks+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return hooks, include
}

// checkout makes repos/<name>, a repository with one commit.
func (w *setupWorkspace) checkout(name string) string {
	w.t.Helper()
	dir := filepath.Join(w.root, "repos", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		w.t.Fatal(err)
	}
	w.git(dir, "init", "-q", "-b", "main")
	w.git(dir, "commit", "-q", "--allow-empty", "-m", "base")
	return dir
}

func (w *setupWorkspace) git(dir string, args ...string) string {
	w.t.Helper()
	out, errOut, err := w.run(dir, "git", args...)
	if err != nil {
		w.t.Fatalf("git %v: %v\n%s%s", args, err, out, errOut)
	}
	return strings.TrimSpace(out)
}

func (w *setupWorkspace) run(dir, name string, args ...string) (string, string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir, cmd.Env = dir, w.env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

// init runs the spec's init as lux's shim runs it, /bin/sh -c in the
// workload's environment, with the manifest of setups placed as lux places
// a file secret. This workspace is the container's /workspace.
func (w *setupWorkspace) init(setups ...Setup) (stdout, stderr string, code int) {
	w.t.Helper()
	return w.initWithin(SetupTimeout, setups...)
}

func (w *setupWorkspace) initWithin(timeout time.Duration, setups ...Setup) (stdout, stderr string, code int) {
	w.t.Helper()
	manifest := filepath.Join(w.t.TempDir(), "setup")
	if err := os.WriteFile(manifest, []byte(SetupManifest(setups)), 0o400); err != nil {
		w.t.Fatal(err)
	}
	return w.runInit(SetupInit(manifest, w.root, timeout))
}

func (w *setupWorkspace) runInit(script string) (stdout, stderr string, code int) {
	w.t.Helper()
	out, errOut, err := w.run(w.t.TempDir(), "/bin/sh", "-c", script)
	code = 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		w.t.Fatal(err)
	}
	return out, errOut, code
}

// setup is name's setup script, checked out where a phase Run has it.
func (w *setupWorkspace) setup(name, script string) Setup {
	return Setup{Name: name, Path: RepoPath(name), Script: script}
}

func exists(path string) bool { _, err := os.Stat(path); return err == nil }

// A repository's setup script runs in its checkout's root the first time,
// and the marker under its git dir makes the next init (a resume) skip it;
// a fresh checkout runs it again.
func TestASetupScriptRunsOncePerCheckout(t *testing.T) {
	w := newSetupWorkspace(t)
	dir := w.checkout("api")
	count := filepath.Join(t.TempDir(), "count")
	s := w.setup("api", `echo ran >> '`+count+`'
pwd > "$(git rev-parse --git-path where)"
echo "said on stdout"
`)

	out, _, code := w.init(s)
	if code != 0 || !strings.Contains(out, "said on stdout") {
		t.Fatalf("first init: exit %d, stdout %q", code, out)
	}
	if b, _ := os.ReadFile(count); string(b) != "ran\n" {
		t.Errorf("first init ran it %q, want once", b)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, ".git", "where")); strings.TrimSpace(string(b)) != dir {
		t.Errorf("it ran in %q, want the checkout %q", b, dir)
	}
	if !exists(filepath.Join(dir, ".git", "dude-setup-done")) {
		t.Error("no marker after a success")
	}
	if exists(filepath.Join(dir, ".git", "dude-setup.sh")) {
		t.Error("the script file was left behind")
	}
	if got := w.git(dir, "status", "--porcelain"); got != "" {
		t.Errorf("the work tree changed: %q", got)
	}

	if _, _, code := w.init(s); code != 0 {
		t.Fatalf("second init: exit %d", code)
	}
	if b, _ := os.ReadFile(count); string(b) != "ran\n" {
		t.Errorf("a resume ran it again: %q", b)
	}

	// A new phase Run's checkout is fresh: no marker, so it runs.
	if err := os.RemoveAll(filepath.Join(w.root, "repos")); err != nil {
		t.Fatal(err)
	}
	w.checkout("api")
	if _, _, code := w.init(s); code != 0 {
		t.Fatalf("fresh checkout: exit %d", code)
	}
	if b, _ := os.ReadFile(count); string(b) != "ran\nran\n" {
		t.Errorf("a fresh checkout ran it %q, want a second time", b)
	}
}

// A failing script (bash -e: the first failing command) makes init exit
// with its code, names the repository on the last line of stderr after
// the script's own output, writes no marker, and runs no later
// repository's script; the next init tries it again.
func TestAFailedSetupScriptFailsInitAndIsTriedAgain(t *testing.T) {
	w := newSetupWorkspace(t)
	api, web := w.checkout("api"), w.checkout("web")
	log := filepath.Join(t.TempDir(), "log")
	first := w.setup("api", "echo api >> '"+log+"'\n")
	failing := w.setup("web", "echo 'installing hooks' >&2\nfalse\necho 'never reached' >&2\n")
	after := w.setup("docs", "echo docs >> '"+log+"'\n")
	w.checkout("docs")

	_, errOut, code := w.init(first, failing, after)
	if code != 1 {
		t.Fatalf("init exited %d, want the script's 1\n%s", code, errOut)
	}
	lines := strings.Split(strings.TrimSpace(errOut), "\n")
	if last := lines[len(lines)-1]; last != setupFailedPrefix+"1 web" {
		t.Errorf("last stderr line %q", last)
	}
	if !strings.Contains(errOut, "installing hooks") || strings.Contains(errOut, "never reached") {
		t.Errorf("stderr:\n%s", errOut)
	}
	if exists(filepath.Join(web, ".git", "dude-setup-done")) {
		t.Error("a marker after a failure")
	}
	if !exists(filepath.Join(api, ".git", "dude-setup-done")) {
		t.Error("the repository before it lost its marker")
	}
	if b, _ := os.ReadFile(log); string(b) != "api\n" {
		t.Errorf("ran %q, want only api's", b)
	}

	// Its own exit code is init's.
	_, _, code = w.init(w.setup("web", "exit 42\n"))
	if code != 42 {
		t.Errorf("exit 42 made init exit %d", code)
	}
}

// A script past its time is stopped, and init fails with timeout's 124;
// one within it finishes. A spec's init gives each script SetupTimeout.
func TestASetupScriptPastItsTimeFails(t *testing.T) {
	w := newSetupWorkspace(t)
	w.checkout("api")
	started := time.Now()
	_, errOut, code := w.initWithin(time.Second, w.setup("api", "sleep 30\n"))
	if code != 124 || !strings.Contains(errOut, setupFailedPrefix+"124 api") {
		t.Errorf("exit %d\n%s", code, errOut)
	}
	if took := time.Since(started); took > 10*time.Second {
		t.Errorf("stopped after %s", took)
	}
	if _, errOut, code := w.initWithin(5*time.Second, w.setup("api", "sleep 0.1\n")); code != 0 {
		t.Errorf("a script within its time: exit %d\n%s", code, errOut)
	}

	if SetupTimeout != 15*time.Minute {
		t.Errorf("SetupTimeout %s", SetupTimeout)
	}
	var spec lux.Spec
	SetupSpec(&spec, nil)
	if spec.Init == nil || spec.Init.Script != SetupInit(SetupManifestPath, workspaceDir, SetupTimeout) {
		t.Errorf("a spec's init is not SetupInit with SetupTimeout: %+v", spec.Init)
	}
}

// Nothing in the manifest is init's code: a name and a path with quotes,
// $(…), backticks and newlines, and a script with a heredoc of the same,
// are taken literally, and the manifest's next line still parses.
func TestASetupScriptOrNameCannotBreakOutOfInit(t *testing.T) {
	w := newSetupWorkspace(t)
	canary := filepath.Join(t.TempDir(), "pwned")
	odd := "a'b\"c$(touch " + canary + ")`touch " + canary + "`;x\nnext line\n"
	dir := filepath.Join(w.root, "repos", odd)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	w.git(dir, "init", "-q", "-b", "main")
	got := filepath.Join(t.TempDir(), "got")
	after := filepath.Join(t.TempDir(), "after")
	w.checkout("web")
	script := "cat > '" + got + "' <<'EOF'\n'\"$(x)`y` EOF\nEOF\n" + "pwd >> '" + got + "'\n"
	_, errOut, code := w.init(Setup{Name: odd, Path: workspaceDir + "/repos/" + odd, Script: script},
		w.setup("web", "touch '"+after+"'\n"))
	if exists(canary) {
		t.Fatal("text from the name, path or script ran as init's code")
	}
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, errOut)
	}
	if b, _ := os.ReadFile(got); string(b) != "'\"$(x)`y` EOF\n"+dir+"\n" {
		t.Errorf("the script read %q", b)
	}
	if !strings.Contains(errOut, setupRunningPrefix+odd+"\n") {
		t.Errorf("init did not name it as given:\n%s", errOut)
	}
	if !exists(after) {
		t.Error("the next repository's script did not run")
	}

	// A failure names it as given too.
	if err := os.Remove(filepath.Join(dir, ".git", "dude-setup-done")); err != nil {
		t.Fatal(err)
	}
	_, errOut, code = w.init(Setup{Name: odd, Path: workspaceDir + "/repos/" + odd, Script: "'; touch " + canary + " #\n"})
	if exists(canary) || code != 2 || !strings.HasSuffix(errOut, setupFailedPrefix+"2 "+odd+"\n") {
		t.Errorf("exit %d\n%s", code, errOut)
	}
}

// No manifest, or one naming no script, is an init that does nothing; a
// repository with no checkout is passed over; a manifest init cannot read
// fails it.
func TestAManifestWithoutScriptsDoesNothing(t *testing.T) {
	w := newSetupWorkspace(t)
	w.checkout("api")
	if got := SetupManifest([]Setup{{Name: "api", Path: RepoPath("api")}, {Name: "web", Path: RepoPath("web"), Script: " \n\t"}}); got != setupManifestHeader {
		t.Errorf("manifest %q", got)
	}
	if out, errOut, code := w.init(w.setup("api", "")); code != 0 || out != "" || errOut != "" {
		t.Errorf("an empty manifest: exit %d, %q %q", code, out, errOut)
	}
	if out, errOut, code := w.runInit(SetupInit(filepath.Join(t.TempDir(), "none"), w.root, SetupTimeout)); code != 0 || out != "" || errOut != "" {
		t.Errorf("no manifest: exit %d, %q %q", code, out, errOut)
	}
	ran := filepath.Join(t.TempDir(), "ran")
	_, errOut, code := w.init(w.setup("gone", "exit 9\n"), w.setup("api", "touch '"+ran+"'\n"))
	if code != 0 || !exists(ran) || !strings.Contains(errOut, "dude: gone has no checkout") {
		t.Errorf("a repository without a checkout: exit %d\n%s", code, errOut)
	}
	manifest := filepath.Join(t.TempDir(), "setup")
	if err := os.WriteFile(manifest, []byte("not!base64 at all\n"), 0o400); err != nil {
		t.Fatal(err)
	}
	if _, errOut, code := w.runInit(SetupInit(manifest, w.root, SetupTimeout)); code != 1 || !strings.Contains(errOut, "unreadable") {
		t.Errorf("an unreadable manifest: exit %d\n%s", code, errOut)
	}
}

// A script reads neither init's stdin nor the manifest.
func TestASetupScriptHasNoInput(t *testing.T) {
	w := newSetupWorkspace(t)
	w.checkout("api")
	got := filepath.Join(t.TempDir(), "got")
	manifest := filepath.Join(t.TempDir(), "setup")
	if err := os.WriteFile(manifest, []byte(SetupManifest([]Setup{w.setup("api",
		"cat > '"+got+"'\n{ cat <&3 || echo closed; } >> '"+got+"' 2>/dev/null\n")})), 0o400); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sh", "-c", SetupInit(manifest, w.root, SetupTimeout))
	cmd.Env, cmd.Stdin = w.env, strings.NewReader("init's own input\n")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if b, _ := os.ReadFile(got); string(b) != "closed\n" {
		t.Errorf("the script read %q", b)
	}
}

// Every indexed git config var is gone inside a script, one past the
// count included, and an env value whose lines look like them neither
// survives nor stops the script.
func TestASetupScriptClearsEveryIndexedGitConfigPastAMultiLineValue(t *testing.T) {
	for _, val := range []string{
		"pem\nGIT_CONFIG_KEY_0.x=y",
		"headers\nGIT_CONFIG_VALUE_3: a=b",
		"line\nGIT_CONFIG_KEY_0=core.hooksPath",
	} {
		w := newSetupWorkspace(t)
		w.env = append(w.env, "MULTI="+val, "GIT_CONFIG_KEY_9=core.hooksPath", "GIT_CONFIG_VALUE_9=/elsewhere")
		w.checkout("api")
		out := filepath.Join(t.TempDir(), "names")
		_, errOut, code := w.init(w.setup("api", "compgen -e | grep '^GIT_CONFIG_' | sort > '"+out+"' || :\n"))
		if code != 0 {
			t.Fatalf("%q: exit %d\n%s", val, code, errOut)
		}
		if b, _ := os.ReadFile(out); string(b) != "GIT_CONFIG_COUNT\nGIT_CONFIG_GLOBAL\nGIT_CONFIG_NOSYSTEM\n" {
			t.Errorf("%q: exported inside the script %q", val, b)
		}
	}
}

// Inside the script the Run's command-scope git settings are gone
// (GIT_CONFIG_COUNT=0, no KEY/VALUE), so git config writes the
// repository's own config, under the command-scope setting the Run's git
// still reads.
func TestASetupScriptRunsWithoutTheRunsGitConfig(t *testing.T) {
	w := newSetupWorkspace(t)
	dir := w.checkout("api")
	out := filepath.Join(t.TempDir(), "env")
	_, errOut, code := w.init(w.setup("api", `env | grep '^GIT_CONFIG_\(COUNT\|KEY\|VALUE\|PARAMETERS\)' | sort > '`+out+`'
git config --show-scope --get-all core.hooksPath >> '`+out+`' || echo "no hooksPath" >> '`+out+`'
git config core.hooksPath .githooks
`))
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, errOut)
	}
	b, _ := os.ReadFile(out)
	if got := strings.Split(strings.TrimSpace(string(b)), "\n"); !slices.Equal(got, []string{"GIT_CONFIG_COUNT=0", "no hooksPath"}) {
		t.Errorf("inside the script: %q", got)
	}
	if got := w.git(dir, "config", "--show-scope", "--get-all", "core.hooksPath"); got != "local\t.githooks\ncommand\t"+w.hooks {
		t.Errorf("after it, core.hooksPath by scope:\n%s", got)
	}
}

// A setup script that links the repository's own pre-commit into its
// hooks directory (as a README tells a person to) leaves dude's hook
// running it: a commit it refuses is refused, and one it lets through
// is made.
func TestAHookASetupScriptInstallsRunsUnderDudesHook(t *testing.T) {
	w := newSetupWorkspace(t)
	dir := w.checkout("api")
	if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scripts", "pre-commit"),
		[]byte("#!/bin/sh\n[ -e REFUSE ] && { echo 'lint: refused' >&2; exit 3; }\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	w.git(dir, "add", "scripts/pre-commit")
	w.git(dir, "commit", "-q", "-m", "hook")
	if _, errOut, code := w.init(w.setup("api",
		`ln -sf ../../scripts/pre-commit "$(git rev-parse --git-path hooks)/pre-commit"`+"\n")); code != 0 {
		t.Fatalf("exit %d\n%s", code, errOut)
	}
	if !exists(filepath.Join(dir, ".git", "hooks", "pre-commit")) {
		t.Fatal("the script did not install the hook in the repository's hooks directory")
	}
	if err := os.WriteFile(filepath.Join(dir, "REFUSE"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	head := w.git(dir, "rev-parse", "HEAD")
	if _, errOut, err := w.run(dir, "git", "commit", "-q", "--allow-empty", "-m", "refused"); err == nil || !strings.Contains(errOut, "lint: refused") {
		t.Errorf("the installed hook did not refuse: %v %q", err, errOut)
	}
	if w.git(dir, "rev-parse", "HEAD") != head {
		t.Error("a refused commit was made")
	}
	if err := os.Remove(filepath.Join(dir, "REFUSE")); err != nil {
		t.Fatal(err)
	}
	w.git(dir, "commit", "-q", "--allow-empty", "-m", "allowed")
	if w.git(dir, "rev-parse", "HEAD") == head {
		t.Error("an allowed commit was not made")
	}
}
