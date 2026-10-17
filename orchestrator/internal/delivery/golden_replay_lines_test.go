package delivery

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

// Every line a replay renders an event as, its cuts and its budget's
// drops: what the agent reads of the conversation before it.
func TestGoldenReplayLines(t *testing.T) {
	stdout, _ := json.Marshal(map[string]any{"head": "first\n", "tail": "\nlast", "omittedBytes": 42})
	long := strings.Repeat("é", replayToolCap)
	events := []replayEvent{
		ev("chat.message", "run_a", "", `{"text":"anyone"}`),
		{Type: "chat.message", RunID: "run_a", ActorID: "github:dee", Payload: map[string]any{"text": "from GitHub"}, Heard: true},
		ev("question.asked", "run_a", "", `{"prompt":"To one?","toName":"Bo"}`),
		ev("question.asked", "run_a", "", `{"items":[{"header":"H","question":"Single?"}]}`),
		ev("session.proposed", "run_a", "", `{"proposalId":"pp2","items":[{"kind":"task","project":"BL","title":"T"},
			{"kind":"comment","task":"BL-3","text":"x"}]}`),
		ev("session.filed", "", "Ana", `{"proposalId":"pp2","by":"Ana","filed":[{"item":0,"key":"BL-9"},{"item":1,"key":"BL-3"}]}`),
		ev("session.renamed", "run_a", "", `{"title":"Mine","by":"agent"}`),
		ev("task.updated", "run_a", "", `{"by":"conductor","goal":"g"}`),
		ev("review.finding_resolved", "run_a", "", `{"by":"conductor","findingId":"fnd_2","note":"person says fine"}`),
		ev("agent.tool.completed", "run_a", "", `{"tool":"bash","callId":"c1","status":"error","exitCode":3,
			"stdout":`+string(stdout)+`,"stderr":{"head":"oops"}}`),
		ev("agent.tool.called", "run_a", "", `{"tool":"read","callId":"c2","input":{"path":"`+long+`"}}`),
		ev("agent.tool.completed", "run_a", "", `{"tool":"read","callId":"c2","status":"completed","output":{"head":"ok"}}`),
		ev("agent.tool.completed", "run_a", "", `{"callId":"","status":"completed","exitCode":0,"output":{"head":"  "}}`),
	}
	golden(t, "replay-lines", renderReplay(events))
	// Over budget: outputs dropped first, then calls, then whole turns.
	turns := []replayEvent{
		ev("chat.message", "run_a", "Ana", `{"text":"`+strings.Repeat("a", 30)+`"}`),
		ev("agent.tool.completed", "run_a", "", `{"tool":"bash","callId":"c1","output":{"head":"`+strings.Repeat("o", 40)+`"}}`),
		ev("agent.message", "run_b", "", `{"text":"`+strings.Repeat("b", 30)+`"}`),
	}
	var b strings.Builder
	for _, budget := range []int{60, 52, 49, 46, 40, 30} {
		b.WriteString("## budget " + strconv.Itoa(budget) + "\n\n" +
			fitReplay(replayStart{summary: "S", run: "run_a"}, replayEntries(turns), budget) + "\n\n")
	}
	golden(t, "replay-budget", b.String())
}
