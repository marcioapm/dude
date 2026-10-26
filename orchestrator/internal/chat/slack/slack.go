// Package slack is the chat.Provider for Slack: the Events API and
// interactivity webhooks, the Web API calls dude makes, and the OAuth v2
// install and Sign in with Slack (OpenID Connect) link. Nothing outside
// this package and the provider's own settings knows it is Slack.
package slack

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/chat"
	"github.com/marciomartins/dude/orchestrator/internal/redact"
)

// DefaultAPIURL is Slack's Web API.
const DefaultAPIURL = "https://slack.com/api"

// MaxSkew is how old (or how far ahead) a signed request's timestamp may be.
const MaxSkew = 5 * time.Minute

// BotScopes are what the app asks for at install: reading mentions and
// DMs, the threads they are in and who is in a conversation (channels:read,
// groups:read, mpim:read for conversations.members), and writing replies
// and reactions.
var BotScopes = []string{"app_mentions:read", "channels:history", "groups:history", "im:history", "mpim:history",
	"channels:read", "groups:read", "im:read", "mpim:read", "chat:write", "reactions:write", "users:read"}

// Config is the Slack app dude runs as.
type Config struct {
	SigningSecret, ClientID, ClientSecret string
	// The Web API's base; DefaultAPIURL when empty. Its parent is where
	// the browser is sent to authorize (…/oauth/v2/authorize).
	APIURL string
	HTTP   *http.Client
}

// Provider is Slack.
type Provider struct {
	cfg      Config
	channels channelCache
}

// New is the Slack provider for cfg.
func New(cfg Config) *Provider {
	if cfg.APIURL == "" {
		cfg.APIURL = DefaultAPIURL
	}
	cfg.APIURL = strings.TrimRight(cfg.APIURL, "/")
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{Timeout: 10 * time.Second}
	}
	return &Provider{cfg: cfg}
}

var _ chat.Provider = (*Provider)(nil)

// Configured is whether the app's credentials are set: without them Slack
// is listed but cannot be installed.
func (p *Provider) Configured() bool {
	return p.cfg.ClientID != "" && p.cfg.ClientSecret != "" && p.cfg.SigningSecret != ""
}

// Info is Slack as shared surfaces draw it.
func (p *Provider) Info() chat.Info {
	return chat.Info{ID: "slack", Name: "Slack", Glyph: "slack", WorkspaceWord: "workspace"}
}

// RequestHeaders are what Slack signs a delivery with.
func (p *Provider) RequestHeaders() []string {
	return []string{"X-Slack-Request-Timestamp", "X-Slack-Signature", "Content-Type"}
}

// VerifyRequest checks Slack's v0 signature over "v0:<timestamp>:<body>"
// with the signing secret, and that the timestamp is within MaxSkew of now:
// a replayed request is refused as an unsigned one is.
func (p *Provider) VerifyRequest(h http.Header, body []byte, now time.Time) error {
	if p.cfg.SigningSecret == "" {
		return fmt.Errorf("%w: no signing secret is configured", chat.ErrUnverified)
	}
	ts := h.Get("X-Slack-Request-Timestamp")
	secs, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return fmt.Errorf("%w: no timestamp", chat.ErrUnverified)
	}
	if d := now.Sub(time.Unix(secs, 0)); d > MaxSkew || d < -MaxSkew {
		return fmt.Errorf("%w: the timestamp is %s from now", chat.ErrUnverified, d.Round(time.Second))
	}
	given, ok := strings.CutPrefix(h.Get("X-Slack-Signature"), "v0=")
	sig, err := hex.DecodeString(given)
	if !ok || err != nil {
		return fmt.Errorf("%w: no v0 signature", chat.ErrUnverified)
	}
	if !hmac.Equal(sig, Sign(p.cfg.SigningSecret, ts, body)) {
		return fmt.Errorf("%w: the signature does not match", chat.ErrUnverified)
	}
	return nil
}

