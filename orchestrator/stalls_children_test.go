package orchestrator_test

// A phase Run whose open call started a child session (a subagent): the
// call is measured from its child's last event, and a report names the
// child and what it waits on.

import (
	"strings"
	"testing"
	"time"
)

// childFor gives a reviewer's open task call a child whose last event was
// ago, waiting on a permission when waiting.
func (w *world) childFor(runID string, ago time.Duration, waiting bool) {
	w.t.Helper()
	waitingOn := `null`
	if waiting {
		waitingOn = `{"permission": "external_directory", "patterns": ["/opt/venv/lib/python3.12/site-packages/*"]}`
	}
	mustExec(w.t, w.owner, `UPDATE runs SET children = (SELECT jsonb_object_agg(k, jsonb_build_object('childId', 'ses_ecc7cf31effeGCzeSYD4SeLE8p',
			'kind', 'task', 'title', 'Review worker state behavior', 'lastEvent', 'grep /opt/venv/lib/python3.12/site-packages',
			'lastEventAt', now() - make_interval(secs => $2)) || jsonb_strip_nulls(jsonb_build_object('waitingOn', $3::jsonb)))
		FROM jsonb_object_keys(open_tool_calls_at) k) WHERE id = $1`, runID, ago.Seconds(), waitingOn)
}

// A reviewer's task call open for 40 minutes whose child did something 5
// minutes ago is working, not stalled, at a 30-minute window; its child
// silent for 31 minutes, it is reported, and the report names the child,
// how long it has been silent, what it did last and the permission it
// waits on. The badge clears when the child moves again.
func TestAnOpenCallIsMeasuredFromItsChildsLastEvent(t *testing.T) {
	w := conducting(t)
	w.hangingReviews(taskCall)
	task := w.task()
	w.conductedReview(task)
	runs := w.reviewersOpen(task, 2)
	id := runs[0]
	mustExec(t, w.owner, `UPDATE runs SET open_tool_calls_at = '{}' WHERE id = $1`, runs[1])
	w.openSince(id, 40*time.Minute)
	w.childFor(id, 5*time.Minute, false)
	w.sweep()
	if n := w.stalls(id); n != 0 {
		t.Fatalf("%d reports for a call whose child moved 5 minutes ago", n)
	}
	w.childFor(id, 31*time.Minute, true)
	note := w.wokenWith(task, "made no progress")
	if w.stalls(id) != 1 {
		t.Fatalf("%d reports for a call whose child is silent for 31 minutes", w.stalls(id))
	}
	for _, want := range []string{
		"Its `task` call \"Review worker state behavior\" has a child (task · ses_ecc7cf31) whose last event was 31 min ago: " +
			"grep /opt/venv/lib/python3.12/site-packages",
		"— waiting on a permission nothing can answer: external_directory for /opt/venv/lib/python3.12/site-packages/*",
	} {
		if !strings.Contains(note, want) {
			t.Errorf("the note lacks %q:\n%s", want, note)
		}
	}
	stall, _ := w.stallOf(id)
	children, _ := stall["children"].([]any)
	if len(children) != 1 || children[0].(map[string]any)["childId"] != "ses_ecc7cf31effeGCzeSYD4SeLE8p" {
		t.Errorf("the report's children: %v", stall["children"])
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND run_stalled(runs)`, id); n != 1 {
		t.Errorf("the Run does not read as stalled")
	}
	// The child moves on: no longer stalled, and changed facts.
	w.childFor(id, 0, false)
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND run_stalled(runs)`, id); n != 0 {
		t.Errorf("the Run still reads as stalled after its child moved")
	}
}

// A child that moves on to something else is a change of facts: reported
// again after the window, not the hour.
func TestAChildsNewLastEventIsChangedFacts(t *testing.T) {
	w := conducting(t)
	w.hangingReviews(taskCall)
	task := w.task()
	w.conductedReview(task)
	runs := w.reviewersOpen(task, 2)
	id := runs[0]
	mustExec(t, w.owner, `UPDATE runs SET open_tool_calls_at = '{}' WHERE id = $1`, runs[1])
	w.openSince(id, 70*time.Minute)
	w.childFor(id, 65*time.Minute, false)
	w.sweep()
	if w.stalls(id) != 1 {
		t.Fatalf("%d reports, want 1", w.stalls(id))
	}
	w.reportedAgo(id, 31*time.Minute)
	w.sweep()
	if n := w.stalls(id); n != 1 {
		t.Fatalf("the same facts reported again at 30 minutes (%d reports)", n)
	}
	mustExec(t, w.owner, `UPDATE runs SET children = jsonb_set(children, ARRAY[(SELECT k FROM jsonb_object_keys(children) k LIMIT 1), 'lastEvent'],
		'"read /opt/venv/lib/python3.12/site-packages/foo.py"') WHERE id = $1`, id)
	w.sweep()
	if n := w.stalls(id); n != 2 {
		t.Errorf("a child's new last event is not changed facts (%d reports)", n)
	}
}
