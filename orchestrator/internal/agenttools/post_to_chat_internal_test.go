package agenttools

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/chat"
	"github.com/marciomartins/dude/orchestrator/internal/chat/slack"
)

type replyOnlyPoster struct {
	chat.Provider
	calls                int
	inst                 chat.Installation
	conversation, thread string
	reply                chat.Reply
}

func (p *replyOnlyPoster) PostReply(_ context.Context, inst chat.Installation, conversation, thread string, reply chat.Reply) (string, error) {
	p.calls++
	p.inst, p.conversation, p.thread, p.reply = inst, conversation, thread, reply
	return "123.456", nil
}

func TestPostFallsBackToPostReplyWithoutTextPoster(t *testing.T) {
	for _, length := range []int{44, 3001} {
		t.Run(fmt.Sprint(length), func(t *testing.T) {
			p := &replyOnlyPoster{}
			inst := chat.Installation{ID: "chi_acme"}
			text := "<https://github.com/acme/repo/pull/1|repo#1>" + strings.Repeat("x", length-44)
			ts, err := post(context.Background(), postChecked{
				postToChatIn: postToChatIn{Text: text},
				postTarget:   postTarget{provider: p, inst: inst, channelID: "C0ENGINEER"},
			})
			if err != nil || ts != "123.456" || p.calls != 1 || p.inst.ID != inst.ID || p.conversation != "C0ENGINEER" || p.thread != "" || p.reply.Text != text {
				t.Fatalf("fallback: ts %q, error %v, provider %+v", ts, err, p)
			}
		})
	}
}

// Slack's code is read from the error's type, however the error is worded
// or wrapped.
func TestAPICodeIsSlacksCodeWhateverTheErrorSays(t *testing.T) {
	api := &slack.APIError{Method: "chat.postMessage", Code: "not_in_channel"}
	for _, tc := range []struct {
		err  error
		want string
	}{
		{api, "not_in_channel"},
		{fmt.Errorf("%w (after 2 tries)", api), "not_in_channel"},
		{chat.Transient(&slack.APIError{Method: "chat.postMessage", Code: "ratelimited"}, 0), "ratelimited"},
		{errors.New("slack chat.postMessage: not_in_channel"), ""},
	} {
		if got := apiCode(tc.err); got != tc.want {
			t.Errorf("apiCode(%q) = %q, want %q", tc.err, got, tc.want)
		}
	}
}
