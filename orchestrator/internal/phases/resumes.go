package phases

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// How long each resume of a Run took, as a person feels it: from when it
// became due (a person's Resume, an answer, an approval) to the agent's
// first output. One run_resumes row per lux resume, keyed by the new
// placement's epoch (migration 067), filled from what dude already handles:
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
	causeMachine    = "machine"
	causePerson     = "person"
	causeIdle       = "idle"
	// A person's message to a parked conductor.
	causeConductor = "conductor"
	// A member's message to a session's parked agent.
	causeSession = "session"
)

// resumeCause is why whilePaused resumes r: a person's Resume, of an idle
// park or of anything else, a message to a parked conductor or session
// agent, or dude's own reason being over.
func resumeCause(r phaseRun) string {
	switch {
	case r.Control == "resume" && r.DudePause == "idle":
		return causeIdle
	case r.Control == "resume":
		return causePerson
	// A person park still waiting yet resumable has no person ask: it is
	// parked only on a peer's reply. No mail to read: resumed for its move.
	case r.DudePause == "machine", r.DudePause == "person" && r.Waiting && r.Moving && !r.HasDirectives:
		return causeMachine
	case r.DudePause == "repository":
		return causeRepository
	case r.DudePause == "conductor":
		return causeConductor
	case r.DudePause == "session":
		return causeSession
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
// A person's Resume: control_requested_at. A repository: its latest
// approval not yet brought (status still approved; a denial brings
// nothing). An answer: the last ask that held the park (delivery.OpenAsk:
// a question, a blocking repository request, or a wait on a peer's reply)
// answered, decided or closed since the park began. The park began at its
// run.parked's parkedAt, on the database's clock like the answers; older
// parks without it fall back to occurred_at, the orchestrator's clock; or,
// for a Run parked only on a peer, mail queued since the park. A
// message to a parked conductor: the first message it has not been sent
// yet. A machine move: when the move was asked for; it settles only after
// lux answers the resume, so here it is still the Run's unsettled one.
const woken = `CASE $3
	WHEN 'person' THEN r.control_requested_at
	WHEN 'idle' THEN r.control_requested_at
	WHEN 'repository' THEN (SELECT max(q.decided_at) FROM repository_requests q WHERE q.run_id = r.id AND q.status = 'approved')
	WHEN 'machine' THEN (SELECT max(m.created_at) FROM machine_moves m WHERE m.run_id = r.id AND m.settled_at IS NULL)
	WHEN 'conductor' THEN (SELECT min(d.created_at) FROM directives d WHERE d.run_id = r.id AND d.sent_at IS NULL AND d.failed_at IS NULL)
	WHEN 'session' THEN (SELECT min(d.created_at) FROM directives d WHERE d.run_id = r.id AND d.sent_at IS NULL AND d.failed_at IS NULL)
	ELSE (SELECT GREATEST(
			(SELECT max(q.answered_at) FROM questions q WHERE q.run_id = r.id AND q.answered_at >= park.at),
			(SELECT max(q.decided_at) FROM repository_requests q WHERE q.run_id = r.id AND q.blocking
				AND q.decided_at >= park.at),
			(SELECT max(w.closed_at) FROM peer_waits w WHERE w.run_id = r.id AND w.closed_at >= park.at),
			(SELECT min(v.occurred_at) FROM events v WHERE v.run_id = r.id AND v.event_type = 'run.checks.conducted'
				AND v.occurred_at >= park.at),
			(SELECT min(d.created_at) FROM directives d WHERE d.run_id = r.id AND d.sent_at IS NULL AND d.failed_at IS NULL
				AND d.created_at >= park.at AND EXISTS (SELECT 1 FROM peer_waits ow WHERE ow.run_id = r.id AND ow.closed_at IS NULL)))
		FROM (SELECT COALESCE(max(COALESCE((e.payload->>'parkedAt')::timestamptz, e.occurred_at)), '-infinity') AS at FROM events e
			WHERE e.run_id = r.id AND e.event_type = 'run.parked') park) END`

// resumeAsked inserts the row of a resume dude is about to ask lux for, in
// a transaction of its own, so frames lux streams before its answer have a
// row to stamp; requested_at is now. A retry moves requested_at only while
// lux still reports the Run stopped (the earlier attempt did not take).
// before is the Run as lux reported it just before. Returns the epoch
// foreseen.
//
// This is the only place the row is made: lost here, the resume is never
// timed. So it is not under the stream's budget but resumeAsked's own
// (resumeLockTimeout, resumeStatementTimeout, per statement), and a lock
// or statement timeout, the database slow for a moment, is tried once
// more in a transaction of its own. The whole call, both attempts, is
// bounded by resumeAskedTimeout besides: it runs on the resume path, in a
// sweep slot, before lux is asked.
func (s *Syncer) resumeAsked(ctx context.Context, r phaseRun, before lux.Run) int {
	epoch := nextEpoch(before)
	ctx, cancel := context.WithTimeout(ctx, resumeAskedTimeout)
	defer cancel()
	for attempt := 1; ; attempt++ {
		err := s.askResume(ctx, r, before, epoch)
		if err == nil {
			break
		}
		// Warn, not Debug: a first attempt timing out is the database
		// slow or a lock held across the resume (a lock-order regression
		// would show here first), not noise.
		if attempt == 1 && databaseTimedOut(err) && ctx.Err() == nil {
			s.logger().Warn("recording a resume's timing timed out; trying again", "run", r.ID, "step", "resume",
				"error", err)
			continue
		}
		s.logger().Warn("recording a resume's timing failed", "run", r.ID, "step", "resume", "error", err)
		break
	}
	return epoch
}

// resumeAskedTimeout bounds resumeAsked, both its attempts and all their
// statements: what a slow database can add to a resume before lux is
// asked, in one of the sweep's slots. Within it each attempt's statements
// get resumeLockTimeout and resumeStatementTimeout; without it an
// attempt's three statements could take 3 × 2s, two attempts 12s. 3s is
// the common case, a lock held past resumeLockTimeout (a follower batch,
// an UPDATE of the Run), covered twice over: the first attempt gives up
// at 1s, and the second has a full 1s lock wait and time left for its
// statements. The context is the bound: a statement still running at it
// is cancelled, which here, in a transaction of its own, costs that
// connection, not a Run's batch.
const resumeAskedTimeout = 3 * time.Second

// databaseTimedOut is whether err is Postgres giving up on a lock wait
// (55P03) or a statement (57014): the database slow, not the statement
// wrong. 57014 is also a statement an administrator cancelled
// (pg_cancel_backend); trying it once more is as harmless.
func databaseTimedOut(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && (pg.Code == "55P03" || pg.Code == "57014")
}

// askResume is one attempt of resumeAsked, in a transaction of its own,
// under ctx: cancelled at resumeAsked's bound, it is rolled back. ctx's
// deadline bounds its statements as a whole; Postgres's timeouts, each
// statement's.
func (s *Syncer) askResume(ctx context.Context, r phaseRun, before lux.Run, epoch int) error {
	_, prev := placementsAround(before.Placements, epoch)
	if deadline, has := ctx.Deadline(); has && time.Until(deadline) < 10*time.Millisecond {
		return fmt.Errorf("too little time left to record the resume: %w", context.DeadlineExceeded)
	}
	return s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT set_config('lock_timeout', $1, true), set_config('statement_timeout', $2, true)`,
			strconv.FormatInt(resumeLockTimeout.Milliseconds(), 10),
			strconv.FormatInt(resumeStatementTimeout.Milliseconds(), 10)); err != nil {
			return err
		}
		// The Run's row before the resume's: a retry's new row version
		// makes writePlacements' foreign-key check take this KEY SHARE,
		// and taken then, it would wait on a follower batch holding the
		// Run while that batch waits to stamp the row. Nothing here
		// writes runs, so KEY SHARE is all it takes, early.
		if _, err := tx.Exec(ctx, `SELECT 1 FROM runs WHERE id = $1 FOR KEY SHARE`, r.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO run_resumes (run_id, organization_id, epoch, cause, woken_at, requested_at)
			SELECT r.id, r.organization_id, $2, $3, `+woken+`, clock_timestamp() FROM runs r WHERE r.id = $1 AND r.status = 'paused'
			ON CONFLICT (run_id, epoch) DO UPDATE SET requested_at = EXCLUDED.requested_at
			WHERE run_resumes.running_at IS NULL AND $4`, r.ID, epoch, resumeCause(r), lux.Terminal(before.State)); err != nil {
			return err
		}
		return writePlacements(ctx, tx, r.ID, epoch, lux.Placement{}, prev)
	})
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
// foreseen, unless lux names a later one. lux answers with the Run's
// current epoch, still the stopped one until its scheduler assigns the new
// placement; a later epoch means lux had already assigned it and moved on.
// A 409 carries no epoch (0).
// Known limit: a fresh resume from a newer epoch lux placed and stopped on
// its own after dude's Get looks already assigned, and goes untimed (the
// Run is unaffected); telling them apart needs placement data the answer
// lacks, or a Get per resume, which dude does not make.
func resumedEpoch(foreseen int, resumed lux.Run) int {
	return max(foreseen, resumed.Epoch)
}

