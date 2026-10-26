package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/ids"
)

// JoinedVia is session.joined's via for someone added to a group session
// because they are in its conversation.
const JoinedVia = "chat_conversation"

// process is one attempt at a claimed delivery. A delivery that recorded
// its request only posts what is left of it; otherwise it is routed, from
// the routing decision kept on it when an earlier attempt got that far.
func (s *Service) process(ctx context.Context, d claimed) error {
	p, err := s.provider(d.Provider)
	if err != nil {
		s.log().Warn("a chat delivery for a provider dude no longer has", "provider", d.Provider, "event", d.ID)
		return nil
	}
	unreadable := func(err error) error {
		s.log().Warn("an unreadable chat delivery", "provider", d.Provider, "event", d.ID, "error", err)
		return nil
	}
	switch d.Kind {
	case "mention":
		var m Mention
		if err := json.Unmarshal(d.Payload, &m); err != nil {
			return unreadable(err)
		}
		return s.route(ctx, d, p, m, nil)
	case "interaction":
		var in Interaction
		if err := json.Unmarshal(d.Payload, &in); err != nil {
			return unreadable(err)
		}
		return s.answer(ctx, d, p, in)
	}
	var ev storedEvent
	if err := json.Unmarshal(d.Payload, &ev); err != nil {
		return unreadable(err)
	}
	switch ev.Kind {
	case EventMention:
		if ev.Mention == nil {
			return unreadable(errors.New("a mention event with no mention"))
		}
		return s.route(ctx, d, p, *ev.Mention, nil)
	case EventUninstalled:
		return s.removeInstallation(ctx, d.Org, d.Provider, "", "uninstalled")
	case EventTokensRevoked:
		return s.tokensRevoked(ctx, d.Org, d.Provider, ev)
	}
	return nil
}

// tokensRevoked is a workspace revoking tokens: the bot's ends the
// installation; a person's (their sign-in) unlinks that person alone.
func (s *Service) tokensRevoked(ctx context.Context, org, provider string, ev storedEvent) error {
	var inst Installation
	var ok bool
	if err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) (err error) {
		inst, ok, err = loadInstallation(ctx, tx, provider)
		return err
	}); err != nil || !ok {
		return err
	}
	if slices.Contains(ev.RevokedBots, inst.BotUserID) {
		return s.removeInstallation(ctx, org, provider, "", "tokens_revoked")
	}
	if len(ev.RevokedUsers) == 0 {
		return nil
	}
	return s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `DELETE FROM chat_identities WHERE provider = $1 AND external_workspace_id = $2 AND external_user_id = ANY($3)
			RETURNING person_id`, provider, inst.WorkspaceID, ev.RevokedUsers)
		if err != nil {
			return err
		}
		gone, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		for _, person := range gone {
			if err := chatLedger(ctx, tx, org, EvChatUnlinked, "system", "", map[string]any{"provider": provider,
				"personId": person, "reason": "tokens_revoked"}); err != nil {
				return err
			}
		}
		return nil
	})
}

// asker is the person a mention is from, as dude knows them.
type asker struct {
	Person, Name, Slug, OrgName string
	Linked                      bool
	// Their provider display name, as linked.
	Display string
}

// whoIs is who external user is in the organisation: linked (a
// chat_identities row) and, when their account is an active member, that
// person. Linked but not a member: Person "".
func whoIs(ctx context.Context, tx pgx.Tx, provider, workspace, user string) (asker, error) {
	var a asker
	if err := tx.QueryRow(ctx, `SELECT slug, name FROM organizations WHERE id = current_organization_id()`).Scan(&a.Slug, &a.OrgName); err != nil {
		return a, err
	}
	var account string
	err := tx.QueryRow(ctx, `SELECT account_id, display_name FROM chat_identities WHERE provider = $1 AND external_workspace_id = $2
		AND external_user_id = $3`, provider, workspace, user).Scan(&account, &a.Display)
	if db.IsNotFound(err) {
		return a, nil
	}
	if err != nil {
		return a, err
	}
	a.Linked = true
	err = tx.QueryRow(ctx, `SELECT id, name FROM people WHERE account_id = $1 AND organization_id = current_organization_id()
		AND removed_at IS NULL`, account).Scan(&a.Person, &a.Name)
	if db.IsNotFound(err) {
		return a, nil
	}
	return a, err
}

// request is one row of chat_requests, as the pipeline fills it in.
type request struct {
	ID, EventID                 string
	Mention                     Mention
	Kind, ConversationName      string
	UserName, Person, Permalink string
	ContextHash                 string
	Route, AskedRoute           string
	Confidence                  *float64
	Reasoning                   string
	Rounds                      int
	ResultKind, ResultID        string
	Added, Skipped              []string
	ModelCall                   string
	Question                    any
	// The session's last event cursor once the mention went in: its mark
	// waits on the agent's first output past it (reactions.go).
	AwaitsAfter *int64
	// What is posted once the request commits.
	effects []effect
	// The conversation's members, for a private or group conversation;
	// the linked ones (user → person); the others' display names.
	members     []string
	linked      map[string]string
	memberNames map[string]string
}

