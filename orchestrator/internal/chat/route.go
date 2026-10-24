package chat

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	"github.com/marciomartins/dude/orchestrator/internal/judge"
)

// JoinedVia is session.joined's via for someone added to a group session
// because they are in its conversation.
const JoinedVia = "chat_conversation"

// Sweep routes stored deliveries: each mention, kept mention and clicked
// choice once, and a workspace's removal. A delivery whose routing failed
// before it recorded a request is tried again later, longer each time; one
// that recorded its request is done (its reply already went, or never will).
func (s *Service) Sweep(ctx context.Context) (int, error) {
	type pending struct {
		Provider, ID, Org, Kind string
		Payload                 json.RawMessage
	}
	var batch []pending
	if err := s.DB.InSystem(ctx, "chat", func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT provider, event_id, organization_id, kind, payload FROM chat_events
			WHERE processed_at IS NULL AND attempts < 10 AND (next_attempt_at IS NULL OR next_attempt_at <= now())
			ORDER BY received_at LIMIT 20`)
		if err != nil {
			return err
		}
		batch, err = pgx.CollectRows(rows, pgx.RowToStructByPos[pending])
		return err
	}); err != nil {
		return 0, err
	}
	for _, d := range batch {
		err := s.process(ctx, d.Org, d.Provider, d.ID, d.Kind, d.Payload)
		if err := s.DB.InOrg(ctx, d.Org, func(tx pgx.Tx) error {
			if err == nil {
				_, e := tx.Exec(ctx, `UPDATE chat_events SET processed_at = now(), last_error = NULL WHERE provider = $1 AND event_id = $2`,
					d.Provider, d.ID)
				return e
			}
			_, e := tx.Exec(ctx, `UPDATE chat_events SET attempts = attempts + 1, last_error = $3,
				next_attempt_at = now() + make_interval(secs => 5 * power(2, attempts)) WHERE provider = $1 AND event_id = $2`,
				d.Provider, d.ID, clip(err.Error(), 2000))
			return e
		}); err != nil {
			return 0, err
		}
		if err != nil {
			s.log().Warn("chat delivery failed", "provider", d.Provider, "event", d.ID, "error", err)
		}
	}
	return len(batch), nil
}

func (s *Service) process(ctx context.Context, org, providerID, eventID, kind string, payload json.RawMessage) error {
	p, err := s.provider(providerID)
	if err != nil {
		return nil
	}
	switch kind {
	case "mention":
		var m Mention
		if err := json.Unmarshal(payload, &m); err != nil {
			return nil
		}
		return s.route(ctx, org, p, eventID, m, nil)
	case "interaction":
		var in Interaction
		if err := json.Unmarshal(payload, &in); err != nil {
			return nil
		}
		return s.answer(ctx, org, p, eventID, in)
	}
	var ev storedEvent
	if err := json.Unmarshal(payload, &ev); err != nil {
		return nil
	}
	switch ev.Kind {
	case EventMention:
		if ev.Mention == nil {
			return nil
		}
		return s.route(ctx, org, p, eventID, *ev.Mention, nil)
	case EventUninstalled, EventTokensRevoked:
		// The workspace took the app out: nothing it issued works now.
		return s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `DELETE FROM chat_installations WHERE provider = $1`, providerID)
			return err
		})
	}
	return nil
}

// asker is the person a mention is from, as dude knows them.
type asker struct {
	Person, Name, Slug, OrgName string
	Linked                      bool
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
	err := tx.QueryRow(ctx, `SELECT account_id FROM chat_identities WHERE provider = $1 AND external_workspace_id = $2 AND external_user_id = $3`,
		provider, workspace, user).Scan(&account)
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
	QuestionMessage             string
	// The conversation's members, for a private or group conversation.
	members []string
}

// record writes the request, in tx: false when the delivery already has one
// (it was handled before), which the caller then leaves alone. Requests
// over 30 days old go as each new one comes.
func record(ctx context.Context, tx pgx.Tx, r *request) (bool, error) {
	if r.ID == "" {
		r.ID = ids.New(ids.ChatRequest)
	}
	mention, _ := json.Marshal(r.Mention)
	var question []byte
	if r.Question != nil {
		question, _ = json.Marshal(r.Question)
	}
	tag, err := tx.Exec(ctx, `INSERT INTO chat_requests (id, organization_id, provider, event_id, external_workspace_id, conversation_id,
			conversation_kind, conversation_name, thread_id, external_user_id, external_user_name, person_id, text, permalink, context_hash,
			route, asked_route, confidence, reasoning, rounds, result_kind, result_id, shared_added, shared_skipped, model_call_id,
			mention, question, question_message_id)
		VALUES ($1, current_organization_id(), $2, $3, $4, $5, NULLIF($6, ''), $7, $8, $9, $10, NULLIF($11, ''), $12, $13, NULLIF($14, ''),
			$15, NULLIF($16, ''), $17, $18, $19, NULLIF($20, ''), NULLIF($21, ''), $22, $23, NULLIF($24, ''), $25, $26, NULLIF($27, ''))
		ON CONFLICT (provider, event_id) DO NOTHING`,
		r.ID, r.Mention.Provider, r.EventID, r.Mention.WorkspaceID, r.Mention.ConversationID, r.Kind, r.ConversationName,
		r.Mention.ThreadID, r.Mention.UserID, r.UserName, r.Person, clip(r.Mention.Text, 4000), r.Permalink, r.ContextHash,
		r.Route, r.AskedRoute, r.Confidence, r.Reasoning, r.Rounds, r.ResultKind, r.ResultID, db.NonNil(r.Added), db.NonNil(r.Skipped),
		r.ModelCall, mention, question, r.QuestionMessage)
	if err != nil || tag.RowsAffected() == 0 {
		return false, err
	}
	_, err = tx.Exec(ctx, `DELETE FROM chat_requests WHERE created_at < now() - interval '30 days'`)
	return true, err
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

// picked is a clarifying question's answer, routing a mention again.
type picked struct {
	Answer  Answer
	Rounds  int
	Request string
}

// route is the routing flow for one mention: installed, connected, a
// member; a bound thread's link or session; else one routing judgement,
// checked, made, bound and replied to, each step recorded in Requests.
func (s *Service) route(ctx context.Context, org string, p Provider, eventID string, m Mention, pick *picked) error {
	var (
		inst Installation
		ok   bool
		who  asker
		done bool
	)
	err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) (err error) {
		if done, err = handled(ctx, tx, m.Provider, eventID); err != nil || done {
			return err
		}
		if inst, ok, err = loadInstallation(ctx, tx, m.Provider); err != nil || !ok {
			return err
		}
		if inst.WorkspaceID != m.WorkspaceID {
			ok = false
			return nil
		}
		who, err = whoIs(ctx, tx, m.Provider, m.WorkspaceID, m.UserID)
		return err
	})
	if err != nil || done || !ok {
		return err
	}
	r := &request{EventID: eventID, Mention: m, Person: who.Person, Permalink: p.Permalink(inst, m.ConversationID, m.MessageID, m.ThreadID)}
	if pick == nil {
		s.react(ctx, org, p, inst, m, ReactionWorking)
	}
	if u, err := p.User(ctx, inst, m.UserID); err == nil {
		r.UserName = u.Name()
	} else {
		r.UserName = m.UserID
	}
	if !who.Linked {
		return s.promptConnect(ctx, org, p, inst, who, r)
	}
	if who.Person == "" {
		r.Route, r.Reasoning = RouteNotMember, "their linked account is not an active member of the organisation"
		return s.refuse(ctx, org, p, inst, r, Reply{Text: "Sorry, dude can't act on this for you."})
	}
	conv, err := p.Conversation(ctx, inst, m.ConversationID)
	if err != nil {
		s.noteFailure(ctx, org, m.Provider, err)
		return err
	}
	r.Kind, r.ConversationName = conv.Kind, conv.Name
	// Who is in a private conversation decides who a session is shared
	// with, and which sessions a mention there may resume.
	var connected []string
	if conv.Kind == KindPrivate || conv.Kind == KindGroup {
		if r.members, err = p.ListMembers(ctx, inst, m.ConversationID); err != nil {
			s.noteFailure(ctx, org, m.Provider, err)
			return err
		}
		if connected, err = s.connectedPeople(ctx, org, inst, r.members); err != nil {
			return err
		}
	}
	if !m.Direct {
		if bound, err := s.bound(ctx, org, p, inst, who, r); bound || err != nil {
			return err
		}
	}
	thread, err := s.thread(ctx, org, p, inst, m)
	if err != nil {
		return err
	}
	r.ContextHash = thread.hash()
	world, err := s.world(ctx, org, who.Person, m, conv.Kind, connected)
	if err != nil {
		return err
	}
	var d Decision
	var a Answer
	if pick != nil {
		a, r.Rounds = pick.Answer, pick.Rounds
		d = Decide(a, world, r.Rounds, true, m.Text)
		r.Reasoning, r.Confidence = "chosen in the clarifying question", ptr(1.0)
	} else {
		if r.Rounds, err = s.rounds(ctx, org, m); err != nil {
			return err
		}
		res, err := s.ask(ctx, org, p, who, m, conv, thread, world)
		r.ModelCall = res.CallID
		if err != nil {
			// No answer, a proxy error or invalid JSON: a brainstorm.
			r.Route, r.Reasoning = RouteFailed, clip("the routing call failed: "+err.Error(), 1000)
			return s.make(ctx, org, p, inst, who, r, thread, Decision{Route: RouteBrainstorm, Title: clip(trimmed(m.Text), 80)})
		}
		a = res.Answer.(Answer)
		r.Confidence, r.Reasoning = ptr(a.Confidence), a.Reasoning
		d = Decide(a, world, r.Rounds, false, m.Text)
	}
	if d.Problem != "" {
		r.AskedRoute = d.AskedRoute
		r.Reasoning = strings.TrimSpace(r.Reasoning + " (" + d.Problem + ")")
	}
	if d.Route == RouteClarify {
		return s.clarify(ctx, org, p, inst, r, a, d)
	}
	return s.make(ctx, org, p, inst, who, r, thread, d)
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
func (s *Service) promptConnect(ctx context.Context, org string, p Provider, inst Installation, who asker, r *request) error {
	state := newState()
	pending, _ := json.Marshal(r.Mention)
	r.Route, r.Reasoning = RouteNotConnected, "their account is not connected to dude"
	var first bool
	if err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) (err error) {
		if first, err = record(ctx, tx, r); err != nil || !first {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO chat_connect_states (state_hash, organization_id, kind, provider, external_workspace_id,
			external_user_id, pending_mention, expires_at) VALUES ($1, current_organization_id(), 'prompt', $2, $3, $4, $5, $6)`,
			hashState(state), r.Mention.Provider, r.Mention.WorkspaceID, r.Mention.UserID, pending, s.now().Add(stateTTL))
		return err
	}); err != nil || !first {
		return err
	}
	name := p.Info().Name
	reply := Reply{Text: fmt.Sprintf("Connect your %s account to dude so it can act for you. I'll pick this message up once you have.", name),
		LinkURL:   s.link(who.Slug, "connect/"+url.PathEscape(r.Mention.Provider)+"?state="+url.QueryEscape(state)),
		LinkLabel: "Connect"}
	s.noteFailure(ctx, org, r.Mention.Provider, p.PostEphemeral(ctx, inst, r.Mention.ConversationID, threadOf(r.Mention), r.Mention.UserID, reply))
	s.react(ctx, org, p, inst, r.Mention, ReactionCleared)
	return nil
}

