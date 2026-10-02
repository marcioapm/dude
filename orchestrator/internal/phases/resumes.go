package phases

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// How long each resume of a Run took, as a person feels it: from when it
// became due (a person's Resume, an answer, an approval) to the agent's
// first output. One run_resumes row per lux resume, keyed by the new
// placement's epoch (migration 066), filled from what dude already handles:
//
//   - whilePaused: the row, its cause and woken_at, requested_at, and the
//     placement it was stopped from, from the Get the resume makes anyway;
//   - the stream: lux reporting the new placement running (running_at:
//     its running state, or the shim's lux.session record for the epoch,
//     whichever dude gets first — lux's state events trail the agent's
//     records, so the state alone can come after the agent's first busy),
//     the agent's first busy (busy_at) and its first message, thought or
//     tool call (first_output_at);
//   - lux's placements, read with Get once the Run runs again, and once
//     more at the first output if lux had not reported everything yet.
//
// Every column is written once, so a replayed frame or a repeated step
// moves nothing. dude's own stamps are clock_timestamp(), when the frame
// was applied: now() is the batch's start, the same for every frame in it.
// All of it is best effort: a failure is logged, and never fails, delays
// or retries the Run.

// Why a Run was resumed (run_resumes.cause).
const (
	causeAnswer     = "answer"
	causeRepository = "repository"
	causePerson     = "person"
	causeIdle       = "idle"
)

// resumeCause is why whilePaused resumes r: a person's Resume, of an idle
// park or of anything else, or dude's own reason being over.
func resumeCause(r phaseRun) string {
	switch {
	case r.Control == "resume" && r.DudePause == "idle":
		return causeIdle
	case r.Control == "resume":
		return causePerson
	case r.DudePause == "repository":
		return causeRepository
	}
	return causeAnswer
}

// nextEpoch is the epoch of the placement a resume asked for now makes,
// from the Run as lux reported it just before. The Run's epoch is its
// current placement's, so normally the next one; but a Run already
// resuming (an earlier resume got through and its answer was lost) may
// have its epoch moved on and no placement for it yet, or one still live.
func nextEpoch(before lux.Run) int {
	if lux.Terminal(before.State) {
		return before.Epoch + 1
	}
	for _, p := range before.Placements {
		if p.Epoch == before.Epoch && p.ExitedAt != nil {
			return before.Epoch + 1
		}
	}
	return max(before.Epoch, 1)
}

// placementsAround are the placement of epoch and the one before it (the
// one the Run was stopped from); zero values for those lux does not list.
func placementsAround(ps []lux.Placement, epoch int) (cur, prev lux.Placement) {
	for _, p := range ps {
		switch {
		case p.Epoch == epoch:
			cur = p
		case p.Epoch < epoch && p.Epoch > prev.Epoch:
			prev = p
		}
	}
	return cur, prev
}

// woken (SQL, over runs r and the cause $3): when the resume became due.
// A person's Resume is control_requested_at. An answer is the latest
// answer or decision on the Run: whichever closed the last open ask. An
// approved repository is its approval.
const woken = `CASE $3
	WHEN 'person' THEN r.control_requested_at
	WHEN 'idle' THEN r.control_requested_at
	WHEN 'repository' THEN (SELECT max(q.decided_at) FROM repository_requests q WHERE q.run_id = r.id AND q.status = 'approved')
	ELSE GREATEST((SELECT max(q.answered_at) FROM questions q WHERE q.run_id = r.id),
		(SELECT max(q.decided_at) FROM repository_requests q WHERE q.run_id = r.id)) END`

