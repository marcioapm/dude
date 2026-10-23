package forge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
)

// jobLog is a GitHub Actions job's log as GitHub serves it: a byte-order
// mark, a timestamp on every line, colour codes, the runner's setup, a
// pytest FAILURES block, the ##[error] line, then the post-job cleanup.
func jobLog(setupLines int) string {
	var b strings.Builder
	b.WriteString("\uFEFF")
	line := func(s string) { fmt.Fprintf(&b, "2026-10-16T19:4%d:01.1234567Z %s\n", b.Len()%10, s) }
	line("Current runner version: '2.329.0'")
	line("##[group]Run actions/checkout@v4")
	for i := range setupLines {
		line(fmt.Sprintf("Receiving objects: %d%% (%d/%d), done.", i%100, i, setupLines))
	}
	line("##[endgroup]")
	line("\x1b[36;1mpython -m pytest tests/\x1b[0m")
	line("============================= test session starts ==============================")
	line("collected 41 items")
	line("tests/test_backup.py ..F......                                                [100%]")
	line("=================================== FAILURES ===================================")
	line("\x1b[31m\x1b[1m____________________________ test_backup_counts ____________________________\x1b[0m")
	line("    def test_backup_counts(self):")
	line(">       self.assertEqual(len(rows), 2)")
	line("\x1b[1m\x1b[31mE       AssertionError: 3 != 2\x1b[0m")
	line("tests/test_backup.py:57: AssertionError")
	line("=========================== short test summary info ============================")
	line("FAILED tests/test_backup.py::BackupTest::test_backup_counts - AssertionError: 3 != 2")
	line("========================= 1 failed, 40 passed in 12.31s ========================")
	line("##[error]Process completed with exit code 1.")
	line("Post job cleanup.")
	line("[command]/usr/bin/git version")
	line("Cleaning up orphan processes")
	return b.String()
}

// logWorld is a fake GitHub whose job 77 is an Actions job that failed by
// its exit code, with log, and the download host its logs redirect to: on
// the same address, another port, as a different host by URL.
func logWorld(t *testing.T, log string) (*fakegithub.Server, *GitHub) {
	t.Helper()
	fake := fakegithub.New("", "acme/target")
	downloads := httptest.NewServer(fake.LogDownloads())
	t.Cleanup(downloads.Close)
	api := httptest.NewServer(fake.Handler())
	t.Cleanup(api.Close)
	fake.Set(func(s *fakegithub.Server) { s.CheckRuns = map[int64]fakegithub.CheckRun{77: fakegithub.ActionsJob()} })
	fake.SetJobLog(77, log, downloads.URL)
	return fake, NewGitHub(Credential{Auth: "pat", Secret: "ghp_log_" + t.Name(), APIBaseURL: api.URL})
}

// A failed Actions job that says only its exit code gives its log's end:
// the pytest failure up to the ##[error] line, without timestamps, colour
// codes or the cleanup after it. The download host never sees the token.
func TestAnActionsJobsFailureIsTheEndOfItsLog(t *testing.T) {
	fake, gh := logWorld(t, jobLog(200))
	read, err := gh.CheckOutput(context.Background(), "acme/target", 77, 4000)
	if err != nil {
		t.Fatal(err)
	}
	out := read.Text
	if !strings.HasSuffix(out, "E       AssertionError: 3 != 2\ntests/test_backup.py:57: AssertionError\n"+
		"=========================== short test summary info ============================\n"+
		"FAILED tests/test_backup.py::BackupTest::test_backup_counts - AssertionError: 3 != 2\n"+
		"========================= 1 failed, 40 passed in 12.31s ========================\n"+
		"##[error]Process completed with exit code 1.") {
		t.Errorf("log tail does not end at the error:\n%s", out)
	}
	for _, gone := range []string{"Post job cleanup", "orphan", "2026-10-16T", "\x1b", "\uFEFF"} {
		if strings.Contains(out, gone) {
			t.Errorf("log tail holds %q:\n%s", gone, out)
		}
	}
	if !strings.Contains(out, "=================================== FAILURES ===") || !strings.Contains(out, "python -m pytest tests/") {
		t.Errorf("log tail lost the failure's start:\n%s", out)
	}
	if len(out) > 4000+len("…\n") || !strings.HasPrefix(out, "…\n") {
		t.Errorf("log tail is %d bytes, want cut to 4000 saying so", len(out))
	}
	auth := fake.DownloadAuthorization()
	if len(auth) != 1 || auth[0] != "" {
		t.Errorf("the download host was sent Authorization %q", auth)
	}
}

