package phases

import (
	"testing"
	"time"
)

// A resume whose first output is in and that was never timed — its
// follow-up lost to a restart — is timed when its Run ends, however it
// ends.
func TestAnEndingRunTimesTheResumeItsFollowerNeverDid(t *testing.T) {
	for _, c := range []struct {
		name string
		end  func(w *resumeWorld) error
	}{
		{"completed", func(w *resumeWorld) error {
			w.exec(`UPDATE runs SET status = 'running', lux_state = 'stopped' WHERE id = $1`, w.run.ID)
			r := w.run
			r.Status, r.LuxState = statusRunning, "stopped"
			_, err := w.s.finish(w.ctx, r)
			return err
		}},
		{"failed", func(w *resumeWorld) error {
			w.exec(`UPDATE runs SET status = 'failed', lux_state = 'stopped' WHERE id = $1`, w.run.ID)
			r := w.run
			r.Status, r.LuxState = "failed", "stopped"
			_, err := w.s.advance(w.ctx, r)
			return err
		}},
		{"aborted", func(w *resumeWorld) error {
			w.exec(`UPDATE runs SET status = 'aborted', control = 'abort' WHERE id = $1`, w.run.ID)
			r := w.run
			r.Status, r.Control = statusAborted, "abort"
			_, err := w.s.advance(w.ctx, r)
			return err
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newResumeWorld(t)
			w.s.Lux = &refusingLux{placementLux: w.lux}
			base := time.Now().Add(-time.Minute)
			w.exec(`INSERT INTO run_resumes (run_id, organization_id, epoch, cause, woken_at, requested_at, first_output_at)
				VALUES ($1, $2, 2, 'person', $3, $4, $5)`, w.run.ID, w.run.Org, base, base.Add(time.Second), base.Add(3*time.Second))
			if err := c.end(w); err != nil {
				t.Fatal(err)
			}
			w.untilTimed()
			got := w.timed()
			if len(got) != 1 || got[0]["epoch"] != 2.0 || got[0]["totalMs"] != 3000.0 {
				t.Errorf("run.resume.timed after the Run %s: %v, want one for epoch 2 of 3000ms", c.name, got)
			}
		})
	}
}

// A Run ended and its process stopped before its timing was published:
// the row has its first output and no timed_at, and nothing of the Run is
// left to do. The startup pass publishes it once; a second pass, nothing.
// A resume older than the pass looks back is left alone.
func TestTheStartupPassTimesWhatAnEndedRunNeverPublished(t *testing.T) {
	w := newResumeWorld(t)
	w.exec(`UPDATE runs SET status = 'completed', lux_state = 'stopped', lux_stop_reason = 'complete', ended_at = now()
		WHERE id = $1`, w.run.ID)
	base := time.Now().Add(-time.Minute)
	w.exec(`INSERT INTO run_resumes (run_id, organization_id, epoch, cause, woken_at, requested_at, first_output_at)
		VALUES ($1, $2, 2, 'person', $3, $4, $5)`, w.run.ID, w.run.Org, base, base.Add(time.Second), base.Add(3*time.Second))
	old := base.Add(-8 * 24 * time.Hour)
	w.exec(`INSERT INTO run_resumes (run_id, organization_id, epoch, cause, woken_at, first_output_at, created_at)
		VALUES ($1, $2, 3, 'person', $3, $4, $3)`, w.run.ID, w.run.Org, old, old.Add(time.Second))

	w.s.TimeUntimedResumes(w.ctx)
	got := w.timed()
	if len(got) != 1 || got[0]["epoch"] != 2.0 || got[0]["totalMs"] != 3000.0 {
		t.Fatalf("after the startup pass: %v, want one run.resume.timed for epoch 2 of 3000ms", got)
	}
	w.s.TimeUntimedResumes(w.ctx)
	if n := len(w.timed()); n != 1 {
		t.Errorf("%d run.resume.timed after a second pass, want 1", n)
	}
}
