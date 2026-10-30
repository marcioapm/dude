package branchdiff

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/ids"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// EvSnapshotCaptured is the ledger event of a new task branch snapshot
// (task.branch_snapshot.captured in the domain's catalogue).
const EvSnapshotCaptured = "task.branch_snapshot.captured"

// Capturer takes due capture requests (task_branch_capture_requests) and
// captures a snapshot for each whose branch head is new. The requests are
// made by the database itself when a branch moves (migration 230's
// triggers on runs.heads, conductor_publishes and pull_requests.head_sha:
// dude's pushes, promotions and merges of the target, and the pull request
// sync's reading of someone else's push), and by the API when a read finds
// nothing yet.
type Capturer struct {
	DB     *db.DB
	Forges delivery.Forges
	Log    *slog.Logger
	// Now, for tests; time.Now when nil.
	Now func() time.Time
}

// Requests taken per pass; a backlog repeats the loop at once.
const batch = 20

// claimFor is how long a taken request is left alone by other workers.
const claimFor = 2 * time.Minute

// maxFailures transient failures in a row make a request unavailable.
const maxFailures = 8

// Request is one claimed (task, attempt, repository) to capture, and when
// it was last asked for.
type Request struct {
	Org, TaskID string
	Attempt     int
	RepoID      string
	RequestedAt time.Time
}

// Sweep claims what is due and captures it; how many captures succeeded.
// The claim (next_attempt_at pushed claimFor ahead) is committed on its own
// before anything is read, so a request whose reads fail keeps it and is
// backed off, never taken again at once; and a pass in which none
// succeeded says 0, so the loop waits before the next.
func (c *Capturer) Sweep(ctx context.Context) (int, error) {
	var due []Request
	if err := c.DB.InSystem(ctx, "branch-snapshots", func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `UPDATE task_branch_capture_requests SET next_attempt_at = now() + make_interval(secs => $2)
			WHERE (task_id, attempt, repository_id) IN (SELECT task_id, attempt, repository_id FROM task_branch_capture_requests
				WHERE status = 'pending' AND next_attempt_at <= now() ORDER BY next_attempt_at LIMIT $1 FOR UPDATE SKIP LOCKED)
			RETURNING organization_id, task_id, attempt, repository_id, requested_at`, batch, claimFor.Seconds())
		if err != nil {
			return err
		}
		due, err = pgx.CollectRows(rows, pgx.RowToStructByPos[Request])
		return err
	}); err != nil {
		return 0, err
	}
	ok := 0
	for _, r := range due {
		err := c.Capture(ctx, r)
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		if err != nil {
			c.log().Warn("capturing a branch snapshot failed", "task", r.TaskID, "attempt", r.Attempt, "repository", r.RepoID, "error", err)
			continue
		}
		ok++
	}
	return ok, nil
}

func (c *Capturer) log() *slog.Logger {
	if c.Log == nil {
		return slog.Default()
	}
	return c.Log
}

// target is what a capture reads before asking GitHub.
type target struct {
	requestedAt              time.Time
	repoName, repoURL        string
	branch, targetRef        string
	prevID, prevHead, prevMB string
	prHead                   string
}

