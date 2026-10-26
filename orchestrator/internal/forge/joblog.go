package forge

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strings"
)

// ActionsApp is the app GitHub Actions reports its check runs as: each is
// a job, whose id is the check run's.
const ActionsApp = "github-actions"

// Bounds of reading a job's log: GitHub keeps it whole, often many MB.
// The read stops at its first ##[error] line or after logReadMax bytes,
// and keeps at most logLineMax bytes of one line.
const (
	logReadMax = 16 << 20
	logLineMax = 4 << 10
)

var (
	// GitHub's own timestamp at the start of every line of a job's log.
	logTimestamp = regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d+)?Z ?`)
	// Colour and cursor codes (CSI), and terminal titles and links (OSC).
	ansiEscape = regexp.MustCompile("\x1b\\[[0-9;?]*[ -/]*[@-~]|\x1b\\][^\x07\x1b]*(?:\x07|\x1b\\\\)")
	// The one annotation GitHub gives a job that failed by its exit code:
	// it says nothing about why, and its log ends with the same line.
	exitCodeAnnotation = regexp.MustCompile(`^Process completed with exit code \d+\.?$`)
)

// checkRun is a check run as GitHub reports it: its app, and what it said
// (output and annotations), written as CheckOutput gives it. For an
// Actions job, said keeps only failure-level annotations: GitHub adds a
// warning or notice (a deprecated Node.js, a runner label's migration) to
// almost every job, and they say nothing about why it failed. exitOnly
// holds the lines of said that are the exit-code annotation alone.
type checkRun struct {
	app, said string
	exitOnly  []string
}

// failureLevel says an annotation reports a failure, not a warning or
// notice.
func failureLevel(level string) bool { return level == "failure" || level == "error" }

func (g *GitHub) readCheckRun(ctx context.Context, slug string, runID int64) (checkRun, error) {
	var run struct {
		Output struct {
			Title   string `json:"title"`
			Summary string `json:"summary"`
			Text    string `json:"text"`
		} `json:"output"`
		App *struct {
			Slug string `json:"slug"`
		} `json:"app"`
	}
	if err := g.do(ctx, "GET", fmt.Sprintf("/repos/%s/check-runs/%d", slug, runID), nil, &run); err != nil {
		return checkRun{}, err
	}
	var annotations []struct {
		Path    string `json:"path"`
		Line    int    `json:"start_line"`
		Level   string `json:"annotation_level"`
		Message string `json:"message"`
	}
	if err := g.do(ctx, "GET", fmt.Sprintf("/repos/%s/check-runs/%d/annotations?per_page=50", slug, runID), nil, &annotations); err != nil {
		var e *Error
		if !asError(err, &e) || Transient(err) {
			return checkRun{}, err
		}
	}
	var out checkRun
	if run.App != nil {
		out.app = run.App.Slug
	}
	var b strings.Builder
	for _, part := range []string{run.Output.Title, run.Output.Summary, run.Output.Text} {
		if part = strings.TrimSpace(part); part != "" {
			b.WriteString(part + "\n")
			if exitCodeAnnotation.MatchString(part) {
				out.exitOnly = append(out.exitOnly, part)
			}
		}
	}
	for _, a := range annotations {
		if out.app == ActionsApp && !failureLevel(a.Level) {
			continue
		}
		line := fmt.Sprintf("%s:%d: %s: %s", a.Path, a.Line, a.Level, a.Message)
		b.WriteString(line + "\n")
		if exitCodeAnnotation.MatchString(strings.TrimSpace(a.Message)) {
			out.exitOnly = append(out.exitOnly, line)
		}
	}
	out.said = strings.TrimSpace(b.String())
	return out, nil
}

// CheckOutput is what a failed check run says about why, for a fixer. For
// a GitHub Actions job, always the end of its log (JobLogTail) after its
// output and failure-level annotations; a log GitHub will not give, or
// could not give now, leaves those with Unread saying why. For any other
// check, its summary, text and annotations as it reported them. A read
// that degraded for a failure that may pass (Transient) comes with that
// error, for the caller to log; the read is still the one to use.
func (g *GitHub) CheckOutput(ctx context.Context, slug string, runID int64, limit int) (CheckLogRead, error) {
	run, err := g.readCheckRun(ctx, slug, runID)
	if err != nil {
		return CheckLogRead{Unread: checkRunUnread(err)}, err
	}
	read, err := g.checkLogResult(ctx, slug, runID, run, limit)
	if err != nil {
		// The log alone failed: what the check run said still stands.
		read = CheckLogRead{Source: "check_run", Actions: true, Said: run.said, Unread: UnreadReason(err)}
		read.Text = read.Cut(limit)
	}
	return read, err
}

// CheckLogRead is a check as an agent reads it: the end of its job's log
// (Source "log"), or, for a check that is not an Actions job or whose log
// GitHub will not give, what its check run said ("check_run").
type CheckLogRead struct {
	// Text is Said then Tail, at most the read's limit (Cut).
	Source, Text string
	// Actions: the check is a GitHub Actions job.
	Actions bool
	// Said is what the check run said worth reading (for an Actions job,
	// its output and failure-level annotations); Tail, its job's log tail.
	Said, Tail string
	// Unread is why an Actions job's log, or the check run itself, could
	// not be read, in dude's words (UnreadReason); "" when it was.
	Unread string
}

// Cut is Said then Tail in at most limit bytes: Said's first lines, up to
// half of limit when there is a tail, and the end of Tail in the rest.
func (r CheckLogRead) Cut(limit int) string {
	switch {
	case r.Tail == "":
		return truncate(r.Said, limit)
	case r.Said == "":
		return truncate(r.Tail, limit)
	}
	head := r.Said
	if limit > 0 && len(head) > limit/2 {
		head = head[:limit/2]
		if i := strings.LastIndexByte(head, '\n'); i > 0 {
			head = head[:i]
		}
		head += "\n…"
	}
	rest := 0
	if limit > 0 {
		rest = max(limit-len(head)-1, 1)
	}
	return head + "\n" + truncate(r.Tail, rest)
}

// UnreadReason says, for a person or an agent, why GitHub did not give a
// check's log: from the status alone, never GitHub's message or body,
// which may echo what was sent.
func UnreadReason(err error) string {
	var e *Error
	switch {
	case err == nil:
		return ""
	case Transient(err):
		return "GitHub was unavailable"
	case !errors.As(err, &e):
		return "the read failed before GitHub answered"
	case e.Status == http.StatusUnauthorized:
		return "GitHub refused dude's credential"
	case e.Status == http.StatusForbidden:
		return "GitHub refused: no Actions: Read permission"
	case e.Status == http.StatusNotFound:
		return "GitHub has no log for this job"
	case e.Status == http.StatusGone:
		return "the log has expired"
	}
	return fmt.Sprintf("GitHub refused the read (HTTP %d)", e.Status)
}

// checkRunUnread is UnreadReason for the check run itself, read before
// its log: a refusal there is of the Checks permission, not Actions.
func checkRunUnread(err error) string {
	var e *Error
	if Transient(err) || !errors.As(err, &e) {
		return UnreadReason(err)
	}
	return fmt.Sprintf("GitHub refused to show the check run (HTTP %d)", e.Status)
}

// CheckLog reads a check run for an agent: an Actions job's log tail,
// cut as JobLogTail cuts it, else its output and annotations; at most
// limit bytes.
func (g *GitHub) CheckLog(ctx context.Context, slug string, runID int64, limit int) (CheckLogRead, error) {
	ctx = agentReadContext(ctx)
	if err := g.readBudget(ctx, slug, "core"); err != nil {
		return CheckLogRead{}, err
	}
	run, err := g.readCheckRun(ctx, slug, runID)
	if err != nil {
		return CheckLogRead{}, err
	}
	return g.checkLogResult(ctx, slug, runID, run, limit)
}

// WorkflowRun is a GitHub Actions run with its latest attempt's jobs.
type WorkflowRun struct {
	ID                   int64
	Name, Branch, Event  string
	HeadSHA, Status, URL string
	Conclusion           string
	Jobs                 []WorkflowJob
	// TotalJobs is how many jobs GitHub has, of which Jobs is the first page.
	TotalJobs int
}

// WorkflowJob is one job of a run: its id is its check run's, which
// CheckLog reads.
type WorkflowJob struct {
	ID                int64
	Name, Status, URL string
	Conclusion        string
	FailedSteps       []string
}

// RunJobs reads Actions run runID of slug and the jobs of its latest
// attempt, at most max of them.
func (g *GitHub) RunJobs(ctx context.Context, slug string, runID int64, max int) (WorkflowRun, error) {
	ctx = agentReadContext(ctx)
	if err := g.readBudget(ctx, slug, "core"); err != nil {
		return WorkflowRun{}, err
	}
	var run struct {
		ID         int64  `json:"id"`
		Name       string `json:"name"`
		HeadBranch string `json:"head_branch"`
		HeadSHA    string `json:"head_sha"`
		Event      string `json:"event"`
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
		HTMLURL    string `json:"html_url"`
	}
	if err := g.do(ctx, "GET", fmt.Sprintf("/repos/%s/actions/runs/%d", slug, runID), nil, &run); err != nil {
		return WorkflowRun{}, err
	}
	var jobs struct {
		Total int `json:"total_count"`
		Jobs  []struct {
			ID         int64  `json:"id"`
			Name       string `json:"name"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
			HTMLURL    string `json:"html_url"`
			Steps      []struct {
				Name       string `json:"name"`
				Conclusion string `json:"conclusion"`
			} `json:"steps"`
		} `json:"jobs"`
	}
	if err := g.do(ctx, "GET", fmt.Sprintf("/repos/%s/actions/runs/%d/jobs?filter=latest&per_page=%d", slug, runID, max), nil, &jobs); err != nil {
		return WorkflowRun{}, err
	}
	out := WorkflowRun{ID: run.ID, Name: run.Name, Branch: run.HeadBranch, Event: run.Event, HeadSHA: run.HeadSHA, Status: run.Status,
		Conclusion: run.Conclusion, URL: run.HTMLURL, TotalJobs: jobs.Total}
	for _, j := range jobs.Jobs[:min(len(jobs.Jobs), max)] {
		job := WorkflowJob{ID: j.ID, Name: j.Name, Status: j.Status, Conclusion: j.Conclusion, URL: j.HTMLURL}
		for _, s := range j.Steps {
			if s.Conclusion == "failure" || s.Conclusion == "timed_out" || s.Conclusion == "cancelled" {
				job.FailedSteps = append(job.FailedSteps, s.Name)
			}
		}
		out.Jobs = append(out.Jobs, job)
	}
	return out, nil
}

