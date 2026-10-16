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

// A publish is counted from GitHub's compares alone; nothing in the
// checkout or its container is read. start is the task's head before the
// publish (dude's), head the pushed commit (GitHub checked its objects on
// the push), and T the trusted base: the commit lux reported fetching
// (git.sync's `to`, kept in runs.synced_bases), which GitHub's base branch
// must contain. With T an ancestor of head, a file of start...head counts
// with GitHub's numbers unless its new name is a path T brought in
// (start...T); such a file counts only as T...head shows it, the task's
// change on top of the base it merged, so a pure import counts nothing.
// No T, a T that head does not contain, or any compare refused or cut
// short at GitHub's file cap counts start...head in full, Full saying why.

// emptyBlob is git's blob of an empty file.
const emptyBlob = "e69de29bb2d1d6434b8b29ae775ad8c2e48c5391"

// accounting is what a publish counts as the task's own change.
type accounting struct {
	Paths []string
	Lines int
	// A file GitHub would not show in lines (a binary, or a diff too
	// large): past any line limit.
	Binary bool
	// Why the count is start...head in full; "" when T's part was left out.
	Full string
}

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
// as the task's own, task being GitHub's compare start...head. trusted is
// T, "" with untrusted saying why. The only error is a forge's that may
// pass (forge.Transient).
func accountPublish(ctx context.Context, gh *forge.GitHub, slug, baseBranch, start, head, trusted, untrusted string,
	task forge.Comparison) (accounting, error) {
	full := func(why string) (accounting, error) {
		acc := counted(task.Files)
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
	onTop, err := gh.Compare(ctx, slug, trusted, head)
	switch {
	case err != nil && forge.Transient(err):
		return accounting{}, err
	case err != nil:
		return full(fmt.Sprintf("GitHub could not compare the push with the fetched %s: %v", short(trusted), err))
	case onTop.BehindBy > 0:
		return full(fmt.Sprintf("the push does not contain the fetched %s of %s", short(trusted), baseBranch))
	case onTop.Truncated():
		return full(fmt.Sprintf("GitHub listed %d files of the push on top of %s, its most", len(onTop.Files), baseBranch))
	}
	byBase := map[string]bool{}
	for _, f := range base.Files {
		byBase[f.Filename] = true
		if f.PreviousFilename != "" {
			byBase[f.PreviousFilename] = true
		}
	}
	ownOnTop := map[string]forge.ComparedFile{}
	for _, f := range onTop.Files {
		ownOnTop[f.Filename] = f
	}
	var own []forge.ComparedFile
	for _, f := range task.Files {
		if !byBase[f.Filename] {
			own = append(own, f)
		} else if g, changed := ownOnTop[f.Filename]; changed {
			own = append(own, g)
		}
	}
	return counted(own), nil
}

// counted adds up GitHub's numbers for files, both names of a rename or
// copy among the paths. A file with no patch is one GitHub would not show
// in lines, whatever its status, unless it is the empty blob.
func counted(files []forge.ComparedFile) accounting {
	var acc accounting
	paths := map[string]bool{}
	for _, f := range files {
		paths[f.Filename] = true
		if f.PreviousFilename != "" {
			paths[f.PreviousFilename] = true
		}
		acc.Lines += f.Additions + f.Deletions
		if f.Patch == "" && f.SHA != emptyBlob {
			acc.Binary = true
		}
	}
	acc.Paths = slices.Sorted(maps.Keys(paths))
	return acc
}
