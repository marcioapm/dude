package phases

import (
	"testing"
)

func rec(typ string, data map[string]any) map[string]any {
	return map[string]any{"type": typ, "data": data}
}

// A phase Run resumed into a new harness session records what lux warned
// and the session it was replaced by, and is told nothing: only a talker
// is briefed again. A resume into the same session records nothing.
func TestAPhaseRunsReplacedSessionIsRecordedOnly(t *testing.T) {
	w := newHarnessWorld(t)
	w.feedAt(1, rec("lux.session", map[string]any{"sessionId": "ses_a"}))
	w.feedAt(2, rec("lux.session", map[string]any{"sessionId": "ses_a"}))
	if got := append(ofType(w.events(), evSessionReplaced), ofType(w.events(), evAgentWarning)...); len(got) != 0 {
		t.Fatalf("a resume in the same session recorded %v", got)
	}
	w.feedAt(3, rec("lux.warning", map[string]any{"message": "thread/resume failed, starting a new thread: no rollout"}),
		rec("lux.session", map[string]any{"sessionId": "ses_b"}))
	events := w.events()
	warnings := ofType(events, evAgentWarning)
	if len(warnings) != 1 || warnings[0].Payload["message"] != "thread/resume failed, starting a new thread: no rollout" {
		t.Errorf("warnings %v", warnings)
	}
	replaced := ofType(events, evSessionReplaced)
	if len(replaced) != 1 {
		t.Fatalf("replaced %v", replaced)
	}
	p := replaced[0].Payload
	if p["from"] != "ses_a" || p["to"] != "ses_b" || p["reason"] != "thread/resume failed, starting a new thread: no rollout" || p["directiveId"] != nil {
		t.Errorf("replaced payload %v", p)
	}
	var n int
	if err := w.owner.QueryRow(t.Context(), `SELECT count(*) FROM directives WHERE run_id = $1`, w.run.ID).Scan(&n); err != nil || n != 0 {
		t.Errorf("%d directives for a phase Run (%v)", n, err)
	}
	// Replaced again, after a restart of the follower, from the id it was
	// replaced by; a warning with no new session is no reason for the next.
	w.restart()
	w.feedAt(4, rec("lux.session", map[string]any{"sessionId": "ses_c"}))
	again := ofType(w.events(), evSessionReplaced)
	if len(again) != 1 || again[0].Payload["from"] != "ses_b" || again[0].Payload["to"] != "ses_c" || again[0].Payload["reason"] != "" {
		t.Errorf("second replacement %v", again)
	}
}
