package forge

import (
	"context"
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
	out, err := gh.CheckOutput(context.Background(), "acme/target", 77, 4000)
	if err != nil {
		t.Fatal(err)
	}
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
	out, err := gh.CheckOutput(context.Background(), "acme/target", 77, 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) > 200+len("…\n") || !strings.HasSuffix(out, "##[error]Process completed with exit code 1.") {
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
	long := strings.Repeat("y", 200<<10) + "\n##[error]boom\n"
	if out, err := logTail(strings.NewReader(long), 8000); err != nil || !strings.HasSuffix(out, "\n##[error]boom") ||
		strings.Count(out, "y") > logLineMax {
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
// check run said; a failure that may pass is still an error.
func TestALogGitHubWillNotGiveFallsBackToTheCheckRun(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusNotFound, http.StatusGone} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			fake, gh := logWorld(t, jobLog(5))
			fake.Set(func(s *fakegithub.Server) { s.LogStatus = status })
			out, err := gh.CheckOutput(context.Background(), "acme/target", 77, 4000)
			if err != nil || out != ".github:442: failure: Process completed with exit code 1." {
				t.Errorf("%q, %v", out, err)
			}
		})
	}
	fake, gh := logWorld(t, jobLog(5))
	fake.Set(func(s *fakegithub.Server) { s.LogStatus = http.StatusBadGateway })
	if _, err := gh.CheckOutput(context.Background(), "acme/target", 77, 4000); !Transient(err) {
		t.Errorf("a 502 reading the log: %v, want transient", err)
	}
}

// A check that is not an Actions job, or an Actions job whose check run
// says why, gives what its check run says; its log is not read.
func TestACheckThatSaysWhyIsReadAsBefore(t *testing.T) {
	fake, gh := logWorld(t, jobLog(5))
	for name, run := range map[string]fakegithub.CheckRun{
		"another app": {App: "circleci", Annotations: []fakegithub.Annotation{{Path: ".circleci", Line: 1, Level: "failure",
			Message: "Process completed with exit code 1."}}},
		"an Actions job that says why": {App: "github-actions", Title: "1 test failed", Summary: "test_backup_counts: 3 != 2"},
	} {
		fake.Set(func(s *fakegithub.Server) { s.CheckRuns[77] = run })
		out, err := gh.CheckOutput(context.Background(), "acme/target", 77, 4000)
		if err != nil || strings.Contains(out, "FAILURES") {
			t.Errorf("%s: %q, %v", name, out, err)
		}
	}
	if auth := fake.DownloadAuthorization(); len(auth) != 0 {
		t.Errorf("a log was read: %v", auth)
	}
	fake.Set(func(s *fakegithub.Server) {
		s.CheckRuns[77] = fakegithub.CheckRun{App: "github-actions", Title: "1 test failed", Summary: "test_backup_counts: 3 != 2"}
	})
	out, _ := gh.CheckOutput(context.Background(), "acme/target", 77, 4000)
	if out != "1 test failed\ntest_backup_counts: 3 != 2" {
		t.Errorf("an Actions job that says why: %q", out)
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
