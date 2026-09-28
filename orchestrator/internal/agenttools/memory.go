package agenttools

import (
	"context"
	"errors"
	"fmt"
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

func searchMemory(ctx context.Context, tx pgx.Tx, c Caller, in searchMemoryIn) ([]searchHit, error) {
	if strings.TrimSpace(in.Query) == "" {
		return nil, refuse("a query is required")
	}
	for _, t := range in.Types {
		if t != "memory" && t != "task" && t != "epic" && t != "project" {
			return nil, refuse("types are memory, task, epic and project, not %q", t)
		}
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 8
	}
	out, err := memory.Ranked(ctx, tx, c.memory.query, memory.Query{Text: in.Query, Project: c.ProjectID, Types: in.Types, Limit: min(limit, 20)})
	if err != nil {
		return nil, err
	}
	var refs []memory.Ref
	for _, r := range out.Results {
		if r.Type == "task" {
			refs = append(refs, memory.Ref{Type: "task", ID: r.ID})
		}
	}
	labels, err := memory.Labels(ctx, tx, refs)
	if err != nil {
		return nil, err
	}
	hits := make([]searchHit, 0, len(out.Results))
	for _, r := range out.Results {
		h := searchHit{Type: r.Type, ID: r.ID, Title: r.Title,
			Snippet: strings.NewReplacer("⟦", "", "⟧", "").Replace(r.Snippet)}
		if r.Type == "task" {
			l := labels["task/"+r.ID]
			// The index titles a task "KEY title"; the key has its own field.
			h.Key, h.Status, h.Title = l.Label, l.Status, strings.TrimPrefix(r.Title, l.Label+" ")
		}
		hits = append(hits, h)
	}
	return hits, nil
}

// ---- get_memory --------------------------------------------------------------

type getMemoryIn struct {
	ID string `json:"id" jsonschema:"a memory's id, as search_memory gives it (mem_…)"`
}

func getMemory(ctx context.Context, tx pgx.Tx, c Caller, in getMemoryIn) (memory.Memory, error) {
	m, err := memory.Get(ctx, tx, strings.TrimSpace(in.ID))
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
	if c.memory.kick != nil {
		c.memory.kick()
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
	err = tx.QueryRow(ctx, `SELECT id FROM epics WHERE project_id = $1 AND lower(title) = lower($2) LIMIT 1`, project, name).Scan(&id)
	if err == nil {
		return memory.Ref{Type: "epic", ID: id}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return memory.Ref{}, err
	}
	return memory.Ref{}, refuse("%s", fmt.Sprintf("no task %q or epic %q in this project (list_tasks shows them)", name, name))
}
