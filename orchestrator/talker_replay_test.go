package orchestrator_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
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

// A message a talker never read when lux ended it is handed to the next
// Run as its briefing message: once, not again in the replay; and a later
// Run's replay has it once, before the answer to it.
func TestAHandedOverMessageIsBriefedOnce(t *testing.T) {
	for kind, start := range talkers(t) {
		t.Run(kind, func(t *testing.T) {
			tk, first := start(t)
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
			tk.until("the next Run to answer the message", func() bool {
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

			answer := tk.said(next)[0]
			third := tk.replaced(next, "and the docs?")
			tk.replayedOnceBeforeItsAnswer(third, "did the build pass?", answer)
		})
	}
}

// replayedOnceBeforeItsAnswer checks that run's replay has a person saying
// text once, before the agent's answer to it. The answer quotes the
// message: only a person's lines count.
func (tk *talking) replayedOnceBeforeItsAnswer(run, text, answer string) {
	tk.t.Helper()
	p := tk.prompt(run)
	replay := strings.Index(p, "## The conversation so far")
	if replay < 0 {
		tk.t.Fatalf("Run %s has no replay:\n%s", run, p)
	}
	lines := strings.Split(p[replay:], "\n\n")
	message, reply := -1, slices.Index(lines, "You: "+answer)
	for i, l := range lines {
		if strings.HasSuffix(l, ": "+text) && !strings.HasPrefix(l, "You") {
			if message >= 0 {
				tk.t.Errorf("%q is in the replay twice:\n%s", text, p[replay:])
			}
			message = i
		}
	}
	if message < 0 || reply < message {
		tk.t.Errorf("%q is not once before its answer in the replay:\n%s", text, p[replay:])
	}
}

// hangingFirst is a brainstorm or conductor whose first Run took its first
// message and is still working on it; the Runs after it answer at once.
func hangingFirst(t *testing.T, kind string) (*talking, string) {
	hung := false
	tk := newTalking(t, kind, func(scripted func(map[string]any) fakelux.Behaviour) func(map[string]any) fakelux.Behaviour {
		return func(spec map[string]any) fakelux.Behaviour {
			b := scripted(spec)
			if labels, _ := spec["labels"].(map[string]any); labels["dude.role"] == kind && !hung {
				b.Hang, hung = true, true
			}
			return b
		}
	})
	var run string
	tk.until("the agent at work on its first message", func() bool {
		run = tk.latest()
		return run != "" && tk.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running' AND lux_state = 'running'
			AND agent_session_epoch > 0 AND agent_busy_at IS NOT NULL AND turn_done_at IS NULL`, run) == 1
	})
	return tk, run
}

// acceptedUnread writes text to the talker mid-turn and waits for lux to
// accept it: taken by the harness, not read. Returns its directive.
func (tk *talking) acceptedUnread(run, text string) string {
	tk.t.Helper()
	if status, reached := tk.write(text); status != 200 || reached != run {
		tk.t.Fatalf("%q: %d reached %q, want %s", text, status, reached, run)
	}
	var id string
	tk.until("lux to accept the message", func() bool {
		_ = tk.owner.QueryRow(context.Background(), `SELECT id FROM directives WHERE run_id = $1 AND accepted_at IS NOT NULL
			AND delivered_at IS NULL AND failed_at IS NULL`, run).Scan(&id)
		return id != ""
	})
	return id
}

// A message lux accepted mid-turn, then failed as unread when it
// terminated the talker (lux.input.failed, as its adapters report every
// accepted, unread input of a Run that stops), is handed to the next Run:
// briefed once, then replayed once before its answer. Its one failure on
// the ended Run names the Run that has it, not lux's cancellation.
func TestAMessageLuxCancelledAtATerminateIsHandedOnAndBriefedOnce(t *testing.T) {
	for _, kind := range []string{"brainstorm", "conductor"} {
		t.Run(kind, func(t *testing.T) {
			tk, first := hangingFirst(t, kind)
			directive := tk.acceptedUnread(first, "did the build pass?")
			tk.stopOnItsOwn(first, "terminated")
			var next string
			tk.until("the next Run to answer the message", func() bool {
				next = tk.latest()
				said := tk.said(next)
				return next != first && len(said) == 1 && strings.Contains(said[0], "did the build pass?")
			})
			if p := tk.prompt(next); strings.Count(p, "did the build pass?") != 1 || !strings.HasSuffix(p, "did the build pass?") {
				t.Errorf("the next Run is not briefed with the message once, last:\n%s", p)
			}
			if n := tk.count(`SELECT count(*) FROM events WHERE event_type = 'run.directive.failed' AND payload->>'directiveId' = $1`,
				directive); n != 1 {
				t.Errorf("%d run.directive.failed for the message, want 1", n)
			}
			if n := tk.count(`SELECT count(*) FROM events WHERE event_type = 'run.directive.failed' AND payload->>'directiveId' = $1
				AND payload->>'nextRunId' = $2`, directive, next); n != 1 {
				t.Errorf("the message's failure does not name %s as having it:\n%s", next, tk.describeRuns())
			}
			answer := tk.said(next)[0]
			third := tk.replaced(next, "and the docs?")
			tk.replayedOnceBeforeItsAnswer(third, "did the build pass?", answer)
		})
	}
}

// A message lux accepted mid-turn and cancelled when it stopped the
// conductor for a person's pause is sent again when the conductor is
// resumed, and answered there once; it was never failed.
func TestAMessageLuxCancelledAtAPauseIsAnsweredOnceAfterTheResume(t *testing.T) {
	tk, run := hangingFirst(t, "conductor")
	directive := tk.acceptedUnread(run, "did the build pass?")
	if status, out := tk.call("/internal/runs/"+run+"/pause", map[string]any{}); status != 200 {
		t.Fatalf("pause: %d %v", status, out)
	}
	tk.until("the conductor paused", func() bool {
		return tk.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND lux_state = 'stopped'`, run) == 1
	})
	if status, out := tk.call("/internal/runs/"+run+"/resume", map[string]any{}); status != 200 {
		t.Fatalf("resume: %d %v", status, out)
	}
	tk.until("the resumed conductor to answer the message", func() bool {
		said := tk.said(run)
		return len(said) > 0 && strings.Contains(said[len(said)-1], "did the build pass?")
	})
	answered := 0
	for _, s := range tk.said(run) {
		answered += strings.Count(s, "did the build pass?")
	}
	if answered != 1 {
		t.Errorf("the message answered %d times, want once: %q", answered, tk.said(run))
	}
	if n := tk.count(`SELECT count(*) FROM events WHERE event_type = 'run.directive.failed' AND payload->>'directiveId' = $1`, directive); n != 0 {
		t.Errorf("the message was failed %d times", n)
	}
}

// A message the harness refused stays failed when lux then stops the Run:
// it is not handed to the next Run.
func TestARefusedMessageStaysFailedAcrossATerminate(t *testing.T) {
	for _, kind := range []string{"brainstorm", "conductor"} {
		t.Run(kind, func(t *testing.T) {
			tk, first := hangingFirst(t, kind)
			directive := tk.acceptedUnread(first, "did the build pass?")
			tk.lux.FailInput(tk.luxRunOf(first), directive, "claude: the message was refused")
			tk.until("the message failed", func() bool {
				return tk.count(`SELECT count(*) FROM directives WHERE id = $1 AND failed_at IS NOT NULL
					AND error = 'claude: the message was refused'`, directive) == 1
			})
			// Terminated mid-turn, the talker ends failed.
			tk.stopOnItsOwn(first, "terminated")
			tk.until("the Run ended", func() bool {
				return tk.count(`SELECT count(*) FROM runs WHERE id = $1 AND status IN ('completed', 'failed')`, first) == 1
			})
			status, next := tk.write("and the docs?")
			if status != 201 || next == first {
				t.Fatalf("after the end: %d reached %q, want a new Run", status, next)
			}
			tk.until("the new Run's answer", func() bool { return len(tk.said(next)) == 1 })
			if p := tk.prompt(next); strings.Contains(p, "did the build pass?") {
				t.Errorf("a refused message was handed on:\n%s", p)
			}
			if n := tk.count(`SELECT count(*) FROM events WHERE event_type = 'run.directive.failed' AND payload->>'directiveId' = $1`, directive); n != 1 {
				t.Errorf("%d run.directive.failed for the refused message, want 1", n)
			}
		})
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
