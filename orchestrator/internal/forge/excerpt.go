package forge

import (
	"bufio"
	"errors"
	"io"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

const (
	excerptSites   = 16
	excerptBefore  = 2
	excerptAfter   = 4
	omissionMarker = "… [lines omitted]"
	lineCapMarker  = " … [line capped]"
	inputCapMarker = "… [input capped at 16 MiB: read stopped there]"
)

type excerptLine struct {
	n    int
	text string
}
type failureSite struct {
	anchor   excerptLine
	priority int
	lines    []excerptLine
}

// failureCollector keeps a fixed number of sites, a small look-behind, and
// a byte-bounded rolling tail. Priorities are categories, not a scoring model.
// Equal-priority sites keep source order; a later stronger site replaces the
// last weakest site. The last observed exit is independent of that site cap.
type failureCollector struct {
	sites                []failureSite
	before, tail         []excerptLine
	tailBytes, window, n int
	exit                 *excerptLine
	capped               bool
}

func excerptBody(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "[lane") {
		if end := strings.IndexByte(s, ']'); end >= 5 {
			digits := s[5:end]
			if digits != "" && strings.Trim(digits, "0123456789") == "" {
				s = strings.TrimSpace(s[end+1:])
			}
		}
	}
	return s
}

// Match emitted failures, not timeout options, fixture names, or configuration.
var timeoutFailure = regexp.MustCompile(`(?i)^(?:E\s+)?(?:Failed: Timeout >|TimeoutError\b|\+{2,}\s*Timeout\b)`)

func failurePriority(s string) int {
	s = excerptBody(s)
	low := strings.ToLower(s)
	switch {
	case strings.HasPrefix(low, "panic:") || strings.HasPrefix(low, "fatal error:") || strings.Contains(low, "timeout") && timeoutFailure.MatchString(s) || strings.HasPrefix(low, "test timed out after ") || low == "running tests:":
		return 0
	case strings.HasPrefix(s, "--- FAIL:") || strings.HasPrefix(s, "FAILED ") || strings.Contains(s, "AssertionError") || strings.HasPrefix(s, "E ") || strings.Contains(s, "assert ") || strings.Contains(s, "FAILURES") || ruffDiagnostic.MatchString(s):
		return 1
	case s == "FAIL" || strings.HasPrefix(s, "FAIL\t") || strings.HasPrefix(s, "FAIL "):
		return 2
	case strings.Contains(low, "error") || strings.Contains(low, "failed") || strings.Contains(low, ": failure:"):
		return 3
	}
	return -1
}

func finalExit(s string) bool {
	s = excerptBody(s)
	return strings.HasPrefix(s, "##[error]Process completed with exit code ") || strings.HasPrefix(s, "Process completed with exit code ") || strings.HasPrefix(s, "exit status ")
}

func (c *failureCollector) add(text string) {
	c.n++
	line := excerptLine{c.n, text}
	// Old omission markers are boundaries, not another failure or context line.
	if text == omissionMarker || text == "…" {
		return
	}
	if text == inputCapMarker {
		c.capped = true
		return
	}
	for i := range c.sites {
		s := &c.sites[i]
		if line.n > s.anchor.n && line.n <= s.anchor.n+excerptAfter {
			s.lines = append(s.lines, line)
		}
	}
	priority := failurePriority(text)
	// A running-tests header is one site with at most four following names,
	// not one strongest site per name. Large timeout blocks cannot fill the cap.
	if finalExit(text) {
		saved := line
		c.exit = &saved
		priority = -1
	}
	if priority >= 0 {
		slot := len(c.sites)
		if slot == excerptSites {
			worst := 0
			for i := 1; i < len(c.sites); i++ {
				a, b := c.sites[i], c.sites[worst]
				if a.priority > b.priority || a.priority == b.priority && a.anchor.n > b.anchor.n {
					worst = i
				}
			}
			if priority < c.sites[worst].priority {
				slot = worst
			}
		}
		// Do not allocate context for a rejected candidate in a noisy stream.
		if slot < excerptSites {
			lines := make([]excerptLine, len(c.before)+1, excerptBefore+1+excerptAfter)
			copy(lines, c.before)
			lines[len(c.before)] = line
			site := failureSite{anchor: line, priority: priority, lines: lines}
			if slot == len(c.sites) {
				c.sites = append(c.sites, site)
			} else {
				c.sites[slot] = site
			}
		}
	}
	c.before = append(c.before, line)
	if len(c.before) > excerptBefore {
		c.before = append(c.before[:0], c.before[1:]...)
	}
	c.tail = append(c.tail, line)
	c.tailBytes += len(text) + 1
	for len(c.tail) > 1 && (c.tailBytes > c.window || len(c.tail) > 64) {
		c.tailBytes -= len(c.tail[0].text) + 1
		c.tail[0] = excerptLine{}
		c.tail = c.tail[1:]
	}
}

// FailureExcerpt selects failure sites and the final observed exit, with nearby
// context and a rolling tail, in source order. Positive limits include markers;
// nonpositive limits use the bounded 16 MiB input budget. An already fitting
// excerpt is returned unchanged, making same-budget delivery recuts idempotent.
// This does not add redaction: callers retain their existing redaction policy.
func FailureExcerpt(text string, limit int) string {
	if limit <= 0 {
		limit = logReadMax
	}
	if len(text) <= limit && len(text) <= logReadMax && utf8.ValidString(text) {
		short := true
		for rest := text; rest != ""; {
			line, next, ok := strings.Cut(rest, "\n")
			if len(line) > logLineMax {
				short = false
				break
			}
			if !ok {
				break
			}
			rest = next
		}
		if short {
			return text
		}
	}
	out, _ := collectFailure(strings.NewReader(text), limit)
	return out
}

