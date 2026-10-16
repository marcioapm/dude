package phases

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// The base a publish may leave out of the task's change. The checkout and
// its refs are the agent's, so the base is never read there: it is the
// commit lux reported fetching (git.sync's `to`, kept in runs.synced_bases
// as the follower reads it), and GitHub's base branch must contain it.
// Without both, nothing is left out: a full count over-counts, and is safe.

// recordSyncedBase keeps the commit of a git.sync lux reported for the
// Run: runs.synced_bases[repo][ref]. A failed sync, or one without a
// commit, keeps nothing.
func recordSyncedBase(ctx context.Context, tx pgx.Tx, runID string, raw json.RawMessage) error {
	var res lux.SyncResult
	if json.Unmarshal(raw, &res) != nil || res.Status == "failed" || res.Repo == "" || res.Ref == "" || !isSHA(res.To) {
		return nil
	}
	_, err := tx.Exec(ctx, `UPDATE runs SET synced_bases = synced_bases
			|| jsonb_build_object($2::text, COALESCE(synced_bases->$2, '{}'::jsonb) || jsonb_build_object($3::text, $4::text))
		WHERE id = $1`, runID, res.Repo, res.Ref, res.To)
	return err
}

// isSHA says s is a full hexadecimal commit name.
func isSHA(s string) bool {
	return len(s) == 40 && strings.Trim(s, "0123456789abcdef") == ""
}

// trustedBase is the commit of branch that lux last reported fetching into
// the Run's checkout of repo (lux's name), if GitHub's branch contains it
// (compare branch...commit is behind or identical: ahead by none); "" and
// why not otherwise. Only a forge failure that may pass is an error.
func (s *Syncer) trustedBase(ctx context.Context, gh *forge.GitHub, org, runID, slug, repo, branch string) (string, string, error) {
	var synced string
	if err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT COALESCE(synced_bases->$2->>$3, '') FROM runs WHERE id = $1`, runID, repo, branch).Scan(&synced)
	}); err != nil {
		return "", "", err
	}
	if synced == "" {
		return "", "lux reported no fetch of " + branch + " for this Run", nil
	}
	cmp, err := gh.Compare(ctx, slug, branch, synced)
	switch {
	case err != nil && forge.Transient(err):
		return "", "", err
	case err != nil:
		return "", fmt.Sprintf("GitHub could not compare %s with the fetched %s: %v", branch, short(synced), err), nil
	case cmp.AheadBy > 0:
		return "", fmt.Sprintf("the fetched %s is not on GitHub's %s", short(synced), branch), nil
	}
	return synced, "", nil
}
