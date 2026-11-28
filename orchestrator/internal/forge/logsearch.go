package forge

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// LogSearch is what to find in a job's whole log: lines Pattern matches,
// each with Context lines before and after it.
type LogSearch struct {
	Pattern *regexp.Regexp
	Context int
}

// LogMatches is a search of a job's log: Text is the lines shown, each
// "<line number>: <line>", in log order, groups apart marked by
// omissionMarker. Total lines matched, of which Shown are in Text; Lines
// were read, and Capped says the read stopped at logReadMax.
type LogMatches struct {
	Text                string
	Total, Shown, Lines int
	Capped              bool
}

// logSearcher collects a search's lines as they stream past, within limit
// bytes of Text; past it, matches are still counted.
type logSearcher struct {
	search LogSearch
	limit  int
	b      strings.Builder
	before []excerptLine
	// after is how many lines after the last shown match are still its
	// context; last is the number of the last line written.
	after, last int
	out         LogMatches
	full        bool
}

// piece is what writing line after line last adds to Text: its separator,
// a gap marker when it does not follow last, and itself.
func piece(line excerptLine, last int) string {
	text := fmt.Sprintf("%d: %s", line.n, line.text)
	if last == 0 {
		return text
	}
	if line.n > last+1 {
		return "\n" + omissionMarker + "\n" + text
	}
	return "\n" + text
}

// write adds lines to Text if all of them fit, and says whether they did.
func (s *logSearcher) write(lines ...excerptLine) bool {
	var pieces []string
	n, last := s.b.Len(), s.last
	for _, line := range lines {
		p := piece(line, last)
		pieces, n, last = append(pieces, p), n+len(p), line.n
	}
	if n > s.limit {
		return false
	}
	for _, p := range pieces {
		s.b.WriteString(p)
	}
	s.last = last
	return true
}

func (s *logSearcher) add(text string) {
	s.out.Lines++
	line := excerptLine{s.out.Lines, text}
	switch {
	case s.search.Pattern.MatchString(text):
		s.out.Total++
		s.after = 0
		if s.full {
			break
		}
		var group []excerptLine
		for _, b := range s.before {
			if b.n > s.last {
				group = append(group, b)
			}
		}
		if !s.write(append(group, line)...) && !s.write(line) {
			s.full = true
			break
		}
		s.out.Shown++
		s.after = s.search.Context
	case s.after > 0:
		s.after--
		if !s.write(line) {
			s.after = 0
		}
	}
	if s.search.Context > 0 {
		s.before = append(s.before, line)
		if len(s.before) > s.search.Context {
			s.before = append(s.before[:0], s.before[1:]...)
		}
	}
}

// searchLog scans r through EOF or logReadMax for search, as JobLogTail
// reads a log (timestamps and terminal codes removed, lines capped).
func searchLog(r io.Reader, search LogSearch, limit int) (LogMatches, error) {
	s := logSearcher{search: search, limit: limit}
	capped, err := scanLogLines(r, s.add)
	if err != nil {
		return LogMatches{}, err
	}
	s.out.Text, s.out.Capped = s.b.String(), capped
	return s.out, nil
}

// SearchCheckLog reads check run runID for an agent as CheckLog does, but
// an Actions job's whole log is searched (Search) instead of cut to its
// failure: the job may have failed or not. A check that is not an Actions
// job, or whose log GitHub will not give, is read as CheckLog reads it.
// Text is at most limit bytes.
func (g *GitHub) SearchCheckLog(ctx context.Context, slug string, runID int64, search LogSearch, limit int) (CheckLogRead, error) {
	ctx = agentReadContext(ctx)
	if err := g.readBudget(ctx, slug, "core"); err != nil {
		return CheckLogRead{}, err
	}
	run, err := g.readCheckRun(ctx, slug, runID)
	if err != nil {
		return CheckLogRead{}, err
	}
	out := CheckLogRead{Source: "check_run", Actions: run.app == ActionsApp, Said: run.said}
	if out.Actions {
		var found LogMatches
		err := g.readJobLog(ctx, slug, runID, func(body io.Reader) (err error) {
			found, err = searchLog(body, search, limit)
			return err
		})
		switch {
		case err == nil && found.Lines > 0:
			return CheckLogRead{Source: "log", Actions: true, Text: found.Text, Search: &found}, nil
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