// resumeAccepted moves the row of a resume lux accepted to the epoch
// resumedEpoch names, when that is above the one foreseen; in the
// transaction that takes the Run out of paused, its row locked, before
// the Run row is updated.
//
// Frames of that epoch committed before the move found no row, and when
// they came cannot be recovered: the agent's events carry no epoch, and a
// chunk may not be an event yet. The only epoch-qualified trace is the
// Run's agent_session_epoch, from the shim's session record, the resumed
// placement's first record. If it already reached the epoch, the row is
// marked frames_missed and true is returned, for it to be timed as it
// stands once the transaction commits: no later frame stamps it.
// lux_state and agent_active_at carry no epoch, so an older placement's
// trailing frame may have set them; they mark nothing.
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
// epoch is about, unless that row is frames_missed or, but for running_at,
// already timed (untimed). It returns the epoch it stamped, if
// any; whether lux had yet to report some of its new placement; and
// whether the epoch is settled: its row is there, or none will ever be.
// An epoch with no row is unsettled only while the Run is paused (lux's
// answer, which may move a row to it, is not in) and the epoch is newer
// than every row; epoch 1 is no resume.
//
// The stamp is the frame's time on Postgres's clock: clock_timestamp()
// read by the savepoint's own set-up statement (savepointAt), before the
// UPDATE can wait on anything, at no extra round trip. If the UPDATE runs
// out of the stream's budget (the row locked elsewhere, the database slow
// for a moment), that time is kept (deferStamp) and written once the
// batch commits, off the batch, before the resume is timed
// (writeLateStamps, from timeResumes): the next such frame may come only
// on the agent's next turn, after the resume was timed without it. The
// batch waits no longer for it than before. The epoch stays unsettled, as
// on any failure: a later frame may stamp it in a later batch, and
// whichever of the two is written first is kept (each writes only an
// empty column).
//
// A frame with no epoch (0) is about the latest resume as the frame is
// applied. A stamp kept for later names that resume, resolved in this
// batch, so it is not whichever is latest when it is written: right after
// the UPDATE timed out, by a plain read that waits on no row lock, under
// the stream's budget. Only then: a stamp that goes in costs no extra
// round trip.
func (t *translator) stamp(ctx context.Context, tx pgx.Tx, s *Syncer, col string, epoch int) (stamped *int, missing, settled bool) {
	var at time.Time
	ran, err := s.savepointAt(ctx, tx, timingLockTimeout, timingStatementTimeout, func(ctx context.Context, tx pgx.Tx, now time.Time) error {
		at = now
		return tx.QueryRow(ctx, `WITH target AS (SELECT epoch FROM run_resumes WHERE run_id = $1 AND `+latestResume+`),
			stamped AS (UPDATE run_resumes SET `+col+` = $3
				WHERE run_id = $1 AND epoch IN (SELECT epoch FROM target) AND `+col+` IS NULL AND NOT frames_missed
					AND `+untimed(col)+`
				RETURNING epoch, assigned_at IS NULL OR image_ready_at IS NULL OR volumes_restored_at IS NULL
					OR container_started_at IS NULL OR workload_started_at IS NULL OR host_name IS NULL AS missing)
			SELECT (SELECT epoch FROM stamped), COALESCE((SELECT missing FROM stamped), false),
				EXISTS (SELECT 1 FROM target) OR $2 = 1
				OR COALESCE($2 < (SELECT max(epoch) FROM run_resumes WHERE run_id = $1), false)
				OR (SELECT status FROM runs WHERE id = $1) <> 'paused'`, t.run.ID, epoch, now).
			Scan(&stamped, &missing, &settled)
	})
	switch {
	case err == nil && ran:
		return stamped, missing, settled
	case err == nil:
		// Too little of the Run's time left to try: unsettled, as on a
		// failure, for a later frame to stamp.
		return nil, false, false
	case databaseTimedOut(err) && !at.IsZero():
		target, rerr := t.resumeOf(ctx, tx, s, epoch)
		if rerr != nil || target == 0 {
			s.logger().Warn("recording a resume's timing failed", "run", t.run.ID, "step", col, "epoch", epoch,
				"error", errors.Join(err, rerr))
			return nil, false, false
		}
		t.deferStamp(s, col, target, at)
		s.logger().Warn("recording a resume's timing timed out; writing it after the batch", "run", t.run.ID,
			"step", col, "epoch", target, "error", err)
		return nil, false, false
	}
	s.logger().Warn("recording a resume's timing failed", "run", t.run.ID, "step", col, "error", err)
	return nil, false, false
}

