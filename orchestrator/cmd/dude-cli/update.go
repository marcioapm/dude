package main

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/version"
)

const releasesURL = "https://api.github.com/repos/marcioapm/dude/releases"

// maxDownload bounds a tarball and the binary in it.
const maxDownload = 256 << 20

// updater replaces the running binary with a GitHub release's. It has its
// own client, which never carries the dude key.
type updater struct {
	// releases is the GitHub API's releases URL; DUDE_RELEASES_URL replaces
	// it, for tests only.
	releases      string
	http          *http.Client
	exe           func() (string, error)
	goos, goarch  string
	current       string
	allowedOrigin string
}

func newUpdater(releases string) *updater {
	if releases == "" {
		releases = releasesURL
	}
	u := &updater{
		releases: strings.TrimRight(releases, "/"),
		exe:      runningExecutable,
		goos:     runtime.GOOS,
		goarch:   runtime.GOARCH,
		current:  version.Version,
	}
	if b, err := url.Parse(u.releases); err == nil {
		u.allowedOrigin = b.Scheme + "://" + b.Host
	}
	u.http = &http.Client{Timeout: 5 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("too many redirects")
		}
		if !u.allowed(req.URL) {
			return fmt.Errorf("refusing a redirect to %s: not GitHub", req.URL.Host)
		}
		return nil
	}}
	return u
}

func runningExecutable() (string, error) {
	p, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(p)
}

// githubHost is a host GitHub serves releases and their assets from; assets
// redirect to objects.githubusercontent.com and its siblings.
func githubHost(h string) bool {
	h = strings.ToLower(h)
	return h == "github.com" || h == "api.github.com" || strings.HasSuffix(h, ".githubusercontent.com")
}

// allowed is whether the updater may fetch u: GitHub over https, or the
// origin releases names.
func (u *updater) allowed(target *url.URL) bool {
	if target.Scheme == "https" && githubHost(target.Hostname()) {
		return true
	}
	return u.allowedOrigin != "" && target.Scheme+"://"+target.Host == u.allowedOrigin
}

func (u *updater) get(rawURL string) ([]byte, int, error) {
	target, err := url.Parse(rawURL)
	if err != nil {
		return nil, 0, err
	}
	if !u.allowed(target) {
		return nil, 0, fmt.Errorf("refusing to download from %s: not GitHub", target.Host)
	}
	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", "dude-cli/"+version.Version)
	resp, err := u.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxDownload+1))
	if err != nil {
		return nil, 0, fmt.Errorf("GET %s: %w", rawURL, err)
	}
	if len(b) > maxDownload {
		return nil, 0, fmt.Errorf("GET %s: larger than %d MiB", rawURL, maxDownload>>20)
	}
	return b, resp.StatusCode, nil
}

type release struct {
	Tag    string `json:"tag_name"`
	Assets []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

// release is the latest release, or the one tagged tag.
func (u *updater) release(tag string) (*release, error) {
	where := u.releases + "/latest"
	if tag != "" {
		where = u.releases + "/tags/" + url.PathEscape(tag)
	}
	b, status, err := u.get(where)
	if err != nil {
		return nil, err
	}
	if status == http.StatusNotFound && tag != "" {
		return nil, fmt.Errorf("no release %s of marcioapm/dude", tag)
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %d %s", where, status, strings.TrimSpace(string(b)))
	}
	var r release
	if err := json.Unmarshal(b, &r); err != nil || r.Tag == "" {
		return nil, fmt.Errorf("GET %s: not a release", where)
	}
	return &r, nil
}

func (u *updater) asset(r *release, name string) ([]byte, error) {
	for _, a := range r.Assets {
		if a.Name != name {
			continue
		}
		b, status, err := u.get(a.URL)
		if err != nil {
			return nil, fmt.Errorf("downloading %s: %w", name, err)
		}
		if status != http.StatusOK {
			return nil, fmt.Errorf("downloading %s: %d", name, status)
		}
		return b, nil
	}
	return nil, fmt.Errorf("release %s has no %s", r.Tag, name)
}

// checksum is name's SHA-256 in a sha256sum listing.
func checksum(sums []byte, name string) (string, error) {
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name {
			return strings.ToLower(f[0]), nil
		}
	}
	return "", fmt.Errorf("SHA256SUMS has no line for %s; refusing an unverified binary", name)
}

// binary is bin/dude from a release tarball.
func binary(tarball []byte) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(tarball))
	if err != nil {
		return nil, fmt.Errorf("the tarball: %w", err)
	}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, errors.New("the tarball has no bin/dude")
		}
		if err != nil {
			return nil, fmt.Errorf("the tarball: %w", err)
		}
		if path.Clean(h.Name) != "bin/dude" || h.Typeflag != tar.TypeReg {
			continue
		}
		b, err := io.ReadAll(io.LimitReader(tr, maxDownload+1))
		if err != nil {
			return nil, fmt.Errorf("the tarball: %w", err)
		}
		if len(b) > maxDownload {
			return nil, errors.New("the tarball's bin/dude is too large")
		}
		return b, nil
	}
}

