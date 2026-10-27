// Package fakeslack is a stand-in for the Slack Web API calls and OAuth
// pages dude uses, for the orchestrator's tests and the E2E suite
// (cmd/fake-slack): one workspace or more, their people and conversations,
// and every message, ephemeral, update and reaction the app makes, kept to
// be asserted on. It is as strict as Slack where dude could get it wrong:
// a call needs the bot token of the message's workspace, a reaction twice
// is already_reacted, removing one that is not there is no_reaction, and a
// thread read names a real thread.
//
// Send and SendInteraction deliver signed Events API and interactivity
// payloads to dude, as Slack signs them.
package fakeslack

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/chat/slack"
)

// User is a person (or bot) of a workspace.
type User struct {
	ID, Team, Name, RealName string
	Bot                      bool
}

// Conversation is a channel, a group DM or a DM.
type Conversation struct {
	ID, Team, Name string
	// public, private, group or dm.
	Kind    string
	Members []string
}

// Message is one message in a conversation; ThreadTS is its thread's root.
// Bot is dude's own message; BotID and BotName are another app's (a CI
// bot's), whose words may be in Attachments and Blocks as well as Text.
type Message struct {
	Channel, TS, ThreadTS, User, Text string
	Bot                               bool
	BotID, BotName                    string
	Blocks                            json.RawMessage
	Attachments                       json.RawMessage
}

// Ephemeral is a message shown to one person.
type Ephemeral struct {
	Channel, ThreadTS, User, Text string
	Blocks                        json.RawMessage
}

// Update is a chat.update of a message.
type Update struct{ Channel, TS, Text string }

// Server is the fake.
type Server struct {
	ClientID, ClientSecret string
	mu                     sync.Mutex
	teams                  map[string]string // id -> name
	users                  map[string]User
	convs                  map[string]*Conversation
	messages               []Message
	ephemerals             []Ephemeral
	updates                []Update
	reactions              map[string]map[string]bool // channel:ts -> names
	tokens                 map[string]string          // bot token -> team
	scopes                 map[string][]string        // bot token -> its scopes
	pendingScopes          map[string][]string        // install code -> the scopes asked for
	userTokens             map[string]string          // OpenID token -> user
	codes                  map[string]code
	revoked                []string
	installer, signer      string // who authorizes the next install / sign-in
	seq                    int
	calls                  map[string]int
	// The arguments of each conversations.history and .replies call, in order.
	args map[string][]map[string]string
	// The most messages one history or replies call returns (SetPageSize).
	pageSize int
	// Methods answered with a Slack error, so many times, for a failure test.
	failNext map[string]failure
}

type failure struct {
	code        string
	left, after int
	// Calls of the method answered normally before the first failure.
	skip int
}

type code struct{ kind, team, user, redirect string }

// New is an empty fake.
func New(clientID, clientSecret string) *Server {
	return &Server{ClientID: clientID, ClientSecret: clientSecret, teams: map[string]string{}, users: map[string]User{},
		convs: map[string]*Conversation{}, reactions: map[string]map[string]bool{}, tokens: map[string]string{},
		userTokens: map[string]string{}, codes: map[string]code{}, calls: map[string]int{}, failNext: map[string]failure{},
		scopes: map[string][]string{}, pendingScopes: map[string][]string{}, args: map[string][]map[string]string{}}
}

// AddTeam adds a workspace with its bot user (B<team>).
func (s *Server) AddTeam(id, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.teams[id] = name
	s.users["UB"+id] = User{ID: "UB" + id, Team: id, Name: "dude", RealName: "dude", Bot: true}
}

// AddUser adds a person to their workspace.
func (s *Server) AddUser(u User) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.users[u.ID] = u
}

// AddConversation adds a conversation.
func (s *Server) AddConversation(c Conversation) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cc := c
	s.convs[c.ID] = &cc
}

// SetMembers replaces who is in a conversation.
func (s *Server) SetMembers(conv string, members []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.convs[conv].Members = members
}

