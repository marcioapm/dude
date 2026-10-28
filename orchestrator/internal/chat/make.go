package chat

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/ids"
)

// errHandled ends a transaction that found its delivery already made a
// request: whatever it did is rolled back.
var errHandled = errors.New("the delivery was handled already")

// Origin is where a session, task or epic came from (their origin column).
// A session's also carries the thread as quoted and how it was routed, for
// its routing line.
type Origin struct {
	Provider         string `json:"provider"`
	WorkspaceID      string `json:"workspaceId"`
	ConversationID   string `json:"conversationId"`
	ConversationName string `json:"conversationName"`
	ConversationKind string `json:"conversationKind"`
	ThreadID         string `json:"threadId"`
	// The mention was at the conversation's top level, not in a thread:
	// its context is the channel before it.
	TopLevel    bool            `json:"topLevel,omitempty"`
	Permalink   string          `json:"permalink"`
	RequestedBy string          `json:"requestedBy"`
	Thread      []quotedMessage `json:"thread,omitempty"`
	// The channel before a top-level mention, as quoted.
	Channel []quotedMessage `json:"channel,omitempty"`
	Routing *routingFacts   `json:"routing,omitempty"`
}

type routingFacts struct {
	Route      string   `json:"route"`
	AskedRoute string   `json:"askedRoute,omitempty"`
	Confidence *float64 `json:"confidence"`
	Reasoning  string   `json:"reasoning"`
	Added      []string `json:"added"`
	Skipped    []string `json:"skipped"`
	// The skipped people's display names, in Skipped's order, for the routing line.
	SkippedNames []string `json:"skippedNames"`
	RequestID    string   `json:"requestId"`
}

// make carries a decision out: a session (with access from where the
// mention was made), a resumed session, a task, or an epic with its tasks;
// binds the thread to it, records the request and replies with its link.
// A decision the database refuses (a relation, a project gone) is not
// tried again: it becomes a brainstorm, and a brainstorm refused is
// recorded as failed and the person told privately.
func (s *Service) make(ctx context.Context, org string, p Provider, inst Installation, who asker, r *request, h heard, d Decision,
	pick *picked) error {
	err := s.makeOnce(ctx, org, p, inst, who, r, h, d, pick)
	if err == nil {
		return nil
	}
	if transient, _ := IsTransient(err); transient {
		return err
	}
	s.log().Warn("a chat decision could not be made", "organization", org, "route", d.Route, "error", err)
	r.ResultKind, r.ResultID, r.Added, r.Skipped, r.AwaitsAfter, r.MadeRoute = "", "", nil, nil, nil, ""
	var refused []string
	if d.Route != RouteBrainstorm {
		r.AskedRoute = d.Route
		refused = append(refused, clip(err.Error(), 300))
		r.Reasoning = strings.TrimSpace(r.Reasoning + " (" + refused[0] + ")")
		fallback := Decision{Route: RouteBrainstorm, Title: d.Title, Project: d.Project}
		if err = s.makeOnce(ctx, org, p, inst, who, r, h, fallback, pick); err == nil {
			return nil
		}
		if transient, _ := IsTransient(err); transient {
			return err
		}
	}
	cause := clip(err.Error(), 300)
	r.Route, r.Reasoning, r.MadeRoute = RouteFailed, strings.TrimSpace(r.Reasoning+" ("+cause+")"), ""
	// In the order they happened: a failed routing call's error, the
	// decision's refusal, then its brainstorm's.
	r.Failure = strings.TrimPrefix(r.Failure+"; "+strings.Join(append(refused, cause), "; "), "; ")
	r.effects = []effect{ephemeralEffect(Reply{Text: "Sorry, dude couldn't act on this."}), reactEffect(ReactionFailed)}
	return s.commit(ctx, org, p, inst, r, pick, nil)
}

