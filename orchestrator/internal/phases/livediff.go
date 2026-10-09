package phases

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/ledger"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// The live diff: what the agent's checkout looks like now, against the
// commit it started from.
//
// lux has no notion of a diff, and dude's own view of the checkout ends at
// what was pushed, so the checkout is asked, by one script (diffScript),
// two ways:
//
//   - While the agent works, the orchestrator runs it through lux's exec:
//     shortly after the agent reports an edit or ends a turn, and every so
//     often while it works (a shell command changes files and says
//     nothing) — never while it is idle or waiting on a person, and less
//     often after several reads that found nothing new.
//   - When the container stops, for any reason lux can see coming — dude's
//     finish, pause, park or abort, lux's own timeout or drain — lux runs
//     it as the Run's beforeStop hook, which publishes the diff as
//     artifacts of the Run. The artifact collector records that as the final
//     diff. One mechanism for every stop; a crash or a lost host runs no
//     hook, and leaves the last live diff.
//
// Every read is hashed first; one identical to the last recorded is
// dropped before it is parsed, so a quiet agent costs no write. The
// run.diff.updated event carries a summary — paths, statuses, counts and
// the checksum — and the hunks stay in run_diffs, one row per Run, for
// the browser to fetch when a summary says something changed.

// EvDiffUpdated is the ledger event for a Run's new diff.
const EvDiffUpdated = "run.diff.updated"

// Defaults for how soon after an edit the diff is read, how often while
// the agent works, and how often once several reads found nothing new
// (Syncer.DiffDelay, DiffEvery, DiffSlow).
const (
	defaultDiffDelay = 1500 * time.Millisecond
	defaultDiffEvery = 15 * time.Second
	defaultDiffSlow  = time.Minute
	// Unchanged periodic reads before they slow down.
	diffBackoffAfter = 4
)

// How long one live read may take.
const diffTimeout = 20 * time.Second

// FinalDiffTimeout bounds the beforeStop hook: lux holds the stop for it.
const FinalDiffTimeout = "10s"

// editTools are the tools that change files, by the names the translator
// records them under: OpenCode's own, and ACP's kind for them.
var editTools = map[string]bool{"edit": true, "write": true, "patch": true, "multiedit": true, "apply_patch": true}

// isEdit says whether a recorded tool call changes files.
func isEdit(name string) bool { return editTools[strings.ToLower(name)] }

// RunDiff is a Run's diff as run_diffs stores it; the API adds when, and
// whether it is final.
type RunDiff struct {
	Base     string
	Files    []DiffFile
	Checksum string
}

// diffState is what the syncer remembers of a Run's live diff: the
// checksum of the last one recorded, seeded from run_diffs the first time,
// so a restarted orchestrator still skips an unchanged read. Its lock
// keeps two reads of one Run from running at once.
type diffState struct {
	mu       sync.Mutex
	loaded   bool
	checksum string
}

func (s *Syncer) diffState(runID string) *diffState {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.diffs == nil {
		s.diffs = map[string]*diffState{}
	}
	st := s.diffs[runID]
	if st == nil {
		st = &diffState{}
		s.diffs[runID] = st
	}
	return st
}

// diffTimings are the syncer's, or the defaults.
func (s *Syncer) diffTimings() (delay, every, slow time.Duration) {
	delay, every, slow = s.DiffDelay, s.DiffEvery, s.DiffSlow
	if delay <= 0 {
		delay = defaultDiffDelay
	}
	if every <= 0 {
		every = defaultDiffEvery
	}
	if slow <= 0 {
		slow = defaultDiffSlow
	}
	return delay, every, slow
}

