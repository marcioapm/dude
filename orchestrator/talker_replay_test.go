package orchestrator_test

import (
	"context"
	"slices"
	"strings"
	"testing"
)

// prompt is the briefing a Run was started with (runs.prompt), from which
// its first prompt is written.
func (tk *talking) prompt(run string) string {
	tk.t.Helper()
	var p string
	if err := tk.owner.QueryRow(context.Background(), `SELECT prompt FROM runs WHERE id = $1`, run).Scan(&p); err != nil {
		tk.t.Fatal(err)
	}
	return p
}

// replaced has lux end the talker's Run for good, and the next message
// start a new Run, which answers it. Returns the new Run.
func (tk *talking) replaced(run, text string) string {
	tk.t.Helper()
	tk.stopOnItsOwn(run, "terminated")
	tk.until("the Run ended", func() bool {
		return tk.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'completed'`, run) == 1
	})
	status, next := tk.write(text)
	if status != 201 || next == run {
		tk.t.Fatalf("%q after the end: %d reached %q, want a new Run", text, status, next)
	}
	tk.until("the new Run's answer", func() bool {
		said := tk.said(next)
		return len(said) == 1 && strings.Contains(said[0], text)
	})
	return next
}

// A talker's new Run — lux ended the last for good — is briefed with the
// conversation so far, across every Run before it, before the message
// that started it; the first Run is briefed with none.
func TestANewTalkerRunIsBriefedWithTheConversationSoFar(t *testing.T) {
	for kind, start := range talkers(t) {
		t.Run(kind, func(t *testing.T) {
			tk, first := start(t)
			firstMessage := map[string]string{"brainstorm": "where would metering live?", "conductor": "what changed?"}[kind]
			messageHeading := map[string]string{"brainstorm": "## The first message", "conductor": "'s message"}[kind]
			if strings.Contains(tk.prompt(first), "## The conversation so far") {
				t.Errorf("the first Run was briefed with a conversation:\n%s", tk.prompt(first))
			}
			firstAnswer := tk.said(first)[0]

			second := tk.replaced(first, "and the tests?")
			p := tk.prompt(second)
			replay := strings.Index(p, "## The conversation so far")
			message := strings.LastIndex(p, messageHeading)
			if replay < 0 || message < replay {
				t.Fatalf("the conversation is not before the message:\n%s", p)
			}
			for _, want := range []string{": " + firstMessage, "You: " + firstAnswer} {
				if !strings.Contains(p[replay:message], want) {
					t.Errorf("the replay lacks %q:\n%s", want, p[replay:message])
				}
			}
			if n := strings.Count(p, "and the tests?"); n != 1 {
				t.Errorf("the message that started it is in its briefing %d times, want once:\n%s", n, p)
			}

			// A third Run is briefed with both before it.
			third := tk.replaced(second, "and the docs?")
			p = tk.prompt(third)
			replay, message = strings.Index(p, "## The conversation so far"), strings.LastIndex(p, messageHeading)
			if replay < 0 || message < replay {
				t.Fatalf("the third Run's conversation is not before its message:\n%s", p)
			}
			r := p[replay:message]
			i, j := strings.Index(r, firstMessage), strings.Index(r, "and the tests?")
			if i < 0 || j < i || !strings.Contains(r, "[A new agent took over here.]") || !strings.Contains(r, "You: "+tk.said(second)[0]) {
				t.Errorf("the third Run's replay is not both Runs' in order:\n%s", r)
			}
			// What it was briefed with is what dude recorded briefing it with.
			ev := map[string]string{"brainstorm": "session.briefed", "conductor": "conductor.briefed"}[kind]
			if n := tk.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = $2 AND payload->>'text' = $3`, third, ev, p); n != 1 {
				t.Errorf("%d %s with the briefing, want 1", n, ev)
			}
		})
	}
}

// A message a conductor never read when lux ended it is handed to the next
// as that conductor's briefing message: once, not again in the replay.
func TestAHandedOverMessageIsBriefedOnce(t *testing.T) {
	tk, first := talkers(t)["conductor"](t)
	tk.lux.InputGate = make(chan struct{})
	if status, reached := tk.write("did the build pass?"); status != 200 || reached != first {
		t.Fatalf("the message: %d reached %q", status, reached)
	}
	tk.until("the message sent to lux", func() bool {
		return tk.count(`SELECT count(*) FROM directives WHERE run_id = $1 AND sent_at IS NOT NULL AND delivered_at IS NULL`, first) == 1
	})
	tk.stopOnItsOwn(first, "terminated")
	tk.releaseInput()
	var next string
	tk.until("the next conductor to answer the message", func() bool {
		next = tk.latest()
		said := tk.said(next)
		return next != first && len(said) == 1 && strings.Contains(said[0], "did the build pass?")
	})
	p := tk.prompt(next)
	if n := strings.Count(p, "did the build pass?"); n != 1 {
		t.Errorf("the handed-over message is in the briefing %d times, want once:\n%s", n, p)
	}
	if !strings.Contains(p, "## The conversation so far") || !strings.HasSuffix(p, "did the build pass?") {
		t.Errorf("not briefed with the conversation, then the message:\n%s", p)
	}
}

// A talker resumed into a blank harness session is briefed again with the
// conversation so far: what it heard and said before the loss.
func TestATalkerBriefedAgainIsGivenTheConversationSoFar(t *testing.T) {
	for kind, start := range talkers(t) {
		t.Run(kind, func(t *testing.T) {
			tk, run := start(t)
			answer := tk.said(run)[0]
			tk.lux.LoseSession(tk.luxRunOf(run))
			tk.stopOnItsOwn(run, "stopped")
			tk.parked(run)
			tk.write("still with me?")
			tk.resumedAnswering(run, "still with me?")
			var inputs []string
			for _, r := range tk.lux.Runs() {
				if strings.Contains(string(r.Spec), `"dude.run":"`+run+`"`) {
					inputs = append(slices.Clone(r.ResumeInputs), r.Inputs...)
				}
			}
			i := slices.IndexFunc(inputs, func(in string) bool { return strings.Contains(in, "restarted without its earlier conversation") })
			if i < 0 {
				t.Fatalf("not briefed again: %q", inputs)
			}
			in := inputs[i]
			replay := strings.Index(in, "## The conversation so far")
			if replay < 0 || !strings.Contains(in[replay:], "You: "+answer) {
				t.Errorf("the briefing again lacks the conversation:\n%s", in)
			}
			if instructions := strings.Index(in, "## How you work"); instructions < replay {
				t.Errorf("the conversation is not in the briefing, before how it works:\n%s", in)
			}
		})
	}
}