// The limit is the caller's: a smaller one keeps less of the same end.
func TestALogTailKeepsTheCallersLimit(t *testing.T) {
	_, gh := logWorld(t, jobLog(10))
	read, err := gh.CheckOutput(context.Background(), "acme/target", 77, 200)
	if err != nil {
		t.Fatal(err)
	}
	if out := read.Text; len(out) > 200+len("…\n") || !strings.HasSuffix(out, "##[error]Process completed with exit code 1.") {
		t.Errorf("%d bytes:\n%s", len(out), out)
	}
}

// countingReader counts what was read through it.
type countingReader struct {
	r io.Reader
	n int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += n
	return n, err
}

// A log with no ##[error] line is read only to its cap, keeping a window
// of its end: never the whole of a huge log in memory.
func TestAHugeLogIsReadToItsCapOnly(t *testing.T) {
	line := strings.Repeat("x", 120) + "\n"
	huge := &countingReader{r: io.LimitReader(repeatReader(line), 64<<20)}
	out, err := logTail(huge, 4000)
	if err != nil {
		t.Fatal(err)
	}
	if huge.n != logReadMax {
		t.Errorf("read %d bytes, past the %d cap", huge.n, logReadMax)
	}
	if len(out) > 4000+len("…\n") || !strings.HasSuffix(out, "read stopped there") {
		t.Errorf("%d bytes, ends %q", len(out), out[max(0, len(out)-80):])
	}
	// A line longer than the reader's buffer is kept to its start.
	long := strings.Repeat("y", 64<<10) + strings.Repeat("z", 136<<10) + "\n##[error]boom\n"
	if out, err := logTail(strings.NewReader(long), 8000); err != nil || !strings.HasSuffix(out, "\n##[error]boom") ||
		out != strings.Repeat("y", logLineMax)+"\n##[error]boom" {
		t.Errorf("long line: %v, %d bytes", err, len(out))
	}
}

func repeatReader(s string) io.Reader {
	return readerFunc(func(p []byte) (int, error) {
		n := 0
		for n < len(p) {
			n += copy(p[n:], s)
		}
		return n, nil
	})
}

type readerFunc func([]byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) { return f(p) }

// A log GitHub will not give — no permission, or gone — leaves what the
// check run said and why, in dude's words; a failure that may pass is
// still an error, with the check run's words and its reason beside it.
func TestALogGitHubWillNotGiveFallsBackToTheCheckRun(t *testing.T) {
	for status, why := range map[int]string{http.StatusForbidden: "GitHub refused: no Actions: Read permission",
		http.StatusNotFound: "GitHub has no log for this job", http.StatusGone: "the log has expired"} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			fake, gh := logWorld(t, jobLog(5))
			fake.Set(func(s *fakegithub.Server) { s.LogStatus = status })
			read, err := gh.CheckOutput(context.Background(), "acme/target", 77, 4000)
			if err != nil || read.Text != ".github:442: failure: Process completed with exit code 1." || read.Unread != why ||
				read.Source != "check_run" {
				t.Errorf("%+v, %v", read, err)
			}
		})
	}
	fake, gh := logWorld(t, jobLog(5))
	fake.Set(func(s *fakegithub.Server) { s.LogStatus = http.StatusBadGateway })
	read, err := gh.CheckOutput(context.Background(), "acme/target", 77, 4000)
	if !Transient(err) || read.Unread != "GitHub was unavailable" || read.Text != ".github:442: failure: Process completed with exit code 1." {
		t.Errorf("a 502 reading the log: %+v, %v; want transient, with the check run's words and why", read, err)
	}
	// An expired log the fake serves as GitHub does (410, no LogStatus).
	fake.SetJobLog(77, "", "")
	fake.Set(func(s *fakegithub.Server) { s.LogStatus = 0 })
	if read, err := gh.CheckOutput(context.Background(), "acme/target", 77, 4000); err != nil || read.Unread != "the log has expired" {
		t.Errorf("an expired log: %+v, %v", read, err)
	}
}