// newer is whether release tag a is after b; tags that are not vX.Y.Z
// differ or not.
func newer(a, b string) bool {
	pa, oka := semver(a)
	pb, okb := semver(b)
	if !oka || !okb {
		return a != b
	}
	for i := range pa {
		if pa[i] != pb[i] {
			return pa[i] > pb[i]
		}
	}
	return false
}

func semver(v string) ([3]int, bool) {
	var out [3]int
	core, _, _ := strings.Cut(strings.TrimPrefix(v, "v"), "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 || !strings.HasPrefix(v, "v") {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

func devBuild(v string) bool { _, ok := semver(v); return !ok }

func (u *updater) check() (obj, error) {
	r, err := u.release("")
	if err != nil {
		return nil, err
	}
	return obj{"current": u.current, "latest": r.Tag, "updateAvailable": !devBuild(u.current) && newer(r.Tag, u.current)}, nil
}

func (u *updater) update(c *cli, want string) (any, error) {
	if u.goos != "darwin" && u.goos != "linux" {
		return nil, fmt.Errorf("update: dude update works on macOS and Linux only; on %s, download dude-cli from https://github.com/marcioapm/dude/releases or build it with go build ./cmd/dude-cli", u.goos)
	}
	if want != "" && !strings.HasPrefix(want, "v") {
		want = "v" + want
	}
	if want == "" && devBuild(u.current) {
		return nil, fmt.Errorf("update: this is a development build (%s); give --version vX.Y.Z to replace it with a release", u.current)
	}
	exe, err := u.exe()
	if err != nil {
		return nil, fmt.Errorf("update: where this binary is: %w", err)
	}
	r, err := u.release(want)
	if err != nil {
		return nil, fmt.Errorf("update: %w", err)
	}
	if r.Tag == u.current || (want == "" && !newer(r.Tag, u.current)) {
		c.say("dude is already at " + u.current + "; nothing to do")
		return obj{"current": u.current, "latest": r.Tag, "updated": false}, nil
	}
	dir := filepath.Dir(exe)
	probe, err := os.CreateTemp(dir, ".dude-update-*")
	if err != nil {
		return nil, fmt.Errorf("update: cannot write to %s, where this binary is (%s): update it the way it was installed, or re-run with permission to write there", dir, exe)
	}
	probe.Close()
	os.Remove(probe.Name())

	name := fmt.Sprintf("dude-cli_%s_%s_%s.tar.gz", r.Tag, u.goos, u.goarch)
	sums, err := u.asset(r, "SHA256SUMS")
	if err != nil {
		return nil, fmt.Errorf("update: %w", err)
	}
	wantSum, err := checksum(sums, name)
	if err != nil {
		return nil, fmt.Errorf("update: %w", err)
	}
	tarball, err := u.asset(r, name)
	if err != nil {
		return nil, fmt.Errorf("update: %w", err)
	}
	got := sha256.Sum256(tarball)
	if gotSum := hex.EncodeToString(got[:]); gotSum != wantSum {
		return nil, fmt.Errorf("update: %s's SHA-256 is %s, but SHA256SUMS says %s; %s is unchanged", name, gotSum, wantSum, exe)
	}
	bin, err := binary(tarball)
	if err != nil {
		return nil, fmt.Errorf("update: %s: %w", name, err)
	}
	if err := replace(exe, bin); err != nil {
		return nil, fmt.Errorf("update: %w; %s is unchanged", err, exe)
	}
	return obj{"updated": true, "from": u.current, "to": r.Tag, "path": exe}, nil
}

// replace writes bin next to exe, then renames it over exe, so exe is
// always one whole binary or the other.
func replace(exe string, bin []byte) error {
	f, err := os.CreateTemp(filepath.Dir(exe), ".dude-update-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(bin)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp, 0o755)
	}
	if err == nil {
		err = os.Rename(tmp, exe)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}

func (c *cli) say(msg string) {
	if c.errOut != nil {
		fmt.Fprintln(c.errOut, msg)
	}
}

// selfUpdateArgs is whether `dude update ARGS` means self-update: no TASK,
// which update needs, and only self-update's flags, or a bare -h/--help.
func selfUpdateArgs(args []string) bool {
	if len(args) == 0 {
		return true
	}
	name, _, _ := strings.Cut(strings.TrimLeft(args[0], "-"), "=")
	if !strings.HasPrefix(args[0], "-") {
		return false
	}
	if name == "h" || name == "help" {
		for _, a := range args[1:] {
			if !strings.HasPrefix(a, "-") {
				return false
			}
		}
		return true
	}
	return name == "check" || name == "version"
}

func init() {
	commands["self-update"] = command{"", "replace this binary with a release of the dude CLI from GitHub (default: the latest); dude update without a TASK does the same",
		func(fs *flag.FlagSet) doer {
			want := fs.String("version", "", "a release, vX.Y.Z, instead of the latest")
			check := fs.Bool("check", false, "only print the current and the latest version")
			return func(c *cli, _ []string) (any, error) {
				if c.updater == nil {
					return nil, errors.New("update: not available in this build")
				}
				if *check {
					return c.updater.check()
				}
				return c.updater.update(c, *want)
			}
		}}
}
