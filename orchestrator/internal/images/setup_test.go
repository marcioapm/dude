package images

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// setupRoot is a directory standing in for / for images/layer/setup.sh
// (DUDE_SETUP_ROOT), with glibc's loader for this machine and the given
// /etc files, and a PATH of the tools the script uses: git, coreutils and
// chown, which only records what it was asked (the agent's uid may not be
// the test's). With accounts, useradd and groupadd are there too, and
// record their arguments in place of changing the host's accounts.
type setupRoot struct {
	t          *testing.T
	root, path string
	log        string
}

func newSetupRoot(t *testing.T, etc map[string]string, accounts bool) *setupRoot {
	t.Helper()
	s := &setupRoot{t: t, root: t.TempDir(), path: t.TempDir()}
	s.log = filepath.Join(t.TempDir(), "calls")
	loader := map[string]string{"arm64": "/lib/ld-linux-aarch64.so.1", "amd64": "/lib64/ld-linux-x86-64.so.2"}[runtime.GOARCH]
	if loader == "" {
		t.Skipf("setup.sh knows no loader for %s", runtime.GOARCH)
	}
	write(t, filepath.Join(s.root, loader), "", 0o755)
	for name, body := range etc {
		write(t, filepath.Join(s.root, "etc", name), body, 0o644)
	}
	// From /usr/bin, never PATH: a wrapper on PATH may need tools this
	// PATH leaves out.
	for _, tool := range []string{"git", "uname", "tail", "tr", "mkdir", "chmod"} {
		bin := filepath.Join("/usr/bin", tool)
		if _, err := os.Stat(bin); err != nil {
			t.Skipf("%s: %v", tool, err)
		}
		if err := os.Symlink(bin, filepath.Join(s.path, tool)); err != nil {
			t.Fatal(err)
		}
	}
	record := "#!/bin/sh\necho \"${0##*/} $*\" >> '" + s.log + "'\n"
	write(t, filepath.Join(s.path, "chown"), record, 0o755)
	if accounts {
		write(t, filepath.Join(s.path, "useradd"), record, 0o755)
		write(t, filepath.Join(s.path, "groupadd"), record, 0o755)
	}
	return s
}

func write(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}

