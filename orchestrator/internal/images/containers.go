package images

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// The container check: a version marked "Can run containers" is checked,
// once its dude layer is on, for what lux's nested containers need of an
// image (lux docs/runspec.md, "Nested containers"): an engine (podman, or
// rootless Docker: dockerd-rootless with dockerd, RootlessKit and
// slirp4netns), fuse-overlayfs, newuidmap and newgidmap able to gain
// CAP_SETUID and CAP_SETGID, and /etc/subuid and /etc/subgid entries for
// the workload user, agent. Missing any, the build fails.

// CheckCommand is the builder's subcommand that inspects the image it runs
// in (ContainersCheck) and prints Found as JSON. The builder is a static
// binary, so it runs in any image of its architecture with nothing of the
// image's own: no shell, getcap or python needed to read a file capability.
const CheckCommand = "containers-check"

// Found is what the check found in an image.
type Found struct {
	// Paths, "" for none; versions as the engine prints them.
	Podman, PodmanVersion string `json:",omitempty"`
	// Docker is the rootless launcher (dockerd-rootless[.sh]); it runs
	// only with Dockerd, Rootlesskit and Slirp4netns beside it.
	Docker, DockerVersion string `json:",omitempty"`
	Dockerd, Rootlesskit  string `json:",omitempty"`
	Slirp4netns           string `json:",omitempty"`
	FuseOverlayfs         string `json:",omitempty"`
	Newuidmap, Newgidmap  Mapper
	// agent's lines in /etc/subuid and /etc/subgid, as written: by the
	// name passwd gives its uid first, or by the uid (agentOwners). Empty
	// for none (or no agent user).
	Subuid, Subgid []string `json:",omitempty"`
}

// RunIDs is how many ids a Run's user namespace has (lux runs every Run
// --userns=auto:size=65536): a subordinate range must lie within it, or
// newuidmap cannot map it inside the Run.
const RunIDs = 65536

// outside are the lines whose range a Run cannot map.
func outside(lines []string) []string {
	var out []string
	for _, l := range lines {
		parts := strings.Split(l, ":")
		start, _ := strconv.ParseUint(parts[1], 10, 64)
		count, _ := strconv.ParseUint(parts[2], 10, 64)
		if start == 0 || count == 0 || start+count > RunIDs {
			out = append(out, l)
		}
	}
	return out
}

func (f Found) ids() bool {
	return len(f.Subuid) > 0 && len(f.Subgid) > 0 && len(outside(f.Subuid)) == 0 && len(outside(f.Subgid)) == 0
}

// Mapper is newuidmap or newgidmap: where it is, and whether it can gain
// the capability it needs: from a file capability (setcap cap_setuid=ep,
// as Fedora and Alpine ship it), or as a setuid-root program (as Debian's
// uidmap package does), which lux's nested Runs also allow (no
// no-new-privileges).
type Mapper struct {
	Path    string `json:",omitempty"`
	FileCap bool   `json:",omitempty"`
	Setuid  bool   `json:",omitempty"`
}

func (m Mapper) able() bool { return m.Path != "" && (m.FileCap || m.Setuid) }

// dockerMissing are the parts rootless Docker lacks besides its launcher.
func (f Found) dockerMissing() []string {
	var out []string
	for _, p := range []struct{ name, path string }{{"dockerd", f.Dockerd}, {"rootlesskit", f.Rootlesskit}, {"slirp4netns", f.Slirp4netns}} {
		if p.path == "" {
			out = append(out, p.name)
		}
	}
	return out
}

func (f Found) docker() bool { return f.Docker != "" && len(f.dockerMissing()) == 0 }

func (f Found) engine() bool { return f.Podman != "" || f.docker() }

// partialDocker is a rootless Docker launcher without the rest of it, in
// an image with no podman: what is missing is Docker's parts.
func (f Found) partialDocker() bool { return f.Podman == "" && f.Docker != "" && !f.docker() }

// Passed is whether the image can run containers.
func (f Found) Passed() bool {
	return f.engine() && f.FuseOverlayfs != "" && f.Newuidmap.able() && f.Newgidmap.able() && f.ids()
}

// engineName is the engine as the build page names it: "podman 5.4".
func (f Found) engineName() string {
	if f.Podman != "" {
		return "podman" + shortVersion(f.PodmanVersion)
	}
	return "rootless Docker" + shortVersion(f.DockerVersion)
}

var versionNumber = regexp.MustCompile(`(\d+)\.(\d+)`)

func shortVersion(s string) string {
	if m := versionNumber.FindStringSubmatch(s); m != nil {
		return " " + m[1] + "." + m[2]
	}
	return ""
}

