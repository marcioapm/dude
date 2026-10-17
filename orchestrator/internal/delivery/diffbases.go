package delivery

import (
	"cmp"
	"context"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// DiffGivens selects only commits lux delivered on this Run's imported branches.
func DiffGivens(ctx context.Context, tx pgx.Tx, runID, taskID string, attempt int, conductor bool) (map[string][]string, error) {
	var history map[string]map[string][]string
	var latest map[string]map[string]string
	if err := tx.QueryRow(ctx, `SELECT sync_history, synced_bases FROM runs WHERE id = $1`, runID).Scan(&history, &latest); err != nil {
		return nil, err
	}
	if history == nil {
		history = map[string]map[string][]string{}
	}
	for repo, refs := range latest {
		if history[repo] == nil {
			history[repo] = map[string][]string{}
		}
		for ref, sha := range refs {
			commits := history[repo][ref]
			if !slices.Contains(commits, sha) {
				history[repo][ref] = append(commits, sha)
			}
		}
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
		branch := cmp.Or(tb.Bases[repo.Name], repo.DefaultBranch)
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
