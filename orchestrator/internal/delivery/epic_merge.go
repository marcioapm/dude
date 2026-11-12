package delivery

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

// A merge on an epic wakes its peers' conductors: when a task's pull
// request merges, the conductor of every other task on its epic with an
// open delivery is woken once, told what merged and which of its files the
// peer's branch changes too, so it brings the merge in before building on
// it.
//
// A worker, not the merge's transaction (as unblock.go), reading stored
// state: every merged pull request with epic_peers_told_at NULL is a
// candidate on each pass, so a crash loses nothing. The wake is keyed by
// the pull request (EpicMergeWakeKey) and the pull request marked told in
// the transaction that records the wakes.

// WakeEpicMerge is the conductor's reason when a peer on its epic merged.
const WakeEpicMerge = "epic_merge"

// EpicMergeWakeKey is a peer's wake key for merged pull request prID.
func EpicMergeWakeKey(prID string) string { return "epic-merge:" + prID }

// epicMergeBatch bounds the merged pull requests one pass takes.
const epicMergeBatch = 20

// epicMergeRetryFor is how long after merged_at (the database's clock) a
// merge whose files the forge will not give is retried before its peers
// are told the overlap is unknown: late news is worse than less news.
const epicMergeRetryFor = 10 * time.Minute

// A merge the worker could not settle waits until epic_peers_retry_at:
// the forge's Retry-After, else the time since the merge, clamped to
// epicMergeBackoff..epicMergeBackoffMax (10s, 20s, 40s, ... 2m).
const (
	epicMergeBackoff    = 10 * time.Second
	epicMergeBackoffMax = 2 * time.Minute
)

// epicMergeFiles bounds the merged pull request's files read from GitHub
// (its listing's cap); a list cut there makes "no overlap" unknown.
const epicMergeFiles = 3000

// EpicMerges is the worker that tells epic peers of a merge.
type EpicMerges struct {
	DB     *db.DB
	Forges Forges
	Log    *slog.Logger
}

// Sweep tells the peers of merged pull requests not yet told that are due
// (past their retry time, or the window), and returns how many it settled.
// One that fails is logged and given a retry time; the batch goes on.
func (e *EpicMerges) Sweep(ctx context.Context) (int, error) {
	type due struct{ ID, Org string }
	var todo []due
	if err := e.DB.InSystem(ctx, "epic-merges", func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id, organization_id FROM pull_requests
			WHERE state = 'merged' AND epic_peers_told_at IS NULL
			  AND (epic_peers_retry_at IS NULL OR epic_peers_retry_at <= now() OR `+epicMergeExpired+`)
			ORDER BY merged_at NULLS FIRST, id LIMIT $2`, epicMergeRetryFor.Seconds(), epicMergeBatch)
		if err != nil {
			return err
		}
		todo, err = pgx.CollectRows(rows, pgx.RowToStructByPos[due])
		return err
	}); err != nil {
		return 0, err
	}
	n := 0
	for _, d := range todo {
		told, err := e.tell(ctx, d.Org, d.ID)
		if ctx.Err() != nil {
			return n, ctx.Err()
		}
		if err != nil {
			e.log().Warn("telling an epic's peers of a merge failed", "pr", d.ID, "error", err)
			e.retryLater(ctx, d.Org, d.ID, err)
			continue
		}
		if told {
			n++
		}
	}
	return n, nil
}

func (e *EpicMerges) log() *slog.Logger {
	if e.Log != nil {
		return e.Log
	}
	return slog.Default()
}

// epicMergeExpired (SQL, over pull_requests, $1 epicMergeRetryFor in
// seconds): the window for the merge's files has ended, on the database's
// clock. A merge with no time recorded has none to wait.
const epicMergeExpired = `COALESCE(merged_at <= now() - make_interval(secs => $1), true)`

// retryLater keeps prID from the passes before its retry time: the
// forge's wait for err when it gave one, else the backoff.
func (e *EpicMerges) retryLater(ctx context.Context, org, prID string, err error) {
	wait := forge.RetryAfter(err).Seconds()
	if dbErr := e.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE pull_requests SET epic_peers_retry_at = now() + make_interval(secs => CASE WHEN $2 > 0 THEN $2
				ELSE LEAST(GREATEST(EXTRACT(EPOCH FROM now() - COALESCE(merged_at, now())), $3), $4) END)
			WHERE id = $1`, prID, wait, epicMergeBackoff.Seconds(), epicMergeBackoffMax.Seconds())
		return err
	}); dbErr != nil {
		e.log().Warn("recording an epic merge's retry failed", "pr", prID, "error", dbErr)
	}
}