// A job log GitHub serves with nothing in it is said to be empty, beside
// what the check run said.
func TestAnEmptyJobLogIsSaidToBeEmpty(t *testing.T) {
	_, gh := logWorld(t, "\uFEFF\n\n")
	read, err := gh.CheckOutput(context.Background(), "acme/target", 77, 4000)
	if err != nil || read.Unread != "the log is empty" || read.Source != "check_run" ||
		read.Text != ".github:442: failure: Process completed with exit code 1." {
		t.Errorf("%+v, %v", read, err)
	}
}

// A check run GitHub will not show is a refusal of the Checks permission,
// not of Actions: Read, and its log is never asked for.
func TestARefusedCheckRunSaysTheCheckRunWasRefused(t *testing.T) {
	fake, gh := logWorld(t, jobLog(5))
	fake.Set(func(s *fakegithub.Server) { s.CheckRunStatus = http.StatusForbidden })
	read, err := gh.CheckOutput(context.Background(), "acme/target", 77, 4000)
	var e *Error
	if !errors.As(err, &e) || e.Status != http.StatusForbidden ||
		read.Unread != "GitHub refused to show the check run (HTTP 403)" || read.Text != "" {
		t.Errorf("%+v, %v", read, err)
	}
	if got := fake.DownloadAuthorization(); len(got) != 0 {
		t.Errorf("the log was downloaded after the check run was refused: %q", got)
	}
}

// The reason a log was not read comes from the status alone: what GitHub
// wrote (here, a body echoing the token) never reaches it.
func TestAnUnreadReasonCarriesNothingGitHubWrote(t *testing.T) {
	token := "ghp_secret_" + t.Name()
	for _, err := range []error{&Error{Status: 403, Message: "Resource not accessible by " + token},
		&Error{Status: 401, Message: "Bad credentials " + token}, &Error{Status: 418, Message: token},
		&Error{Status: 503, Message: token}, &Unreachable{Err: errors.New("dial " + token)}} {
		if why := UnreadReason(err); why == "" || strings.Contains(why, token) || strings.Contains(why, "ghp_") {
			t.Errorf("%v: %q", err, why)
		}
	}
}

func TestAnExitOnlyCheckOutputUsesTheActionsLog(t *testing.T) {
	for _, field := range []string{"title", "summary", "text"} {
		t.Run(field, func(t *testing.T) {
			fake, gh := logWorld(t, jobLog(5))
			run := fakegithub.ActionsJob()
			switch field {
			case "title":
				run.Title = "Process completed with exit code 1."
			case "summary":
				run.Summary = "Process completed with exit code 2."
			case "text":
				run.Text = "Process completed with exit code 127."
			}
			fake.Set(func(s *fakegithub.Server) { s.CheckRuns[77] = run })
			read, err := gh.CheckOutput(context.Background(), "acme/target", 77, 4000)
			if err != nil || !strings.Contains(read.Text, "AssertionError: 3 != 2") {
				t.Fatalf("exit-only %s: %q, %v", field, read.Text, err)
			}
		})
	}
}

// (a) JERV-1: GitHub's deprecation warning and runner-label notice beside
// the exit code's failure. The fixer is given the log's end, failure line
// included, and neither the warning nor the notice.
func TestAnActionsJobWithWarningsAndNoticesGivesItsLog(t *testing.T) {
	fake, gh := logWorld(t, jobLog(5))
	fake.Set(func(s *fakegithub.Server) { s.CheckRuns[77] = fakegithub.NoisyActionsJob() })
	read, err := gh.CheckOutput(context.Background(), "acme/target", 77, 4000)
	if err != nil || read.Source != "log" || read.Unread != "" {
		t.Fatalf("%+v, %v", read, err)
	}
	if !strings.Contains(read.Text, "E       AssertionError: 3 != 2") ||
		!strings.HasSuffix(read.Text, "##[error]Process completed with exit code 1.") {
		t.Errorf("not the log's end:\n%s", read.Text)
	}
	for _, noise := range []string{"Node.js 20", "deprecated", "ubuntu-latest", "warning", "notice"} {
		if strings.Contains(read.Text, noise) {
			t.Errorf("holds %q:\n%s", noise, read.Text)
		}
	}
}

