package agenttools

// post_to_chat: an automation's agent posts Slack mrkdwn to a channel, as
// the organisation's chat installation (its bot), at most postsPerRun times
// a run. Its text was redacted with the run's secret values before the call
// reached the tool (Server.call), so no secret value leaves dude.
//
// The call has three steps. resolvePostChannel (the tool's prepare step, no
// transaction open) finds the channel; a channel the bot cannot post in is
// refused there, before any budget is spent. checkPostToChat, in the call's
// transaction, checks the text and the run's ten and writes the attempt.
// postOutside calls chat.postMessage with no transaction open.

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/chat"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

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

// postTarget is the channel resolvePostChannel found, and how to post in it.
type postTarget struct {
	inst                  chat.Installation
	provider              chat.Provider
	channelID, channelName string
}

type postChecked struct {
	postToChatIn
	postTarget
}

type postDone struct {
	ts, permalink string
}

// postChannel is the channel as the agent named it, trimmed; "" for one
// that names nothing.
func postChannel(raw string) string {
	ch := strings.TrimSpace(raw)
	if strings.TrimPrefix(ch, "#") == "" || strings.ContainsAny(ch, " \n\t") {
		return ""
	}
	return ch
}

// resolvePostChannel is post_to_chat's prepare step: the organisation's
// installation and the channel's id, read before the call's transaction
// opens. It answers a postTarget, a refusal (the channel cannot be posted
// in: refused with no budget spent), an error, or nil when checkPost refuses
// the call on its own (no Slack, not an automation, no channel named).
func resolvePostChannel(ctx context.Context, c Caller, args json.RawMessage) any {
	var in postToChatIn
	if c.AutomationID == "" || json.Unmarshal(args, &in) != nil || c.env.db == nil {
		return nil
	}
	channel := postChannel(in.Channel)
	p, registered := c.env.chat.Get(chatProvider)
	if channel == "" || !registered {
		return nil
	}
	var inst chat.Installation
	var installed bool
	var made, recent int
	if err := c.env.db.InOrg(ctx, c.Org, func(tx pgx.Tx) (err error) {
		if inst, installed, err = chat.Installed(ctx, tx, chatProvider); err != nil {
			return err
		}
		if recent, made, err = callsMade(ctx, tx, c.RunID, "post_to_chat"); err != nil {
			return err
		}
		// Lookup refusals write no call, so the minute's bound counts them here.
		var lookups int
		err = tx.QueryRow(ctx, `SELECT count(*) FROM events WHERE run_id = $1 AND event_type = $2
			AND occurred_at > now() - interval '1 minute'`, c.RunID, EvChatPostRefused).Scan(&lookups)
		recent += lookups
		return err
	}); err != nil {
		return err
	}
	switch {
	case !installed, made >= postsPerRun:
		// checkPost refuses it, under the run's lock.
		return nil
	case recent >= callsPerMinute:
		return refuse(rTooManyCallsAtMostMinute, callsPerMinute)
	}
	refused := func(name string, a ...any) error {
		return postRefused(channel, refuse(postRefusal(name), a...).Error())
	}
	shown := "#" + strings.TrimPrefix(channel, "#")
	target := postTarget{inst: inst, provider: p, channelID: channel, channelName: shown}
	if channelID.MatchString(channel) {
		if info, err := p.Conversation(ctx, inst, channel); err == nil {
			target.channelName = info.Name
		} else if apiCode(err) == "channel_not_found" {
			return refused("unknown-channel", channel)
		}
		return target
	}
	finder, ok := p.(channelFinder)
	if !ok {
		return refused("unknown-channel", shown)
	}
	id, member, found, err := finder.FindChannel(ctx, inst, channel)
	switch {
	case err != nil:
		return slackFailed(refused, err)
	case !found:
		return refused("unknown-channel", shown)
	case !member:
		return refused("not-in-channel", shown)
	}
	target.channelID = id
	return target
}

func checkPostToChat(ctx context.Context, tx pgx.Tx, c Caller, in postToChatIn) (postChecked, error) {
	out, unconnectedOrg, err := checkPost(ctx, tx, c, in)
	var refused refusal
	if errors.As(err, &refused) && refused.event == nil && c.AutomationID != "" {
		// Recorded once the call's transaction is gone: the run's log shows
		// the refused call and why.
		recorded := postRefused(in.Channel, refused.msg).(refusal)
		if unconnectedOrg != "" {
			recorded.event.payload["code"] = PostNotConnected
			recorded.event.payload["reason"] = NotConnectedReason(unconnectedOrg)
		}
		return out, recorded
	}
	return out, err
}

