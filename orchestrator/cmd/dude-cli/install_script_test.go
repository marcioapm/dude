package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// installScript runs scripts/install-cli.sh against srv into a temp
// directory, as on this machine.
func installScript(t *testing.T, srv string, env ...string) (dir, stderr string, err error) {
	t.Helper()
	script, _ := filepath.Abs(filepath.Join("..", "..", "..", "scripts", "install-cli.sh"))
	return installScriptFrom(t, script, srv, env...)
}

// installScriptFrom is installScript with another copy of the script.
func installScriptFrom(t *testing.T, script, srv string, env ...string) (dir, stderr string, err error) {
	t.Helper()
	sh, lerr := exec.LookPath("sh")
	if lerr != nil {
		t.Skip("no sh")
	}
	for _, tool := range []string{"curl", "tar"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip("no " + tool)
		}
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" || runtime.GOARCH != "arm64" && runtime.GOARCH != "amd64" {
		t.Skip("install-cli.sh is for darwin and linux, arm64 and amd64")
	}
	home := t.TempDir()
	dir = filepath.Join(home, "bin with space")
	cmd := exec.Command(sh, script)
	cmd.Env = append([]string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + home,
		"DUDE_RELEASES_URL=" + srv + "/repos/marcioapm/dude/releases",
		"DUDE_INSTALL_DIR=" + dir,
	}, env...)
	var errBuf strings.Builder
	cmd.Stderr = &errBuf
	err = cmd.Run()
	return dir, errBuf.String(), err
}

