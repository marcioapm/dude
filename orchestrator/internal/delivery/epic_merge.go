package delivery

import (
	"context"
	"errors"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

// A merge on an epic wakes its peers' conductors: when a pull request of a
// task on an epic merges, the live conductor of every other task on that
// epic with an open delivery is woken once, told which task merged, its
// pull request and merge commit, and which of the files the merge changed
// its own branch changes too. Notes and messages never wake anyone; this
// does, so a peer brings the merge in before it builds further on what
// the merge changed.
//
// As a merge's release of what waits on it (unblock.go): never in the
// merge's transaction, by a worker. The worker reads stored state, not the
// sync's event: every merged pull request not yet told
// (pull_requests.epic_peers_told_at IS NULL) is a candidate on each pass,
// so a sync that crashed after recording the merge, or a worker that
// crashed mid-pass, loses nothing. Each peer's wake is keyed by the merged
// pull request (EpicMergeWakeKey), so recording it twice records one; the
// pull request is marked told in the transaction that records its wakes.

// WakeEpicMerge is the conductor's reason when a peer on its epic merged.
const WakeEpicMerge = "epic_merge"

// EpicMergeWakeKey is a peer's wake key for merged pull request prID: one
// wake per (peer, merged pull request), across passes and restarts.
func EpicMergeWakeKey(prID string) string { return "epic-merge:" + prID }

// epicMergeBatch bounds the merged pull requests one pass takes.
const epicMergeBatch = 20

// epicMergeRetryFor is how long a merged pull request whose files the forge
// would not give (rate limit, an outage) waits for a later pass before its
// peers are woken with the overlap unknown: news late is worse than news
// without the overlap.
const epicMergeRetryFor = 10 * time.Minute

// epicMergeFiles bounds the merged pull request's files read from GitHub
// (its listing's cap); a list cut there makes "no overlap" unknown.
const epicMergeFiles = 3000

// EpicMerges is the worker that tells epic peers of a merge.
type EpicMerges struct {
	DB     *db.DB
	Forges Forges
	// Skip, when set, passes over a pull request as a missed event would
	// (tests).
	Skip func(prID string) bool
}

// Sweep tells the peers of the merged pull requests not yet told. Returns
// how many it settled.
func (e *EpicMerges) Sweep(ctx context.Context) (int, error) {
	type due struct{ ID, Org string }
	var todo []due
	if err := e.DB.InSystem(ctx, "epic-merges", func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id, organization_id FROM pull_requests
			WHERE state = 'merged' AND epic_peers_told_at IS NULL ORDER BY merged_at NULLS FIRST, id LIMIT $1`, epicMergeBatch)
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
		if e.Skip != nil && e.Skip(d.ID) {
			continue
		}
		told, err := e.tell(ctx, d.Org, d.ID)
		if err != nil {
			if ctx.Err() != nil {
				return n, ctx.Err()
			}
			return n, err
		}
		if told {
			n++
		}
	}
	return n, nil
}

// epicMerge is a merged pull request as its peers are told of it.
type epicMerge struct {
	TaskID, TaskKey, Epic, RepoID, Repo, Base, Slug, HeadSHA, MergeSHA string
	Number                                                             int
	MergedAt                                                           time.Time
}

// epicPeer is a task woken for a merge, and what its branch changes in the
// merged pull request's repository: Files, with Known false when that
// could not be read (no snapshot), Partial when the list may be cut.
type epicPeer struct {
	TaskID         string
	Writes         bool
	Known, Partial bool
	Files          []string
}

// tell settles one merged pull request: its peers read, the merge's files
// read (stored, else from the forge, outside any transaction), then the
// wakes recorded and the pull request marked told in one transaction that
// reads the peers again. Returns whether it settled it; false leaves it
// for a later pass.
func (e *EpicMerges) tell(ctx context.Context, org, prID string) (bool, error) {
	var m epicMerge
	var peers []epicPeer
	var merged []string
	var mergedKnown, mergedPartial bool
	if err := e.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		var epic *string
		var url string
		var mergedAt *time.Time
		var head, mergeSHA *string
		err := tx.QueryRow(ctx, `SELECT pr.task_id, p.key_prefix || '-' || t.number, t.epic_id, pr.repository_id, r.name, pr.base_branch, r.url,
				pr.head_sha, pr.merge_commit_sha, pr.number, pr.merged_at
			FROM pull_requests pr JOIN tasks t ON t.id = pr.task_id JOIN projects p ON p.id = t.project_id
			JOIN repositories r ON r.id = pr.repository_id
			WHERE pr.id = $1 AND pr.state = 'merged' AND pr.epic_peers_told_at IS NULL`, prID).Scan(
			&m.TaskID, &m.TaskKey, &epic, &m.RepoID, &m.Repo, &m.Base, &url, &head, &mergeSHA, &m.Number, &mergedAt)
		if db.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		m.Slug = forge.SlugFromURL(url)
		m.HeadSHA, m.MergeSHA = deref(head), deref(mergeSHA)
		if mergedAt != nil {
			m.MergedAt = *mergedAt
		}
		if epic == nil {
			return nil
		}
		m.Epic = *epic
		if peers, err = epicMergePeers(ctx, tx, m); err != nil || len(peers) == 0 {
			return err
		}
		merged, mergedKnown, mergedPartial, err = storedMergeFiles(ctx, tx, m)
		return err
	}); err != nil {
		return false, err
	}
	if m.TaskID == "" {
		return false, nil // told by another pass, or no longer merged
	}
	if len(peers) > 0 && !mergedKnown {
		var err error
		merged, mergedKnown, mergedPartial, err = e.forgeMergeFiles(ctx, org, m)
		if err != nil && time.Since(m.MergedAt) < epicMergeRetryFor {
			return false, nil // the forge could not say yet: a later pass asks again
		}
	}
	err := e.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		var untold bool
		err := tx.QueryRow(ctx, `SELECT epic_peers_told_at IS NULL FROM pull_requests WHERE id = $1 AND state = 'merged'
			FOR UPDATE SKIP LOCKED`, prID).Scan(&untold)
		if db.IsNotFound(err) || err == nil && !untold {
			return nil
		}
		if err != nil {
			return err
		}
		if m.Epic != "" {
			// Read again under the pull request's lock: a peer that ended
			// or lost its delivery since is not woken.
			now, err := epicMergePeers(ctx, tx, m)
			if err != nil {
				return err
			}
			for _, p := range now {
				if i := slices.IndexFunc(peers, func(q epicPeer) bool { return q.TaskID == p.TaskID }); i >= 0 {
					p = peers[i]
				}
				line := epicMergeLine(m, overlapOf(p, merged, mergedKnown, mergedPartial))
				if _, err := RecordWakeTx(ctx, tx, org, p.TaskID, WakeEpicMerge, EpicMergeWakeKey(prID), line); err != nil {
					return err
				}
			}
		}
		_, err = tx.Exec(ctx, `UPDATE pull_requests SET epic_peers_told_at = now() WHERE id = $1`, prID)
		return err
	})
	return err == nil, err
}

// epicMergePeers are the tasks m's merge wakes: on its epic, not its own
// task, not ended (done, cancelled, failed), with an open delivery, and a
// conductor that is live or will be started for a reason already due. A
// peer that has not started has no delivery: its first briefing shows the
// epic. Each with what its branch changes in m's repository, as stored.
func epicMergePeers(ctx context.Context, tx pgx.Tx, m epicMerge) ([]epicPeer, error) {
	rows, err := tx.Query(ctx, `SELECT t.id,
			EXISTS (SELECT 1 FROM task_repositories tr WHERE tr.task_id = t.id AND tr.repository_id = $3 AND tr.access = 'write'),
			s.files IS NOT NULL OR COALESCE(c.status = 'absent', false),
			COALESCE(s.status = 'partial', false),
			COALESCE(ARRAY(SELECT f->>'path' FROM jsonb_array_elements(s.files) f ORDER BY 1), '{}')
		FROM tasks t
		LEFT JOIN LATERAL (SELECT s.files, s.status FROM task_branch_snapshots s
			WHERE s.task_id = t.id AND s.repository_id = $3 AND s.attempt = task_attempt(t.id)
			ORDER BY s.captured_at DESC, s.id DESC LIMIT 1) s ON true
		LEFT JOIN task_branch_capture_requests c ON c.task_id = t.id AND c.repository_id = $3 AND c.attempt = task_attempt(t.id)
		WHERE t.epic_id = $1 AND t.id <> $2 AND t.status NOT IN ('done', 'aborted', 'failed')
		  AND EXISTS (SELECT 1 FROM workflow_runs w WHERE w.task_id = t.id AND w.workflow_type = $4
		      AND w.status IN ('running', 'waiting'))
		  AND (EXISTS (SELECT 1 FROM runs r WHERE r.task_id = t.id AND `+LiveConductor+`)
		       OR EXISTS (SELECT 1 FROM conductor_wakes cw WHERE cw.task_id = t.id AND cw.delivered_at IS NULL))
		ORDER BY t.id`, m.Epic, m.TaskID, m.RepoID, WorkflowType)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[epicPeer])
}

// storedMergeFiles is what m's merge changed, from the merged task's
// stored snapshot at the pull request's head: ok false when none is
// stored at that head.
func storedMergeFiles(ctx context.Context, tx pgx.Tx, m epicMerge) (files []string, ok, partial bool, err error) {
	if m.HeadSHA == "" {
		return nil, false, false, nil
	}
	err = tx.QueryRow(ctx, `SELECT COALESCE(ARRAY(SELECT f->>'path' FROM jsonb_array_elements(s.files) f ORDER BY 1), '{}'),
			s.status = 'partial'
		FROM task_branch_snapshots s WHERE s.task_id = $1 AND s.repository_id = $2 AND s.head_sha = $3
		ORDER BY s.captured_at DESC, s.id DESC LIMIT 1`, m.TaskID, m.RepoID, m.HeadSHA).Scan(&files, &partial)
	if db.IsNotFound(err) {
		return nil, false, false, nil
	}
	return files, err == nil, partial, err
}

// errNoForge is a merge whose files no forge can give.
var errNoForge = errors.New("no forge to read the merged pull request's files")

// forgeMergeFiles is what m's merge changed, from GitHub's listing of its
// pull request's files: one read, outside any transaction. An error is
// the forge not answering (ok false).
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
	files, err := gh.PullFiles(ctx, m.Slug, m.Number, epicMergeFiles)
	if err != nil {
		return nil, false, false, err
	}
	slices.Sort(files)
	return files, true, len(files) >= epicMergeFiles, nil
}

// epicOverlap is what a peer is told of the files it shares with a merge:
// Known false says it could not be told; Files the shared ones.
type epicOverlap struct {
	Known bool
	Files []string
}

// overlapOf is the files peer p's branch shares with merged. Unknown when
// either side could not be read, or neither shares one but a side's list
// may be cut: "can't tell" is never "none". A peer that does not write
// the merged repository shares nothing.
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
	return epicOverlap{Known: true, Files: shared}
}

// epicMergeShown bounds the shared files a line names; the rest are
// counted.
const epicMergeShown = 4

// epicMergeLine tells a peer's conductor m merged, and what its branch
// shares with it: as many shared files as fit a wake's line, the rest
// counted, so the hint after them is never cut.
func epicMergeLine(m epicMerge, o epicOverlap) string {
	sha := m.MergeSHA
	if len(sha) > 12 {
		sha = sha[:12]
	}
	line := func(n int) string {
		shown := o.Files[:n]
		return render(epicMergeTmpl, struct {
			Task, Repo, Base, SHA string
			Number, More          int
			Known                 bool
			Files                 []string
		}{m.TaskKey, m.Repo, m.Base, sha, m.Number, len(o.Files) - n, o.Known, shown})
	}
	n := min(len(o.Files), epicMergeShown)
	for n > 1 && utf8.RuneCountInString(oneLine(line(n))) > wakeLineChars {
		n--
	}
	return line(n)
}
