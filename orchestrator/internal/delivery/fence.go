package delivery

import (
	"context"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
)

// HiddenProjectSQL tests whether projectCol is hidden from providerExpr.
func HiddenProjectSQL(projectCol, providerExpr string) string {
	return fmt.Sprintf(`EXISTS (SELECT 1 FROM chat_hidden_projects hp WHERE hp.project_id = %s AND hp.provider = %s)`, projectCol, providerExpr)
}

// SessionHidesSQL tests projectCol against the session's origin provider.
func SessionHidesSQL(sessionExpr, projectCol string) string {
	return fmt.Sprintf(`EXISTS (SELECT 1 FROM sessions fs JOIN chat_hidden_projects hp ON hp.provider = fs.origin->>'provider'
		WHERE fs.id = %s AND hp.project_id = %s)`, sessionExpr, projectCol)
}

// SessionRepoHiddenSQL is SessionHidesSQL over session_repositories sr and
// repositories repo, as a constant for the syncer's constant predicates.
const SessionRepoHiddenSQL = `EXISTS (SELECT 1 FROM sessions fs JOIN chat_hidden_projects hp ON hp.provider = fs.origin->>'provider'
		WHERE fs.id = sr.session_id AND hp.project_id = repo.project_id)`

// Fence holds a chat provider's hidden projects. Task callers exempt their
// own project; sessions have no exemption. The zero value hides nothing.
type Fence struct {
	Provider string
	Hidden   []string
}

func (f Fence) Hides(project string) bool { return project != "" && slices.Contains(f.Hidden, project) }

func (f Fence) Keep(projects []string) []string {
	if len(f.Hidden) == 0 {
		return projects
	}
	return slices.DeleteFunc(slices.Clone(projects), f.Hides)
}

// FenceOf reads the current hidden projects of the session's origin provider,
// or the task's when no session is named. It does not exempt the task's project.
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

// InheritOrigin copies a session or task's chat provider to a task or epic
// without an origin. It records {provider, via, from}, not a conversation:
// a task filed from a chat-born session reaches that session's thread
// through from (chat_notices.go).
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
