package phases

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// checkoutRead is what the gate read of a Run's checkouts at its checks:
// CheckoutScript's account (each repository's HEAD, its committer time and
// whether the tree is clean, and the container's clock), and the
// orchestrator's clock as the exec was sent, which places the commit on
// the clock the Run's calls are recorded on (delivery.Checkouts.Since).
type checkoutRead struct {
	delivery.Checkouts
	ReadStart time.Time
}

// unchangedBeforePush inspects, now, every repository the Run may push
// (runs.lux_pushes) against where its checkout started (runs.base_shas: lux's
// git.checkout, or a continued round's sync), in one exec
// (delivery.CheckoutScript). unchanged only when every one of them answered
// clean at its starting commit; any repository without a recorded base, an
// exec lux refuses or that exits non-zero, or output that does not account
// for each repository exactly once leaves it false, and the report is
// judged as any other. heads: each repository's HEAD as read. read: the
// whole account, for the evidence the conductor is shown (nil when the
// checkouts could not be read).
func (s *Syncer) unchangedBeforePush(ctx context.Context, r phaseRun) (heads map[string]string, unchanged bool, read *checkoutRead, err error) {
	if r.LuxRunID == "" {
		return nil, false, nil, nil
	}
	var pushes []string
	var bases map[string]string
	if err := s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		var raw []byte
		if err := tx.QueryRow(ctx, `SELECT lux_pushes, base_shas FROM runs WHERE id = $1`, r.ID).Scan(&pushes, &raw); err != nil {
			return err
		}
		return json.Unmarshal(raw, &bases)
	}); err != nil {
		return nil, false, nil, err
	}
	if len(pushes) == 0 {
		return nil, false, nil, nil
	}
	repos := map[string]string{}
	for _, name := range pushes {
		repos[name] = RepoPath(name)
	}
	cctx, cancel := context.WithTimeout(ctx, diffTimeout)
	defer cancel()
	start := s.clock()
	res, err := s.Lux.Exec(cctx, r.LuxRunID, delivery.CheckoutCommand("", repos))
	if err != nil {
		if ctx.Err() != nil {
			return nil, false, nil, ctx.Err()
		}
		s.Log.Info("could not inspect a Run's checkouts for changes; its checks are judged", "run", r.ID, "error", err)
		return nil, false, nil, nil
	}
	if c, ok := delivery.ParseCheckouts(string(res.Stdout), res.ExitCode, pushes); ok && len(c.Missing) == 0 {
		read = &checkoutRead{Checkouts: c, ReadStart: start}
	}
	heads, unchanged = parseUnchanged(string(res.Stdout), res.ExitCode, pushes, bases)
	if !unchanged && res.ExitCode != 0 {
		s.Log.Info("inspecting a Run's checkouts failed; its checks are judged", "run", r.ID,
			"error", fmt.Sprintf("exit %d: %s", res.ExitCode, strings.TrimSpace(string(res.Stderr))))
	}
	return heads, unchanged, read, nil
}

// clock is the orchestrator's clock, as its events' occurred_at has it.
func (s *Syncer) clock() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// parseUnchanged reads CheckoutScript's output: true only for exit 0 and
// exactly one clean line per repository of pushes, each at its base.
func parseUnchanged(out string, exit int, pushes []string, bases map[string]string) (map[string]string, bool) {
	c, ok := delivery.ParseCheckouts(out, exit, pushes)
	if !ok || len(c.Missing) > 0 || len(c.Heads) != len(pushes) {
		return nil, false
	}
	heads := map[string]string{}
	for _, h := range c.Heads {
		if !h.Clean || !isSHA(bases[h.Repo]) || h.SHA != bases[h.Repo] {
			return nil, false
		}
		heads[h.Repo] = h.SHA
	}
	return heads, true
}

// pushMovedUnchanged says whether a push made after a turn recorded as
// unchanged (checksDecision.Unchanged) moved any repository off the HEAD that
// inspection read: work that would reach the branch without being judged.
// raw is lux's git.push result.
func pushMovedUnchanged(raw json.RawMessage, heads map[string]string) (string, bool) {
	var push struct {
		Results []struct{ Repo, Commit, Status string } `json:"results"`
	}
	if err := json.Unmarshal(raw, &push); err != nil {
		return "", true
	}
	for _, res := range push.Results {
		if res.Status == "skipped" || res.Commit == "" {
			continue
		}
		if heads[res.Repo] != res.Commit {
			return res.Repo, true
		}
	}
	return "", false
}

// unchangedPushRefused fails r, kept, when the push made after a turn
// recorded as unchanged moved a repository: false when r may publish.
func (s *Syncer) unchangedPushRefused(ctx context.Context, r phaseRun) (bool, error) {
	var raw []byte
	err := s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		turn, err := delivery.LatestTurnTx(ctx, tx, r.ID)
		if err != nil {
			return err
		}
		err = tx.QueryRow(ctx, `SELECT payload FROM events WHERE run_id = $1 AND event_type = $2 AND (payload->>'turn')::bigint = $3
			ORDER BY cursor DESC LIMIT 1`, r.ID, delivery.EvRunChecksJudged, turn).Scan(&raw)
		if db.IsNotFound(err) {
			return nil
		}
		return err
	})
	if err != nil || raw == nil {
		return false, err
	}
	var d checksDecision
	if err := json.Unmarshal(raw, &d); err != nil || !d.Unchanged {
		return false, err
	}
	repo, moved := pushMovedUnchanged(r.PushResult, d.Heads)
	if !moved {
		return false, nil
	}
	return true, s.failKept(ctx, r, fmt.Sprintf("dude did not publish this work: its checks were not needed because the checkouts were "+
		"unchanged when the turn ended, but the push moved %s. Resume the agent so its report is judged, or retry the step.", repo))
}
