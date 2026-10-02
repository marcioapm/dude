package phases

import (
	"context"
	"errors"
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
// A person's Resume is control_requested_at. An approved repository is
// the latest approval not yet brought (status still approved): a denial
// brings nothing. An answer is the last of the asks that held the park
// (delivery.OpenAsk: a question, or a blocking repository request) to be
// answered or decided since the park began; an ask that held nothing, or
// closed before this park, made nothing due. The park began at its
// run.parked's parkedAt, on the database's clock as answers and decisions
// are; a park recorded before parkedAt existed falls back to the event's
// occurred_at, the orchestrator's clock.
const woken = `CASE $3
	WHEN 'person' THEN r.control_requested_at
	WHEN 'idle' THEN r.control_requested_at
	WHEN 'repository' THEN (SELECT max(q.decided_at) FROM repository_requests q WHERE q.run_id = r.id AND q.status = 'approved')
	ELSE (SELECT GREATEST(
			(SELECT max(q.answered_at) FROM questions q WHERE q.run_id = r.id AND q.answered_at >= park.at),
			(SELECT max(q.decided_at) FROM repository_requests q WHERE q.run_id = r.id AND q.blocking
				AND q.decided_at >= park.at))
		FROM (SELECT COALESCE(max(COALESCE((e.payload->>'parkedAt')::timestamptz, e.occurred_at)), '-infinity') AS at FROM events e
			WHERE e.run_id = r.id AND e.event_type = 'run.parked') park) END`

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

// resumedEpoch is the epoch a resume lux accepted is for: the one
// foreseen, unless lux's answer names a later one. lux answers with the
// Run's current epoch, which stays the stopped one until its scheduler
// assigns the new placement, after the answer; an epoch above the
// foreseen one means lux had already assigned it and moved on (a
// placement failed and was rescheduled). No epoch (a 409) is 0.
func resumedEpoch(foreseen int, resumed lux.Run) int {
	return max(foreseen, resumed.Epoch)
}

// resumeAccepted moves the row of a resume lux accepted to the epoch lux
// says it is for, when that is above the one foreseen (resumedEpoch); in
// the transaction that takes the Run out of paused, with the Run's row
// locked, before it is updated.
//
// Frames of that epoch the stream committed before the move found no row
// to stamp, and nothing durable records when they came: the agent's
// events carry no epoch, and a chunk may not be an event yet. The only
// epoch-qualified trace on the Run's row is agent_session_epoch: the
// shim's session record for the epoch, the first record of a resumed
// placement. Once it has reached the epoch, the row is marked
// frames_missed, and true is returned, for its timing to be written as it
// stands once the transaction commits: no later frame stamps it.
// lux_state and agent_active_at record no epoch, so a trailing frame of an
// older placement could have set them; they mark nothing.
func (s *Syncer) resumeAccepted(ctx context.Context, tx pgx.Tx, r phaseRun, foreseen int, resumed lux.Run) (missed bool) {
	epoch := resumedEpoch(foreseen, resumed)
	if epoch == foreseen {
		return false
	}
	s.bestEffort(ctx, tx, r, "accepted", func(ctx context.Context, tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `UPDATE run_resumes rr SET epoch = $3,
				frames_missed = COALESCE(r.agent_session_epoch >= $3, false)
			FROM runs r WHERE r.id = rr.run_id AND rr.run_id = $1 AND rr.epoch = $2
			AND NOT EXISTS (SELECT 1 FROM run_resumes WHERE run_id = $1 AND epoch = $3)
			RETURNING rr.frames_missed`, r.ID, foreseen, epoch).Scan(&missed)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	})
	return missed
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
// is newer than every row. The first placement (epoch 1) is no resume. A
// row whose first frames were missed (frames_missed) takes no stamp.
func (t *translator) stamp(ctx context.Context, tx pgx.Tx, s *Syncer, col string, epoch int) (stamped *int, missing, settled bool) {
	s.bestEffort(ctx, tx, t.run, col, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `WITH target AS (SELECT epoch FROM run_resumes WHERE run_id = $1 AND `+latestResume+`),
			stamped AS (UPDATE run_resumes SET `+col+` = clock_timestamp()
				WHERE run_id = $1 AND epoch IN (SELECT epoch FROM target) AND `+col+` IS NULL AND NOT frames_missed
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
	if s.followedUp != nil {
		s.followedUp()
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

// The startup pass's bounds: resumes this recent, this many at most.
const (
	untimedWithin = 7 * 24 * time.Hour
	untimedLimit  = 500
)

// TimeUntimedResumes writes run.resume.timed for every recent resume whose
// first output is in and that was never timed, whatever its Run's status:
// a Run that ended while its follow-up was pending, and whose process then
// stopped, has nothing else left to time it. Once, at startup; best
// effort, a failure is logged. Each goes through timeResumes, which
// publishes each resume once. Returns how many it went through.
func (s *Syncer) TimeUntimedResumes(ctx context.Context) int {
	type untimed struct {
		run   phaseRun
		epoch int
	}
	var due []untimed
	if err := s.DB.InSystem(ctx, "resume-timing", func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT r.id, r.organization_id, r.project_id, r.task_id, rr.epoch
			FROM run_resumes rr JOIN runs r ON r.id = rr.run_id
			WHERE rr.timed_at IS NULL AND (rr.first_output_at IS NOT NULL OR rr.frames_missed)
			  AND rr.created_at > now() - make_interval(secs => $1)
			ORDER BY rr.created_at LIMIT $2`, untimedWithin.Seconds(), untimedLimit)
		if err != nil {
			return err
		}
		due, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (u untimed, err error) {
			err = row.Scan(&u.run.ID, &u.run.Org, &u.run.ProjectID, &u.run.TaskID, &u.epoch)
			return u, err
		})
		return err
	}); err != nil {
		s.logger().Warn("finding resumes never timed failed", "error", err)
		return 0
	}
	for _, u := range due {
		if err := s.timeResumes(ctx, u.run, u.epoch); err != nil {
			s.logger().Warn("recording a resume's timing failed", "run", u.run.ID, "epoch", u.epoch, "error", err)
		}
	}
	return len(due)
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
