package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/ids"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// A brainstorm session: a conversation with an agent (role brainstorm)
// that belongs to an organisation and its members. It reads the projects
// linked to it and proposes work; a member files it, as themselves. Every
// read and write of a session checks the person is an accepted member
// (SessionRole); there is no admin override.

// Session roles (session_people.role).
const (
	SessionOwner = "owner"
	SessionChat  = "chat"
	SessionRead  = "read"
)

// Ledger events of a session, on the session (no Run, or its Run).
const (
	EvSessionCreated     = "session.created"
	EvSessionShared      = "session.shared"
	EvSessionJoined      = "session.joined"
	EvSessionDeclined    = "session.declined"
	EvSessionRoleChanged = "session.role_changed"
	EvSessionRemoved     = "session.member_removed"
	EvSessionOwner       = "session.owner_changed"
	EvSessionLinked      = "session.linked"
	EvSessionProposed    = "session.proposed"
	EvSessionFiled       = "session.filed"
	EvSessionBriefed     = "session.briefed"
	EvSessionTurnStopped = "session.turn_stopped"
)

// ErrNotMember is a session the person may not see: no such session, not
// a member, or an invitation not accepted. One error for all three, so a
// caller cannot tell a session exists.
var ErrNotMember = errors.New("not a member of this session")

// SessionRole is the person's role in the session, accepted; ErrNotMember
// for anyone else.
func SessionRole(ctx context.Context, tx pgx.Tx, sessionID, personID string) (string, error) {
	if personID == "" {
		return "", ErrNotMember
	}
	var role *string
	if err := tx.QueryRow(ctx, `SELECT session_role($1, $2)`, sessionID, personID).Scan(&role); err != nil {
		return "", err
	}
	if role == nil {
		return "", ErrNotMember
	}
	return *role, nil
}

