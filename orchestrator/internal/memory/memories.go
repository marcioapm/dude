package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/ids"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// Event types: the contract with the backend and the browser
// (packages/domain/src/events/types.ts).
const (
	EvCreated  = "memory.created"
	EvUpdated  = "memory.updated"
	EvArchived = "memory.archived"
	EvRestored = "memory.restored"
)

var kinds = map[string]bool{"fact": true, "procedure": true, "note": true}

// Ref is what a memory is about, or where it was learned.
type Ref struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	// Filled when read: a task's key and status, an epic's or project's name.
	Label  string `json:"label,omitempty"`
	Status string `json:"status,omitempty"`
}

// Author is who wrote a memory, as a person reads it.
type Author struct {
	Kind string `json:"kind"` // person | system | agent
	// The person: who wrote it, or the one the agent worked for.
	PersonID   string `json:"personId,omitempty"`
	PersonName string `json:"personName,omitempty"`
	Role       string `json:"role,omitempty"`
	RunID      string `json:"runId,omitempty"`
	TaskKey    string `json:"taskKey,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

type Memory struct {
	ID        string `json:"id"`
	ProjectID string `json:"projectId,omitempty"`
	// A brainstorm session's memory: only its members and its agent read it.
	SessionID  string     `json:"sessionId,omitempty"`
	Title      string     `json:"title"`
	Content    string     `json:"content"`
	Kind       string     `json:"kind"`
	Author     Author     `json:"author"`
	Source     *Ref       `json:"source,omitempty"`
	About      []Ref      `json:"about"`
	ArchivedAt *time.Time `json:"archivedAt,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	UpdatedAt  time.Time  `json:"updatedAt"`
	// The index's view: embedded, waiting, or failed with its reason.
	Index     string `json:"index"`
	IndexNote string `json:"indexNote,omitempty"`
}

// Invalid is a refusal a person or an agent can act on.
type Invalid struct{ Reason string }

func (e *Invalid) Error() string { return e.Reason }

func invalid(format string, a ...any) error { return &Invalid{Reason: fmt.Sprintf(format, a...)} }

// ErrNotFound: no such memory in this organization, or none the viewer may read.
var ErrNotFound = errors.New("memory: not found")

// Viewer is who reads: a person (Person), or an agent — a brainstorm
// session's (Session) or any other (neither). A session's memories are
// read by its accepted members and its own agent, by nobody else.
type Viewer struct {
	Person  string
	Session string
}

// visible (SQL) keeps what the viewer may read, over a row with session_id;
// $n and $n+1 are the viewer's session and person.
func visible(alias string, n int) string {
	return fmt.Sprintf(`memory_visible(%s.session_id, $%d, $%d)`, alias, n, n+1)
}

// New is what is written: the fields, who, and where it was learned.
type New struct {
	ProjectID string
	// A brainstorm session's memory: ProjectID is then empty.
	SessionID string
	Title     string
	Content   string
	Kind      string
	About     []Ref
	Author    Author
	Source    *Ref
}

// Actor is who is acting, for the ledger: a person's key, dude, or a Run.
type Actor struct {
	Type string // ledger.ActorHuman / ActorAgent / ActorSystem
	ID   string
	// On a Run's behalf: its project and task, so the event sits on them.
	ProjectID, TaskID, RunID string
}

func clean(title, content, kind string) (string, string, string, error) {
	title, content, kind = strings.TrimSpace(title), strings.TrimSpace(content), strings.TrimSpace(kind)
	if kind == "" {
		kind = "fact"
	}
	switch {
	case title == "":
		return "", "", "", invalid("a title is required")
	case len([]rune(title)) > 200:
		return "", "", "", invalid("a title is at most 200 characters: one line to scan in a list")
	case len(content) > 20000:
		return "", "", "", invalid("at most 20000 characters: keep one fact, procedure or note per memory")
	case !kinds[kind]:
		return "", "", "", invalid("kind is fact, procedure or note")
	}
	return title, content, kind, nil
}

