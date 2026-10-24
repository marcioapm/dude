package chat

import (
	"context"
	"errors"
	"fmt"
	"net/url"

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
	Provider         string          `json:"provider"`
	WorkspaceID      string          `json:"workspaceId"`
	ConversationID   string          `json:"conversationId"`
	ConversationName string          `json:"conversationName"`
	ConversationKind string          `json:"conversationKind"`
	ThreadID         string          `json:"threadId"`
	Permalink        string          `json:"permalink"`
	RequestedBy      string          `json:"requestedBy"`
	Thread           []quotedMessage `json:"thread,omitempty"`
	Routing          *routingFacts   `json:"routing,omitempty"`
}

type routingFacts struct {
	Route      string   `json:"route"`
	AskedRoute string   `json:"askedRoute,omitempty"`
	Confidence *float64 `json:"confidence"`
	Reasoning  string   `json:"reasoning"`
	Added      []string `json:"added"`
	Skipped    []string `json:"skipped"`
	RequestID  string   `json:"requestId"`
}

// make carries a decision out: a session (with access from where the
// mention was made), a resumed session, a task, or an epic with its tasks;
// binds the thread to it, records the request and replies with its link.
func (s *Service) make(ctx context.Context, org string, p Provider, inst Installation, who asker, r *request, t threadContext, d Decision) error {
	m := r.Mention
	if r.Route == "" {
		r.Route = d.Route
	}
	var members []string
	if d.Route == RouteBrainstorm && (r.Kind == KindPrivate || r.Kind == KindGroup) {
		var err error
		if members, err = p.ListMembers(ctx, inst, m.ConversationID); err != nil {
			s.noteFailure(ctx, org, m.Provider, err)
			return err
		}
	}
	r.ID = ids.New(ids.ChatRequest)
	origin := Origin{Provider: m.Provider, WorkspaceID: m.WorkspaceID, ConversationID: m.ConversationID, ConversationName: r.ConversationName,
		ConversationKind: r.Kind, ThreadID: m.ThreadID, Permalink: r.Permalink, RequestedBy: r.UserName}
	f := delivery.Filer{ActorType: "person", ActorID: who.Person, Person: who.Person}
	var reply Reply
	err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		switch d.Route {
		case RouteBrainstorm:
			id, title, err := s.startSession(ctx, tx, org, inst, who, r, t, d, origin, members)
			if err != nil {
				return err
			}
			r.ResultKind, r.ResultID = "session", id
			reply = Reply{Text: sessionReply(title, r, who), LinkURL: s.link(who.Slug, "sessions/"+url.PathEscape(id))}
		case RouteResume:
			if err := delivery.LockSession(ctx, tx, d.Session); err != nil {
				return err
			}
			var title string
			if err := tx.QueryRow(ctx, `SELECT COALESCE(title, '') FROM sessions WHERE id = $1`, d.Session).Scan(&title); err != nil {
				return err
			}
			if err := sendToSession(ctx, tx, org, d.Session, who, m.Text, threadQuote(t), m.Provider); err != nil {
				return err
			}
			r.ResultKind, r.ResultID = "session", d.Session
			reply = Reply{Text: "Added to your session " + quoteTitle(title), LinkURL: s.link(who.Slug, "sessions/"+url.PathEscape(d.Session))}
		case RouteTask:
			epic := ""
			if d.Epic != nil {
				epic = d.Epic.ID
			}
			id, key, err := delivery.CreateTaskTx(ctx, tx, org, d.Project.ID, f, epic, clip(d.Title, 200), withThread(d.Goal, t), d.Criteria)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE tasks SET origin = $2 WHERE id = $1`, id, origin); err != nil {
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
		first, err := record(ctx, tx, r)
		if err != nil {
			return err
		}
		if !first {
			return errHandled
		}
		// A DM is never bound: a thread forwarded into one routes afresh.
		if m.Direct {
			return nil
		}
		_, err = tx.Exec(ctx, `INSERT INTO chat_threads (organization_id, provider, external_workspace_id, conversation_id, thread_id,
				conversation_kind, conversation_name, session_id, task_id, epic_id, bound_by, last_read_message_id)
			VALUES (current_organization_id(), $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
			ON CONFLICT (provider, external_workspace_id, conversation_id, thread_id) DO NOTHING`,
			m.Provider, m.WorkspaceID, m.ConversationID, m.ThreadID, r.Kind, r.ConversationName,
			db.Nullable(pick(r.ResultKind == "session", r.ResultID)), db.Nullable(pick(r.ResultKind == "task", r.ResultID)),
			db.Nullable(pick(r.ResultKind == "epic", r.ResultID)), who.Person, m.MessageID)
		return err
	})
	if errors.Is(err, errHandled) {
		return nil
	}
	if err != nil {
		return err
	}
	s.kick()
	s.reply(ctx, org, p, inst, m, reply)
	return nil
}

func pick(ok bool, v string) string {
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

// withThread is a task's goal with the thread it came from quoted under it.
func withThread(goal string, t threadContext) string {
	if len(t.Messages) == 0 {
		return goal
	}
	return goal + "\n\n**From the thread:**\n\n" + t.render()
}

// startSession makes the session a brainstorm route asks for: owned by the
// person, linked to the routed project, its access from where the mention
// was made (a public channel's open to the organisation; a private
// channel's or group's shared, as chat, with each connected member of the
// conversation; a DM's private), its first message the mention with the
// thread quoted.
func (s *Service) startSession(ctx context.Context, tx pgx.Tx, org string, inst Installation, who asker, r *request, t threadContext,
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
		if r.Added, r.Skipped, err = addMembers(ctx, tx, inst, id, members, who.Person); err != nil {
			return "", "", err
		}
	}
	origin.Thread = t.Messages
	origin.Routing = &routingFacts{Route: r.Route, AskedRoute: r.AskedRoute, Confidence: r.Confidence, Reasoning: r.Reasoning,
		Added: nonNil(r.Added), Skipped: nonNil(r.Skipped), RequestID: r.ID}
	if _, err := tx.Exec(ctx, `UPDATE sessions SET origin = $2 WHERE id = $1`, id, origin); err != nil {
		return "", "", err
	}
	full := r.Mention.Text
	if q := threadQuote(t); q != "" {
		full = q + "\n\n" + full
	}
	wr := delivery.Writer{ActorType: "person", ActorID: who.Person, Person: who.Person, Via: map[string]any{"via": r.Mention.Provider}}
	if _, _, err := delivery.StartBrainstorm(ctx, tx, org, id, wr,
		delivery.Opening{Message: delivery.Attributed(who.Name, full), Shown: r.Mention.Text}); err != nil {
		return "", "", err
	}
	return id, title, nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
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