// threadOf is where a reply to m goes: its thread, or none in a DM.
func threadOf(m Mention) string {
	if m.Direct && m.ThreadID == m.MessageID {
		return ""
	}
	return m.ThreadID
}

// refuse records the request and tells the person privately.
func (s *Service) refuse(ctx context.Context, org string, p Provider, inst Installation, r *request, reply Reply) error {
	var first bool
	if err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) (err error) {
		first, err = record(ctx, tx, r)
		return err
	}); err != nil || !first {
		return err
	}
	s.noteFailure(ctx, org, r.Mention.Provider, p.PostEphemeral(ctx, inst, r.Mention.ConversationID, threadOf(r.Mention), r.Mention.UserID, reply))
	s.react(ctx, org, p, inst, r.Mention, ReactionCleared)
	return nil
}

// binding is a thread's chat_threads row.
type binding struct {
	Session, Task, Epic, LastRead, Kind string
}

// bound handles a mention in a thread bound to something, with no model:
// a task's or epic's link, or the message into the session (joining a
// group session's conversation members first). False: not bound.
func (s *Service) bound(ctx context.Context, org string, p Provider, inst Installation, who asker, r *request) (bool, error) {
	m := r.Mention
	var b binding
	err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT COALESCE(session_id, ''), COALESCE(task_id, ''), COALESCE(epic_id, ''), last_read_message_id,
				conversation_kind FROM chat_threads WHERE provider = $1 AND external_workspace_id = $2 AND conversation_id = $3 AND thread_id = $4`,
			m.Provider, m.WorkspaceID, m.ConversationID, m.ThreadID).Scan(&b.Session, &b.Task, &b.Epic, &b.LastRead, &b.Kind)
	})
	if db.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if b.Task != "" || b.Epic != "" {
		return true, s.linkBound(ctx, org, p, inst, who, r, b)
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
	var title string
	var first, can bool
	err = s.DB.InOrg(ctx, org, func(tx pgx.Tx) (err error) {
		if err := delivery.LockSession(ctx, tx, b.Session); err != nil {
			return err
		}
		// Group membership is read on each mention: anyone of the
		// conversation who has connected since joins, the asker among them.
		// Only the session's own conversation shares it: a thread bound by
		// a resume elsewhere sends in only for those who can chat already.
		var origin string
		if err := tx.QueryRow(ctx, `SELECT COALESCE(origin->>'conversationId', '') FROM sessions WHERE id = $1`, b.Session).Scan(&origin); err != nil {
			return err
		}
		if len(r.members) > 0 && origin == m.ConversationID && (b.Kind == KindPrivate || b.Kind == KindGroup) {
			if r.Added, r.Skipped, err = addMembers(ctx, tx, org, inst, b.Session, r.members, who.Person); err != nil {
				return err
			}
		}
		var role *string
		if err := tx.QueryRow(ctx, `SELECT session_role($1, $2), COALESCE(title, '') FROM sessions WHERE id = $1`, b.Session, who.Person).
			Scan(&role, &title); err != nil {
			return err
		}
		if can = role != nil && (*role == delivery.SessionOwner || *role == "chat"); !can {
			r.Route, r.Reasoning = RouteNotMember, "the thread's session is not one they can chat in"
			first, err = record(ctx, tx, r)
			return err
		}
		r.Route, r.Reasoning, r.ResultKind, r.ResultID = RouteResume, "the thread is bound to this session", "session", b.Session
		r.Confidence = nil
		if first, err = record(ctx, tx, r); err != nil || !first {
			return err
		}
		if err := sendToSession(ctx, tx, org, b.Session, who, m.Text, quoted, m.Provider); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE chat_threads SET last_read_message_id = $5 WHERE provider = $1 AND external_workspace_id = $2
			AND conversation_id = $3 AND thread_id = $4`, m.Provider, m.WorkspaceID, m.ConversationID, m.ThreadID, m.MessageID)
		return err
	})
	if err != nil || !first {
		return true, err
	}
	if !can {
		s.noteFailure(ctx, org, m.Provider, p.PostEphemeral(ctx, inst, m.ConversationID, threadOf(m), m.UserID,
			Reply{Text: "This thread's session is private, and you are not in it."}))
		s.react(ctx, org, p, inst, m, ReactionCleared)
		return true, nil
	}
	s.kick()
	text := "Added to your session " + quoteTitle(title)
	s.reply(ctx, org, p, inst, m, Reply{Text: text, LinkURL: s.link(who.Slug, "sessions/"+url.PathEscape(b.Session)), LinkLabel: "Open in dude"})
	return true, nil
}