// resumeAsked inserts the row for a resume dude is about to ask lux for,
// in a transaction of its own, before lux is asked: lux may stream the
// new placement's first frames before its answer is back, and those
// frames have the row to stamp. requested_at is now, as dude asks. A
// second attempt, after a refusal that may pass, moves it to that
// attempt if lux still reports the Run stopped (the first did not take);
// if lux is already resuming it, the first attempt's answer was lost and
// its requested_at stands. before is the Run as lux reported it just
// before. Returns the epoch foreseen.
func (s *Syncer) resumeAsked(ctx context.Context, r phaseRun, before lux.Run) int {
	epoch := nextEpoch(before)
	_, prev := placementsAround(before.Placements, epoch)
	if err := s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		s.bestEffort(ctx, tx, r, "resume", func(ctx context.Context, tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `INSERT INTO run_resumes (run_id, organization_id, epoch, cause, woken_at, requested_at)
				SELECT r.id, r.organization_id, $2, $3, `+woken+`, clock_timestamp() FROM runs r WHERE r.id = $1 AND r.status = 'paused'
				ON CONFLICT (run_id, epoch) DO UPDATE SET requested_at = EXCLUDED.requested_at
				WHERE run_resumes.running_at IS NULL AND $4`, r.ID, epoch, resumeCause(r), lux.Terminal(before.State)); err != nil {
				return err
			}
			return writePlacements(ctx, tx, r.ID, epoch, lux.Placement{}, prev)
		})
		return nil
	}); err != nil {
		s.logger().Warn("recording a resume's timing failed", "run", r.ID, "step", "resume", "error", err)
	}
	return epoch
}

// resumeRefused deletes the row of a resume lux refused for good: there
// was no such resume.
func (s *Syncer) resumeRefused(ctx context.Context, r phaseRun, epoch int) {
	if err := s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		s.bestEffort(ctx, tx, r, "refused", func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `DELETE FROM run_resumes WHERE run_id = $1 AND epoch = $2 AND running_at IS NULL`, r.ID, epoch)
			return err
		})
		return nil
	}); err != nil {
		s.logger().Warn("recording a resume's timing failed", "run", r.ID, "step", "refused", "error", err)
	}
}