// LockSession takes the session's Chat lock for the rest of tx, as
// LockChat does a task's: one message at a time, and the syncer ending its
// Run under the same lock. Its own key space ("session:"), so a session
// and a task never share one.
func LockSession(ctx context.Context, tx pgx.Tx, sessionID string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('session:' || $1))`, sessionID)
	return err
}

// SessionRef is the ledger's view of a session, for events with no Run.
func SessionRef(org, sessionID string) RunRef { return RunRef{Org: org, SessionID: sessionID} }

// SessionEvent appends an event to the session by an actor.
func SessionEvent(ctx context.Context, tx pgx.Tx, ref RunRef, typ, actorType, actorID string, payload map[string]any) error {
	ev := ref.Event(typ, actorType, payload)
	ev.ActorID = actorID
	_, err := ledger.Append(ctx, tx, ev)
	return err
}

// Attributed is a member's message as the agent reads it: their name, then
// their words, so it can tell people apart and answer each by name.
func Attributed(name, text string) string {
	if strings.TrimSpace(name) == "" {
		name = "Someone"
	}
	return name + ": " + text
}

// PersonName is a person's name, or "" for none.
func PersonName(ctx context.Context, tx pgx.Tx, personID string) (string, error) {
	if personID == "" {
		return "", nil
	}
	var name string
	err := tx.QueryRow(ctx, `SELECT name FROM people WHERE id = $1`, personID).Scan(&name)
	if db.IsNotFound(err) {
		return "", nil
	}
	return name, err
}

// SessionRepo is a repository a session's agent has checked out:
// repos/<project key>/<name>, named SpecName in the lux spec, never
// pushed.
type SessionRepo struct {
	ID, ProjectID, Key, Name, URL, DefaultBranch string
}

// SpecName is the repository's name in the lux spec: <key>-<name>, unique
// within the Run since names are unique only within a project, made one
// lux takes (lux.SpecName; SQL's lux_name, SessionSpecNameSQL). Keys are
// unique in an organisation (projects_key_idx, migration 095) and names
// per project, so <key>-<name> is distinct for every repository a session
// can link. Keys are upper case, so the spec name is always rewritten and
// carries the hash of <key>-<name>: two repositories share one only if
// their 32-bit hash suffixes and truncated prefixes collide. Nothing else
// guards against that.
func (r SessionRepo) SpecName() string { return lux.SpecName(r.Key + "-" + r.Name) }

// SessionSpecNameSQL is SpecName in SQL, over projects p and repositories
// repo: what runs.lux_repositories holds for a session's checkout.
const SessionSpecNameSQL = `lux_name(p.key_prefix || '-' || repo.name)`

// SessionRepoPath is where it is checked out.
func SessionRepoPath(key, name string) string { return "/workspace/repos/" + key + "/" + name }

// SessionRepositories are the repositories linked to a session, in project
// key and name order.
func SessionRepositories(ctx context.Context, tx pgx.Tx, sessionID string) ([]SessionRepo, error) {
	rows, err := tx.Query(ctx, `SELECT r.id, r.project_id, p.key_prefix, r.name, r.url, r.default_branch
		FROM session_repositories sr JOIN repositories r ON r.id = sr.repository_id JOIN projects p ON p.id = r.project_id
		JOIN session_projects sp ON sp.session_id = sr.session_id AND sp.project_id = r.project_id
		WHERE sr.session_id = $1 ORDER BY p.key_prefix, r.name`, sessionID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[SessionRepo])
}

// LinkedProject is a project linked to a session.
type LinkedProject struct{ ID, Key, Name string }

// SessionProjects are the projects linked to a session, by key.
func SessionProjects(ctx context.Context, tx pgx.Tx, sessionID string) ([]LinkedProject, error) {
	rows, err := tx.Query(ctx, `SELECT p.id, p.key_prefix, p.name FROM session_projects sp JOIN projects p ON p.id = sp.project_id
		WHERE sp.session_id = $1 ORDER BY p.key_prefix, p.name`, sessionID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[LinkedProject])
}

// HeldBack (SQL, over directives d): a message held while the agent waits
// for one member's answer (ask_person with to): it goes with the answer,
// not as it, so it is not sent while that question is open.
const HeldBack = `(d.held_for IS NOT NULL AND EXISTS (SELECT 1 FROM questions hq WHERE hq.id = d.held_for AND hq.status = 'open'))`

// LiveBrainstorm (SQL, over runs r): the session's agent that can still
// hear a message — at most one (runs_live_brainstorm_idx).
const LiveBrainstorm = `r.role = 'brainstorm' AND r.kind = 'agent' AND r.status IN ('pending', 'scheduled', 'starting', 'running', 'paused')`

// StartBrainstorm creates the session's agent, briefed by dude with the
// member's message (already attributed).
func StartBrainstorm(ctx context.Context, tx pgx.Tx, org, sessionID string, w Writer, message, shown string) (string, error) {
	id := ids.New(ids.Run)
	briefing, err := sessionBriefing(ctx, tx, sessionID, message)
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO runs (id, organization_id, session_id, attempt, status, kind, role, base_refs, prompt, started_by)
		VALUES ($1, $2, $3, 1, 'pending', 'agent', 'brainstorm', '{}'::jsonb, $4, NULLIF($5, ''))`,
		id, org, sessionID, briefing, w.Person); err != nil {
		return "", err
	}
	ref := RunRef{Org: org, SessionID: sessionID, RunID: id}
	if _, err := ledger.Append(ctx, tx, ref.Event(EvRunCreated, ledger.ActorSystem,
		map[string]any{"role": RoleBrainstorm, "publishes": false})); err != nil {
		return "", err
	}
	if shown != "" {
		if err := ChatEvent(ctx, tx, ref, w, map[string]any{"text": shown}); err != nil {
			return "", err
		}
	}
	return id, SessionEvent(ctx, tx, ref, EvSessionBriefed, ledger.ActorSystem, "dude", map[string]any{"text": briefing})
}

