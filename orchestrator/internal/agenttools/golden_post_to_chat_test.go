package agenttools

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/capabilities"
)

// What an automation's agent is given: its tools in order, post_to_chat's
// definition, and every refusal post_to_chat can answer.
func TestGoldenAutomationTools(t *testing.T) {
	var b strings.Builder
	b.WriteString("\"automation\":")
	for _, tl := range (Caller{AutomationID: "aut_1", Role: "automation", grants: capabilities.Of(capabilities.TasksCreate, capabilities.GitHubRead)}).tools() {
		b.WriteString(" " + tl.name)
	}
	b.WriteString("\n")
	golden(t, "tool-lists-automation", b.String())

	schema, err := json.MarshalIndent(postToChatTool.schema, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "tool-post_to_chat", fmt.Sprintf("# %s\n\n%s\n\n```json\n%s\n```\n", postToChatTool.name, postToChatTool.description, schema))

	goldenLines(t, "refusals-post-to-chat", [][2]string{
		{"automations-only", rPostAutomationsOnly},
		{"name-a-channel", rPostNameAChannel},
		{"text-length", fmt.Sprintf(rPostTextLength, postTextMax, 40001)},
		{"per-run", fmt.Sprintf(rPostPerRun, postsPerRun)},
		{"not-connected", fmt.Sprintf(rPostNotConnected, "Acme")},
		{"unknown-channel", fmt.Sprintf(rPostUnknownChannel, "#nowhere")},
		{"not-in-channel", fmt.Sprintf(rPostNotInChannel, "#random")},
		{"lookup-cut", fmt.Sprintf(rPostLookupCut, "#team-59")},
		{"slack-busy", fmt.Sprintf(rPostSlackBusy, "slack chat.postMessage answered 503")},
		{"slack-refused", fmt.Sprintf(rPostSlackRefused, "slack chat.postMessage: msg_too_long")},
	})
}