// watchDiff reads a Run's diff until ctx ends: a moment (delay) after a
// poke — once for a burst of them — and every `every` besides, slowing to
// `slow` after diffBackoffAfter periodic reads that found nothing new (or
// were skipped, the agent not working). A poke brings the pace back. A
// throttle rather than a debounce: an agent editing steadily would hold a
// debounce off for as long as the diff changes. read says whether the
// diff changed; periodic is false for a read after a poke.
func watchDiff(ctx context.Context, poke <-chan struct{}, delay, every, slow time.Duration,
	read func(ctx context.Context, periodic bool) bool) {
	tick := time.NewTicker(every)
	defer tick.Stop()
	var soon <-chan time.Time
	unchanged := 0
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
			read(ctx, false)
			unchanged = 0
			tick.Reset(every)
		case <-tick.C:
			if read(ctx, true) {
				unchanged = 0
				tick.Reset(every)
			} else if unchanged++; unchanged == diffBackoffAfter {
				tick.Reset(slow)
			}
		}
	}
}

// pokeDiff asks for a Run's diff to be read soon: it has edited something,
// or finished a turn. Never blocks; a poke already pending covers it.
func (s *Syncer) pokeDiff(runID string) {
	s.mu.Lock()
	f := s.following[runID]
	s.mu.Unlock()
	if f == nil {
		return
	}
	select {
	case f.poke <- struct{}{}:
	default:
	}
}

// readDiff reads the Run's checkout through lux's exec and records its
// diff if it changed, saying whether it did. A periodic read happens only
// while the agent is working. Anything in the way — the container not
// running yet or any more, lux unreachable — leaves the last diff as it was.
func (s *Syncer) readDiff(ctx context.Context, r phaseRun, periodic bool) (bool, error) {
	st := s.diffState(r.ID)
	st.mu.Lock()
	defer st.mu.Unlock()

	var refs map[string]string
	var luxState string
	var working bool
	var stored *string
	var epoch int
	if err := s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT r.base_shas, COALESCE(r.lux_state, ''),
				r.status = 'running' AND r.agent_busy_at IS NOT NULL AND r.turn_done_at IS NULL AND r.waiting_since IS NULL,
				-- A final diff is from a stop; after a resume the live one replaces it.
				(SELECT d.checksum FROM run_diffs d WHERE d.run_id = r.id AND NOT d.final),
				r.agent_session_epoch
			FROM runs r WHERE r.id = $1`, r.ID).Scan(&refs, &luxState, &working, &stored, &epoch)
	}); err != nil {
		return false, err
	}
	if !st.loaded && stored != nil {
		st.checksum = *stored
	}
	st.loaded = true
	// Until lux reports the checkout there is nothing to diff against; and
	// an agent not working changes nothing.
	if luxState != "running" || len(refs) == 0 || periodic && !working {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(ctx, diffTimeout)
	defer cancel()
	res, err := s.Lux.Exec(ctx, r.LuxRunID, diffCommand("-", refs))
	if err != nil {
		return false, err
	}
	if res.ExitCode != 0 {
		return false, fmt.Errorf("the diff exited %d: %s", res.ExitCode, strings.TrimSpace(string(res.Stderr)))
	}
	sum := diffChecksum(res.Stdout)
	if sum == st.checksum {
		return false, nil // the same as the last: not parsed, not written
	}
	text := string(res.Stdout)
	// The placement is the session's: until a resumed one reports its
	// session, a read counts as the last one's and yields to its final diff.
	changed, err := recordRunDiff(ctx, s.DB.InOrg, r.Org, r.ProjectID, r.TaskID, r.ID, text, epoch, false)
	if changed {
		st.checksum = sum
	}
	return changed, err
}

// inOrg is db.DB.InOrg, so the artifact collector can record too.
type inOrg func(ctx context.Context, org string, fn func(pgx.Tx) error) error

// recordRunDiff records what diffScript printed as the Run's diff, from lux
// placement epoch, replacing the last unless that is newer: from a later
// placement, or its placement's final diff where this is a live read.
// Announced with a summary — which files, how, and how much; the hunks are
// fetched from the API by whoever is looking.
func recordRunDiff(ctx context.Context, in inOrg, org, projectID, taskID, runID, text string, epoch int, final bool) (bool, error) {
	diff := parseRunDiff(text)
	files, err := json.Marshal(diff.Files)
	if err != nil {
		return false, err
	}
	summary := make([]map[string]any, len(diff.Files))
	for i, f := range diff.Files {
		summary[i] = map[string]any{"path": f.Path, "status": f.Status, "additions": f.Additions, "deletions": f.Deletions}
	}
	changed := false
	err = in(ctx, org, func(tx pgx.Tx) error {
		var at time.Time
		var existed bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM run_diffs WHERE run_id = $1)`, runID).Scan(&existed); err != nil {
			return err
		}
		// A final diff identical to the live one only marks it final.
		err := tx.QueryRow(ctx, `INSERT INTO run_diffs (run_id, organization_id, base, files, checksum, final, epoch)
			VALUES ($1, $2, $3, $4::jsonb, $5, $6, $7)
			ON CONFLICT (run_id) DO UPDATE SET base = EXCLUDED.base, files = EXCLUDED.files,
				checksum = EXCLUDED.checksum, final = EXCLUDED.final, epoch = EXCLUDED.epoch, updated_at = now()
			WHERE (run_diffs.checksum <> EXCLUDED.checksum OR run_diffs.final <> EXCLUDED.final)
				AND (EXCLUDED.epoch > run_diffs.epoch
					OR EXCLUDED.epoch = run_diffs.epoch AND (EXCLUDED.final OR NOT run_diffs.final))
			RETURNING updated_at`, runID, org, diff.Base, files, diff.Checksum, final, epoch).Scan(&at)
		if err == pgx.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
		changed = true
		// A new checksum is the Run's files changing: progress, for the
		// no-progress check (stalls.go). The first read of a checkout the
		// agent has not touched is where it started, not a change.
		if !final && (existed || len(diff.Files) > 0) {
			if _, err := tx.Exec(ctx, `UPDATE runs SET files_changed_at = now() WHERE id = $1`, runID); err != nil {
				return err
			}
		}
		_, err = ledger.Append(ctx, tx, ledger.Event{
			Type: EvDiffUpdated, OrganizationID: org, ProjectID: projectID, TaskID: taskID, RunID: runID,
			ActorType: ledger.ActorAgent, ActorID: runID, Source: ledger.SourceRunner, CorrelationID: taskID,
			Payload: map[string]any{"checksum": diff.Checksum, "updatedAt": at, "final": final, "files": summary},
		})
		return err
	})
	return changed, err
}