// Sign is Slack's v0 signature of body at ts.
func Sign(secret, ts string, body []byte) []byte {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "v0:%s:", ts)
	mac.Write(body)
	return mac.Sum(nil)
}

type envelope struct {
	Type           string `json:"type"`
	Challenge      string `json:"challenge"`
	TeamID         string `json:"team_id"`
	EventID        string `json:"event_id"`
	Authorizations []struct {
		UserID string `json:"user_id"`
	} `json:"authorizations"`
	Event struct {
		Type        string `json:"type"`
		Subtype     string `json:"subtype"`
		User        string `json:"user"`
		BotID       string `json:"bot_id"`
		Text        string `json:"text"`
		TS          string `json:"ts"`
		ThreadTS    string `json:"thread_ts"`
		Channel     string `json:"channel"`
		ChannelType string `json:"channel_type"`
		Tokens      struct {
			OAuth []string `json:"oauth"`
			Bot   []string `json:"bot"`
		} `json:"tokens"`
	} `json:"event"`
}

var mentionRE = regexp.MustCompile(`<@[UW][A-Z0-9]+(\|[^>]*)?>`)

// ParseEvent reads an Events API delivery. app_mention, in a channel or a
// thread, and a person's message in a DM with the app are mentions; the
// app's removal and revoked tokens are their own kinds; anything else,
// including bots' messages and a channel message (its mention comes as
// app_mention, and the thread's other messages are read at the next
// mention), is ignored.
func (p *Provider) ParseEvent(body []byte) (chat.Event, error) {
	var e envelope
	if err := json.Unmarshal(body, &e); err != nil {
		return chat.Event{}, fmt.Errorf("an event is JSON: %w", err)
	}
	switch e.Type {
	case "url_verification":
		return chat.Event{Kind: chat.EventURLVerification, Challenge: e.Challenge}, nil
	case "event_callback":
	default:
		return chat.Event{Kind: chat.EventIgnored}, nil
	}
	if e.EventID == "" || e.TeamID == "" {
		return chat.Event{}, errors.New("an event names its event_id and team_id")
	}
	out := chat.Event{Kind: chat.EventIgnored, ID: e.EventID, WorkspaceID: e.TeamID}
	ev := e.Event
	switch ev.Type {
	case "app_uninstalled":
		out.Kind = chat.EventUninstalled
		return out, nil
	case "tokens_revoked":
		// A person's sign-in token is revoked as well as the bot's: only
		// the bot's ends the installation.
		out.RevokedBots, out.RevokedUsers = ev.Tokens.Bot, ev.Tokens.OAuth
		if len(out.RevokedBots) == 0 && len(out.RevokedUsers) == 0 {
			return out, nil
		}
		out.Kind = chat.EventTokensRevoked
		return out, nil
	case "app_mention":
	case "message":
		if ev.ChannelType != "im" {
			return out, nil
		}
	default:
		return out, nil
	}
	if ev.BotID != "" || ev.User == "" || (ev.Subtype != "" && ev.Subtype != "thread_broadcast") {
		return out, nil
	}
	thread := ev.ThreadTS
	if thread == "" {
		thread = ev.TS
	}
	out.Kind = chat.EventMention
	out.Mention = &chat.Mention{Provider: "slack", WorkspaceID: e.TeamID, EventID: e.EventID, ConversationID: ev.Channel,
		ThreadID: thread, MessageID: ev.TS, UserID: ev.User, Text: StripMentions(ev.Text), Direct: ev.ChannelType == "im"}
	return out, nil
}

// StripMentions is text without Slack's user mentions (the bot's handle
// among them), spaces collapsed.
func StripMentions(text string) string {
	return strings.Join(strings.Fields(mentionRE.ReplaceAllString(text, " ")), " ")
}

