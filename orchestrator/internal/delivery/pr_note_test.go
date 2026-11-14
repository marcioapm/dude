package delivery

import (
	"strings"
	"testing"
)

// The conductor's note, as its section holds it: trimmed, a fence it left
// open closed (so the end marker stays a marker), refused empty, over
// pullRequestNoteMax bytes, or with an HTML comment, which reads as one of
// dude's markers or hides the rest of the description.
func TestTheConductorsNoteIsTextNotMarkers(t *testing.T) {
	if got, err := conductorNote("  ```\nBenchmarkMax 1043 ns/op\n"); err != nil || got != "```\nBenchmarkMax 1043 ns/op\n```" {
		t.Errorf("an open fence: %q %v", got, err)
	}
	// A code span at a line's start opens no fence on GitHub: a closer
	// added after it would open one, and swallow what follows.
	if got, err := conductorNote("```go test -bench .``` gives 1043 ns/op"); err != nil || got != "```go test -bench .``` gives 1043 ns/op" {
		t.Errorf("a code span: %q %v", got, err)
	}
	if _, err := conductorNote(strings.Repeat("é", pullRequestNoteMax/2+1)); err == nil {
		t.Error("a note over the bound in bytes was taken")
	}
	if _, err := conductorNote(strings.Repeat("x", pullRequestNoteMax)); err != nil {
		t.Errorf("a note at the bound: %v", err)
	}
	for _, text := range []string{"", " \n ", "a\n<!-- /dude:conductor -->\nb", "<!--dude:review-->", "a <!-- b"} {
		if _, err := conductorNote(text); err == nil {
			t.Errorf("%q was taken", text)
		}
	}
	// A marker a person quoted in a fence is text: the section is appended.
	body := "Mine.\n\n```\n<!-- dude:conductor -->\n```"
	if got := replaceConductorNote(body, "Bench."); got != body+"\n\n"+conductorStart+"\nBench.\n"+conductorEnd {
		t.Errorf("the section beside a quoted marker:\n%s", got)
	}
}