// Capture reads the branch of a request Sweep claimed and settles the
// request: done (a snapshot captured, or its head already has one), absent
// (no branch yet), unavailable (why), or due again later after a failure —
// of GitHub's, or of the reads before it — counted and backed off.
func (c *Capturer) Capture(ctx context.Context, r Request) error {
	t := target{requestedAt: r.RequestedAt}
	if err := c.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT name, url, default_branch FROM repositories WHERE id = $1`, r.RepoID).
			Scan(&t.repoName, &t.repoURL, &t.targetRef); err != nil {
			return fmt.Errorf("read the repository: %w", err)
		}
		tb, err := delivery.TaskBranchTx(ctx, tx, r.TaskID, r.Attempt)
		if err != nil {
			return err
		}
		t.branch = tb.Branch
		var prBase *string
		err = tx.QueryRow(ctx, `SELECT head_sha, base_branch FROM pull_requests WHERE task_id = $1 AND repository_id = $2
			AND state IN ('open', 'draft') ORDER BY created_at DESC LIMIT 1`, r.TaskID, r.RepoID).Scan(&t.prHead, &prBase)
		if err != nil && !db.IsNotFound(err) {
			return err
		}
		switch {
		case tb.Bases[t.repoName] != "":
			t.targetRef = tb.Bases[t.repoName]
		case prBase != nil && *prBase != "":
			t.targetRef = *prBase
		}
		// The snapshot before: the one the branch was last read at, else the newest.
		err = tx.QueryRow(ctx, `SELECT s.id, s.head_sha, s.merge_base_sha FROM task_branch_snapshots s
			LEFT JOIN task_branch_capture_requests q ON q.task_id = s.task_id AND q.attempt = s.attempt AND q.repository_id = s.repository_id
			WHERE s.task_id = $1 AND s.attempt = $2 AND s.repository_id = $3
			ORDER BY s.id = q.snapshot_id DESC NULLS LAST, s.captured_at DESC, s.id DESC LIMIT 1`,
			r.TaskID, r.Attempt, r.RepoID).Scan(&t.prevID, &t.prevHead, &t.prevMB)
		if db.IsNotFound(err) {
			return nil
		}
		return err
	}); err != nil {
		if ctx.Err() != nil {
			return err
		}
		return c.failed(ctx, r, t, "The task branch", err)
	}
	snap, err := c.read(ctx, r, t)
	return c.settle(ctx, r, t, snap, err)
}

// snapshot is a capture's result, before it is recorded.
type snapshot struct {
	head, targetSHA, mergeBase, relation string
	files                                any
	stats                                Stats
	checksum                             string
	reason                               string
}

// Refusals a capture records as the request's reason.
var (
	errAbsent = errors.New("the branch does not exist yet")
	errSame   = errors.New("the head already has a snapshot")
)

type unavailable struct{ reason string }

func (u unavailable) Error() string { return u.reason }

func (c *Capturer) read(ctx context.Context, r Request, t target) (snapshot, error) {
	slug := forge.SlugFromURL(t.repoURL)
	if slug == "" {
		return snapshot{}, unavailable{"the repository is not on GitHub"}
	}
	gh, err := c.Forges.For(ctx, r.Org)
	if err != nil {
		return snapshot{}, err
	}
	if gh == nil {
		return snapshot{}, unavailable{"the organization has no GitHub connection"}
	}
	var s snapshot
	if s.head, err = gh.BranchSHA(ctx, slug, t.branch); err != nil {
		return s, err
	}
	if s.head == "" {
		return s, errAbsent
	}
	// The target moving alone changes nothing to show.
	if s.head == t.prevHead {
		return s, errSame
	}
	if s.targetSHA, err = gh.BranchSHA(ctx, slug, t.targetRef); err != nil {
		return s, err
	}
	if s.targetSHA == "" {
		return s, unavailable{fmt.Sprintf("the target branch %s does not exist", t.targetRef)}
	}
	cmp, err := gh.Compare(ctx, slug, s.targetSHA, s.head)
	if err != nil {
		return s, err
	}
	s.mergeBase = cmp.MergeBase.SHA
	descends := false
	if t.prevHead != "" {
		// prev...head: behind_by 0 is the previous head an ancestor of this one.
		lineage, err := gh.Compare(ctx, slug, t.prevHead, s.head)
		var fe *forge.Error
		switch {
		case errors.As(err, &fe) && fe.NotFound():
			// The previous head is gone from GitHub (force-pushed and collected).
		case err != nil:
			return s, err
		default:
			descends = lineage.BehindBy == 0
		}
	}
	s.relation = Classify(t.prevHead != "", t.prevMB, s.mergeBase, descends)
	files, stats := Files(cmp.Files)
	s.files, s.stats = files, stats
	s.checksum = Checksum(s.mergeBase, s.head, files)
	if cmp.Truncated() {
		s.reason = partialReason(len(cmp.Files))
	}
	return s, nil
}

// retryAfter is the wait after the nth transient failure in a row.
func retryAfter(n int, asked time.Duration) time.Duration {
	if asked > 0 {
		return asked
	}
	d := 5 * time.Second << min(n, 8)
	return min(d, 15*time.Minute)
}

// mark settles the request as status, only if it was not asked for again
// since it was claimed (requested_at unchanged); a newer ask stays pending,
// due now.
func mark(ctx context.Context, tx pgx.Tx, r Request, t target, status, reason, snapshotID string) error {
	tag, err := tx.Exec(ctx, `UPDATE task_branch_capture_requests SET status = $4, reason = NULLIF($5, ''), failures = 0,
		settled_at = now(), snapshot_id = COALESCE(NULLIF($7, ''), snapshot_id)
		WHERE task_id = $1 AND attempt = $2 AND repository_id = $3 AND requested_at = $6`,
		r.TaskID, r.Attempt, r.RepoID, status, clip(reason), t.requestedAt, snapshotID)
	if err != nil || tag.RowsAffected() > 0 {
		return err
	}
	// Asked again while this one read: still pending, due now.
	_, err = tx.Exec(ctx, `UPDATE task_branch_capture_requests SET failures = 0, next_attempt_at = now(),
		snapshot_id = COALESCE(NULLIF($4, ''), snapshot_id)
		WHERE task_id = $1 AND attempt = $2 AND repository_id = $3`, r.TaskID, r.Attempt, r.RepoID, snapshotID)
	return err
}

// retryable is a read failure worth trying again: not one of the outcomes
// settle records as they are.
func retryable(err error) bool {
	var un unavailable
	var fe *forge.Error
	switch {
	case err == nil, errors.Is(err, errAbsent), errors.Is(err, errSame), errors.As(err, &un):
		return false
	case errors.As(err, &fe) && !forge.Transient(err) && fe.Status >= 400 && fe.Status < 500:
		return false
	}
	return true
}

// failed counts a failure of the request's capture — GitHub's, or of the
// reads before it — in a transaction of its own: due again after the
// back-off, or unavailable after maxFailures in a row. The claim stays in
// place if even this cannot be written. Returns cause.
func (c *Capturer) failed(ctx context.Context, r Request, t target, what string, cause error) error {
	err := c.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		var failures int
		if err := tx.QueryRow(ctx, `UPDATE task_branch_capture_requests SET failures = failures + 1
			WHERE task_id = $1 AND attempt = $2 AND repository_id = $3 AND requested_at = $4 RETURNING failures`,
			r.TaskID, r.Attempt, r.RepoID, t.requestedAt).Scan(&failures); err != nil {
			if db.IsNotFound(err) {
				// A newer ask landed during the read. Do not delay it with this
				// read's failure (nor erase earlier failures).
				_, err = tx.Exec(ctx, `UPDATE task_branch_capture_requests SET next_attempt_at = now()
					WHERE task_id = $1 AND attempt = $2 AND repository_id = $3`, r.TaskID, r.Attempt, r.RepoID)
				return err
			}
			return err
		}
		if failures >= maxFailures {
			return mark(ctx, tx, r, t, "unavailable", what+" could not be read: "+cause.Error(), "")
		}
		var asked time.Duration
		var fe *forge.Error
		if errors.As(cause, &fe) {
			asked = fe.RetryAfter
		}
		_, err := tx.Exec(ctx, `UPDATE task_branch_capture_requests SET next_attempt_at = now() + make_interval(secs => $4)
			WHERE task_id = $1 AND attempt = $2 AND repository_id = $3`,
			r.TaskID, r.Attempt, r.RepoID, retryAfter(failures, asked).Seconds())
		return err
	})
	if err != nil {
		return errors.Join(cause, fmt.Errorf("recording the failure: %w", err))
	}
	return cause
}

// settle records what read found, in one transaction, only if the request
// was not asked for again meanwhile (requested_at unchanged): a newer ask
// stays pending for the next pass. A failure is counted (failed).
func (c *Capturer) settle(ctx context.Context, r Request, t target, s snapshot, readErr error) error {
	if retryable(readErr) {
		return c.failed(ctx, r, t, "GitHub", readErr)
	}
	return c.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		var un unavailable
		var fe *forge.Error
		switch {
		case errors.Is(readErr, errAbsent):
			return mark(ctx, tx, r, t, "absent", "", "")
		case errors.Is(readErr, errSame):
			return mark(ctx, tx, r, t, "done", "", t.prevID)
		case errors.As(readErr, &un):
			return mark(ctx, tx, r, t, "unavailable", un.reason, "")
		case errors.As(readErr, &fe):
			return mark(ctx, tx, r, t, "unavailable", fmt.Sprintf("GitHub refused the read (%d): %s", fe.Status, fe.Message), "")
		}
		files, _ := json.Marshal(s.files)
		stats, _ := json.Marshal(s.stats)
		status := "complete"
		if s.reason != "" {
			status = "partial"
		}
		var prev any
		if t.prevID != "" {
			prev = t.prevID
		}
		id := ids.New(ids.BranchSnapshot)
		// Idempotent on (head, merge base): a head read again is the snapshot it already has.
		err := tx.QueryRow(ctx, `INSERT INTO task_branch_snapshots (id, organization_id, task_id, attempt, repository_id, repository,
				branch_ref, target_ref, target_sha, merge_base_sha, head_sha, files, checksum, stats, truncated, binary_files,
				status, reason, source, pr_head_sha, previous_snapshot_id, relation)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12::jsonb, $13, $14::jsonb, $15, $16, $17, NULLIF($18, ''), $19,
				NULLIF($20, ''), $21, $22)
			ON CONFLICT (task_id, attempt, repository_id, head_sha, merge_base_sha) DO NOTHING
			RETURNING id`,
			id, r.Org, r.TaskID, r.Attempt, r.RepoID, t.repoName, t.branch, t.targetRef, s.targetSHA, s.mergeBase, s.head,
			files, s.checksum, stats, s.stats.TruncatedFiles > 0 || s.reason != "", s.stats.BinaryFiles, status, s.reason, Source,
			validSHA(t.prHead), prev, s.relation).Scan(&id)
		created := err == nil
		if db.IsNotFound(err) {
			err = tx.QueryRow(ctx, `SELECT id FROM task_branch_snapshots WHERE task_id = $1 AND attempt = $2
				AND repository_id = $3 AND head_sha = $4 AND merge_base_sha = $5`,
				r.TaskID, r.Attempt, r.RepoID, s.head, s.mergeBase).Scan(&id)
		}
		if err != nil {
			return err
		}
		// Once per snapshot: the task's watchers word "New changes available" from it.
		if created {
			var project string
			if err := tx.QueryRow(ctx, `SELECT project_id FROM tasks WHERE id = $1`, r.TaskID).Scan(&project); err != nil {
				return err
			}
			if _, err := ledger.Append(ctx, tx, ledger.Event{Type: EvSnapshotCaptured, OrganizationID: r.Org, ProjectID: project,
				TaskID: r.TaskID, ActorType: ledger.ActorSystem, ActorID: "dude", Source: ledger.SourceOrchestrator, CorrelationID: r.TaskID,
				Payload: map[string]any{"snapshotId": id, "attempt": r.Attempt, "repository": t.repoName, "relation": s.relation,
					"previousSnapshotId": t.prevID, "mergeBaseSha": s.mergeBase, "headSha": s.head,
					"previousMergeBaseSha": t.prevMB, "previousHeadSha": t.prevHead}}); err != nil {
				return err
			}
		}
		return mark(ctx, tx, r, t, "done", "", id)
	})
}

func validSHA(s string) string {
	if len(s) < 40 {
		return ""
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return ""
		}
	}
	return s
}

func clip(s string) string {
	if len(s) > 500 {
		return s[:497] + "…"
	}
	return s
}
