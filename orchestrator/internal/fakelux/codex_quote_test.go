package fakelux

import (
	"os/exec"
	"strings"
	"testing"
)

// Codex reports a command as the shell's joined argv. A script containing an
// apostrophe must round-trip through a real POSIX shell from every fake Codex
// command emitter.
func TestCodexCommandEmittersQuoteApostrophes(t *testing.T) {
	const script = "echo 'hello'"
	fake := New("", "k", func(map[string]any) Behaviour { return Behaviour{Hang: true} })
	t.Cleanup(fake.Close)
	client, submitted := submitAdapter(t, fake, "codex")
	waitState(t, client, submitted.ID, "running")
	fake.mu.Lock()
	run := fake.runs[submitted.ID]
	fake.command(run, "scripted", [3]string{script, "hello", "0"})
	fake.harnessTool(run, "translated", "bash", map[string]any{"command": script}, false, "")
	fake.mu.Unlock()
	for _, step := range []ChildStep{
		{Op: "start", Title: "Look", AgentType: "explore"},
		{Op: "tool", ToolCallID: "child", Tool: "bash", Input: map[string]any{"command": script}},
	} {
		step.CallID, step.ChildID = "call_1", "kid_1"
		if err := fake.Child(submitted.ID, step); err != nil {
			t.Fatal(err)
		}
	}

	commands := map[string]string{}
	for _, rec := range fake.Records(submitted.ID) {
		data, _ := rec["data"].(map[string]any)
		item, _ := data["item"].(map[string]any)
		if rec["type"] == "codex.item/started" && item["type"] == "commandExecution" {
			id, _ := item["id"].(string)
			commands[id], _ = item["command"].(string)
		}
	}
	for id, shell := range map[string]string{"scripted": "/bin/bash", "translated": "/bin/bash", "child": "/bin/zsh"} {
		command := commands[id]
		word, ok := strings.CutPrefix(command, shell+" -lc ")
		if !ok {
			t.Fatalf("%s emitted %q", id, command)
		}
		out, err := exec.Command("/bin/sh", "-c", "printf %s "+word).CombinedOutput()
		if err != nil {
			t.Fatalf("%s: decoding %q: %v: %s", id, word, err, out)
		}
		if got := string(out); got != script {
			t.Errorf("%s emitted %q, which the shell decodes as %q, want %q", id, command, got, script)
		}
	}
}