// Token is a bot token for team, as an install would have issued it,
// with the scopes given (slack.BotScopes with none).
func (s *Server) Token(team string, scopes ...string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	tok := "xoxb-" + team + "-" + strconv.Itoa(len(s.tokens)+1)
	s.tokens[tok] = team
	if len(scopes) == 0 {
		scopes = slack.BotScopes
	}
	s.scopes[tok] = scopes
	return tok
}

// methodScopes are the bot scopes each Web API method needs, as Slack's
// method pages list them, for a conversation of kind ("" for any): one of
// the listed is enough.
func methodScopes(method, kind string) []string {
	byKind := func(public, private, group, dm string) []string {
		switch kind {
		case "public":
			return []string{public}
		case "private":
			return []string{private}
		case "group":
			return []string{group}
		case "dm":
			return []string{dm}
		}
		return nil
	}
	switch method {
	case "conversations.info", "conversations.members":
		return byKind("channels:read", "groups:read", "mpim:read", "im:read")
	case "conversations.replies", "conversations.history":
		return byKind("channels:history", "groups:history", "mpim:history", "im:history")
	case "chat.postMessage", "chat.postEphemeral", "chat.update":
		return []string{"chat:write"}
	case "reactions.add", "reactions.remove":
		return []string{"reactions:write"}
	case "users.info":
		return []string{"users:read"}
	}
	return nil
}

// AuthorizeAs sets who authorizes the next install (installer) and the next
// sign-in (signer) at the fake's OAuth pages.
func (s *Server) AuthorizeAs(installer, signer string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.installer, s.signer = installer, signer
}

// FailNext makes the next call of method answer ok:false with code
// ("ratelimited" answers as Slack does: HTTP 429 with Retry-After: 1).
func (s *Server) FailNext(method, code string) { s.FailTimes(method, code, 1) }

// FailTimes makes the next n calls of method fail as FailNext does.
func (s *Server) FailTimes(method, code string, n int) { s.FailRetryAfter(method, code, n, 1) }

// FailRetryAfter is FailTimes whose "ratelimited" answers carry
// Retry-After: after seconds.
func (s *Server) FailRetryAfter(method, code string, n, after int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failNext[method] = failure{code: code, left: n, after: after}
}

// FailAfter is FailTimes past the next ok calls of method, which answer
// normally: the (ok+1)th call is the first to fail.
func (s *Server) FailAfter(method, code string, ok, n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failNext[method] = failure{code: code, left: n, after: 1, skip: ok}
}

// Post adds a person's message, in thread (a new root with ""), returning its ts.
func (s *Server) Post(channel, thread, user, text string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.post(Message{Channel: channel, ThreadTS: thread, User: user, Text: text})
}

// PostApp adds another app's message (botID, shown as name), with its
// attachments and blocks as Slack's JSON (nil for none), returning its ts.
func (s *Server) PostApp(channel, thread, botID, name, text string, attachments, blocks any) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := Message{Channel: channel, ThreadTS: thread, Text: text, BotID: botID, BotName: name}
	if attachments != nil {
		m.Attachments, _ = json.Marshal(attachments)
	}
	if blocks != nil {
		m.Blocks, _ = json.Marshal(blocks)
	}
	return s.post(m)
}

// PostOwn adds a message by team's dude app, as its chat.postMessage would.
func (s *Server) PostOwn(channel, thread, team, text string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.post(Message{Channel: channel, ThreadTS: thread, User: "UB" + team, Text: text, Bot: true})
}

// wire is m as conversations.replies and conversations.history return it.
func (m Message) wire(team string) map[string]any {
	msg := map[string]any{"ts": m.TS, "text": m.Text, "thread_ts": m.ThreadTS}
	if m.User != "" {
		msg["user"] = m.User
	}
	switch {
	case m.Bot:
		msg["bot_id"] = "B" + team
	case m.BotID != "":
		msg["bot_id"], msg["username"], msg["subtype"] = m.BotID, m.BotName, "bot_message"
	}
	if len(m.Attachments) > 0 {
		msg["attachments"] = m.Attachments
	}
	if len(m.Blocks) > 0 {
		msg["blocks"] = m.Blocks
	}
	return msg
}

