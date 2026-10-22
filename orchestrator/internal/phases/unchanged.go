package phases

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// unchangedScript says, per repository, whether its checkout is exactly
// where it started: HEAD at the given commit, nothing staged, unstaged or
// untracked (ignored files aside, which nothing commits or pushes), and no
// merge, rebase, cherry-pick, revert or sequencer under way. Arguments:
// name, checkout, starting commit, per repository. Each prints one line,
// "clean <name> <head>" or "changed <name> <head>"; a checkout that is
// missing or that git cannot read prints "fail <name>" and the script exits
// 4. Read-only POSIX sh: git status refreshes the index's stat cache at most.
const unchangedScript = `set -u
status=0
while [ $# -ge 3 ]; do
  name=$1 dir=$2 base=$3; shift 3
  if [ ! -d "$dir" ]; then echo "fail $name"; status=4; continue; fi
  if ! head=$(git -C "$dir" rev-parse -q --verify 'HEAD^{commit}'); then echo "fail $name"; status=4; continue; fi
  if [ "$head" != "$base" ]; then echo "changed $name $head"; continue; fi
  if ! st=$(git -C "$dir" status --porcelain=v1 --untracked-files=all --ignore-submodules=none); then echo "fail $name"; status=4; continue; fi
  op=
  for ref in MERGE_HEAD CHERRY_PICK_HEAD REVERT_HEAD REBASE_HEAD; do
    git -C "$dir" rev-parse -q --verify "$ref" > /dev/null 2>&1 && op=$ref
  done
  for d in rebase-merge rebase-apply sequencer/todo; do
    if ! p=$(git -C "$dir" rev-parse --git-path "$d"); then op=unknown; continue; fi
    case $p in /*) ;; *) p=$dir/$p ;; esac
    [ -e "$p" ] && op=$d
  done
  if [ -z "$st" ] && [ -z "$op" ]; then
    echo "clean $name $head"
  else
    echo "changed $name $head"
  fi
done
exit $status`

// unchangedBeforePush inspects, now, every repository the Run may push
// (runs.lux_pushes) against where its checkout started (runs.base_shas: lux's
// git.checkout, or a continued round's sync). unchanged only when every one
// of them answered clean at its starting commit; any repository without a
// recorded base, an exec lux refuses or that exits non-zero, or output that
// does not account for each repository exactly once leaves it false, and the
// report is judged as any other. heads: each repository's HEAD as read.
func (s *Syncer) unchangedBeforePush(ctx context.Context, r phaseRun) (heads map[string]string, unchanged bool, err error) {
	if r.LuxRunID == "" {
		return nil, false, nil
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
		return nil, false, err
	}
	if len(pushes) == 0 {
		return nil, false, nil
	}
	cmd := []string{"sh", "-c", unchangedScript, "dude-unchanged"}
	for _, name := range slices.Sorted(slices.Values(pushes)) {
		if !isSHA(bases[name]) {
			return nil, false, nil
		}
		cmd = append(cmd, name, RepoPath(name), bases[name])
	}
	cctx, cancel := context.WithTimeout(ctx, diffTimeout)
	defer cancel()
	res, err := s.Lux.Exec(cctx, r.LuxRunID, cmd)
	if err != nil {
		if ctx.Err() != nil {
			return nil, false, ctx.Err()
		}
		s.Log.Info("could not inspect a Run's checkouts for changes; its checks are judged", "run", r.ID, "error", err)
		return nil, false, nil
	}
	heads, unchanged = parseUnchanged(string(res.Stdout), res.ExitCode, pushes, bases)
	if !unchanged && res.ExitCode != 0 {
		s.Log.Info("inspecting a Run's checkouts failed; its checks are judged", "run", r.ID,
			"error", fmt.Sprintf("exit %d: %s", res.ExitCode, strings.TrimSpace(string(res.Stderr))))
	}
	return heads, unchanged, nil
}

// parseUnchanged reads unchangedScript's output: true only for exit 0 and
// exactly one clean line per repository of pushes, each at its base.
func parseUnchanged(out string, exit int, pushes []string, bases map[string]string) (map[string]string, bool) {
	if exit != 0 {
		return nil, false
	}
	heads := map[string]string{}
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		f := strings.Fields(line)
		if len(f) != 3 || f[0] != "clean" || !slices.Contains(pushes, f[1]) || heads[f[1]] != "" || f[2] != bases[f[1]] {
			return nil, false
		}
		heads[f[1]] = f[2]
	}
	if len(heads) != len(pushes) {
		return nil, false
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
