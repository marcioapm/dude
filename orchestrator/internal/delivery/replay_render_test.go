package delivery

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

// ev is an event as it comes back from the ledger: its payload decoded
// from JSON, as replayEvents reads it.
func ev(typ, run, who, payload string) replayEvent {
	var p map[string]any
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		panic(err)
	}
	return replayEvent{Type: typ, RunID: run, Who: who, Payload: p, Heard: true}
}

// Every kind of event a brainstorm's replay renders, as the agent reads it.
func TestTheReplayRendersASessionsConversation(t *testing.T) {
	events := []replayEvent{
		ev("chat.message", "run_a", "Ana", `{"text":"where would metering live? ![arch](attachment:att_1)"}`),
		ev("agent.thought", "run_a", "", `{"text":"SECRET THOUGHT"}`),
		ev("agent.tool.called", "run_a", "", `{"tool":"read","callId":"c1","input":{"path":"go.mod"}}`),
		ev("agent.tool.completed", "run_a", "", `{"tool":"read","callId":"c1","status":"completed","output":{"head":"module x"}}`),
		ev("agent.tool.completed", "run_a", "", `{"tool":"bash","callId":"c2","status":"error","exitCode":2,
			"stdout":{"head":"HEAD","tail":"TAIL","omittedBytes":9000},"stderr":{"head":"boom"}}`),
		ev("agent.message", "run_a", "", `{"text":"In billing.\n\nTwo options."}`),
		ev("session.renamed", "run_a", "", `{"title":"Metering","by":"agent"}`),
		ev("question.asked", "run_a", "", `{"kind":"agent","prompt":"Which?","toName":"Ana",
			"items":[{"header":"","question":"Which?","choices":[{"label":"A"},{"label":"B"}]}]}`),
		ev("question.answered", "run_a", "Ana", `{"answer":"B","questionId":"q1"}`),
		ev("session.proposed", "run_a", "", `{"proposalId":"pp1","items":[{"kind":"task","project":"BL","title":"Meter"},
			{"kind":"comment","task":"BL-3","text":"x"}]}`),
		ev("session.filed", "", "Ana", `{"proposalId":"pp1","by":"Ana","filed":[{"item":0,"key":"BL-9","kind":"task"}]}`),
		ev("session.told", "run_a", "", `{"text":"Bo owns this session now."}`),
		ev("session.renamed", "", "Bo", `{"title":"Usage metering","by":"per_bo"}`),
		ev("agent.session.replaced", "run_a", "", `{"from":"s1","to":"s2"}`),
		ev("question.closed", "run_a", "", `{"by":"withdrawn"}`),
		ev("chat.message", "run_b", "Bo", `{"text":"carry on","attachments":[{"id":"att_2","name":"plan.png"}]}`),
		ev("agent.message", "run_b", "", `{"text":"On it."}`),
	}
	got := renderReplay(events)
	want := `Ana: where would metering live? [image: arch]

read({"path":"go.mod"}) → module x

bash() → error: HEAD
[… 9000 bytes cut …]
TAIL
stderr: boom (exit 2)

You: In billing.

Two options.

You named the session "Metering".

You asked Ana: Which? (choices: A; B)

Ana answered: B

You proposed: 1. task "Meter" in BL; 2. a comment on BL-3 — item 1 filed by Ana as BL-9.

dude: Bo owns this session now.

Bo named the session "Usage metering".

[Here the agent's session restarted without its conversation.]

dude: your question was withdrawn; nobody answered it.

[A new agent took over here.]

Bo: carry on [image: plan.png]

You: On it.`
	if got != want {
		t.Errorf("rendered:\n%s\n\nwant:\n%s", got, want)
	}
}