// ParseInteraction reads an interactivity delivery (form field payload): a
// block_actions click on one of dude's buttons.
func (p *Provider) ParseInteraction(body []byte) (chat.Interaction, error) {
	form, err := url.ParseQuery(string(body))
	if err != nil || form.Get("payload") == "" {
		return chat.Interaction{}, errors.New("an interaction is a form with a payload")
	}
	var in struct {
		Type string `json:"type"`
		Team struct {
			ID string `json:"id"`
		} `json:"team"`
		User struct {
			ID       string `json:"id"`
			Username string `json:"username"`
			Name     string `json:"name"`
		} `json:"user"`
		Container struct {
			ChannelID string `json:"channel_id"`
			MessageTS string `json:"message_ts"`
			ThreadTS  string `json:"thread_ts"`
		} `json:"container"`
		Message struct {
			TS       string `json:"ts"`
			ThreadTS string `json:"thread_ts"`
		} `json:"message"`
		Actions []struct {
			Value    string `json:"value"`
			ActionTS string `json:"action_ts"`
		} `json:"actions"`
	}
	if err := json.Unmarshal([]byte(form.Get("payload")), &in); err != nil {
		return chat.Interaction{}, fmt.Errorf("an interaction's payload is JSON: %w", err)
	}
	if in.Type != "block_actions" || len(in.Actions) == 0 || in.Team.ID == "" || in.User.ID == "" {
		return chat.Interaction{}, errors.New("not a click on a button")
	}
	a := in.Actions[0]
	thread := firstOf(in.Message.ThreadTS, in.Container.ThreadTS, in.Message.TS, in.Container.MessageTS)
	return chat.Interaction{ID: in.Team.ID + ":" + in.User.ID + ":" + a.ActionTS, WorkspaceID: in.Team.ID,
		ConversationID: in.Container.ChannelID, ThreadID: thread, MessageID: firstOf(in.Container.MessageTS, in.Message.TS),
		User:  chat.ExternalUser{ID: in.User.ID, WorkspaceID: in.Team.ID, Handle: in.User.Username, DisplayName: in.User.Name},
		Value: a.Value}, nil
}

func firstOf(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}

// APIError is Slack's ok:false answer.
type APIError struct{ Method, Code string }

func (e *APIError) Error() string { return fmt.Sprintf("slack %s: %s", e.Method, e.Code) }

// jsonMethods send blocks as JSON; other methods use form encoding
// because Slack reads reject JSON with invalid_arguments.
var jsonMethods = map[string]bool{"chat.postMessage": true, "chat.postEphemeral": true, "chat.update": true}

// call POSTs one Web API method, as JSON for jsonMethods and form-encoded
// otherwise, with token as a bearer (none for the OAuth exchanges, which
// take client credentials), and decodes the answer into out, an ok:false
// into an *APIError. Its error never carries
// the token or the app's secrets, however Slack's answer echoes them: it
// is logged and kept as the installation's last failure.
func (p *Provider) call(ctx context.Context, token, method string, args map[string]any, out any) error {
	return p.scrub(token, p.send(ctx, token, method, args, out))
}

// scrub is err with the token and the app's secrets redacted (as
// [REDACTED:bot_token] and the like), keeping whether it is transient.
func (p *Provider) scrub(token string, err error) error {
	if err == nil {
		return nil
	}
	red := redact.FromList([]redact.Secret{{Name: "bot_token", Value: token},
		{Name: "slack.client_secret", Value: p.cfg.ClientSecret}, {Name: "slack.signing_secret", Value: p.cfg.SigningSecret}})
	msg := err.Error()
	clean := red.String(msg)
	if clean == msg {
		return err
	}
	var apiErr *APIError
	scrubbed := error(errors.New(clean))
	if errors.As(err, &apiErr) {
		scrubbed = &APIError{Method: apiErr.Method, Code: red.String(apiErr.Code)}
	}
	if t := (*chat.TransientError)(nil); errors.As(err, &t) {
		return chat.Transient(scrubbed, t.RetryAfter)
	}
	return scrubbed
}