func quoteTitle(t string) string {
	if t == "" {
		return "(untitled)"
	}
	return "“" + t + "”"
}

// linkBound replies with the bound task's or epic's link.
func (s *Service) linkBound(ctx context.Context, org string, p Provider, inst Installation, who asker, r *request, b binding) error {
	var label, hash string
	var first bool
	err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) (err error) {
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
		first, err = record(ctx, tx, r)
		return err
	})
	if err != nil || !first {
		return err
	}
	s.reply(ctx, org, p, inst, r.Mention, Reply{Text: label, LinkURL: s.link(who.Slug, hash), LinkLabel: "Open in dude"})
	return nil
}

// reply posts in the mention's thread and marks it done.
func (s *Service) reply(ctx context.Context, org string, p Provider, inst Installation, m Mention, reply Reply) {
	_, err := p.PostReply(ctx, inst, m.ConversationID, threadOf(m), reply)
	s.noteFailure(ctx, org, m.Provider, err)
	if err != nil {
		s.react(ctx, org, p, inst, m, ReactionFailed)
		return
	}
	s.react(ctx, org, p, inst, m, ReactionDone)
}

// addMembers adds to a group session, as chat, every connected active
// member among the conversation's members who has no role in it yet, and
// returns those added and the members not connected (skipped, by
// provider id). Bots are neither. Members who left keep their access.
func addMembers(ctx context.Context, tx pgx.Tx, org string, inst Installation, session string, members []string, by string) (added, skipped []string, err error) {
	members = slices.DeleteFunc(slices.Clone(members), func(u string) bool { return u == inst.BotUserID })
	people, err := connectedIn(ctx, tx, inst, members)
	if err != nil {
		return nil, nil, err
	}
	for _, u := range members {
		person, ok := people[u]
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

// connectedPeople is the person ids of the connected members among users.
func (s *Service) connectedPeople(ctx context.Context, org string, inst Installation, users []string) ([]string, error) {
	var out []string
	err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		people, err := connectedIn(ctx, tx, inst, users)
		for _, p := range people {
			out = append(out, p)
		}
		return err
	})
	slices.Sort(out)
	return out, err
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

// threadContext is the thread a mention is in, as the model and the
// session are told it.
type threadContext struct {
	Messages []quotedMessage
}

type quotedMessage struct {
	By     string `json:"by"`
	Handle string `json:"handle,omitempty"`
	Person string `json:"person,omitempty"`
	Text   string `json:"text"`
	InDude bool   `json:"inDude"`
}

func (t threadContext) hash() string {
	sum := sha256.New()
	for _, m := range t.Messages {
		fmt.Fprintf(sum, "%s\x00%s\x00", m.By, m.Text)
	}
	return hex.EncodeToString(sum.Sum(nil))
}

// render is the thread as text: each message by its author, people not
// in dude marked so.
func (t threadContext) render() string {
	var b strings.Builder
	for _, m := range t.Messages {
		by := m.By
		if !m.InDude {
			by += " (not in dude)"
		}
		fmt.Fprintf(&b, "> %s: %s\n", by, strings.ReplaceAll(m.Text, "\n", "\n> "))
	}
	return strings.TrimRight(b.String(), "\n")
}

const threadMessagesMax = 30

// thread reads the mention's thread, without the mention: everyone's
// messages, members' and not (dude's own left out).
func (s *Service) thread(ctx context.Context, org string, p Provider, inst Installation, m Mention) (threadContext, error) {
	if m.ThreadID == "" || m.ThreadID == m.MessageID {
		return threadContext{}, nil
	}
	msgs, err := p.FetchThread(ctx, inst, m.ConversationID, m.ThreadID, "")
	if err != nil {
		s.noteFailure(ctx, org, m.Provider, err)
		return threadContext{}, err
	}
	quoted, err := s.quoteMessages(ctx, org, p, inst, msgs, m.MessageID)
	return threadContext{Messages: quoted}, err
}

// quote is newer thread messages as the text put before a message into a session.
func (s *Service) quote(ctx context.Context, org string, p Provider, inst Installation, msgs []ThreadMessage, skip string) (string, error) {
	quoted, err := s.quoteMessages(ctx, org, p, inst, msgs, skip)
	if err != nil || len(quoted) == 0 {
		return "", err
	}
	return "Earlier in the thread:\n" + threadContext{Messages: quoted}.render(), nil
}

func (s *Service) quoteMessages(ctx context.Context, org string, p Provider, inst Installation, msgs []ThreadMessage, skip string) ([]quotedMessage, error) {
	var users []string
	for _, msg := range msgs {
		if !msg.Bot && msg.ID != skip && msg.UserID != "" && !slices.Contains(users, msg.UserID) {
			users = append(users, msg.UserID)
		}
	}
	people := map[string][2]string{}
	if len(users) > 0 {
		if err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
			rows, err := tx.Query(ctx, `SELECT i.external_user_id, pe.id, pe.name FROM chat_identities i
				JOIN people pe ON pe.account_id = i.account_id AND pe.organization_id = current_organization_id() AND pe.removed_at IS NULL
				WHERE i.provider = $1 AND i.external_workspace_id = $2 AND i.external_user_id = ANY($3)`, inst.Provider, inst.WorkspaceID, users)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var u, id, name string
				if err := rows.Scan(&u, &id, &name); err != nil {
					return err
				}
				people[u] = [2]string{id, name}
			}
			return rows.Err()
		}); err != nil {
			return nil, err
		}
	}
	names := map[string]string{}
	var out []quotedMessage
	for _, msg := range msgs {
		if msg.Bot || msg.ID == skip || msg.UserID == inst.BotUserID {
			continue
		}
		q := quotedMessage{Text: clip(msg.Text, 2000)}
		if person, ok := people[msg.UserID]; ok {
			q.Person, q.By, q.InDude = person[0], person[1], true
		} else {
			if _, ok := names[msg.UserID]; !ok {
				names[msg.UserID] = msg.UserID
				if u, err := p.User(ctx, inst, msg.UserID); err == nil {
					names[msg.UserID] = u.Name()
				}
			}
			q.By = names[msg.UserID]
		}
		out = append(out, q)
	}
	if len(out) > threadMessagesMax {
		out = out[len(out)-threadMessagesMax:]
	}
	return out, nil
}

