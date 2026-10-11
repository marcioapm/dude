package delivery

import (
	"encoding/json"
	"fmt"
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
	// stdout as phases.capOutput stores an output over 4 KiB: its first and
	// last 2048 bytes, and how many bytes between them it dropped.
	head := strings.Repeat("h", 2047) + "\n"
	tail := strings.Repeat("t", 2040) + "\nEXIT OK"
	stdout, _ := json.Marshal(map[string]any{"head": head, "tail": tail, "omittedBytes": 9000})
	events := []replayEvent{
		ev("chat.message", "run_a", "Ana", `{"text":"where would metering live? ![arch](attachment:att_1)"}`),
		ev("agent.thought", "run_a", "", `{"text":"SECRET THOUGHT"}`),
		ev("agent.tool.called", "run_a", "", `{"tool":"read","callId":"c1","input":{"path":"go.mod"}}`),
		ev("agent.tool.completed", "run_a", "", `{"tool":"read","callId":"c1","status":"completed","output":{"head":"module x"}}`),
		ev("agent.tool.completed", "run_a", "", `{"tool":"bash","callId":"c2","status":"error","exitCode":2,
			"stdout":`+string(stdout)+`,"stderr":{"head":"boom"}}`),
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

bash() → error: ` + head + `
[… 9000 bytes cut …]
` + tail + `
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
	if i := strings.Index(got, "\n\nYou: In billing."); !strings.Contains(got, "\nEXIT OK\nstderr: boom (exit 2)") && i >= 0 {
		t.Errorf("the stored tail's last line is cut: …%q", got[max(0, i-80):i])
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
		ev("run.steered", "run_c", "", `{"text":"CONDUCTOR STEER","by":"conductor"}`),
		ev("conductor.woken", "run_c", "", `{"text":"Decision waiting: before the pull request."}`),
		ev("conductor.decided", "run_c", "", `{"point":"before_pull_request","action":"start_phase","phase":"fix","note":"one more"}`),
		ev("review.finding_resolved", "run_c", "", `{"findingId":"fnd_1","note":"Dismissed by the conductor: generated","by":"conductor"}`),
		ev("review.finding_resolved", "run_c", "", `{"findingId":"fnd_2","note":"a person's"}`),
		ev("task.updated", "run_c", "", `{"goal":"g","acceptanceCriteria":[],"by":"conductor"}`),
		ev("question.asked", "run_c", "", `{"kind":"agent","items":[{"header":"Scope","question":"Retry what?"},
			{"header":"Cap","question":"How long?","choices":[{"label":"8s"}]}]}`),
		ev("question.answered", "run_c", "Márcio", `{"answer":"1. ...","note":"and log it"}`),
		ev("question.closed", "run_c", "", `{"questionId":"q2","by":"decision","action":"start_phase"}`),
	}
	want := `Márcio: what changed?

You: The retry.

ana (GitHub): why 8s?

You (on pull request sdk#4): Because.

Márcio: stop

dude: Decision waiting: before the pull request.

You decided, at before the pull request: start_phase (fix): one more.

Finding fnd_1: dismissed by the conductor: generated.

You changed the task's goal and acceptance criteria.

You asked:
1. Scope — Retry what?
2. Cap — How long? (choices: 8s)

Márcio answered: 1. ...
Márcio, also: and log it

dude: your question was settled elsewhere, unanswered.`
	if got := renderReplay(events); got != want {
		t.Errorf("rendered:\n%s\n\nwant:\n%s", got, want)
	}
}

// The replay starts from the agent's newest compaction summary when the
// ledger has one: the summary under its heading, then only what came
// after. A compaction with no summary (a remote one) changes nothing.
func TestTheReplayStartsFromTheNewestCompactionSummary(t *testing.T) {
	before := ev("chat.message", "run_a", "Ana", `{"text":"OLDEST"}`)
	first := ev("agent.context.compacted", "run_a", "", `{"summary":"FIRST SUMMARY"}`)
	mid := ev("agent.message", "run_a", "", `{"text":"MIDDLE"}`)
	second := ev("agent.context.compacted", "run_b", "", `{"summary":"SECOND SUMMARY","trigger":"auto"}`)
	empty := ev("agent.context.compacted", "run_b", "", `{"trigger":"auto"}`)
	after := ev("chat.message", "run_b", "Bo", `{"text":"NEWEST"}`)

	summary, rest := fromCompaction([]replayEvent{before, first, mid, after})
	if got := renderReplayFrom(summary, rest); got != "## Earlier, as the agent summarised it\n\nFIRST SUMMARY\n\n## Since then\n\nYou: MIDDLE\n\n"+
		"[A new agent took over here.]\n\nBo: NEWEST" {
		t.Errorf("one summary mid-ledger:\n%s", got)
	}
	summary, rest = fromCompaction([]replayEvent{before, first, mid, second, empty, after})
	got := renderReplayFrom(summary, rest)
	if got != "## Earlier, as the agent summarised it\n\nSECOND SUMMARY\n\n## Since then\n\nBo: NEWEST" {
		t.Errorf("two summaries, want the newest:\n%s", got)
	}
	summary, rest = fromCompaction([]replayEvent{before, empty, after})
	if got := renderReplayFrom(summary, rest); got != "Ana: OLDEST\n\n[A new agent took over here.]\n\nBo: NEWEST" {
		t.Errorf("a compaction with no summary:\n%s", got)
	}
	// The summary alone, when nothing came after it.
	summary, rest = fromCompaction([]replayEvent{before, first})
	if got := renderReplayFrom(summary, rest); got != "## Earlier, as the agent summarised it\n\nFIRST SUMMARY" {
		t.Errorf("a summary last:\n%s", got)
	}
	// A summary by Run A's agent, then Run B's: B's agent took over.
	summary, rest = fromCompaction([]replayEvent{before, first, after})
	if got := renderReplayFrom(summary, rest); got != "## Earlier, as the agent summarised it\n\nFIRST SUMMARY\n\n## Since then\n\n"+
		"[A new agent took over here.]\n\nBo: NEWEST" {
		t.Errorf("a summary, then another Run:\n%s", got)
	}
}

// A proposal made before the summary and filed after it: the filing is
// said on its own, since the proposal's line is not in the replay.
func TestAFilingOfAProposalBeforeTheSummaryIsReplayed(t *testing.T) {
	summary, rest := fromCompaction([]replayEvent{
		ev("session.proposed", "run_s", "", `{"proposalId":"pp1","items":[{"kind":"task","project":"BL","title":"Meter"}]}`),
		ev("agent.context.compacted", "run_s", "", `{"summary":"SUM"}`),
		ev("session.filed", "", "Ana", `{"proposalId":"pp1","by":"Ana","filed":[{"item":0,"key":"BL-9","kind":"task"}]}`),
		ev("agent.message", "run_s", "", `{"text":"Noted."}`),
	})
	want := "## Earlier, as the agent summarised it\n\nSUM\n\n## Since then\n\nYour earlier proposal: item 1 filed by Ana as BL-9.\n\nYou: Noted."
	if got := renderReplayFrom(summary, rest); got != want {
		t.Errorf("rendered:\n%s\n\nwant:\n%s", got, want)
	}
}

// Over budget, the replay sheds in order: tool outputs, oldest first, the
// call kept; then tool calls whole, oldest first; then, only if still
// over, the oldest turns, said in one line. A person's or the agent's
// words are never cut, and the summary is never dropped.
func TestTheReplayKeepsToItsBudget(t *testing.T) {
	words := func(who, s string) replayEntry { return replayEntry{run: "run_a", words: who + ": " + s} }
	tool := func(name, out string) replayEntry {
		return replayEntry{run: "run_a", tool: true, call: name + "({})", output: out}
	}
	big := strings.Repeat("o", 400)
	ana := words("Ana", strings.Repeat("a", 200))
	you := words("You", strings.Repeat("y", 200))
	entries := func() []replayEntry {
		return []replayEntry{ana, tool("t1", big), you, tool("t2", big), words("Bo", "last word")}
	}
	full := joinReplay(replayStart{}, entries(), 0)
	tokens := replayTokens(full)
	if tokens != (utf8.RuneCountInString(full)+3)/4 {
		t.Fatalf("tokens %d for %d characters", tokens, utf8.RuneCountInString(full))
	}
	if got := fitReplay(replayStart{}, entries(), tokens); got != full {
		t.Errorf("within budget, changed:\n%s", got)
	}

	// Step 1: the oldest output goes first, its call line kept.
	got := fitReplay(replayStart{}, entries(), tokens-50)
	if !strings.Contains(got, "t1({}) → [output dropped]") || !strings.Contains(got, "t2({}) → "+big) {
		t.Errorf("step 1, the oldest output:\n%s", got)
	}
	// Then every output.
	got = fitReplay(replayStart{}, entries(), tokens-150)
	if !strings.Contains(got, "t1({}) → [output dropped]") || !strings.Contains(got, "t2({}) → [output dropped]") {
		t.Errorf("step 1, both outputs:\n%s", got)
	}
	// Step 2: calls whole, oldest first; every word kept.
	afterOutputs := replayTokens(got)
	got = fitReplay(replayStart{}, entries(), afterOutputs-3)
	if strings.Contains(got, "t1(") || !strings.Contains(got, "t2({}) → [output dropped]") || !strings.Contains(got, ana.words) {
		t.Errorf("step 2, the oldest call:\n%s", got)
	}
	got = fitReplay(replayStart{}, entries(), afterOutputs-12)
	if strings.Contains(got, "t1(") || strings.Contains(got, "t2(") ||
		got != ana.words+"\n\n"+you.words+"\n\nBo: last word" {
		t.Errorf("step 2, every call:\n%s", got)
	}
	// Step 3: the oldest turns, whole, said in a line.
	got = fitReplay(replayStart{}, entries(), replayTokens(you.words+"\n\nBo: last word")+12)
	if got != "[1 earlier messages omitted]\n\n"+you.words+"\n\nBo: last word" {
		t.Errorf("step 3:\n%s", got)
	}
	// The summary stays whatever the budget.
	summary := strings.Repeat("s", 600)
	got = fitReplay(replayStart{summary: summary}, entries(), 10)
	if !strings.HasPrefix(got, "## Earlier, as the agent summarised it\n\n"+summary) {
		t.Errorf("the summary dropped:\n%s", got)
	}
	for _, w := range []string{ana.words, you.words, "Bo: last word"} {
		if i := strings.Index(got, w[:min(len(w), 8)]); i >= 0 && !strings.Contains(got, w) {
			t.Errorf("words cut: %q", got[i:])
		}
	}
	if replayBudgetTokens != 100_000 {
		t.Errorf("budget %d", replayBudgetTokens)
	}
}

// A dropped tool call can bring two Runs' turns together, adding a
// take-over line the estimate did not count: the rendering is then over
// by less than a turn, and the oldest turn left goes whole, never cut.
func TestTheReplayDropsAWholeTurnWhenItsRenderingIsStillOver(t *testing.T) {
	a1 := replayEntry{run: "run_a", words: "Ana: " + strings.Repeat("a", 200)}
	a2 := replayEntry{run: "run_a", words: "You: " + strings.Repeat("y", 200)}
	tb := replayEntry{run: "run_b", tool: true, call: "t({})", output: strings.Repeat("o", 300)}
	b1 := replayEntry{run: "run_b", words: "Bo: " + strings.Repeat("b", 200)}
	b2 := replayEntry{run: "run_b", words: "You: " + strings.Repeat("z", 200)}
	entries := []replayEntry{a1, a2, tb, b1, b2}
	// With a1 and the call gone, the estimate leaves out the take-over line
	// before b1: within budget by it, over by the rendering.
	rendered := joinReplay(replayStart{}, []replayEntry{a2, b1, b2}, 1)
	budget := (utf8.RuneCountInString(rendered) - 1) / 4
	estimate := utf8.RuneCountInString(rendered) - utf8.RuneCountInString(tookOver) - 2
	if estimate > budget*4 {
		t.Fatalf("the budget %d is under the estimate %d", budget*4, estimate)
	}
	got := fitReplay(replayStart{}, entries, budget)
	want := "[2 earlier messages omitted]\n\n" + b1.words + "\n\n" + b2.words
	if got != want {
		t.Errorf("fitted:\n%s\n\nwant:\n%s", got, want)
	}
	for _, p := range strings.Split(got, "\n\n") {
		for _, e := range entries {
			if len(p) < len(e.text()) && strings.HasPrefix(e.text(), p) {
				t.Errorf("a turn cut: %q", p)
			}
		}
	}
}

// A ledger longer than the budget is rendered within it: the oldest
// messages said in a line, the newest whole.
func TestTheRenderedReplayKeepsToTheBudget(t *testing.T) {
	var events []replayEvent
	for i := range 120 {
		text := fmt.Sprintf("%03d ", i) + strings.Repeat("w", 3996)
		events = append(events, ev("agent.message", "run_a", "", `{"text":"`+text+`"}`))
	}
	got := renderReplayFrom(replayStart{}, events)
	if n := replayTokens(got); n > replayBudgetTokens {
		t.Errorf("%d tokens, over the budget of %d", n, replayBudgetTokens)
	}
	if !strings.HasPrefix(got, "[") || !strings.Contains(got[:40], " earlier messages omitted]\n\n") {
		t.Errorf("does not start saying what it omitted: %q", got[:min(len(got), 60)])
	}
	if last := "You: 119 " + strings.Repeat("w", 3996); !strings.HasSuffix(got, "\n\n"+last) {
		t.Errorf("the last message is not whole at the end: …%q", got[max(0, len(got)-60):])
	}
}

// A tool's input and output are each cut at 4 KiB, on a character, saying
// how much; a message the agent never heard is not in its past.
func TestTheReplayCutsLongToolCallsAndSkipsWhatWasNeverHeard(t *testing.T) {
	// One byte in, so byte 4096 of the input and of the output falls inside
	// a two-byte character.
	long := "x" + strings.Repeat("é", 3000) // 6001 bytes
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

// Dropping the oldest turns of many Runs keeps the newest that fit: each
// dropped turn that began a Run's turn takes its take-over line with it,
// so turns are not shed beyond what the budget needs.
func TestTheReplayBudgetCountsTheTakeOverLinesItDrops(t *testing.T) {
	var entries []replayEntry
	for i := range 200 {
		entries = append(entries, replayEntry{run: fmt.Sprintf("run_%03d", i), words: fmt.Sprintf("Ana: message number %03d here", i)})
	}
	budget := 2000
	got := fitReplay(replayStart{}, entries, budget)
	if replayTokens(got) > budget {
		t.Fatalf("over budget: %d tokens", replayTokens(got))
	}
	kept := strings.Count(got, "Ana: message number")
	// The fewest turns that fit, kept from the newest, at most one under.
	best := 0
	for n := 1; n <= len(entries); n++ {
		if replayTokens(joinReplay(replayStart{}, entries[len(entries)-n:], len(entries)-n)) > budget {
			break
		}
		best = n
	}
	if kept < best-1 {
		t.Fatalf("kept %d turns, %d fit", kept, best)
	}
	if !strings.Contains(got, "Ana: message number 199 here") {
		t.Fatalf("the newest turn is missing:\n%s", got)
	}
}
