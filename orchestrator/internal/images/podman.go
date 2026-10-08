package images

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Podman is what the builder asks of podman. Run by CLI (CLI); tests of the
// queue use a fake, and podman_test.go the real thing.
type Podman interface {
	// Build builds the Containerfile in dir as tag, writing its output to log.
	Build(ctx context.Context, dir, tag string, args map[string]string, log io.Writer) error
	// Push pushes tag and returns the digest the registry stored (sha256:…).
	Push(ctx context.Context, tag string, log io.Writer) (string, error)
	// FreeBytes is the free space where podman keeps images.
	FreeBytes(ctx context.Context) (int64, error)
	// Prune removes every image no container uses: base images and the
	// dude layer come back with a pull, and everything built is in the
	// registry.
	Prune(ctx context.Context, log io.Writer) error
	// Remove untags tag, removing its image when nothing else names it.
	Remove(ctx context.Context, tag string, log io.Writer) error
	// Controllers are the cgroup controllers podman can apply limits with.
	Controllers(ctx context.Context) ([]string, error)
	// CheckContainers runs the container check (ContainersCheck) in image,
	// a local tag, and returns what it found.
	CheckContainers(ctx context.Context, image string, log io.Writer) (Found, error)
}

// Limits are each build's: what a build may use of the dude host.
type Limits struct {
	Platform string
	CPUs     float64
	// podman's notation: 1536m.
	Memory  string
	Timeout time.Duration
}

// CLI runs the podman on PATH, rootless as the builder's own user.
type CLI struct {
	Limits Limits
	// The registry's auth file (DUDE_BUILDER_AUTHFILE); "" for podman's own.
	Authfile string
	// false only for a local registry over plain HTTP, in tests.
	TLSVerify bool
	// The podman binary; "podman" when "".
	Bin string
	// A static build of dude-image-builder, run in an image to check it
	// can run containers (CheckCommand); the builder's own executable.
	Self string
}

func (c CLI) bin() string {
	if c.Bin == "" {
		return "podman"
	}
	return c.Bin
}

func (c CLI) auth() []string {
	var out []string
	if c.Authfile != "" {
		out = append(out, "--authfile", c.Authfile)
	}
	if !c.TLSVerify {
		out = append(out, "--tls-verify=false")
	}
	return out
}

// BuildArgs is the podman build command line for dir, tag and args: the
// limits apply to every RUN step. podman build has no --cpus or
// --pids-limit: CPUs are a CFS quota over a 100 ms period, and processes
// are capped with the nproc ulimit, the closest it offers.
func (c CLI) BuildArgs(dir, tag string, args map[string]string) []string {
	quota := int64(c.Limits.CPUs * 100000)
	out := []string{"build",
		"--platform", c.Limits.Platform,
		"--cpu-period", "100000", "--cpu-quota", strconv.FormatInt(quota, 10),
		"--memory", c.Limits.Memory, "--memory-swap", c.Limits.Memory,
		"--ulimit", "nproc=4096:4096",
		"--pull=newer", "--layers=false", "--force-rm",
	}
	out = append(out, c.auth()...)
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		out = append(out, "--build-arg", k+"="+args[k])
	}
	return append(out, "-t", tag, "-f", filepath.Join(dir, "Containerfile"), dir)
}

func (c CLI) Build(ctx context.Context, dir, tag string, args map[string]string, log io.Writer) error {
	return c.run(ctx, log, c.BuildArgs(dir, tag, args)...)
}

func (c CLI) Push(ctx context.Context, tag string, log io.Writer) (string, error) {
	f, err := os.CreateTemp("", "dude-digest-")
	if err != nil {
		return "", err
	}
	f.Close()
	defer os.Remove(f.Name())
	args := append([]string{"push"}, c.auth()...)
	args = append(args, "--digestfile", f.Name(), tag)
	if err := c.run(ctx, log, args...); err != nil {
		return "", err
	}
	raw, err := os.ReadFile(f.Name())
	if err != nil {
		return "", err
	}
	digest := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(digest, "sha256:") {
		return "", fmt.Errorf("podman push wrote no digest (%q)", digest)
	}
	return digest, nil
}