// sessionBriefing is the brainstorm's first prompt before its
// instructions: the session, who is in it, what it reads, and the message.
func sessionBriefing(ctx context.Context, tx pgx.Tx, sessionID, message string) (string, error) {
	var title string
	if err := tx.QueryRow(ctx, `SELECT title FROM sessions WHERE id = $1`, sessionID).Scan(&title); err != nil {
		return "", fmt.Errorf("briefing: the session: %w", err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Brainstorm, this is the session %q. You think with its members about their projects: read, "+
		"ask, propose. Below is what dude knows; read more with your tools.", oneLine(title))
	people, err := SessionMembers(ctx, tx, sessionID)
	if err != nil {
		return "", err
	}
	b.WriteString("\n\n## People\n")
	for _, p := range people {
		if !p.Accepted {
			continue
		}
		fmt.Fprintf(&b, "\n- %s (%s)", p.Name, sessionRoleWords[p.Role])
	}
	projects, err := SessionProjects(ctx, tx, sessionID)
	if err != nil {
		return "", err
	}
	repos, err := SessionRepositories(ctx, tx, sessionID)
	if err != nil {
		return "", err
	}
	b.WriteString("\n\n## Linked projects\n")
	if len(projects) == 0 {
		b.WriteString("\nNone yet: you can talk and search memory; a member links projects when the idea is concrete.")
	}
	for _, p := range projects {
		fmt.Fprintf(&b, "\n- %s (%s)", p.Key, p.Name)
		for _, r := range repos {
			if r.ProjectID == p.ID {
				fmt.Fprintf(&b, "\n  - `%s` at `%s`, %s", r.Name, SessionRepoPath(r.Key, r.Name), r.DefaultBranch)
			}
		}
	}
	fmt.Fprintf(&b, "\n\n## The first message\n\n%s", message)
	return b.String(), nil
}

var sessionRoleWords = map[string]string{SessionOwner: "owner", SessionChat: "can chat", SessionRead: "can read"}

// Member is a person in a session, or invited to it.
type Member struct {
	PersonID, Name, Role   string
	Accepted, BecomesOwner bool
}

// SessionMembers are the session's people, owner first, then by when
// they were invited.
func SessionMembers(ctx context.Context, tx pgx.Tx, sessionID string) ([]Member, error) {
	rows, err := tx.Query(ctx, `SELECT sp.person_id, p.name, sp.role, sp.accepted_at IS NOT NULL, sp.becomes_owner
		FROM session_people sp JOIN people p ON p.id = sp.person_id WHERE sp.session_id = $1
		ORDER BY sp.role <> 'owner', sp.invited_at, p.name`, sessionID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Member])
}

// BrainstormPrompt is the brainstorm's first prompt: the briefing, then
// how it works, its repositories and its tools.
func BrainstormPrompt(briefing string, in PromptInput) string {
	sections := []string{briefing, "## How you work"}
	lead, _ := in.instructions(RoleBrainstorm)
	sections = append(sections, lead...)
	if len(in.Repositories) == 0 {
		sections = append(sections, "No repository is checked out for this session.")
	} else {
		var b strings.Builder
		b.WriteString("Your checkouts, read only (never pushed):")
		for _, r := range in.Repositories {
			fmt.Fprintf(&b, "\n- `%s` at `%s`", r.Name, r.Path)
		}
		sections = append(sections, b.String())
	}
	if in.Tools {
		sections = append(sections, brainstormToolsNote)
	}
	if c := strings.TrimSpace(in.Context); c != "" {
		sections = append(sections, "## Notes\n\n"+c)
	}
	return strings.Join(sections, "\n\n")
}

