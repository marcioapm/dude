package forge

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
)

// redshiftLog is ABS-44's shape: a passing Actions job of thousands of
// lines, GitHub's timestamps and colour on each, the two PASSED lines and
// the pytest summary in the middle, setup noise before and cleanup after.
func redshiftLog() string {
	var b strings.Builder
	b.WriteString("\uFEFF")
	line := func(s string) { fmt.Fprintf(&b, "2026-11-27T03:1%d:01.1234567Z %s\n", b.Len()%10, s) }
	line("Current runner version: '2.329.0'")
	for i := range 1100 {
		line(fmt.Sprintf("Receiving objects: %d%% (%d/1100), done.", i%100, i))
	}
	line("\x1b[36;1mpython -m pytest tests/e2e/redshift -v\x1b[0m")
	for i := range 24 {
		line(fmt.Sprintf("tests/e2e/redshift/test_inserter.py::test_case_%02d \x1b[32mPASSED\x1b[0m [%d%%]", i, i*2))
	}
	line("tests/e2e/redshift/test_inserter.py::test_goal_description_replaces_nul_in_event_metadata \x1b[32mPASSED\x1b[0m [49%]")
	for i := 24; i < 49; i++ {
		line(fmt.Sprintf("tests/e2e/redshift/test_inserter.py::test_case_%02d \x1b[32mPASSED\x1b[0m [%d%%]", i, i*2))
	}
	line("tests/e2e/redshift/test_inserter.py::test_goal_with_nul_in_name_is_forwarded_without_creating_a_goal \x1b[32mPASSED\x1b[0m [98%]")
	line("tests/e2e/redshift/test_inserter.py::test_case_skip \x1b[33mSKIPPED\x1b[0m [100%]")
	line("\x1b[32m======================= 51 passed, 8 skipped in 412.03s =======================\x1b[0m")
	for i := range 1200 {
		line(fmt.Sprintf("Post job cleanup %d.", i))
	}
	return b.String()
}

func search(t *testing.T, expr string, context int) LogSearch {
	t.Helper()
	return LogSearch{Pattern: regexp.MustCompile("(?i)" + expr), Context: context}
}

// ABS-44: the lines saying the two NUL tests passed sit in the middle of a
// passing job's log, where the failure excerpt never looks. A search of
// the whole log finds them, with their line numbers, in log order, and
// the download host never sees the token.
func TestASearchFindsPassedLinesInTheMiddleOfAPassingJobsLog(t *testing.T) {
	log := redshiftLog()
	fake, gh := logWorld(t, log)
	read, err := gh.SearchCheckLog(context.Background(), "acme/target", fakegithub.DefaultCheckRunID,
		search(t, `test_goal.*nul.*PASSED|\d+ passed, \d+ skipped`, 0), 10<<10)
	if err != nil {
		t.Fatal(err)
	}
	s := read.Search
	if s == nil || read.Source != "log" || !read.Actions || read.Text != s.Text {
		t.Fatalf("not a search of the log: %+v", read)
	}
	lines := strings.Split(log, "\n")
	numberOf := func(part string) int {
		for i, l := range lines {
			if strings.Contains(l, part) {
				return i + 1
			}
		}
		t.Fatalf("fixture has no %q", part)
		return 0
	}
	want := fmt.Sprintf("%d: tests/e2e/redshift/test_inserter.py::test_goal_description_replaces_nul_in_event_metadata PASSED [49%%]\n%s\n"+
		"%d: tests/e2e/redshift/test_inserter.py::test_goal_with_nul_in_name_is_forwarded_without_creating_a_goal PASSED [98%%]\n%s\n"+
		"%d: ======================= 51 passed, 8 skipped in 412.03s =======================",
		numberOf("replaces_nul"), omissionMarker, numberOf("with_nul_in_name"), omissionMarker, numberOf("51 passed"))
	if s.Text != want || s.Total != 3 || s.Shown != 3 || s.Capped || s.Lines != len(lines)-1 {
		t.Fatalf("search:\n%s\ntotal=%d shown=%d lines=%d capped=%v\nwant:\n%s", s.Text, s.Total, s.Shown, s.Lines, s.Capped, want)
	}
	if auth := fake.DownloadAuthorization(); len(auth) != 1 || auth[0] != "" {
		t.Errorf("the download host was sent Authorization %q", auth)
	}
	// The failure excerpt of the same log does not have them: why match exists.
	tail, err := gh.CheckLog(context.Background(), "acme/target", fakegithub.DefaultCheckRunID, 10<<10)
	if err != nil || strings.Contains(tail.Text, "replaces_nul") || tail.Search != nil {
		t.Fatalf("the failure excerpt holds the middle, or is a search (%v): the fixture is not ABS-44's", err)
	}
}

