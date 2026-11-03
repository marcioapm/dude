package phases

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// People's feedback calls the conductor once per burst (step 3b, PR C2):
// the clock is moved by moving the reasons' created_at, never by sleeping.

// debounceTask is a task whose conductor is parked between turns (it hears
// a note at once), its delivery conducted in voice.
func (w *wakeSweepWorld) debounceTask(task, voice string) {
	w.t.Helper()
	w.exec(`INSERT INTO tasks (id, organization_id, project_id, number, title, goal)
		VALUES ($2, $1, 'prj_'||$1, (SELECT count(*) + 1 FROM tasks WHERE organization_id = $1), 'T', 'G')`, w.org, task)
	w.exec(`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, role, kind, status, dude_pause, turn_done_at)
		VALUES ('run_c_'||$2, $1, 'prj_'||$1, $2, 1, 'conductor', 'agent', 'paused', 'conductor', now())`, w.org, task)
	w.exec(`INSERT INTO workflow_runs (id, organization_id, task_id, workflow_type, idempotency_key, status, step, state)
		VALUES ('wf_'||$2, $1, $2, 'task.delivery', 'delivery:'||$2, 'waiting', 'awaitFix',
			jsonb_build_object('attempt', 1, 'decider', 'conductor', 'hadConductor', true, 'voice', $3::text))`, w.org, task, voice)
}

// feedbackAt records a feedback reason (as routeFeedbackTx does) that
// arrived ago.
func (w *wakeSweepWorld) feedbackAt(task, id string, ago time.Duration) {
	w.t.Helper()
	w.exec(`INSERT INTO conductor_wakes (id, organization_id, task_id, kind, key, line, created_at)
		VALUES ($3, $1, $2, $4, 'feedback:'||$3, '1 feedback items from a person ('||$3||'): read them', now() - make_interval(secs => $5))`,
		w.org, task, id, delivery.WakeFeedback, ago.Seconds())
}

// age moves every pending reason of the task by d into the past.
func (w *wakeSweepWorld) age(task string, d time.Duration) {
	w.t.Helper()
	w.exec(`UPDATE conductor_wakes SET created_at = created_at - make_interval(secs => $2) WHERE task_id = $1 AND delivered_at IS NULL`,
		task, d.Seconds())
}

