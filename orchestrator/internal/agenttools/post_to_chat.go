package agenttools

// post_to_chat: an automation's agent posts Slack mrkdwn to a channel, as
// the organisation's chat installation (its bot), at most postsPerRun times
// a run. Its text was redacted with the run's secret values before the call
// reached the tool (Server.call), so no secret value leaves dude.

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/chat"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// ChatProviders are the chat providers post_to_chat posts through (the
// orchestrator's chat.Registry); without "slack" every call is refused as
// not connected.
type ChatProviders = chat.Registry

const (
	// postsPerRun bounds one run's posts.
	postsPerRun = 10
	// postTextMax is Slack's limit on a message's text, in characters.
	postTextMax = 40000
	// chatProvider is the provider post_to_chat posts through.
	chatProvider = "slack"
	// EvChatPosted records a message an automation's agent posted.
	EvChatPosted = "chat.message.posted"
	// EvChatPostRefused records a post the checks passed and Slack, or the
	// channel lookup, refused: the run's log shows the call failed, and why.
	EvChatPostRefused = "chat.message.refused"
)

// channelFinder is a provider that finds a channel's id by its name.
type channelFinder interface {
	FindChannel(ctx context.Context, inst chat.Installation, name string) (id string, member, found bool, err error)
}

// channelID is a Slack conversation id as an agent may give one: C (public),
// G (private) or D (DM), then upper-case letters and digits.
var channelID = regexp.MustCompile(`^[CGD][A-Z0-9]{8,}$`)

var postRefusals = func() map[string]delivery.Text {
	out := map[string]delivery.Text{}
	for _, name := range []string{"automations-only", "name-a-channel", "text-length", "per-run", "not-connected", "unknown-channel",
		"not-in-channel", "slack-busy", "slack-refused"} {
		out[name] = delivery.MustText("refusals/post-to-chat/" + name)
	}
	return out
}()

// postRefusal is a refusal template's format.
func postRefusal(name string) string { return postRefusals[name].Render(nil) }

type postToChatIn struct {
	Channel string `json:"channel"`
	Text    string `json:"text"`
}

type postChecked struct {
	postToChatIn
	inst         chat.Installation
	provider     chat.Provider
	notConnected bool
}

type postDone struct {
	channelID, channelName, ts, permalink string
}

func checkPostToChat(ctx context.Context, tx pgx.Tx, c Caller, in postToChatIn) (postChecked, error) {
	out, err := checkPost(ctx, tx, c, in)
	var refused refusal
	if errors.As(err, &refused) && refused.event == nil && c.AutomationID != "" {
		// Recorded once the call's transaction is gone: the run's log shows
		// the refused call and why.
		recorded := postRefused(in.Channel, refused.msg).(refusal)
		if out.notConnected {
			recorded.event.payload["code"] = PostNotConnected
		}
		return out, recorded
	}
	return out, err
}

// PostNotConnected is the code of a post refused because the organisation
// has no Slack installation: the automation run fails on it.
const PostNotConnected = "not_connected"

// postRefused is a refusal recorded on the run as EvChatPostRefused.
func postRefused(channel, msg string) error {
	return refuseRecorded(EvChatPostRefused, map[string]any{"tool": "post_to_chat", "channel": channel, "error": msg}, msg)
}

func checkPost(ctx context.Context, tx pgx.Tx, c Caller, in postToChatIn) (postChecked, error) {
	out := postChecked{postToChatIn: in}
	if c.AutomationID == "" {
		return out, refuseText(postRefusal("automations-only"))
	}
	out.Channel = strings.TrimSpace(in.Channel)
	if strings.TrimPrefix(out.Channel, "#") == "" || strings.ContainsAny(out.Channel, " \n\t") {
		return out, refuseText(postRefusal("name-a-channel"))
	}
	if n := utf8.RuneCountInString(in.Text); strings.TrimSpace(in.Text) == "" || n > postTextMax {
		return out, refuse(postRefusal("text-length"), postTextMax, n)
	}
	// The run's row, locked, serialises its calls: two at once cannot both
	// take its last post. Counted as tool.limit counts (callsMade, every
	// attempt once), checked here so the refusal says "messages per run".
	if _, err := tx.Exec(ctx, `SELECT 1 FROM automation_runs WHERE id = $1 FOR UPDATE`, c.RunID); err != nil {
		return out, err
	}
	_, made, err := callsMade(ctx, tx, c.RunID, "post_to_chat")
	if err != nil {
		return out, err
	}
	if made >= postsPerRun {
		return out, refuse(postRefusal("per-run"), postsPerRun)
	}
	inst, ok, err := chat.Installed(ctx, tx, chatProvider)
	if err != nil {
		return out, err
	}
	p, registered := c.env.chat.Get(chatProvider)
	if !ok || !registered {
		var org string
		if err := tx.QueryRow(ctx, `SELECT name FROM organizations WHERE id = $1`, c.Org).Scan(&org); err != nil {
			return out, err
		}
		out.notConnected = true
		return out, refuse(postRefusal("not-connected"), org)
	}
	out.inst, out.provider = inst, p
	return out, nil
}