func (s *Server) post(m Message) string {
	s.seq++
	m.TS = fmt.Sprintf("1700000000.%06d", s.seq)
	if m.ThreadTS == "" {
		m.ThreadTS = m.TS
	}
	s.messages = append(s.messages, m)
	return m.TS
}

// PostAged adds a person's top-level message age before the fake's clock
// (its messages are all at 1700000000), returning its ts.
func (s *Server) PostAged(channel, user, text string, age time.Duration) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	ts := fmt.Sprintf("%d.%06d", 1700000000-int64(age/time.Second), s.seq)
	s.messages = append(s.messages, Message{Channel: channel, TS: ts, ThreadTS: ts, User: user, Text: text})
	return ts
}

func tsNum(ts string) float64 {
	f, _ := strconv.ParseFloat(ts, 64)
	return f
}

// BotMessages are the app's messages, in order.
func (s *Server) BotMessages() []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Message
	for _, m := range s.messages {
		if m.Bot {
			out = append(out, m)
		}
	}
	return out
}

// Ephemerals are the messages shown to one person, in order.
func (s *Server) Ephemerals() []Ephemeral {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Ephemeral(nil), s.ephemerals...)
}

// Updates are the chat.update calls, in order.
func (s *Server) Updates() []Update {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Update(nil), s.updates...)
}

// Reactions are a message's reactions, sorted.
func (s *Server) Reactions(channel, ts string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for name := range s.reactions[channel+":"+ts] {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Revoked are the tokens auth.revoke revoked.
func (s *Server) Revoked() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.revoked...)
}

// Calls is how many times method was called.
func (s *Server) Calls(method string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[method]
}

// SetPageSize caps how many messages one conversations.history or
// .replies call returns, whatever its limit, as Slack's own caps do (0:
// the call's limit alone).
func (s *Server) SetPageSize(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pageSize = n
}

// paged is the page of msgs a call's limit and cursor (an offset) ask
// for, and the next page's cursor ("" at the end).
func (s *Server) paged(msgs []any, a map[string]json.RawMessage) ([]any, string) {
	limit, _ := strconv.Atoi(str(a, "limit"))
	if limit <= 0 {
		limit = 100
	}
	if s.pageSize > 0 {
		limit = min(limit, s.pageSize)
	}
	from, _ := strconv.Atoi(str(a, "cursor"))
	from = min(from, len(msgs))
	to := min(from+limit, len(msgs))
	next := ""
	if to < len(msgs) {
		next = strconv.Itoa(to)
	}
	return append([]any{}, msgs[from:to]...), next
}

// Args are the arguments of each call of a conversations.history or
// conversations.replies, in order.
func (s *Server) Args(method string) []map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]map[string]string(nil), s.args[method]...)
}

// ServeHTTP serves /api/<method>, the OAuth pages and, for the E2E suite,
// /_fake/ controls.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/oauth/v2/authorize":
		s.authorize(w, r, "install")
	case r.URL.Path == "/openid/connect/authorize":
		s.authorize(w, r, "identity")
	case strings.HasPrefix(r.URL.Path, "/api/"):
		s.api(w, r, strings.TrimPrefix(r.URL.Path, "/api/"))
	case strings.HasPrefix(r.URL.Path, "/_fake/"):
		s.control(w, r, strings.TrimPrefix(r.URL.Path, "/_fake/"))
	default:
		http.NotFound(w, r)
	}
}

