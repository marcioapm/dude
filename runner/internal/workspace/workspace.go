// Package workspace materializes Session Workspaces on a worker node.
//
// A Session Workspace is the durable working directory for a Run. It lives on
// the node, outside the container's writable layer, and is bind-mounted in —
// so a dead container does not destroy the work (plan §61):
//
//	container dies  ≠  workspace dies
//
// Speed comes from a node-local bare mirror per repository. Materializing a
// workspace is then a local clone against that mirror rather than a network
// fetch, which keeps "new Session feels instant" achievable (plan §68).
package workspace

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Layout inside a workspace. Fixed names because the container, the harness
// and the artifact uploader all address them.
const (
	DirRepos            = "repos"
	DirAgentState       = "agent-state"
	DirScratch          = "scratch"
	DirArtifactsStaging = "artifacts-staging"
	FileManifest        = "runtime-manifest.json"
)

// Repository describes one repo to materialize.
type Repository struct {
	Name          string
	URL           string
	DefaultBranch string
}

// MaterializedRepo records where a repo landed and at what commit.
type MaterializedRepo struct {
	Name string `json:"name"`
	Path string `json:"path"`
	// The real remote, so a push reaches the forge rather than the local
	// mirror the clone came from.
	URL     string `json:"url"`
	Branch  string `json:"branch"`
	HeadSHA string `json:"headSha"`
}

// Manifest is written into the workspace so anything inside the container can
// discover what it is working on without calling the control plane.
type Manifest struct {
	RunID        string             `json:"runId"`
	Organization string             `json:"organizationId"`
	Repos        []MaterializedRepo `json:"repos"`
	CreatedAt    string             `json:"createdAt"`
}

// Manager owns the node's workspace root and repository mirror cache.
type Manager struct {
	root string

	// Guards mirror updates per repository URL. Two Runs on the same repo
	// would otherwise fetch into the same bare mirror concurrently, which git
	// does not tolerate (plan §61, cache update locking).
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func NewManager(root string) *Manager {
	return &Manager{root: root, locks: make(map[string]*sync.Mutex)}
}

// Root is the base directory holding workspaces and the mirror cache.
func (m *Manager) Root() string { return m.root }

func (m *Manager) mirrorRoot() string    { return filepath.Join(m.root, "cache", "mirrors") }
func (m *Manager) workspaceRoot() string { return filepath.Join(m.root, "workspaces") }

// WorkspacePath is where a Run's workspace lives on this node.
func (m *Manager) WorkspacePath(runID string) string {
	return filepath.Join(m.workspaceRoot(), runID)
}

// lockFor returns the per-repository mutex, creating it on first use.
func (m *Manager) lockFor(key string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	if l, ok := m.locks[key]; ok {
		return l
	}
	l := &sync.Mutex{}
	m.locks[key] = l
	return l
}

// mirrorPath maps a clone URL to its local bare mirror directory. The name is
// derived from the URL so two repos with the same basename do not collide.
func (m *Manager) mirrorPath(url string) string {
	safe := strings.NewReplacer("/", "_", ":", "_", "@", "_", " ", "_").Replace(url)
	if len(safe) > 160 {
		safe = safe[len(safe)-160:]
	}
	return filepath.Join(m.mirrorRoot(), safe+".git")
}

func run(ctx context.Context, dir string, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	// Never block on credential or host-key prompts: fail fast instead.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=true")

	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, out)
	}
	return string(out), nil
}

// EnsureMirror creates or refreshes the node-local bare mirror for a repo.
//
// Serialized per URL, because concurrent fetches into one bare repository
// corrupt it.
func (m *Manager) EnsureMirror(ctx context.Context, repo Repository) (string, error) {
	path := m.mirrorPath(repo.URL)

	lock := m.lockFor(path)
	lock.Lock()
	defer lock.Unlock()

	if _, err := os.Stat(filepath.Join(path, "HEAD")); err == nil {
		// Refresh. A failure here is not fatal: a slightly stale mirror still
		// produces a valid workspace, and the Run should not die because the
		// remote was briefly unreachable.
		if _, err := run(ctx, path, "git", "remote", "update", "--prune"); err != nil {
			return path, nil
		}
		return path, nil
	}

	if err := os.MkdirAll(m.mirrorRoot(), 0o755); err != nil {
		return "", fmt.Errorf("create mirror root: %w", err)
	}
	if _, err := run(ctx, m.mirrorRoot(), "git", "clone", "--mirror", repo.URL, path); err != nil {
		return "", err
	}
	return path, nil
}

