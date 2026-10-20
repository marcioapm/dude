package phases

import (
	"fmt"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// childDirective drives a finished investigator child that streamed frames
// through the production path — the artifact sweep's terminal drain,
// settlement, the outbox — and returns the directive its parent is sent.
func childDirective(t *testing.T, frames ...lux.Frame) string {
	t.Helper()
	w := newResumeWorld(t)
	w.exec(`INSERT INTO runs(id,organization_id,project_id,task_id,attempt,role,status) VALUES('run_parent',$1,$2,$3,1,'conductor','running')`, w.run.Org, w.run.ProjectID, w.run.TaskID)
	w.exec(`INSERT INTO workflow_runs(id,organization_id,task_id,workflow_type,idempotency_key,status,step,state) VALUES('wf_reply',$1,$2,'task.delivery','reply','waiting','awaitPullRequest','{"attempt":1}')`, w.run.Org, w.run.TaskID)
	w.exec(`UPDATE runs SET source_kind='task_conductor',parent_run_id='run_parent',phase='investigate',role='investigator',child_profile='investigator',child_capabilities='{}',status='completed',ended_at=now() WHERE id=$1`, w.run.ID)
	store := &delivery.Store{DB: w.s.DB}
	fake := &childTerminalLux{sweepLux: &sweepLux{}, frames: frames}
	if _, err := (&Artifacts{DB: w.s.DB, Lux: fake, Log: w.s.Log}).Sweep(w.ctx); err != nil {
		t.Fatal(err)
	}
	if n, err := settleCollectedChildren(w.ctx, store); err != nil || n != 1 {
		t.Fatalf("settlement %d %v", n, err)
	}
	if n, err := store.EnqueueChildResults(w.ctx); err != nil || n != 1 {
		t.Fatalf("parent directive %d %v", n, err)
	}
	var text string
	if err := w.owner.QueryRow(w.ctx, `SELECT d.text FROM child_result_outbox o JOIN directives d ON d.id = o.directive_id
		WHERE o.child_run_id = $1`, w.run.ID).Scan(&text); err != nil {
		t.Fatal(err)
	}
	return text
}

func said(text string) lux.Frame {
	return record(1, "acp.agent_message_chunk", map[string]any{"sessionUpdate": "agent_message_chunk",
		"content": map[string]any{"type": "text", "text": text}})
}

func toolCall(id, status string) lux.Frame {
	return record(1, "acp.tool_call", map[string]any{"sessionUpdate": "tool_call", "toolCallId": id, "title": "bash", "status": status,
		"rawInput": map[string]any{"command": "go test ./..."}})
}

var idleFrame = record(1, "lux.activity", map[string]any{"activity": "idle"})

// quotedReply is the fenced reply section of a child-result directive, ""
// when it has none.
func quotedReply(t *testing.T, text string) string {
	t.Helper()
	_, after, ok := strings.Cut(text, "```\n")
	if !ok {
		return ""
	}
	body, _, ok := strings.Cut(after, "\n```")
	if !ok {
		t.Fatalf("unclosed reply quote: %q", text)
	}
	return body
}

func TestChildResultCarriesFinalReply(t *testing.T) {
	for name, end := range map[string][]lux.Frame{"turn ended": {idleFrame}, "stream ended mid-message": nil} {
		t.Run(name, func(t *testing.T) {
			frames := []lux.Frame{said("Looking at the branch first."), toolCall("c1", "in_progress"), toolCall("c1", "completed"),
				said("I did not implement it: "), said("the base branch is missing.\n\nchecks: []")}
			text := childDirective(t, append(frames, end...)...)
			if got := quotedReply(t, text); got != "I did not implement it: the base branch is missing.\n\nchecks: []" {
				t.Fatalf("quoted reply %q in\n%s", got, text)
			}
		})
	}
}

func TestChildResultBoundsLongReply(t *testing.T) {
	head, tail := "HEAD-OF-REPLY ", " TAIL-OF-REPLY"
	long := head + strings.Repeat("x", 3*delivery.ChildReplyLimit) + tail
	text := childDirective(t, said(long), idleFrame)
	got := quotedReply(t, text)
	if n := len([]rune(got)); n > delivery.ChildReplyLimit || n < delivery.ChildReplyLimit-100 {
		t.Fatalf("quoted reply is %d characters, limit %d", n, delivery.ChildReplyLimit)
	}
	if !strings.HasPrefix(got, head) || !strings.HasSuffix(got, tail) {
		t.Fatalf("lost the reply's head or tail: %q … %q", got[:40], got[len(got)-40:])
	}
	parts := strings.SplitN(got, "\n\n", 3)
	var omitted int
	if len(parts) != 3 {
		t.Fatalf("no elision marker in the bounded reply")
	}
	if _, err := fmt.Sscanf(parts[1], "[… %d characters of the reply left out here …]", &omitted); err != nil {
		t.Fatalf("elision marker %q: %v", parts[1], err)
	}
	if kept := len([]rune(parts[0])) + len([]rune(parts[2])); kept+omitted != len([]rune(long)) {
		t.Fatalf("kept %d + left out %d characters, reply has %d", kept, omitted, len([]rune(long)))
	}
}

func TestChildResultWithoutReplyHasNoQuote(t *testing.T) {
	text := childDirective(t, said("Running it."), toolCall("c1", "in_progress"), toolCall("c1", "completed"), idleFrame)
	if strings.Contains(text, "```") || strings.Contains(text, "Running it.") || strings.Contains(text, "final reply") {
		t.Fatalf("a child with no final reply got a reply section:\n%s", text)
	}
}
