package images

// The builder against a real rootless podman and a real registry. Skipped
// where podman is missing (macOS hosts, CI without it); run on a Linux host
// with rootless podman and cgroup v2 cpu+memory delegated to the user:
//
//	scripts/test-image-builder.sh
//
// which starts a throwaway registry container on localhost and runs
//
//	DUDE_PODMAN_TEST_REGISTRY=localhost:5000 go -C orchestrator test -run Podman -v ./internal/images/
//
// DUDE_PODMAN_TEST_REGISTRY names a registry reachable over plain HTTP.
// Without it the tests that push are skipped too.

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func realPodman(t *testing.T) (CLI, string) {
	t.Helper()
	if _, err := exec.LookPath("podman"); err != nil {
		t.Skip("podman is not on PATH: these tests need rootless podman (see scripts/test-image-builder.sh)")
	}
	registry := os.Getenv("DUDE_PODMAN_TEST_REGISTRY")
	if registry == "" {
		t.Skip("DUDE_PODMAN_TEST_REGISTRY is unset: run scripts/test-image-builder.sh, which starts a local registry")
	}
	platform := "linux/" + map[string]string{"aarch64": "arm64", "x86_64": "amd64"}[strings.TrimSpace(uname(t))]
	return CLI{Limits: Limits{Platform: platform, CPUs: 1.5, Memory: "1536m", Timeout: 10 * time.Minute}}, registry
}

