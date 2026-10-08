package agenttools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/ledger"
	"github.com/marciomartins/dude/orchestrator/internal/memory"
)

// How many memories a Run may add: enough to write down what it learned,
// not enough for a loop to fill the index.
const remembersPerRun = 20

// ---- search_memory -----------------------------------------------------------

type searchMemoryIn struct {
	Query string   `json:"query" jsonschema:"what you want to know, in words: a question or the terms you would expect"`
	Types []string `json:"types,omitempty" jsonschema:"only these: memory, task, epic, project (default all)"`
	Limit int      `json:"limit,omitempty" jsonschema:"how many results, 1 to 20 (default 8)"`
}

type searchHit struct {
	Type    string `json:"type"`
	ID      string `json:"id"`
	Key     string `json:"key,omitempty"`
	Status  string `json:"status,omitempty"`
	Title   string `json:"title"`
	Snippet string `json:"snippet"`
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
		return nil, refuse("a query is required")
	}
	for _, t := range in.Types {
		if !slices.Contains(memory.Types, t) {
			return nil, refuse("types are memory, task, epic and project, not %q", t)
		}
	}
	emb, _ := c.env.prepared.(memory.Embedded)
	limit := in.Limit
	if limit <= 0 {
		limit = 8
	}
	scope.Text, scope.Types, scope.Limit = in.Query, in.Types, min(limit, 20)
	// An agent finds its own session's memories, no other session's.
	scope.Viewer = memory.Viewer{Session: c.SessionID}
	out, err := memory.Ranked(ctx, tx, emb, scope)
	if err != nil {
		return nil, err
	}
	hits := make([]searchHit, 0, len(out.Results))
	for _, r := range out.Results {
		hits = append(hits, searchHit{Type: r.Type, ID: r.ID, Key: r.Key, Status: r.Status, Title: r.Title,
			Snippet: r.Snippet})
	}
	return hits, nil
}

// ---- get_memory --------------------------------------------------------------

type getMemoryIn struct {
	ID string `json:"id" jsonschema:"a memory's id, as search_memory gives it (mem_…)"`
}

func getMemory(ctx context.Context, tx pgx.Tx, c Caller, in getMemoryIn) (memory.Memory, error) {
	m, err := memory.Get(ctx, tx, strings.TrimSpace(in.ID), memory.Viewer{Session: c.SessionID})
	switch {
	case errors.Is(err, memory.ErrNotFound):
		return memory.Memory{}, refuse("no memory %s", in.ID)
	case err != nil:
		return memory.Memory{}, err
	case m.ProjectID != "" && m.ProjectID != c.ProjectID:
		// Another project's memory is not this Run's to read.
		return memory.Memory{}, refuse("no memory %s", in.ID)
	case m.ArchivedAt != nil:
		return memory.Memory{}, refuse("memory %s was archived: it is no longer held true", in.ID)
	}
	return m, nil
}

// ---- remember ----------------------------------------------------------------

type rememberIn struct {
	Title   string   `json:"title" jsonschema:"one line that says what it is, as another agent would scan it in a list"`
	Content string   `json:"content" jsonschema:"the fact, procedure or note, in Markdown: what, and why it holds"`
	Kind    string   `json:"kind,omitempty" jsonschema:"fact (default), procedure (steps that work) or note"`
	About   []string `json:"about,omitempty" jsonschema:"task keys (TEXT-12) or epic titles this is about, in this project"`
	Scope   string   `json:"scope,omitempty" jsonschema:"project (default): this project only; organization: true in every project"`
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
		return rememberOut{}, refuse("scope is project or organization")
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
	return memory.Ref{}, refuse("%s", fmt.Sprintf("no task %q or epic %q in this project (list_tasks shows them)", name, name))
}