// record writes the request with its effects, in tx: false when the
// delivery already has one (it was handled before).
func record(ctx context.Context, tx pgx.Tx, r *request) (bool, error) {
	if r.ID == "" {
		r.ID = ids.New(ids.ChatRequest)
	}
	mention, err := json.Marshal(r.Mention)
	if err != nil {
		return false, err
	}
	var question []byte
	if r.Question != nil {
		if question, err = json.Marshal(r.Question); err != nil {
			return false, err
		}
	}
	effects, err := json.Marshal(db.NonNil(r.effects))
	if err != nil {
		return false, err
	}
	tag, err := tx.Exec(ctx, `INSERT INTO chat_requests (id, organization_id, provider, event_id, external_workspace_id, conversation_id,
			conversation_kind, conversation_name, thread_id, external_user_id, external_user_name, person_id, text, permalink, context_hash,
			route, asked_route, confidence, reasoning, rounds, result_kind, result_id, shared_added, shared_skipped, model_call_id,
			mention, question, effects, awaits_session_after)
		VALUES ($1, current_organization_id(), $2, $3, $4, $5, NULLIF($6, ''), $7, $8, $9, $10, NULLIF($11, ''), $12, $13, NULLIF($14, ''),
			$15, NULLIF($16, ''), $17, $18, $19, NULLIF($20, ''), NULLIF($21, ''), $22, $23, NULLIF($24, ''), $25, $26, $27, $28)
		ON CONFLICT (provider, event_id) DO NOTHING`,
		r.ID, r.Mention.Provider, r.EventID, r.Mention.WorkspaceID, r.Mention.ConversationID, r.Kind, r.ConversationName,
		r.Mention.ThreadID, r.Mention.UserID, r.UserName, r.Person, clip(r.Mention.Text, 4000), r.Permalink, r.ContextHash,
		r.Route, r.AskedRoute, r.Confidence, clip(r.Reasoning, 2000), r.Rounds, r.ResultKind, r.ResultID, db.NonNil(r.Added),
		db.NonNil(r.Skipped), r.ModelCall, mention, question, effects, r.AwaitsAfter)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// commit makes what fn makes, records the request with its effects and
// marks a clicked question answered, in one transaction, then posts the
// effects. Nothing is made or posted twice: a delivery recorded already, or
// a question another click answered first, rolls fn's work back.
func (s *Service) commit(ctx context.Context, org string, p Provider, inst Installation, r *request, pick *picked,
	fn func(tx pgx.Tx) error) error {
	err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		if fn != nil {
			if err := fn(tx); err != nil {
				return err
			}
		}
		if pick != nil {
			tag, err := tx.Exec(ctx, `UPDATE chat_requests SET answered_at = now(), answered_by_event = $2 WHERE id = $1 AND answered_at IS NULL`,
				pick.Request, r.EventID)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return errHandled
			}
			r.effects = append([]effect{pick.Update}, r.effects...)
		}
		first, err := record(ctx, tx, r)
		if err == nil && !first {
			err = errHandled
		}
		return err
	})
	if errors.Is(err, errHandled) {
		return nil
	}
	if err != nil {
		return err
	}
	if r.ResultKind == "session" && s.RunStarted != nil {
		s.RunStarted()
	}
	return s.deliver(ctx, org, p, inst, r.EventID)
}