// Create materializes a Session Workspace for a Run.
//
// Existing workspaces are reused: a replacement container for the same Run
// must see the same working tree, including uncommitted changes.
// Create materializes a workspace for a Run.
//
// `baseRef` is the commit every repository is checked out at, which is how a
// phase builds on what the phase before it produced. Empty means each
// repository's own default branch — an implement phase, or a Run created
// directly through the API.
func (m *Manager) Create(
	ctx context.Context,
	organizationID, runID string,
	repos []Repository,
	baseRef string,
) (string, []MaterializedRepo, error) {
	wsPath := m.WorkspacePath(runID)

	for _, dir := range []string{DirRepos, DirAgentState, DirScratch, DirArtifactsStaging} {
		if err := os.MkdirAll(filepath.Join(wsPath, dir), 0o755); err != nil {
			return "", nil, fmt.Errorf("create workspace dir %s: %w", dir, err)
		}
	}

	materialized := make([]MaterializedRepo, 0, len(repos))
	for _, repo := range repos {
		out, err := m.materialize(ctx, wsPath, repo, baseRef)
		if err != nil {
			return "", nil, err
		}
		materialized = append(materialized, out)
	}

	manifest := Manifest{
		RunID:        runID,
		Organization: organizationID,
		Repos:        materialized,
		CreatedAt:    time.Now().UTC().Format(time.RFC3339),
	}
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return "", nil, fmt.Errorf("encode manifest: %w", err)
	}
	if err := os.WriteFile(filepath.Join(wsPath, FileManifest), encoded, 0o644); err != nil {
		return "", nil, fmt.Errorf("write manifest: %w", err)
	}

	return wsPath, materialized, nil
}

// materialize clones one repo from the node-local mirror into the workspace.
//
// An isolated checkout rather than a git worktree: worktrees share the parent
// repository's object store and refs, so an agent running destructive git
// commands could damage the cache other Runs depend on (plan §61).
func (m *Manager) materialize(
	ctx context.Context,
	wsPath string,
	repo Repository,
	baseRef string,
) (MaterializedRepo, error) {
	target := filepath.Join(wsPath, DirRepos, repo.Name)
	branch := repo.DefaultBranch
	if branch == "" {
		branch = "main"
	}

	if _, err := os.Stat(filepath.Join(target, ".git")); err == nil {
		/*
		 * Already materialized. Reuse it rather than discarding work — but
		 * only if it is at the ref this Run asked for. A workspace left at
		 * another phase's commit would silently give this agent the wrong
		 * code, which is worse than the cost of re-materializing.
		 */
		if baseRef != "" {
			current, err := run(ctx, target, "git", "rev-parse", "HEAD")
			if err != nil || strings.TrimSpace(current) != baseRef {
				if err := os.RemoveAll(target); err != nil {
					return MaterializedRepo{}, fmt.Errorf("discard stale checkout: %w", err)
				}
				return m.materialize(ctx, wsPath, repo, baseRef)
			}
		}

		sha, err := run(ctx, target, "git", "rev-parse", "HEAD")
		if err != nil {
			return MaterializedRepo{}, err
		}
		current, err := run(ctx, target, "git", "rev-parse", "--abbrev-ref", "HEAD")
		if err != nil {
			return MaterializedRepo{}, err
		}
		return MaterializedRepo{
			Name:    repo.Name,
			Path:    target,
			URL:     repo.URL,
			Branch:  strings.TrimSpace(current),
			HeadSHA: strings.TrimSpace(sha),
		}, nil
	}

	mirror, err := m.EnsureMirror(ctx, repo)
	if err != nil {
		return MaterializedRepo{}, err
	}

	// Local clone: no network, and hardlinked objects keep it cheap on disk.
	if _, err := run(ctx, wsPath, "git", "clone", "--local", "--no-hardlinks", mirror, target); err != nil {
		return MaterializedRepo{}, err
	}

	// Point the clone at the real remote so pushes reach the right place; the
	// mirror is an implementation detail the agent must not depend on.
	if _, err := run(ctx, target, "git", "remote", "set-url", "origin", repo.URL); err != nil {
		return MaterializedRepo{}, err
	}

	/*
	 * Check out what this phase was given. A ref rather than a branch when
	 * the workflow supplied one: a phase builds on the previous phase's
	 * commit, not on whatever its branch has moved to since.
	 */
	checkout := branch
	if baseRef != "" {
		checkout = baseRef
	}
	if _, err := run(ctx, target, "git", "checkout", checkout); err != nil {
		if baseRef != "" {
			// The ref the workflow named is not in this clone. Carrying on
			// at the default branch would run the agent against the wrong
			// code and report success.
			return MaterializedRepo{}, fmt.Errorf("checkout %s in %s: %w", baseRef, repo.Name, err)
		}
		// A repo without that branch (or with no commits) is still usable.
		branch = "HEAD"
	}

	sha, err := run(ctx, target, "git", "rev-parse", "HEAD")
	if err != nil {
		// An empty repository has no HEAD; that is not a failure.
		sha = ""
	}

	return MaterializedRepo{
		Name:    repo.Name,
		Path:    target,
		URL:     repo.URL,
		Branch:  strings.TrimSpace(branch),
		HeadSHA: strings.TrimSpace(sha),
	}, nil
}

// Remove deletes a workspace. The mirror cache is deliberately left alone: it
// is shared across Runs and is what makes the next startup fast.
func (m *Manager) Remove(runID string) error {
	return os.RemoveAll(m.WorkspacePath(runID))
}

// CachedRepositories lists mirrored repository URLs, reported in heartbeats so
// the scheduler can prefer nodes that already hold a project's repos.
//
// Always returns a non-nil slice: a nil slice marshals to JSON null, which is
// not the same as "this node caches nothing".
func (m *Manager) CachedRepositories() []string {
	out := []string{}
	entries, err := os.ReadDir(m.mirrorRoot())
	if err != nil {
		return out
	}
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, strings.TrimSuffix(e.Name(), ".git"))
		}
	}
	return out
}
