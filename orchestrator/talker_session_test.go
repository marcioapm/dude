package orchestrator_test

import (
	"slices"
	"strings"
	"testing"
)

// A talker resumed into a new harness session — lux could not reload its
// own, warned so and reported another id — records agent.session.replaced
// with lux's reason and lux's warning, and is briefed again: its own
// briefing, saying the conversation is lost, before the message that
// resumed it, which it still answers.
func TestATalkerResumedWithoutItsSessionIsBriefedAgain(t *testing.T) {
	for kind, start := range talkers(t) {
		t.Run(kind, func(t *testing.T) {
			tk, run := start(t)
			first := tk.sessionIDs(run)
			tk.lux.LoseSession(tk.luxRunOf(run))
			tk.stopOnItsOwn(run, "stopped")
			tk.until("the Run parked", func() bool {
				return tk.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused'`, run) == 1
			})
			if status, reached := tk.write("still with me?"); status != 200 || reached != run {
				t.Fatalf("the message: %d reached %q", status, reached)
			}
			tk.resumedAnswering(run, "still with me?")
			if n := tk.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'agent.warning'
				AND payload->>'message' LIKE 'session/load failed%'`, run); n != 1 {
				t.Errorf("%d agent.warning carrying lux's text, want 1", n)
			}
			var from, to, reason string
			if err := tk.owner.QueryRow(t0(), `SELECT payload->>'from', payload->>'to', payload->>'reason' FROM events
				WHERE run_id = $1 AND event_type = 'agent.session.replaced'`, run).Scan(&from, &to, &reason); err != nil {
				t.Fatalf("no agent.session.replaced: %v", err)
			}
			if len(first) != 1 || from != first[0] || to == from || !strings.HasPrefix(reason, "session/load failed") {
				t.Errorf("replaced from %q to %q (%q); the first session was %v", from, to, reason, first)
			}
			var inputs []string
			for _, r := range tk.lux.Runs() {
				if strings.Contains(string(r.Spec), `"dude.run":"`+run+`"`) {
					inputs = append(slices.Clone(r.ResumeInputs), r.Inputs...)
				}
			}
			briefed := slices.IndexFunc(inputs, func(in string) bool {
				return strings.Contains(in, "restarted without its earlier conversation") && strings.Contains(in, "session/load failed")
			})
			if briefed < 0 {
				t.Fatalf("the agent was not briefed again: %q", inputs)
			}
			if kind == "brainstorm" && !strings.Contains(inputs[briefed], "## Linked projects") {
				t.Errorf("not the session's briefing: %q", inputs[briefed])
			}
			if kind == "conductor" && !strings.Contains(inputs[briefed], "## The task") {
				t.Errorf("not the task's briefing: %q", inputs[briefed])
			}
			if !slices.ContainsFunc(inputs, func(in string) bool { return strings.Contains(in, "still with me?") }) {
				t.Errorf("the message that resumed it never reached it: %q", inputs)
			}
		})
	}
}

// A resume that keeps its session records nothing more.
func TestATalkerResumedInItsSessionIsNotBriefedAgain(t *testing.T) {
	for kind, start := range talkers(t) {
		t.Run(kind, func(t *testing.T) {
			tk, run := start(t)
			tk.stopOnItsOwn(run, "stopped")
			tk.until("the Run parked", func() bool {
				return tk.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused'`, run) == 1
			})
			tk.write("still with me?")
			tk.resumedAnswering(run, "still with me?")
			if n := tk.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type IN ('agent.session.replaced', 'agent.warning')`, run); n != 0 {
				t.Errorf("%d replaced/warning events for a resume in the same session", n)
			}
			if n := tk.count(`SELECT count(*) FROM directives WHERE run_id = $1 AND strpos(text, 'restarted without') > 0`, run); n != 0 {
				t.Errorf("briefed again in its own session")
			}
		})
	}
}