// epicMerge is a merged pull request as its peers are told of it.
type epicMerge struct {
	TaskID, TaskKey, Epic, RepoID, Repo, Base, Slug, HeadSHA, MergeSHA string
	Number                                                             int
}

// epicPeer is a task woken for a merge, and what its branch changes in the
// merged pull request's repository: Files (fileNames), sorted bytewise;
// Known false when there is no snapshot; Partial when the list may be cut,
// miss a name, or be stale: a capture pending, whatever its timestamps,
// is a newer read owed (a push during a read leaves the request pending
// and older than the snapshot that read the branch before it). SameBase:
// its delivery measures the repository against the base m merged into.
type epicPeer struct {
	TaskID         string
	Writes         bool
	Known, Partial bool
	SameBase       bool
	Files          []string
}

// tell settles one merged pull request: its peers read, the merge's files
// read (stored, else from the forge, outside any transaction), then the
// wakes recorded and the pull request marked told in one transaction that
// reads the peers again. Returns whether it settled it; false, nil when
// another pass holds or told it. An error leaves it for a later pass.
func (e *EpicMerges) tell(ctx context.Context, org, prID string) (bool, error) {
	var m epicMerge
	var hasPeers, expired, waiting bool
	var merged []string
	var mergedKnown, mergedPartial bool
	if err := e.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		var epic *string
		var url string
		var mergeSHA *string
		err := tx.QueryRow(ctx, `SELECT pr.task_id, p.key_prefix || '-' || t.number, t.epic_id, pr.repository_id, r.name, pr.base_branch, r.url,
				pr.head_sha, pr.merge_commit_sha, pr.number, `+epicMergeExpired+`, COALESCE(pr.epic_peers_retry_at > now(), false)
			FROM pull_requests pr JOIN tasks t ON t.id = pr.task_id JOIN projects p ON p.id = t.project_id
			JOIN repositories r ON r.id = pr.repository_id
			WHERE pr.id = $2 AND pr.state = 'merged' AND pr.epic_peers_told_at IS NULL`, epicMergeRetryFor.Seconds(), prID).Scan(
			&m.TaskID, &m.TaskKey, &epic, &m.RepoID, &m.Repo, &m.Base, &url, &m.HeadSHA, &mergeSHA, &m.Number, &expired, &waiting)
		if db.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		m.Slug = forge.SlugFromURL(url)
		m.MergeSHA = deref(mergeSHA)
		if epic == nil {
			return nil
		}
		m.Epic = *epic
		peers, err := epicMergePeers(ctx, tx, m)
		if err != nil || len(peers) == 0 {
			return err
		}
		hasPeers = true
		merged, mergedKnown, mergedPartial, err = storedMergeFiles(ctx, tx, m)
		return err
	}); err != nil {
		return false, err
	}
	if m.TaskID == "" {
		return false, nil // told by another pass, or no longer merged
	}
	if hasPeers && !mergedKnown && !waiting {
		var err error
		merged, mergedKnown, mergedPartial, err = e.forgeMergeFiles(ctx, org, m)
		if err != nil && !expired {
			return false, fmt.Errorf("reading the merged pull request's files: %w", err)
		}
	}
	settled := false
	err := e.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		var untold bool
		err := tx.QueryRow(ctx, `SELECT epic_peers_told_at IS NULL FROM pull_requests WHERE id = $1 AND state = 'merged'
			FOR UPDATE SKIP LOCKED`, prID).Scan(&untold)
		if db.IsNotFound(err) || err == nil && !untold {
			return nil // held by another, or told since: not settled here
		}
		if err != nil {
			return err
		}
		if m.Epic != "" {
			// Read again under the pull request's lock: a peer that ended
			// or lost its delivery since is not woken, and each is told
			// what its branch is now.
			peers, err := epicMergePeers(ctx, tx, m)
			if err != nil {
				return err
			}
			for _, p := range peers {
				line := epicMergeLine(m, overlapOf(p, merged, mergedKnown, mergedPartial))
				if _, err := RecordWakeTx(ctx, tx, org, p.TaskID, WakeEpicMerge, EpicMergeWakeKey(prID), line); err != nil {
					return err
				}
			}
		}
		_, err = tx.Exec(ctx, `UPDATE pull_requests SET epic_peers_told_at = now(), epic_peers_retry_at = NULL WHERE id = $1`, prID)
		settled = err == nil
		return err
	})
	return settled && err == nil, err
}

