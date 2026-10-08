package images

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTheCheckSaysWhatIsMissingInOneSentence(t *testing.T) {
	ids := []string{"agent:1:999", "agent:1001:64535"}
	m := Mapper{Path: "/usr/bin/newuidmap", FileCap: true}
	g := Mapper{Path: "/usr/bin/newgidmap", FileCap: true}
	for _, c := range []struct {
		name   string
		f      Found
		detail string
		want   string
	}{
		{"nothing", Found{}, "Missing: podman or Docker, fuse-overlayfs, newuidmap, newgidmap, subuid for agent, subgid for agent",
			"Can't run containers: the image has no podman or rootless Docker, no fuse-overlayfs, no newuidmap or newgidmap, and no /etc/subuid or /etc/subgid entry for agent."},
		{"no capability", Found{Podman: "/p", FuseOverlayfs: "/f", Newuidmap: Mapper{Path: "/usr/bin/newuidmap"}, Newgidmap: g, Subuid: ids, Subgid: ids},
			"Missing: cap_setuid on newuidmap", "Can't run containers: newuidmap has no cap_setuid file capability."},
		{"no subuid", Found{Podman: "/p", FuseOverlayfs: "/f", Newuidmap: m, Newgidmap: g, Subgid: ids},
			"Missing: subuid for agent", "Can't run containers: the image has no /etc/subuid entry for agent."},
		{"a range a Run cannot map", Found{Podman: "/p", FuseOverlayfs: "/f", Newuidmap: m, Newgidmap: g, Subuid: []string{"1000:100000:65536"}, Subgid: ids},
			"Missing: subuid within 65536 ids", "Can't run containers: agent's subordinate ids 1000:100000:65536 are outside the 65536 a Run has."},
		{"setuid root newuidmap (Debian's uidmap) can", Found{Docker: "/usr/bin/dockerd-rootless.sh", Dockerd: "/d", Rootlesskit: "/r", Slirp4netns: "/s", FuseOverlayfs: "/f",
			Newuidmap: Mapper{Path: "/usr/bin/newuidmap", Setuid: true}, Newgidmap: Mapper{Path: "/usr/bin/newgidmap", Setuid: true}, Subuid: ids, Subgid: ids},
			"rootless Docker, fuse-overlayfs, newuidmap/newgidmap with capabilities, subuid for agent", ""},
	} {
		if got := c.f.Detail(); got != c.detail {
			t.Errorf("%s: detail = %q, want %q", c.name, got, c.detail)
		}
		if c.want == "" {
			if !c.f.Passed() {
				t.Errorf("%s: did not pass", c.name)
			}
			continue
		}
		if got := c.f.Sentence(); c.f.Passed() || got != c.want {
			t.Errorf("%s: sentence = %q, want %q", c.name, got, c.want)
		}
	}
}

// ableDocker is an image with rootless Docker and nothing of podman.
func ableDocker() Found {
	ids := []string{"agent:1:999", "agent:1001:64535"}
	return Found{Docker: "/usr/bin/dockerd-rootless.sh", Dockerd: "/usr/bin/dockerd", Rootlesskit: "/usr/bin/rootlesskit",
		Slirp4netns: "/usr/bin/slirp4netns", FuseOverlayfs: "/usr/bin/fuse-overlayfs",
		Newuidmap: Mapper{Path: "/usr/bin/newuidmap", Setuid: true}, Newgidmap: Mapper{Path: "/usr/bin/newgidmap", Setuid: true},
		Subuid: ids, Subgid: ids}
}