// resumeOf is the epoch of the resume a frame of epoch is about: epoch,
// or for a frame with none (0) the Run's latest resume, 0 for none.
func (t *translator) resumeOf(ctx context.Context, tx pgx.Tx, s *Syncer, epoch int) (int, error) {
	if epoch != 0 {
		return epoch, nil
	}
	var latest *int
	ran, err := s.savepointAt(ctx, tx, timingLockTimeout, timingStatementTimeout, func(ctx context.Context, tx pgx.Tx, _ time.Time) error {
		return tx.QueryRow(ctx, `SELECT max(epoch) FROM run_resumes WHERE run_id = $1`, t.run.ID).Scan(&latest)
	})
	if err != nil || !ran || latest == nil {
		return 0, err
	}
	return *latest, nil
}

// untimed (SQL) is the condition for col to be stamped on a resume:
// before it is timed, so nothing lands on a resume after its
// run.resume.timed (a later turn's busy would come after first_output_at
// and contradict it). Except running_at: lux's running state trails the
// agent's records and may come after the first output, and more than
// timing reads it — roundSyncLanded bounds a continuation's sync wait
// from it, and resumeAsked's retry keeps requested_at once it is in — so
// it is stamped whenever it comes, as before.
func untimed(col string) string {
	if col == "running_at" {
		return "true"
	}
	return "timed_at IS NULL"
}