// Create writes a memory, live at once, and records it.
func Create(ctx context.Context, tx pgx.Tx, org string, n New, by Actor) (Memory, error) {
	title, content, kind, err := clean(n.Title, n.Content, n.Kind)
	if err != nil {
		return Memory{}, err
	}
	if n.ProjectID != "" && n.SessionID != "" {
		return Memory{}, invalid("a session's memory belongs to no project")
	}
	if n.ProjectID != "" {
		if err := exists(ctx, tx, "project", n.ProjectID); err != nil {
			return Memory{}, err
		}
	}
	about, err := checkRefs(ctx, tx, n.About)
	if err != nil {
		return Memory{}, err
	}
	id := ids.New(ids.Memory)
	var srcType, srcID any
	if n.Source != nil {
		srcType, srcID = n.Source.Type, n.Source.ID
	}
	if _, err := tx.Exec(ctx, `INSERT INTO memories (id, organization_id, project_id, session_id, title, content, kind,
			author_kind, author_person_id, created_by_run_id, system_reason, source_type, source_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
		id, org, db.Nullable(n.ProjectID), db.Nullable(n.SessionID), title, content, kind,
		n.Author.Kind, db.Nullable(n.Author.PersonID), db.Nullable(n.Author.RunID), db.Nullable(n.Author.Reason),
		srcType, srcID); err != nil {
		return Memory{}, err
	}
	if err := setRefs(ctx, tx, org, id, about); err != nil {
		return Memory{}, err
	}
	if err := record(ctx, tx, org, by, EvCreated, id, n.ProjectID, map[string]any{"title": title, "kind": kind}); err != nil {
		return Memory{}, err
	}
	return get(ctx, tx, id)
}

// Patch changes what is given; About, when given, replaces the list.
type Patch struct {
	Title     *string
	Content   *string
	Kind      *string
	ProjectID *string // "" moves it to the whole organization
	About     *[]Ref
}

// Update changes m, already read (and checked by the caller).
func Update(ctx context.Context, tx pgx.Tx, org string, m Memory, p Patch, by Actor) (Memory, error) {
	id := m.ID
	title, content, kind, project := m.Title, m.Content, m.Kind, m.ProjectID
	var err error
	if p.Title != nil {
		title = *p.Title
	}
	if p.Content != nil {
		content = *p.Content
	}
	if p.Kind != nil {
		kind = *p.Kind
	}
	if p.ProjectID != nil {
		project = *p.ProjectID
		if project != "" && m.SessionID != "" {
			return Memory{}, invalid("a session's memory stays the session's: it belongs to no project")
		}
		if project != "" {
			if err := exists(ctx, tx, "project", project); err != nil {
				return Memory{}, err
			}
		}
	}
	if title, content, kind, err = clean(title, content, kind); err != nil {
		return Memory{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE memories SET title = $2, content = $3, kind = $4, project_id = $5 WHERE id = $1`,
		id, title, content, kind, db.Nullable(project)); err != nil {
		return Memory{}, err
	}
	if p.About != nil {
		about, err := checkRefs(ctx, tx, *p.About)
		if err != nil {
			return Memory{}, err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM memory_refs WHERE memory_id = $1`, id); err != nil {
			return Memory{}, err
		}
		if err := setRefs(ctx, tx, org, id, about); err != nil {
			return Memory{}, err
		}
	}
	if err := record(ctx, tx, org, by, EvUpdated, id, project, map[string]any{"title": title}); err != nil {
		return Memory{}, err
	}
	return get(ctx, tx, id)
}

// Archive takes m, already read, out of every search, or puts it back.
func Archive(ctx context.Context, tx pgx.Tx, org string, m Memory, archived bool, by Actor) (Memory, error) {
	if (m.ArchivedAt != nil) == archived {
		return m, nil
	}
	id := m.ID
	var err error
	ev := EvRestored
	if archived {
		ev = EvArchived
		_, err = tx.Exec(ctx, `UPDATE memories SET archived_at = now(), archived_by = $2 WHERE id = $1`, id, by.ID)
	} else {
		_, err = tx.Exec(ctx, `UPDATE memories SET archived_at = NULL, archived_by = NULL WHERE id = $1`, id)
	}
	if err != nil {
		return Memory{}, err
	}
	if err := record(ctx, tx, org, by, ev, id, m.ProjectID, map[string]any{"title": m.Title}); err != nil {
		return Memory{}, err
	}
	return get(ctx, tx, id)
}

func record(ctx context.Context, tx pgx.Tx, org string, by Actor, typ, id, project string, payload map[string]any) error {
	payload["memoryId"] = id
	if project == "" {
		project = by.ProjectID
	}
	_, err := ledger.Append(ctx, tx, ledger.Event{
		Type: typ, OrganizationID: org, ProjectID: project, TaskID: by.TaskID, RunID: by.RunID,
		ActorType: by.Type, ActorID: by.ID, Source: ledger.SourceOrchestrator, Payload: payload,
	})
	return err
}

var refTables = map[string]string{"task": "tasks", "epic": "epics", "project": "projects"}

func exists(ctx context.Context, tx pgx.Tx, typ, id string) error {
	missing, err := absent(ctx, tx, []Ref{{Type: typ, ID: id}})
	if err != nil {
		return err
	}
	if len(missing) > 0 {
		return invalid("no %s %s", typ, id)
	}
	return nil
}

// absent returns the refs that name nothing here: one query per type.
func absent(ctx context.Context, tx pgx.Tx, refs []Ref) ([]Ref, error) {
	byType := map[string][]string{}
	for _, r := range refs {
		if refTables[r.Type] == "" {
			return nil, invalid("%q is not a task, epic or project", r.Type)
		}
		byType[r.Type] = append(byType[r.Type], r.ID)
	}
	var missing []Ref
	for typ, list := range byType {
		rows, err := tx.Query(ctx, `SELECT u.id FROM unnest($1::text[]) u(id)
			WHERE NOT EXISTS (SELECT 1 FROM `+refTables[typ]+` x WHERE x.id = u.id)`, list)
		if err != nil {
			return nil, err
		}
		gone, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return nil, err
		}
		for _, id := range gone {
			missing = append(missing, Ref{Type: typ, ID: id})
		}
	}
	return missing, nil
}

func checkRefs(ctx context.Context, tx pgx.Tx, refs []Ref) ([]Ref, error) {
	if len(refs) > 20 {
		return nil, invalid("at most 20 things a memory is about")
	}
	seen := map[Ref]bool{}
	var out []Ref
	for _, r := range refs {
		r = Ref{Type: r.Type, ID: strings.TrimSpace(r.ID)}
		if !seen[r] {
			seen[r] = true
			out = append(out, r)
		}
	}
	missing, err := absent(ctx, tx, out)
	if err != nil {
		return nil, err
	}
	if len(missing) > 0 {
		return nil, invalid("no %s %s", missing[0].Type, missing[0].ID)
	}
	return out, nil
}

func setRefs(ctx context.Context, tx pgx.Tx, org, id string, refs []Ref) error {
	if len(refs) == 0 {
		return nil
	}
	types, ids := make([]string, len(refs)), make([]string, len(refs))
	for i, r := range refs {
		types[i], ids[i] = r.Type, r.ID
	}
	_, err := tx.Exec(ctx, `INSERT INTO memory_refs (memory_id, organization_id, ref_type, ref_id)
		SELECT $1, $2, t, i FROM unnest($3::text[], $4::text[]) u(t, i)`, id, org, types, ids)
	return err
}

// selectMemory reads a memory as people see it: who wrote it (an agent's
// Run gives the person it worked for and its task), and its index state.
const selectMemory = `SELECT m.id, coalesce(m.project_id, ''), coalesce(m.session_id, ''), m.title, m.content, m.kind, m.author_kind,
		coalesce(m.author_person_id, op.id, ''), coalesce(p.name, op.name, ''),
		coalesce(r.role::text, ''), coalesce(m.created_by_run_id, ''), coalesce(tp.key_prefix || '-' || t.number, ''),
		coalesce(m.system_reason, ''), coalesce(m.source_type, ''), coalesce(m.source_id, ''),
		m.archived_at, m.created_at, m.updated_at,
		CASE WHEN d.embedding IS NOT NULL THEN 'embedded' WHEN d.last_error IS NOT NULL THEN 'failed' ELSE 'waiting' END,
		coalesce(d.last_error, '')
	FROM memories m
	LEFT JOIN people p ON p.id = m.author_person_id
	LEFT JOIN runs r ON r.id = m.created_by_run_id
	LEFT JOIN tasks t ON t.id = r.task_id
	LEFT JOIN projects tp ON tp.id = t.project_id
	LEFT JOIN LATERAL (SELECT p.id, p.name FROM task_people membership JOIN people p ON p.id = membership.person_id
		WHERE membership.task_id = t.id AND p.removed_at IS NULL
		ORDER BY membership.position, membership.person_id LIMIT 1) op ON true
	LEFT JOIN search_documents d ON d.source_type = 'memory' AND d.source_id = m.id`

func scan(row pgx.Row) (Memory, error) {
	var m Memory
	var srcType, srcID string
	err := row.Scan(&m.ID, &m.ProjectID, &m.SessionID, &m.Title, &m.Content, &m.Kind, &m.Author.Kind,
		&m.Author.PersonID, &m.Author.PersonName, &m.Author.Role, &m.Author.RunID, &m.Author.TaskKey,
		&m.Author.Reason, &srcType, &srcID, &m.ArchivedAt, &m.CreatedAt, &m.UpdatedAt, &m.Index, &m.IndexNote)
	if srcType != "" {
		m.Source = &Ref{Type: srcType, ID: srcID}
	}
	return m, err
}

// Get reads one memory the viewer may read, with what it is about and
// where it was learned labelled; ErrNotFound for one they may not.
func Get(ctx context.Context, tx pgx.Tx, id string, v Viewer) (Memory, error) {
	return getWhere(ctx, tx, ` WHERE m.id = $1 AND `+visible("m", 2), id, v.Session, v.Person)
}

// get reads one memory just written by the caller, whoever may read it.
func get(ctx context.Context, tx pgx.Tx, id string) (Memory, error) {
	return getWhere(ctx, tx, ` WHERE m.id = $1`, id)
}

func getWhere(ctx context.Context, tx pgx.Tx, where string, args ...any) (Memory, error) {
	m, err := scan(tx.QueryRow(ctx, selectMemory+where, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return Memory{}, ErrNotFound
	}
	if err != nil {
		return Memory{}, err
	}
	all, err := labelled(ctx, tx, []Memory{m})
	if err != nil {
		return Memory{}, err
	}
	return all[0], nil
}

// ListQuery filters the Memories page.
type ListQuery struct {
	// Project: its own and the organization's. Empty: all of them.
	Project string
	// Scope, for the organization's page: "organization" or a project id.
	Scope    string
	Author   string // person | system | agent
	Text     string // a filter on the title and content, not a search
	Archived bool
	Limit    int
	Viewer   Viewer
}

func List(ctx context.Context, tx pgx.Tx, q ListQuery) ([]Memory, error) {
	if q.Limit <= 0 || q.Limit > 500 {
		q.Limit = 200
	}
	rows, err := tx.Query(ctx, selectMemory+`
		WHERE ($1 = '' OR m.project_id = $1 OR m.project_id IS NULL)
		  AND ($2 = '' OR ($2 = 'organization' AND m.project_id IS NULL AND m.session_id IS NULL) OR m.project_id = $2)
		  AND ($3 = '' OR m.author_kind = $3)
		  AND ($4 = '' OR m.title ILIKE '%' || $4 || '%' ESCAPE '\' OR m.content ILIKE '%' || $4 || '%' ESCAPE '\')
		  AND ($5 OR m.archived_at IS NULL)
		  AND `+visible("m", 7)+`
		ORDER BY m.archived_at IS NOT NULL, m.created_at DESC LIMIT $6`,
		q.Project, q.Scope, q.Author, db.LikeLiteral(strings.TrimSpace(q.Text)), q.Archived, q.Limit, q.Viewer.Session, q.Viewer.Person)
	if err != nil {
		return nil, err
	}
	all, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Memory, error) { return scan(r) })
	if err != nil {
		return nil, err
	}
	return labelled(ctx, tx, all)
}

// labelled fills each memory's refs and source with what a person reads:
// a task's key and status, an epic's title, a project's name.
func labelled(ctx context.Context, tx pgx.Tx, ms []Memory) ([]Memory, error) {
	if len(ms) == 0 {
		return ms, nil
	}
	idx := map[string]int{}
	memIDs := make([]string, len(ms))
	for i := range ms {
		idx[ms[i].ID] = i
		memIDs[i] = ms[i].ID
		ms[i].About = []Ref{}
	}
	rows, err := tx.Query(ctx, `SELECT memory_id, ref_type, ref_id FROM memory_refs WHERE memory_id = ANY($1) ORDER BY ref_type DESC, ref_id`, memIDs)
	if err != nil {
		return nil, err
	}
	type ref struct {
		mem string
		r   Ref
	}
	refs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (ref, error) {
		var x ref
		return x, row.Scan(&x.mem, &x.r.Type, &x.r.ID)
	})
	if err != nil {
		return nil, err
	}
	var want []Ref
	for _, x := range refs {
		want = append(want, x.r)
	}
	for _, m := range ms {
		if m.Source != nil {
			want = append(want, *m.Source)
		}
	}
	labels, err := Labels(ctx, tx, want)
	if err != nil {
		return nil, err
	}
	for _, x := range refs {
		i := idx[x.mem]
		if l, ok := labels[x.r.Type+"/"+x.r.ID]; ok {
			ms[i].About = append(ms[i].About, l)
		}
	}
	for i := range ms {
		if s := ms[i].Source; s != nil {
			if l, ok := labels[s.Type+"/"+s.ID]; ok {
				ms[i].Source = &l
			}
		}
	}
	return ms, nil
}

// Labels names tasks (by key, with status), epics and projects, in one
// query each. What no longer exists is left out.
func Labels(ctx context.Context, tx pgx.Tx, refs []Ref) (map[string]Ref, error) {
	byType := map[string][]string{}
	for _, r := range refs {
		byType[r.Type] = append(byType[r.Type], r.ID)
	}
	out := map[string]Ref{}
	queries := map[string]string{
		"task":    `SELECT t.id, p.key_prefix || '-' || t.number, t.status::text FROM tasks t JOIN projects p ON p.id = t.project_id WHERE t.id = ANY($1)`,
		"epic":    `SELECT id, title, '' FROM epics WHERE id = ANY($1)`,
		"project": `SELECT id, name, '' FROM projects WHERE id = ANY($1)`,
	}
	for typ, list := range byType {
		q, ok := queries[typ]
		if !ok {
			continue
		}
		rows, err := tx.Query(ctx, q, list)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			r := Ref{Type: typ}
			if err := rows.Scan(&r.ID, &r.Label, &r.Status); err != nil {
				rows.Close()
				return nil, err
			}
			out[typ+"/"+r.ID] = r
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}
