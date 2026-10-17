package agenttools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

// firstKey is the first key of the JSON object out.
func firstKey(t *testing.T, out []byte) string {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(out))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		t.Fatalf("not an object: %v %v", tok, err)
	}
	tok, err := dec.Token()
	if err != nil {
		t.Fatal(err)
	}
	key, _ := tok.(string)
	return key
}

// Every read's result begins with its note, whole or cut to the budget,
// so a harness keeping only a result's head keeps what says it is a read.
func TestEveryReadResultBeginsWithItsNote(t *testing.T) {
	w := wrapper{id: "first"}
	long := strings.Repeat("Retries count twice. ", 400)
	big := forge.IssueRead{IssueLine: forge.IssueLine{Number: 7, Title: "t"}, Body: long}
	for range 50 {
		big.Comments = append(big.Comments, forge.IssueComment{Author: "bo", Body: long})
	}
	for _, tc := range []struct {
		name string
		v    map[string]any
	}{
		{"search", w.search([]forge.IssueLine{{Number: 1, Title: "a"}}, 1)},
		{"issue", w.issue(forge.IssueRead{IssueLine: forge.IssueLine{Number: 7, Title: "t"}, Body: "b"})},
		{"pull request", w.pullRequest(forge.PullRequestRead{IssueLine: forge.IssueLine{Number: 8}, Body: "b"}, nil)},
		{"issue past the budget", w.issue(big)},
		{"pull request past the budget", w.pullRequest(threadShape(), nil)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := w.fit(tc.v)
			if err != nil || !json.Valid(out) {
				t.Fatalf("fit: %v", err)
			}
			if k := firstKey(t, out); k != "external" {
				t.Fatalf("first key %q, want external: %.120s", k, out)
			}
			if !delivery.CarriesGitHubRead(string(out[:min(len(out), 60)])) {
				t.Fatalf("a 60-byte prefix is not known for a read: %.60s", out)
			}
		})
	}
}

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
	marshalResult = func(v map[string]any) ([]byte, error) { calls++; return marshal(v) }
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

// balancedQuotes says every quote in v opens and closes under id.
func balancedQuotes(v any, id string) bool {
	switch x := v.(type) {
	case string:
		return strings.Count(x, "⟦external "+id+"⟧") == strings.Count(x, "⟦/external "+id+"⟧")
	case []any:
		for _, item := range x {
			if !balancedQuotes(item, id) {
				return false
			}
		}
	case map[string]any:
		for _, item := range x {
			if !balancedQuotes(item, id) {
				return false
			}
		}
	}
	return true
}

// Past the budget, what says what the result is — the note on its quotes,
// the issue or pull request's identity, and the flags saying what is
// missing — is kept whole, and the bodies and lists give way.
func TestFitKeepsIdentityNoteAndFlagsBeforeBodies(t *testing.T) {
	w := wrapper{id: "order"}
	long := strings.Repeat("Retries count twice. ", 400)
	issue := forge.IssueRead{IssueLine: forge.IssueLine{Number: 7, Title: "Rollup counts twice", Author: "ana", State: "open",
		URL: "https://github.test/acme/target/issues/7"}, Body: long, CommentCount: 80}
	for range 50 {
		issue.Comments = append(issue.Comments, forge.IssueComment{Author: "bo", Body: long})
	}
	pr := forge.PullRequestRead{IssueLine: forge.IssueLine{Number: 8, Title: "Count once", Author: "ana", State: "draft",
		URL: "https://github.test/acme/target/pull/8"}, Body: long, Draft: true, Head: "feature/once", Base: "main",
		ReviewsCut: true, ThreadsUnknown: true, ChecksCut: true}
	for range 50 {
		pr.Reviews = append(pr.Reviews, forge.ReviewRead{Author: "bo", State: "COMMENTED", Body: long})
	}
	for range 300 {
		pr.Files = append(pr.Files, forge.FileRead{Path: strings.Repeat("dir/", 40) + "file.go"})
	}
	for _, tc := range []struct {
		name string
		v    map[string]any
		kept []string
	}{
		{"issue", w.issue(issue), []string{"external", "issue", "moreComments"}},
		{"pull request", w.pullRequest(pr, []string{"missing.go"}),
			[]string{"external", "pullRequest", "draft", "head", "base", "moreReviews", "threadsUnknown", "moreChecks", "moreFiles", "notListed"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			whole, _ := json.Marshal(tc.v)
			if len(whole) <= resultCap {
				t.Fatalf("the shape fits already: %d bytes", len(whole))
			}
			var before map[string]any
			_ = json.Unmarshal(whole, &before)
			out, err := w.fit(tc.v)
			if err != nil || len(out) > resultCap || !json.Valid(out) {
				t.Fatalf("fit: %d bytes, err %v", len(out), err)
			}
			var after map[string]any
			_ = json.Unmarshal(out, &after)
			for _, k := range tc.kept {
				b, _ := json.Marshal(before[k])
				a, _ := json.Marshal(after[k])
				if string(a) != string(b) {
					t.Errorf("%s changed in the cut:\n got %.200s\nwant %.200s", k, a, b)
				}
			}
			if cut, _ := after["cut"].(string); !strings.HasPrefix(cut, cutMark) {
				t.Errorf("no cut note: %v", after["cut"])
			}
			if !balancedQuotes(after, w.id) {
				t.Error("a quote lost its end")
			}
		})
	}
}

// GitHub text with invalid UTF-8 reaches the agent as valid UTF-8, each
// invalid sequence one U+FFFD, cut on a whole character and counted as
// what is shown.
func TestInvalidUTF8FromGitHubIsReplacedBeforeTheCut(t *testing.T) {
	w := wrapper{id: "utf8"}
	for _, tc := range []struct {
		name, text string
		limit      int
		want       string
	}{
		{"malformed leading byte", "\xffab", 100, "\uFFFDab"},
		{"truncated sequence", "ab\xe7\x95", 100, "ab\uFFFD"},
		{"interior invalid sequence", "a\xc0\xafb", 100, "a\uFFFDb"},
		{"cut after a malformed leading byte", "\xffabcdef", 4, "\uFFFDa"},
		{"cut through a replaced sequence", "ab\xe7\x95cdef", 4, "ab"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := w.text(tc.text, tc.limit)
			inner, _, _ := strings.Cut(strings.TrimPrefix(got, "⟦external utf8⟧"), "⟦/external utf8⟧")
			if !utf8.ValidString(got) || inner != tc.want {
				t.Fatalf("text(%q, %d) = %q, want %q inside, valid UTF-8", tc.text, tc.limit, got, tc.want)
			}
			normalised := strings.ToValidUTF8(tc.text, "\uFFFD")
			if len(normalised) > tc.limit && !strings.HasSuffix(got, fmt.Sprintf("%s: %d of %d bytes", cutMark, len(tc.want), len(normalised))) {
				t.Errorf("cut note: %q", got)
			}
		})
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
