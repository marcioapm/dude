package agenttools

import (
	"context"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// A session or task started from chat is fenced from the projects hidden
// from its provider (delivery.Fence). A session's own tools read projects
// through delivery.SessionProjects and OrgProjects, which leave fenced ones
// out; fenced is the check every other reach makes: a task agent's own
// project, a project or task named by key, a memory's project.

// fenceOf is c's fence: its session's, else its task's.
func fenceOf(ctx context.Context, tx pgx.Tx, c Caller) (delivery.Fence, error) {
	if c.AutomationID != "" {
		return delivery.Fence{}, nil
	}
	return delivery.FenceOf(ctx, tx, c.SessionID, c.TaskID)
}

// fenced refuses project for c when it is hidden from c's chat provider;
// named is how the refusal names it.
func fenced(ctx context.Context, tx pgx.Tx, c Caller, project, named string) error {
	if project == "" {
		return nil
	}
	f, err := fenceOf(ctx, tx, c)
	if err != nil || !f.Hides(project) {
		return err
	}
	if strings.TrimSpace(named) == "" {
		var key string
		if err := tx.QueryRow(ctx, `SELECT key_prefix FROM projects WHERE id = $1`, project).Scan(&key); err == nil {
			named = key
		} else {
			named = "the project"
		}
	}
	return refuse(rProjectHiddenFromChat, named, f.Provider)
}

// hiddenNamed refuses key when it names (by key, name or id) a project
// fenced off from c, so the agent is told why rather than that it does not
// exist; nil otherwise.
func hiddenNamed(ctx context.Context, tx pgx.Tx, c Caller, key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil
	}
	f, err := fenceOf(ctx, tx, c)
	if err != nil || len(f.Hidden) == 0 {
		return err
	}
	var id, prefix string
	err = tx.QueryRow(ctx, `SELECT id, key_prefix FROM projects WHERE id = ANY($1)
		AND (id = $2 OR upper(key_prefix) = upper($2) OR lower(name) = lower($2)) LIMIT 1`, f.Hidden, key).Scan(&id, &prefix)
	if db.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return refuse(rProjectHiddenFromChat, prefix, f.Provider)
}

// ownProjectTools are the task agents' tools that read or write their own
// task's project (its tasks, epics, repositories, memory and settings): a
// task filed from chat whose project was hidden since is refused them. The
// GitHub reads are fenced by readableRepos.
var ownProjectTools = []string{"list_tasks", "list_epics", "list_repositories", "request_repository", "create_task",
	"search_memory", "get_memory", "remember", "settings", "run_chat", "run_diff"}

func reachesOwnProject(t tool) bool { return !t.session && slices.Contains(ownProjectTools, t.name) }

// ownProjectFenced refuses a task agent's call when its own task's project
// is hidden from the chat provider its task was filed from.
func ownProjectFenced(ctx context.Context, tx pgx.Tx, c Caller) error {
	if c.SessionID != "" || c.ProjectID == "" {
		return nil
	}
	f, err := fenceOf(ctx, tx, c)
	if err != nil || !f.Hides(c.ProjectID) {
		return err
	}
	return refuse(rTaskProjectHiddenFromChat, f.Provider)
}