// Over a snapshot s: fileNames is every path its files name, each file's
// and a rename's previous one; snapshotPartial says the list may be cut,
// or miss a name (a rename stored before its previous path was kept).
const (
	fileNames = `COALESCE(ARRAY(SELECT DISTINCT n FROM jsonb_array_elements(s.files) f,
		LATERAL (VALUES (f->>'path'), (f->>'previousPath')) v(n) WHERE n IS NOT NULL), '{}')`
	snapshotPartial = `(s.status = 'partial'
		OR EXISTS (SELECT 1 FROM jsonb_array_elements(s.files) f WHERE f->>'status' = 'R' AND NOT f ? 'previousPath'))`
)

// epicMergePeers are the tasks m's merge wakes: on its epic, not its task,
// not ended, with an open delivery, and a conductor that is live or will
// be started for a pending reason that is not held news (heldWakePrefix
// starts none). Otherwise this wake would start one, which an unblocked
// task's does not either (wakeUnblockedTx). A peer not started has no
// delivery; its first briefing shows the epic. SameBase uses deliveryBase,
// as NotifyBaseMovedTx does.
func epicMergePeers(ctx context.Context, tx pgx.Tx, m epicMerge) ([]epicPeer, error) {
	rows, err := tx.Query(ctx, `SELECT t.id,
			EXISTS (SELECT 1 FROM task_repositories tr WHERE tr.task_id = t.id AND tr.repository_id = $3 AND tr.access = 'write'),
			s.files IS NOT NULL OR COALESCE(c.status = 'absent', false),
			COALESCE(`+snapshotPartial+` OR c.status = 'pending', false),
			`+deliveryBase+` = $5,
			`+fileNames+`
		FROM tasks t
		JOIN LATERAL (SELECT w.state FROM workflow_runs w WHERE w.task_id = t.id AND w.workflow_type = $4
			AND w.status IN ('running', 'waiting') ORDER BY w.created_at DESC LIMIT 1) w ON true
		JOIN repositories r ON r.id = $3
		LEFT JOIN LATERAL (SELECT s.files, s.status, s.captured_at FROM task_branch_snapshots s
			WHERE s.task_id = t.id AND s.repository_id = $3 AND s.attempt = task_attempt(t.id)
			ORDER BY s.captured_at DESC, s.id DESC LIMIT 1) s ON true
		LEFT JOIN task_branch_capture_requests c ON c.task_id = t.id AND c.repository_id = $3 AND c.attempt = task_attempt(t.id)
		WHERE t.epic_id = $1 AND t.id <> $2 AND t.status NOT IN ('done', 'aborted', 'failed')
		  AND (EXISTS (SELECT 1 FROM runs r WHERE r.task_id = t.id AND `+LiveConductor+`)
		       OR EXISTS (SELECT 1 FROM conductor_wakes cw WHERE cw.task_id = t.id AND cw.delivered_at IS NULL
		         AND cw.key NOT LIKE '`+heldWakePrefix+`%'))
		ORDER BY t.id`, m.Epic, m.TaskID, m.RepoID, WorkflowType, m.Base)
	if err != nil {
		return nil, err
	}
	peers, err := pgx.CollectRows(rows, pgx.RowToStructByPos[epicPeer])
	for _, p := range peers {
		slices.Sort(p.Files) // bytewise, as overlapOf searches: not the database's collation
	}
	return peers, err
}

