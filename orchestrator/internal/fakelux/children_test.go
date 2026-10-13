package fakelux

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// A child's tool step with Done completes the call on every harness, its
// output empty or not; without Done the call stays open.
func TestAChildToolStepCompletesOnDoneEvenWithNoOutput(t *testing.T) {
	// Each harness's record that says a child's call completed.
	completed := map[string]func(rec map[string]any) bool{
		"opencode": func(rec map[string]any) bool {
			d, _ := rec["data"].(map[string]any)
			return rec["type"] == "opencode.part" && d["status"] == "completed" && d["callID"] == "k1"
		},
		"claude-code": func(rec map[string]any) bool {
			b, _ := json.Marshal(rec["data"])
			var line struct {
				Message struct {
					Content []struct {
						Type      string `json:"type"`
						ToolUseID string `json:"tool_use_id"`
					} `json:"content"`
				} `json:"message"`
			}
			_ = json.Unmarshal(b, &line)
			return rec["type"] == "claude.user" && len(line.Message.Content) == 1 &&
				line.Message.Content[0].Type == "tool_result" && line.Message.Content[0].ToolUseID == "k1"
		},
		"codex": func(rec map[string]any) bool {
			d, _ := rec["data"].(map[string]any)
			item, _ := d["item"].(map[string]any)
			return rec["type"] == "codex.item/completed" && item["id"] == "k1" && item["status"] == "completed"
		},
	}
	for adapter, isCompleted := range completed {
		for _, done := range []bool{true, false} {
			t.Run(adapter+map[bool]string{true: " done", false: " open"}[done], func(t *testing.T) {
				fake := New("", "k", func(map[string]any) Behaviour { return Behaviour{Hang: true} })
				t.Cleanup(fake.Close)
				client, run := submitAdapter(t, fake, adapter)
				waitState(t, client, run.ID, "running")
				for _, st := range []ChildStep{
					{Op: "start", Title: "Look", AgentType: "explore"},
					{Op: "tool", ToolCallID: "k1", Tool: "bash", Input: map[string]any{"command": "true"}, Done: done},
				} {
					st.CallID, st.ChildID = "call_1", "kid_1"
					if err := fake.Child(run.ID, st); err != nil {
						t.Fatal(err)
					}
				}
				n := 0
				for _, rec := range fake.Records(run.ID) {
					if isCompleted(rec) {
						n++
					}
				}
				if want := map[bool]int{true: 1, false: 0}[done]; n != want {
					t.Errorf("%d completions of the child's call, want %d: %v", n, want, fake.Records(run.ID))
				}
			})
		}
	}
}

func submitAdapter(t *testing.T, fake *Server, adapter string) (*lux.HTTPClient, lux.Run) {
	t.Helper()
	srv := httptest.NewServer(fake.Handler())
	t.Cleanup(srv.Close)
	client := lux.New(srv.URL, "k")
	run, err := client.Submit(context.Background(), lux.Spec{Name: "t", Image: lux.Image{Ref: "agent:1"},
		Workload: lux.Workload{Adapter: adapter, Prompt: "go"}}, "child-"+adapter)
	if err != nil {
		t.Fatal(err)
	}
	return client, run
}