func (s *Service) makeOnce(ctx context.Context, org string, p Provider, inst Installation, who asker, r *request, h heard, d Decision,
	pick *picked) error {
	m := r.Mention
	// A failed routing call is logged as failed, whatever it then made.
	if r.Route != RouteFailed {
		r.Route = d.Route
	}
	var members []string
	if d.Route == RouteBrainstorm && (r.Kind == KindPrivate || r.Kind == KindGroup) {
		members = r.members
		if r.memberNames == nil {
			s.memberNames(ctx, p, inst, r)
		}
	}
	r.ID = ids.New(ids.ChatRequest)
	r.offer = nil
	origin := Origin{Provider: m.Provider, WorkspaceID: m.WorkspaceID, ConversationID: m.ConversationID, ConversationName: r.ConversationName,
		ConversationKind: r.Kind, ThreadID: m.ThreadID, TopLevel: topLevel(m), Permalink: r.Permalink, RequestedBy: r.UserName}
	f := delivery.Filer{ActorType: "person", ActorID: who.Person, Person: who.Person}
	return s.commit(ctx, org, p, inst, r, pick, func(tx pgx.Tx) error {
		var reply Reply
		switch d.Route {
		case RouteBrainstorm:
			id, title, err := s.startSession(ctx, tx, org, who, r, h, d, origin, members)
			if err != nil {
				return err
			}
			r.ResultKind, r.ResultID = "session", id
			reply = Reply{Text: sessionReply(title, r, who), LinkURL: s.link(who.Slug, "sessions/"+url.PathEscape(id)), LinkLabel: "Open in dude"}
			if d.Project == nil && len(r.candidates) > 0 {
				r.offer = &linkOffer{Text: reply.Text, LinkURL: reply.LinkURL, LinkLabel: reply.LinkLabel,
					Session: id, Candidates: r.candidates}
				reply.Row = r.offer.row(r.ID, nil)
			}
		case RouteResume:
			if err := delivery.LockSession(ctx, tx, d.Session); err != nil {
				return err
			}
			var title string
			if err := tx.QueryRow(ctx, `SELECT COALESCE(title, '') FROM sessions WHERE id = $1`, d.Session).Scan(&title); err != nil {
				return err
			}
			if err := sendToSession(ctx, tx, org, d.Session, who, m.Text, h.quote(), m, r.ConversationName); err != nil {
				return err
			}
			r.ResultKind, r.ResultID = "session", d.Session
			reply = Reply{Text: "Added to your session " + quoteTitle(title), LinkURL: s.link(who.Slug, "sessions/"+url.PathEscape(d.Session))}
		case RouteTask:
			epic := ""
			if d.Epic != nil {
				epic = d.Epic.ID
			}
			id, key, err := delivery.CreateTaskTx(ctx, tx, org, d.Project.ID, f, epic, clip(d.Title, 200), withThread(d.Goal, h), d.Criteria)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE tasks SET origin = $2 WHERE id = $1`, id, withQuoted(origin, h)); err != nil {
				return err
			}
			r.ResultKind, r.ResultID = "task", id
			reply = Reply{Text: fmt.Sprintf("Filed %s %s in Backlog", key, d.Title), LinkURL: s.link(who.Slug, "task/"+url.PathEscape(id))}
		case RouteEpic:
			id, n, err := createEpic(ctx, tx, org, d, f, origin)
			if err != nil {
				return err
			}
			r.ResultKind, r.ResultID = "epic", id
			reply = Reply{Text: fmt.Sprintf("Opened epic %s › %s with %d tasks in Backlog", d.Project.Key, d.NewEpic.Title, n),
				LinkURL: s.link(who.Slug, "epic/"+url.PathEscape(id))}
		default:
			return fmt.Errorf("nothing makes route %q", d.Route)
		}
		reply.LinkLabel = "Open in dude"
		if r.Route == RouteFailed {
			r.MadeRoute = d.Route
		}
		r.effects = []effect{replyEffect(reply), reactEffect(ReactionDone)}
		if r.ResultKind == "session" {
			if err := awaitSession(ctx, tx, r); err != nil {
				return err
			}
			r.effects[1] = sessionMark(pick)
		}
		// A DM is never bound: a thread forwarded into one routes afresh.
		if m.Direct {
			return nil
		}
		_, err := tx.Exec(ctx, `INSERT INTO chat_threads (organization_id, provider, external_workspace_id, conversation_id, thread_id,
				conversation_kind, conversation_name, session_id, task_id, epic_id, bound_by, last_read_message_id)
			VALUES (current_organization_id(), $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
			ON CONFLICT (provider, external_workspace_id, conversation_id, thread_id) DO NOTHING`,
			m.Provider, m.WorkspaceID, m.ConversationID, m.ThreadID, r.Kind, r.ConversationName,
			db.Nullable(pick2(r.ResultKind == "session", r.ResultID)), db.Nullable(pick2(r.ResultKind == "task", r.ResultID)),
			db.Nullable(pick2(r.ResultKind == "epic", r.ResultID)), who.Person, m.MessageID)
		return err
	})
}

func pick2(ok bool, v string) string {
	if ok {
		return v
	}
	return ""
}

// sessionReply is what dude says of a session it started: who can open it.
func sessionReply(title string, r *request, who asker) string {
	base := "Started a session " + quoteTitle(title)
	switch r.Kind {
	case KindPublic:
		return base + ", open to everyone in " + who.OrgName
	case KindPrivate, KindGroup:
		s := fmt.Sprintf("%s, shared with %d %s from %s", base, len(r.Added), plural(len(r.Added), "person", "people"), r.ConversationName)
		if n := len(r.Skipped); n > 0 {
			s += fmt.Sprintf("; %d %s not connected", n, plural(n, "is", "are"))
		}
		return s
	}
	return base
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// threadQuote is the thread as the text put before a message into a session.
func threadQuote(t threadContext) string {
	if len(t.Messages) == 0 {
		return ""
	}
	return "Earlier in the thread:\n" + t.render()
}

// quote is what a mention was said after, as the text put before it into a
// session: the thread, or the channel before a top-level mention.
func (h heard) quote() string {
	if c := h.channel(); c != "" {
		return "Earlier in the channel, before this message (not a thread):\n" + c
	}
	return threadQuote(h.Thread)
}

// withQuoted is an origin carrying the thread (or channel) as it was
// filed, for the task page's quote of it.
func withQuoted(o Origin, h heard) Origin {
	o.Thread, o.Channel = h.Thread.Messages, h.Channel.Messages
	return o
}

// withThread is a task's goal with the thread or channel it came from
// quoted under it.
func withThread(goal string, h heard) string {
	if c := h.channel(); c != "" {
		return goal + "\n\n**From the channel, before the request:**\n\n" + c
	}
	if len(h.Thread.Messages) == 0 {
		return goal
	}
	return goal + "\n\n**From the thread:**\n\n" + h.Thread.render()
}

// startSession makes the session a brainstorm route asks for: owned by the
// person, linked to the routed project, its access from where the mention
// was made (a public channel's open to the organisation; a private
// channel's or group's shared, as chat, with each connected member of the
// conversation; a DM's private), its first message the mention with the
// thread (or the channel before it) quoted.
func (s *Service) startSession(ctx context.Context, tx pgx.Tx, org string, who asker, r *request, h heard,
	d Decision, origin Origin, members []string) (string, string, error) {
	id := ids.New(ids.Session)
	title := clip(trimmed(d.Title), 120)
	if title == "" {
		title = clip(trimmed(r.Mention.Text), 80)
	}
	access := delivery.AccessPrivate
	if r.Kind == KindPublic {
		access = delivery.AccessOrganisation
	}
	if _, err := tx.Exec(ctx, `INSERT INTO sessions (id, organization_id, title, titled_by, created_by, access)
		VALUES ($1, $2, NULLIF($3, ''), CASE WHEN $3 <> '' THEN 'person' END, $4, $5)`, id, org, title, who.Person, access); err != nil {
		return "", "", err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO session_people (session_id, person_id, organization_id, role, invited_by, accepted_at)
		VALUES ($1, $2, $3, 'owner', $2, now())`, id, who.Person, org); err != nil {
		return "", "", err
	}
	if d.Project != nil {
		if _, err := tx.Exec(ctx, `INSERT INTO session_projects (session_id, project_id, organization_id) VALUES ($1, $2, $3)`,
			id, d.Project.ID, org); err != nil {
			return "", "", err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO session_repositories (session_id, repository_id, organization_id)
			SELECT $1, r.id, $3 FROM repositories r WHERE r.project_id = $2`, id, d.Project.ID, org); err != nil {
			return "", "", err
		}
	}
	ref := delivery.SessionRef(org, id)
	if err := delivery.SessionEvent(ctx, tx, ref, delivery.EvSessionCreated, "person", who.Person,
		map[string]any{"title": db.Nullable(title), "via": r.Mention.Provider}); err != nil {
		return "", "", err
	}
	if len(members) > 0 {
		var err error
		if r.Added, r.Skipped, err = addMembers(ctx, tx, org, id, members, r.linked, who.Person); err != nil {
			return "", "", err
		}
	}
	origin.Thread, origin.Channel = h.Thread.Messages, h.Channel.Messages
	skippedNames := []string{}
	for _, u := range r.Skipped {
		if n, ok := r.memberNames[u]; ok {
			skippedNames = append(skippedNames, n)
		} else {
			skippedNames = append(skippedNames, u)
		}
	}
	origin.Routing = &routingFacts{Route: r.Route, AskedRoute: r.AskedRoute, Confidence: r.Confidence, Reasoning: r.Reasoning,
		Added: db.NonNil(r.Added), Skipped: db.NonNil(r.Skipped), SkippedNames: skippedNames, RequestID: r.ID}
	if _, err := tx.Exec(ctx, `UPDATE sessions SET origin = $2 WHERE id = $1`, id, origin); err != nil {
		return "", "", err
	}
	full := r.Mention.Text
	if q := h.quote(); q != "" {
		full = q + "\n\n" + full
	}
	wr := writerFrom(who, r.Mention, r.ConversationName)
	if _, _, err := delivery.StartBrainstorm(ctx, tx, org, id, wr,
		delivery.Opening{Message: delivery.Attributed(who.Name, full), Shown: r.Mention.Text}); err != nil {
		return "", "", err
	}
	return id, title, nil
}

// createEpic files the epic and its tasks in the project, each owned by
// the person, in the Backlog and not started, with origin, and the
// blocked-by relations between the tasks. Returns the epic and its task count.
func createEpic(ctx context.Context, tx pgx.Tx, org string, d Decision, f delivery.Filer, origin Origin) (string, int, error) {
	epicID, err := delivery.CreateEpicTx(ctx, tx, org, d.Project.ID, f, clip(d.NewEpic.Title, 200), d.NewEpic.Description)
	if err != nil {
		return "", 0, err
	}
	if _, err := tx.Exec(ctx, `UPDATE epics SET origin = $2 WHERE id = $1`, epicID, origin); err != nil {
		return "", 0, err
	}
	made := map[string]delivery.RelatedTask{}
	for _, nt := range d.NewEpic.Tasks {
		id, key, err := delivery.CreateTaskTx(ctx, tx, org, d.Project.ID, f, epicID, clip(nt.Title, 200), nt.Goal, nt.Criteria)
		if err != nil {
			return "", 0, err
		}
		if _, err := tx.Exec(ctx, `UPDATE tasks SET origin = $2 WHERE id = $1`, id, origin); err != nil {
			return "", 0, err
		}
		made[nt.Ref] = delivery.RelatedTask{ID: id, Key: key, ProjectID: d.Project.ID}
	}
	for _, nt := range d.NewEpic.Tasks {
		for _, b := range nt.BlockedBy {
			if err := delivery.AddRelationTx(ctx, tx, org, delivery.Edge{Here: made[nt.Ref], Other: made[b], Side: delivery.SideBlockedBy,
				Person: f.Person}); err != nil {
				return "", 0, err
			}
		}
	}
	return epicID, len(d.NewEpic.Tasks), nil
}