const brainstormToolsNote = "The dude tools read the linked projects: list_tasks, list_epics, pull_requests, findings, " +
	"list_repositories, search_memory and get_memory, each with a project key (optional when one is linked). " +
	"propose fills the session's proposal card — new epics and tasks, edits to tasks, comments — for a member to " +
	"file with a click, as themselves: you never create, edit or comment yourself. remember saves what is worth " +
	"knowing next time. ask_person asks a member something and ends your turn; with to, only that member answers, " +
	"and what others say meanwhile reaches you with the answer."

// SessionHandOver settles what members sent an ended brainstorm and it
// never read: each goes, in order and with its writer, to the session's
// live brainstorm, or to a new one briefed with the first. Images are not
// carried by a briefing; such a message fails saying to send it again.
// The caller holds the session's lock.
func SessionHandOver(ctx context.Context, tx pgx.Tx, ref RunRef) (string, error) {
	rows, err := tx.Query(ctx, `SELECT d.id, d.text, d.interrupt,
			COALESCE(e.actor_type, ''), COALESCE(e.actor_id, ''), COALESCE(e.payload->>'text', '')
		FROM directives d
		LEFT JOIN LATERAL (SELECT e.actor_type, e.actor_id, e.payload FROM events e WHERE e.run_id = d.run_id
			AND e.event_type IN ('chat.message', 'question.answered') AND e.payload->>'directiveId' = d.id
			ORDER BY e.cursor LIMIT 1) e ON true
		WHERE d.run_id = $1 AND d.delivered_at IS NULL AND d.failed_at IS NULL
		ORDER BY d.created_at, d.id FOR UPDATE OF d`, ref.RunID)
	if err != nil {
		return "", err
	}
	type unread struct {
		ID, Text           string
		Interrupt          bool
		ActorType, ActorID string
		Shown              string
	}
	left, err := pgx.CollectRows(rows, pgx.RowToStructByPos[unread])
	if err != nil || len(left) == 0 {
		return "", err
	}
	next := ""
	failed := map[string]string{}
	for _, d := range left {
		if d.Interrupt {
			continue
		}
		w := Writer{ActorType: d.ActorType, ActorID: d.ActorID}
		if w.ActorType == "" {
			w.ActorType = ledger.ActorSystem
		}
		if next == "" {
			err := tx.QueryRow(ctx, `SELECT r.id FROM runs r WHERE r.session_id = $1 AND `+LiveBrainstorm+` FOR NO KEY UPDATE`,
				ref.SessionID).Scan(&next)
			if err != nil && !db.IsNotFound(err) {
				return "", err
			}
		}
		if next == "" {
			images, err := DirectiveAttachments(ctx, tx, d.ID)
			if err != nil {
				return "", err
			}
			if len(images) > 0 {
				failed[d.ID] = "the session's agent stopped before reading it; its images were not passed on; send it again"
				continue
			}
			if next, err = StartBrainstorm(ctx, tx, ref.Org, ref.SessionID, w, d.Text, ""); err != nil {
				return "", err
			}
			continue
		}
		to := RunRef{Org: ref.Org, SessionID: ref.SessionID, RunID: next}
		id, _, err := QueueDirective(ctx, tx, to, Directive{Text: d.Text, Scope: "run", Supersedes: d.ID})
		if err != nil {
			return "", err
		}
		if err := RequestResumeForMessage(ctx, tx, next, "a message handed on from the session's agent before"); err != nil {
			return "", err
		}
		if err := ChatEvent(ctx, tx, to, w, map[string]any{"text": d.Shown, "directiveId": id}); err != nil {
			return "", err
		}
	}
	for _, d := range left {
		reason := failed[d.ID]
		if reason == "" {
			reason = "the session's agent stopped before reading it"
			if next != "" {
				reason += "; the next one, " + next + ", has it"
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE directives SET failed_at = now(), error = $2 WHERE id = $1`, d.ID, reason); err != nil {
			return "", err
		}
		if _, err := ledger.Append(ctx, tx, ref.Event(evDirectiveFailed, ledger.ActorSystem,
			map[string]any{"directiveId": d.ID, "error": reason, "nextRunId": db.Nullable(next)})); err != nil {
			return "", err
		}
	}
	return next, nil
}

// EndBrainstorm completes a session's agent whose container stopped
// without dude asking (Ending), and hands what it never read to the next.
// The caller holds the session's lock.
func EndBrainstorm(ctx context.Context, tx pgx.Tx, ref RunRef, why string) error {
	tag, err := tx.Exec(ctx, `UPDATE runs r SET status = 'completed', ended_at = now(), lux_stop_reason = 'complete'
		WHERE r.id = $1 AND `+Ending, ref.RunID)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	if _, err := ledger.Append(ctx, tx, ref.Event("run.completed", ledger.ActorSystem,
		map[string]any{"status": "completed", "reason": why})); err != nil {
		return err
	}
	_, err = SessionHandOver(ctx, tx, ref)
	return err
}

// UnheardBrainstorm (SQL, a FROM source yielding r): an ended brainstorm
// holding messages it never read, for the syncer to hand over.
const UnheardBrainstorm = `(SELECT d.run_id FROM directives d WHERE d.delivered_at IS NULL AND d.failed_at IS NULL) c
	CROSS JOIN LATERAL (SELECT u.id, u.organization_id, u.session_id, u.status, u.ended_at FROM runs u
	  WHERE u.id = c.run_id AND u.role = 'brainstorm' AND u.status IN ('completed', 'failed') OFFSET 0) r`

// WithdrawQuestionsTo closes, unanswered, the session's open questions put
// to person (removed, or no longer able to chat): nobody else may answer
// them, so they would hold the agent and every message behind them for
// ever. The messages held for them are released, and dude tells the agent
// the question is withdrawn, which resumes it if parked. No one's message
// is taken as the answer. Under the session's lock.
func WithdrawQuestionsTo(ctx context.Context, tx pgx.Tx, org, sessionID, person string) error {
	rows, err := tx.Query(ctx, `UPDATE questions q SET status = 'cancelled'
		FROM runs r WHERE r.id = q.run_id AND r.session_id = $1 AND q.to_person = $2 AND q.status = 'open'
		RETURNING q.id, q.run_id, q.prompt`, sessionID, person)
	if err != nil {
		return err
	}
	closed, err := pgx.CollectRows(rows, pgx.RowToStructByPos[struct{ ID, RunID, Prompt string }])
	if err != nil || len(closed) == 0 {
		return err
	}
	name, err := PersonName(ctx, tx, person)
	if err != nil {
		return err
	}
	for _, q := range closed {
		if _, err := tx.Exec(ctx, `UPDATE directives SET held_for = NULL WHERE held_for = $1`, q.ID); err != nil {
			return err
		}
		ref := RunRef{Org: org, SessionID: sessionID, RunID: q.RunID}
		if _, err := ledger.Append(ctx, tx, ref.Event(EvQuestionClosed, ledger.ActorSystem,
			map[string]any{"questionId": q.ID, "by": "withdrawn"})); err != nil {
			return err
		}
		if err := TellBrainstorm(ctx, tx, org, sessionID, fmt.Sprintf("Your question to %s is withdrawn: they can no longer answer "+
			"in this session. Nobody answered %q; ask someone else if you still need it decided.", name, oneLine(q.Prompt))); err != nil {
			return err
		}
	}
	return nil
}

// TellBrainstorm queues a line from dude for the session's live agent,
// resuming it if it is parked; nothing when none is live.
func TellBrainstorm(ctx context.Context, tx pgx.Tx, org, sessionID, text string) error {
	var runID string
	err := tx.QueryRow(ctx, `SELECT r.id FROM runs r WHERE r.session_id = $1 AND `+LiveBrainstorm+` FOR NO KEY UPDATE`,
		sessionID).Scan(&runID)
	if db.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	ref := RunRef{Org: org, SessionID: sessionID, RunID: runID}
	id, _, err := QueueDirective(ctx, tx, ref, Directive{Text: text, Scope: "run"})
	if err != nil {
		return err
	}
	if err := RequestResumeForMessage(ctx, tx, runID, "dude told it something"); err != nil {
		return err
	}
	return SessionEvent(ctx, tx, ref, "session.told", ledger.ActorSystem, "dude", map[string]any{"text": text, "directiveId": id})
}

// Proposal items (session_proposals.items), each one thing the card asks
// a member to file.
type ProposalItem struct {
	// epic, task, edit or comment.
	Kind string `json:"kind"`
	// The project, by key (epic, task).
	Project string `json:"project,omitempty"`
	// A task's epic: an existing one's title, or a new epic's of this
	// proposal (by its title).
	Epic               string   `json:"epic,omitempty"`
	Title              string   `json:"title,omitempty"`
	Goal               string   `json:"goal,omitempty"`
	AcceptanceCriteria []string `json:"acceptanceCriteria,omitempty"`
	Description        string   `json:"description,omitempty"`
	// edit, comment: the task, by key.
	Task string `json:"task,omitempty"`
	// edit: the task's text before, as the agent read it, for the card.
	Before *TaskText `json:"before,omitempty"`
	After  *TaskText `json:"after,omitempty"`
	// comment.
	Text string `json:"text,omitempty"`
}

// TaskText is a task's goal and criteria, either or both.
type TaskText struct {
	Goal               *string   `json:"goal,omitempty"`
	AcceptanceCriteria *[]string `json:"acceptanceCriteria,omitempty"`
}

// RecordProposal stores the agent's proposal as the session's card.
func RecordProposal(ctx context.Context, tx pgx.Tx, ref RunRef, items []ProposalItem) (string, error) {
	id := ids.New(ids.Proposal)
	raw, _ := json.Marshal(items)
	if _, err := tx.Exec(ctx, `INSERT INTO session_proposals (id, organization_id, session_id, run_id, items) VALUES ($1, $2, $3, $4, $5::jsonb)`,
		id, ref.Org, ref.SessionID, db.Nullable(ref.RunID), raw); err != nil {
		return "", err
	}
	_, err := ledger.Append(ctx, tx, ref.Event(EvSessionProposed, ledger.ActorAgent, map[string]any{"proposalId": id, "items": items}))
	return id, err
}

// taskKey matches a task's key as people write it (BL-58): a project key
// as PROJECT_KEY (@dude/domain) allows, digits included, then its number.
var taskKey = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9]*)-([0-9]+)$`)

// TaskKey is a task's key as TaskByKey reads it, written one way
// ("BL-58" for "bl-058"), and whether it is one.
func TaskKey(key string) (string, bool) {
	m := taskKey.FindStringSubmatch(strings.TrimSpace(key))
	if m == nil {
		return "", false
	}
	n, err := strconv.Atoi(m[2])
	if err != nil {
		return "", false
	}
	return strings.ToUpper(m[1]) + "-" + strconv.Itoa(n), true
}

// TaskByKey finds a task by its key among the projects given; "" for none.
func TaskByKey(ctx context.Context, tx pgx.Tx, projectIDs []string, key string) (id, projectID string, err error) {
	m := taskKey.FindStringSubmatch(strings.TrimSpace(key))
	if m == nil {
		return "", "", nil
	}
	err = tx.QueryRow(ctx, `SELECT t.id, t.project_id FROM tasks t JOIN projects p ON p.id = t.project_id
		WHERE t.project_id = ANY($1) AND upper(p.key_prefix) = upper($2) AND t.number = $3::int`, projectIDs, m[1], m[2]).
		Scan(&id, &projectID)
	if db.IsNotFound(err) {
		return "", "", nil
	}
	return id, projectID, err
}
