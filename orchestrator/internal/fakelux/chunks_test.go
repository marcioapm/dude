package fakelux

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// A reply is streamed in chunks the agent's ACP stream carries as JSON
// strings: a chunk that ends inside a multibyte character arrives as U+FFFD.
func TestChunksCutOnRuneBoundaries(t *testing.T) {
	reply := `You asked: "This task was stopped, and Márcio picked it back up"`
	for n := 1; n <= 9; n++ {
		got := chunks(reply, n)
		for _, c := range got {
			if !utf8.ValidString(c) {
				t.Fatalf("chunks(%d) cut a character: %q", n, c)
			}
			if utf8.RuneCountInString(c) > n {
				t.Errorf("chunks(%d) gave %d characters: %q", n, utf8.RuneCountInString(c), c)
			}
		}
		if joined := strings.Join(got, ""); joined != reply {
			t.Errorf("chunks(%d) joined is %q", n, joined)
		}
	}
}
