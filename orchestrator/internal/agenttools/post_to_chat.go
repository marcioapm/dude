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
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/chat"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

const (
	// postsPerRun bounds one run's posts.
	postsPerRun = 10
	// postTextMax is Slack's limit on a message's text, in characters.
	postTextMax = 40000
	// chatProvider is the provider post_to_chat posts through.
	chatProvider = "slack"
	// EvSlackPosted records a message an automation's agent posted.
	EvSlackPosted = "slack.message.posted"
	// EvSlackPostRefused records a post the checks passed and Slack, or the
	// channel lookup, refused: the run's log shows the call failed, and why.
	EvSlackPostRefused = "slack.message.refused"
)

// channelFinder is a provider that finds a channel's id by its name, and
// forgets what it found when a post there is refused.
type channelFinder interface {
	FindChannel(ctx context.Context, inst chat.Installation, name string) (id string, member, found bool, err error)
	ForgetChannel(inst chat.Installation, name string)
}

// channelID is a Slack conversation id as an agent may give one: C (public),
// G (private) or D (DM), then upper-case letters and digits.
var channelID = regexp.MustCompile(`^[CGD][A-Z0-9]{8,}$`)

type postToChatIn struct {
	Channel string `json:"channel"`
	Text    string `json:"text"`
}

// postTarget is the channel resolvePostChannel found, and how to post in it.
type postTarget struct {
	inst                   chat.Installation
	provider               chat.Provider
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

// textChecked is resolvePostChannel having checked the text's length and
// found no channel to post in: checkPost refuses the call.
type textChecked struct{}

// resolvePostChannel is post_to_chat's prepare step: the organisation's
// installation and the channel's id, read before the call's transaction
// opens. It answers a postTarget, a refusal (the text's length, or a channel
// that cannot be posted in: refused with no budget spent), an error,
// textChecked, or nil when checkPost refuses the call on its own (not an
// automation, no channel named).
func resolvePostChannel(ctx context.Context, c Caller, args json.RawMessage) any {
	var in postToChatIn
	if c.AutomationID == "" || json.Unmarshal(args, &in) != nil || c.env.db == nil {
		return nil
	}
	p, registered := c.env.chatProviders.Get(chatProvider)
	var inst chat.Installation
	var installed bool
	var made, recent int
	if err := c.env.db.InOrg(ctx, c.Org, func(tx pgx.Tx) (err error) {
		// The channel goes to Slack and to the ledger redacted, as Server.call
		// redacts every argument after this step.
		red, err := ledger.Redactor(ctx, tx, c.RunID)
		if err != nil {
			return err
		}
		in.Channel = red.String(in.Channel)
		if inst, installed, err = chat.Installed(ctx, tx, chatProvider); err != nil {
			return err
		}
		if recent, made, err = callsMade(ctx, tx, c.RunID, "post_to_chat"); err != nil {
			return err
		}
		// This step's refusals (tagged "lookup") write no call, so the minute's bound adds them here.
		var lookups int
		err = tx.QueryRow(ctx, `SELECT count(*) FROM events WHERE run_id = $1 AND event_type = $2
			AND payload->>'lookup' = 'true' AND occurred_at > now() - interval '1 minute'`, c.RunID, EvSlackPostRefused).Scan(&lookups)
		recent += lookups
		return err
	}); err != nil {
		return err
	}
	channel := postChannel(in.Channel)
	if channel == "" {
		return nil
	}
	// The agent's own text is measured, not its redacted copy.
	if err := checkText(in.Text); err != nil {
		return postRefused(channel, err.Error())
	}
	switch {
	case !registered, !installed, made >= postsPerRun:
		// checkPost refuses it, under the run's lock.
		return textChecked{}
	case recent >= callsPerMinute:
		return refuse(rTooManyCallsAtMostMinute, callsPerMinute)
	}
	shown := "#" + strings.TrimPrefix(channel, "#")
	target := postTarget{inst: inst, provider: p, channelID: channel, channelName: shown}
	if channelID.MatchString(channel) {
		info, err := p.Conversation(ctx, inst, channel)
		switch {
		case err == nil:
			target.channelName = info.Name
		case apiCode(err) == "channel_not_found":
			return refusedIn(channel, rPostUnknownChannel, channel)
		case revokedCodes[apiCode(err)]:
			return slackFailed(ctx, c, channel, err)
		default:
			// The post itself says whether the channel takes it; its name
			// unknown, the channel is shown as the id it was given.
			target.channelName = channel
			noteFailure(ctx, c, err)
		}
		return target
	}
	finder, ok := p.(channelFinder)
	if !ok {
		return refusedIn(channel, rPostUnknownChannel, shown)
	}
	id, member, found, err := finder.FindChannel(ctx, inst, channel)
	switch {
	case errors.Is(err, chat.ErrChannelLookupCut):
		return refusedIn(channel, rPostLookupCut, shown)
	case err != nil:
		return slackFailed(ctx, c, channel, err)
	case !found:
		return refusedIn(channel, rPostUnknownChannel, shown)
	case !member:
		return refusedIn(channel, rPostNotInChannel, shown)
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
			recorded = notConnected(in.Channel, unconnectedOrg)
		}
		return out, recorded
	}
	return out, err
}

