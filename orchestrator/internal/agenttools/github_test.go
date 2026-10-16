package agenttools

import (
	"encoding/json"
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

// A cut check list says so, beside the checks it has; a whole one does not.
func TestCutChecksSaySo(t *testing.T) {
	w := wrapper{id: "checks"}
	p := forge.PullRequestRead{Checks: []forge.Check{{Name: "ci", Status: "completed", Conclusion: "success"}}, ChecksCut: true}
	out := w.pullRequest(p, nil)
	if out["moreChecks"] != cutMark+": check runs incomplete" || len(out["checks"].([]ghCheckOut)) != 1 {
		t.Fatalf("cut checks: moreChecks=%v checks=%v", out["moreChecks"], out["checks"])
	}
	p.ChecksCut = false
	if _, ok := w.pullRequest(p, nil)["moreChecks"]; ok {
		t.Fatal("a whole check list said it was cut")
	}
}

func TestGitHubAuthorsAreExternalText(t *testing.T) {
	w := wrapper{id: "authors"}
	if got := w.line(forge.IssueLine{Author: "ana"}).Author; !strings.Contains(got, "⟦external authors⟧ana⟦/external authors⟧") {
		t.Fatalf("search author unwrapped: %q", got)
	}
	if got := w.comments([]forge.IssueComment{{Author: "bo"}})[0].Author; !strings.Contains(got, "⟦external authors⟧bo⟦/external authors⟧") {
		t.Fatalf("comment author unwrapped: %q", got)
	}
	out := w.pullRequest(forge.PullRequestRead{Reviews: []forge.ReviewRead{{Author: "cy"}}}, nil)
	if got := out["reviews"].([]ghReviewOut)[0].Author; !strings.Contains(got, "⟦external authors⟧cy⟦/external authors⟧") {
		t.Fatalf("review author unwrapped: %q", got)
	}
}

func threadShape() forge.PullRequestRead {
	p := forge.PullRequestRead{}
	for range 250 {
		t := forge.ThreadRead{Path: "thread.go"}
		for range 20 {
			t.Comments = append(t.Comments, forge.IssueComment{Author: "ana", Body: strings.Repeat("界", 2700)})
		}
		p.Threads = append(p.Threads, t)
	}
	return p
}

func TestFitBatchesLargeThreadShape(t *testing.T) {
	w := wrapper{id: "budget"}
	calls := 0
	marshal := marshalResult
	marshalResult = func(v any) ([]byte, error) { calls++; return marshal(v) }
	t.Cleanup(func() { marshalResult = marshal })
	out, err := w.fit(w.pullRequest(threadShape(), nil))
	if err != nil || len(out) > resultCap || calls > 2 || !json.Valid(out) || !strings.Contains(string(out), cutMark) {
		t.Fatalf("fit bytes=%d marshals=%d err=%v", len(out), calls, err)
	}
	var result map[string]any
	_ = json.Unmarshal(out, &result)
	threads, _ := result["unresolvedThreads"].([]any)
	if len(threads) == 0 {
		t.Fatal("budget kept no thread")
	}
	body := threads[0].(map[string]any)["comments"].([]any)[0].(map[string]any)["body"].(string)
	if !strings.HasPrefix(body, "⟦external budget⟧") || !strings.Contains(body, "⟦/external budget⟧") {
		t.Fatalf("external quote not whole: %.80q", body)
	}
}

func BenchmarkFitThreadShape(b *testing.B) {
	w := wrapper{id: "budget"}
	p := threadShape()
	b.ResetTimer()
	for range b.N {
		if _, err := w.fit(w.pullRequest(p, nil)); err != nil {
			b.Fatal(err)
		}
	}
}

func TestCutBytesKeepsWholeUTF8Prefix(t *testing.T) {
	for _, tc := range []struct {
		text        string
		limit, want int
	}{{strings.Repeat("界", 3000), 8192, 8190}, {strings.Repeat("𐀀", 3000), 8191, 8188}, {strings.Repeat("𐀀", 3000), 8192, 8192}} {
		got := cutBytes(tc.text, tc.limit)
		if len(got) != tc.want || !utf8.ValidString(got) {
			t.Errorf("cut to %d: got %d valid=%v, want %d", tc.limit, len(got), utf8.ValidString(got), tc.want)
		}
	}
}
