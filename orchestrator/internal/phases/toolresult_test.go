package phases

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestShortOutputIsKeptWhole(t *testing.T) {
	got := capOutput("ok\n")
	if got["head"] != "ok\n" || got["tail"] != nil {
		t.Errorf("got %v", got)
	}
}

func TestLongOutputKeepsHeadAndTailAsValidText(t *testing.T) {
	// Multi-byte characters straddling both cuts.
	s := strings.Repeat("é", 3000)
	got := capOutput(s)
	head, tail := got["head"].(string), got["tail"].(string)
	if !utf8.ValidString(head) || !utf8.ValidString(tail) {
		t.Fatal("a cut split a character")
	}
	if len(head)+len(tail)+got["omittedBytes"].(int) != len(s) {
		t.Errorf("head %d + tail %d + omitted %v != %d", len(head), len(tail), got["omittedBytes"], len(s))
	}
}

func TestSeparateStreamsStayApart(t *testing.T) {
	got := toolResult(map[string]any{"rawOutput": map[string]any{"stdout": "out", "stderr": "err", "exitCode": float64(1)}})
	if got["exitCode"] != 1 || got["stdout"].(map[string]any)["head"] != "out" || got["stderr"].(map[string]any)["head"] != "err" {
		t.Errorf("got %v", got)
	}
}

func TestOpenCodeOutputPrefersWhatAPersonSees(t *testing.T) {
	// A file read: content is the file; rawOutput wraps it in markup.
	got := toolResult(map[string]any{
		"content":   []any{map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": "the file"}}},
		"rawOutput": map[string]any{"output": "<path>x</path><content>1: the file</content>"},
	})
	if got["output"].(map[string]any)["head"] != "the file" {
		t.Errorf("got %v", got)
	}
	if _, ok := got["exitCode"]; ok {
		t.Error("a read has no exit code")
	}
}

func TestAnInvalidByteEarlyOnDoesNotEmptyTheHead(t *testing.T) {
	// Latin-1 text: invalid UTF-8 from the first line.
	s := "caf\xe9\n" + strings.Repeat("x", 5000)
	head := capOutput(s)["head"].(string)
	if len(head) != outputKeep {
		t.Errorf("head is %d bytes, want %d", len(head), outputKeep)
	}
}

func TestColourReachesTheChatUntouched(t *testing.T) {
	// Rendering it is the chat's job; the orchestrator keeps the bytes.
	out := "\x1b[31mFAILED\x1b[0m test_x.py::\x1b[1mtest_a\x1b[0m\n"
	if got := capOutput(out)["head"]; got != out {
		t.Errorf("got %q", got)
	}
}
