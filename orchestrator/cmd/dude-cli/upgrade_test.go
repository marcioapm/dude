package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// cliTarball is a release tarball as dist.sh lays it out, with bin as bin/dude.
func cliTarball(t *testing.T, bin string) []byte {
	t.Helper()
	return tarball(t,
		tar.Header{Name: "LICENSE", Mode: 0o644, Typeflag: tar.TypeReg}, "MIT",
		tar.Header{Name: "bin/dude", Mode: 0o755, Typeflag: tar.TypeReg}, bin,
		tar.Header{Name: "share/dude/third-party/go-toml-LICENSE", Mode: 0o644, Typeflag: tar.TypeReg}, "MIT")
}

// tarball is a gzipped tar of header, body pairs, in order.
func tarball(t *testing.T, entries ...any) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for i := 0; i < len(entries); i += 2 {
		h, body := entries[i].(tar.Header), entries[i+1].(string)
		if h.Typeflag == tar.TypeReg {
			h.Size = int64(len(body))
		}
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		io.WriteString(tw, body)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	zw.Close()
	return buf.Bytes()
}

func sha(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

// fakeRelease is GitHub's releases API and asset hosting for one release.
type fakeRelease struct {
	tag    string
	assets map[string][]byte
	// sums replaces SHA256SUMS when set.
	sums string
	// redirect sends asset downloads to this URL's host instead.
	redirect string
	// assetBase replaces the server's own URL in browser_download_url.
	assetBase string
	mu        sync.Mutex
	requests  []string
	auths     []string
}

func (f *fakeRelease) serve(t *testing.T) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.requests = append(f.requests, r.URL.Path)
		f.auths = append(f.auths, r.Header.Get("Authorization"))
		f.mu.Unlock()
		const base = "/repos/marcioapm/dude/releases"
		switch {
		case r.URL.Path == base+"/latest" || r.URL.Path == base+"/tags/"+f.tag:
			rel := map[string]any{"tag_name": f.tag}
			base := srv.URL
			if f.assetBase != "" {
				base = f.assetBase
			}
			var assets []map[string]string
			for name := range f.assets {
				assets = append(assets, map[string]string{"name": name, "browser_download_url": base + "/download/" + f.tag + "/" + name})
			}
			rel["assets"] = assets
			json.NewEncoder(w).Encode(rel)
		case strings.HasPrefix(r.URL.Path, "/download/"):
			if f.redirect != "" {
				http.Redirect(w, r, f.redirect+"/stored"+r.URL.Path, http.StatusFound)
				return
			}
			fallthrough
		case strings.HasPrefix(r.URL.Path, "/stored/download/"):
			name := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
			b, ok := f.assets[name]
			if !ok {
				http.NotFound(w, r)
				return
			}
			if name == "SHA256SUMS" && f.sums != "" {
				b = []byte(f.sums)
			}
			w.Write(b)
		default:
			http.NotFound(w, r)
		}
	}))
	srv.Start()
	t.Cleanup(srv.Close)
	return srv
}

// newFakeRelease is release tag with tarballs for darwin and linux, arm64
// and amd64, and this machine, whose bin/dude is "dude TAG OS_ARCH", and
// their SHA256SUMS.
func newFakeRelease(t *testing.T, tag string) *fakeRelease {
	f := &fakeRelease{tag: tag, assets: map[string][]byte{}}
	var sums strings.Builder
	for _, osArch := range []string{"darwin_arm64", "darwin_amd64", "linux_amd64", "linux_arm64", runtime.GOOS + "_" + runtime.GOARCH} {
		name := fmt.Sprintf("dude-cli_%s_%s.tar.gz", tag, osArch)
		if _, dup := f.assets[name]; dup {
			continue
		}
		f.assets[name] = cliTarball(t, "dude "+tag+" "+osArch)
		fmt.Fprintf(&sums, "%s  %s\n", sha(f.assets[name]), name)
	}
	f.assets["SHA256SUMS"] = []byte(sums.String())
	return f
}