// run runs setup.sh as a finish build does, under s.root.
func (s *setupRoot) run() (string, error) {
	s.t.Helper()
	script, err := filepath.Abs("../../../images/layer/setup.sh")
	if err != nil {
		s.t.Fatal(err)
	}
	cmd := exec.Command("/bin/sh", script)
	cmd.Env = []string{"PATH=" + s.path, "DUDE_SETUP_ROOT=" + s.root, "HOME=" + s.t.TempDir()}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (s *setupRoot) read(name string) string {
	s.t.Helper()
	b, err := os.ReadFile(filepath.Join(s.root, name))
	if err != nil {
		s.t.Fatal(err)
	}
	return string(b)
}

func (s *setupRoot) calls() []string {
	b, _ := os.ReadFile(s.log)
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

// gitSystem is what git reads from s's /etc/gitconfig for key.
func (s *setupRoot) gitSystem(key string) string {
	s.t.Helper()
	out, _ := exec.Command("git", "config", "--file", filepath.Join(s.root, "etc/gitconfig"), "--get", key).Output()
	return strings.TrimSpace(string(out))
}

// On an image with no agent and no system git config, without useradd:
// agent's passwd, group and locked shadow lines, its home, and git's
// identity, default branch and dude's hook directory as core.hooksPath in
// the system config. A second run changes nothing.
func TestLayerSetupAddsTheAgentAndGitsSystemConfig(t *testing.T) {
	s := newSetupRoot(t, map[string]string{"passwd": "root:x:0:0:root:/root:/bin/sh", "group": "root:x:0:\n"}, false)
	if out, err := s.run(); err != nil {
		t.Fatalf("setup.sh: %v\n%s", err, out)
	}
	if got := s.read("etc/passwd"); got != "root:x:0:0:root:/root:/bin/sh\nagent:x:1000:1000::/home/agent:/bin/sh\n" {
		t.Errorf("passwd = %q", got)
	}
	if got := s.read("etc/group"); got != "root:x:0:\nagent:x:1000:\n" {
		t.Errorf("group = %q", got)
	}
	if got := s.read("etc/shadow"); got != "agent:!::0:99999:7:::\n" {
		t.Errorf("shadow = %q", got)
	}
	if st, err := os.Stat(filepath.Join(s.root, "home/agent")); err != nil || !st.IsDir() {
		t.Errorf("home: %v", err)
	}
	if got := s.calls(); len(got) != 1 || got[0] != "chown 1000:1000 "+s.root+"/home/agent" {
		t.Errorf("calls = %q", got)
	}
	for key, want := range map[string]string{"user.name": "dude", "user.email": "dude@localhost",
		"init.defaultBranch": "main", "core.hooksPath": "/usr/local/share/dude/git-hooks"} {
		if got := s.gitSystem(key); got != want {
			t.Errorf("system %s = %q, want %q", key, got, want)
		}
	}
	before := map[string]string{}
	for _, f := range []string{"etc/passwd", "etc/group", "etc/shadow", "etc/gitconfig"} {
		before[f] = s.read(f)
	}
	if out, err := s.run(); err != nil {
		t.Fatalf("second run: %v\n%s", err, out)
	}
	for f, want := range before {
		if got := s.read(f); got != want {
			t.Errorf("a second run changed %s:\n%s\nwas\n%s", f, got, want)
		}
	}
}

// What the image's own system config sets is kept, each key alone: its
// core.hooksPath, its identity; only what it lacks is added.
func TestLayerSetupKeepsTheImagesOwnGitSettings(t *testing.T) {
	s := newSetupRoot(t, map[string]string{
		"passwd":    "agent:x:1000:1000::/home/agent:/bin/sh\n",
		"group":     "agent:x:1000:\n",
		"gitconfig": "[User]\n\tName = Acme Bot\n[core]\n\thooksPath = /opt/hooks",
	}, false)
	if out, err := s.run(); err != nil {
		t.Fatalf("setup.sh: %v\n%s", err, out)
	}
	for key, want := range map[string]string{"user.name": "Acme Bot", "user.email": "dude@localhost",
		"init.defaultBranch": "main", "core.hooksPath": "/opt/hooks"} {
		if got := s.gitSystem(key); got != want {
			t.Errorf("system %s = %q, want %q", key, got, want)
		}
	}
	if got := s.read("etc/passwd"); got != "agent:x:1000:1000::/home/agent:/bin/sh\n" {
		t.Errorf("passwd = %q", got)
	}
}

// With useradd and groupadd, they add agent, as a second name for a uid
// 1000 another user holds (node on node images).
func TestLayerSetupUsesUseraddWhereTheImageHasIt(t *testing.T) {
	s := newSetupRoot(t, map[string]string{"passwd": "node:x:1000:1000::/home/node:/bin/sh\n", "group": "node:x:1000:\n"}, true)
	if out, err := s.run(); err != nil {
		t.Fatalf("setup.sh: %v\n%s", err, out)
	}
	want := []string{
		"groupadd --non-unique --gid 1000 agent",
		"useradd --non-unique --uid 1000 --gid 1000 --no-create-home --home-dir /home/agent --shell /bin/sh agent",
		"chown 1000:1000 " + s.root + "/home/agent",
	}
	if got := s.calls(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("calls:\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// An image without glibc's loader, or without git, is refused in the
// sentence the build's failure reads (Failure).
func TestLayerSetupRefusesAnImageWithoutGlibcOrGit(t *testing.T) {
	s := newSetupRoot(t, nil, false)
	if err := os.RemoveAll(filepath.Join(s.root, "lib")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(s.root, "lib64")); err != nil {
		t.Fatal(err)
	}
	if out, err := s.run(); err == nil || strings.TrimSpace(out) != "dude layer: the dude layer needs /bin/sh and glibc in the image" {
		t.Errorf("no loader: %v %q", err, out)
	}
	s = newSetupRoot(t, nil, false)
	if err := os.Remove(filepath.Join(s.path, "git")); err != nil {
		t.Fatal(err)
	}
	out, err := s.run()
	if err == nil || strings.TrimSpace(out) != "dude layer: the image needs git: agents commit with it" {
		t.Errorf("no git: %v %q", err, out)
	}
	if got := Failure("finishing", out, err, "1536m", false); got != "the image needs git: agents commit with it" {
		t.Errorf("Failure = %q", got)
	}
}