// world is what the person may route to: every project of the
// organisation with its epics not done, and the chat-born sessions they
// can chat in from the last 14 days, each with whether every connected
// member of the mention's conversation (connected, by person id) can chat
// in it too.
func (s *Service) world(ctx context.Context, org, person string, m Mention, kind string, connected []string) (World, error) {
	w := World{Conversation: m.ConversationID, Kind: kind}
	err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT p.id, p.key_prefix, p.name, COALESCE(json_agg(json_build_object('ID', e.id, 'Title', e.title)
				ORDER BY e.position) FILTER (WHERE e.id IS NOT NULL), '[]')
			FROM projects p LEFT JOIN epics e ON e.project_id = p.id AND COALESCE(e.state, 'active') <> 'done'
			WHERE p.key_prefix IS NOT NULL GROUP BY p.id ORDER BY p.name LIMIT 100`)
		if err != nil {
			return err
		}
		w.Projects, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (Project, error) {
			var p Project
			err := row.Scan(&p.ID, &p.Key, &p.Name, &p.Epics)
			return p, err
		})
		if err != nil {
			return err
		}
		rows, err = tx.Query(ctx, `SELECT s.id, COALESCE(s.title, ''), s.access, COALESCE(s.origin->>'conversationId', ''),
				COALESCE(s.origin->>'conversationKind', ''),
				NOT EXISTS (SELECT 1 FROM unnest($3::text[]) AS c(person) WHERE COALESCE(session_role(s.id, c.person), '') NOT IN ('owner', 'chat'))
			FROM sessions s
			WHERE s.origin IS NOT NULL AND s.created_at > now() - make_interval(days => $2)
			  AND session_role(s.id, $1) IN ('owner', 'chat') ORDER BY s.created_at DESC LIMIT 20`, person, resumeLookback, db.NonNil(connected))
		if err != nil {
			return err
		}
		w.Sessions, err = pgx.CollectRows(rows, pgx.RowToStructByPos[SessionRef])
		return err
	})
	return w, err
}

// rounds is how many clarifying questions this thread was asked.
func (s *Service) rounds(ctx context.Context, org string, m Mention) (int, error) {
	var n int
	err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM chat_requests WHERE provider = $1 AND external_workspace_id = $2
			AND conversation_id = $3 AND thread_id = $4 AND route = 'clarify'`, m.Provider, m.WorkspaceID, m.ConversationID, m.ThreadID).Scan(&n)
	})
	return n, err
}

