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
// the push), and T the trusted base: GitHub's merge base of its base
// branch and head (compare base...head), the newest commit of the base
// branch head contains, however it got there (a sync_base, the agent's
// own merge, GitHub's Update branch). A file of start...head counts with
// GitHub's numbers unless its new name is a path T brought in
// (start...T); such a file counts only as T...head shows it under that
// name, and under its old name too as T...head lists it, the task's
// change on top of the base it merged, so a pure import counts nothing.
// Each path counts once. A head holding no base newer than start has
// nothing in start...T.
// Any compare refused or cut short at GitHub's file cap counts
// start...head in full, Full saying why.

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
		, sync_history = sync_history || jsonb_build_object($2::text,
			COALESCE(sync_history->$2, '{}'::jsonb) || jsonb_build_object($3::text,
				CASE WHEN COALESCE(sync_history->$2->$3, '[]'::jsonb) ? $4::text
				THEN sync_history->$2->$3 ELSE COALESCE(sync_history->$2->$3, '[]'::jsonb) || to_jsonb($4::text) END))
		WHERE id = $1`, runID, res.Repo, res.Ref, res.To)
	return err
}

// isSHA says s is a full hexadecimal commit name.
func isSHA(s string) bool {
	return len(s) == 40 && strings.Trim(s, "0123456789abcdef") == ""
}

// accountPublish is what a publish of start..head in one repository counts
// as the task's own, task being GitHub's compare start...head. The only
// error is a forge's that may pass (forge.Transient).
func accountPublish(ctx context.Context, gh *forge.GitHub, slug, baseBranch, start, head string,
	task forge.Comparison) (accounting, error) {
	full := func(why string) (accounting, error) {
		acc := counted(task.Files)
		acc.Full = why
		return acc, nil
	}
	if task.Truncated() {
		return full(fmt.Sprintf("GitHub listed %d files, its most: every change counts", len(task.Files)))
	}
	// Three dots: onTop's files are T...head, T its merge base.
	onTop, err := gh.Compare(ctx, slug, baseBranch, head)
	switch {
	case err != nil && forge.Transient(err):
		return accounting{}, err
	case err != nil:
		return full(fmt.Sprintf("GitHub could not compare the push with %s: %v", baseBranch, err))
	case !isSHA(onTop.MergeBase.SHA):
		return full(fmt.Sprintf("GitHub named no merge base of the push and %s", baseBranch))
	case onTop.Truncated():
		return full(fmt.Sprintf("GitHub listed %d files of the push on top of %s, its most", len(onTop.Files), baseBranch))
	}
	trusted := onTop.MergeBase.SHA
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
		byBase[f.Filename] = true
		if f.PreviousFilename != "" {
			byBase[f.PreviousFilename] = true
		}
	}
	ownOnTop := map[string]forge.ComparedFile{}
	for _, f := range onTop.Files {
		ownOnTop[f.Filename] = f
	}
	// Each name counts once: a copy's source the task also changed is listed
	// under its own name and as the copy's previous_filename.
	var own []forge.ComparedFile
	counts := map[string]bool{}
	add := func(g forge.ComparedFile) {
		if !counts[g.Filename] {
			counts[g.Filename] = true
			own = append(own, g)
		}
	}
	for _, f := range task.Files {
		if !byBase[f.Filename] {
			add(f)
			continue
		}
		if g, changed := ownOnTop[f.Filename]; changed {
			add(g)
		}
		// A task deletion GitHub paired with a file the base added: T...head
		// lists the deletion under the old name, whether or not the base
		// changed that file too.
		if f.PreviousFilename != "" {
			if g, changed := ownOnTop[f.PreviousFilename]; changed {
				add(g)
			}
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
