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
	// it says nothing about why.
	exitCodeAnnotation = regexp.MustCompile(`^Process completed with exit code \d+\.?$`)
)

// checkRun is a check run as GitHub reports it: its app, and what it said
// (output and annotations), written as CheckOutput gives it. Generic: it
// said nothing but that its process exited non-zero.
type checkRun struct {
	app, said string
	generic   bool
}

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
	out := checkRun{generic: true}
	if run.App != nil {
		out.app = run.App.Slug
	}
	var b strings.Builder
	for _, part := range []string{run.Output.Title, run.Output.Summary, run.Output.Text} {
		if part = strings.TrimSpace(part); part != "" {
			b.WriteString(part + "\n")
			if !exitCodeAnnotation.MatchString(part) {
				out.generic = false
			}
		}
	}
	for _, a := range annotations {
		fmt.Fprintf(&b, "%s:%d: %s: %s\n", a.Path, a.Line, a.Level, a.Message)
		if !exitCodeAnnotation.MatchString(strings.TrimSpace(a.Message)) {
			out.generic = false
		}
	}
	out.said = strings.TrimSpace(b.String())
	return out, nil
}

// CheckOutput is what a failed check run says about why: its summary and
// text, and its annotations (file, line, message). For a GitHub Actions
// job that says only that it exited non-zero, the end of its log instead
// (JobLogTail), when GitHub gives it. Truncated: a fixer wants the
// reason, not the whole log.
func (g *GitHub) CheckOutput(ctx context.Context, slug string, runID int64, limit int) (string, error) {
	run, err := g.readCheckRun(ctx, slug, runID)
	if err != nil {
		return "", err
	}
	if run.generic {
		log, err := g.checkLogResult(ctx, slug, runID, run, limit)
		return log.Text, err
	}
	return truncate(run.said, limit), nil
}

// CheckLogRead is a check as an agent reads it: the end of its job's log
// (Source "log"), or, for a check that is not an Actions job or whose log
// GitHub will not give, what its check run said ("check_run").
type CheckLogRead struct {
	Source, Text string
	// Actions: the check is a GitHub Actions job.
	Actions bool
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

func (g *GitHub) checkLogResult(ctx context.Context, slug string, runID int64, run checkRun, limit int) (CheckLogRead, error) {
	out := CheckLogRead{Source: "check_run", Actions: run.app == ActionsApp}
	if out.Actions {
		tail, err := g.JobLogTail(ctx, slug, runID, limit)
		switch {
		case err == nil && tail != "":
			out.Source, out.Text = "log", tail
			return out, nil
		case err != nil && !logGone(err):
			return CheckLogRead{}, err
		}
	}
	out.Text = truncate(run.said, limit)
	return out, nil
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