// Instructions are the routing call's instructions in order: dude's
// built-in ones, the organisation's additional ones, the provider's.
func Instructions(orgName, organisation, providerName, provider string) string {
	parts := []string{BuiltInInstructions}
	if t := strings.TrimSpace(organisation); t != "" {
		parts = append(parts, "## "+orgName+"'s additional instructions\n\n"+t)
	}
	if t := strings.TrimSpace(provider); t != "" {
		parts = append(parts, "## Additional instructions for "+providerName+"\n\n"+t)
	}
	return strings.Join(parts, "\n\n")
}

// ask is the one routing judgement, on the organisation's Judgements tier.
func (s *Service) ask(ctx context.Context, org string, p Provider, who asker, m Mention, conv ConversationInfo, t threadContext, w World) (judge.Structured, error) {
	if s.Judge == nil {
		return judge.Structured{}, errors.New("no judge is configured")
	}
	var orgText, providerText string
	if err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT o.chat_instructions, COALESCE(i.settings->>'additional_instructions', '')
			FROM organizations o LEFT JOIN chat_installations i ON i.organization_id = o.id AND i.provider = $1
			WHERE o.id = current_organization_id()`, m.Provider).Scan(&orgText, &providerText)
	}); err != nil {
		return judge.Structured{}, err
	}
	var in strings.Builder
	fmt.Fprintf(&in, "## The message\n\n%s wrote in %s on %s:\n\n%s\n", who.Name, conv.Name, p.Info().Name, m.Text)
	if len(t.Messages) > 0 {
		fmt.Fprintf(&in, "\n## The thread before it\n\n%s\n", t.render())
	}
	in.WriteString("\n## Projects they can file in\n\n")
	if len(w.Projects) == 0 {
		in.WriteString("None.\n")
	}
	for _, pr := range w.Projects {
		fmt.Fprintf(&in, "- %s (%s)", pr.Key, pr.Name)
		if len(pr.Epics) > 0 {
			var titles []string
			for _, e := range pr.Epics {
				titles = append(titles, strconv.Quote(e.Title))
			}
			fmt.Fprintf(&in, ", epics: %s", strings.Join(titles, ", "))
		}
		in.WriteString("\n")
	}
	if resumable := w.Resumable(); len(resumable) > 0 {
		in.WriteString("\n## Their chat sessions from the last 14 days\n\n")
		for _, se := range resumable {
			fmt.Fprintf(&in, "- %s: %s\n", se.ID, quoteTitle(se.Title))
		}
	}
	return s.Judge.Structured(ctx, org, judge.StructuredAsk{Asker: "chat.route", About: p.Info().Name + " · " + conv.Name,
		Instructions: Instructions(who.OrgName, orgText, p.Info().Name, providerText), Input: in.String(), MaxTokens: 4096,
		Parse: func(obj []byte) (any, judge.Judgement, error) {
			a, err := ParseAnswer(obj)
			if err != nil {
				return nil, judge.Judgement{}, err
			}
			return a, judge.Judgement{Decision: a.Route, Confidence: a.Confidence, Reasoning: a.Reasoning}, nil
		}})
}

// asked is what a clarify request keeps to route the answer: the question
// and the answer it was asked about.
type asked struct {
	Question Asked  `json:"question"`
	Answer   Answer `json:"answer"`
}

// clarify posts the question with a button per choice and records it.
func (s *Service) clarify(ctx context.Context, org string, p Provider, inst Installation, r *request, a Answer, d Decision) error {
	r.ID = ids.New(ids.ChatRequest)
	r.Route, r.Rounds = RouteClarify, r.Rounds+1
	var cs []Choice
	for i, c := range d.Question.Choices {
		cs = append(cs, Choice{Label: c.Label, Value: r.ID + ":" + strconv.Itoa(i)})
	}
	msg, err := p.PostQuestion(ctx, inst, r.Mention.ConversationID, threadOf(r.Mention), Question{Text: d.Question.Text, Choices: cs})
	if err != nil {
		s.noteFailure(ctx, org, r.Mention.Provider, err)
		return err
	}
	r.Question, r.QuestionMessage = asked{Question: *d.Question, Answer: a}, msg
	if err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		_, err := record(ctx, tx, r)
		return err
	}); err != nil {
		return err
	}
	s.react(ctx, org, p, inst, r.Mention, ReactionCleared)
	return nil
}

// answer is a click on a clarifying question's choice: only the person who
// was asked, once. The question becomes "X chose Y" and the mention is
// routed again with the choice.
func (s *Service) answer(ctx context.Context, org string, p Provider, eventID string, in Interaction) error {
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
		mine   bool
	)
	err = s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		var installed bool
		if inst, installed, err = loadInstallation(ctx, tx, p.Info().ID); err != nil || !installed {
			return err
		}
		var qRaw, mRaw []byte
		err := tx.QueryRow(ctx, `SELECT question, mention, external_user_id, rounds FROM chat_requests
			WHERE id = $1 AND route = 'clarify' AND answered_at IS NULL FOR UPDATE`, reqID).Scan(&qRaw, &mRaw, &user, &rounds)
		if db.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if json.Unmarshal(qRaw, &q) != nil || json.Unmarshal(mRaw, &m) != nil || i < 0 || i >= len(q.Question.Choices) {
			return nil
		}
		found, mine = true, user == in.User.ID
		if !mine {
			return nil
		}
		_, err = tx.Exec(ctx, `UPDATE chat_requests SET answered_at = now() WHERE id = $1`, reqID)
		return err
	})
	if err != nil || !found {
		return err
	}
	if !mine {
		s.noteFailure(ctx, org, p.Info().ID, p.PostEphemeral(ctx, inst, in.ConversationID, in.ThreadID, in.User.ID,
			Reply{Text: "Only the person who asked can answer this."}))
		return nil
	}
	choice := q.Question.Choices[i]
	by := in.User
	if u, err := p.User(ctx, inst, in.User.ID); err == nil {
		by = u
	}
	s.noteFailure(ctx, org, p.Info().ID, p.UpdateQuestion(ctx, inst, in.ConversationID, in.MessageID, by, choice.Label))
	return s.route(ctx, org, p, eventID, m, &picked{Answer: Choose(q.Answer, choice), Rounds: rounds, Request: reqID})
}
