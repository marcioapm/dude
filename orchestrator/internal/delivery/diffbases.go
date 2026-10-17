package delivery

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// DiffGivens selects only commits lux delivered on this Run's imported branches.
func DiffGivens(ctx context.Context, tx pgx.Tx, runID, taskID string, attempt int, conductor bool) (map[string][]string, error) {
	var history map[string]map[string][]string
	if err := tx.QueryRow(ctx, `SELECT sync_history FROM runs WHERE id = $1`, runID).Scan(&history); err != nil || len(history) == 0 {
		return nil, err
	}
	tb, err := TaskBranchTx(ctx, tx, taskID, attempt)
	if err != nil {
		return nil, err
	}
	repos, err := TaskRepositories(ctx, tx, taskID)
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for _, repo := range repos {
		branch := tb.Bases[repo.Name]
		if branch == "" {
			branch = repo.DefaultBranch
		}
		branches := []string{branch}
		if conductor {
			branches = append([]string{tb.Branch}, branches...)
		}
		name := lux.SpecName(repo.Name)
		for _, ref := range branches {
			for _, sha := range history[name][ref] {
				if len(sha) == 40 && strings.Trim(sha, "0123456789abcdef") == "" {
					out[name] = append(out[name], sha)
				}
			}
		}
	}
	return out, nil
}
