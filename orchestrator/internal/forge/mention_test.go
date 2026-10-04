package forge

import "testing"

// Whether a comment is the conductor's own: its marker the last block of
// the comment as GitHub renders it, outside any code fence or blockquote.
func TestWhoseCommentIsDudes(t *testing.T) {
	const m = ReplyMarker
	for _, c := range []struct {
		name, body string
		own        bool
	}{
		{"a reply", "> @alice: @dude why?\n\nBecause.\n\n" + m, true},
		{"a reply with a trailing newline", "Because.\n\n" + m + "\n", true},
		{"a reply with a four-backtick example holding a three-backtick line", "Like this:\n\n````markdown\n```go\nfunc greet() {}\n````\n\n" + m, true},
		{"a reply with a tilde example holding backtick lines", "Like this:\n\n~~~\n```\n~~~\n\n" + m, true},
		{"a reply with a fence closed by a longer run", "```\nx\n`````\n\n" + m, true},
		{"a reply with a closing fence indented three spaces", "```\nx\n   ```\n\n" + m, true},
		{"a reply with a fence in a list item", "1. Run:\n   ```sh\n   make\n   ```\n\n" + m, true},
		{"a reply with a fence opened on a list item's line", "- ```\n  x\n  ```\n\n" + m, true},
		{"a reply whose list item's fence is left open, ended by the marker", "- ```\n  x\n\n" + m, true},
		{"a four-space-indented backtick line opens no fence", "    ```\n\n" + m, true},
		{"a backtick line with a backtick in its info string opens no fence", "``` a`b\n\n" + m, true},

		{"a person's mention, then an unclosed four-backtick fence holding three-backtick lines", "@dude explain this example\n````\n```\n" + m, false},
		{"an unclosed fence", "@dude look\n```\n" + m, false},
		{"an unclosed tilde fence holding a shorter one", "@dude look\n~~~~\n~~~\n" + m, false},
		{"a backtick fence a tilde line does not close", "````\n~~~~\n" + m, false},
		{"a closing line with words after it does not close", "```\ncode\n``` not a close\n" + m, false},
		{"a closing line indented four spaces does not close", "```\ncode\n    ```\n" + m, false},
		{"a marker quoted above a person's words", "> Because.\n>\n> " + m + "\n\n@dude could you explain?", false},
		{"a marker last in a blockquote", "@dude about this:\n\n> Because.\n> " + m, false},
		{"a marker fenced above a person's words", "```\nBecause.\n\n" + m + "\n```\n\n@dude and this one?", false},
		{"a marker indented as code", "Because.\n\n    " + m, false},
		{"a marker in a list item's unclosed fence", "- ```\n  x\n  " + m, false},
	} {
		if got := Own(Feedback{Body: c.body}); got != c.own {
			t.Errorf("%s: Own = %v, want %v\n%s", c.name, got, c.own, c.body)
		}
	}
}