func (w *wakeSweepWorld) notes(task string) []string {
	w.t.Helper()
	rows, err := w.owner.Query(context.Background(), `SELECT text FROM directives WHERE run_id = 'run_c_'||$1 ORDER BY created_at, id`, task)
	if err != nil {
		w.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			w.t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func (w *wakeSweepWorld) sweep() {
	w.t.Helper()
	if err := w.s.wakeConductors(context.Background()); err != nil {
		w.t.Fatal(err)
	}
}

// A burst: three submits a minute apart wake the conductor once, with all
// three, ten minutes after the last — quiet and chatty alike.
func TestABurstOfFeedbackWakesTheConductorOnceTenMinutesAfterTheLast(t *testing.T) {
	for _, voice := range []string{delivery.VoiceQuiet, delivery.VoiceChatty} {
		t.Run(voice, func(t *testing.T) {
			w := newWakeSweepWorld(t)
			w.s.WakeWindow = 15 * time.Second
			w.debounceTask("wi_burst", voice)
			w.feedbackAt("wi_burst", "fbi_1", 2*time.Minute)
			w.sweep()
			w.feedbackAt("wi_burst", "fbi_2", time.Minute)
			w.sweep()
			w.feedbackAt("wi_burst", "fbi_3", 0)
			w.sweep()
			if n := len(w.notes("wi_burst")); n != 0 {
				t.Fatalf("%d notes during the burst", n)
			}
			// 9 minutes after the last: still quiet for less than 10.
			w.age("wi_burst", 9*time.Minute)
			w.sweep()
			if n := len(w.notes("wi_burst")); n != 0 {
				t.Fatalf("%d notes 9 minutes after the last", n)
			}
			w.age("wi_burst", time.Minute+time.Second)
			w.sweep()
			notes := w.notes("wi_burst")
			if len(notes) != 1 {
				t.Fatalf("%d notes 10 minutes after the last, want 1", len(notes))
			}
			for _, id := range []string{"fbi_1", "fbi_2", "fbi_3"} {
				if !strings.Contains(notes[0], id) {
					t.Errorf("the note does not carry %s:\n%s", id, notes[0])
				}
			}
			// One block: one header, three lines.
			if strings.Count(notes[0], "dude woke you") != 1 || !strings.Contains(notes[0], "3 things:") {
				t.Errorf("the note is not one block of three:\n%s", notes[0])
			}
			w.age("wi_burst", time.Hour)
			w.sweep()
			if n := len(w.notes("wi_burst")); n != 1 {
				t.Errorf("%d notes after another sweep, want 1", n)
			}
		})
	}
}

// A trickle: one submit every 5 minutes is never quiet for 10, and is told
// once its oldest has waited 30 minutes.
func TestATrickleOfFeedbackIsToldAtTheThirtyMinuteCap(t *testing.T) {
	w := newWakeSweepWorld(t)
	w.s.WakeWindow = 15 * time.Second
	w.debounceTask("wi_trickle", delivery.VoiceQuiet)
	for i := range 6 {
		if i > 0 {
			w.age("wi_trickle", 5*time.Minute)
		}
		w.feedbackAt("wi_trickle", "fbi_t"+strconv.Itoa(i), 0)
		w.sweep()
		if n := len(w.notes("wi_trickle")); n != 0 {
			t.Fatalf("%d notes after %d minutes of trickle", n, 5*i)
		}
	}
	// The oldest is 25 minutes old, the newest just in: another 5 minutes
	// and one more submit, and the oldest has waited past 30.
	w.age("wi_trickle", 5*time.Minute+time.Second)
	w.feedbackAt("wi_trickle", "fbi_t6", 0)
	w.sweep()
	notes := w.notes("wi_trickle")
	if len(notes) != 1 {
		t.Fatalf("%d notes at the cap, want 1", len(notes))
	}
	for i := range 7 {
		if !strings.Contains(notes[0], "fbi_t"+strconv.Itoa(i)) {
			t.Errorf("the note at the cap does not carry fbi_t%d", i)
		}
	}
}

// Riding along: a decision arriving while feedback debounces is told on
// the window as ever, and carries the feedback; the feedback is not told
// again.
func TestADecisionWakeCarriesTheDebouncingFeedback(t *testing.T) {
	w := newWakeSweepWorld(t)
	w.s.WakeWindow = 15 * time.Second
	w.debounceTask("wi_ride", delivery.VoiceQuiet)
	w.feedbackAt("wi_ride", "fbi_r1", 2*time.Minute)
	w.feedbackAt("wi_ride", "fbi_r2", 10*time.Second)
	w.exec(`INSERT INTO conductor_wakes (id, organization_id, task_id, kind, key, line, created_at)
		VALUES ('cwk_dec', $1, 'wi_ride', 'decision', 'decision:x', 'Decision waiting: after review', now() - interval '20 seconds')`, w.org)
	w.sweep()
	notes := w.notes("wi_ride")
	if len(notes) != 1 {
		t.Fatalf("%d notes once the decision settled, want 1", len(notes))
	}
	for _, s := range []string{"Decision waiting", "fbi_r1", "fbi_r2"} {
		if !strings.Contains(notes[0], s) {
			t.Errorf("the decision's note does not carry %q:\n%s", s, notes[0])
		}
	}
	if n := w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = 'wi_ride' AND delivered_at IS NULL`); n != 0 {
		t.Errorf("%d reasons still pending", n)
	}
	w.age("wi_ride", time.Hour)
	w.sweep()
	if n := len(w.notes("wi_ride")); n != 1 {
		t.Errorf("%d notes after the debounce passed, want 1: the feedback was told again", n)
	}
}

// Feedback does not hold back other reasons: fresh feedback, inside the
// window, does not delay a settled decision.
func TestFreshFeedbackDoesNotDelayADecision(t *testing.T) {
	w := newWakeSweepWorld(t)
	w.s.WakeWindow = 15 * time.Second
	w.debounceTask("wi_fresh", delivery.VoiceQuiet)
	w.exec(`INSERT INTO conductor_wakes (id, organization_id, task_id, kind, key, line, created_at)
		VALUES ('cwk_dec', $1, 'wi_fresh', 'decision', 'decision:x', 'Decision waiting: after review', now() - interval '1 minute')`, w.org)
	w.feedbackAt("wi_fresh", "fbi_f1", 0)
	w.sweep()
	if notes := w.notes("wi_fresh"); len(notes) != 1 || !strings.Contains(notes[0], "fbi_f1") {
		t.Fatalf("fresh feedback held back a settled decision, or did not ride on it: %q", notes)
	}
}

// No starvation: 200 tasks whose feedback debounces, with older reasons,
// are not selected (Wakeable excludes them in SQL), so a ready task beyond
// the batch is told in one sweep.
func TestDebouncingFeedbackDoesNotCrowdOutTheSweep(t *testing.T) {
	w := newWakeSweepWorld(t)
	org := w.org
	w.s.WakeWindow = 15 * time.Second
	w.exec(`INSERT INTO tasks (id, organization_id, project_id, number, title, goal)
		SELECT 'wi_'||n, $1, 'prj_'||$1, n, 'T', 'G' FROM generate_series(1, `+strconv.Itoa(wakeBatch)+`) n`, org)
	w.exec(`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, role, kind, status, dude_pause, turn_done_at)
		SELECT 'run_f'||n, $1, 'prj_'||$1, 'wi_'||n, 1, 'conductor', 'agent', 'paused', 'conductor', now()
		FROM generate_series(1, `+strconv.Itoa(wakeBatch)+`) n`, org)
	w.exec(`INSERT INTO conductor_wakes (id, organization_id, task_id, kind, key, line, created_at)
		SELECT 'cwk_f'||n, $1, 'wi_'||n, $2, 'feedback:'||n, 'feedback', now() - interval '5 minutes'
		FROM generate_series(1, `+strconv.Itoa(wakeBatch)+`) n`, org, delivery.WakeFeedback)
	w.debounceTask("wi_zready", delivery.VoiceQuiet)
	w.exec(`INSERT INTO conductor_wakes (id, organization_id, task_id, kind, key, line, created_at)
		VALUES ('cwk_ready', $1, 'wi_zready', 'decision', 'k', 'ready to decide', now() - interval '1 minute')`, org)
	if n := w.count(`SELECT count(DISTINCT c.task_id) FROM conductor_wakes c WHERE `+delivery.Wakeable, 15.0); n != 1 {
		t.Errorf("Wakeable selects %d tasks, want only the ready one", n)
	}
	w.sweep()
	if n := w.count(`SELECT count(*) FROM conductor_wakes WHERE id = 'cwk_ready' AND delivered_at IS NOT NULL`); n != 1 {
		t.Fatal("the ready reason was crowded out by debouncing feedback")
	}
	if n := w.count(`SELECT count(*) FROM conductor_wakes WHERE kind = $1 AND delivered_at IS NOT NULL`, delivery.WakeFeedback); n != 0 {
		t.Errorf("%d debouncing feedback reasons told", n)
	}
}
