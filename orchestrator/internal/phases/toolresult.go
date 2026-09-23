package phases

import (
	"strings"
	"unicode/utf8"
)

// What the ledger keeps of a tool's output: enough to see what happened,
// not a copy of every file an agent read. Longer output keeps its head and
// its tail, which is where a command says what it is doing and how it ended.
const (
	outputLimit = 4096
	outputKeep  = outputLimit / 2
)

// promptRequestID is the id lux acknowledges the task itself under, as
// opposed to a person's steering.
const promptRequestID = "prompt"

// toolResult is a finished tool call's outcome in dude's vocabulary:
// `output` (or `stdout` and `stderr`, for an agent that keeps them apart),
// each capped, and `exitCode` when the agent reports one.
//
// ACP puts what a person should see in `content`, and the tool's own
// result in `rawOutput`, whose shape is the agent's. OpenCode's bash tool
// merges both streams into rawOutput.output and reports the exit code in
// rawOutput.metadata.exit.
func toolResult(u map[string]any) map[string]any {
	out := map[string]any{}
	raw, _ := u["rawOutput"].(map[string]any)
	meta, _ := raw["metadata"].(map[string]any)

	if exit, ok := meta["exit"].(float64); ok {
		out["exitCode"] = int(exit)
	} else if exit, ok := raw["exitCode"].(float64); ok {
		out["exitCode"] = int(exit)
	}

	stdout, hasStdout := raw["stdout"].(string)
	stderr, hasStderr := raw["stderr"].(string)
	switch {
	case hasStdout || hasStderr:
		if stdout != "" {
			out["stdout"] = capOutput(stdout)
		}
		if stderr != "" {
			out["stderr"] = capOutput(stderr)
		}
	default:
		// What the agent shows a person, before the tool's raw result: a file
		// read's content is the file, not its line-numbered wrapper.
		text := contentText(u["content"])
		if text == "" {
			text, _ = raw["output"].(string)
		}
		if text != "" {
			out["output"] = capOutput(text)
		}
	}
	return out
}

// contentText joins the text blocks of an ACP content list.
func contentText(v any) string {
	items, _ := v.([]any)
	var b strings.Builder
	for _, item := range items {
		m, _ := item.(map[string]any)
		inner, _ := m["content"].(map[string]any)
		if inner["type"] == "text" {
			s, _ := inner["text"].(string)
			b.WriteString(s)
		}
	}
	return b.String()
}

// capOutput keeps output whole up to the limit, and otherwise its first and
// last halves with how much was left out between them.
func capOutput(s string) map[string]any {
	if len(s) <= outputLimit {
		return map[string]any{"head": s}
	}
	head, tail := cutRunes(s[:outputKeep], false), cutRunes(s[len(s)-outputKeep:], true)
	return map[string]any{"head": head, "tail": tail, "omittedBytes": len(s) - len(head) - len(tail)}
}

// cutRunes drops a character split by the byte cut, so the result is
// valid UTF-8: at the end of a head, or at the start of a tail.
func cutRunes(s string, fromStart bool) string {
	if fromStart {
		for len(s) > 0 && !utf8.RuneStart(s[0]) {
			s = s[1:]
		}
		return s
	}
	// Only a character cut at the end: invalid bytes elsewhere are the
	// output's own, and JSON encoding replaces them.
	for i := 0; i < utf8.UTFMax-1 && len(s) > 0; i++ {
		if r, size := utf8.DecodeLastRuneInString(s); r != utf8.RuneError || size != 1 {
			break
		}
		s = s[:len(s)-1]
	}
	return s
}