// PostNotConnected is the code of a post refused because the organisation
// has no Slack installation: the automation run fails on it, with the
// refusal's "reason" (NotConnectedReason).
const PostNotConnected = "not_connected"

// NotConnectedReason is why a run that posted with no Slack installation
// for org failed.
func NotConnectedReason(org string) string { return "Slack is not connected for " + org }

// postRefused is a refusal recorded on the run as EvChatPostRefused.
func postRefused(channel, msg string) error {
	return refuseRecorded(EvChatPostRefused, map[string]any{"tool": "post_to_chat", "channel": channel, "error": msg}, msg)
}

// checkPost checks the call in the run's transaction; unconnectedOrg is the
// organisation's name when it was refused for having no Slack installation.
func checkPost(ctx context.Context, tx pgx.Tx, c Caller, in postToChatIn) (out postChecked, unconnectedOrg string, err error) {
	out.postToChatIn = in
	if c.AutomationID == "" {
		return out, "", refuseText(postRefusal("automations-only"))
	}
	if out.Channel = postChannel(in.Channel); out.Channel == "" {
		return out, "", refuseText(postRefusal("name-a-channel"))
	}
	if n := utf8.RuneCountInString(in.Text); strings.TrimSpace(in.Text) == "" || n > postTextMax {
		return out, "", refuse(postRefusal("text-length"), postTextMax, n)
	}
	// The run's row, locked, serialises its calls: two at once cannot both
	// take its last post. Counted as tool.limit counts (callsMade, every
	// attempt once), checked here so the refusal says "messages per run".
	if _, err := tx.Exec(ctx, `SELECT 1 FROM automation_runs WHERE id = $1 FOR UPDATE`, c.RunID); err != nil {
		return out, "", err
	}
	_, made, err := callsMade(ctx, tx, c.RunID, "post_to_chat")
	if err != nil {
		return out, "", err
	}
	if made >= postsPerRun {
		return out, "", refuse(postRefusal("per-run"), postsPerRun)
	}
	switch target := c.env.prepared.(type) {
	case error:
		return out, "", target
	case postTarget:
		out.postTarget = target
		return out, "", nil
	}
	var org string
	if err := tx.QueryRow(ctx, `SELECT name FROM organizations WHERE id = $1`, c.Org).Scan(&org); err != nil {
		return out, "", err
	}
	return out, org, refuse(postRefusal("not-connected"), org)
}

// postOutside posts, with no transaction open.
func postOutside(ctx context.Context, c Caller, in postChecked) (postDone, error) {
	refused := func(name string, a ...any) error {
		return postRefused(in.Channel, refuse(postRefusal(name), a...).Error())
	}
	ts, err := post(ctx, in)
	if err != nil {
		switch apiCode(err) {
		case "channel_not_found":
			return postDone{}, refused("unknown-channel", in.channelName)
		case "not_in_channel":
			return postDone{}, refused("not-in-channel", in.channelName)
		}
		return postDone{}, slackFailed(refused, err)
	}
	return postDone{ts: ts, permalink: in.provider.Permalink(in.inst, in.channelID, ts, "")}, nil
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
func post(ctx context.Context, in postChecked) (string, error) {
	if p, ok := in.provider.(textPoster); ok && utf8.RuneCountInString(in.Text) > blockMax {
		return p.PostText(ctx, in.inst, in.channelID, in.Text)
	}
	return in.provider.PostReply(ctx, in.inst, in.channelID, "", chat.Reply{Text: in.Text})
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
	out := map[string]any{"channel": in.channelName, "channelId": in.channelID, "ts": done.ts, "permalink": done.permalink}
	_, err := ledger.Append(ctx, tx, c.event(EvChatPosted, map[string]any{"provider": chatProvider, "channel": in.channelName,
		"channelId": in.channelID, "ts": done.ts, "permalink": done.permalink}))
	return out, err
}

var postToChatTool = around(define("post_to_chat", checkPostToChat).before(resolvePostChannel).countingAttempts(), postOutside, recordPostToChat)
