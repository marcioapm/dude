package phases

import (
	"testing"
	"time"
)

// wokenWorld is a resumeWorld whose Run has no ask yet, parked at base,
// and a repository it can ask for.
func wokenWorld(t *testing.T, base time.Time) *resumeWorld {
	w := newResumeWorld(t)
	w.exec(`DELETE FROM questions WHERE run_id = $1`, w.run.ID)
	w.exec(`INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch)
		VALUES ('repo_'||$1, $1, $2, 'web', 'git://h/web.git', 'main')`, w.run.Org, w.run.ProjectID)
	w.exec(`INSERT INTO events (id, organization_id, event_type, occurred_at, run_id, actor_type, actor_id, source, payload)
		VALUES ('ev_park_'||$1, $1, 'run.parked', $2, $3, 'system', $3, 'runner', '{"reason":"person"}')`,
		w.run.Org, base, w.run.ID)
	return w
}

func (w *resumeWorld) exec(sql string, args ...any) {
	w.t.Helper()
	if _, err := w.owner.Exec(w.ctx, sql, args...); err != nil {
		w.t.Fatal(err)
	}
}

// question is an answered question on w's Run, answered at.
func (w *resumeWorld) question(id string, at time.Time) {
	w.t.Helper()
	w.exec(`INSERT INTO questions (id, organization_id, task_id, run_id, prompt, status, answer, answered_at)
		VALUES ($1, $2, $3, $4, 'Sorted?', 'answered', 'yes', $5)`, id, w.run.Org, w.run.TaskID, w.run.ID, at)
}

// request is a repository request on w's Run, decided at with status.
func (w *resumeWorld) request(id string, blocking bool, status string, at time.Time) {
	w.t.Helper()
	w.exec(`INSERT INTO repository_requests (id, organization_id, task_id, run_id, repository_id, reason, status, blocking,
		decided_at) VALUES ($1, $2, $3, $4, 'repo_'||$2, 'needs it', $5, $6, $7)`,
		id, w.run.Org, w.run.TaskID, w.run.ID, status, blocking, at)
}

func (w *resumeWorld) wokenAt(r phaseRun) (string, time.Time) {
	w.t.Helper()
	w.s.resumeAsked(w.ctx, r, stoppedOnHost1(time.Now()))
	row := w.row(2)
	woken, _ := row["woken_at"].(time.Time)
	return row["cause"].(string), woken
}

func TestAnAnswerWakesFromTheLastAskThatHeldThePark(t *testing.T) {
	base := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	sec := func(n int) time.Time { return base.Add(time.Duration(n) * time.Second) }

	t.Run("the later of two answers", func(t *testing.T) {
		w := wokenWorld(t, base)
		w.question("q_a_"+w.run.Org, sec(1))
		w.question("q_b_"+w.run.Org, sec(3))
		if cause, got := w.wokenAt(w.run); cause != causeAnswer || !got.Equal(sec(3)) {
			t.Errorf("%s at %v, want answer at %v", cause, got, sec(3))
		}
	})

	t.Run("a later decision on a request that held nothing is not it", func(t *testing.T) {
		w := wokenWorld(t, base)
		w.question("q_a_"+w.run.Org, sec(2))
		w.request("rr_"+w.run.Org, false, "denied", sec(5))
		if _, got := w.wokenAt(w.run); !got.Equal(sec(2)) {
			t.Errorf("woken at %v, want the answer at %v", got, sec(2))
		}
	})

	t.Run("a blocking request decided last is it", func(t *testing.T) {
		w := wokenWorld(t, base)
		w.question("q_a_"+w.run.Org, sec(2))
		w.request("rr_"+w.run.Org, true, "denied", sec(4))
		if _, got := w.wokenAt(w.run); !got.Equal(sec(4)) {
			t.Errorf("woken at %v, want the decision at %v", got, sec(4))
		}
	})

	t.Run("an earlier park's answer is not this park's", func(t *testing.T) {
		w := wokenWorld(t, base)
		w.exec(`INSERT INTO events (id, organization_id, event_type, occurred_at, run_id, actor_type, actor_id, source)
			VALUES ('ev_park0_'||$1, $1, 'run.parked', $2, $3, 'system', $3, 'runner')`, w.run.Org, sec(-60), w.run.ID)
		w.question("q_old_"+w.run.Org, sec(-50))
		// This park's question was withdrawn, not answered.
		w.exec(`INSERT INTO questions (id, organization_id, task_id, run_id, prompt, status)
			VALUES ('q_now_'||$1, $1, $2, $3, 'Still?', 'cancelled')`, w.run.Org, w.run.TaskID, w.run.ID)
		if _, got := w.wokenAt(w.run); !got.IsZero() {
			t.Errorf("woken at %v, from an answer to an earlier park; want unknown", got)
		}
	})
}

