// Package runtime manages the Docker container that executes one Run.
//
// Isolation posture (plan §25, §17.1):
//   - the host Docker socket is never mounted into an agent container;
//   - containers drop all capabilities and disable privilege escalation;
//   - the workspace is the only writable bind mount;
//   - CPU, memory and PID limits are always set, so one Run cannot starve
//     the node.
//
// The container is disposable. The workspace it mounts is not.
package runtime

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
)

// DefaultImage is used when a project does not pin its own runtime image.
const DefaultImage = "ghcr.io/marciomartins/dude-runtime:latest"

// Where the Session Workspace appears inside the container.
const ContainerWorkspacePath = "/workspace"

// Limits bound a single Run's resource use.
type Limits struct {
	CPUs     float64 // e.g. 2.0 = two cores
	MemoryMB int64
	PidsMax  int64
}

func DefaultLimits() Limits {
	return Limits{CPUs: 2.0, MemoryMB: 4096, PidsMax: 512}
}

// Spec describes the container to create for a Run.
type Spec struct {
	RunID          string
	OrganizationID string
	Image          string
	HostWorkspace  string
	Limits         Limits
	Env            map[string]string
	// NetworkMode is "bridge" for trusted work and "none" for untrusted
	// repositories, which must not reach the network at all (plan §47).
	NetworkMode string
}

// Manager creates and destroys Run containers.
type Manager struct {
	docker *client.Client
}

func NewManager() (*Manager, error) {
	// FromEnv honours DOCKER_HOST and friends, so the same binary works
	// against a local daemon or a remote one.
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("connect to docker: %w", err)
	}
	return &Manager{docker: cli}, nil
}

func (m *Manager) Close() error { return m.docker.Close() }

// Ping verifies the daemon is reachable, so the runner fails at startup
// rather than on the first Run.
func (m *Manager) Ping(ctx context.Context) error {
	if _, err := m.docker.Ping(ctx); err != nil {
		return fmt.Errorf("docker ping: %w", err)
	}
	return nil
}

// ContainerName is derived from the Run so a restarted runner can find and
// reconcile containers it previously created.
func ContainerName(runID string) string {
	return "dude-run-" + strings.ToLower(runID)
}

// EnsureImage pulls the image if it is not already present locally.
func (m *Manager) EnsureImage(ctx context.Context, ref string) error {
	if _, err := m.docker.ImageInspect(ctx, ref); err == nil {
		return nil
	}

	reader, err := m.docker.ImagePull(ctx, ref, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("pull %s: %w", ref, err)
	}
	defer reader.Close()

	// The pull is only complete once the response body is drained.
	if _, err := io.Copy(io.Discard, reader); err != nil {
		return fmt.Errorf("pull %s: %w", ref, err)
	}
	return nil
}

// Created is a started container.
type Created struct {
	ContainerID string
	ImageDigest string
}

// Create starts the container for a Run.
//
// An existing container with the same name is reused when running, or removed
// when dead, so a runner restart converges rather than colliding.
func (m *Manager) Create(ctx context.Context, spec Spec) (*Created, error) {
	name := ContainerName(spec.RunID)

	if existing, err := m.docker.ContainerInspect(ctx, name); err == nil {
		if existing.State != nil && existing.State.Running {
			return &Created{ContainerID: existing.ID, ImageDigest: existing.Image}, nil
		}
		if err := m.docker.ContainerRemove(ctx, existing.ID,
			container.RemoveOptions{Force: true}); err != nil {
			return nil, fmt.Errorf("remove stale container: %w", err)
		}
	}

	img := spec.Image
	if img == "" {
		img = DefaultImage
	}
	if err := m.EnsureImage(ctx, img); err != nil {
		return nil, err
	}

	limits := spec.Limits
	if limits.CPUs == 0 {
		limits = DefaultLimits()
	}

	env := make([]string, 0, len(spec.Env)+2)
	env = append(env, "DUDE_RUN_ID="+spec.RunID, "DUDE_WORKSPACE="+ContainerWorkspacePath)
	for k, v := range spec.Env {
		env = append(env, k+"="+v)
	}

	networkMode := spec.NetworkMode
	if networkMode == "" {
		networkMode = "bridge"
	}

	hostConfig := &container.HostConfig{
		// The workspace is the only writable mount. No docker.sock: mounting
		// it would hand the agent root on the host (plan §25.4, §34.9).
		Binds:       []string{spec.HostWorkspace + ":" + ContainerWorkspacePath + ":rw"},
		NetworkMode: container.NetworkMode(networkMode),
		Resources: container.Resources{
			NanoCPUs: int64(limits.CPUs * 1e9),
			Memory:   limits.MemoryMB * 1024 * 1024,
			PidsLimit: func() *int64 {
				v := limits.PidsMax
				return &v
			}(),
		},
		CapDrop: []string{"ALL"},
		SecurityOpt: []string{
			// Blocks setuid escalation inside the container.
			"no-new-privileges:true",
		},
		// Never resurrect an agent container behind the control plane's back.
		RestartPolicy: container.RestartPolicy{Name: "no"},
	}

	config := &container.Config{
		Image:      img,
		Env:        env,
		WorkingDir: ContainerWorkspacePath,
		Labels: map[string]string{
			"dude.run_id":          spec.RunID,
			"dude.organization_id": spec.OrganizationID,
			"dude.managed":         "true",
		},
		// Hold the container open; the harness is driven through exec.
		Cmd:          []string{"sleep", "infinity"},
		Tty:          false,
		ExposedPorts: nat.PortSet{},
	}

	created, err := m.docker.ContainerCreate(ctx, config, hostConfig, nil, nil, name)
	if err != nil {
		return nil, fmt.Errorf("create container: %w", err)
	}

	if err := m.docker.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		// Do not leave a created-but-unstarted container behind.
		_ = m.docker.ContainerRemove(ctx, created.ID, container.RemoveOptions{Force: true})
		return nil, fmt.Errorf("start container: %w", err)
	}

	inspected, err := m.docker.ContainerInspect(ctx, created.ID)
	if err != nil {
		return &Created{ContainerID: created.ID}, nil
	}
	return &Created{ContainerID: created.ID, ImageDigest: inspected.Image}, nil
}