// lateStamp is a stamp a batch could not write in its budget: col of the
// resume into epoch, the one its frame was about as it was applied (a
// frame with no epoch resolved to the latest then, by stamp), at the
// frame's time on Postgres's clock.
type lateStamp struct {
	col   string
	epoch int
	at    time.Time
}

// lateCols are the columns a stamp can be kept for: stamp's.
var lateCols = []string{"running_at", "busy_at", "first_output_at"}

// deferStamp keeps a stamp the batch could not write, and has the batch's
// follow-up write it and time the resume, if it can be. Kept on the
// Syncer from here, inside the batch's transaction, as owe is, so no
// timing that sees the batch committed misses it; a batch rolled back
// takes it back (rolledBack), and is read again and stamps again. A
// timing that writes it meanwhile writes the time this frame was applied,
// which the replay's COALESCE keeps.
//
// One per column and resume: one already kept is an earlier batch's,
// committed (a Run's batches run one at a time, and one rolled back took
// its own back), so its frame came first and its time is the one to keep;
// its follow-up is still to come or already failed, and either way this
// one adds nothing to it. So a Run whose row stays locked costs one
// follow-up per stamp, not one per contended batch.
func (t *translator) deferStamp(s *Syncer, col string, epoch int, at time.Time) {
	l := lateStamp{col, epoch, at}
	s.lateMu.Lock()
	if s.late == nil {
		s.late = map[string][]lateStamp{}
	}
	if slices.ContainsFunc(s.late[t.run.ID], func(k lateStamp) bool { return k.col == col && k.epoch == epoch }) {
		s.lateMu.Unlock()
		return
	}
	s.late[t.run.ID] = append(s.late[t.run.ID], l)
	s.lateMu.Unlock()
	t.late = append(t.late, l)
	// A placement read wants a known epoch; running_at or first_output_at
	// stamped would have asked for one (or might have: lux's placements
	// not all in).
	t.afterBatch(s, epoch, col != "busy_at")
}

// committed is the batch committed: it returns the resumes for its
// follow-up, nil for none, and the stamps it kept, for that follow-up to
// write whatever becomes of the Syncer's copy (forgetLate).
func (t *translator) committed() (map[int]bool, []lateStamp) {
	resumes, late := t.resumes, t.late
	t.resumes, t.late = nil, nil
	return resumes, late
}