// resumeAccepted moves the row of a resume lux accepted to the epoch lux
// says it is for, when that is not the one foreseen; in the transaction
// that takes the Run out of paused. lux's answer with no epoch (a resume
// already under way, a 409) leaves it.
func (s *Syncer) resumeAccepted(ctx context.Context, tx pgx.Tx, r phaseRun, foreseen int, resumed lux.Run) {
	if resumed.Epoch == 0 || resumed.Epoch == foreseen {
		return
	}
	s.bestEffort(ctx, tx, r, "accepted", func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE run_resumes SET epoch = $3 WHERE run_id = $1 AND epoch = $2
			AND NOT EXISTS (SELECT 1 FROM run_resumes WHERE run_id = $1 AND epoch = $3)`, r.ID, foreseen, resumed.Epoch)
		return err
	})
}

// writePlacements records what lux reported of a resume's placement (cur)
// and the one it was stopped from (prev), each field once.
func writePlacements(ctx context.Context, tx pgx.Tx, runID string, epoch int, cur, prev lux.Placement) error {
	_, err := tx.Exec(ctx, `UPDATE run_resumes SET
		assigned_at = COALESCE(assigned_at, $3), image_ready_at = COALESCE(image_ready_at, $4),
		volumes_restored_at = COALESCE(volumes_restored_at, $5), container_started_at = COALESCE(container_started_at, $6),
		workload_started_at = COALESCE(workload_started_at, $7), host_name = COALESCE(host_name, NULLIF($8, '')),
		stopped_host_name = COALESCE(stopped_host_name, NULLIF($9, '')), stop_requested_at = COALESCE(stop_requested_at, $10),
		exited_at = COALESCE(exited_at, $11), snapshot_done_at = COALESCE(snapshot_done_at, $12),
		uploaded_at = COALESCE(uploaded_at, $13), snapshot_bytes = COALESCE(snapshot_bytes, $14),
		moved = COALESCE(moved, COALESCE(host_name, NULLIF($8, '')) <> COALESCE(stopped_host_name, NULLIF($9, '')))
		WHERE run_id = $1 AND epoch = $2`, runID, epoch,
		cur.AssignedAt, cur.ImageReadyAt, cur.VolumesRestoredAt, cur.ContainerStartedAt, cur.WorkloadStartedAt, cur.HostName,
		prev.HostName, prev.StopRequestedAt, prev.ExitedAt, prev.SnapshotDoneAt, prev.UploadedAt, prev.SnapshotBytes)
	return err
}

// latestResume (SQL): the Run's ($1) resume into the frame's epoch ($2):
// a frame from another placement — the one before, or one lux moved the
// Run to on its own — is not about it. A frame with no epoch (0) is about
// the last.
const latestResume = `epoch = CASE WHEN $2 = 0 THEN (SELECT max(epoch) FROM run_resumes WHERE run_id = $1) ELSE $2 END`

// stamp writes col (a stamp of dude's own) once on the resume a frame of
// epoch is about. It says which resume it stamped, if any, whether lux
// had yet to report some of that resume's new placement, and whether the
// epoch is settled: its row is there, or none will ever be. The row of a
// resume is in before lux is asked for it (resumeAsked), so a frame of an
// epoch with no row is settled unless the row is still to come or to move
// to it: while the Run is paused (lux's answer not in yet) and the epoch
// is newer than every row. The first placement (epoch 1) is no resume.
func (t *translator) stamp(ctx context.Context, tx pgx.Tx, s *Syncer, col string, epoch int) (stamped *int, missing, settled bool) {
	s.bestEffort(ctx, tx, t.run, col, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `WITH target AS (SELECT epoch FROM run_resumes WHERE run_id = $1 AND `+latestResume+`),
			stamped AS (UPDATE run_resumes SET `+col+` = clock_timestamp()
				WHERE run_id = $1 AND epoch IN (SELECT epoch FROM target) AND `+col+` IS NULL
				RETURNING epoch, assigned_at IS NULL OR image_ready_at IS NULL OR volumes_restored_at IS NULL
					OR container_started_at IS NULL OR workload_started_at IS NULL OR host_name IS NULL AS missing)
			SELECT (SELECT epoch FROM stamped), COALESCE((SELECT missing FROM stamped), false),
				EXISTS (SELECT 1 FROM target) OR $2 = 1
				OR COALESCE($2 < (SELECT max(epoch) FROM run_resumes WHERE run_id = $1), false)
				OR (SELECT status FROM runs WHERE id = $1) <> 'paused'`, t.run.ID, epoch).
			Scan(&stamped, &missing, &settled)
	})
	return stamped, missing, settled
}

// resumeRunning records lux reporting the Run running again, and has its
// placements read once the batch commits.
func (t *translator) resumeRunning(ctx context.Context, tx pgx.Tx, s *Syncer, epoch int) {
	if e, _, _ := t.stamp(ctx, tx, s, "running_at", epoch); e != nil {
		t.afterBatch(*e, true)
	}
}

// resumeBusy records the agent's first busy after a resume: it took its
// input. Looked for once per settled epoch, at most once a batch before.
func (t *translator) resumeBusy(ctx context.Context, tx pgx.Tx, s *Syncer, epoch int) {
	if epoch != 0 && (epoch == t.busyEpoch || t.unsettled[unsettledKey{"busy", epoch}]) {
		return
	}
	if _, _, settled := t.stamp(ctx, tx, s, "busy_at", epoch); settled {
		t.busyEpoch = epoch
	} else {
		t.unsettle("busy", epoch)
	}
}

// resumeOutput records the agent's first output after a resume; its
// timing is written once the batch commits, its placements read again
// first if lux had not reported all of them. Looked for once per settled
// epoch, at most once a batch before.
func (t *translator) resumeOutput(ctx context.Context, tx pgx.Tx, s *Syncer, epoch int) {
	if epoch == t.outputEpoch || t.unsettled[unsettledKey{"output", epoch}] {
		return
	}
	e, missing, settled := t.stamp(ctx, tx, s, "first_output_at", epoch)
	if e != nil {
		t.afterBatch(*e, missing)
	}
	if settled {
		t.outputEpoch = epoch
	} else {
		t.unsettle("output", epoch)
	}
}

// unsettledKey is a stamp looked for in this batch on an epoch not
// settled yet.
type unsettledKey struct {
	what  string
	epoch int
}

func (t *translator) unsettle(what string, epoch int) {
	if t.unsettled == nil {
		t.unsettled = map[unsettledKey]bool{}
	}
	t.unsettled[unsettledKey{what, epoch}] = true
}

// afterBatch notes a resume to follow up once the batch commits: its
// placements read (read), then its timing written if it is complete.
func (t *translator) afterBatch(epoch int, read bool) {
	if t.resumes == nil {
		t.resumes = map[int]bool{}
	}
	t.resumes[epoch] = t.resumes[epoch] || read
}

// resumeFollowUp does what a committed batch noted for each resume: read
// lux's placements, where wanted, then write its timing if it is complete.
// In the background, so a slow lux never holds up the Run's output; both
// steps are idempotent, so two follow-ups of one resume do no harm.
func (s *Syncer) resumeFollowUp(r phaseRun, resumes map[int]bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for epoch, read := range resumes {
		if read {
			if err := s.readPlacements(ctx, r, epoch); err != nil {
				s.logger().Warn("reading a resume's placements failed", "run", r.ID, "epoch", epoch, "error", err)
			}
		}
		if err := s.timeResumes(ctx, r, epoch); err != nil {
			s.logger().Warn("recording a resume's timing failed", "run", r.ID, "epoch", epoch, "error", err)
		}
	}
}

// timeResumesLater writes, in the background, run.resume.timed for every
// resume of r whose first output is in and that is not timed yet: one a
// batch committed whose follow-up never ran (the orchestrator stopped in
// between).
func (s *Syncer) timeResumesLater(r phaseRun) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := s.timeResumes(ctx, r, 0); err != nil {
			s.logger().Warn("recording a resume's timing failed", "run", r.ID, "error", err)
		}
	}()
}

// readPlacements reads the Run from lux and records the placements of the
// resume into epoch.
func (s *Syncer) readPlacements(ctx context.Context, r phaseRun, epoch int) error {
	lr, err := s.Lux.Get(ctx, r.LuxRunID)
	if err != nil {
		return err
	}
	cur, prev := placementsAround(lr.Placements, epoch)
	return s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		return writePlacements(ctx, tx, r.ID, epoch, cur, prev)
	})
}

// The Postgres-side budget of each timing statement in a Run's
// transaction: a row lock held elsewhere, or a slow statement, fails it
// as a statement error the savepoint undoes, instead of holding up the
// Run's batch.
const (
	timingLockTimeout      = 100 * time.Millisecond
	timingStatementTimeout = 250 * time.Millisecond
)

// timingBudget is the lock and statement timeouts for timing work under
// ctx: the defaults, or less when the Run's own deadline is closer, so
// timing always leaves the Run at least half of what remains. false when
// too little remains to try.
func timingBudget(ctx context.Context) (lock, statement time.Duration, ok bool) {
	lock, statement = timingLockTimeout, timingStatementTimeout
	if deadline, has := ctx.Deadline(); has {
		half := time.Until(deadline) / 2
		if half < 5*time.Millisecond {
			return 0, 0, false
		}
		lock, statement = min(lock, half), min(statement, half)
	}
	return lock, statement, true
}

// bestEffort runs fn in a savepoint of tx, under the timing budget: a
// failure is undone and logged, and the transaction carries on as if fn
// had not run, with its own lock_timeout and statement_timeout.
//
// fn's context is never cancelled: the Run's context ending while a
// timing statement waits would make pgx close the connection under the
// Run's transaction. Postgres's timeouts bound it instead.
func (s *Syncer) bestEffort(ctx context.Context, tx pgx.Tx, r phaseRun, what string, fn func(context.Context, pgx.Tx) error) {
	lock, statement, ok := timingBudget(ctx)
	if !ok || ctx.Err() != nil {
		return
	}
	ctx = context.WithoutCancel(ctx)
	var lockWas, statementWas string
	sp, err := tx.Begin(ctx)
	if err == nil {
		// SET LOCAL, in its function form, so the values are parameters.
		err = sp.QueryRow(ctx, `SELECT current_setting('lock_timeout'), current_setting('statement_timeout'),
			set_config('lock_timeout', $1, true), set_config('statement_timeout', $2, true)`,
			strconv.FormatInt(lock.Milliseconds(), 10), strconv.FormatInt(statement.Milliseconds(), 10)).
			Scan(&lockWas, &statementWas, nil, nil)
		if err == nil {
			if err = fn(ctx, sp); err == nil {
				err = sp.Commit(ctx)
			}
		}
		if err != nil {
			// Back to the savepoint, which puts the settings back too.
			_ = sp.Rollback(ctx)
		} else {
			_, err = tx.Exec(ctx, `SELECT set_config('lock_timeout', $1, true), set_config('statement_timeout', $2, true)`,
				lockWas, statementWas)
		}
	}
	if err != nil {
		s.logger().Warn("recording a resume's timing failed", "run", r.ID, "step", what, "error", err)
	}
}

func (s *Syncer) logger() *slog.Logger {
	if s.Log == nil {
		return slog.Default()
	}
	return s.Log
}