// checkLogResult reads an Actions job's log whatever its check run said,
// and keeps its check run's words before it, less the exit-code line the
// log ends with. A log GitHub will not give leaves the check run's words
// and why; a failure that may pass is returned.
func (g *GitHub) checkLogResult(ctx context.Context, slug string, runID int64, run checkRun, limit int) (CheckLogRead, error) {
	out := CheckLogRead{Source: "check_run", Actions: run.app == ActionsApp, Said: run.said}
	if out.Actions {
		tail, err := g.JobLogTail(ctx, slug, runID, limit)
		switch {
		case err == nil && tail != "":
			out.Source, out.Tail = "log", tail
			out.Said = withoutLines(run.said, run.exitOnly)
		case err == nil:
			out.Unread = "the log is empty"
		case logGone(err):
			out.Unread = UnreadReason(err)
		default:
			return CheckLogRead{}, err
		}
	}
	out.Text = out.Cut(limit)
	return out, nil
}

// withoutLines is text less each of its lines that is in drop.
func withoutLines(text string, drop []string) string {
	if len(drop) == 0 {
		return text
	}
	var kept []string
	for _, line := range strings.Split(text, "\n") {
		if !slices.Contains(drop, line) {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

// logGone says GitHub will not give a job's log: no permission to read
// it (403), or no log, or one expired (404, 410).
func logGone(err error) bool {
	var e *Error
	return errors.As(err, &e) && !Transient(err) &&
		(e.Status == http.StatusForbidden || e.Status == http.StatusNotFound || e.Status == http.StatusGone)
}

// JobLogTail is the end of a GitHub Actions job's log, as a person reads
// the failure in it: GitHub's timestamps and terminal codes removed, up to
// and including the first ##[error] line (later lines are the job's
// cleanup), its last limit bytes. GitHub answers with a redirect to a
// short-lived download URL on another host, which is followed without the
// token. The read streams: it keeps a window of the text, stops at the
// first ##[error] line or after logReadMax bytes, and honours ctx.
func (g *GitHub) JobLogTail(ctx context.Context, slug string, jobID int64, limit int) (string, error) {
	path := fmt.Sprintf("/repos/%s/actions/jobs/%d/logs", slug, jobID)
	target := g.cred.APIBaseURL + path
	token, err := g.Token(ctx, slug)
	if err != nil {
		return "", err
	}
	authorization := "Bearer " + token
	if isAgentRead(ctx) {
		if err := reserveAgentRequest(target, authorization, "core"); err != nil {
			return "", err
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", authorization)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	client := *g.http
	client.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return errors.New("too many redirects reading a job's log")
		}
		if next.Response != nil {
			noteRate(target, authorization, next.Response.Header)
		}
		// The download URL carries its own signature, never our credential.
		next.Header.Del("Authorization")
		if next.URL.User != nil || (via[0].URL.Scheme == "https" && next.URL.Scheme != "https") {
			return errors.New("unsafe redirect reading a job's log")
		}
		return nil
	}
	res, err := client.Do(req)
	if err != nil {
		return "", &Unreachable{err}
	}
	defer res.Body.Close()
	if res.Request != nil && res.Request.URL.Host == req.URL.Host {
		noteRate(target, authorization, res.Header)
	}
	if res.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
		var e struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(data, &e)
		if e.Message == "" {
			e.Message = fmt.Sprintf("GET %s failed", path)
		}
		refusal := &Error{Status: res.StatusCode, Message: e.Message}
		markRateLimit(refusal, res.Header, "rate limit: ")
		return "", refusal
	}
	text, err := logTail(res.Body, limit)
	if err != nil {
		return "", &Unreachable{err}
	}
	return text, nil
}

// logTail reads a job's log from r as JobLogTail describes, keeping no
// more than about twice the limit in memory.
func logTail(r io.Reader, limit int) (string, error) {
	window := max(limit, 1<<10)
	in := bufio.NewReaderSize(io.LimitReader(r, logReadMax), 64<<10)
	var kept []byte
	read, full := 0, false
	for {
		line, err := in.ReadSlice('\n')
		read += len(line)
		long := errors.Is(err, bufio.ErrBufferFull)
		if long {
			// A line past the buffer: its start is kept, the rest skipped.
			line = bytes.Clone(line[:min(len(line), logLineMax)])
			for errors.Is(err, bufio.ErrBufferFull) {
				var more []byte
				more, err = in.ReadSlice('\n')
				read += len(more)
			}
		}
		if len(line) > 0 {
			text := cleanLogLine(line)
			kept = append(kept, text...)
			kept = append(kept, '\n')
			if strings.HasPrefix(text, "##[error]") {
				break
			}
			if len(kept) > 2*window {
				kept = append(kept[:0], kept[len(kept)-window:]...)
			}
		}
		if err == io.EOF {
			full = read >= logReadMax
			break
		}
		if err != nil {
			return "", err
		}
	}
	out := strings.TrimRight(string(kept), "\n")
	if full {
		out += fmt.Sprintf("\n…the log is longer than %d MiB: read stopped there", logReadMax>>20)
	}
	return truncate(out, limit), nil
}

// cleanLogLine is one line of a job's log as a person reads it.
func cleanLogLine(line []byte) string {
	line = bytes.TrimPrefix(bytes.TrimRight(line, "\r\n"), []byte("\uFEFF"))
	if len(line) > logLineMax {
		line = line[:logLineMax]
	}
	line = logTimestamp.ReplaceAll(line, nil)
	line = ansiEscape.ReplaceAll(line, nil)
	return strings.ToValidUTF8(string(line), "\uFFFD")
}

// CleanLog is text as cleanLogLine leaves each of its lines, without the
// other control characters (NUL, BEL…) a terminal would not show: what is
// written into a fixer's workspace as a file.
func CleanLog(text string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = strings.Map(func(r rune) rune {
			if r < 0x20 && r != '\t' || r == 0x7f {
				return -1
			}
			return r
		}, cleanLogLine([]byte(line)))
	}
	return strings.Join(lines, "\n")
}
