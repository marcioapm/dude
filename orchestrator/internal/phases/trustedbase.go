package phases

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
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
// the Run's checkout of repo (lux's name), if GitHub's branch contains it;
// "" and why not otherwise. Only a failure that may pass is an error.
func (s *Syncer) trustedBase(ctx context.Context, gh *forge.GitHub, org, runID, slug, repo, branch string) (string, string, error) {
	var synced string
	if err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT COALESCE(synced_bases->$2->>$3, '') FROM runs WHERE id = $1`, runID, repo, branch).Scan(&synced)
	}); err != nil {
		return "", "", err
	}
	return verifyBase(ctx, gh, slug, branch, synced)
}

// verifyBase is synced if GitHub's branch contains it (compare
// branch...synced is behind or identical: ahead by none), else "" and why.
func verifyBase(ctx context.Context, gh *forge.GitHub, slug, branch, synced string) (string, string, error) {
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

// accountPublish is what a publish of start..head in one repository counts
// as the task's own, with GitHub's compare of the two (task) as the truth
// for every path the trusted base did not change. Only on paths the base
// changed too (GitHub's compare start...trusted) does the checkout's
// evidence decide, as the resolution of a base merge or a net diff: it can
// narrow such a path's count, never drop a path the base left alone.
// Without a trusted base (untrusted says why), with either compare cut
// short at GitHub's file cap, or with no evidence, the count is GitHub's
// whole compare and Full says why. An error is one that may pass: a
// forge's (forge.Transient), or an errAccountingTransient.
func accountPublish(ctx context.Context, evidence evidenceFunc, gh *forge.GitHub, slug, repo, baseBranch,
	start, head, trusted, untrusted string, task forge.Comparison) (accounting, error) {
	full := func(why string) (accounting, error) {
		acc := fromComparison(task, nil, nil)
		acc.Full = why
		return acc, nil
	}
	if task.Truncated() {
		return full(fmt.Sprintf("GitHub listed %d files, its most: every change counts", len(task.Files)))
	}
	if trusted == "" {
		return full(untrusted)
	}
	base, err := gh.Compare(ctx, slug, start, trusted)
	switch {
	case err != nil && forge.Transient(err):
		return accounting{}, err
	case err != nil:
		return full(fmt.Sprintf("GitHub could not say what %s brought in: %v", baseBranch, err))
	case base.Truncated():
		return full(fmt.Sprintf("GitHub listed %d files of what %s brought in, its most", len(base.Files), baseBranch))
	}
	byBase := map[string]bool{}
	for _, f := range base.Files {
		byBase[f.Filename], byBase[f.PreviousFilename] = true, f.PreviousFilename != ""
	}
	delete(byBase, "")
	shared := false
	for _, f := range task.Files {
		shared = shared || byBase[f.Filename] || byBase[f.PreviousFilename]
	}
	if !shared {
		return fromComparison(task, nil, nil), nil
	}
	ev, err := evidence(ctx, start, head, trusted, repo)
	switch {
	case accountingTransient(err):
		return accounting{}, err
	case err != nil:
		return full("the checkout's evidence is unavailable: " + err.Error())
	}
	return combine(task, byBase, ev), nil
}

// fromComparison is GitHub's compare as an accounting, leaving out the
// files skip names. A file added or modified with no lines counted is
// taken for one GitHub cannot count in lines (a binary), unless text says
// it is text; an empty file reads the same, and is over-counted.
func fromComparison(c forge.Comparison, skip func(forge.ComparedFile) bool, text map[string]bool) accounting {
	acc := accounting{ByPath: map[string]int{}}
	seen := map[string]bool{}
	for _, f := range c.Files {
		if skip != nil && skip(f) {
			continue
		}
		seen[f.Filename] = true
		if f.PreviousFilename != "" {
			seen[f.PreviousFilename] = true
		}
		acc.Lines += f.Additions + f.Deletions
		acc.ByPath[f.Filename] += f.Additions + f.Deletions
		if f.Additions+f.Deletions == 0 && (f.Status == "added" || f.Status == "modified") && !text[f.Filename] {
			acc.Binary = true
		}
	}
	acc.Paths = slices.Sorted(maps.Keys(seen))
	return acc
}

// combine counts GitHub's compare for each file the base did not touch,
// and the evidence for each path of a file both touched.
func combine(task forge.Comparison, byBase map[string]bool, ev accounting) accounting {
	touched := func(f forge.ComparedFile) bool { return byBase[f.Filename] || byBase[f.PreviousFilename] }
	text := map[string]bool{}
	for _, p := range ev.Paths {
		text[p] = !ev.binary[p]
	}
	acc := fromComparison(task, touched, text)
	decided := map[string]bool{}
	for _, f := range task.Files {
		if touched(f) {
			decided[f.Filename], decided[f.PreviousFilename] = true, f.PreviousFilename != ""
		}
	}
	paths := map[string]bool{}
	for _, p := range acc.Paths {
		paths[p] = true
	}
	for _, p := range ev.Paths {
		if !decided[p] {
			continue
		}
		paths[p] = true
		acc.Lines += ev.ByPath[p]
		acc.ByPath[p] += ev.ByPath[p]
		acc.Binary = acc.Binary || ev.binary[p]
	}
	acc.Paths = slices.Sorted(maps.Keys(paths))
	return acc
}