func collectFailure(r io.Reader, limit int) (string, error) {
	if limit <= 0 {
		limit = logReadMax
	}
	c := failureCollector{window: max(1024, min(limit, logReadMax))}
	in := bufio.NewReaderSize(io.LimitReader(r, logReadMax), logLineMax+1)
	read := 0
	for {
		part, err := in.ReadSlice('\n')
		read += len(part)
		line := append([]byte(nil), part[:min(len(part), logLineMax)]...)
		long := len(part) > logLineMax && (len(part) > logLineMax+1 || part[len(part)-1] != '\n')
		for errors.Is(err, bufio.ErrBufferFull) {
			long = true
			part, err = in.ReadSlice('\n')
			read += len(part)
		}
		if len(line) > 0 {
			text := cleanLogLine(line)
			if long || len(text) > logLineMax {
				text = utf8Prefix(text, logLineMax-len(lineCapMarker)) + lineCapMarker
			}
			c.add(text)
		}
		if err == io.EOF {
			c.capped = c.capped || read >= logReadMax
			break
		}
		if err != nil {
			return "", err
		}
	}
	return c.render(limit), nil
}

func utf8Prefix(s string, n int) string {
	n = max(0, min(n, len(s)))
	for n > 0 && !utf8.ValidString(s[:n]) {
		n--
	}
	return s[:n]
}

// visitExcerpt expects source-sorted lines and emits the same pieces for
// measurement and rendering. Selection trials never build output strings.
func visitExcerpt(lines []excerptLine, end int, capped bool, emit func(string)) {
	prev, written := 0, false
	add := func(text string) {
		if written {
			emit("\n")
		}
		emit(text)
		written = written || text != ""
	}
	for _, line := range lines {
		if line.n == prev {
			continue
		}
		if line.n > prev+1 {
			add(omissionMarker)
		}
		add(line.text)
		prev = line.n
	}
	if prev < end {
		add(omissionMarker)
	}
	if capped {
		add(inputCapMarker)
	}
}

func excerptLength(lines []excerptLine, end int, capped bool) int {
	n, trailing := 0, 0
	visitExcerpt(lines, end, capped, func(s string) {
		n += len(s)
		trimmed := strings.TrimRight(s, "\n")
		if trimmed == "" {
			trailing += len(s)
		} else {
			trailing = len(s) - len(trimmed)
		}
	})
	return n - trailing
}

func renderLines(lines []excerptLine, end int, capped bool) string {
	slices.SortFunc(lines, func(a, b excerptLine) int { return a.n - b.n })
	var b strings.Builder
	b.Grow(excerptLength(lines, end, capped))
	visitExcerpt(lines, end, capped, func(s string) { b.WriteString(s) })
	return strings.TrimRight(b.String(), "\n")
}

func (c *failureCollector) render(limit int) string {
	// Try the entire retained union before dropping context.
	all := append([]excerptLine{}, c.tail...)
	for _, s := range c.sites {
		all = append(all, s.lines...)
	}
	if c.exit != nil {
		all = append(all, *c.exit)
	}
	slices.SortFunc(all, func(a, b excerptLine) int { return a.n - b.n })
	if excerptLength(all, c.n, c.capped) <= limit {
		return renderLines(all, c.n, c.capped)
	}
	selected := make([]excerptLine, 0, len(all)+1)
	try := func(line excerptLine) bool {
		i, found := slices.BinarySearchFunc(selected, line, func(a, b excerptLine) int { return a.n - b.n })
		if found {
			return true
		}
		selected = append(selected, excerptLine{})
		copy(selected[i+1:], selected[i:])
		selected[i] = line
		if excerptLength(selected, c.n, c.capped) > limit {
			copy(selected[i:], selected[i+1:])
			selected = selected[:len(selected)-1]
			return false
		}
		return true
	}
	if c.exit != nil {
		try(*c.exit)
	}
	sites := append([]failureSite{}, c.sites...)
	slices.SortStableFunc(sites, func(a, b failureSite) int {
		if a.priority != b.priority {
			return a.priority - b.priority
		}
		return a.anchor.n - b.anchor.n
	})
	for _, s := range sites {
		anchor := s.anchor
		// A single capped diagnostic must not lose its failure prefix merely
		// because the caller's budget is smaller than the per-line cap.
		if len(anchor.text) > max(32, limit/3) {
			anchor.text = utf8Prefix(anchor.text, max(0, limit/3-len(lineCapMarker))) + lineCapMarker
		}
		try(anchor)
	}
	// Add context by distance, so large diagnostics cannot consume every byte.
	for distance := 1; distance <= max(excerptBefore, excerptAfter); distance++ {
		for _, s := range sites {
			for _, line := range s.lines {
				if line.n == s.anchor.n-distance || line.n == s.anchor.n+distance {
					try(line)
				}
			}
		}
	}
	for i := len(c.tail) - 1; i >= 0; i-- {
		try(c.tail[i])
	}
	if len(selected) > 0 {
		return renderLines(selected, c.n, c.capped)
	}
	// Tiny budgets cannot fit complete anchors plus an omission marker. Keep a
	// UTF-8 prefix of the strongest anchor, with a compact truncation marker.
	text := ""
	if len(sites) > 0 {
		text = sites[0].anchor.text
	} else if c.exit != nil {
		text = c.exit.text
	} else if len(c.tail) > 0 {
		text = c.tail[len(c.tail)-1].text
	}
	if c.capped {
		text = inputCapMarker
	}
	if limit < 3 {
		return strings.Repeat(".", limit)
	}
	return utf8Prefix(text, limit-3) + "…"
}