// upgradeCLI is a cli whose updater replaces a fake binary at a temp
// path, from srv, as linux/amd64 at version current.
func upgradeCLI(t *testing.T, srv *httptest.Server, current string) (*cli, string, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "bin", "dude")
	os.MkdirAll(filepath.Dir(exe), 0o755)
	if err := os.WriteFile(exe, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	u := newUpdater(srv.URL + "/repos/marcioapm/dude/releases")
	u.exe = func() (string, error) { return exe, nil }
	u.goos, u.goarch, u.current = "linux", "amd64", current
	var out, errOut bytes.Buffer
	return &cli{updater: u, out: &out, errOut: &errOut,
		creds: func(string) (string, string, error) { return "https://dude.example", "dude-key", nil }}, exe, &out, &errOut
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestUpgradeReplacesTheBinary(t *testing.T) {
	for _, args := range [][]string{{"upgrade"}, {"upgrade", "--version", "v1.2.0"}, {"upgrade", "--version=1.2.0"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			f := newFakeRelease(t, "v1.2.0")
			srv := f.serve(t)
			c, exe, out, errOut := upgradeCLI(t, srv, "v1.1.0")
			// A hard link keeps the old inode: a rename leaves it alone, an
			// in-place write would change it.
			keep := filepath.Join(filepath.Dir(exe), "dude.old")
			if err := os.Link(exe, keep); err != nil {
				t.Fatal(err)
			}
			if err := c.run(args); err != nil {
				t.Fatal(err)
			}
			if got := readFile(t, keep); got != "old binary" {
				t.Fatalf("the old binary was written in place: %q", got)
			}
			if want := "dude upgraded from v1.1.0 to v1.2.0 (" + exe + ")\n"; errOut.String() != want {
				t.Fatalf("stderr %q, want %q", errOut, want)
			}
			if got := readFile(t, exe); got != "dude v1.2.0 linux_amd64" {
				t.Fatalf("binary: %q", got)
			}
			if fi, _ := os.Stat(exe); fi.Mode().Perm() != 0o755 {
				t.Fatalf("mode %v", fi.Mode())
			}
			var got map[string]any
			if err := json.Unmarshal(out.Bytes(), &got); err != nil || got["to"] != "v1.2.0" || got["from"] != "v1.1.0" || got["updated"] != true {
				t.Fatalf("output %s %v", out, err)
			}
			if left, _ := filepath.Glob(filepath.Join(filepath.Dir(exe), ".dude-upgrade-*")); len(left) != 0 {
				t.Fatalf("left behind %v", left)
			}
			for _, a := range f.auths {
				if a != "" {
					t.Fatalf("sent Authorization %q to the release host", a)
				}
			}
		})
	}
}

func TestUpgradeFollowsAssetRedirectsOnlyWithinGitHub(t *testing.T) {
	assets := newFakeRelease(t, "v1.2.0")
	assetSrv := assets.serve(t)

	// The same origin stands in for GitHub's own asset host here.
	f := newFakeRelease(t, "v1.2.0")
	srv := f.serve(t)
	f.redirect = srv.URL
	c, exe, _, _ := upgradeCLI(t, srv, "v1.1.0")
	if err := c.run([]string{"upgrade"}); err != nil || readFile(t, exe) != "dude v1.2.0 linux_amd64" {
		t.Fatalf("same-origin redirect: %v %q", err, readFile(t, exe))
	}

	f = newFakeRelease(t, "v1.2.0")
	srv = f.serve(t)
	f.redirect = strings.Replace(assetSrv.URL, "127.0.0.1", "localhost", 1)
	c, exe, _, _ = upgradeCLI(t, srv, "v1.1.0")
	err := c.run([]string{"upgrade"})
	if err == nil || !strings.Contains(err.Error(), "refusing a redirect to localhost") {
		t.Fatalf("err = %v", err)
	}
	if got := readFile(t, exe); got != "old binary" {
		t.Fatalf("binary changed: %q", got)
	}
	if len(assets.requests) != 0 {
		t.Fatalf("the other host was asked %v", assets.requests)
	}
}