// handled is whether the delivery already made its request.
func handled(ctx context.Context, tx pgx.Tx, provider, eventID string) (bool, error) {
	var done bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM chat_requests WHERE provider = $1 AND event_id = $2)`, provider, eventID).Scan(&done)
	return done, err
}

// link is an address in the web app, absolute when dude knows its public URL.
func (s *Service) link(slug, hash string) string {
	return strings.TrimRight(s.PublicURL, "/") + "/#/o/" + url.PathEscape(slug) + "/" + hash
}

// connectLink is where a private Connect button sends someone: it names
// no organisation, which the page reads from the state once they sign in.
func (s *Service) connectLink(provider, state string) string {
	return strings.TrimRight(s.PublicURL, "/") + "/#/connect/" + url.PathEscape(provider) + "?state=" + url.QueryEscape(state)
}

// picked is a clarifying question's answer, routing a mention again: the
// answer as chosen, the rounds asked, the question's request, and the
// question's update to who chose what.
type picked struct {
	Answer  Answer
	Rounds  int
	Request string
	Update  effect
}

// storedDecision is the routing judgement's outcome, kept on the delivery
// before dude acts on it: a retry acts on it and never asks again.
type storedDecision struct {
	Answer *Answer `json:"answer,omitempty"`
	Failed string  `json:"failed,omitempty"`
	CallID string  `json:"callId,omitempty"`
	Rounds int     `json:"rounds"`
	// What it was decided from, which a retry reuses.
	Conv    *ConversationInfo `json:"conv,omitempty"`
	Members []string          `json:"members,omitempty"`
	Thread  *threadContext    `json:"thread,omitempty"`
	// The channel before a top-level mention.
	Channel *threadContext `json:"channel,omitempty"`
}

// route is the routing flow for one mention: installed, connected, a
// member; a bound thread's link or session; else one routing judgement,
// kept, checked, made, bound and replied to, each step recorded in Requests.
func (s *Service) route(ctx context.Context, d claimed, p Provider, m Mention, pick *picked) error {
	org, eventID := d.Org, d.ID
	var (
		inst Installation
		ok   bool
		who  asker
		done bool
		mark bool
	)
	err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) (err error) {
		if done, err = handled(ctx, tx, m.Provider, eventID); err != nil {
			return err
		}
		if inst, ok, err = loadInstallation(ctx, tx, m.Provider); err != nil || !ok || done {
			return err
		}
		if inst.WorkspaceID != m.WorkspaceID {
			ok = false
			return nil
		}
		if pick == nil {
			// The 👀 is claimed before it is put: at most once per delivery.
			tag, err := tx.Exec(ctx, `UPDATE chat_events SET working_at = now() WHERE provider = $1 AND event_id = $2 AND working_at IS NULL`,
				d.Provider, eventID)
			if err != nil {
				return err
			}
			mark = tag.RowsAffected() == 1
		}
		who, err = whoIs(ctx, tx, m.Provider, m.WorkspaceID, m.UserID)
		return err
	})
	if err != nil || !ok {
		return err
	}
	if done {
		return s.deliver(ctx, org, p, inst, eventID)
	}
	r := &request{EventID: eventID, Mention: m, Person: who.Person, Permalink: p.Permalink(inst, m.ConversationID, m.MessageID, m.ThreadID)}
	if mark {
		s.react(ctx, org, p, inst, m, ReactionWorking)
	}
	if who.Linked && who.Display != "" {
		r.UserName = who.Display
	} else {
		r.UserName = s.names(ctx, p, inst, []string{m.UserID})[m.UserID]
	}
	if !who.Linked || who.Person == "" {
		// A refusal names where it was refused when the provider says; a
		// failed lookup records it unnamed rather than holding it back.
		if conv, err := p.Conversation(ctx, inst, m.ConversationID); err != nil {
			s.noteFailure(ctx, org, m.Provider, err)
		} else {
			r.Kind, r.ConversationName = conv.Kind, conv.Name
		}
	}
	if !who.Linked {
		return s.promptConnect(ctx, org, p, inst, r)
	}
	if who.Person == "" {
		r.Route, r.Reasoning = RouteNotMember, "their linked account is not an active member of the organisation"
		r.effects = []effect{ephemeralEffect(Reply{Text: "Sorry, dude can't act on this for you."}), reactEffect(ReactionCleared)}
		return s.commit(ctx, org, p, inst, r, pick, nil)
	}
	// A retry after the routing decision was kept reuses what it was made
	// from instead of reading the provider again.
	var kept *storedDecision
	if pick == nil && len(d.Decision) > 0 {
		var k storedDecision
		if json.Unmarshal(d.Decision, &k) == nil && k.Conv != nil {
			kept = &k
		}
	}
	var conv ConversationInfo
	if kept != nil {
		conv = *kept.Conv
	} else if conv, err = p.Conversation(ctx, inst, m.ConversationID); err != nil {
		s.noteFailure(ctx, org, m.Provider, err)
		return err
	}
	r.Kind, r.ConversationName = conv.Kind, conv.Name
	if !m.Direct {
		if bound, err := s.bound(ctx, org, p, inst, who, r, pick); bound || err != nil {
			return err
		}
	}
	// Who is in a private conversation decides which sessions a mention
	// there may resume, and who a session made there is shared with.
	var connected []string
	if conv.Kind == KindPrivate || conv.Kind == KindGroup {
		var members []string
		if kept != nil {
			members = kept.Members
		}
		if err := s.readMembers(ctx, org, p, inst, r, members, kept != nil); err != nil {
			return err
		}
		for _, person := range r.linked {
			connected = append(connected, person)
		}
		slices.Sort(connected)
	}
	var h heard
	if kept != nil && kept.Thread != nil {
		h.Thread = *kept.Thread
		if kept.Channel != nil {
			h.Channel = kept.Channel.within(channelChars)
		}
	} else {
		if h.Thread, err = s.thread(ctx, org, p, inst, m); err != nil {
			return err
		}
		if h.Channel, err = s.channelBefore(ctx, org, p, inst, m); err != nil {
			return err
		}
	}
	r.ContextHash = h.hash()
	world, err := s.world(ctx, org, who.Person, m, conv.Kind, connected)
	if err != nil {
		return err
	}
	var dec Decision
	var a Answer
	switch {
	case pick != nil:
		a, r.Rounds = pick.Answer, pick.Rounds
		dec = Decide(a, world, r.Rounds, true, m.Text)
		r.Reasoning, r.Confidence = "chosen in the clarifying question", ptr(1.0)
	default:
		got, err := s.decision(ctx, d, org, p, who, m, conv, r.members, h, world)
		if err != nil {
			return err
		}
		r.ModelCall, r.Rounds = got.CallID, got.Rounds
		if got.Answer == nil {
			// No answer, a proxy error or invalid JSON: a brainstorm.
			r.Route, r.Reasoning = RouteFailed, "the routing call failed: "+got.Failed
			return s.make(ctx, org, p, inst, who, r, h, Decision{Route: RouteBrainstorm, Title: clip(trimmed(m.Text), 80)}, nil)
		}
		a = *got.Answer
		r.Confidence, r.Reasoning = ptr(a.Confidence), a.Reasoning
		dec = Decide(a, world, r.Rounds, false, m.Text)
	}
	if dec.Problem != "" {
		r.AskedRoute = dec.AskedRoute
		r.Reasoning = strings.TrimSpace(r.Reasoning + " (" + dec.Problem + ")")
	}
	if s.beforeMake != nil {
		if err := s.beforeMake(ctx); err != nil {
			return err
		}
	}
	if dec.Route == RouteClarify {
		return s.clarify(ctx, org, p, inst, r, a, dec, pick)
	}
	if dec.Route == RouteChannelProjects {
		return s.setChannelProjects(ctx, org, p, inst, who, r, dec, pick)
	}
	return s.make(ctx, org, p, inst, who, r, h, dec, pick)
}

// setChannelProjects is the channel_projects route: an admin of the
// organisation changes what the conversation is usually about, and the
// reply says what changed. Anyone else is told privately that only an
// admin may, and nothing changes.
func (s *Service) setChannelProjects(ctx context.Context, org string, p Provider, inst Installation, who asker, r *request, d Decision,
	pick *picked) error {
	m := r.Mention
	r.Route = RouteChannelProjects
	return s.commit(ctx, org, p, inst, r, pick, func(tx pgx.Tx) error {
		var admin bool
		if err := tx.QueryRow(ctx, `SELECT role = 'admin' FROM people WHERE id = $1`, who.Person).Scan(&admin); err != nil {
			return err
		}
		if !admin {
			r.Reasoning = strings.TrimSpace(r.Reasoning + " (only an admin may change what a channel is usually about)")
			r.effects = []effect{ephemeralEffect(Reply{Text: fmt.Sprintf("Only %s's admins can change what %s is usually about.",
				who.OrgName, conversationWord(r.Kind))}), reactEffect(ReactionCleared)}
			return nil
		}
		var added, removed []string
		for _, pr := range d.Add {
			tag, err := tx.Exec(ctx, `INSERT INTO chat_channel_projects (organization_id, provider, external_workspace_id, conversation_id,
					conversation_name, project_id, set_by)
				VALUES (current_organization_id(), $1, $2, $3, $4, $5, $6) ON CONFLICT DO NOTHING`,
				m.Provider, m.WorkspaceID, m.ConversationID, r.ConversationName, pr.ID, who.Person)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 1 {
				added = append(added, pr.Key)
			}
		}
		for _, pr := range d.Remove {
			tag, err := tx.Exec(ctx, `DELETE FROM chat_channel_projects WHERE provider = $1 AND external_workspace_id = $2
				AND conversation_id = $3 AND project_id = $4`, m.Provider, m.WorkspaceID, m.ConversationID, pr.ID)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 1 {
				removed = append(removed, pr.Key)
			}
		}
		rows, err := tx.Query(ctx, `SELECT p.key_prefix FROM chat_channel_projects c JOIN projects p ON p.id = c.project_id
			WHERE c.provider = $1 AND c.external_workspace_id = $2 AND c.conversation_id = $3 ORDER BY p.key_prefix`,
			m.Provider, m.WorkspaceID, m.ConversationID)
		if err != nil {
			return err
		}
		now, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		if len(added)+len(removed) > 0 {
			if err := chatLedger(ctx, tx, org, EvChatChannelProjects, "person", who.Person, map[string]any{"provider": m.Provider,
				"conversationId": m.ConversationID, "conversationName": r.ConversationName, "added": db.NonNil(added),
				"removed": db.NonNil(removed), "via": "chat"}); err != nil {
				return err
			}
		}
		r.effects = []effect{replyEffect(Reply{Text: channelProjectsReply(r.ConversationName, added, removed, now)}), reactEffect(ReactionDone)}
		return nil
	})
}

// channelProjectsReply says what changed in what a conversation is
// usually about, and what it is about now.
func channelProjectsReply(where string, added, removed, now []string) string {
	if where == "" {
		where = "This conversation"
	}
	var parts []string
	if len(added) > 0 {
		parts = append(parts, "added "+strings.Join(added, ", "))
	}
	if len(removed) > 0 {
		parts = append(parts, "removed "+strings.Join(removed, ", "))
	}
	change := "Nothing changed"
	if len(parts) > 0 {
		change = "Done: " + strings.Join(parts, "; ")
	}
	state := where + " is now usually about " + strings.Join(now, ", ")
	if len(now) == 0 {
		state = where + " is now about no project in particular"
	}
	return change + ". " + state + ". dude takes it as a hint; what a message says still decides."
}

// decision is the routing judgement for the delivery: the one kept on it,
// or asked now and kept before anything acts on it. A model that gave no
// usable answer is kept as failed; only the delivery's own deadline
// leaves nothing kept, for a retry to ask again.
func (s *Service) decision(ctx context.Context, d claimed, org string, p Provider, who asker, m Mention, conv ConversationInfo,
	members []string, h heard, w World) (storedDecision, error) {
	var kept storedDecision
	if len(d.Decision) > 0 && json.Unmarshal(d.Decision, &kept) == nil {
		return kept, nil
	}
	rounds, err := s.rounds(ctx, org, m)
	if err != nil {
		return kept, err
	}
	res, err := s.ask(ctx, org, p, who, m, conv, h, w)
	if ctx.Err() != nil {
		return kept, ctx.Err()
	}
	kept = storedDecision{CallID: res.CallID, Rounds: rounds, Conv: &conv, Members: members, Thread: &h.Thread}
	if len(h.Channel.Messages) > 0 {
		kept.Channel = &h.Channel
	}
	if err != nil {
		kept.Failed = clip(err.Error(), 900)
	} else {
		a := res.Answer.(Answer)
		kept.Answer = &a
	}
	raw, err := json.Marshal(kept)
	if err != nil {
		return kept, err
	}
	err = s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE chat_events SET decision = $3 WHERE provider = $1 AND event_id = $2`, d.Provider, d.ID, raw)
		return err
	})
	return kept, err
}

