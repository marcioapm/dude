package phases

import (
	"context"
	"log/slog"
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

// recordResume inserts the row for a resume lux has just accepted, in the
// transaction that moves the Run back to running. before is the Run as lux
// reported it just before the resume.
func (s *Syncer) recordResume(ctx context.Context, tx pgx.Tx, r phaseRun, before lux.Run) {
	epoch := nextEpoch(before)
	_, prev := placementsAround(before.Placements, epoch)
	s.bestEffort(ctx, tx, r, "resume", func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO run_resumes (run_id, organization_id, epoch, cause, woken_at, requested_at)
			SELECT r.id, r.organization_id, $2, $3, `+woken+`, clock_timestamp() FROM runs r WHERE r.id = $1 AND r.status = 'paused'
			ON CONFLICT (run_id, epoch) DO NOTHING`, r.ID, epoch, resumeCause(r)); err != nil {
			return err
		}
		return writePlacements(ctx, tx, r.ID, epoch, lux.Placement{}, prev)
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

// resumeRunning records lux reporting the Run running again, and has its
// placements read once the batch commits.
func (t *translator) resumeRunning(ctx context.Context, tx pgx.Tx, s *Syncer, epoch int) {
	s.bestEffort(ctx, tx, t.run, "running", func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `UPDATE run_resumes SET running_at = clock_timestamp()
			WHERE run_id = $1 AND running_at IS NULL AND `+latestResume+` RETURNING epoch`, t.run.ID, epoch)
		if err != nil {
			return err
		}
		got, err := pgx.CollectRows(rows, pgx.RowTo[int])
		for _, e := range got {
			t.afterBatch(e, true)
		}
		return err
	})
}

// resumeBusy records the agent's first busy after a resume: it took its
// input.
func (t *translator) resumeBusy(ctx context.Context, tx pgx.Tx, s *Syncer, epoch int) {
	if epoch != 0 && epoch == t.busyEpoch {
		return
	}
	t.busyEpoch = epoch
	s.bestEffort(ctx, tx, t.run, "busy", func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE run_resumes SET busy_at = clock_timestamp()
			WHERE run_id = $1 AND busy_at IS NULL AND `+latestResume, t.run.ID, epoch)
		return err
	})
}

// resumeOutput records the agent's first output after a resume; its
// timing is written once the batch commits, its placements read again
// first if lux had not reported all of them.
func (t *translator) resumeOutput(ctx context.Context, tx pgx.Tx, s *Syncer, epoch int) {
	if epoch == t.outputEpoch {
		return
	}
	t.outputEpoch = epoch
	s.bestEffort(ctx, tx, t.run, "first output", func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `UPDATE run_resumes SET first_output_at = clock_timestamp()
			WHERE run_id = $1 AND first_output_at IS NULL AND `+latestResume+`
			RETURNING epoch, assigned_at IS NULL OR image_ready_at IS NULL OR volumes_restored_at IS NULL
				OR container_started_at IS NULL OR workload_started_at IS NULL OR host_name IS NULL`, t.run.ID, epoch)
		if err != nil {
			return err
		}
		got, err := pgx.CollectRows(rows, pgx.RowToStructByPos[struct {
			Epoch   int
			Missing bool
		}])
		for _, g := range got {
			t.afterBatch(g.Epoch, g.Missing)
		}
		return err
	})
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

// bestEffort runs fn in a savepoint of tx: a failure is undone and
// logged, and the transaction carries on as if fn had not run.
func (s *Syncer) bestEffort(ctx context.Context, tx pgx.Tx, r phaseRun, what string, fn func(pgx.Tx) error) {
	sp, err := tx.Begin(ctx)
	if err == nil {
		if err = fn(sp); err == nil {
			err = sp.Commit(ctx)
		}
		if err != nil {
			_ = sp.Rollback(ctx)
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