func (p *Provider) send(ctx context.Context, token, method string, args map[string]any, out any) error {
	var body io.Reader
	contentType := "application/json; charset=utf-8"
	if !jsonMethods[method] {
		form := url.Values{}
		for k, v := range args {
			form.Set(k, fmt.Sprint(v))
		}
		body, contentType = strings.NewReader(form.Encode()), "application/x-www-form-urlencoded"
	} else {
		raw, err := json.Marshal(args)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.APIURL+"/"+method, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", contentType)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := p.cfg.HTTP.Do(req)
	if err != nil {
		return chat.Transient(fmt.Errorf("slack %s: %w", method, err), 0)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return chat.Transient(fmt.Errorf("slack %s: %w", method, err), 0)
	}
	if res.StatusCode == http.StatusTooManyRequests || res.StatusCode/100 == 5 {
		return chat.Transient(fmt.Errorf("slack %s answered %d", method, res.StatusCode), retryAfter(res.Header))
	}
	if res.StatusCode/100 != 2 {
		return fmt.Errorf("slack %s answered %d", method, res.StatusCode)
	}
	var status struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &status); err != nil {
		return fmt.Errorf("slack %s: the answer is not JSON: %w", method, err)
	}
	if !status.OK {
		apiErr := &APIError{Method: method, Code: status.Error}
		switch status.Error {
		case "ratelimited", "internal_error", "fatal_error", "service_unavailable", "request_timeout":
			return chat.Transient(apiErr, retryAfter(res.Header))
		}
		return apiErr
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

// retryAfter is Slack's Retry-After, in seconds; 0 without one.
func retryAfter(h http.Header) time.Duration {
	secs, err := strconv.Atoi(strings.TrimSpace(h.Get("Retry-After")))
	if err != nil || secs < 0 {
		return 0
	}
	return time.Duration(secs) * time.Second
}

// FetchThread is the thread's messages after since (all with since ""),
// oldest first, following Slack's cursor, at most 200.
func (p *Provider) FetchThread(ctx context.Context, inst chat.Installation, conversation, thread, since string) ([]chat.ThreadMessage, error) {
	var out []chat.ThreadMessage
	cursor := ""
	for len(out) < 200 {
		args := map[string]any{"channel": conversation, "ts": thread, "limit": 100}
		if cursor != "" {
			args["cursor"] = cursor
		}
		if since != "" {
			args["oldest"], args["inclusive"] = since, false
		}
		var page struct {
			Messages []struct {
				TS    string `json:"ts"`
				User  string `json:"user"`
				Text  string `json:"text"`
				BotID string `json:"bot_id"`
			} `json:"messages"`
			Meta struct {
				NextCursor string `json:"next_cursor"`
			} `json:"response_metadata"`
		}
		if err := p.call(ctx, inst.Token, "conversations.replies", args, &page); err != nil {
			return nil, err
		}
		for _, m := range page.Messages {
			if since != "" && !tsAfter(m.TS, since) {
				continue
			}
			out = append(out, chat.ThreadMessage{ID: m.TS, UserID: m.User, Text: m.Text, Bot: m.BotID != ""})
		}
		if cursor = page.Meta.NextCursor; cursor == "" {
			break
		}
	}
	return out, nil
}

// tsAfter compares two Slack timestamps ("1700000000.000100") as numbers.
func tsAfter(a, b string) bool {
	x, _ := strconv.ParseFloat(a, 64)
	y, _ := strconv.ParseFloat(b, 64)
	return x > y
}

// Conversation reads conversations.info: a public channel, a private one,
// a group DM or a DM.
func (p *Provider) Conversation(ctx context.Context, inst chat.Installation, conversation string) (chat.ConversationInfo, error) {
	var out struct {
		Channel struct {
			Name      string `json:"name"`
			IsChannel bool   `json:"is_channel"`
			IsGroup   bool   `json:"is_group"`
			IsPrivate bool   `json:"is_private"`
			IsMpim    bool   `json:"is_mpim"`
			IsIM      bool   `json:"is_im"`
		} `json:"channel"`
	}
	if err := p.call(ctx, inst.Token, "conversations.info", map[string]any{"channel": conversation}, &out); err != nil {
		return chat.ConversationInfo{}, err
	}
	c := out.Channel
	switch {
	case c.IsIM:
		return chat.ConversationInfo{Kind: chat.KindDM, Name: "a direct message"}, nil
	case c.IsMpim:
		return chat.ConversationInfo{Kind: chat.KindGroup, Name: "a group message"}, nil
	case c.IsPrivate || c.IsGroup:
		return chat.ConversationInfo{Kind: chat.KindPrivate, Name: "#" + c.Name}, nil
	}
	return chat.ConversationInfo{Kind: chat.KindPublic, Name: "#" + c.Name}, nil
}

// ListMembers is everyone in the conversation, by user id.
func (p *Provider) ListMembers(ctx context.Context, inst chat.Installation, conversation string) ([]string, error) {
	var out []string
	cursor := ""
	for range 20 {
		args := map[string]any{"channel": conversation, "limit": 200}
		if cursor != "" {
			args["cursor"] = cursor
		}
		var page struct {
			Members []string `json:"members"`
			Meta    struct {
				NextCursor string `json:"next_cursor"`
			} `json:"response_metadata"`
		}
		if err := p.call(ctx, inst.Token, "conversations.members", args, &page); err != nil {
			return nil, err
		}
		out = append(out, page.Members...)
		if cursor = page.Meta.NextCursor; cursor == "" {
			break
		}
	}
	return out, nil
}

// User reads users.info.
func (p *Provider) User(ctx context.Context, inst chat.Installation, userID string) (chat.ExternalUser, error) {
	var out struct {
		User struct {
			ID       string `json:"id"`
			TeamID   string `json:"team_id"`
			Name     string `json:"name"`
			RealName string `json:"real_name"`
			IsBot    bool   `json:"is_bot"`
			Profile  struct {
				DisplayName string `json:"display_name"`
				RealName    string `json:"real_name"`
			} `json:"profile"`
		} `json:"user"`
	}
	if err := p.call(ctx, inst.Token, "users.info", map[string]any{"user": userID}, &out); err != nil {
		return chat.ExternalUser{}, err
	}
	u := out.User
	return chat.ExternalUser{ID: u.ID, WorkspaceID: firstOf(u.TeamID, inst.WorkspaceID), Handle: u.Name,
		DisplayName: firstOf(u.Profile.RealName, u.RealName, u.Profile.DisplayName), Bot: u.IsBot}, nil
}

// Permalink is the message's address in the workspace, built as Slack
// builds it (archives/<channel>/p<ts without the dot>), with no API call.
func (p *Provider) Permalink(inst chat.Installation, conversation, message, thread string) string {
	if inst.WorkspaceDomain == "" || message == "" {
		return ""
	}
	link := fmt.Sprintf("https://%s.slack.com/archives/%s/p%s", inst.WorkspaceDomain, conversation, strings.ReplaceAll(message, ".", ""))
	if thread != "" && thread != message {
		link += "?thread_ts=" + url.QueryEscape(thread) + "&cid=" + url.QueryEscape(conversation)
	}
	return link
}

// blocks is a reply as Block Kit: its words, and its link as a button.
func blocks(r chat.Reply) []any {
	out := []any{map[string]any{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": r.Text}}}
	if r.LinkURL != "" {
		label := firstOf(r.LinkLabel, "Open in dude")
		out = append(out, map[string]any{"type": "actions", "elements": []any{map[string]any{"type": "button",
			"action_id": "open_link", "text": map[string]any{"type": "plain_text", "text": label}, "url": r.LinkURL}}})
	}
	return out
}

// PostReply posts r in the thread, and returns its message id.
func (p *Provider) PostReply(ctx context.Context, inst chat.Installation, conversation, thread string, r chat.Reply) (string, error) {
	var out struct {
		TS string `json:"ts"`
	}
	args := map[string]any{"channel": conversation, "text": r.Text, "blocks": blocks(r), "unfurl_links": false}
	if thread != "" {
		args["thread_ts"] = thread
	}
	err := p.call(ctx, inst.Token, "chat.postMessage", args, &out)
	return out.TS, err
}

// PostEphemeral shows r to user alone, in the thread.
func (p *Provider) PostEphemeral(ctx context.Context, inst chat.Installation, conversation, thread, user string, r chat.Reply) error {
	args := map[string]any{"channel": conversation, "user": user, "text": r.Text, "blocks": blocks(r)}
	if thread != "" {
		args["thread_ts"] = thread
	}
	return p.call(ctx, inst.Token, "chat.postEphemeral", args, nil)
}

// PostQuestion posts q with a button per choice.
func (p *Provider) PostQuestion(ctx context.Context, inst chat.Installation, conversation, thread string, q chat.Question) (string, error) {
	var buttons []any
	for i, c := range q.Choices {
		buttons = append(buttons, map[string]any{"type": "button", "action_id": fmt.Sprintf("choice_%d", i),
			"text": map[string]any{"type": "plain_text", "text": c.Label}, "value": c.Value})
	}
	args := map[string]any{"channel": conversation, "text": q.Text, "blocks": []any{
		map[string]any{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": q.Text}},
		map[string]any{"type": "actions", "elements": buttons}}}
	if thread != "" {
		args["thread_ts"] = thread
	}
	var out struct {
		TS string `json:"ts"`
	}
	err := p.call(ctx, inst.Token, "chat.postMessage", args, &out)
	return out.TS, err
}

// UpdateQuestion replaces a question's buttons with who chose what.
func (p *Provider) UpdateQuestion(ctx context.Context, inst chat.Installation, conversation, message string, by chat.ExternalUser, label string) error {
	text := fmt.Sprintf("%s chose %s", by.Name(), label)
	return p.call(ctx, inst.Token, "chat.update", map[string]any{"channel": conversation, "ts": message, "text": text,
		"blocks": []any{map[string]any{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": text}}}}, nil)
}

var reactionNames = map[chat.Reaction]string{chat.ReactionWorking: "eyes", chat.ReactionBooting: "hourglass_flowing_sand",
	chat.ReactionDone: "white_check_mark", chat.ReactionFailed: "x"}

// React marks a message: working (👀), booting (⏳ in its place), then done
// (✅) or failed (❌) in place of either, or cleared (either taken off). A
// mark already there, or one already gone, is no failure.
func (p *Provider) React(ctx context.Context, inst chat.Installation, conversation, message string, r chat.Reaction) error {
	if r != chat.ReactionWorking {
		// 👀 and ⏳ are never on together (⏳ goes on only once 👀 is off),
		// so ⏳ is looked for only when 👀 was not there: a mark that never
		// booted costs no second call.
		gone, err := p.unreact(ctx, inst, conversation, message, chat.ReactionWorking)
		if err == nil && gone && r != chat.ReactionBooting {
			_, err = p.unreact(ctx, inst, conversation, message, chat.ReactionBooting)
		}
		if err != nil {
			return err
		}
	}
	if r == chat.ReactionCleared {
		return nil
	}
	err := p.call(ctx, inst.Token, "reactions.add", map[string]any{"channel": conversation, "timestamp": message,
		"name": reactionNames[r]}, nil)
	if apiErr := (*APIError)(nil); errors.As(err, &apiErr) && apiErr.Code == "already_reacted" {
		return nil
	}
	return err
}

// unreact takes mark r off the message; gone says it was not there.
func (p *Provider) unreact(ctx context.Context, inst chat.Installation, conversation, message string, r chat.Reaction) (gone bool, err error) {
	err = p.call(ctx, inst.Token, "reactions.remove", map[string]any{"channel": conversation, "timestamp": message,
		"name": reactionNames[r]}, nil)
	if apiErr := (*APIError)(nil); errors.As(err, &apiErr) && apiErr.Code == "no_reaction" {
		return true, nil
	}
	return false, err
}

// webBase is where the browser authorizes: the API's parent.
func (p *Provider) webBase() string { return strings.TrimSuffix(p.cfg.APIURL, "/api") }

// InstallURL sends an admin to add the app to a workspace (OAuth v2).
func (p *Provider) InstallURL(state, redirect string) string {
	q := url.Values{"client_id": {p.cfg.ClientID}, "scope": {strings.Join(BotScopes, ",")}, "state": {state}, "redirect_uri": {redirect}}
	return p.webBase() + "/oauth/v2/authorize?" + q.Encode()
}

// CompleteInstall exchanges the install's code for the bot token.
func (p *Provider) CompleteInstall(ctx context.Context, code, redirect string) (chat.Installation, error) {
	var out struct {
		AccessToken string `json:"access_token"`
		Scope       string `json:"scope"`
		BotUserID   string `json:"bot_user_id"`
		Team        struct {
			ID     string `json:"id"`
			Name   string `json:"name"`
			Domain string `json:"domain"`
		} `json:"team"`
		AuthedUser struct {
			ID string `json:"id"`
		} `json:"authed_user"`
	}
	if err := p.call(ctx, "", "oauth.v2.access", map[string]any{"client_id": p.cfg.ClientID, "client_secret": p.cfg.ClientSecret,
		"code": code, "redirect_uri": redirect}, &out); err != nil {
		return chat.Installation{}, err
	}
	if out.AccessToken == "" || out.Team.ID == "" {
		return chat.Installation{}, errors.New("slack oauth.v2.access: no bot token or team")
	}
	inst := chat.Installation{Provider: "slack", WorkspaceID: out.Team.ID, WorkspaceName: out.Team.Name, BotUserID: out.BotUserID,
		Token: out.AccessToken, Scopes: strings.Split(out.Scope, ",")}
	// oauth.v2.access names no domain; team.info does (team:read is not
	// asked for, so auth.test's url is read instead).
	var who struct {
		URL string `json:"url"`
	}
	if err := p.call(ctx, inst.Token, "auth.test", map[string]any{}, &who); err == nil {
		if u, err := url.Parse(who.URL); err == nil {
			inst.WorkspaceDomain, _, _ = strings.Cut(u.Hostname(), ".")
		}
	}
	if inst.WorkspaceDomain == "" {
		inst.WorkspaceDomain = out.Team.Domain
	}
	return inst, nil
}

// IdentityURL sends a member to Sign in with Slack, in workspace when known.
func (p *Provider) IdentityURL(state, redirect, workspace string) string {
	q := url.Values{"response_type": {"code"}, "client_id": {p.cfg.ClientID}, "scope": {"openid profile"}, "state": {state},
		"redirect_uri": {redirect}}
	if workspace != "" {
		q.Set("team", workspace)
	}
	return p.webBase() + "/openid/connect/authorize?" + q.Encode()
}

// CompleteIdentity exchanges a Sign in with Slack code and reads who signed in.
func (p *Provider) CompleteIdentity(ctx context.Context, code, redirect string) (chat.ExternalUser, error) {
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if err := p.call(ctx, "", "openid.connect.token", map[string]any{"client_id": p.cfg.ClientID, "client_secret": p.cfg.ClientSecret,
		"code": code, "redirect_uri": redirect, "grant_type": "authorization_code"}, &tok); err != nil {
		return chat.ExternalUser{}, err
	}
	var info map[string]any
	if err := p.call(ctx, tok.AccessToken, "openid.connect.userInfo", map[string]any{}, &info); err != nil {
		return chat.ExternalUser{}, err
	}
	str := func(k string) string { s, _ := info[k].(string); return s }
	u := chat.ExternalUser{ID: str("https://slack.com/user_id"), WorkspaceID: str("https://slack.com/team_id"),
		DisplayName: str("name"), Handle: str("given_name")}
	if u.ID == "" || u.WorkspaceID == "" {
		return chat.ExternalUser{}, errors.New("slack openid.connect.userInfo: no user or team")
	}
	return u, nil
}

// Revoke revokes the installation's bot token.
func (p *Provider) Revoke(ctx context.Context, inst chat.Installation) error {
	return p.call(ctx, inst.Token, "auth.revoke", map[string]any{}, nil)
}
