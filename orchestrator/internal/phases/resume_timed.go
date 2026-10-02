package phases

import (
	"context"
	"math"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// evResumeTimed is written once per resume, when its first output is in.
const evResumeTimed = "run.resume.timed"

// resumeTiming is a run_resumes row, as its event reports it.
type resumeTiming struct {
	Epoch    int
	Cause    string
	Moved    *bool
	HostName *string
	Woken, Requested, Assigned, ImageReady, Restored, WorkloadStarted,
	Running, Busy, FirstOutput *time.Time
}

// timeResumes writes run.resume.timed for each of the Run's resumes whose
// first output is in, or whose first frames were missed (frames_missed),
// and that has none yet — the one into epoch, or with epoch 0 every one (a
// follower starting again, after a batch whose follow-up never ran). Once
// each: timed_at is set in the same transaction.
func (s *Syncer) timeResumes(ctx context.Context, r phaseRun, epoch int) error {
	return s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `UPDATE run_resumes SET timed_at = now()
			WHERE run_id = $1 AND ($2 = 0 OR epoch = $2) AND timed_at IS NULL
			  AND (first_output_at IS NOT NULL OR frames_missed)
			RETURNING epoch, cause, moved, host_name, woken_at, requested_at, assigned_at, image_ready_at,
				volumes_restored_at, workload_started_at, running_at, busy_at, first_output_at`, r.ID, epoch)
		if err != nil {
			return err
		}
		timed, err := pgx.CollectRows(rows, pgx.RowToStructByPos[resumeTiming])
		if err != nil {
			return err
		}
		for _, rt := range timed {
			if err := s.event(ctx, tx, r, evResumeTimed, ledger.ActorSystem, rt.payload()); err != nil {
				return err
			}
		}
		return nil
	})
}

// payload is run.resume.timed's: the resume's numbers in milliseconds.
// totalMs is until the agent said something, untilBusyMs until it took its
// input. A phase whose start or end is unknown is left out, not zero.
func (rt resumeTiming) payload() map[string]any {
	phases := map[string]any{}
	for _, p := range []struct {
		name       string
		start, end *time.Time
	}{
		{"react", rt.Woken, rt.Requested},
		{"schedule", rt.Requested, rt.Assigned},
		{"image", rt.Assigned, rt.ImageReady},
		{"restore", rt.ImageReady, rt.Restored},
		{"start", rt.Restored, rt.WorkloadStarted},
		{"reload", rt.WorkloadStarted, rt.Running},
		{"take", rt.Running, rt.Busy},
		{"firstOutput", rt.Busy, rt.FirstOutput},
	} {
		if ms, ok := millis(p.start, p.end); ok {
			phases[p.name] = ms
		}
	}
	out := map[string]any{"epoch": rt.Epoch, "cause": rt.Cause, "moved": rt.Moved, "hostName": rt.HostName, "phases": phases}
	if ms, ok := millis(rt.Woken, rt.FirstOutput); ok {
		out["totalMs"] = ms
	}
	if ms, ok := millis(rt.Woken, rt.Busy); ok {
		out["untilBusyMs"] = ms
	}
	return out
}

// millis is end − start in whole milliseconds; false when either is unknown.
func millis(start, end *time.Time) (int64, bool) {
	if start == nil || end == nil {
		return 0, false
	}
	return int64(math.Round(float64(end.Sub(*start)) / float64(time.Millisecond))), true
}
