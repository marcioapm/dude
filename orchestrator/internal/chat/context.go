package chat

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/judge"
)

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
	// Posted by an app (a bot), never a person of the organisation.
	App bool `json:"app,omitempty"`
}

func (t threadContext) hash() string {
	sum := sha256.New()
	for _, m := range t.Messages {
		fmt.Fprintf(sum, "%s\x00%s\x00", m.By, m.Text)
	}
	return hex.EncodeToString(sum.Sum(nil))
}

// render is the thread as text: each message by its author, people not
// in dude and apps marked so.
func (t threadContext) render() string {
	var b strings.Builder
	for _, m := range t.Messages {
		by := m.By
		switch {
		case m.App:
			by += " (an app)"
		case !m.InDude:
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
	// dude's own messages are its replies, not context; every other bot's
	// (a CI report, an alert) is kept, by its name, never as a person.
	msgs = slices.DeleteFunc(slices.Clone(msgs), func(m ThreadMessage) bool {
		return m.ID == skip || (m.UserID != "" && m.UserID == inst.BotUserID)
	})
	var users, bots []string
	for _, msg := range msgs {
		switch {
		case msg.Bot && msg.BotName == "" && msg.UserID != "" && !slices.Contains(bots, msg.UserID):
			bots = append(bots, msg.UserID)
		case !msg.Bot && msg.UserID != "" && !slices.Contains(users, msg.UserID):
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
	var strangers []string
	for _, u := range users {
		if _, ok := people[u]; !ok {
			strangers = append(strangers, u)
		}
	}
	names := s.names(ctx, p, inst, append(strangers, bots...))
	var out []quotedMessage
	for _, msg := range msgs {
		q := quotedMessage{Text: clip(msg.Text, 2000)}
		switch person, ok := people[msg.UserID]; {
		case msg.Bot:
			q.App, q.By = true, firstNonEmpty(msg.BotName, names[msg.UserID], "an app")
		case ok:
			q.Person, q.By, q.InDude = person[0], person[1], true
		default:
			q.By = names[msg.UserID]
		}
		out = append(out, q)
	}
	if len(out) > threadMessagesMax {
		out = out[len(out)-threadMessagesMax:]
	}
	return out, nil
}

// The routing call's context is bounded: at most so many projects, epics
// per project and sessions, and at most contextBudget characters of
// projects and sessions together. What is left out is said, so the model
// knows the list is not everything.
const (
	projectsMax   = 100
	epicsMax      = 20
	sessionsMax   = 20
	contextBudget = 24000
)

// world is what the person may route to: the organisation's projects with
// their epics not done (the most recent epicsMax of each), and the
// chat-born sessions they can chat in from the last 14 days, each with
// whether every connected member of the mention's conversation (connected,
// by person id) can chat in it too.
func (s *Service) world(ctx context.Context, org, person string, m Mention, kind string, connected []string) (World, error) {
	w := World{Conversation: m.ConversationID, Kind: kind}
	err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM projects WHERE key_prefix IS NOT NULL`).Scan(&w.ProjectsTotal); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT p.id, p.key_prefix, p.name,
				COALESCE((SELECT json_agg(json_build_object('ID', e.id, 'Title', e.title) ORDER BY e.position)
				  FROM (SELECT e.id, e.title, e.position FROM epics e WHERE e.project_id = p.id AND COALESCE(e.state, 'active') <> 'done'
				        ORDER BY e.created_at DESC LIMIT $1) e), '[]'),
				(SELECT count(*) FROM epics e WHERE e.project_id = p.id AND COALESCE(e.state, 'active') <> 'done')
			FROM projects p WHERE p.key_prefix IS NOT NULL ORDER BY p.name LIMIT $2`, epicsMax, projectsMax)
		if err != nil {
			return err
		}
		w.Projects, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (Project, error) {
			var p Project
			err := row.Scan(&p.ID, &p.Key, &p.Name, &p.Epics, &p.EpicsTotal)
			return p, err
		})
		if err != nil {
			return err
		}
		// Read newest first along sessions_chat_recent_idx. The CASE makes
		// the inlined test, a superset of session_role's owner/chat, run
		// first: the planner would otherwise call session_role on every
		// recent chat-born session before it.
		rows, err = tx.Query(ctx, `SELECT s.id, COALESCE(s.title, ''), s.access, COALESCE(s.origin->>'conversationId', ''),
				COALESCE(s.origin->>'conversationKind', ''),
				NOT EXISTS (SELECT 1 FROM unnest($3::text[]) AS c(person) WHERE COALESCE(session_role(s.id, c.person), '') NOT IN ('owner', 'chat'))
			FROM sessions s
			WHERE s.organization_id = current_organization_id() AND s.origin IS NOT NULL
			  AND s.created_at > now() - make_interval(days => $2)
			  AND CASE WHEN s.access = 'organisation' OR s.owned_by_admins OR EXISTS (SELECT 1 FROM session_people sp
			        WHERE sp.session_id = s.id AND sp.person_id = $1 AND sp.accepted_at IS NOT NULL AND sp.role IN ('owner', 'chat'))
			    THEN session_role(s.id, $1) IN ('owner', 'chat') ELSE false END
			ORDER BY s.created_at DESC LIMIT $4`,
			person, resumeLookback, db.NonNil(connected), sessionsMax)
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

// RoutingInput is the routing call's user message: the mention, the thread
// before it, the projects and the sessions it may resume, within
// contextBudget, each list ending with how many were left out.
func RoutingInput(who, where, provider, text string, thread string, w World) string {
	var in strings.Builder
	fmt.Fprintf(&in, "## The message\n\n%s wrote in %s on %s:\n\n%s\n", who, where, provider, text)
	if thread != "" {
		fmt.Fprintf(&in, "\n## The thread before it\n\n%s\n", thread)
	}
	budget := contextBudget
	in.WriteString("\n## Projects they can file in\n\n")
	if len(w.Projects) == 0 {
		in.WriteString("None.\n")
	}
	shown := 0
	for _, pr := range w.Projects {
		var line strings.Builder
		fmt.Fprintf(&line, "- %s (%s)", pr.Key, pr.Name)
		if len(pr.Epics) > 0 {
			var titles []string
			for _, e := range pr.Epics {
				titles = append(titles, strconv.Quote(e.Title))
			}
			fmt.Fprintf(&line, ", epics: %s", strings.Join(titles, ", "))
			if more := pr.EpicsTotal - len(pr.Epics); more > 0 {
				fmt.Fprintf(&line, " (and %d more not shown)", more)
			}
		}
		line.WriteString("\n")
		if line.Len() > budget {
			break
		}
		budget -= line.Len()
		in.WriteString(line.String())
		shown++
	}
	if more := max(w.ProjectsTotal, len(w.Projects)) - shown; more > 0 {
		fmt.Fprintf(&in, "- %d more not shown\n", more)
	}
	if resumable := w.Resumable(); len(resumable) > 0 {
		in.WriteString("\n## Their chat sessions from the last 14 days\n\n")
		shown := 0
		for _, se := range resumable {
			line := fmt.Sprintf("- %s: %s\n", se.ID, quoteTitle(clip(se.Title, 200)))
			if len(line) > budget {
				break
			}
			budget -= len(line)
			in.WriteString(line)
			shown++
		}
		if more := len(resumable) - shown; more > 0 {
			fmt.Fprintf(&in, "- %d more not shown\n", more)
		}
	}
	return in.String()
}

// ask is the one routing judgement, on the organisation's Judgements tier,
// bounded by RoutingTimeout.
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
	var thread string
	if len(t.Messages) > 0 {
		thread = t.render()
	}
	j := *s.Judge
	if j.Timeout == 0 || j.Timeout > RoutingTimeout {
		j.Timeout = RoutingTimeout
	}
	return j.Structured(ctx, org, judge.StructuredAsk{Asker: "chat.route", About: p.Info().Name + " · " + conv.Name,
		Instructions: Instructions(who.OrgName, orgText, p.Info().Name, providerText),
		Input:        RoutingInput(who.Name, conv.Name, p.Info().Name, m.Text, thread, w), MaxTokens: 4096,
		Parse: func(obj []byte) (any, judge.Judgement, error) {
			a, err := ParseAnswer(obj)
			if err != nil {
				return nil, judge.Judgement{}, err
			}
			return a, judge.Judgement{Decision: a.Route, Confidence: a.Confidence, Reasoning: a.Reasoning}, nil
		}})
}