func TestARepositoryResumeWakesFromItsApprovalNotALaterRejection(t *testing.T) {
	base := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	w := wokenWorld(t, base)
	w.request("rr_ok_"+w.run.Org, true, "approved", base.Add(time.Second))
	w.exec(`INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch)
		VALUES ('repo2_'||$1, $1, $2, 'api', 'git://h/api.git', 'main')`, w.run.Org, w.run.ProjectID)
	w.exec(`INSERT INTO repository_requests (id, organization_id, task_id, run_id, repository_id, reason, status, blocking,
		decided_at) VALUES ('rr_no_'||$1, $1, $2, $3, 'repo2_'||$1, 'needs it', 'denied', true, $4)`,
		w.run.Org, w.run.TaskID, w.run.ID, base.Add(2*time.Second))
	r := w.run
	r.DudePause = "repository"
	if cause, got := w.wokenAt(r); cause != causeRepository || !got.Equal(base.Add(time.Second)) {
		t.Errorf("%s at %v, want repository at %v", cause, got, base.Add(time.Second))
	}
}

func TestAPersonsResumeOfAParkWakesFromTheirResume(t *testing.T) {
	base := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	w := wokenWorld(t, base)
	w.question("q_a_"+w.run.Org, base.Add(3*time.Second))
	w.exec(`UPDATE runs SET control = 'resume', control_requested_at = $2 WHERE id = $1`, w.run.ID, base.Add(7*time.Second))
	r := w.run
	r.Control = "resume"
	if cause, got := w.wokenAt(r); cause != causePerson || !got.Equal(base.Add(7*time.Second)) {
		t.Errorf("%s at %v, want person at %v", cause, got, base.Add(7*time.Second))
	}
}

// The orchestrator's clock is 100ms ahead of the database's. dude parks
// the Run while it waits on a question, and a person answers it 30ms
// later: the answer is after the park began, though its database stamp
// is before the park event's own time. It wakes the resume.
func TestAnAnswerJustAfterTheParkWakesItWhateverTheOrchestratorsClock(t *testing.T) {
	w := newResumeWorld(t)
	w.exec(`DELETE FROM questions WHERE run_id = $1`, w.run.ID)
	w.exec(`INSERT INTO questions (id, organization_id, task_id, run_id, prompt, status)
		VALUES ('q_'||$1, $1, $2, $3, 'Sorted?', 'open')`, w.run.Org, w.run.TaskID, w.run.ID)
	w.exec(`UPDATE runs SET status = 'running', lux_state = 'running', dude_pause = NULL, lux_stop_reason = NULL,
		waiting_since = now() - interval '1 hour' WHERE id = $1`, w.run.ID)
	w.s.Now = func() time.Time {
		var db time.Time
		if err := w.owner.QueryRow(w.ctx, `SELECT clock_timestamp()`).Scan(&db); err != nil {
			w.t.Fatal(err)
		}
		return db.Add(100 * time.Millisecond)
	}
	r := w.run
	r.Status, r.LuxState, r.DudePause = statusRunning, "running", ""
	if err := w.s.requestPause(w.ctx, r, "person", "parked while it waits for a person"); err != nil {
		t.Fatal(err)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.parked'`); n != 1 {
		t.Fatalf("%d run.parked", n)
	}
	time.Sleep(30 * time.Millisecond)
	var answered time.Time
	if err := w.owner.QueryRow(w.ctx, `UPDATE questions SET status = 'answered', answer = 'yes', answered_at = now()
		WHERE run_id = $1 RETURNING answered_at`, w.run.ID).Scan(&answered); err != nil {
		t.Fatal(err)
	}
	w.exec(`UPDATE runs SET status = 'paused', dude_pause = 'person', control = 'none' WHERE id = $1`, w.run.ID)
	if cause, got := w.wokenAt(w.run); cause != causeAnswer || !got.Equal(answered) {
		t.Errorf("%s at %v, want the answer at %v", cause, got, answered)
	}
}

// An older park, recorded before run.parked carried parkedAt, is bounded
// by the event's own time.
func TestAParkWithoutParkedAtIsBoundedByItsEventsTime(t *testing.T) {
	base := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	w := wokenWorld(t, base)
	w.question("q_before_"+w.run.Org, base.Add(-time.Second))
	if _, got := w.wokenAt(w.run); !got.IsZero() {
		t.Errorf("woken at %v by an answer before the park; want unknown", got)
	}
}