// authorize is Slack's consent page, consented at once by whoever
// AuthorizeAs named: back to redirect_uri with a code and the state, or
// error=access_denied with nobody named.
func (s *Server) authorize(w http.ResponseWriter, r *http.Request, kind string) {
	q := r.URL.Query()
	back, err := url.Parse(q.Get("redirect_uri"))
	if err != nil || back.Scheme == "" || q.Get("client_id") != s.ClientID {
		http.Error(w, "bad redirect_uri or client_id", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	who := s.installer
	if kind == "identity" {
		who = s.signer
	}
	params := back.Query()
	params.Set("state", q.Get("state"))
	if u, ok := s.users[who]; ok {
		s.seq++
		c := fmt.Sprintf("code-%s-%d", kind, s.seq)
		s.codes[c] = code{kind: kind, team: u.Team, user: u.ID, redirect: q.Get("redirect_uri")}
		if kind == "install" {
			// The scopes the install asked for are the token's.
			s.pendingScopes[c] = strings.Split(q.Get("scope"), ",")
		}
		params.Set("code", c)
	} else {
		params.Set("error", "access_denied")
	}
	s.mu.Unlock()
	back.RawQuery = params.Encode()
	http.Redirect(w, r, back.String(), http.StatusFound)
}

func reply(w http.ResponseWriter, v map[string]any) {
	v["ok"] = true
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func refuse(w http.ResponseWriter, code string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": code})
}

// args reads a call's arguments, JSON or form-encoded, as strings and raw values.
func args(r *http.Request) (map[string]json.RawMessage, error) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	out := map[string]json.RawMessage{}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		if len(bytes.TrimSpace(raw)) > 0 {
			err = json.Unmarshal(raw, &out)
		}
		return out, err
	}
	form, err := url.ParseQuery(string(raw))
	for k := range form {
		out[k], _ = json.Marshal(form.Get(k))
	}
	for k := range r.URL.Query() {
		out[k], _ = json.Marshal(r.URL.Query().Get(k))
	}
	return out, err
}

func str(a map[string]json.RawMessage, k string) string {
	var s string
	if json.Unmarshal(a[k], &s) == nil {
		return s
	}
	var n json.Number
	if json.Unmarshal(a[k], &n) == nil {
		return n.String()
	}
	return ""
}

// jsonMethods accept JSON; other methods reject it with invalid_arguments.
var jsonMethods = map[string]bool{"chat.postMessage": true, "chat.postEphemeral": true, "chat.update": true,
	"reactions.add": true, "reactions.remove": true}

func (s *Server) api(w http.ResponseWriter, r *http.Request, method string) {
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") && !jsonMethods[method] {
		refuse(w, "invalid_arguments")
		return
	}
	a, err := args(r)
	if err != nil {
		refuse(w, "invalid_arguments")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls[method]++
	if method == "conversations.history" || method == "conversations.replies" {
		got := map[string]string{}
		for k := range a {
			got[k] = str(a, k)
		}
		s.args[method] = append(s.args[method], got)
	}
	if f, ok := s.failNext[method]; ok && f.skip > 0 {
		f.skip--
		s.failNext[method] = f
	} else if ok {
		if f.left--; f.left <= 0 {
			delete(s.failNext, method)
		} else {
			s.failNext[method] = f
		}
		if f.code == "ratelimited" {
			w.Header().Set("Retry-After", strconv.Itoa(f.after))
			w.WriteHeader(http.StatusTooManyRequests)
		}
		refuse(w, f.code)
		return
	}
	switch method {
	case "oauth.v2.access", "openid.connect.token":
		s.exchange(w, a, method)
		return
	}
	token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if method == "openid.connect.userInfo" {
		u, ok := s.users[s.userTokens[token]]
		if !ok {
			refuse(w, "invalid_auth")
			return
		}
		reply(w, map[string]any{"https://slack.com/user_id": u.ID, "https://slack.com/team_id": u.Team, "name": u.RealName,
			"given_name": u.Name})
		return
	}
	team, ok := s.tokens[token]
	if !ok {
		refuse(w, "invalid_auth")
		return
	}
	kind := ""
	if c, ok := s.convs[str(a, "channel")]; ok {
		kind = c.Kind
	}
	if need := methodScopes(method, kind); len(need) > 0 && !containsAny(s.scopes[token], need) {
		refuse(w, "missing_scope")
		return
	}
	conv := func() (*Conversation, bool) {
		c, ok := s.convs[str(a, "channel")]
		if !ok || c.Team != team {
			refuse(w, "channel_not_found")
			return nil, false
		}
		// A private channel or group the bot is not in is not found, as
		// Slack answers; a public one it may read but not post in.
		if c.Kind != "public" && !contains(c.Members, "UB"+team) {
			refuse(w, "channel_not_found")
			return nil, false
		}
		if c.Kind == "public" && !contains(c.Members, "UB"+team) && strings.HasPrefix(method, "chat.") {
			refuse(w, "not_in_channel")
			return nil, false
		}
		return c, true
	}
	switch method {
	case "auth.test":
		reply(w, map[string]any{"url": "https://" + strings.ToLower(team) + ".slack.com/", "team_id": team, "user_id": "UB" + team})
	case "auth.revoke":
		delete(s.tokens, token)
		s.revoked = append(s.revoked, token)
		reply(w, map[string]any{"revoked": true})
	case "conversations.info":
		c, ok := conv()
		if !ok {
			return
		}
		reply(w, map[string]any{"channel": map[string]any{"id": c.ID, "name": c.Name, "is_channel": c.Kind == "public" || c.Kind == "private",
			"is_private": c.Kind == "private" || c.Kind == "group", "is_mpim": c.Kind == "group", "is_im": c.Kind == "dm"}})
	case "conversations.members":
		c, ok := conv()
		if !ok {
			return
		}
		reply(w, map[string]any{"members": append([]string{}, c.Members...), "response_metadata": map[string]any{"next_cursor": ""}})
	case "conversations.replies":
		c, ok := conv()
		if !ok {
			return
		}
		root, oldest, latest := str(a, "ts"), str(a, "oldest"), str(a, "latest")
		var msgs []any
		var parent any
		for _, m := range s.messages {
			if m.Channel != c.ID || m.ThreadTS != root {
				continue
			}
			if m.TS == root {
				parent = m.wire(team)
				continue
			}
			if oldest != "" && m.TS <= oldest || latest != "" && m.TS >= latest {
				continue
			}
			msgs = append(msgs, m.wire(team))
		}
		if parent == nil {
			refuse(w, "thread_not_found")
			return
		}
		// Oldest first, as Slack lists replies, the parent first whatever
		// oldest and latest say, on the first page, counted in its limit.
		msgs = append([]any{parent}, msgs...)
		page, next := s.paged(msgs, a)
		reply(w, map[string]any{"messages": page, "has_more": next != "", "response_metadata": map[string]any{"next_cursor": next}})
	case "conversations.history":
		c, ok := conv()
		if !ok {
			return
		}
		latest, oldest := tsNum(str(a, "latest")), tsNum(str(a, "oldest"))
		var top []Message
		for _, m := range s.messages {
			if m.Channel != c.ID || m.ThreadTS != m.TS {
				continue
			}
			ts := tsNum(m.TS)
			if (str(a, "latest") != "" && ts >= latest) || (str(a, "oldest") != "" && ts <= oldest) {
				continue
			}
			top = append(top, m)
		}
		// Newest first, as Slack lists a channel.
		sort.SliceStable(top, func(i, j int) bool { return tsNum(top[i].TS) > tsNum(top[j].TS) })
		msgs := []any{}
		for _, m := range top {
			msgs = append(msgs, m.wire(team))
		}
		page, next := s.paged(msgs, a)
		reply(w, map[string]any{"messages": page, "has_more": next != "", "response_metadata": map[string]any{"next_cursor": next}})
	case "users.info":
		u, ok := s.users[str(a, "user")]
		if !ok || u.Team != team {
			refuse(w, "user_not_found")
			return
		}
		reply(w, map[string]any{"user": map[string]any{"id": u.ID, "team_id": u.Team, "name": u.Name, "real_name": u.RealName,
			"is_bot": u.Bot, "profile": map[string]any{"real_name": u.RealName, "display_name": u.Name}}})
	case "chat.postMessage":
		c, ok := conv()
		if !ok {
			return
		}
		ts := s.post(Message{Channel: c.ID, ThreadTS: str(a, "thread_ts"), User: "UB" + team, Text: str(a, "text"), Bot: true,
			Blocks: a["blocks"]})
		reply(w, map[string]any{"ts": ts, "channel": c.ID})
	case "chat.postEphemeral":
		c, ok := conv()
		if !ok {
			return
		}
		user := str(a, "user")
		if !contains(c.Members, user) {
			refuse(w, "user_not_in_channel")
			return
		}
		s.ephemerals = append(s.ephemerals, Ephemeral{Channel: c.ID, ThreadTS: str(a, "thread_ts"), User: user, Text: str(a, "text"),
			Blocks: a["blocks"]})
		reply(w, map[string]any{"message_ts": "1700000000.999999"})
	case "chat.update":
		c, ok := conv()
		if !ok {
			return
		}
		ts := str(a, "ts")
		for i := range s.messages {
			if s.messages[i].Channel == c.ID && s.messages[i].TS == ts {
				if !s.messages[i].Bot {
					refuse(w, "cant_update_message")
					return
				}
				s.messages[i].Text = str(a, "text")
				s.updates = append(s.updates, Update{Channel: c.ID, TS: ts, Text: str(a, "text")})
				reply(w, map[string]any{"ts": ts})
				return
			}
		}
		refuse(w, "message_not_found")
	case "reactions.add", "reactions.remove":
		if _, ok := conv(); !ok {
			return
		}
		key := str(a, "channel") + ":" + str(a, "timestamp")
		name := str(a, "name")
		if s.reactions[key] == nil {
			s.reactions[key] = map[string]bool{}
		}
		switch {
		case method == "reactions.add" && s.reactions[key][name]:
			refuse(w, "already_reacted")
		case method == "reactions.remove" && !s.reactions[key][name]:
			refuse(w, "no_reaction")
		case method == "reactions.add":
			s.reactions[key][name] = true
			reply(w, map[string]any{})
		default:
			delete(s.reactions[key], name)
			reply(w, map[string]any{})
		}
	default:
		refuse(w, "unknown_method")
	}
}

func containsAny(list, want []string) bool {
	for _, w := range want {
		if contains(list, w) {
			return true
		}
	}
	return false
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// exchange trades a code from the OAuth pages: a bot token for an install,
// a user token for a sign-in. A code works once, for its own kind.
func (s *Server) exchange(w http.ResponseWriter, a map[string]json.RawMessage, method string) {
	if str(a, "client_id") != s.ClientID || str(a, "client_secret") != s.ClientSecret {
		refuse(w, "invalid_client")
		return
	}
	c, ok := s.codes[str(a, "code")]
	want := "install"
	if method == "openid.connect.token" {
		want = "identity"
	}
	if !ok || c.kind != want {
		refuse(w, "invalid_code")
		return
	}
	// Slack requires the redirect_uri the authorize step was given.
	if str(a, "redirect_uri") != c.redirect {
		refuse(w, "bad_redirect_uri")
		return
	}
	delete(s.codes, str(a, "code"))
	if want == "identity" {
		tok := "xoxp-" + c.user + "-" + strconv.Itoa(len(s.userTokens)+1)
		s.userTokens[tok] = c.user
		reply(w, map[string]any{"access_token": tok, "token_type": "Bearer", "id_token": "unused"})
		return
	}
	tok := "xoxb-" + c.team + "-" + strconv.Itoa(len(s.tokens)+1)
	s.tokens[tok] = c.team
	scopes := s.pendingScopes[str(a, "code")]
	delete(s.pendingScopes, str(a, "code"))
	s.scopes[tok] = scopes
	reply(w, map[string]any{"access_token": tok, "token_type": "bot", "bot_user_id": "UB" + c.team,
		"scope": strings.Join(scopes, ","), "team": map[string]any{"id": c.team, "name": s.teams[c.team]},
		"authed_user": map[string]any{"id": c.user}})
}

// control is the E2E suite's hand on the fake: set the world up, add a
// person's message, and read what the app did.
func (s *Server) control(w http.ResponseWriter, r *http.Request, what string) {
	var in struct {
		ID, Team, Name, RealName, Kind, Channel, Thread, User, Text, Installer, Signer string
		Members                                                                        []string
		BotID                                                                          string
		Attachments, Blocks                                                            json.RawMessage
	}
	if r.Method == http.MethodPost {
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	out := map[string]any{}
	switch what {
	case "team":
		s.AddTeam(in.ID, in.Name)
	case "user":
		s.AddUser(User{ID: in.ID, Team: in.Team, Name: in.Name, RealName: in.RealName})
	case "conversation":
		s.AddConversation(Conversation{ID: in.ID, Team: in.Team, Name: in.Name, Kind: in.Kind, Members: in.Members})
	case "authorize-as":
		s.AuthorizeAs(in.Installer, in.Signer)
	case "post":
		out["ts"] = s.Post(in.Channel, in.Thread, in.User, in.Text)
	case "post-app":
		var attachments, blocks any
		if len(in.Attachments) > 0 {
			attachments = in.Attachments
		}
		if len(in.Blocks) > 0 {
			blocks = in.Blocks
		}
		out["ts"] = s.PostApp(in.Channel, in.Thread, in.BotID, in.Name, in.Text, attachments, blocks)
	case "state":
		out["messages"], out["ephemerals"], out["updates"] = s.BotMessages(), s.Ephemerals(), s.Updates()
		s.mu.Lock()
		reactions := map[string][]string{}
		for k, names := range s.reactions {
			for n := range names {
				reactions[k] = append(reactions[k], n)
			}
			sort.Strings(reactions[k])
		}
		s.mu.Unlock()
		out["reactions"] = reactions
	default:
		http.NotFound(w, r)
		return
	}
	reply(w, out)
}

// Send delivers an Events API payload to endpoint, signed with secret now.
func Send(endpoint, secret string, payload any) (*http.Response, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return send(endpoint, secret, "application/json", raw, time.Now())
}

// SendInteraction delivers an interactivity payload (form field payload).
func SendInteraction(endpoint, secret string, payload any) (*http.Response, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return send(endpoint, secret, "application/x-www-form-urlencoded", []byte(url.Values{"payload": {string(raw)}}.Encode()), time.Now())
}

// SignedRequest is a request carrying body signed with secret at at.
func SignedRequest(endpoint, secret, contentType string, body []byte, at time.Time) (*http.Request, error) {
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	ts := strconv.FormatInt(at.Unix(), 10)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("X-Slack-Request-Timestamp", ts)
	req.Header.Set("X-Slack-Signature", fmt.Sprintf("v0=%x", slack.Sign(secret, ts, body)))
	return req, nil
}

func send(endpoint, secret, contentType string, body []byte, at time.Time) (*http.Response, error) {
	req, err := SignedRequest(endpoint, secret, contentType, body, at)
	if err != nil {
		return nil, err
	}
	return http.DefaultClient.Do(req)
}

// Mention is an app_mention event payload: user mentions the app in
// channel, in thread ("" for a new thread) at ts.
func Mention(team, eventID, channel, thread, ts, user, text string) map[string]any {
	ev := map[string]any{"type": "app_mention", "user": user, "text": "<@UB" + team + "> " + text, "ts": ts, "channel": channel}
	if thread != "" && thread != ts {
		ev["thread_ts"] = thread
	}
	return map[string]any{"type": "event_callback", "team_id": team, "event_id": eventID, "event": ev,
		"authorizations": []any{map[string]any{"user_id": "UB" + team}}}
}

// DirectMessage is a message.im event payload.
func DirectMessage(team, eventID, channel, ts, user, text string) map[string]any {
	return map[string]any{"type": "event_callback", "team_id": team, "event_id": eventID, "event": map[string]any{
		"type": "message", "channel_type": "im", "user": user, "text": text, "ts": ts, "channel": channel}}
}

// Click is a block_actions payload: user clicks the button with value on
// message ts in channel.
func Click(team, channel, ts, thread, user, value string) map[string]any {
	return map[string]any{"type": "block_actions", "team": map[string]any{"id": team}, "user": map[string]any{"id": user, "username": user},
		"container": map[string]any{"type": "message", "channel_id": channel, "message_ts": ts, "thread_ts": thread},
		"message":   map[string]any{"ts": ts, "thread_ts": thread},
		"actions":   []any{map[string]any{"action_id": "choice", "value": value, "action_ts": strconv.FormatInt(time.Now().UnixNano(), 10)}}}
}
