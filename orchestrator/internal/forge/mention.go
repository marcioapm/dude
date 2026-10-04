package forge

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// DudeMention is the name a pull request comment addresses dude by,
// besides the logins dude comments as.
const DudeMention = "dude"

// ReplyMarker ends every comment the conductor posts: an HTML comment
// GitHub keeps in the body and does not render. Feedback carrying it is
// dude's own, whichever login posted it — a token comments as its owner,
// who may be a person whose comments otherwise count.
const ReplyMarker = "<!-- dude:conductor -->"

// Own says the feedback is a comment the conductor posted: its last
// non-blank line is the marker itself, as ConductReply writes it, outside
// any code fence and blockquote. A person quoting a reply carries the
// marker inside a blockquote or a code fence, or above their own words,
// and stays theirs.
func Own(f Feedback) bool {
	lines := strings.Split(strings.ReplaceAll(strings.TrimRight(f.Body, " \t\r\n"), "\r\n", "\n"), "\n")
	ind, rest := indent(lines[len(lines)-1])
	// The marker line itself goes through the fence reading: a marker that
	// ends a list item ends a fence left open in it.
	return strings.TrimSpace(rest) == ReplyMarker && ind <= 3 && !fenced(lines)
}

// fenced says the last of lines is inside a code fence, as CommonMark
// reads them: a run of three or more ` or ~ indented at most 3 spaces in
// its container (the document, or a list item) opens one; only a run of
// the same character, at least as long, with nothing after it, closes it;
// the end of its list item, or of the comment, closes it too. A fence
// inside a blockquote ends with the quote, so quoted lines open none.
func fenced(lines []string) bool {
	var fence byte // 0: outside any fence
	var length int // the opening run's length
	var cont int   // the column the fence's container starts at
	var item int   // the content column of the list item last opened, 0 for none
	for _, line := range lines {
		ind, rest := indent(line)
		if fence != 0 {
			if strings.TrimSpace(rest) == "" {
				continue
			}
			if cont == 0 || ind >= cont {
				if ind-cont <= 3 && closes(rest, fence, length) {
					fence = 0
				}
				continue
			}
			fence = 0 // its list item ended, and the fence with it
		}
		if strings.TrimSpace(rest) == "" {
			continue
		}
		rel := ind
		cont = 0
		if ind <= 3 && strings.HasPrefix(rest, ">") {
			continue
		}
		// A horizontal rule ("- - -", "***", "_ _ _") is neither a list
		// item nor inside one it does not indent into: it ends the list.
		if ind <= 3 && (item == 0 || ind < item) && thematicBreak(rest) {
			item = 0
			continue
		}
		if w, ok := listMarker(rest); ok && ind <= 3 {
			// Spaces past the marker's own are the content's indentation:
			// four or more make it indented code, not a fence.
			item = ind + w
			cont = item
			rel, rest = indent(rest[w:])
		} else if item > 0 && ind >= item {
			cont, rel = item, ind-item
		} else if ind < item {
			item = 0
		}
		if rel > 3 {
			continue
		}
		if c, n := run(rest); n >= 3 && (c == '~' || !strings.ContainsRune(rest[n:], '`')) {
			fence, length = c, n
		}
	}
	return fence != 0
}

// indent counts a line's leading spaces, a tab as up to the next multiple
// of 4, and returns the rest.
func indent(line string) (int, string) {
	n := 0
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case ' ':
			n++
		case '\t':
			n += 4 - n%4
		default:
			return n, line[i:]
		}
	}
	return n, ""
}

// run is the fence character a line starts with and how many of it.
func run(s string) (byte, int) {
	if s == "" || s[0] != '`' && s[0] != '~' {
		return 0, 0
	}
	n := 0
	for n < len(s) && s[n] == s[0] {
		n++
	}
	return s[0], n
}

// closes says a line (its indentation removed) closes a fence of c, length
// long: c at least as many times, then only whitespace.
func closes(s string, c byte, length int) bool {
	got, n := run(s)
	return got == c && n >= length && strings.TrimSpace(s[n:]) == ""
}

