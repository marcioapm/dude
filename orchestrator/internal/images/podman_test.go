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
	// What was pushed can be pulled back by that digest.
	ref := Repository(tag) + "@" + digest
	if out, err := exec.Command("podman", "pull", "--tls-verify=false", ref).CombinedOutput(); err != nil {
		t.Fatalf("pull %s: %v\n%s", ref, err, out)
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
git --version >/dev/null 2>&1 || { echo "dude: the image needs git: agents commit with it"; exit 1; }
`

func runIn(t *testing.T, image string, cmd ...string) string {
	t.Helper()
	out, err := exec.Command("podman", append([]string{"run", "--rm", image}, cmd...)...).CombinedOutput()
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
