package fakelux

import "testing"

// An agent's notes are Markdown on every host the tests run on, not only
// where the host's MIME table happens to know .md.
func TestMarkdownIsTypedAsMarkdownWhateverTheHost(t *testing.T) {
	for _, name := range []string{"NOTES.md", "docs/plan.markdown"} {
		if got := mimeFor(name); got != "text/markdown; charset=utf-8" {
			t.Errorf("mimeFor(%q) = %q", name, got)
		}
	}
	if got := mimeFor("blob.unknownext"); got != "application/octet-stream" {
		t.Errorf("unknown extension = %q", got)
	}
}