// Context lines come before and after each match, shared between matches
// close together, never written twice; a match is case-insensitive.
func TestASearchShowsContextAroundEachMatch(t *testing.T) {
	log := "a\nb\nNEEDLE one\nc\nd\ne\nf\ng\nneedle two\nneedle three\nh\ni\nj\n"
	got, err := searchLog(strings.NewReader(log), search(t, "needle", 2), 10<<10)
	if err != nil {
		t.Fatal(err)
	}
	want := "1: a\n2: b\n3: NEEDLE one\n4: c\n5: d\n" + omissionMarker + "\n7: f\n8: g\n9: needle two\n10: needle three\n11: h\n12: i"
	if got.Text != want || got.Total != 3 || got.Shown != 3 || got.Lines != 13 {
		t.Fatalf("got:\n%s\n%+v\nwant:\n%s", got.Text, got, want)
	}
}

// A match whose context does not fit is shown alone, and the lines it
// skipped are still marked before the next match that does fit.
func TestASearchWhoseContextDoesNotFitStillMarksTheGap(t *testing.T) {
	log := "M one\n" + strings.Repeat("y", 80) + "\nM two\n"
	got, err := searchLog(strings.NewReader(log), search(t, "M", 1), 50)
	if err != nil {
		t.Fatal(err)
	}
	if want := "1: M one\n" + omissionMarker + "\n3: M two"; got.Text != want {
		t.Fatalf("got %q, want %q", got.Text, want)
	}
}

// More matches than the budget holds: as many as fit are shown, whole, in
// log order, and the result says how many there were in all.
func TestASearchPastItsBudgetSaysShownOfTotal(t *testing.T) {
	var b strings.Builder
	for i := range 5000 {
		fmt.Fprintf(&b, "tests/test_x.py::test_%04d PASSED\nnoise %d\n", i, i)
	}
	got, err := searchLog(strings.NewReader(b.String()), search(t, "PASSED", 1), 2000)
	if err != nil {
		t.Fatal(err)
	}
	if got.Total != 5000 || got.Shown == 0 || got.Shown >= got.Total || len(got.Text) > 2000 || got.Lines != 10000 {
		t.Fatalf("total=%d shown=%d bytes=%d lines=%d", got.Total, got.Shown, len(got.Text), got.Lines)
	}
	if n := strings.Count(got.Text, "PASSED"); n != got.Shown || !strings.HasPrefix(got.Text, "1: tests/test_x.py::test_0000 PASSED\n2: noise 0") {
		t.Fatalf("shown %d, %d in text:\n%s", got.Shown, n, got.Text)
	}
}

// No line matching is a search like any other: nothing shown, said so by
// its counts, and the whole log read.
func TestASearchMatchingNothingSaysSo(t *testing.T) {
	got, err := searchLog(strings.NewReader(redshiftLog()), search(t, "test_never_ran", 2), 10<<10)
	if err != nil || got.Text != "" || got.Total != 0 || got.Shown != 0 || got.Lines < 2000 {
		t.Fatalf("%+v %v", got, err)
	}
}