// Every kind of event a conductor's replay renders.
func TestTheReplayRendersATasksChat(t *testing.T) {
	events := []replayEvent{
		ev("chat.message", "run_c", "Márcio", `{"text":"what changed?"}`),
		ev("agent.message", "run_c", "", `{"text":"The retry."}`),
		ev("chat.message", "run_c", "", `{"text":"why 8s?","github":{"login":"ana","repo":"sdk","number":4}}`),
		ev("chat.message", "run_c", "", `{"text":"Because.","by":"conductor","github":{"repo":"sdk","number":4}}`),
		ev("run.steered", "run_c", "Márcio", `{"text":"stop"}`),
		ev("run.steered", "run_c", "Márcio", `{"text":"stop","supersedes":"dir_1","interrupt":true}`),
		ev("conductor.woken", "run_c", "", `{"text":"Decision waiting: before the pull request."}`),
		ev("conductor.decided", "run_c", "", `{"point":"nope","action":"start_phase","phase":"fix","note":"one more"}`),
		ev("review.finding_resolved", "run_c", "", `{"findingId":"fnd_1","note":"Dismissed by the conductor: generated","by":"conductor"}`),
		ev("review.finding_resolved", "run_c", "", `{"findingId":"fnd_2","note":"a person's"}`),
		ev("task.updated", "run_c", "", `{"goal":"g","acceptanceCriteria":[],"by":"conductor"}`),
		ev("question.asked", "run_c", "", `{"kind":"agent","items":[{"header":"Scope","question":"Retry what?"},
			{"header":"Cap","question":"How long?","choices":[{"label":"8s"}]}]}`),
		ev("question.answered", "run_c", "Márcio", `{"answer":"1. ...","note":"and log it"}`),
	}
	want := `Márcio: what changed?

You: The retry.

ana (GitHub): why 8s?

You (on pull request sdk#4): Because.

Márcio: stop

dude: Decision waiting: before the pull request.

You decided, at nope: start_phase (fix): one more.

You dismissed finding fnd_1: Dismissed by the conductor: generated

You changed the task's goal and acceptance criteria.

You asked:
1. Scope — Retry what?
2. Cap — How long? (choices: 8s)

Márcio answered: 1. ...
Márcio, also: and log it`
	if got := renderReplay(events); got != want {
		t.Errorf("rendered:\n%s\n\nwant:\n%s", got, want)
	}
}

// A tool's input and output are each cut at 4 KiB, on a character, saying
// how much; a message the agent never heard is not in its past.
func TestTheReplayCutsLongToolCallsAndSkipsWhatWasNeverHeard(t *testing.T) {
	long := strings.Repeat("é", 3000) // 6000 bytes
	in, _ := json.Marshal(map[string]any{"cmd": long})
	out, _ := json.Marshal(map[string]any{"tool": "bash", "callId": "c", "output": map[string]any{"head": long}})
	unheard := ev("chat.message", "run_a", "Ana", `{"text":"NEVER READ","directiveId":"dir_x"}`)
	unheard.Heard = false
	got := renderReplay([]replayEvent{
		ev("agent.tool.called", "run_a", "", `{"tool":"bash","callId":"c","input":`+string(in)+`}`),
		ev("agent.tool.completed", "run_a", "", string(out)),
		unheard,
	})
	if strings.Contains(got, "NEVER READ") {
		t.Errorf("an unheard message replayed:\n%s", got)
	}
	call, output, ok := strings.Cut(got, " → ")
	if !ok {
		t.Fatalf("no call line: %q", got[:200])
	}
	for name, s := range map[string]string{"input": call, "output": output} {
		i := strings.Index(s, "[… ")
		if i < 0 || !strings.Contains(s[i:], "bytes cut]") {
			t.Errorf("%s not marked cut: …%q", name, s[max(0, len(s)-60):])
			continue
		}
		if !utf8.ValidString(s[:i]) {
			t.Errorf("%s cut inside a character", name)
		}
		if i > replayToolCap+len("bash(") {
			t.Errorf("%s kept %d bytes, want at most %d", name, i, replayToolCap)
		}
	}
}