func TestUpgradeRefusesAnAssetURLOutsideGitHub(t *testing.T) {
	other := newFakeRelease(t, "v1.2.0")
	otherSrv := other.serve(t)
	// Another host name for the same loopback server: refused, so it is
	// never asked. Only loopback, so a broken check still reaches no network.
	f := newFakeRelease(t, "v1.2.0")
	f.assetBase = strings.Replace(otherSrv.URL, "127.0.0.1", "localhost", 1)
	c, exe, _, _ := upgradeCLI(t, f.serve(t), "v1.1.0")
	err := c.run([]string{"upgrade"})
	if err == nil || !strings.Contains(err.Error(), "refusing to download from localhost") {
		t.Fatalf("err = %v", err)
	}
	if got := readFile(t, exe); got != "old binary" {
		t.Fatalf("binary changed: %q", got)
	}
	other.mu.Lock()
	defer other.mu.Unlock()
	if len(other.requests) != 0 {
		t.Fatalf("the other host was asked %v", other.requests)
	}
}

// Entries ahead of bin/dude that are named dude, or are a link named
// bin/dude, are skipped: the regular bin/dude is what is installed.
func TestUpgradeInstallsBinDudePastDecoys(t *testing.T) {
	for name, decoy := range map[string][]any{
		"share/dude":        {tar.Header{Name: "share/dude", Mode: 0o755, Typeflag: tar.TypeReg}, "decoy"},
		"dude":              {tar.Header{Name: "dude", Mode: 0o755, Typeflag: tar.TypeReg}, "decoy"},
		"symlink bin/dude":  {tar.Header{Name: "bin/dude", Linkname: "../decoy", Typeflag: tar.TypeSymlink}, ""},
		"hardlink bin/dude": {tar.Header{Name: "bin/dude", Linkname: "share/dude", Typeflag: tar.TypeLink}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFakeRelease(t, "v1.2.0")
			asset := "dude-cli_v1.2.0_linux_amd64.tar.gz"
			f.assets[asset] = tarball(t, append(decoy, tar.Header{Name: "bin/dude", Mode: 0o755, Typeflag: tar.TypeReg}, "dude v1.2.0 linux_amd64")...)
			f.sums = sha(f.assets[asset]) + "  " + asset + "\n"
			c, exe, _, _ := upgradeCLI(t, f.serve(t), "v1.1.0")
			if err := c.run([]string{"upgrade"}); err != nil {
				t.Fatal(err)
			}
			if got := readFile(t, exe); got != "dude v1.2.0 linux_amd64" {
				t.Fatalf("installed %q", got)
			}
		})
	}
}

func TestGitHubHosts(t *testing.T) {
	u := newUpdater("")
	for host, ok := range map[string]bool{
		"https://github.com/x": true, "https://api.github.com/x": true,
		"https://objects.githubusercontent.com/x": true, "https://release-assets.githubusercontent.com/x": true,
		"http://objects.githubusercontent.com/x": false, "https://githubusercontent.com.evil.example/x": false,
		"https://evilgithubusercontent.com/x": false, "https://github.com.evil.example/x": false,
	} {
		target, _ := http.NewRequest("GET", host, nil)
		if got := u.allowed(target.URL); got != ok {
			t.Errorf("%s: allowed %v, want %v", host, got, ok)
		}
	}
}

func TestUpgradeRefusesAWrongChecksum(t *testing.T) {
	f := newFakeRelease(t, "v1.2.0")
	name := "dude-cli_v1.2.0_linux_amd64.tar.gz"
	f.sums = strings.Repeat("0", 64) + "  " + name + "\n"
	c, exe, _, _ := upgradeCLI(t, f.serve(t), "v1.1.0")
	err := c.run([]string{"upgrade"})
	if err == nil || !strings.Contains(err.Error(), "SHA256SUMS says") || !strings.Contains(err.Error(), "unchanged") {
		t.Fatalf("err = %v", err)
	}
	if got := readFile(t, exe); got != "old binary" {
		t.Fatalf("binary changed: %q", got)
	}
}