// readMembers reads the conversation's members (or takes those an earlier
// attempt read, when known) and which of them are linked to the
// organisation's people. Display names are read only for a session made
// here (memberNames).
func (s *Service) readMembers(ctx context.Context, org string, p Provider, inst Installation, r *request, members []string, known bool) error {
	if !known {
		var err error
		if members, err = p.ListMembers(ctx, inst, r.Mention.ConversationID); err != nil {
			s.noteFailure(ctx, org, r.Mention.Provider, err)
			return err
		}
	}
	r.members = slices.DeleteFunc(members, func(u string) bool { return u == inst.BotUserID })
	return s.DB.InOrg(ctx, org, func(tx pgx.Tx) (err error) {
		r.linked, err = connectedIn(ctx, tx, inst, r.members)
		return err
	})
}

// memberNames reads the display names of the conversation's members who
// are not connected, which a session made here names as skipped.
func (s *Service) memberNames(ctx context.Context, p Provider, inst Installation, r *request) {
	var others []string
	for _, u := range r.members {
		if _, ok := r.linked[u]; !ok {
			others = append(others, u)
		}
	}
	r.memberNames = s.names(ctx, p, inst, others)
}

func ptr[T any](v T) *T { return &v }

// react marks the mention, noting a failure for the installation's health.
func (s *Service) react(ctx context.Context, org string, p Provider, inst Installation, m Mention, r Reaction) {
	if m.MessageID == "" {
		return
	}
	s.noteFailure(ctx, org, m.Provider, p.React(ctx, inst, m.ConversationID, m.MessageID, r))
}