// A search reads the log to logReadMax, as the excerpt does, saying the
// read stopped there; a line past logLineMax is cut, saying so.
func TestASearchOfAHugeLogStopsAtTheCap(t *testing.T) {
	line := strings.Repeat("x", 120) + "\n"
	huge := &countingReader{r: io.MultiReader(strings.NewReader("needle first\n"), io.LimitReader(repeatReader(line), 64<<20))}
	got, err := searchLog(huge, search(t, "needle", 0), 4000)
	if err != nil || huge.n != logReadMax || !got.Capped || got.Total != 1 || got.Text != "1: needle first" {
		t.Fatalf("read %d, %+v %v", huge.n, got, err)
	}
	long := strings.Repeat("y", 64<<10) + " needle\n"
	got, _ = searchLog(strings.NewReader("needle\n"+long), search(t, "y", 0), 10<<10)
	if got.Total != 1 || got.Text != "2: "+strings.Repeat("y", logLineMax-len(lineCapMarker))+lineCapMarker {
		t.Fatalf("long line: %+v", got)
	}
}

// A check that is not an Actions job has no log to search: it is read as
// CheckLog reads it, no search; a log GitHub will not give says why.
func TestASearchOfACheckWithoutALogIsItsCheckRun(t *testing.T) {
	fake, gh := logWorld(t, redshiftLog())
	fake.Set(func(s *fakegithub.Server) { s.LogStatus = http.StatusGone })
	read, err := gh.SearchCheckLog(context.Background(), "acme/target", fakegithub.DefaultCheckRunID, search(t, "PASSED", 0), 10<<10)
	if err != nil || read.Search != nil || read.Source != "check_run" || read.Unread != "the log has expired" {
		t.Fatalf("%+v %v", read, err)
	}
}

// A search is an agent's read: once delivery's share of the rate limit is
// all that is left, it is refused before any request, as every agent read.
func TestASearchLeavesDeliveryItsShareOfTheRateLimit(t *testing.T) {
	fake, gh := logWorld(t, redshiftLog())
	ctx := context.Background()
	fake.Set(func(s *fakegithub.Server) { s.RateRemaining = map[string]int{"core": 999} })
	// The read that learns 999 are left is made; its next request is not.
	if _, err := gh.CheckLog(ctx, "acme/target", fakegithub.DefaultCheckRunID, 10<<10); err != nil && !RateLimited(err) {
		t.Fatalf("the read that learns 999 are left: %v", err)
	}
	if len(fake.Requests()) == 0 {
		t.Fatal("no read learnt the rate")
	}
	before, downloads := len(fake.Requests()), len(fake.DownloadAuthorization())
	_, err := gh.SearchCheckLog(ctx, "acme/target", fakegithub.DefaultCheckRunID, search(t, "PASSED", 0), 10<<10)
	// readBudget's refusal, which names the reading, not a request's own
	// (reserveAgentRequest's "GitHub's rate limit is kept for delivery").
	if !RateLimited(err) || RetryAfter(err) <= 0 || !strings.Contains(err.Error(), "GitHub's core rate limit has ") ||
		!strings.Contains(err.Error(), "requests left until") {
		t.Fatalf("with 999 left: %v", err)
	}
	if after := len(fake.Requests()); after != before || len(fake.DownloadAuthorization()) != downloads {
		t.Errorf("a refused search reached GitHub: %v", fake.Requests()[before:])
	}
}

// An Actions job whose log is empty has nothing to search: it is read as
// its check run, saying the log is empty, not as a search matching nothing.
func TestASearchOfAnEmptyLogIsItsCheckRun(t *testing.T) {
	fake, gh := logWorld(t, "unused")
	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	t.Cleanup(empty.Close)
	fake.SetJobLog(fakegithub.DefaultCheckRunID, "unused", empty.URL)
	read, err := gh.SearchCheckLog(context.Background(), "acme/target", fakegithub.DefaultCheckRunID, search(t, "PASSED", 0), 10<<10)
	if err != nil || read.Search != nil || read.Unread != "the log is empty" || read.Source != "check_run" || !read.Actions {
		t.Fatalf("%+v %v", read, err)
	}
}