func TestUpgradeRefusesATarballSHA256SUMSDoesNotList(t *testing.T) {
	f := newFakeRelease(t, "v1.2.0")
	f.sums = sha(f.assets["dude-cli_v1.2.0_darwin_arm64.tar.gz"]) + "  dude-cli_v1.2.0_darwin_arm64.tar.gz\n"
	c, exe, _, _ := upgradeCLI(t, f.serve(t), "v1.1.0")
	if err := c.run([]string{"upgrade"}); err == nil || !strings.Contains(err.Error(), "no line for dude-cli_v1.2.0_linux_amd64.tar.gz") {
		t.Fatalf("err = %v", err)
	}
	if got := readFile(t, exe); got != "old binary" {
		t.Fatalf("binary changed: %q", got)
	}
}

func TestUpgradeNamesAMissingAsset(t *testing.T) {
	for _, missing := range []string{"dude-cli_v1.2.0_linux_amd64.tar.gz", "SHA256SUMS"} {
		f := newFakeRelease(t, "v1.2.0")
		delete(f.assets, missing)
		c, exe, _, _ := upgradeCLI(t, f.serve(t), "v1.1.0")
		if err := c.run([]string{"upgrade"}); err == nil || !strings.Contains(err.Error(), "release v1.2.0 has no "+missing) {
			t.Fatalf("%s: err = %v", missing, err)
		}
		if got := readFile(t, exe); got != "old binary" {
			t.Fatalf("binary changed: %q", got)
		}
	}
	f := newFakeRelease(t, "v1.2.0")
	c, _, _, _ := upgradeCLI(t, f.serve(t), "v1.1.0")
	if err := c.run([]string{"upgrade", "--version", "v9.9.9"}); err == nil || !strings.Contains(err.Error(), "no release v9.9.9") {
		t.Fatalf("unknown version: %v", err)
	}
}

func TestUpgradeCheck(t *testing.T) {
	for _, tc := range []struct {
		current, want string
	}{
		{"v1.1.0", `{"current":"v1.1.0","latest":"v1.2.0","updateAvailable":true}`},
		{"v1.2.0", `{"current":"v1.2.0","latest":"v1.2.0","updateAvailable":false}`},
		{"v1.10.0", `{"current":"v1.10.0","latest":"v1.2.0","updateAvailable":false}`},
		{"dev", `{"current":"dev","latest":"v1.2.0","updateAvailable":false}`},
	} {
		f := newFakeRelease(t, "v1.2.0")
		c, exe, out, _ := upgradeCLI(t, f.serve(t), tc.current)
		if err := c.run([]string{"upgrade", "--check"}); err != nil {
			t.Fatal(err)
		}
		var got map[string]any
		json.Unmarshal(out.Bytes(), &got)
		if b, _ := json.Marshal(got); string(b) != tc.want {
			t.Errorf("%s: %s, want %s", tc.current, b, tc.want)
		}
		if readFile(t, exe) != "old binary" || len(f.requests) != 1 {
			t.Fatalf("--check changed the binary or downloaded: %v", f.requests)
		}
	}
}

func TestUpgradeAtTheLatestDoesNothing(t *testing.T) {
	for _, current := range []string{"v1.2.0", "v1.3.0"} {
		f := newFakeRelease(t, "v1.2.0")
		c, exe, _, errOut := upgradeCLI(t, f.serve(t), current)
		if err := c.run([]string{"upgrade"}); err != nil {
			t.Fatal(err)
		}
		if readFile(t, exe) != "old binary" || len(f.requests) != 1 || !strings.Contains(errOut.String(), "already at "+current) {
			t.Fatalf("%s: %q %v", current, errOut, f.requests)
		}
	}
}