// postOutside finds the channel and posts, with no transaction open.
func postOutside(ctx context.Context, c Caller, in postChecked) (postDone, error) {
	refused := func(name string, a ...any) error {
		return postRefused(in.Channel, refuse(postRefusal(name), a...).Error())
	}
	shown := "#" + strings.TrimPrefix(in.Channel, "#")
	done := postDone{channelID: in.Channel, channelName: shown}
	if channelID.MatchString(in.Channel) {
		if info, err := in.provider.Conversation(ctx, in.inst, in.Channel); err == nil {
			done.channelName = info.Name
		} else if apiCode(err) == "channel_not_found" {
			return done, refused("unknown-channel", in.Channel)
		}
	} else {
		finder, ok := in.provider.(channelFinder)
		if !ok {
			return done, refused("unknown-channel", shown)
		}
		id, member, found, err := finder.FindChannel(ctx, in.inst, in.Channel)
		switch {
		case err != nil:
			return done, slackFailed(refused, err)
		case !found:
			return done, refused("unknown-channel", shown)
		case !member:
			return done, refused("not-in-channel", shown)
		}
		done.channelID = id
	}
	ts, err := post(ctx, in, done.channelID)
	if err != nil {
		switch apiCode(err) {
		case "channel_not_found":
			return done, refused("unknown-channel", done.channelName)
		case "not_in_channel":
			return done, refused("not-in-channel", done.channelName)
		}
		return done, slackFailed(refused, err)
	}
	done.ts, done.permalink = ts, in.provider.Permalink(in.inst, done.channelID, ts, "")
	return done, nil
}

// textPoster is a provider that posts text past what one of PostReply's
// blocks takes (blockMax characters).
type textPoster interface {
	PostText(ctx context.Context, inst chat.Installation, conversation, text string) (string, error)
}

// blockMax is what one Block Kit section's text takes (Slack's limit).
const blockMax = 3000

// post posts in.Text with no thread: through PostReply, or, longer than a
// block takes, as the message's plain mrkdwn text.
func post(ctx context.Context, in postChecked, channel string) (string, error) {
	if p, ok := in.provider.(textPoster); ok && utf8.RuneCountInString(in.Text) > blockMax {
		return p.PostText(ctx, in.inst, channel, in.Text)
	}
	return in.provider.PostReply(ctx, in.inst, channel, "", chat.Reply{Text: in.Text})
}

// slackFailed is a provider error said to the agent: a transient one to
// try again, any other with Slack's own code.
func slackFailed(refused func(string, ...any) error, err error) error {
	if t := (*chat.TransientError)(nil); errors.As(err, &t) {
		return refused("slack-busy", err.Error())
	}
	return refused("slack-refused", err.Error())
}

// apiCode is the provider's error code ("not_in_channel"), the last word
// of its error ("slack chat.postMessage: not_in_channel"), or "".
func apiCode(err error) string {
	msg := err.Error()
	if i := strings.LastIndex(msg, ": "); i >= 0 && !strings.Contains(msg[i+2:], " ") {
		return msg[i+2:]
	}
	return ""
}

func recordPostToChat(ctx context.Context, tx pgx.Tx, c Caller, in postChecked, done postDone) (map[string]any, error) {
	out := map[string]any{"channel": done.channelName, "channelId": done.channelID, "ts": done.ts, "permalink": done.permalink}
	_, err := ledger.Append(ctx, tx, c.event(EvChatPosted, map[string]any{"provider": chatProvider, "channel": done.channelName,
		"channelId": done.channelID, "ts": done.ts, "permalink": done.permalink}))
	return out, err
}

var postToChatTool = around(define("post_to_chat", checkPostToChat).countingAttempts(), postOutside, recordPostToChat)
