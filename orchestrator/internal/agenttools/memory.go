package agenttools

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/guide"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
	"github.com/marciomartins/dude/orchestrator/internal/memory"
)

// How many memories a Run may add: enough to write down what it learned,
// not enough for a loop to fill the index.
const remembersPerRun = 20

// ---- search_memory -----------------------------------------------------------

type searchMemoryIn struct {
	Query string   `json:"query"`
	Types []string `json:"types,omitempty"`
	Limit int      `json:"limit,omitempty"`
}

type searchHit struct {
	Type    string `json:"type"`
	ID      string `json:"id"`
	Key     string `json:"key,omitempty"`
	Status  string `json:"status,omitempty"`
	Title   string `json:"title"`
	Snippet string `json:"snippet"`
	// A guide section's: where a person reads it in the web app.
	Link string `json:"link,omitempty"`
}

// embedQuery embeds search_memory's query before its transaction opens, so
// a slow embedder holds no connection (EmbedQuery waits at most 5s).
func embedQuery(ctx context.Context, c Caller, args json.RawMessage) any {
	var in struct{ Query string }
	_ = json.Unmarshal(args, &in)
	return memory.EmbedQuery(ctx, c.env.embedder, in.Query)
}

func searchMemory(ctx context.Context, tx pgx.Tx, c Caller, in searchMemoryIn) ([]searchHit, error) {
	return searchScoped(ctx, tx, c, in, memory.Query{Project: c.ProjectID})
}

// searchScoped searches within scope (its Project, OrgOnly and Session).
func searchScoped(ctx context.Context, tx pgx.Tx, c Caller, in searchMemoryIn, scope memory.Query) ([]searchHit, error) {
	if strings.TrimSpace(in.Query) == "" {
		return nil, refuseText(rQueryRequired)
	}
	for _, t := range in.Types {
		if !slices.Contains(memory.AgentTypes, t) {
			return nil, refuse(rTypesAreMemoryTaskEpicAnd, t)
		}
	}
	emb, _ := c.env.prepared.(memory.Embedded)
	limit := in.Limit
	if limit <= 0 {
		limit = 8
	}
	types := in.Types
	if len(types) == 0 {
		types = memory.AgentTypes
	}
	scope.Text, scope.Types, scope.Limit = in.Query, types, min(limit, 20)
	// An agent finds its own session's memories, no other session's.
	scope.Viewer = memory.Viewer{Session: c.SessionID}
	out, err := memory.Ranked(ctx, tx, emb, scope)
	if err != nil {
		return nil, err
	}
	hits := make([]searchHit, 0, len(out.Results))
	var slug string
	for _, r := range out.Results {
		hit := searchHit{Type: r.Type, ID: r.ID, Key: r.Key, Status: r.Status, Title: r.Title, Snippet: r.Snippet}
		if r.Type == memory.DocType {
			if slug == "" {
				if slug, err = orgSlug(ctx, tx, c); err != nil {
					return nil, err
				}
			}
			page, anchor, _ := strings.Cut(r.ID, "#")
			hit.ID, hit.Link = docPrefix+r.ID, guide.Link(c.env.publicURL, slug, page, anchor)
			hit.Snippet = guide.Resolve(hit.Snippet, c.env.publicURL, slug)
		}
		hits = append(hits, hit)
	}
	return hits, nil
}

// docPrefix marks a guide section's id among memory ids: doc:<page>#<anchor>.
const docPrefix = "doc:"

func orgSlug(ctx context.Context, tx pgx.Tx, c Caller) (string, error) {
	var slug string
	err := tx.QueryRow(ctx, `SELECT slug FROM organizations WHERE id = $1`, c.Org).Scan(&slug)
	return slug, err
}

// ---- get_memory --------------------------------------------------------------

type getMemoryIn struct {
	ID string `json:"id"`
}

// docOut is a guide section in full: dude's own text, not quoted as
// external text.
type docOut struct {
	ID    string `json:"id"`
	Type  string `json:"type"`
	Title string `json:"title"`
	Text  string `json:"text"`
	Link  string `json:"link"`
}