func TestUpgradeOfADevBuildNeedsAVersion(t *testing.T) {
	f := newFakeRelease(t, "v1.2.0")
	c, exe, _, _ := upgradeCLI(t, f.serve(t), "dev")
	if err := c.run([]string{"upgrade"}); err == nil || !strings.Contains(err.Error(), "development build") || len(f.requests) != 0 {
		t.Fatalf("err = %v, requests %v", err, f.requests)
	}
	if err := c.run([]string{"upgrade", "--version", "v1.2.0"}); err != nil || readFile(t, exe) != "dude v1.2.0 linux_amd64" {
		t.Fatalf("with --version: %v", err)
	}
}

func TestUpgradeRefusesAnUnwritableDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes to a 0555 directory")
	}
	f := newFakeRelease(t, "v1.2.0")
	c, exe, _, _ := upgradeCLI(t, f.serve(t), "v1.1.0")
	dir := filepath.Dir(exe)
	os.Chmod(dir, 0o555)
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	err := c.run([]string{"upgrade"})
	if err == nil || !strings.Contains(err.Error(), "cannot write to "+dir) || !strings.Contains(err.Error(), "the way it was installed") {
		t.Fatalf("err = %v", err)
	}
	if readFile(t, exe) != "old binary" || len(f.requests) != 1 {
		t.Fatalf("downloaded after the refusal: %v", f.requests)
	}
}

func TestUpgradeRefusesWindows(t *testing.T) {
	f := newFakeRelease(t, "v1.2.0")
	c, _, _, _ := upgradeCLI(t, f.serve(t), "v1.1.0")
	c.updater.goos = "windows"
	if err := c.run([]string{"upgrade"}); err == nil || !strings.Contains(err.Error(), "macOS and Linux only") || len(f.requests) != 0 {
		t.Fatalf("err = %v", err)
	}
}

// task update is the task command only: without a TASK it is a usage error
// and never reaches the updater; a bare update is no command at all.
func TestUpdateIsTheTaskCommand(t *testing.T) {
	f := newFakeRelease(t, "v1.2.0")
	c, exe, _, _ := upgradeCLI(t, f.serve(t), "v1.1.0")
	for _, args := range [][]string{{"task", "update"}, {"task", "update", "--check"}, {"task", "update", "--version", "v1.2.0"}} {
		if err := c.run(args); err == nil || !strings.HasPrefix(err.Error(), "usage: dude task update TASK") && !strings.Contains(err.Error(), "flag provided but not defined") {
			t.Errorf("%v: err = %v", args, err)
		}
	}
	if err := c.run([]string{"update", "--check"}); err == nil || err.Error() != `unknown command "update"; see dude --help` {
		t.Errorf("update: err = %v", err)
	}
	if readFile(t, exe) != "old binary" || len(f.requests) != 0 {
		t.Fatalf("update without a TASK reached the updater: %v", f.requests)
	}
	srv, calls := fakeDude(t)
	if _, err := runCLI(t, srv, "", "task", "update", "wi_12", "--title", "x"); err != nil || last(*calls).method != "PATCH" {
		t.Fatalf("%v %+v", err, *calls)
	}
}

func TestUpgradeHelpAndUsageNameDudeUpgrade(t *testing.T) {
	for _, help := range []string{"--help", "-h"} {
		f := newFakeRelease(t, "v1.2.0")
		c, _, out, _ := upgradeCLI(t, f.serve(t), "v1.1.0")
		if err := c.run([]string{"upgrade", help}); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(out.String(), "usage: dude upgrade \n") || !strings.Contains(out.String(), "-check") || !strings.Contains(out.String(), "-version") {
			t.Fatalf("%s:\n%s", help, out)
		}
	}
	for _, args := range [][]string{{"upgrade", "extra"}, {"upgrade", "--check", "extra"}} {
		f := newFakeRelease(t, "v1.2.0")
		c, _, _, _ := upgradeCLI(t, f.serve(t), "v1.1.0")
		if err := c.run(args); err == nil || err.Error() != "usage: dude upgrade " || len(f.requests) != 0 {
			t.Fatalf("%v: err = %v, requests %v", args, err, f.requests)
		}
	}
}
