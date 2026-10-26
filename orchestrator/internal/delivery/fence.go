package delivery

import (
	"context"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
)

// A chat integration's hidden projects (chat_hidden_projects): what a
// session, task or epic started from that provider's chat may never see.
// Whatever started it is in its origin column ({provider, ...}); the fence
// is read from there on every reach, so a project hidden later is fenced
// from then on, and what was started before a reinstall stays fenced.

// HiddenProjectSQL is the SQL test that the project in projectCol is
// hidden from the chat provider in providerExpr.
func HiddenProjectSQL(projectCol, providerExpr string) string {
	return fmt.Sprintf(`EXISTS (SELECT 1 FROM chat_hidden_projects hp WHERE hp.project_id = %s AND hp.provider = %s)`, projectCol, providerExpr)
}

// SessionHidesSQL is the SQL test that the project in projectCol is fenced
// off from the session in sessionExpr: hidden from the chat provider the
// session was started from. The one predicate every session read of a
// project, its repositories and its checkout shares.
func SessionHidesSQL(sessionExpr, projectCol string) string {
	return fmt.Sprintf(`EXISTS (SELECT 1 FROM sessions fs JOIN chat_hidden_projects hp ON hp.provider = fs.origin->>'provider'
		WHERE fs.id = %s AND hp.project_id = %s)`, sessionExpr, projectCol)
}

// SessionRepoHiddenSQL is SessionHidesSQL over session_repositories sr and
// repositories repo, as a constant for the syncer's constant predicates.
const SessionRepoHiddenSQL = `EXISTS (SELECT 1 FROM sessions fs JOIN chat_hidden_projects hp ON hp.provider = fs.origin->>'provider'
		WHERE fs.id = sr.session_id AND hp.project_id = repo.project_id)`

// Fence is what a session or task started from chat may not reach: the
// projects hidden from its provider. The zero Fence fences nothing.
type Fence struct {
	Provider string
	Hidden   []string
}

// Hides says project id is fenced off.
func (f Fence) Hides(project string) bool { return project != "" && slices.Contains(f.Hidden, project) }

// Keep is projects less the fenced ones.
func (f Fence) Keep(projects []string) []string {
	if len(f.Hidden) == 0 {
		return projects
	}
	return slices.DeleteFunc(slices.Clone(projects), f.Hides)
}

// FenceOf is the fence of the session (or, with no session, the task)
// named: its origin's provider's hidden projects. A task or epic filed from
// a chat-born session, or made by such a task's agent, carries the
// provider in its own origin (InheritOrigin), so it is fenced the same way.
func FenceOf(ctx context.Context, tx pgx.Tx, sessionID, taskID string) (Fence, error) {
	provider, err := originProvider(ctx, tx, sessionID, taskID)
	if err != nil || provider == "" {
		return Fence{}, err
	}
	return HiddenFrom(ctx, tx, provider)
}

func originProvider(ctx context.Context, tx pgx.Tx, sessionID, taskID string) (string, error) {
	var provider *string
	var err error
	switch {
	case sessionID != "":
		err = tx.QueryRow(ctx, `SELECT origin->>'provider' FROM sessions WHERE id = $1`, sessionID).Scan(&provider)
	case taskID != "":
		err = tx.QueryRow(ctx, `SELECT COALESCE(t.origin->>'provider', e.origin->>'provider') FROM tasks t
			LEFT JOIN epics e ON e.id = t.epic_id WHERE t.id = $1`, taskID).Scan(&provider)
	default:
		return "", nil
	}
	if db.IsNotFound(err) {
		return "", nil
	}
	if err != nil || provider == nil {
		return "", err
	}
	return *provider, nil
}

// HiddenFrom is the fence of everything provider's chat starts.
func HiddenFrom(ctx context.Context, tx pgx.Tx, provider string) (Fence, error) {
	rows, err := tx.Query(ctx, `SELECT project_id FROM chat_hidden_projects WHERE provider = $1 ORDER BY project_id`, provider)
	if err != nil {
		return Fence{}, err
	}
	hidden, err := pgx.CollectRows(rows, pgx.RowTo[string])
	return Fence{Provider: provider, Hidden: hidden}, err
}

// InheritOrigin gives a task or epic (table "tasks" or "epics", id) made
// from a session or a task that came from chat that origin's provider, so
// the same fence holds for it: {provider, via, from} with via "session" or
// "task" and from its id. It carries no conversation: it did not come from
// chat itself. Nothing is written when the source has no chat provider, or
// the row has an origin of its own.
func InheritOrigin(ctx context.Context, tx pgx.Tx, table, id, fromSession, fromTask string) error {
	provider, err := originProvider(ctx, tx, fromSession, fromTask)
	if err != nil || provider == "" {
		return err
	}
	via, from := "task", fromTask
	if fromSession != "" {
		via, from = "session", fromSession
	}
	query := `UPDATE tasks SET origin = jsonb_build_object('provider', $2::text, 'via', $3::text, 'from', $4::text) WHERE id = $1 AND origin IS NULL`
	if table == "epics" {
		query = `UPDATE epics SET origin = jsonb_build_object('provider', $2::text, 'via', $3::text, 'from', $4::text) WHERE id = $1 AND origin IS NULL`
	}
	_, err = tx.Exec(ctx, query, id, provider, via, from)
	return err
}