// ExecResult is the outcome of a command run inside a container.
type ExecResult struct {
	ExitCode int
	Output   string
}

// Exec runs a command inside the Run container and returns its combined
// output. Used to drive the harness and to run deterministic tooling.
func (m *Manager) Exec(ctx context.Context, containerID string, cmd []string) (*ExecResult, error) {
	created, err := m.docker.ContainerExecCreate(ctx, containerID, container.ExecOptions{
		Cmd:          cmd,
		AttachStdout: true,
		AttachStderr: true,
		WorkingDir:   ContainerWorkspacePath,
	})
	if err != nil {
		return nil, fmt.Errorf("exec create: %w", err)
	}

	attached, err := m.docker.ContainerExecAttach(ctx, created.ID, container.ExecAttachOptions{})
	if err != nil {
		return nil, fmt.Errorf("exec attach: %w", err)
	}
	defer attached.Close()

	// Bounded: a command that floods stdout must not exhaust runner memory.
	out, err := io.ReadAll(io.LimitReader(attached.Reader, 16<<20))
	if err != nil {
		return nil, fmt.Errorf("exec read: %w", err)
	}

	inspected, err := m.docker.ContainerExecInspect(ctx, created.ID)
	if err != nil {
		return nil, fmt.Errorf("exec inspect: %w", err)
	}
	return &ExecResult{ExitCode: inspected.ExitCode, Output: string(out)}, nil
}

// Stop stops and removes the container for a Run. Safe to call when the
// container is already gone.
func (m *Manager) Stop(ctx context.Context, runID string) error {
	name := ContainerName(runID)

	timeout := 10
	if err := m.docker.ContainerStop(ctx, name, container.StopOptions{Timeout: &timeout}); err != nil {
		if client.IsErrNotFound(err) {
			return nil
		}
		// Fall through to removal: a container that refused to stop should
		// still be forcibly removed rather than leaked.
	}

	if err := m.docker.ContainerRemove(ctx, name, container.RemoveOptions{Force: true}); err != nil {
		if client.IsErrNotFound(err) {
			return nil
		}
		return fmt.Errorf("remove container: %w", err)
	}
	return nil
}

// ListManaged returns run IDs for containers this runner owns, so a restarted
// runner can reconcile what is actually on the node against what the control
// plane believes.
func (m *Manager) ListManaged(ctx context.Context) (map[string]string, error) {
	containers, err := m.docker.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}

	out := make(map[string]string)
	for _, c := range containers {
		if c.Labels["dude.managed"] != "true" {
			continue
		}
		if runID := c.Labels["dude.run_id"]; runID != "" {
			out[runID] = c.ID
		}
	}
	return out, nil
}

// WaitHealthy blocks until the container reports running, or the timeout
// elapses.
func (m *Manager) WaitHealthy(ctx context.Context, containerID string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		inspected, err := m.docker.ContainerInspect(ctx, containerID)
		if err != nil {
			return fmt.Errorf("inspect: %w", err)
		}
		if inspected.State != nil && inspected.State.Running {
			return nil
		}
		if inspected.State != nil && inspected.State.Dead {
			return fmt.Errorf("container died during startup")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	return fmt.Errorf("container did not become healthy within %s", timeout)
}
