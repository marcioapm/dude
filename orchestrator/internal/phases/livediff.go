package phases

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/ledger"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// The live diff: what the agent's checkout looks like now, against the
// commit it started from, while it works.
//
// lux has no notion of a diff, and dude's own view of the checkout ends at
// what was pushed. So the orchestrator asks the container itself, through
// lux's exec: `git diff <base>` and the untracked files. It asks shortly
// after the agent reports an edit (the stream says so, as a tool call), and
// every so often besides — a shell command changes files as well, and says
// nothing about it. Only a change is recorded, as the Run's latest diff
// (run_diffs) and a run.diff.updated event, which is how the browser hears
// of it. When the Run ends the last diff stays.

// EvDiffUpdated is the ledger event for a Run's new live diff.
const EvDiffUpdated = "run.diff.updated"

// Defaults for how soon after an edit, and how often otherwise, the diff is
// read (Syncer.DiffDelay, DiffEvery).
const (
	defaultDiffDelay = 1500 * time.Millisecond
	defaultDiffEvery = 15 * time.Second
)

// How long one read of the diff may take: a checkout too big to diff in
// this long is not worth holding a stream open for.
const diffTimeout = 20 * time.Second

// editTools are the tools that change files, by the names the translator
// records them under: OpenCode's own, and ACP's kind for them.
var editTools = map[string]bool{"edit": true, "write": true, "patch": true, "multiedit": true, "apply_patch": true}

// isEdit says whether a recorded tool call changes files.
func isEdit(name string) bool { return editTools[strings.ToLower(name)] }

// RunDiff is a Run's live diff, as stored and as the API serves it.
type RunDiff struct {
	Base      string     `json:"base"`
	Files     []DiffFile `json:"files"`
	UpdatedAt time.Time  `json:"updatedAt"`
}

// watchDiff calls refresh a moment (delay) after a poke — once for a burst
// of them — and every `every` besides, until ctx ends. A throttle rather
// than a debounce: an agent editing steadily would hold a debounce off for
// as long as it works, which is exactly when the diff is changing.
func watchDiff(ctx context.Context, poke <-chan struct{}, delay, every time.Duration, refresh func(context.Context)) {
	tick := time.NewTicker(every)
	defer tick.Stop()
	var soon <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-poke:
			if soon == nil {
				soon = time.After(delay)
			}
		case <-soon:
			soon = nil
			refresh(ctx)
			tick.Reset(every)
		case <-tick.C:
			refresh(ctx)
		}
	}
}

// pokeDiff asks for a Run's diff to be read soon: it has edited something,
// or finished a turn. Never blocks; a poke already pending covers it.
func (s *Syncer) pokeDiff(runID string) {
	s.mu.Lock()
	f := s.following[runID]
	s.mu.Unlock()
	if f == nil || f.poke == nil {
		return
	}
	select {
	case f.poke <- struct{}{}:
	default:
	}
}

func (s *Syncer) diffTiming() (time.Duration, time.Duration) {
	delay, every := s.DiffDelay, s.DiffEvery
	if delay <= 0 {
		delay = defaultDiffDelay
	}
	if every <= 0 {
		every = defaultDiffEvery
	}
	return delay, every
}

// refreshDiff reads the Run's checkout through lux and records its diff if
// it changed. Anything in the way — the container not running yet or any
// more, lux unreachable, git failing — leaves the last diff as it was: the
// next poke or tick tries again.
func (s *Syncer) refreshDiff(ctx context.Context, r phaseRun) error {
	var bases map[string]string
	var luxState string
	if err := s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT base_shas, COALESCE(lux_state, '') FROM runs WHERE id = $1`, r.ID).Scan(&bases, &luxState)
	}); err != nil {
		return err
	}
	// Until lux reports the checkout, there is nothing to diff against.
	if luxState != "running" || len(bases) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, diffTimeout)
	defer cancel()
	repos := slices.Sorted(maps.Keys(bases))
	diff := RunDiff{Base: bases[repos[0]], Files: []DiffFile{}}
	for _, repo := range repos {
		res, err := s.Lux.Exec(ctx, r.LuxRunID, diffCommand(RepoPath(repo), bases[repo]))
		if err != nil {
			return err
		}
		if res.ExitCode != 0 {
			return fmt.Errorf("git diff in %s exited %d: %s", repo, res.ExitCode, strings.TrimSpace(string(res.Stderr)))
		}
		files := ParseDiff(string(res.Stdout))
		for i := range files {
			// With several repositories, each path says which.
			if len(repos) > 1 {
				files[i].Path = repo + "/" + files[i].Path
			}
		}
		diff.Files = append(diff.Files, files...)
	}
	return s.recordDiff(ctx, r, diff)
}

// recordDiff stores a Run's diff and announces it, only if it differs from
// the one stored: a quiet agent is read every few seconds and must not fill
// the ledger with the same diff.
func (s *Syncer) recordDiff(ctx context.Context, r phaseRun, diff RunDiff) error {
	files, err := json.Marshal(diff.Files)
	if err != nil {
		return err
	}
	return s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		var at time.Time
		err := tx.QueryRow(ctx, `INSERT INTO run_diffs (run_id, organization_id, base, files) VALUES ($1, $2, $3, $4::jsonb)
			ON CONFLICT (run_id) DO UPDATE SET base = EXCLUDED.base, files = EXCLUDED.files, updated_at = now()
			WHERE run_diffs.base IS DISTINCT FROM EXCLUDED.base OR run_diffs.files IS DISTINCT FROM EXCLUDED.files
			RETURNING updated_at`, r.ID, r.Org, diff.Base, files).Scan(&at)
		if err == pgx.ErrNoRows {
			return nil // unchanged
		}
		if err != nil {
			return err
		}
		return s.event(ctx, tx, r, EvDiffUpdated, ledger.ActorAgent,
			map[string]any{"base": diff.Base, "files": json.RawMessage(files), "updatedAt": at})
	})
}

// lastDiff reads the diff once more before the container stops, so what
// stays is what the agent left — bounded, and best-effort: finishing a Run
// does not wait on it.
func (s *Syncer) lastDiff(ctx context.Context, r phaseRun) {
	if r.LuxRunID == "" || lux.Terminal(r.LuxState) {
		return
	}
	if err := s.refreshDiff(ctx, r); err != nil {
		s.Log.Debug("reading the last diff failed", "run", r.ID, "error", err)
	}
}