// rolledBack forgets what a batch that failed noted: what was read but not
// recorded is read again from the saved cursor by the next follower;
// nothing of it is owed, and none of its late stamps kept.
func (t *translator) rolledBack(s *Syncer) {
	for epoch, read := range t.resumes {
		if read {
			s.paid(t.run.ID, epoch)
		}
	}
	s.lateDone(t.run.ID, t.late)
	t.resumes, t.late = nil, nil
}

// lateStamps are the Run's late stamps kept so far, and own besides (the
// follow-up's batch's, which the Syncer may have forgotten since).
func (s *Syncer) lateStamps(runID string, own ...lateStamp) []lateStamp {
	s.lateMu.Lock()
	defer s.lateMu.Unlock()
	late := slices.Clone(s.late[runID])
	for _, o := range own {
		if !slices.ContainsFunc(late, func(l lateStamp) bool { return l.col == o.col && l.epoch == o.epoch && l.at.Equal(o.at) }) {
			late = append(late, o)
		}
	}
	return late
}

// forgetLate forgets every late stamp of the Run: its follower is over,
// every batch of it committed or abandoned, and each committed one's
// follow-up holds its own (committed). What is left would otherwise stay
// for as long as the process runs.
func (s *Syncer) forgetLate(runID string) {
	s.lateMu.Lock()
	defer s.lateMu.Unlock()
	delete(s.late, runID)
}

// lateDone forgets the Run's late stamps that are in the database, or
// never will be.
func (s *Syncer) lateDone(runID string, done []lateStamp) {
	if len(done) == 0 {
		return
	}
	s.lateMu.Lock()
	defer s.lateMu.Unlock()
	kept := slices.DeleteFunc(s.late[runID], func(l lateStamp) bool {
		return slices.ContainsFunc(done, func(d lateStamp) bool { return d.col == l.col && d.epoch == l.epoch && d.at.Equal(l.at) })
	})
	if len(kept) == 0 {
		delete(s.late, runID)
		return
	}
	s.late[runID] = kept
}

// lockUntimed locks, in tx, every resume of the Run not timed yet, in
// epoch order, before its late stamps are read (writeLateStamps). A batch
// that stamped a resume holds its row until it commits, and keeps the
// stamps it could not write (deferStamp) before it commits; so once these
// locks are granted, every stamp kept by a batch whose output the timing
// can see is in the Syncer's copy. Taken later, a batch could keep busy_at
// and commit first_output_at between the read and the timing, and the
// resume be timed without its busy, for good.
//
// Every untimed resume, not only those being timed: the late stamps may be
// of any of them, and two timings of a Run locking different sets would
// each wait on the other's. In epoch order, as two timings of a Run take
// them, so neither waits on the other while holding what it needs. FOR NO
// KEY UPDATE, as the timed_at UPDATE takes them anyway: nothing conflicts
// with it that did not already, and like that UPDATE it waits under tx's
// own bounds (no lock_timeout; the follow-up's or recovery's context), so
// the wait is the one the UPDATE made before, moved earlier. A batch's
// stamp waits on it under the stream's budget and keeps its stamp for its
// own follow-up; resumeAsked under its own, and on a resume's own row
// only on a retry; neither takes these rows before the Run's.
func lockUntimed(ctx context.Context, tx pgx.Tx, runID string) error {
	_, err := tx.Exec(ctx, `SELECT 1 FROM run_resumes WHERE run_id = $1 AND timed_at IS NULL ORDER BY epoch FOR NO KEY UPDATE`, runID)
	return err
}