// storedMergeFiles is what m's merge changed, from the merged task's
// newest snapshot at the pull request's head against m's base, sorted
// bytewise; ok false when there is none. A snapshot read after the merge
// reached the base has the head for its merge base and lists nothing: it
// is not "the merge changed nothing", so it is passed over.
func storedMergeFiles(ctx context.Context, tx pgx.Tx, m epicMerge) (files []string, ok, partial bool, err error) {
	err = tx.QueryRow(ctx, `SELECT `+fileNames+`, `+snapshotPartial+`
		FROM task_branch_snapshots s WHERE s.task_id = $1 AND s.repository_id = $2 AND s.head_sha = $3
		  AND s.merge_base_sha <> s.head_sha AND s.target_ref = $4
		ORDER BY s.captured_at DESC, s.id DESC LIMIT 1`, m.TaskID, m.RepoID, m.HeadSHA, m.Base).Scan(&files, &partial)
	if db.IsNotFound(err) {
		return nil, false, false, nil
	}
	slices.Sort(files)
	return files, err == nil, partial, err
}

// errNoForge is a merge whose files no forge can give.
var errNoForge = errors.New("no forge to read the merged pull request's files")

// forgeMergeFiles is what m's merge changed, from GitHub's listing of its
// pull request's files (each path and a rename's previous one, sorted
// bytewise; partial when cut at the cap or a rename has no previous name):
// one read, outside any transaction.
func (e *EpicMerges) forgeMergeFiles(ctx context.Context, org string, m epicMerge) ([]string, bool, bool, error) {
	if e.Forges == nil || m.Slug == "" {
		return nil, false, false, errNoForge
	}
	gh, err := e.Forges.For(ctx, org)
	if err != nil {
		return nil, false, false, err
	}
	if gh == nil {
		return nil, false, false, errNoForge
	}
	listed, err := gh.PullFileList(ctx, m.Slug, m.Number, epicMergeFiles)
	if err != nil {
		return nil, false, false, err
	}
	partial := len(listed) >= epicMergeFiles
	var files []string
	for _, f := range listed {
		files = append(files, f.Path)
		if f.Previous != "" {
			files = append(files, f.Previous)
		} else if f.Status == "renamed" {
			partial = true
		}
	}
	slices.Sort(files)
	return slices.Compact(files), true, partial, nil
}

// epicOverlap is what a peer is told of the files it shares with a merge:
// Known false says it could not be told; Files the shared ones; SameBase
// that the merge went into the peer's own base.
type epicOverlap struct {
	Known, SameBase bool
	Files           []string
}

// overlapOf is the files peer p's branch shares with merged. Unknown when
// a side could not be read, or nothing is shared but a side is partial:
// "can't tell" is never "none". A peer that does not write the merged
// repository shares nothing.
func overlapOf(p epicPeer, merged []string, mergedKnown, mergedPartial bool) epicOverlap {
	if !p.Writes {
		return epicOverlap{Known: true}
	}
	if !p.Known || !mergedKnown {
		return epicOverlap{}
	}
	var shared []string
	for _, f := range p.Files {
		if _, found := slices.BinarySearch(merged, f); found {
			shared = append(shared, f)
		}
	}
	if len(shared) == 0 && (p.Partial || mergedPartial) {
		return epicOverlap{}
	}
	return epicOverlap{Known: true, SameBase: p.SameBase, Files: shared}
}

// epicMergeShown bounds the shared files a line names; the rest are
// counted.
const epicMergeShown = 4

// epicMergeLine tells a peer's conductor m merged and what it shares: as
// many shared files as fit a wake's line (maybe none), the rest counted,
// so the instruction after them is never cut.
func epicMergeLine(m epicMerge, o epicOverlap) string {
	sha := m.MergeSHA
	if len(sha) > 12 {
		sha = sha[:12]
	}
	line := func(n int) string {
		return render(epicMergeTmpl, struct {
			Task, Repo, Base, SHA string
			Number, Shared, More  int
			Known, SameBase       bool
			Files                 []string
		}{m.TaskKey, m.Repo, m.Base, sha, m.Number, len(o.Files), len(o.Files) - n, o.Known, o.SameBase, o.Files[:n]})
	}
	n := min(len(o.Files), epicMergeShown)
	for n > 0 && utf8.RuneCountInString(oneLine(line(n))) > wakeLineChars {
		n--
	}
	return line(n)
}
