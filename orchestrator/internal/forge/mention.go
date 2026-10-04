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

// Own says the feedback is a comment the conductor posted.
func Own(f Feedback) bool { return strings.Contains(f.Body, ReplyMarker) }

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