// Rootless Docker is an engine only with dockerd, RootlessKit and
// slirp4netns beside its launcher; podman alone needs none of them.
func TestRootlessDockerNeedsDockerdRootlesskitAndSlirp4netns(t *testing.T) {
	if f := ableDocker(); !f.Passed() || f.Detail() != "rootless Docker, fuse-overlayfs, newuidmap/newgidmap with capabilities, subuid for agent" {
		t.Errorf("complete rootless Docker: passed %v, detail %q", f.Passed(), f.Detail())
	}
	for _, c := range []struct {
		name, detail, sentence string
		drop                   func(*Found)
	}{
		{"no dockerd", "Missing: dockerd for rootless Docker", "Can't run containers: rootless Docker has no dockerd.", func(f *Found) { f.Dockerd = "" }},
		{"no rootlesskit", "Missing: rootlesskit for rootless Docker", "Can't run containers: rootless Docker has no rootlesskit.", func(f *Found) { f.Rootlesskit = "" }},
		{"no slirp4netns", "Missing: slirp4netns for rootless Docker", "Can't run containers: rootless Docker has no slirp4netns.", func(f *Found) { f.Slirp4netns = "" }},
		{"the launcher alone", "Missing: dockerd, rootlesskit, and slirp4netns for rootless Docker",
			"Can't run containers: rootless Docker has no dockerd, rootlesskit or slirp4netns.", func(f *Found) { f.Dockerd, f.Rootlesskit, f.Slirp4netns = "", "", "" }},
	} {
		f := ableDocker()
		c.drop(&f)
		if f.Passed() || f.Detail() != c.detail || f.Sentence() != c.sentence {
			t.Errorf("%s: passed %v, detail %q, sentence %q", c.name, f.Passed(), f.Detail(), f.Sentence())
		}
		// podman beside a partial Docker is an engine on its own.
		f.Podman = "/usr/bin/podman"
		if !f.Passed() {
			t.Errorf("%s, with podman: %s", c.name, f.Sentence())
		}
	}
}

// ContainersCheck finds Docker's parts on the image's PATH.
func TestTheCheckLooksForEachPartOfRootlessDocker(t *testing.T) {
	t.Setenv("PATH", "/usr/bin")
	for _, c := range []struct {
		name  string
		files []string
		want  bool
	}{
		{"all of it", []string{"dockerd-rootless.sh", "dockerd", "rootlesskit", "slirp4netns"}, true},
		{"no rootlesskit", []string{"dockerd-rootless.sh", "dockerd", "slirp4netns"}, false},
		{"the launcher alone", []string{"dockerd-rootless.sh"}, false},
	} {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, "usr/bin"), 0o755); err != nil {
			t.Fatal(err)
		}
		for _, n := range c.files {
			if err := os.WriteFile(filepath.Join(root, "usr/bin", n), nil, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		if got := ContainersCheck(root).docker(); got != c.want {
			t.Errorf("%s: docker = %v, want %v", c.name, got, c.want)
		}
	}
}

// The check counts only lines a mapper looks the workload up by: the first
// passwd name for agent's uid, or the uid.
func TestTheCheckCountsTheLinesOfTheFirstNameForAgentsUid(t *testing.T) {
	for _, c := range []struct {
		name, passwd, subuid, want string
	}{
		{"agent first", "agent:x:1000:1000::/home/agent:/bin/sh\n", "agent:1:999\n1000:1001:64535\nother:1:5\n", "agent:1:999,1000:1001:64535"},
		{"node first", "node:x:1000:1000::/home/node:/bin/sh\nagent:x:1000:1000::/home/agent:/bin/sh\n", "agent:1:999\nnode:100000:65536\n1000:1001:64535\n", "node:100000:65536,1000:1001:64535"},
		{"node first, agent lines only", "node:x:1000:1000::/home/node:/bin/sh\nagent:x:1000:1000::/home/agent:/bin/sh\n", "agent:1:999\nagent:1001:64535\n", ""},
	} {
		root := t.TempDir()
		_ = os.MkdirAll(filepath.Join(root, "etc"), 0o755)
		_ = os.WriteFile(filepath.Join(root, "etc/passwd"), []byte(c.passwd), 0o644)
		_ = os.WriteFile(filepath.Join(root, "etc/subuid"), []byte(c.subuid), 0o644)
		if got := strings.Join(ContainersCheck(root).Subuid, ","); got != c.want {
			t.Errorf("%s: subuid = %q, want %q", c.name, got, c.want)
		}
	}
}

// An engine's --version that leaves a child holding its output is cut off
// at versionTimeout, child and all.
func TestAVersionProbeThatHangsIsBounded(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "podman")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nsleep 60 &\nsleep 60\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	defer func(d time.Duration) { versionTimeout = d }(versionTimeout)
	versionTimeout = 200 * time.Millisecond
	done := make(chan string, 1)
	start := time.Now()
	go func() { done <- version(bin) }()
	select {
	case got := <-done:
		if got != "" || time.Since(start) > 3*time.Second {
			t.Errorf("version = %q after %s", got, time.Since(start))
		}
	case <-time.After(10 * time.Second):
		t.Fatal("version still waiting after 10s")
	}
}