// getDoc reads the guide section a doc: id names, as guide_sections holds
// it (the text this deployment's search found).
func getDoc(ctx context.Context, tx pgx.Tx, c Caller, id string) (docOut, error) {
	key := strings.TrimPrefix(id, docPrefix)
	out := docOut{ID: id, Type: memory.DocType}
	var page, anchor string
	err := tx.QueryRow(ctx, `SELECT page, anchor, title, body FROM guide_sections WHERE id = $1`, key).
		Scan(&page, &anchor, &out.Title, &out.Text)
	if errors.Is(err, pgx.ErrNoRows) {
		return docOut{}, refuse(rNoGuideSection, id)
	}
	if err != nil {
		return docOut{}, err
	}
	slug, err := orgSlug(ctx, tx, c)
	if err != nil {
		return docOut{}, err
	}
	out.Link = guide.Link(c.env.publicURL, slug, page, anchor)
	out.Text = guide.Resolve(out.Text, c.env.publicURL, slug)
	return out, nil
}

func getMemory(ctx context.Context, tx pgx.Tx, c Caller, in getMemoryIn) (any, error) {
	if id := strings.TrimSpace(in.ID); strings.HasPrefix(id, docPrefix) {
		return getDoc(ctx, tx, c, id)
	}
	return getOneMemory(ctx, tx, c, in)
}

func getOneMemory(ctx context.Context, tx pgx.Tx, c Caller, in getMemoryIn) (memory.Memory, error) {
	m, err := memory.Get(ctx, tx, strings.TrimSpace(in.ID), memory.Viewer{Session: c.SessionID})
	switch {
	case errors.Is(err, memory.ErrNotFound):
		return memory.Memory{}, refuse(rNoMemory, in.ID)
	case err != nil:
		return memory.Memory{}, err
	case m.ProjectID != "" && m.ProjectID != c.ProjectID:
		// Another project's memory is not this Run's to read.
		return memory.Memory{}, refuse(rNoMemory, in.ID)
	case m.ArchivedAt != nil:
		return memory.Memory{}, refuse(rMemoryWasArchivedNoLongerHeld, in.ID)
	}
	return m, nil
}

// ---- remember ----------------------------------------------------------------

type rememberIn struct {
	Title   string   `json:"title"`
	Content string   `json:"content"`
	Kind    string   `json:"kind,omitempty"`
	About   []string `json:"about,omitempty"`
	Scope   string   `json:"scope,omitempty"`
}

type rememberOut struct {
	ID string `json:"id"`
}

func remember(ctx context.Context, tx pgx.Tx, c Caller, in rememberIn) (rememberOut, error) {
	project := c.ProjectID
	switch in.Scope {
	case "", "project":
	case "organization":
		project = ""
	default:
		return rememberOut{}, refuseText(rScopeProjectOrOrganization)
	}
	var about []memory.Ref
	for _, a := range in.About {
		ref, err := resolveAbout(ctx, tx, c.ProjectID, a)
		if err != nil {
			return rememberOut{}, err
		}
		about = append(about, ref)
	}
	m, err := memory.Create(ctx, tx, c.Org, memory.New{
		ProjectID: project, Title: in.Title, Content: in.Content, Kind: in.Kind, About: about,
		Author: memory.Author{Kind: "agent", RunID: c.RunID},
		Source: &memory.Ref{Type: "task", ID: c.TaskID},
	}, memory.Actor{Type: ledger.ActorAgent, ID: c.RunID, ProjectID: c.ProjectID, TaskID: c.TaskID, RunID: c.RunID})
	var bad *memory.Invalid
	if errors.As(err, &bad) {
		return rememberOut{}, refuse("%s", bad.Reason)
	}
	if err != nil {
		return rememberOut{}, err
	}
	if c.env.kick != nil {
		c.env.kick()
	}
	return rememberOut{ID: m.ID}, nil
}

// resolveAbout finds what an agent names: a task by its key, or an epic by
// its title, in its project.
func resolveAbout(ctx context.Context, tx pgx.Tx, project, name string) (memory.Ref, error) {
	name = strings.TrimSpace(name)
	var id string
	err := tx.QueryRow(ctx, `SELECT t.id FROM tasks t JOIN projects p ON p.id = t.project_id
		WHERE t.project_id = $1 AND upper(p.key_prefix || '-' || t.number) = upper($2)`, project, name).Scan(&id)
	if err == nil {
		return memory.Ref{Type: "task", ID: id}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return memory.Ref{}, err
	}
	if id, err = epicByTitle(ctx, tx, project, name); err != nil || id != "" {
		return memory.Ref{Type: "epic", ID: id}, err
	}
	return memory.Ref{}, refuse(rNoTaskOrEpicInThisProject, name, name)
}
