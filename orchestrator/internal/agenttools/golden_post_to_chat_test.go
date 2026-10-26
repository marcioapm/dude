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
		{"automations-only", postRefusal("automations-only")},
		{"name-a-channel", postRefusal("name-a-channel")},
		{"text-length", fmt.Sprintf(postRefusal("text-length"), postTextMax, 40001)},
		{"per-run", fmt.Sprintf(postRefusal("per-run"), postsPerRun)},
		{"not-connected", fmt.Sprintf(postRefusal("not-connected"), "Acme")},
		{"unknown-channel", fmt.Sprintf(postRefusal("unknown-channel"), "#nowhere")},
		{"not-in-channel", fmt.Sprintf(postRefusal("not-in-channel"), "#random")},
		{"slack-busy", fmt.Sprintf(postRefusal("slack-busy"), "slack chat.postMessage answered 503")},
		{"slack-refused", fmt.Sprintf(postRefusal("slack-refused"), "slack chat.postMessage: msg_too_long")},
	})
}