// fakeUname is a directory whose uname answers FAKE_UNAME_S and FAKE_UNAME_M,
// for the front of the script's PATH. One per test: the first exec of a new
// executable costs macOS about 0.2 s.
func fakeUname(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\ncase \"$1\" in -s) echo \"$FAKE_UNAME_S\" ;; -m) echo \"$FAKE_UNAME_M\" ;; *) exit 2 ;; esac\n"
	if err := os.WriteFile(filepath.Join(dir, "uname"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// The payload is never run, so every platform's mapping is tested on any host.
func TestInstallScriptMapsUname(t *testing.T) {
	uname := fakeUname(t)
	for _, tc := range []struct{ sysname, machine, want string }{
		{"Linux", "aarch64", "dude v1.2.0 linux_arm64"},
		{"Linux", "x86_64", "dude v1.2.0 linux_amd64"},
		{"Darwin", "x86_64", "dude v1.2.0 darwin_amd64"},
		{"Darwin", "arm64", "dude v1.2.0 darwin_arm64"},
		{"FreeBSD", "amd64", ""},
	} {
		t.Run(tc.sysname+"/"+tc.machine, func(t *testing.T) {
			f := newFakeRelease(t, "v1.2.0")
			path := "PATH=" + uname + string(os.PathListSeparator) + os.Getenv("PATH")
			dir, stderr, err := installScript(t, f.serve(t).URL, path, "FAKE_UNAME_S="+tc.sysname, "FAKE_UNAME_M="+tc.machine)
			if tc.want == "" {
				if err == nil || !strings.Contains(stderr, "not FreeBSD") {
					t.Fatalf("err %v, stderr:\n%s", err, stderr)
				}
				if _, serr := os.Stat(filepath.Join(dir, "dude")); !os.IsNotExist(serr) {
					t.Fatalf("installed: %v", serr)
				}
				f.mu.Lock()
				defer f.mu.Unlock()
				if len(f.requests) != 0 {
					t.Fatalf("asked %v", f.requests)
				}
				return
			}
			if err != nil {
				t.Fatalf("%v\n%s", err, stderr)
			}
			if got := readFile(t, filepath.Join(dir, "dude")); got != tc.want {
				t.Fatalf("installed %q, want %q", got, tc.want)
			}
		})
	}
}

func TestInstallScriptInstallsTheRelease(t *testing.T) {
	f := newFakeRelease(t, "v1.2.0")
	dir, stderr, err := installScript(t, f.serve(t).URL)
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	exe := filepath.Join(dir, "dude")
	if got := readFile(t, exe); got != "dude v1.2.0 "+runtime.GOOS+"_"+runtime.GOARCH {
		t.Fatalf("installed %q", got)
	}
	if fi, _ := os.Stat(exe); fi.Mode().Perm() != 0o755 {
		t.Fatalf("mode %v", fi.Mode())
	}
	if !strings.Contains(stderr, "is not on your PATH") || !strings.Contains(stderr, `export PATH="`+dir+`:$PATH"`) {
		t.Fatalf("no PATH hint:\n%s", stderr)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".dude*")); len(left) != 0 {
		t.Fatalf("left behind %v", left)
	}
}

func TestInstallScriptTakesAVersion(t *testing.T) {
	f := newFakeRelease(t, "v1.1.0")
	dir, stderr, err := installScript(t, f.serve(t).URL, "DUDE_VERSION=1.1.0")
	if err != nil || readFile(t, filepath.Join(dir, "dude")) != "dude v1.1.0 "+runtime.GOOS+"_"+runtime.GOARCH {
		t.Fatalf("%v\n%s", err, stderr)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) == 0 || f.requests[0] != "/repos/marcioapm/dude/releases/tags/v1.1.0" {
		t.Fatalf("asked %v", f.requests)
	}
}

func TestInstallScriptRefusesABadChecksum(t *testing.T) {
	f := newFakeRelease(t, "v1.2.0")
	name := "dude-cli_v1.2.0_" + runtime.GOOS + "_" + runtime.GOARCH + ".tar.gz"
	for sums, want := range map[string]string{
		strings.Repeat("0", 64) + "  " + name + "\n":               "SHA256SUMS says",
		sha([]byte("x")) + "  dude-cli_v1.2.0_plan9_mips.tar.gz\n": "no line for " + name,
	} {
		f.sums = sums
		dir, stderr, err := installScript(t, f.serve(t).URL)
		if err == nil || !strings.Contains(stderr, want) {
			t.Fatalf("err %v, stderr:\n%s", err, stderr)
		}
		if _, err := os.Stat(filepath.Join(dir, "dude")); !os.IsNotExist(err) {
			t.Fatalf("installed anyway: %v", err)
		}
	}
}

func TestInstallScriptNamesAMissingAsset(t *testing.T) {
	f := newFakeRelease(t, "v1.2.0")
	name := "dude-cli_v1.2.0_" + runtime.GOOS + "_" + runtime.GOARCH + ".tar.gz"
	delete(f.assets, name)
	_, stderr, err := installScript(t, f.serve(t).URL)
	if err == nil || !strings.Contains(stderr, "release v1.2.0 has no "+name) {
		t.Fatalf("err %v, stderr:\n%s", err, stderr)
	}
}

// A GitHub API refusal, such as the unauthenticated rate limit's 403, is
// not reported as "no release" only.
func TestInstallScriptNamesTheRateLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"API rate limit exceeded"}`, http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)
	_, stderr, err := installScript(t, srv.URL)
	if err == nil || !strings.Contains(stderr, "rate limit") || !strings.Contains(stderr, "DUDE_VERSION") {
		t.Fatalf("err %v, stderr:\n%s", err, stderr)
	}
}

// SHA256SUMS comes first, so a release that does not list the tarball costs
// no tarball download.
func TestInstallScriptReadsSHA256SUMSBeforeTheTarball(t *testing.T) {
	f := newFakeRelease(t, "v1.2.0")
	f.sums = sha([]byte("x")) + "  dude-cli_v1.2.0_plan9_mips.tar.gz\n"
	if _, _, err := installScript(t, f.serve(t).URL); err == nil {
		t.Fatal("installed an unlisted tarball")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.requests {
		if strings.HasSuffix(p, ".tar.gz") {
			t.Fatalf("downloaded the tarball: %v", f.requests)
		}
	}
}

// A copy of the script cut off before its last line, as `curl | sh` runs
// one whose download stopped, does nothing.
func TestInstallScriptCutShortRunsNothing(t *testing.T) {
	whole, err := os.ReadFile(filepath.Join("..", "..", "..", "scripts", "install-cli.sh"))
	if err != nil {
		t.Fatal(err)
	}
	body := strings.TrimRight(string(whole), "\n")
	cut := filepath.Join(t.TempDir(), "install-cli.sh")
	if err := os.WriteFile(cut, []byte(body[:strings.LastIndex(body, "\n")+1]), 0o644); err != nil {
		t.Fatal(err)
	}
	f := newFakeRelease(t, "v1.2.0")
	dir, stderr, err := installScriptFrom(t, cut, f.serve(t).URL)
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	if _, serr := os.Stat(filepath.Join(dir, "dude")); !os.IsNotExist(serr) {
		t.Fatalf("installed: %v", serr)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) != 0 || stderr != "" {
		t.Fatalf("requests %v, stderr %q", f.requests, stderr)
	}
}