// Detail is the check's one line on the build page: what it found, or
// "Missing: …".
func (f Found) Detail() string {
	if f.Passed() {
		return f.engineName() + ", fuse-overlayfs, newuidmap/newgidmap with capabilities, subuid for agent"
	}
	var missing []string
	switch {
	case f.partialDocker():
		missing = append(missing, andList(f.dockerMissing())+" for rootless Docker")
	case !f.engine():
		missing = append(missing, "podman or Docker")
	}
	if f.FuseOverlayfs == "" {
		missing = append(missing, "fuse-overlayfs")
	}
	for _, m := range []struct {
		name, cap string
		m         Mapper
	}{{"newuidmap", "cap_setuid", f.Newuidmap}, {"newgidmap", "cap_setgid", f.Newgidmap}} {
		switch {
		case m.m.Path == "":
			missing = append(missing, m.name)
		case !m.m.able():
			missing = append(missing, m.cap+" on "+m.name)
		}
	}
	if len(f.Subuid) == 0 {
		missing = append(missing, "subuid for agent")
	}
	if len(f.Subgid) == 0 {
		missing = append(missing, "subgid for agent")
	}
	if bad := append(outside(f.Subuid), outside(f.Subgid)...); len(bad) > 0 {
		missing = append(missing, "subuid within 65536 ids")
	}
	return "Missing: " + strings.Join(missing, ", ")
}

// Sentence is why a version that cannot run containers failed its build,
// in one sentence: "Can't run containers: the image has no podman or
// rootless Docker, no fuse-overlayfs, and no newuidmap or newgidmap."
func (f Found) Sentence() string {
	var absent []string
	if !f.engine() && !f.partialDocker() {
		absent = append(absent, "no podman or rootless Docker")
	}
	if f.FuseOverlayfs == "" {
		absent = append(absent, "no fuse-overlayfs")
	}
	switch {
	case f.Newuidmap.Path == "" && f.Newgidmap.Path == "":
		absent = append(absent, "no newuidmap or newgidmap")
	case f.Newuidmap.Path == "":
		absent = append(absent, "no newuidmap")
	case f.Newgidmap.Path == "":
		absent = append(absent, "no newgidmap")
	}
	switch {
	case len(f.Subuid) == 0 && len(f.Subgid) == 0:
		absent = append(absent, "no /etc/subuid or /etc/subgid entry for agent")
	case len(f.Subuid) == 0:
		absent = append(absent, "no /etc/subuid entry for agent")
	case len(f.Subgid) == 0:
		absent = append(absent, "no /etc/subgid entry for agent")
	}
	var clauses []string
	if f.partialDocker() {
		clauses = append(clauses, "rootless Docker has no "+orList(f.dockerMissing()))
	}
	if len(absent) > 0 {
		clauses = append(clauses, "the image has "+andList(absent))
	}
	if f.Newuidmap.Path != "" && !f.Newuidmap.able() {
		clauses = append(clauses, "newuidmap has no cap_setuid file capability")
	}
	if f.Newgidmap.Path != "" && !f.Newgidmap.able() {
		clauses = append(clauses, "newgidmap has no cap_setgid file capability")
	}
	if bad := append(outside(f.Subuid), outside(f.Subgid)...); len(bad) > 0 {
		clauses = append(clauses, fmt.Sprintf("agent's subordinate ids %s are outside the %d a Run has", strings.Join(dedupe(bad), ", "), RunIDs))
	}
	return "Can't run containers: " + andList(clauses) + "."
}

