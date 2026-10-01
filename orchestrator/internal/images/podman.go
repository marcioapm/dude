package images

import (
	"bytes"
	"context"
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
	// Prune removes images nothing has used for a day.
	Prune(ctx context.Context, log io.Writer) error
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
	return c.run(ctx, log, "image", "prune", "-a", "-f", "--filter", "until=24h")
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
