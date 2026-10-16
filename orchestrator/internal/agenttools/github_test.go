package agenttools

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

func TestCappedFileListingDoesNotClaimTailUnchanged(t *testing.T) {
	p := forge.PullRequestRead{}
	for range 300 {
		p.Files = append(p.Files, forge.FileRead{Path: "listed.go"})
	}
	out := (wrapper{id: "test"}).pullRequest(p, []string{"file301.go"})
	if _, ok := out["notChanged"]; ok {
		t.Fatalf("capped listing claimed notChanged: %v", out["notChanged"])
	}
	if _, ok := out["notListed"]; !ok {
		t.Fatal("missing notListed diagnostic")
	}
	p.Files = p.Files[:1]
	out = (wrapper{id: "test"}).pullRequest(p, []string{"file301.go"})
	if _, ok := out["notChanged"]; !ok {
		t.Fatal("complete listing must identify unchanged paths")
	}
}

func TestCutBytesKeepsWholeUTF8Prefix(t *testing.T) {
	for _, tc := range []struct {
		text string
		limit, want int
	}{{strings.Repeat("界", 3000), 8192, 8190}, {strings.Repeat("𐀀", 3000), 8191, 8188}, {strings.Repeat("𐀀", 3000), 8192, 8192}} {
		got := cutBytes(tc.text, tc.limit)
		if len(got) != tc.want || !utf8.ValidString(got) {
			t.Errorf("cut to %d: got %d valid=%v, want %d", tc.limit, len(got), utf8.ValidString(got), tc.want)
		}
	}
}
