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

// A blank harness session has no prompt but what it is sent: the briefing
// again carries everything the first prompt had around the briefing — how
// the agent works, its tools — and says the conversation is lost in dude's
// own words, not as a member's or person's message.
func TestATalkerBriefedAgainIsToldHowItWorks(t *testing.T) {
	for kind, start := range talkers(t) {
		t.Run(kind, func(t *testing.T) {
			tk, run := start(t)
			tk.syncer.Agent.ToolsURL = "http://10.9.8.7:3120/mcp"
			tk.lux.LoseSession(tk.luxRunOf(run))
			tk.stopOnItsOwn(run, "stopped")
			tk.until("the Run parked", func() bool {
				return tk.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused'`, run) == 1
			})
			tk.write("still with me?")
			tk.until("the briefing again queued", func() bool {
				return tk.count(`SELECT count(*) FROM directives WHERE run_id = $1 AND strpos(text, 'restarted without') > 0`, run) == 1
			})
			var text string
			if err := tk.owner.QueryRow(t0(), `SELECT text FROM directives WHERE run_id = $1 AND strpos(text, 'restarted without') > 0`,
				run).Scan(&text); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(text, "## How you work") {
				t.Errorf("no role instructions:\n%s", text)
			}
			lost := strings.Index(text, "restarted without its earlier conversation")
			switch kind {
			case "brainstorm":
				if !strings.Contains(text, "You think out loud with the members of this session") ||
					!strings.Contains(text, "The dude tools read the linked projects") || !strings.Contains(text, "name_session") {
					t.Errorf("not the brainstorm's instructions and tools:\n%s", text)
				}
				heading := strings.Index(text, "## Your earlier conversation is lost")
				if strings.Contains(text, "## The first message") || heading < 0 || lost < heading {
					t.Errorf("dude's note not under a heading of its own:\n%s", text)
				}
			case "conductor":
				if !strings.Contains(text, "The people on the task talk to you in its Chat") ||
					!strings.Contains(text, "The dude tools read what dude knows about this task") {
					t.Errorf("not the conductor's instructions and tools:\n%s", text)
				}
				if !strings.Contains(text, "dude woke you") || !strings.Contains(text, "## dude's message") ||
					strings.Contains(text, "wrote in the Chat") {
					t.Errorf("dude's note framed as a person's message:\n%s", text)
				}
			}
			if lost < 0 {
				t.Errorf("no word of the lost conversation:\n%s", text)
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