// notConnected is the refusal of a post for want of a Slack installation
// for org, recorded with the code and reason the run fails on.
func notConnected(channel, org string) refusal {
	r := refusedIn(channel, rPostNotConnected, org).(refusal)
	r.event.payload["code"] = PostNotConnected
	r.event.payload["reason"] = NotConnectedReason(org)
	return r
}

// PostNotConnected is the code of a post refused because the organisation
// has no Slack installation: the automation run fails on it, with the
// refusal's "reason" (NotConnectedReason).
const PostNotConnected = "not_connected"

// NotConnectedReason is why a run that posted with no Slack installation
// for org failed.
func NotConnectedReason(org string) string { return "Slack is not connected for " + org }

// postRefused is a refusal recorded on the run as EvSlackPostRefused.
func postRefused(channel, msg string) error {
	return refuseRecorded(EvSlackPostRefused, map[string]any{"tool": "post_to_chat", "channel": channel, "error": msg}, msg)
}

// refusedIn is postRefused with a refusal format's words.
func refusedIn(channel, format string, a ...any) error {
	return postRefused(channel, fmt.Sprintf(format, a...))
}

func orgName(ctx context.Context, tx pgx.Tx, org string) (name string, err error) {
	err = tx.QueryRow(ctx, `SELECT name FROM organizations WHERE id = $1`, org).Scan(&name)
	return name, err
}

// checkPost checks the call in the run's transaction; unconnectedOrg is the
// organisation's name when it was refused for having no Slack installation.
func checkPost(ctx context.Context, tx pgx.Tx, c Caller, in postToChatIn) (out postChecked, unconnectedOrg string, err error) {
	out.postToChatIn = in
	if c.AutomationID == "" {
		return out, "", refuseText(rPostAutomationsOnly)
	}
	if out.Channel = postChannel(in.Channel); out.Channel == "" {
		return out, "", refuseText(rPostNameAChannel)
	}
	if c.env.prepared == nil {
		// resolvePostChannel, which measures the text before its redaction, did not run.
		if err := checkText(in.Text); err != nil {
			return out, "", err
		}
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
		return out, "", refuse(rPostPerRun, postsPerRun)
	}
	switch target := c.env.prepared.(type) {
	case error:
		return out, "", target
	case postTarget:
		out.postTarget = target
		return out, "", nil
	}
	org, err := orgName(ctx, tx, c.Org)
	if err != nil {
		return out, "", err
	}
	return out, org, refuse(rPostNotConnected, org)
}

// checkText refuses a post's text that is blank or longer than Slack takes.
func checkText(text string) error { return textWithin(text, postTextMax, rPostTextLength) }