// promptConnect answers someone not linked yet: privately, a Connect
// button to the connect page, with a one-time 15-minute state that keeps
// the mention to route once they link.
func (s *Service) promptConnect(ctx context.Context, org string, p Provider, inst Installation, r *request) error {
	state := newState()
	pending, err := json.Marshal(r.Mention)
	if err != nil {
		return err
	}
	r.Route, r.Reasoning = RouteNotConnected, "their account is not connected to dude"
	r.effects = []effect{ephemeralEffect(Reply{
		Text:    fmt.Sprintf("Connect your %s account to dude so it can act for you. I'll pick this message up once you have.", p.Info().Name),
		LinkURL: s.connectLink(r.Mention.Provider, state), LinkLabel: "Connect"}), reactEffect(ReactionCleared)}
	return s.commit(ctx, org, p, inst, r, nil, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO chat_connect_states (state_hash, organization_id, kind, provider, external_workspace_id,
			external_user_id, pending_mention, expires_at) VALUES ($1, current_organization_id(), 'prompt', $2, $3, $4, $5, $6)`,
			hashState(state), r.Mention.Provider, r.Mention.WorkspaceID, r.Mention.UserID, pending, s.now().Add(stateTTL))
		return err
	})
}

// threadOf is where a reply to m goes: its thread, or none in a DM.
func threadOf(m Mention) string {
	if m.Direct && m.ThreadID == m.MessageID {
		return ""
	}
	return m.ThreadID
}

// binding is a thread's chat_threads row.
type binding struct {
	Session, Task, Epic, LastRead, Kind string
}

// bound handles a mention in a thread bound to something, with no model:
// a task's or epic's link, or the message into the session (joining a
// group session's conversation members first). False: not bound.
func (s *Service) bound(ctx context.Context, org string, p Provider, inst Installation, who asker, r *request, pick *picked) (bool, error) {
	m := r.Mention
	var b binding
	var origin string
	err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT COALESCE(t.session_id, ''), COALESCE(t.task_id, ''), COALESCE(t.epic_id, ''), t.last_read_message_id,
				t.conversation_kind, COALESCE(s.origin->>'conversationId', '')
			FROM chat_threads t LEFT JOIN sessions s ON s.id = t.session_id
			WHERE t.provider = $1 AND t.external_workspace_id = $2 AND t.conversation_id = $3 AND t.thread_id = $4`,
			m.Provider, m.WorkspaceID, m.ConversationID, m.ThreadID).Scan(&b.Session, &b.Task, &b.Epic, &b.LastRead, &b.Kind, &origin)
	})
	if db.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if b.Task != "" || b.Epic != "" {
		err := s.linkBound(ctx, org, p, inst, who, r, b, pick)
		if db.IsNotFound(err) {
			// The task or epic went between reading the binding and linking
			// it: the thread is routed as unbound.
			return false, nil
		}
		return true, err
	}
	// Group membership is read on each mention: anyone of the conversation
	// who has connected since joins, the asker among them. Only the
	// session's own conversation shares it: a thread bound by a resume
	// elsewhere sends in only for those who can chat already.
	share := origin == m.ConversationID && (b.Kind == KindPrivate || b.Kind == KindGroup)
	if share && r.members == nil {
		if err := s.readMembers(ctx, org, p, inst, r, nil, false); err != nil {
			return true, err
		}
	}
	since := b.LastRead
	var newer []ThreadMessage
	if since != "" {
		if newer, err = p.FetchThread(ctx, inst, m.ConversationID, m.ThreadID, since); err != nil {
			s.noteFailure(ctx, org, m.Provider, err)
			return true, err
		}
	}
	quoted, err := s.quote(ctx, org, p, inst, newer, m.MessageID)
	if err != nil {
		return true, err
	}
	return true, s.commit(ctx, org, p, inst, r, pick, func(tx pgx.Tx) error {
		if err := delivery.LockSession(ctx, tx, b.Session); err != nil {
			return err
		}
		var title string
		if err := tx.QueryRow(ctx, `SELECT COALESCE(title, '') FROM sessions WHERE id = $1`, b.Session).Scan(&title); err != nil {
			return err
		}
		if share && len(r.members) > 0 {
			var err error
			if r.Added, r.Skipped, err = addMembers(ctx, tx, org, b.Session, r.members, r.linked, who.Person); err != nil {
				return err
			}
		}
		var role *string
		if err := tx.QueryRow(ctx, `SELECT session_role($1, $2)`, b.Session, who.Person).Scan(&role); err != nil {
			return err
		}
		if role == nil || (*role != delivery.SessionOwner && *role != "chat") {
			r.Route, r.Reasoning = RouteNotMember, "the thread's session is not one they can chat in"
			r.effects = []effect{ephemeralEffect(Reply{Text: "This thread's session is private, and you are not in it."}), reactEffect(ReactionCleared)}
			return nil
		}
		r.Route, r.Reasoning, r.ResultKind, r.ResultID = RouteResume, "the thread is bound to this session", "session", b.Session
		r.Confidence = nil
		if err := sendToSession(ctx, tx, org, b.Session, who, m.Text, quoted, m.Provider); err != nil {
			return err
		}
		r.effects = []effect{replyEffect(Reply{Text: "Added to your session " + quoteTitle(title),
			LinkURL: s.link(who.Slug, "sessions/"+url.PathEscape(b.Session)), LinkLabel: "Open in dude"}), sessionMark(pick)}
		if err := awaitSession(ctx, tx, r); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE chat_threads SET last_read_message_id = $5 WHERE provider = $1 AND external_workspace_id = $2
			AND conversation_id = $3 AND thread_id = $4`, m.Provider, m.WorkspaceID, m.ConversationID, m.ThreadID, m.MessageID)
		return err
	})
}

func quoteTitle(t string) string {
	if t == "" {
		return "(untitled)"
	}
	return "“" + t + "”"
}

// linkBound replies with the bound task's or epic's link.
func (s *Service) linkBound(ctx context.Context, org string, p Provider, inst Installation, who asker, r *request, b binding, pick *picked) error {
	return s.commit(ctx, org, p, inst, r, pick, func(tx pgx.Tx) error {
		var label, hash string
		if b.Task != "" {
			var key, title string
			if err := tx.QueryRow(ctx, `SELECT p.key_prefix || '-' || t.number, t.title FROM tasks t JOIN projects p ON p.id = t.project_id
				WHERE t.id = $1`, b.Task).Scan(&key, &title); err != nil {
				return err
			}
			label, hash = fmt.Sprintf("This thread is %s %s", key, title), "task/"+url.PathEscape(b.Task)
			r.ResultKind, r.ResultID = "task", b.Task
		} else {
			var key, title string
			if err := tx.QueryRow(ctx, `SELECT p.key_prefix, e.title FROM epics e JOIN projects p ON p.id = e.project_id WHERE e.id = $1`, b.Epic).
				Scan(&key, &title); err != nil {
				return err
			}
			label, hash = fmt.Sprintf("This thread is the epic %s › %s", key, title), "epic/"+url.PathEscape(b.Epic)
			r.ResultKind, r.ResultID = "epic", b.Epic
		}
		r.Route, r.Reasoning = RouteTaskLink, "the thread is bound to it"
		r.effects = []effect{replyEffect(Reply{Text: label, LinkURL: s.link(who.Slug, hash), LinkLabel: "Open in dude"}), reactEffect(ReactionDone)}
		return nil
	})
}

// addMembers adds to a group session, as chat, every connected active
// member among the conversation's members (linked: user → person) who has
// no role in it yet, and returns those added and the members not
// connected (skipped, by provider id). Members who left keep their access.
func addMembers(ctx context.Context, tx pgx.Tx, org, session string, members []string, linked map[string]string, by string) (added, skipped []string, err error) {
	for _, u := range members {
		person, ok := linked[u]
		if !ok {
			skipped = append(skipped, u)
			continue
		}
		tag, err := tx.Exec(ctx, `INSERT INTO session_people (session_id, person_id, organization_id, role, invited_by, accepted_at)
			VALUES ($1, $2, current_organization_id(), 'chat', $3, now())
			ON CONFLICT (session_id, person_id) DO UPDATE SET accepted_at = now(), role = 'chat'
			WHERE session_people.accepted_at IS NULL AND NOT session_people.becomes_owner`, session, person, by)
		if err != nil {
			return nil, nil, err
		}
		if tag.RowsAffected() == 0 {
			continue
		}
		added = append(added, person)
		if err := delivery.SessionEvent(ctx, tx, delivery.SessionRef(org, session), delivery.EvSessionJoined, "person", by,
			map[string]any{"person": person, "via": JoinedVia}); err != nil {
			return nil, nil, err
		}
	}
	return added, skipped, nil
}

// connectedIn is, of the provider users given, those linked to an active
// member of the organisation: user id → person id.
func connectedIn(ctx context.Context, tx pgx.Tx, inst Installation, users []string) (map[string]string, error) {
	rows, err := tx.Query(ctx, `SELECT i.external_user_id, pe.id FROM chat_identities i
		JOIN people pe ON pe.account_id = i.account_id AND pe.organization_id = current_organization_id() AND pe.removed_at IS NULL
		WHERE i.provider = $1 AND i.external_workspace_id = $2 AND i.external_user_id = ANY($3)`, inst.Provider, inst.WorkspaceID, db.NonNil(users))
	if err != nil {
		return nil, err
	}
	people := map[string]string{}
	for rows.Next() {
		var u, person string
		if err := rows.Scan(&u, &person); err != nil {
			rows.Close()
			return nil, err
		}
		people[u] = person
	}
	rows.Close()
	return people, rows.Err()
}

// sendToSession is the person's message, with the thread's newer messages
// quoted before it, written into the session as theirs: queued for its
// live agent, or starting one. They join an organisation session by
// writing. The caller holds the session's lock.
func sendToSession(ctx context.Context, tx pgx.Tx, org, session string, who asker, text, quoted, provider string) error {
	if err := delivery.JoinByWriting(ctx, tx, delivery.SessionRef(org, session), who.Person, "person", who.Person); err != nil {
		return err
	}
	full := text
	if quoted != "" {
		full = quoted + "\n\n" + text
	}
	wr := delivery.Writer{ActorType: "person", ActorID: who.Person, Person: who.Person, Via: map[string]any{"via": provider}}
	var runID string
	err := tx.QueryRow(ctx, `SELECT r.id FROM runs r WHERE r.session_id = $1 AND `+delivery.LiveBrainstorm+` AND NOT `+delivery.Ending+`
		FOR NO KEY UPDATE`, session).Scan(&runID)
	if db.IsNotFound(err) {
		_, _, err = delivery.StartBrainstorm(ctx, tx, org, session, wr, delivery.Opening{Message: delivery.Attributed(who.Name, full), Shown: text})
		return err
	}
	if err != nil {
		return err
	}
	ref := delivery.RunRef{Org: org, SessionID: session, RunID: runID}
	directive, _, err := delivery.QueueDirective(ctx, tx, ref, delivery.Directive{Text: delivery.Attributed(who.Name, full), Scope: "run"})
	if err != nil {
		return err
	}
	if err := delivery.RequestResumeForMessage(ctx, tx, runID, "a message from chat"); err != nil {
		return err
	}
	return delivery.ChatEvent(ctx, tx, ref, wr, map[string]any{"text": text, "directiveId": directive})
}

// asked is what a clarify request keeps to route the answer: the question
// and the answer it was asked about.
type asked struct {
	Question Asked  `json:"question"`
	Answer   Answer `json:"answer"`
}

// clarify records the question, a button per choice naming the request,
// and then posts it: a retry posts the question recorded, never a second.
func (s *Service) clarify(ctx context.Context, org string, p Provider, inst Installation, r *request, a Answer, d Decision, pick *picked) error {
	r.ID = ids.New(ids.ChatRequest)
	r.Route, r.Rounds = RouteClarify, r.Rounds+1
	var cs []Choice
	for i, c := range d.Question.Choices {
		cs = append(cs, Choice{Label: c.Label, Value: r.ID + ":" + strconv.Itoa(i)})
	}
	r.Question = asked{Question: *d.Question, Answer: a}
	r.effects = []effect{{Kind: "question", Text: d.Question.Text, Choices: cs}, reactEffect(ReactionCleared)}
	return s.commit(ctx, org, p, inst, r, pick, nil)
}

// answer is a click on a clarifying question's choice: only the person who
// was asked, once. The mention is routed again with the choice, and the
// question becomes "X chose Y" once that routing commits.
func (s *Service) answer(ctx context.Context, d claimed, p Provider, in Interaction) error {
	reqID, idx, ok := strings.Cut(in.Value, ":")
	i, err := strconv.Atoi(idx)
	if !ok || err != nil {
		return nil
	}
	var (
		inst   Installation
		q      asked
		m      Mention
		user   string
		rounds int
		found  bool
	)
	err = s.DB.InOrg(ctx, d.Org, func(tx pgx.Tx) error {
		var installed bool
		if inst, installed, err = loadInstallation(ctx, tx, p.Info().ID); err != nil || !installed {
			return err
		}
		var qRaw, mRaw []byte
		err := tx.QueryRow(ctx, `SELECT question, mention, external_user_id, rounds FROM chat_requests
			WHERE id = $1 AND route = 'clarify' AND (answered_at IS NULL OR answered_by_event = $2)`, reqID, d.ID).Scan(&qRaw, &mRaw, &user, &rounds)
		if db.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if json.Unmarshal(qRaw, &q) != nil || json.Unmarshal(mRaw, &m) != nil || i < 0 || i >= len(q.Question.Choices) {
			return nil
		}
		found = true
		return nil
	})
	if err != nil || !found {
		return err
	}
	if user != in.User.ID {
		s.noteFailure(ctx, d.Org, p.Info().ID, p.PostEphemeral(ctx, inst, in.ConversationID, in.ThreadID, in.User.ID,
			Reply{Text: "Only the person who asked can answer this."}))
		return nil
	}
	choice := q.Question.Choices[i]
	by := in.User
	if n := s.names(ctx, p, inst, []string{in.User.ID})[in.User.ID]; n != in.User.ID {
		by.DisplayName = n
	}
	return s.route(ctx, d, p, m, &picked{Answer: Choose(q.Answer, choice), Rounds: rounds, Request: reqID,
		Update: effect{Kind: "update", Message: in.MessageID, ChosenBy: by, Text: choice.Label}})
}

// giveUp ends a delivery dude will not try again (failed transiently
// MaxAttempts times, or deterministically once): Requests says failed and
// why, the person is told privately and the mention marked ❌, as far as
// the provider lets dude. A delivery that already recorded a request (made,
// but its reply never went out) has that request turned failed, keeping
// what it made, and the note and mark are posted directly.
func (s *Service) giveUp(ctx context.Context, d claimed, cause error, transient bool) {
	p, err := s.provider(d.Provider)
	if err != nil {
		return
	}
	var m Mention
	switch d.Kind {
	case "mention":
		_ = json.Unmarshal(d.Payload, &m)
	case "interaction":
		var in Interaction
		_ = json.Unmarshal(d.Payload, &in)
		m = Mention{Provider: d.Provider, WorkspaceID: in.WorkspaceID, ConversationID: in.ConversationID, ThreadID: in.ThreadID, UserID: in.User.ID}
	default:
		var ev storedEvent
		_ = json.Unmarshal(d.Payload, &ev)
		if ev.Mention != nil {
			m = *ev.Mention
		}
	}
	if m.ConversationID == "" || m.UserID == "" {
		return
	}
	var inst Installation
	var ok bool
	var who asker
	if err := s.DB.InOrg(ctx, d.Org, func(tx pgx.Tx) (err error) {
		if inst, ok, err = loadInstallation(ctx, tx, d.Provider); err != nil || !ok {
			return err
		}
		who, err = whoIs(ctx, tx, d.Provider, m.WorkspaceID, m.UserID)
		return err
	}); err != nil || !ok {
		return
	}
	reason := fmt.Sprintf("%s did not answer after %d tries", p.Info().Name, MaxAttempts)
	note := Reply{Text: fmt.Sprintf("Sorry, dude couldn't act on this: %s did not answer. Try again in a while.", p.Info().Name)}
	if !transient {
		reason = "dude could not act on it: " + clip(cause.Error(), 300)
		note = Reply{Text: "Sorry, dude couldn't act on this."}
	}
	var recorded, unanswered bool
	var effects []effect
	// The decision as stored now: the attempt giving up may itself have made
	// and kept the routing call, after d was claimed.
	var decision []byte
	if err := s.DB.InOrg(ctx, d.Org, func(tx pgx.Tx) (err error) {
		if recorded, err = handled(ctx, tx, d.Provider, d.ID); err != nil {
			return err
		}
		if !recorded {
			return tx.QueryRow(ctx, `SELECT decision FROM chat_events WHERE provider = $1 AND event_id = $2`,
				d.Provider, d.ID).Scan(&decision)
		}
		// Made (or refused) but never told: failed, with what it asked and
		// made kept, and the request's own mention (a click's is the one asked).
		var mRaw, eRaw []byte
		err = tx.QueryRow(ctx, `UPDATE chat_requests SET route = 'failed', replied_at = now(),
				asked_route = CASE WHEN route = 'failed' THEN asked_route ELSE COALESCE(asked_route, route) END,
				reasoning = left(btrim(reasoning || ' (' || $3 || ')'), 2000),
				mark_settled = COALESCE(mark_settled, 'failed'), mark_settled_at = COALESCE(mark_settled_at, now())
			WHERE provider = $1 AND event_id = $2 AND replied_at IS NULL RETURNING mention, effects`,
			d.Provider, d.ID, "its reply was not posted: "+reason).Scan(&mRaw, &eRaw)
		if db.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		unanswered = true
		if len(mRaw) > 0 {
			_ = json.Unmarshal(mRaw, &m)
		}
		return json.Unmarshal(eRaw, &effects)
	}); err != nil {
		s.log().Warn("recording that dude gave up failed", "provider", d.Provider, "event", d.ID, "error", err)
		return
	}
	if recorded {
		if !unanswered {
			return
		}
		// A link it made and never posted still reaches the asker, privately.
		for _, e := range effects {
			if e.Kind == "reply" && !e.Done && e.LinkURL != "" {
				note = Reply{Text: e.Text + fmt.Sprintf(" (%s did not take dude's reply in the thread.)", p.Info().Name),
					LinkURL: e.LinkURL, LinkLabel: e.LinkLabel}
				break
			}
		}
		s.noteFailure(ctx, d.Org, d.Provider, p.PostEphemeral(ctx, inst, m.ConversationID, threadOf(m), m.UserID, note))
		s.react(ctx, d.Org, p, inst, m, ReactionFailed)
		return
	}
	r := &request{EventID: d.ID, Mention: m, Route: RouteFailed, Person: who.Person, UserName: who.Display, Reasoning: reason,
		effects: []effect{ephemeralEffect(note), reactEffect(ReactionFailed)}}
	// A routing call already made and kept is the model asked, whatever
	// failed after it; a click's choice asked nothing.
	var kept storedDecision
	if d.Kind != "interaction" && len(decision) > 0 && json.Unmarshal(decision, &kept) == nil {
		r.ModelCall = kept.CallID
	}
	if r.UserName == "" {
		r.UserName = s.names(ctx, p, inst, []string{m.UserID})[m.UserID]
	}
	if err := s.commit(ctx, d.Org, p, inst, r, nil, nil); err != nil {
		s.log().Warn("telling the person dude gave up failed", "provider", d.Provider, "event", d.ID, "error", err)
	}
}