// writeLateStamps writes, in tx, the late stamps batches of the Run kept
// (and own, the follow-up's batch's), each only where its column is still
// empty and, as stamp does (untimed), the resume not timed yet: the
// frame's time is kept, a stamp a batch got in meanwhile wins (earlier,
// or the same frame replayed), and nothing lands on a timed resume. One
// statement, under resumeAsked's budget, not the stream's: off the Run's
// batch, it can wait out the lock or the slow moment the batch could not,
// and holds the rows it locks no longer than that budget all told. A
// stamp that lands nowhere, its column still empty, is logged.
//
// They are tried once: forgotten once tx commits (the returned func),
// written or not. A failure is logged, and the resume timed without them.
// A transaction that does not commit, or a write never tried (too little
// time left), leaves them to the Run's next timing.
func (s *Syncer) writeLateStamps(ctx context.Context, tx pgx.Tx, r phaseRun, own []lateStamp) (committed func()) {
	late := s.lateStamps(r.ID, own...)
	if len(late) == 0 {
		return func() {}
	}
	cols, epochs, ats := make([]string, len(late)), make([]int, len(late)), make([]time.Time, len(late))
	for i, l := range late {
		cols[i], epochs[i], ats[i] = l.col, l.epoch, l.at
	}
	set, filters := "", ""
	for i, c := range lateCols {
		if i > 0 {
			set, filters = set+", ", filters+", "
		}
		set += c + ` = CASE WHEN ` + untimed(c) + ` THEN COALESCE(rr.` + c + `, l.` + c + `) ELSE rr.` + c + ` END`
		filters += `min(at) FILTER (WHERE col = '` + c + `') AS ` + c
	}
	type landed struct {
		Epoch                      int
		Running, Busy, FirstOutput *time.Time
	}
	var rows []landed
	ran, err := s.savepointAt(ctx, tx, resumeLockTimeout, resumeStatementTimeout, func(ctx context.Context, tx pgx.Tx, _ time.Time) error {
		got, err := tx.Query(ctx, `WITH l AS (SELECT epoch, `+filters+`
				FROM unnest($2::text[], $3::int[], $4::timestamptz[]) AS l(col, epoch, at) GROUP BY epoch),
			u AS (UPDATE run_resumes rr SET `+set+`
				FROM l WHERE rr.run_id = $1 AND rr.epoch = l.epoch AND NOT rr.frames_missed
					AND (rr.timed_at IS NULL OR l.running_at IS NOT NULL)
				RETURNING rr.epoch, rr.running_at, rr.busy_at, rr.first_output_at)
			SELECT l.epoch, COALESCE(u.running_at, rr.running_at), COALESCE(u.busy_at, rr.busy_at),
				COALESCE(u.first_output_at, rr.first_output_at)
			FROM l LEFT JOIN u ON u.epoch = l.epoch LEFT JOIN run_resumes rr ON rr.run_id = $1 AND rr.epoch = l.epoch`,
			r.ID, cols, epochs, ats)
		if err != nil {
			return err
		}
		rows, err = pgx.CollectRows(got, pgx.RowToStructByPos[landed])
		return err
	})
	switch {
	case err != nil:
		s.logger().Warn("recording a resume's timing failed", "run", r.ID, "step", "late", "error", err)
	case !ran:
		return func() {}
	default:
		for _, l := range late {
			i := slices.IndexFunc(rows, func(w landed) bool { return w.Epoch == l.epoch })
			if i >= 0 && map[string]*time.Time{"running_at": rows[i].Running, "busy_at": rows[i].Busy,
				"first_output_at": rows[i].FirstOutput}[l.col] != nil {
				continue
			}
			s.logger().Warn("a resume's late timing stamp landed nowhere: its resume is timed, missed or gone", "run", r.ID,
				"step", "late", "column", l.col, "epoch", l.epoch)
		}
	}
	return func() { s.lateDone(r.ID, late) }
}