// beforeStop is the hook every phase Run's spec carries: lux runs it in the
// container whenever it stops the Run, and it leaves the final diff where
// the artifact collector finds it (FinalDiffPrefix). refs is each
// repository's starting point as the spec names it — the commit, or the
// branch lux checks out — since the hook runs before dude has heard where
// the checkout started.
func beforeStop(refs map[string]string) *lux.BeforeStop {
	if len(refs) == 0 {
		return nil
	}
	return &lux.BeforeStop{Command: diffCommand("artifacts", refs), Timeout: FinalDiffTimeout}
}

// recordFinalDiff records the diff the beforeStop hook left, one patch per
// repository, read from lux: in name order they are what a live read
// prints, and so have the same checksum when nothing changed since.
func (a *Artifacts) recordFinalDiff(ctx context.Context, r dueRun, patches []lux.Artifact) error {
	repo := func(art lux.Artifact) string { name, _ := finalDiffRepo(art.Path); return name }
	slices.SortFunc(patches, func(x, y lux.Artifact) int { return strings.Compare(repo(x), repo(y)) })
	var text strings.Builder
	for _, art := range patches {
		body, err := a.Lux.Download(ctx, art.ID)
		if err != nil {
			return err
		}
		_, err = io.Copy(&text, io.LimitReader(body, 16<<20))
		body.Close()
		if err != nil {
			return err
		}
	}
	_, err := recordRunDiff(ctx, a.DB.InOrg, r.Org, r.ProjectID, r.TaskID, r.ID, text.String(), patches[0].Epoch, true)
	return err
}