func dedupe(list []string) []string {
	var out []string
	for _, s := range list {
		if !contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

func orList(items []string) string {
	if len(items) > 1 {
		return strings.Join(items[:len(items)-1], ", ") + " or " + items[len(items)-1]
	}
	return strings.Join(items, "")
}

func andList(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	case 2:
		return items[0] + " and " + items[1]
	}
	return strings.Join(items[:len(items)-1], ", ") + ", and " + items[len(items)-1]
}

// LogLines are the check's lines in the build's log, one per thing it
// looked for.
func (f Found) LogLines() []string {
	or := func(path, version string) string {
		if path == "" {
			return "not found"
		}
		if v := shortVersion(version); v != "" {
			return path + " (" + strings.TrimSpace(v) + ")"
		}
		return path
	}
	mapper := func(m Mapper, cap string) string {
		switch {
		case m.Path == "":
			return "missing"
		case m.FileCap:
			return m.Path + " " + cap + "=ep"
		case m.Setuid:
			return m.Path + " setuid root"
		}
		return m.Path + " has no " + cap
	}
	ids := func(lines []string) string {
		if len(lines) == 0 {
			return "no entry for agent"
		}
		return strings.Join(lines, ", ")
	}
	docker := or(f.Docker, f.DockerVersion)
	if f.Docker != "" && !f.docker() {
		docker += ", without " + andList(f.dockerMissing())
	}
	return []string{
		"check  engine  podman: " + or(f.Podman, f.PodmanVersion) + " · docker: " + docker,
		"check  fuse-overlayfs  " + or(f.FuseOverlayfs, ""),
		"check  newuidmap  " + mapper(f.Newuidmap, "cap_setuid"),
		"check  newgidmap  " + mapper(f.Newgidmap, "cap_setgid"),
		"check  subuid  " + ids(f.Subuid),
		"check  subgid  " + ids(f.Subgid),
	}
}

// Capability bits in vfs_cap_data's first permitted word.
const (
	capSetgid = 6
	capSetuid = 7
)

// ContainersCheck inspects the image it runs in, under root (an absolute
// root for tests; "/" in the image).
func ContainersCheck(root string) Found {
	var f Found
	look := func(names ...string) string {
		for _, n := range names {
			for _, dir := range searchPath() {
				p := filepath.Join(dir, n)
				if st, err := os.Stat(filepath.Join(root, p)); err == nil && st.Mode().IsRegular() && st.Mode()&0o111 != 0 {
					return p
				}
			}
		}
		return ""
	}
	f.Podman = look("podman")
	if f.Podman != "" && root == "/" {
		f.PodmanVersion = version(f.Podman)
	}
	f.Docker = look("dockerd-rootless", "dockerd-rootless.sh")
	if f.Docker != "" {
		f.Dockerd = look("dockerd")
		f.Rootlesskit = look("rootlesskit")
		f.Slirp4netns = look("slirp4netns")
		if f.Dockerd != "" && root == "/" {
			f.DockerVersion = version(f.Dockerd)
		}
	}
	f.FuseOverlayfs = look("fuse-overlayfs")
	f.Newuidmap = mapper(root, look("newuidmap"), capSetuid)
	f.Newgidmap = mapper(root, look("newgidmap"), capSetgid)
	owners := agentOwners(root)
	f.Subuid = idLines(filepath.Join(root, "etc/subuid"), owners)
	f.Subgid = idLines(filepath.Join(root, "etc/subgid"), owners)
	return f
}

func searchPath() []string {
	dirs := filepath.SplitList(os.Getenv("PATH"))
	for _, d := range []string{"/usr/local/sbin", "/usr/local/bin", "/usr/sbin", "/usr/bin", "/sbin", "/bin"} {
		if !contains(dirs, d) {
			dirs = append(dirs, d)
		}
	}
	return dirs
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func version(bin string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(ctx, bin, "--version").Output()
	return strings.TrimSpace(string(out))
}

func mapper(root, path string, capBit uint) Mapper {
	if path == "" {
		return Mapper{}
	}
	m := Mapper{Path: path}
	full := filepath.Join(root, path)
	if st, err := os.Stat(full); err == nil {
		if sys, ok := st.Sys().(*syscall.Stat_t); ok && sys.Uid == 0 && st.Mode()&os.ModeSetuid != 0 {
			m.Setuid = true
		}
	}
	buf := make([]byte, 64)
	n, err := syscall.Getxattr(full, "security.capability", buf)
	// vfs_cap_data: magic_etc, then permitted and inheritable of the low
	// 32 capabilities (v2 and v3 have a second pair, and v3 a root id).
	if err == nil && n >= 12 {
		permitted := binary.LittleEndian.Uint32(buf[4:8])
		m.FileCap = permitted&(1<<capBit) != 0
	}
	return m
}

// agentOwners are the owners a subordinate id line must name to be the
// workload's: the first passwd name for agent's uid (node on node images,
// agent otherwise) and the uid itself. podman and RootlessKit look the
// caller up by uid and match only those two, so an agent line behind
// another name is not used. None without an agent user.
func agentOwners(root string) []string {
	raw, err := os.ReadFile(filepath.Join(root, "etc/passwd"))
	if err != nil {
		return nil
	}
	var entries [][]string
	uid := ""
	for _, l := range strings.Split(string(raw), "\n") {
		parts := strings.Split(l, ":")
		if len(parts) > 2 {
			entries = append(entries, parts)
			if parts[0] == "agent" && uid == "" {
				uid = parts[2]
			}
		}
	}
	if uid == "" {
		return nil
	}
	for _, e := range entries {
		if e[2] == uid {
			return []string{e[0], uid}
		}
	}
	return nil
}

// idLines are a subuid or subgid file's entries for any of owners.
func idLines(path string, owners []string) []string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []string
	for _, l := range strings.Split(string(raw), "\n") {
		parts := strings.Split(strings.TrimSpace(l), ":")
		if len(parts) != 3 || !contains(owners, parts[0]) {
			continue
		}
		if _, err := strconv.ParseUint(parts[1], 10, 32); err != nil {
			continue
		}
		if _, err := strconv.ParseUint(parts[2], 10, 32); err != nil {
			continue
		}
		out = append(out, strings.TrimSpace(l))
	}
	return out
}

// CheckMain is CheckCommand: the check of the image it runs in, as JSON on
// stdout.
func CheckMain() int {
	if err := json.NewEncoder(os.Stdout).Encode(ContainersCheck("/")); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

// parseFound reads CheckMain's output.
func parseFound(out []byte) (Found, error) {
	var f Found
	if err := json.Unmarshal(out, &f); err != nil {
		return Found{}, errors.New("the container check printed no result: " + strings.TrimSpace(string(out)))
	}
	return f, nil
}