func uname(t *testing.T) string {
	out, err := exec.Command("uname", "-m").Output()
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func buildIn(t *testing.T, c CLI, containerfile, tag string) (string, error) {
	t.Helper()
	dir, err := contextDir(containerfile)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	var log bytes.Buffer
	err = c.Build(context.Background(), dir, tag, nil, &log)
	return log.String(), err
}

func TestPodmanBuildsWithinItsMemoryAndPushesByDigest(t *testing.T) {
	c, registry := realPodman(t)
	tag := fmt.Sprintf("%s/dude/custom:imv_test%d-user", registry, time.Now().UnixNano())
	log, err := buildIn(t, c, "FROM docker.io/library/debian:bookworm-slim\nRUN head -c 64m /dev/zero | tail -c 1 >/dev/null\n", tag)
	if err != nil {
		t.Fatalf("build: %v\n%s", err, log)
	}
	var push bytes.Buffer
	digest, err := c.Push(context.Background(), tag, &push)
	if err != nil {
		t.Fatalf("push: %v\n%s", err, push.String())
	}
	if !strings.HasPrefix(digest, "sha256:") || len(digest) != 71 {
		t.Fatalf("digest = %q", digest)
	}
	// Removed from the builder's storage once pushed, as the builder does.
	if err := c.Remove(context.Background(), tag, &push); err != nil {
		t.Fatalf("rmi: %v\n%s", err, push.String())
	}
	if exec.Command("podman", "image", "exists", tag).Run() == nil {
		t.Errorf("%s is still in podman's storage", tag)
	}
	// What was pushed can be pulled back by that digest.
	ref := Repository(tag) + "@" + digest
	if out, err := exec.Command("podman", "pull", "--tls-verify=false", ref).CombinedOutput(); err != nil {
		t.Fatalf("pull %s: %v\n%s", ref, err, out)
	}
}

func TestPodmanOnThisHostCanLimitABuild(t *testing.T) {
	c, _ := realPodman(t)
	if err := CheckLimits(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if got, err := c.Controllers(context.Background()); err != nil || !strings.Contains(fmt.Sprint(got), "memory") {
		t.Fatalf("controllers = %v, %v", got, err)
	}
}

func TestPodmanKillsABuildPastItsMemoryAndTheBuilderSaysSo(t *testing.T) {
	c, registry := realPodman(t)
	// 2 GiB held in one process's memory, past the 1.5 GB cap.
	cf := "FROM docker.io/library/python:3.13-slim-bookworm\nRUN python3 -c \"b = bytearray(2 * 1024**3); print(len(b))\"\n"
	log, err := buildIn(t, c, cf, registry+"/dude/custom:oom-test")
	if err == nil {
		t.Fatalf("a build past 1.5 GB passed: the memory limit does not apply (is memory delegated to this user's cgroup?)\n%s", log)
	}
	if got := Failure("building", log, err, c.Limits.Memory, false); got != "ran out of memory (1.5 GB) at step 2" {
		t.Fatalf("Failure = %q\n%s", got, log)
	}
}

// layerFor makes a stand-in dude layer on the registry: a FROM scratch
// image whose /rootfs/ holds a setup.sh that does to the user what
// aiverse's does, and an opencode.json. Returns it by digest.
func layerFor(t *testing.T, c CLI, registry string) string {
	t.Helper()
	tag := fmt.Sprintf("%s/dude/layer:test%d", registry, time.Now().UnixNano())
	script := base64.StdEncoding.EncodeToString([]byte(testSetup))
	cf := "FROM docker.io/library/busybox:1 AS files\n" +
		"RUN mkdir -p /rootfs/usr/local/share/dude && echo " + script + " | base64 -d > /rootfs/usr/local/share/dude/setup.sh" +
		" && echo '{}' > /rootfs/usr/local/share/dude/opencode.json\n" +
		"FROM scratch\nCOPY --from=files /rootfs/ /rootfs/\n"
	if log, err := buildIn(t, c, cf, tag); err != nil {
		t.Fatalf("layer: %v\n%s", err, log)
	}
	digest, err := c.Push(context.Background(), tag, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	return Repository(tag) + "@" + digest
}

// finishOn builds a user image on base, pushes it, and finishes it with a
// stand-in dude layer, as a finish job does.
func finishOn(t *testing.T, base string) string {
	t.Helper()
	c, registry := realPodman(t)
	stamp := time.Now().UnixNano()
	user := fmt.Sprintf("%s/dude/custom:imv_%d-user", registry, stamp)
	if log, err := buildIn(t, c, "FROM "+base+"\nRUN command -v git || (apt-get update && apt-get install -y --no-install-recommends git)\n", user); err != nil {
		t.Fatalf("user image: %v\n%s", err, log)
	}
	digest, err := c.Push(context.Background(), user, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	layer := layerFor(t, c, registry)
	final := FinalTag(Repository(user), fmt.Sprintf("imv_%d", stamp), layer)
	if log, err := buildIn(t, c, FinishContainerfile(Repository(user)+"@"+digest, layer), final); err != nil {
		t.Fatalf("finish: %v\n%s", err, log)
	}
	return final
}

// testSetup stands in for the layer's setup.sh (aiverse's): the agent user
// at uid 1000, as a second name when 1000 is taken, and its home.
const testSetup = `#!/bin/sh
set -e
if ! grep -q "^agent:" /etc/passwd; then
  if grep -q "^[^:]*:[^:]*:1000:" /etc/passwd; then
    echo "agent:x:1000:1000::/home/agent:/bin/sh" >> /etc/passwd
  else
    echo "agent:x:1000:1000::/home/agent:/bin/sh" >> /etc/passwd
    grep -q "^[^:]*:[^:]*:1000:" /etc/group || echo "agent:x:1000:" >> /etc/group
  fi
fi
mkdir -p /home/agent && chown 1000:1000 /home/agent
[ -n "$(git config --system user.name 2>/dev/null)" ] || printf '[user]\n\tname = dude\n\temail = dude@localhost\n[init]\n\tdefaultBranch = main\n' >> /etc/gitconfig
git --version >/dev/null 2>&1 || { echo "dude: the image needs git: agents commit with it"; exit 1; }
`

func runIn(t *testing.T, image string, cmd ...string) string {
	t.Helper()
	return runAs(t, "", image, cmd...)
}

// runAs runs cmd in image as user ("" for the image's own).
func runAs(t *testing.T, user, image string, cmd ...string) string {
	t.Helper()
	args := []string{"run", "--rm"}
	if user != "" {
		args = append(args, "--user", user)
	}
	out, err := exec.Command("podman", append(append(args, image), cmd...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("run %v: %v\n%s", cmd, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestPodmanFinishesDebianAsTheAgentUser(t *testing.T) {
	final := finishOn(t, "docker.io/library/debian:bookworm-slim")
	if got := runIn(t, final, "id", "-u"); got != "1000" {
		t.Errorf("uid = %s", got)
	}
	if got := runIn(t, final, "sh", "-c", "echo $HOME:$OPENCODE_CONFIG:$(pwd)"); got != "/home/agent:/usr/local/share/dude/opencode.json:/home/agent" {
		t.Errorf("env = %s", got)
	}
}

func TestPodmanFinishesNodeWhoseUid1000IsNode(t *testing.T) {
	final := finishOn(t, "docker.io/library/node:24-bookworm-slim")
	if got := runIn(t, final, "id", "-u"); got != "1000" {
		t.Errorf("uid = %s", got)
	}
	if got := runIn(t, final, "sh", "-c", "getent passwd agent | cut -d: -f3,6"); got != "1000:/home/agent" {
		t.Errorf("agent = %s", got)
	}
	// node's passwd entry comes first for uid 1000 (home /home/node): run
	// by name or by uid, as a runtime may, HOME and the git identity are
	// still the agent's.
	for _, user := range []string{"", "1000", "1000:1000"} {
		if got := runAs(t, user, final, "sh", "-c", "echo $HOME; git config user.name; cd && pwd"); got != "/home/agent\ndude\n/home/agent" {
			t.Errorf("as %q: HOME and git identity = %q", user, got)
		}
	}
}

func TestPodmanFinishOnAnImageWithoutAShellSaysWhatItNeeds(t *testing.T) {
	c, registry := realPodman(t)
	stamp := time.Now().UnixNano()
	user := fmt.Sprintf("%s/dude/custom:noshell%d-user", registry, stamp)
	if log, err := buildIn(t, c, "FROM docker.io/library/busybox:1 AS b\nFROM scratch\nCOPY --from=b /bin/busybox /busybox\n", user); err != nil {
		t.Fatalf("%v\n%s", err, log)
	}
	digest, err := c.Push(context.Background(), user, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	log, err := buildIn(t, c, FinishContainerfile(Repository(user)+"@"+digest, layerFor(t, c, registry)), user+"-final")
	if err == nil {
		t.Fatalf("finish passed on an image with no /bin/sh\n%s", log)
	}
	if got := Failure("finishing", log, err, c.Limits.Memory, false); got != "the dude layer needs /bin/sh and glibc in the image" {
		t.Errorf("Failure = %q\n%s", got, log)
	}
}

// localPodman is rootless podman for tests that need no registry.
func localPodman(t *testing.T) CLI {
	t.Helper()
	if _, err := exec.LookPath("podman"); err != nil {
		t.Skip("podman is not on PATH")
	}
	return CLI{Limits: Limits{Platform: "", CPUs: 1.5, Memory: "1536m", Timeout: 10 * time.Minute}}
}

// localBuild builds containerfile as tag with local images only.
func localBuild(t *testing.T, containerfile, tag string) {
	t.Helper()
	dir, err := contextDir(containerfile)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	out, err := exec.Command("podman", "build", "--pull=missing", "-q", "-t", tag, "-f", dir+"/Containerfile", dir).CombinedOutput()
	if err != nil {
		t.Fatalf("build %s: %v\n%s", tag, err, out)
	}
	t.Cleanup(func() { _ = exec.Command("podman", "rmi", "--ignore", tag).Run() })
}

// localLayer is a stand-in dude layer, as layerFor, held locally.
func localLayer(t *testing.T) string {
	t.Helper()
	tag := fmt.Sprintf("localhost/dude-test-layer:%d", time.Now().UnixNano())
	script := base64.StdEncoding.EncodeToString([]byte(testSetup))
	localBuild(t, "FROM docker.io/library/busybox:1 AS files\n"+
		"RUN mkdir -p /rootfs/usr/local/share/dude && echo "+script+" | base64 -d > /rootfs/usr/local/share/dude/setup.sh"+
		" && echo '{}' > /rootfs/usr/local/share/dude/opencode.json\n"+
		"FROM scratch\nCOPY --from=files /rootfs/ /rootfs/\n", tag)
	return tag
}

// localFinish builds userContainerfile and finishes it with a stand-in
// dude layer, as a build job does, all locally.
func localFinish(t *testing.T, userContainerfile string) string {
	t.Helper()
	stamp := time.Now().UnixNano()
	user := fmt.Sprintf("localhost/dude-test-user:%d", stamp)
	localBuild(t, userContainerfile, user)
	final := fmt.Sprintf("localhost/dude-test-final:%d", stamp)
	localBuild(t, FinishContainerfile(user, localLayer(t)), final)
	return final
}

// staticSelf is dude-image-builder built static, as the release ships it,
// for CheckContainers to run in an image.
func staticSelf(t *testing.T) string {
	t.Helper()
	bin := t.TempDir() + "/dude-image-builder"
	cmd := exec.Command("go", "build", "-o", bin, "../../cmd/dude-image-builder")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building the builder: %v\n%s", err, out)
	}
	return bin
}

// The dude layer's step gives agent subordinate ids within a Run's 65536
// when the image has none, and keeps a line the image has for it.
func TestPodmanTheLayerGivesAgentSubordinateIdsARunCanMap(t *testing.T) {
	localPodman(t)
	for _, c := range []struct {
		name, base, want string
	}{
		{"debian, none", "docker.io/library/debian:bookworm-slim", "agent:1:999\nagent:1001:64535"},
		// node holds uid 1000 (agent is its second name) and has
		// node:100000:65536: agent's line is by uid, the image's kept.
		{"node, uid 1000 is node's", "docker.io/library/node:24-bookworm-slim", "node:100000:65536\n1000:1:999\n1000:1001:64535"},
		{"one of agent's own, kept", "docker.io/library/debian:bookworm-slim\nRUN printf 'agent:200:300\\n' > /etc/subuid && cp /etc/subuid /etc/subgid", "agent:200:300"},
	} {
		t.Run(c.name, func(t *testing.T) {
			final := localFinish(t, "FROM "+c.base+"\nRUN command -v git || (apt-get update && apt-get install -y --no-install-recommends git)\n")
			for _, f := range []string{"/etc/subuid", "/etc/subgid"} {
				if got := runIn(t, final, "cat", f); got != c.want {
					t.Errorf("%s = %q, want %q", f, got, c.want)
				}
			}
		})
	}
}

// On an image whose uid 1000 is node's, agent's lines by name are not the
// workload's: podman looks the caller up as node or 1000 and, finding
// neither, runs with a single id. The layer adds 1000's lines all the same,
// the check counts those alone, and podman maps them as the workload.
func TestPodmanAgentLinesBehindAnotherNameAreNotTheWorkloads(t *testing.T) {
	c := localPodman(t)
	c.Self = staticSelf(t)
	final := localFinish(t, "FROM docker.io/library/node:24-bookworm-slim\n"+
		"RUN apt-get update && apt-get install -y --no-install-recommends git podman fuse-overlayfs uidmap libcap2-bin"+
		" && chmod u-s /usr/bin/newuidmap /usr/bin/newgidmap"+
		" && setcap cap_setuid=ep /usr/bin/newuidmap && setcap cap_setgid=ep /usr/bin/newgidmap"+
		" && printf 'agent:1:999\\nagent:1001:64535\\n' > /etc/subuid && cp /etc/subuid /etc/subgid\n")
	if got := runIn(t, final, "cat", "/etc/subuid"); got != "agent:1:999\nagent:1001:64535\n1000:1:999\n1000:1001:64535" {
		t.Errorf("/etc/subuid = %q", got)
	}
	var log bytes.Buffer
	found, err := c.CheckContainers(context.Background(), final, &log)
	if err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	if got := strings.Join(found.Subuid, ","); got != "1000:1:999,1000:1001:64535" || !found.Passed() {
		t.Errorf("the check's subuid = %s, passed %v", got, found.Passed())
	}
	// As the workload, the map podman makes: its own id and both ranges.
	// The mappers carry file capabilities: under a rootless podman a
	// setuid-root newuidmap cannot write uid_map.
	if got := runAs(t, "1000", final, "sh", "-c", "podman unshare cat /proc/self/uid_map 2>/dev/null | wc -l"); got != "3" {
		t.Errorf("podman's uid_map as the workload has %s lines, want 3", got)
	}
}

// The check, run in a finished image as the builder runs it: podman,
// fuse-overlayfs and newuidmap/newgidmap with their file capabilities, and
// the layer's ids, pass; the same image without them fails, naming each.
func TestPodmanChecksAFinishedImageCanRunContainers(t *testing.T) {
	c := localPodman(t)
	c.Self = staticSelf(t)
	able := localFinish(t, "FROM docker.io/library/alpine:3\n"+
		"RUN apk add --no-cache git podman fuse-overlayfs shadow-uidmap libcap-setcap"+
		" && setcap cap_setuid=ep /usr/bin/newuidmap && setcap cap_setgid=ep /usr/bin/newgidmap\n")
	var log bytes.Buffer
	found, err := c.CheckContainers(context.Background(), able, &log)
	if err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	if !found.Passed() || !strings.HasPrefix(found.Detail(), "podman 5.") {
		t.Errorf("found %+v: %s", found, found.Detail())
	}
	if got := strings.Join(found.Subuid, ","); got != "agent:1:999,agent:1001:64535" {
		t.Errorf("subuid = %s", got)
	}
	// newuidmap and newgidmap without their capabilities: cp drops the
	// xattr Alpine's package ships them with.
	nocap := localFinish(t, "FROM docker.io/library/alpine:3\nRUN apk add --no-cache git podman fuse-overlayfs shadow-uidmap"+
		" && for m in newuidmap newgidmap; do cp /usr/bin/$m /tmp/$m && mv /tmp/$m /usr/bin/$m; done\n")
	found, err = c.CheckContainers(context.Background(), nocap, &log)
	if err != nil {
		t.Fatal(err)
	}
	if want := "Can't run containers: newuidmap has no cap_setuid file capability and newgidmap has no cap_setgid file capability."; found.Sentence() != want {
		t.Errorf("sentence = %q", found.Sentence())
	}
	plain := localFinish(t, "FROM docker.io/library/debian:bookworm-slim\nRUN apt-get update && apt-get install -y --no-install-recommends git\n")
	found, err = c.CheckContainers(context.Background(), plain, &log)
	if err != nil {
		t.Fatal(err)
	}
	if want := "Can't run containers: the image has no podman or rootless Docker, no fuse-overlayfs, and no newuidmap or newgidmap."; found.Passed() || found.Sentence() != want {
		t.Errorf("sentence = %q", found.Sentence())
	}
}

// expiring is a context whose deadline passes when expire is called.
type expiring struct {
	context.Context
	done chan struct{}
}

func (e *expiring) Done() <-chan struct{} { return e.done }
func (e *expiring) Err() error {
	select {
	case <-e.done:
		return context.DeadlineExceeded
	default:
		return nil
	}
}
func (e *expiring) expire() { close(e.done) }

// A check stopped while it runs, by the job's time limit or the builder
// stopping, leaves no container behind: here the image's /etc/subuid is a
// FIFO nothing writes, so the check blocks reading it until it is stopped.
func TestPodmanACancelledCheckLeavesNoContainer(t *testing.T) {
	c := localPodman(t)
	c.Self = staticSelf(t)
	image := fmt.Sprintf("localhost/dude-test-hang:%d", time.Now().UnixNano())
	localBuild(t, "FROM docker.io/library/alpine:3\nRUN echo agent:x:1000:1000::/home/agent:/bin/sh >> /etc/passwd && mkfifo /etc/subuid\n", image)
	running := func() string {
		out, _ := exec.Command("podman", "ps", "--filter", "ancestor="+image, "--filter", "status=running", "--format", "{{.Names}}").Output()
		return strings.TrimSpace(string(out))
	}
	// A podman client deaf to SIGTERM is killed once WaitDelay passes,
	// and its container outlives it: only the removal after it stops it.
	deaf := filepath.Join(t.TempDir(), "podman")
	if err := os.WriteFile(deaf, []byte("#!/bin/sh\ntrap '' TERM\npodman \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, how := range []string{"timeout", "shutdown", "a client deaf to SIGTERM"} {
		t.Run(how, func(t *testing.T) {
			c := c
			if how == "a client deaf to SIGTERM" {
				c.Bin = deaf
			}
			parent, cancel := context.WithCancel(context.Background())
			ctx, stop := context.Context(parent), cancel
			if how == "timeout" {
				e := &expiring{Context: parent, done: make(chan struct{})}
				ctx, stop = e, e.expire
			}
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := c.CheckContainers(ctx, image, &bytes.Buffer{})
				done <- err
			}()
			var name string
			for deadline := time.Now().Add(60 * time.Second); name == "" && time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
				name = running()
			}
			if name == "" {
				t.Fatal("the check's container never ran")
			}
			stop()
			if err := <-done; err == nil {
				t.Fatal("a stopped check returned no error")
			}
			if exec.Command("podman", "container", "exists", name).Run() == nil {
				t.Errorf("container %s is left after the check returned", name)
				_ = exec.Command("podman", "rm", "-f", "-t", "0", name).Run()
			}
		})
	}
}

// TestPodmanFinishOnAnImageWithoutAShellSaysWhatItNeeds, with no registry.
func TestPodmanLocalFinishWithoutAShellSaysWhatItNeeds(t *testing.T) {
	c := localPodman(t)
	user := fmt.Sprintf("localhost/dude-test-noshell:%d", time.Now().UnixNano())
	localBuild(t, "FROM docker.io/library/busybox:1 AS b\nFROM scratch\nCOPY --from=b /bin/busybox /busybox\n", user)
	dir, err := contextDir(FinishContainerfile(user, localLayer(t)))
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	out, err := exec.Command("podman", "build", "--pull=never", "-t", user+"-final", "-f", dir+"/Containerfile", dir).CombinedOutput()
	if err == nil {
		t.Fatalf("finish passed on an image with no /bin/sh\n%s", out)
	}
	if got := Failure("finishing", string(out), err, c.Limits.Memory, false); got != "the dude layer needs /bin/sh and glibc in the image" {
		t.Errorf("Failure = %q\n%s", got, out)
	}
}
