package images

import "testing"

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
		{"setuid root newuidmap (Debian's uidmap) can", Found{Docker: "/usr/bin/dockerd-rootless.sh", FuseOverlayfs: "/f",
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