// thematicBreak says s (its indentation removed) is a horizontal rule:
// three or more of one of - * _, with only spaces or tabs between.
func thematicBreak(s string) bool {
	if s == "" || !strings.ContainsRune("-*_", rune(s[0])) {
		return false
	}
	n := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case s[0]:
			n++
		case ' ', '\t':
		default:
			return false
		}
	}
	return n >= 3
}

// listMarker is a list item's marker at the start of s ("- ", "* ", "+ ",
// "1. ", "1) "): its width, including the one space after it.
func listMarker(s string) (int, bool) {
	if len(s) >= 2 && strings.ContainsRune("-*+", rune(s[0])) && (s[1] == ' ' || s[1] == '\t') {
		return 2, true
	}
	d := 0
	for d < len(s) && d < 9 && s[d] >= '0' && s[d] <= '9' {
		d++
	}
	if d > 0 && d+1 < len(s) && (s[d] == '.' || s[d] == ')') && (s[d+1] == ' ' || s[d+1] == '\t') {
		return d + 2, true
	}
	return 0, false
}

// mentionLogin is what GitHub allows in a login: letters, digits, hyphens.
var mentionLogin = regexp.MustCompile(`^[A-Za-z0-9-]+$`)

// Mentions says a comment's body addresses dude: @dude, or @ one of the
// logins dude comments as, in any case, as a whole @-mention (not part of
// an e-mail address or a longer login).
func Mentions(body string, factoryLogins []string) bool {
	names := []string{regexp.QuoteMeta(DudeMention)}
	for _, l := range factoryLogins {
		if mentionLogin.MatchString(l) {
			names = append(names, regexp.QuoteMeta(l))
		}
	}
	re := regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9_.@/-])@(?:` + strings.Join(names, "|") + `)(?:$|[^A-Za-z0-9_-])`)
	return re.MatchString(body)
}

// AddressedToDude says a piece of feedback is a message to the task's
// conductor rather than feedback for a fixer: it mentions dude, and is not
// dude's own (a factory login's, a bot's, or a reply the conductor posted).
// Who may address it is the same rule as who may wake a fixer (MayWake),
// applied by the caller.
func AddressedToDude(f Feedback, factoryLogins []string) bool {
	if isBot(f.Author) || Own(f) || strings.TrimSpace(f.Body) == "" {
		return false
	}
	for _, login := range factoryLogins {
		if strings.EqualFold(f.Author, login) {
			return false
		}
	}
	return Mentions(f.Body, factoryLogins)
}

// Posted is a comment dude posted: its feedback id (as Feedback names
// comments) and where a person reads it.
type Posted struct {
	ID, URL string
}

type ghPosted struct {
	ID      int64  `json:"id"`
	HTMLURL string `json:"html_url"`
}

// Comment posts a comment on a pull request's conversation.
func (g *GitHub) Comment(ctx context.Context, slug string, number int, body string) (Posted, error) {
	var out ghPosted
	if err := g.do(ctx, "POST", fmt.Sprintf("/repos/%s/issues/%d/comments", slug, number), map[string]string{"body": body}, &out); err != nil {
		return Posted{}, err
	}
	return Posted{ID: fmt.Sprintf("issue-comment-%d", out.ID), URL: out.HTMLURL}, nil
}

// ReplyToLineComment answers a line comment in its review thread.
func (g *GitHub) ReplyToLineComment(ctx context.Context, slug string, number int, commentID int64, body string) (Posted, error) {
	var out ghPosted
	if err := g.do(ctx, "POST", fmt.Sprintf("/repos/%s/pulls/%d/comments/%d/replies", slug, number, commentID),
		map[string]string{"body": body}, &out); err != nil {
		return Posted{}, err
	}
	return Posted{ID: fmt.Sprintf("line-comment-%d", out.ID), URL: out.HTMLURL}, nil
}