// textWithin refuses text that is blank or longer than max characters, in
// format's words (max, then the text's length).
func textWithin(text string, max int, format string) error {
	if n := utf8.RuneCountInString(text); strings.TrimSpace(text) == "" || n > max {
		return refuse(format, max, n)
	}
	return nil
}

// outsideRefusals are the refusal formats of Slack's answers that the
// channel no longer takes the bot's posts.
var outsideRefusals = map[string]string{"channel_not_found": rPostUnknownChannel, "not_in_channel": rPostNotInChannel}

// postOutside posts, with no transaction open.
func postOutside(ctx context.Context, c Caller, in postChecked) (postDone, error) {
	ts, err := post(ctx, in)
	if err != nil {
		format, ok := outsideRefusals[apiCode(err)]
		if !ok {
			return postDone{}, slackFailed(ctx, c, in.Channel, err)
		}
		// The remembered lookup is stale: the next call looks the channel up again.
		if finder, ok := in.provider.(channelFinder); ok {
			finder.ForgetChannel(in.inst, in.Channel)
		}
		return postDone{}, refusedIn(in.Channel, format, in.channelName)
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

// revokedCodes are Slack's answers for an installation that no longer
// works: the post is refused as not connected, and the run fails on it.
var revokedCodes = map[string]bool{"invalid_auth": true, "token_revoked": true, "account_inactive": true}

// slackFailed is a provider error, counted for the installation's health
// and said to the agent: a revoked installation as not connected, a
// transient error to try again, any other with Slack's own code.
func slackFailed(ctx context.Context, c Caller, channel string, err error) error {
	noteFailure(ctx, c, err)
	if revokedCodes[apiCode(err)] {
		var org string
		if qerr := c.env.db.InOrg(ctx, c.Org, func(tx pgx.Tx) (err error) {
			org, err = orgName(ctx, tx, c.Org)
			return err
		}); qerr != nil {
			return qerr
		}
		return notConnected(channel, org)
	}
	format := rPostSlackRefused
	if t := (*chat.TransientError)(nil); errors.As(err, &t) {
		format = rPostSlackBusy
	}
	return refusedIn(channel, format, err.Error())
}

// noteFailure counts err for the installation's health, as the chat
// service counts its own calls' failures.
func noteFailure(ctx context.Context, c Caller, err error) {
	if c.env.db != nil {
		chat.NoteFailure(ctx, c.env.db, nil, c.Org, chatProvider, err)
	}
}

// apiCode is the provider's error code ("not_in_channel"), or "" for an
// error that carries none (slack.APIError's ErrorCode).
func apiCode(err error) string {
	var coded interface{ ErrorCode() string }
	if errors.As(err, &coded) {
		return coded.ErrorCode()
	}
	return ""
}

func recordPostToChat(ctx context.Context, tx pgx.Tx, c Caller, in postChecked, done postDone) (map[string]any, error) {
	out := map[string]any{"channel": in.channelName, "channelId": in.channelID, "ts": done.ts, "permalink": done.permalink}
	_, err := ledger.Append(ctx, tx, c.event(EvSlackPosted, map[string]any{"provider": chatProvider, "channel": in.channelName,
		"channelId": in.channelID, "ts": done.ts, "permalink": done.permalink}))
	return out, err
}

var postToChatTool = around(define("post_to_chat", checkPostToChat).before(lookedUp(resolvePostChannel)).countingAttempts(), postOutside, recordPostToChat)

// lookedUp tags each refusal of prepare "lookup": it writes no call, so
// resolvePostChannel adds it to the minute's calls.
func lookedUp(prepare func(context.Context, Caller, json.RawMessage) any) func(context.Context, Caller, json.RawMessage) any {
	return func(ctx context.Context, c Caller, args json.RawMessage) any {
		prepared := prepare(ctx, c, args)
		if r, ok := prepared.(refusal); ok && r.event != nil {
			r.event.payload["lookup"] = true
		}
		return prepared
	}
}