// An Actions job whose check run says why gives its words first, then
// its log's end; a failure annotation stays, a warning goes.
func TestAnActionsJobThatSaysWhyGivesItsWordsThenItsLog(t *testing.T) {
	fake, gh := logWorld(t, jobLog(5))
	fake.Set(func(s *fakegithub.Server) {
		s.CheckRuns[77] = fakegithub.CheckRun{App: "github-actions", Title: "1 test failed", Annotations: []fakegithub.Annotation{
			{Path: "tests/test_backup.py", Line: 57, Level: "failure", Message: "AssertionError: 3 != 2"},
			{Path: ".github", Line: 2, Level: "warning", Message: "Node.js 20 is deprecated"}}}
	})
	read, err := gh.CheckOutput(context.Background(), "acme/target", 77, 4000)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(read.Text, "1 test failed\ntests/test_backup.py:57: failure: AssertionError: 3 != 2\n") ||
		!strings.HasSuffix(read.Text, "##[error]Process completed with exit code 1.") || strings.Contains(read.Text, "Node.js") {
		t.Errorf("%s", read.Text)
	}
}

// (b) A check that is not an Actions job gives what its check run says,
// every annotation level included; no log is read.
func TestACheckOfAnotherAppIsReadAsBefore(t *testing.T) {
	fake, gh := logWorld(t, jobLog(5))
	fake.Set(func(s *fakegithub.Server) {
		s.CheckRuns[77] = fakegithub.CheckRun{App: "circleci", Title: "lint", Annotations: []fakegithub.Annotation{
			{Path: ".circleci", Line: 1, Level: "failure", Message: "Process completed with exit code 1."},
			{Path: "main.go", Line: 3, Level: "warning", Message: "unused variable"}}}
	})
	read, err := gh.CheckOutput(context.Background(), "acme/target", 77, 4000)
	if err != nil || read.Source != "check_run" || read.Actions || read.Unread != "" ||
		read.Text != "lint\n.circleci:1: failure: Process completed with exit code 1.\nmain.go:3: warning: unused variable" {
		t.Errorf("%+v, %v", read, err)
	}
	if auth := fake.DownloadAuthorization(); len(auth) != 0 {
		t.Errorf("a log was read: %v", auth)
	}
}

// An agent's read of a check names its source: the log for an Actions
// job, the check run for any other.
func TestAnAgentReadsACheckLog(t *testing.T) {
	fake, gh := logWorld(t, jobLog(5))
	read, err := gh.CheckLog(context.Background(), "acme/target", 77, 16<<10)
	if err != nil || read.Source != "log" || !read.Actions || !strings.HasSuffix(read.Text, "##[error]Process completed with exit code 1.") {
		t.Fatalf("%+v, %v", read, err)
	}
	fake.Set(func(s *fakegithub.Server) {
		s.CheckRuns[77] = fakegithub.CheckRun{App: "circleci", Summary: "lint failed"}
	})
	if read, err := gh.CheckLog(context.Background(), "acme/target", 77, 16<<10); err != nil || read.Source != "check_run" ||
		read.Actions || read.Text != "lint failed" {
		t.Errorf("%+v, %v", read, err)
	}
}

// (g) github_check_log's read of the JERV-1 shape is the log.
func TestAnAgentReadsTheLogOfAJobWithWarnings(t *testing.T) {
	fake, gh := logWorld(t, jobLog(5))
	fake.Set(func(s *fakegithub.Server) { s.CheckRuns[77] = fakegithub.NoisyActionsJob() })
	read, err := gh.CheckLog(context.Background(), "acme/target", 77, 16<<10)
	if err != nil || read.Source != "log" || !strings.Contains(read.Text, "AssertionError: 3 != 2") || strings.Contains(read.Text, "Node.js") {
		t.Fatalf("%+v, %v", read, err)
	}
}

// Cut keeps a check run's words first, at most half the limit, and the end
// of the log in the rest.
func TestACutKeepsTheWordsThenTheLogsEnd(t *testing.T) {
	r := CheckLogRead{Said: strings.Repeat("said line\n", 100) + "last said", Tail: strings.Repeat("log line\n", 500) + "##[error]boom"}
	out := r.Cut(1000)
	if len(out) > 1000+len("…\n") || !strings.HasPrefix(out, "said line\n") || !strings.HasSuffix(out, "##[error]boom") ||
		!strings.Contains(out, "said line\n…\n…\nlog line") {
		t.Errorf("%d bytes:\n%s", len(out), out)
	}
	if got := (CheckLogRead{Said: "a", Tail: "b"}).Cut(100); got != "a\nb" {
		t.Errorf("%q", got)
	}
}