// resumeRunning records lux reporting the Run running again, and has its
// placements read once the batch commits.
func (t *translator) resumeRunning(ctx context.Context, tx pgx.Tx, s *Syncer, epoch int) {
	if e, _, _ := t.stamp(ctx, tx, s, "running_at", epoch); e != nil {
		t.afterBatch(s, *e, true)
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
	if t.run.AutomationRunID != "" || epoch == t.outputEpoch || t.unsettled[unsettledKey{"output", epoch}] {
		return
	}
	e, missing, settled := t.stamp(ctx, tx, s, "first_output_at", epoch)
	if e != nil {
		t.afterBatch(s, *e, missing)
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
// placements read (read), then its timing written if it is complete. A
// read is owed from here, inside the batch's transaction, so recovery
// never sees the committed row without the mark; at most once per epoch
// per batch, as the batch's follow-up pays it once.
func (t *translator) afterBatch(s *Syncer, epoch int, read bool) {
	if t.resumes == nil {
		t.resumes = map[int]bool{}
	}
	if read && !t.resumes[epoch] {
		s.owe(t.run.ID, epoch)
	}
	t.resumes[epoch] = t.resumes[epoch] || read
}

// owedResume is a resume a live follow-up has yet to read the placements of.
type owedResume struct {
	runID string
	epoch int
}

// owe marks the Run's resume into epoch as having a follow-up to come that
// reads its placements; paid undoes one owe.
func (s *Syncer) owe(runID string, epoch int) {
	s.owingMu.Lock()
	defer s.owingMu.Unlock()
	if s.owing == nil {
		s.owing = map[owedResume]int{}
	}
	s.owing[owedResume{runID, epoch}]++
}

func (s *Syncer) paid(runID string, epoch int) {
	s.owingMu.Lock()
	defer s.owingMu.Unlock()
	k := owedResume{runID, epoch}
	if s.owing[k] <= 1 {
		delete(s.owing, k)
		return
	}
	s.owing[k]--
}

// owed is the epochs of the Run's resumes with a placement read to come.
func (s *Syncer) owed(runID string) map[int]bool {
	s.owingMu.Lock()
	defer s.owingMu.Unlock()
	epochs := map[int]bool{}
	for k := range s.owing {
		if k.runID == runID {
			epochs[k.epoch] = true
		}
	}
	return epochs
}

// resumeFollowUp does what a committed batch noted for each resume: read
// lux's placements, where wanted, then write its timing if it is complete.
// In the background, so a slow lux never holds up the Run's output; both
// steps are idempotent, so two follow-ups of one resume do no harm. A
// resume it reads is its own to time: recovery skips it until the read
// and the timing are done, whether they succeeded or not. late are the
// stamps the batch kept (committed), written by its first timing that
// commits even if the Run's follower has ended and the Syncer forgotten
// them since (forgetLate).
func (s *Syncer) resumeFollowUp(r phaseRun, resumes map[int]bool, late ...lateStamp) {
	timeout := s.followUpTimeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	for epoch, read := range resumes {
		if s.followUp(ctx, r, epoch, read, late) {
			late = nil
		}
	}
	if s.followedUp != nil {
		s.followedUp()
	}
}

// followUpTimingTimeout is the budget of a follow-up's own timing, apart
// from its placement read's: a read that ran out of time still leaves the
// owner time to publish the resume, without the placements.
const followUpTimingTimeout = 5 * time.Second

// followUp is resumeFollowUp's for one resume; timed is whether its timing
// committed, late with it.
func (s *Syncer) followUp(ctx context.Context, r phaseRun, epoch int, read bool, late []lateStamp) (timed bool) {
	if read {
		defer s.paid(r.ID, epoch)
		if err := s.readPlacements(ctx, r, epoch); err != nil {
			s.logger().Warn("reading a resume's placements failed", "run", r.ID, "epoch", epoch, "error", err)
		}
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), followUpTimingTimeout)
	defer cancel()
	if err := s.timeResumes(ctx, r, epoch, late...); err != nil {
		s.logger().Warn("recording a resume's timing failed", "run", r.ID, "epoch", epoch, "error", err)
		return false
	}
	return true
}

// timeResumesLater writes, in the background, run.resume.timed for every
// resume of r whose first output is in and that is not timed yet: one a
// batch committed whose follow-up never ran (the orchestrator stopped in
// between). A resume a live follow-up still has to read the placements of
// is left to it, for its timing to carry them.
func (s *Syncer) timeResumesLater(r phaseRun) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := s.timeUnowned(ctx, r, 0); err != nil {
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
// effort, a failure is logged. Each goes through timeUnowned, which
// publishes each resume once and leaves one a live follow-up of this
// process still reads the placements of to it. Returns how many it went
// through.
func (s *Syncer) TimeUntimedResumes(ctx context.Context) int {
	type untimed struct {
		run   phaseRun
		epoch int
	}
	var due []untimed
	if err := s.DB.InSystem(ctx, "resume-timing", func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT r.id, r.organization_id, COALESCE(r.project_id, ''), COALESCE(r.task_id, ''), rr.epoch
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
		if err := s.timeUnowned(ctx, u.run, u.epoch); err != nil {
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

// The Postgres-side budget of each timing statement written from a Run's
// stream (stamp, resumeAccepted, resumeRefused): a row lock held
// elsewhere, or a slow statement, fails it as a statement error the
// savepoint undoes, instead of holding up the Run's batch. A stamp that
// fails so is not lost: its time is kept and written after the batch
// (stamp, writeLateStamps); resumeAccepted's and resumeRefused's writes
// cost the resume only that step.
const (
	timingLockTimeout      = 100 * time.Millisecond
	timingStatementTimeout = 250 * time.Millisecond
)

// The budget of resumeAsked's statements, which make a resume's row: the
// only place it is made, so losing it loses the resume's timing for good
// (stamp updates a row, and nothing times a resume without one). It is
// off any batch: its own transaction, on the resume path, before lux is
// asked. It waits on a follower batch holding the Run, a checkpoint or
// autovacuum; a batch starting meanwhile waits on its KEY SHARE no longer
// than its statements take. Each is per statement, and an attempt runs
// three; resumeAskedTimeout bounds the whole call, so a Run held for long
// costs its resume at most that, and only its timing.
//
// The same budget writes a batch's late stamps (writeLateStamps), off the
// batch too, in one statement.
const (
	resumeLockTimeout      = time.Second
	resumeStatementTimeout = 2 * time.Second
)

// budgetUnder is the lock and statement timeouts for timing work under
// ctx: those given, or less when the Run's own deadline is closer, so
// timing always leaves the Run at least half of what remains. false when
// too little remains to try.
func budgetUnder(ctx context.Context, lock, statement time.Duration) (time.Duration, time.Duration, bool) {
	if deadline, has := ctx.Deadline(); has {
		half := time.Until(deadline) / 2
		if half < 5*time.Millisecond {
			return 0, 0, false
		}
		lock, statement = min(lock, half), min(statement, half)
	}
	return lock, statement, true
}

// bestEffort runs fn in a savepoint of tx, under the stream's timing
// budget: a failure is undone and logged, and the transaction carries on
// as if fn had not run, with its own lock_timeout and statement_timeout.
//
// fn's context is never cancelled: the Run's context ending while a
// timing statement waits would make pgx close the connection under the
// Run's transaction. Postgres's timeouts bound it instead.
func (s *Syncer) bestEffort(ctx context.Context, tx pgx.Tx, r phaseRun, what string, fn func(context.Context, pgx.Tx) error) {
	if err := s.trySavepoint(ctx, tx, timingLockTimeout, timingStatementTimeout, fn); err != nil {
		s.logger().Warn("recording a resume's timing failed", "run", r.ID, "step", what, "error", err)
	}
}

// trySavepoint is bestEffort under the given lock and statement timeouts,
// cut by budgetUnder, returning fn's failure, undone, for the caller to
// log. Nothing runs, and nil is returned, when too little time remains.
func (s *Syncer) trySavepoint(ctx context.Context, tx pgx.Tx, lock, statement time.Duration,
	fn func(context.Context, pgx.Tx) error) error {
	_, err := s.savepointAt(ctx, tx, lock, statement, func(ctx context.Context, tx pgx.Tx, _ time.Time) error {
		return fn(ctx, tx)
	})
	return err
}

// savepointAt is trySavepoint, handing fn Postgres's clock_timestamp() as
// the savepoint was set up: read with its settings, before fn's own
// statements can wait on anything, at no round trip of its own. ran is
// whether it tried: false, with no error, when too little time remained.
func (s *Syncer) savepointAt(ctx context.Context, tx pgx.Tx, lock, statement time.Duration,
	fn func(context.Context, pgx.Tx, time.Time) error) (ran bool, err error) {
	lock, statement, ok := budgetUnder(ctx, lock, statement)
	if !ok || ctx.Err() != nil {
		return false, nil
	}
	ctx = context.WithoutCancel(ctx)
	var lockWas, statementWas string
	var now time.Time
	sp, err := tx.Begin(ctx)
	if err == nil {
		// SET LOCAL, in its function form, so the values are parameters.
		err = sp.QueryRow(ctx, `SELECT current_setting('lock_timeout'), current_setting('statement_timeout'),
			set_config('lock_timeout', $1, true), set_config('statement_timeout', $2, true), clock_timestamp()`,
			strconv.FormatInt(lock.Milliseconds(), 10), strconv.FormatInt(statement.Milliseconds(), 10)).
			Scan(&lockWas, &statementWas, nil, nil, &now)
		if err == nil {
			if err = fn(ctx, sp, now); err == nil {
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
	return true, err
}

func (s *Syncer) logger() *slog.Logger {
	if s.Log == nil {
		return slog.Default()
	}
	return s.Log
}