func (c CLI) FreeBytes(ctx context.Context) (int64, error) {
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, c.bin(), "info", "--format", "{{.Store.GraphRoot}}")
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return 0, fmt.Errorf("podman info: %w", err)
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(strings.TrimSpace(out.String()), &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * int64(st.Bsize), nil //nolint:unconvert // the field types differ by OS
}

func (c CLI) Prune(ctx context.Context, log io.Writer) error {
	return c.run(ctx, log, "image", "prune", "-a", "-f")
}

func (c CLI) Remove(ctx context.Context, tag string, log io.Writer) error {
	return c.run(ctx, log, "rmi", "--ignore", tag)
}

// CheckContainers runs Self's CheckCommand in image, as root, offline,
// under the build's memory limit, with nothing of the host but Self.
// Cancelled (a timeout, the builder stopping), podman is asked to stop
// first, and the container is removed whatever happened: a killed podman
// client leaves its container running.
func (c CLI) CheckContainers(ctx context.Context, image string, log io.Writer) (Found, error) {
	if c.Self == "" {
		return Found{}, errors.New("the builder cannot check containers: it does not know its own executable")
	}
	name := "dude-check-" + randomHex(8)
	defer c.removeContainer(ctx, name, log)
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, c.bin(), "run", "--rm", "--name", name, "--pull=never", "--network=none", "--user=0:0",
		"--security-opt=label=disable", "--memory", c.Limits.Memory,
		"--volume", c.Self+":/.dude-check:ro", "--entrypoint", "/.dude-check", image, CheckCommand)
	cmd.Stdout, cmd.Stderr = &out, log
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 10 * time.Second
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return Found{}, ctx.Err()
		}
		return Found{}, fmt.Errorf("the container check could not run in the image: %w", err)
	}
	return parseFound(out.Bytes())
}

// checkRemoveTimeout bounds removing a check's container, on a context of
// its own: the job's may be done.
const checkRemoveTimeout = 30 * time.Second

func (c CLI) removeContainer(ctx context.Context, name string, log io.Writer) {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), checkRemoveTimeout)
	defer cancel()
	cmd := exec.CommandContext(rctx, c.bin(), "rm", "--force", "--ignore", "--time", "0", name)
	cmd.Stdout, cmd.Stderr = io.Discard, log
	cmd.WaitDelay = 5 * time.Second
	_ = cmd.Run()
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (c CLI) Controllers(ctx context.Context) ([]string, error) {
	var out, errb bytes.Buffer
	cmd := exec.CommandContext(ctx, c.bin(), "info", "--format", "{{json .Host.CgroupControllers}}")
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("podman info: %w: %s", err, strings.TrimSpace(errb.String()))
	}
	var list []string
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &list); err != nil {
		return nil, fmt.Errorf("podman info: reading its cgroup controllers %q: %w", strings.TrimSpace(out.String()), err)
	}
	return list, nil
}

// CheckLimits refuses a podman that cannot apply a build's CPU and memory
// limits: without the cpu and memory cgroup controllers delegated to the
// builder's user, podman only warns and builds unlimited.
func CheckLimits(ctx context.Context, p Podman) error {
	have, err := p.Controllers(ctx)
	if err != nil {
		return err
	}
	var missing []string
	for _, c := range []string{"cpu", "memory"} {
		if !slices.Contains(have, c) {
			missing = append(missing, c)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("podman cannot limit builds: the %s cgroup controller%s not delegated to this user (podman has %v); "+
			"delegate cpu and memory to its user@.service", strings.Join(missing, " and "), map[bool]string{true: "s are", false: " is"}[len(missing) > 1], have)
	}
	return nil
}

func (c CLI) run(ctx context.Context, log io.Writer, args ...string) error {
	cmd := exec.CommandContext(ctx, c.bin(), args...)
	cmd.Stdout, cmd.Stderr = log, log
	// podman's children hold the pipes; a killed podman must not hang Wait.
	cmd.WaitDelay = 10 * time.Second
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	err := cmd.Run()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// errTimeout marks a build that ran past its time limit.
var errTimeout = errors.New("timed out")
