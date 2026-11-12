package agenttools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/redact"
)

// checkLogForge is a forge reading check DefaultCheckRunID, an Actions job whose log is
// log, from a fake GitHub that redirects downloads to another host.
func checkLogForge(t *testing.T, log, secret string) (*fakegithub.Server, *forge.GitHub) {
	t.Helper()
	fake := fakegithub.New("", "acme/target")
	downloads := httptest.NewServer(fake.LogDownloads())
	t.Cleanup(downloads.Close)
	api := httptest.NewServer(fake.Handler())
	t.Cleanup(api.Close)
	fake.Set(func(s *fakegithub.Server) {
		s.CheckRuns = map[int64]fakegithub.CheckRun{fakegithub.DefaultCheckRunID: fakegithub.ActionsJob()}
	})
	fake.SetJobLog(fakegithub.DefaultCheckRunID, log, downloads.URL)
	return fake, forge.NewGitHub(forge.Credential{Auth: "pat", Secret: secret, APIBaseURL: api.URL})
}

// The real HTTP forge read and agent wrapper must keep a middle timeout,
// its running test names, the package FAIL and final exit, not just cleanup.
func TestGitHubCheckLogHTTPKeepsMiddleFailureWithinBudget(t *testing.T) {
	const budget = 16380 // readOnGitHub reserves len("…\n") from 16 KiB.
	input := "##[error]early wrapper error\n" + strings.Repeat("setup noise 界\n", 4000) +
		"[lane2] panic: test timed out after 10m0s\n[lane2] running tests:\n" +
		"[lane2]     TestBlocked (9m59s)\n[lane2]     TestWaiting (9m58s)\n" +
		strings.Repeat("goroutine noise\n", 4000) + "FAIL example/service 600s\n" +
		"##[error]Process completed with exit code 2.\n" + strings.Repeat("cleanup noise\n", 4000)
	fake, gh := checkLogForge(t, input, "ghp_check_log_"+t.Name())
	read, err := gh.CheckLog(context.Background(), "acme/target", fakegithub.DefaultCheckRunID, budget)
	if err != nil {
		t.Fatal(err)
	}
	if read.Source != "log" || !read.Actions || len(read.Text) > budget || !utf8.ValidString(read.Text) {
		t.Fatalf("source=%q actions=%v bytes=%d valid=%v", read.Source, read.Actions, len(read.Text), utf8.ValidString(read.Text))
	}
	w := wrapper{id: "check-log"}
	out, err := w.fit(w.checkLog(read))
	if err != nil || len(out) > resultCap || !json.Valid(out) || !utf8.Valid(out) {
		t.Fatalf("wrapper: bytes=%d err=%v", len(out), err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if firstKey(t, out) != "external" || !balancedQuotes(got, w.id) || got["source"] != "log" || got["actions"] != true {
		t.Fatalf("lost external note, quote or source metadata: %s", out)
	}
	text, _ := got["log"].(string)
	if text != delivery.ExternalText(w.id, read.Text) {
		t.Fatal("wrapper changed or truncated the bounded forge excerpt")
	}
	for _, want := range []string{"panic: test timed out", "running tests:", "TestBlocked", "TestWaiting", "FAIL example/service", "Process completed with exit code 2.", "…"} {
		if !strings.Contains(text, want) {
			t.Errorf("excerpt lost %q", want)
		}
	}
	if auth := fake.DownloadAuthorization(); len(auth) != 1 || auth[0] != "" {
		t.Errorf("download Authorization = %q", auth)
	}
}

// Compiler diagnostics without an "error" keyword must survive the real HTTP
// read and agent wrapper, even when progress pushes them out of the log's tail.
func TestGitHubCheckLogHTTPKeepsGoCompilerDiagnostic(t *testing.T) {
	const diagnostic = "main.go:7:2: undefined: Widget"
	const exit = "##[error]Process completed with exit code 1."
	input := strings.Repeat("setup noise 界\n", 4000) + diagnostic + "\n" +
		strings.Repeat("progress "+strings.Repeat("界", 80)+"\n", 100) + exit
	_, gh := checkLogForge(t, input, "ghp_check_log_"+t.Name())
	read, err := gh.CheckLog(context.Background(), "acme/target", fakegithub.DefaultCheckRunID, 16380)
	if err != nil {
		t.Fatal(err)
	}
	if len(read.Text) > 16380 || !utf8.ValidString(read.Text) {
		t.Fatalf("excerpt: bytes=%d valid=%v", len(read.Text), utf8.ValidString(read.Text))
	}
	w := wrapper{id: "compiler-check"}
	out, err := w.fit(w.checkLog(read))
	if err != nil || len(out) > resultCap || !json.Valid(out) || !utf8.Valid(out) {
		t.Fatalf("wrapper: bytes=%d err=%v", len(out), err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if firstKey(t, out) != "external" || !balancedQuotes(got, w.id) || got["source"] != "log" || got["actions"] != true {
		t.Fatalf("lost external note, quote or source metadata: %s", out)
	}
	text, _ := got["log"].(string)
	if text != delivery.ExternalText(w.id, forge.FailureExcerpt(read.Text, 10<<10)) {
		t.Fatal("wrapper changed the failure excerpt of the bounded forge read")
	}
	for _, want := range []string{diagnostic, exit, "… [lines omitted]"} {
		if !strings.Contains(text, want) {
			t.Errorf("excerpt lost %q", want)
		}
	}
}

// Escape-heavy logs must fit after JSON encoding, not only before wrapping.
func TestGitHubCheckLogHTTPEscapedEnvelope(t *testing.T) {
	for _, unit := range []string{"<", ">", "&", "\x00", "\x01", "\"", "\\"} {
		t.Run(fmt.Sprintf("%q", unit), func(t *testing.T) {
			input := "##[error]early wrapper error\n" + strings.Repeat(strings.Repeat(unit, 4000)+"\n", 4) +
				strings.Repeat("setup noise\n", 4000) +
				"[lane2] panic: test timed out after 10m0s\n[lane2] running tests:\n" +
				"[lane2]     TestBlocked (9m59s)\n[lane2]     TestWaiting (9m58s)\n" +
				strings.Repeat("goroutine noise\n", 4000) + "FAIL example/service 600s\n" +
				"##[error]Process completed with exit code 2.\n" + strings.Repeat("cleanup noise\n", 4000)
			fake, gh := checkLogForge(t, input, "local-fixture-token")
			read, err := gh.CheckLog(context.Background(), "acme/target", fakegithub.DefaultCheckRunID, 16380)
			if err != nil {
				t.Fatal(err)
			}
			if len(read.Text) > 16380 {
				t.Fatalf("forge bytes=%d", len(read.Text))
			}
			w := wrapper{id: "check-log"}
			raw, err := w.fit(w.checkLog(read))
			t.Logf("forge bytes=%d serialized bytes=%d", len(read.Text), len(raw))
			if err != nil || len(raw) > resultCap || !json.Valid(raw) || !utf8.Valid(raw) {
				t.Fatalf("envelope bytes=%d cap=%d err=%v", len(raw), resultCap, err)
			}
			var got map[string]any
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatal(err)
			}
			if firstKey(t, raw) != "external" || !balancedQuotes(got, w.id) || got["source"] != "log" || got["actions"] != true {
				t.Fatal("lost external boundary or source metadata")
			}
			text, _ := got["log"].(string)
			for _, want := range []string{"panic: test timed out", "running tests:", "TestBlocked", "TestWaiting", "FAIL example/service", "Process completed with exit code 2."} {
				if !strings.Contains(text, want) {
					t.Errorf("excerpt lost %q", want)
				}
			}
			if auth := fake.DownloadAuthorization(); len(auth) != 1 || auth[0] != "" {
				t.Errorf("download Authorization = %q", auth)
			}
		})
	}
}

func TestGitHubCheckLogShortAndUnreadUnchanged(t *testing.T) {
	for _, read := range []forge.CheckLogRead{
		{},
		{Source: "log", Actions: true, Text: "ordinary log\n"},
		{Source: "check_run", Actions: true, Text: "annotation\n", Unread: "GitHub refused: no Actions: Read permission"},
	} {
		w := wrapper{id: "check-log"}
		raw, err := w.fit(w.checkLog(read))
		if err != nil {
			t.Fatal(err)
		}
		var got map[string]any
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		if got["source"] != read.Source || got["actions"] != read.Actions || got["log"] != w.text(read.Text, 16<<10) {
			t.Fatal("changed short log or metadata")
		}
		if unread, present := got["unread"]; present != (read.Unread != "") || present && unread != read.Unread {
			t.Fatal("changed unread metadata")
		}
	}
}

func TestGitHubCheckLogMaximumEscaping(t *testing.T) {
	w := wrapper{id: "check-log"}
	raw, err := w.fit(w.checkLog(forge.CheckLogRead{Source: "log", Actions: true, Text: strings.Repeat("<", 16380)}))
	t.Logf("serialized bytes=%d", len(raw))
	if err != nil || len(raw) > resultCap || !json.Valid(raw) {
		t.Fatalf("envelope bytes=%d cap=%d err=%v", len(raw), resultCap, err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if !balancedQuotes(got, w.id) {
		t.Fatal("lost external boundary")
	}
}

// This is only a caller-supplied scrub-before-excerpt seam, not a claim
// that github_check_log loads secrets or redacts its returned log. Neither
// forge.CheckLog nor wrapper.checkLog does that; Server.call redacts args.
func TestGitHubCheckLogWithCallerSuppliedRedaction(t *testing.T) {
	const secret = "known-check-log-secret/123+&"
	red := redact.New(map[string]string{"CI_TOKEN": secret})
	// Scrub intact known values before any lossy selection. Exact-value
	// redaction after a cut cannot recognize a partial secret.
	input := strings.Repeat("passing package\n", 1000) + "panic: " + secret + "\nexit status 2\n" + strings.Repeat("cleanup\n", 1000)
	read := forge.CheckLogRead{Source: "log", Actions: true, Text: forge.FailureExcerpt(red.String(input), 4000)}
	w := wrapper{id: "redacted-check"}
	raw, err := w.fit(w.checkLog(read))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	text, _ := got["log"].(string)
	if strings.Contains(text, secret) || !strings.Contains(text, "[REDACTED:CI_TOKEN]") ||
		!strings.Contains(text, "exit status 2") || !balancedQuotes(got, w.id) {
		t.Fatalf("caller redaction lost the marker, evidence or quote: %s", raw)
	}
}

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

// A pull request's conversation comments reach the agent as external text,
// and a conversation longer than the read keeps says it holds the newest.
func TestAPullRequestReadCarriesItsConversation(t *testing.T) {
	w := wrapper{id: "talk"}
	p := forge.PullRequestRead{CommentCount: 1, Comments: []forge.IssueComment{{Author: "dee", Body: "but the metric name is wrong",
		CreatedAt: "2026-10-19T09:00:00Z"}}}
	out := w.pullRequest(p, nil)
	comments, _ := out["comments"].([]ghCommentOut)
	if len(comments) != 1 || comments[0].Body != "⟦external talk⟧but the metric name is wrong⟦/external talk⟧" ||
		comments[0].Author != "⟦external talk⟧dee⟦/external talk⟧" || comments[0].CreatedAt != "2026-10-19T09:00:00Z" {
		t.Fatalf("comments %+v", out["comments"])
	}
	if _, ok := out["moreComments"]; ok {
		t.Fatalf("a whole conversation said it was cut: %v", out["moreComments"])
	}
	p.CommentCount, p.Comments = 73, make([]forge.IssueComment, commentsMax)
	if got := w.pullRequest(p, nil)["moreComments"]; got != cutMark+": the newest 50 of 73 comments" {
		t.Fatalf("moreComments %q", got)
	}
	if got := w.pullRequest(forge.PullRequestRead{}, nil)["comments"]; got == nil || len(got.([]ghCommentOut)) != 0 {
		t.Fatalf("no conversation: comments %#v, want an empty list", got)
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
	pr.CommentCount = 80
	for range 50 {
		pr.Comments = append(pr.Comments, forge.IssueComment{Author: "dee", Body: long})
	}
	for _, tc := range []struct {
		name string
		v    map[string]any
		kept []string
		// What gives way: the lists of bodies, below their size.
		shortened []string
	}{
		{"issue", w.issue(issue), []string{"external", "issue", "moreComments"}, []string{"comments"}},
		{"pull request", w.pullRequest(pr, []string{"missing.go"}),
			[]string{"external", "pullRequest", "draft", "head", "base", "moreReviews", "threadsUnknown", "moreChecks", "moreFiles", "notListed",
				"moreComments"},
			[]string{"comments", "reviews", "files"}},
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
			// The lists gave way: each is shorter than it was, and none is gone.
			for _, k := range tc.shortened {
				b, _ := json.Marshal(before[k])
				a, _ := json.Marshal(after[k])
				if _, ok := after[k]; !ok || len(a) >= len(b) {
					t.Errorf("%s not shortened: %d bytes of %d", k, len(a), len(b))
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

// A run whose jobs GitHub failed to answer for is listed, its jobs none,
// and says how to read them; a cut list says it was cut.
func TestABranchRunWithUnreadJobsSaysSo(t *testing.T) {
	out := newWrapper().branchRuns(githubRead{Slug: "acme/api", Branch: "main"}, []forge.BranchRun{
		{ID: 2, JobsUnread: true},
		{ID: 1, Jobs: []forge.BranchJob{{ID: 11, Name: "unit"}}, TotalJobs: 3},
	})
	runs := out["runs"].([]ghBranchRunOut)
	if len(runs) != 2 || len(runs[0].Jobs) != 0 || !strings.Contains(runs[0].More, "github_run_jobs") ||
		!strings.Contains(runs[1].More, "the first 1 of 3 jobs") {
		t.Fatalf("runs %+v", runs)
	}
}
