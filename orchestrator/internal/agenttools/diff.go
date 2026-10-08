package agenttools

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// run_diff reads what run_diffs stores (migration 050, written by
// phases/livediff.go): one snapshot per Run, its checkout against the commit
// it started from. Files first and cheap; lines only for the paths asked.
const (
	diffListDefault = 200
	diffListMax     = 1000
	// Lines of patch text, hunk headers included, one call returns over
	// all the paths it names.
	diffPatchLines = 2000
)

type runDiffIn struct {
	Run        string   `json:"run,omitempty" jsonschema:"a Run of this task (run_…); empty is your own"`
	Paths      []string `json:"paths,omitempty" jsonschema:"files to show the changes of, as the list names them (exact); empty lists the files"`
	NameStatus bool     `json:"nameStatus,omitempty" jsonschema:"list only each file's path and status (M, A, D, R): the cheapest form"`
	Limit      int      `json:"limit,omitempty" jsonschema:"files per page of the list, 1 to 1000 (default 200)"`
	Offset     int      `json:"offset,omitempty" jsonschema:"files of the list to skip, for the next page"`
}

// diffHeader opens every answer: which snapshot, and its size in all.
type diffHeader struct {
	Run   string `json:"run"`
	Base  string `json:"base"`
	Final bool   `json:"final"`
	// null: no diff has been read for this Run yet.
	UpdatedAt  *time.Time `json:"updatedAt"`
	TotalFiles int        `json:"totalFiles"`
	Additions  int        `json:"additions"`
	Deletions  int        `json:"deletions"`
}

type diffListOut struct {
	diffHeader
	Offset  int  `json:"offset"`
	Limit   int  `json:"limit"`
	HasMore bool `json:"hasMore"`
	Files   any  `json:"files"`
}

type diffFileOut struct {
	Path      string `json:"path"`
	Status    string `json:"status"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Truncated bool   `json:"truncated,omitempty"`
	Binary    bool   `json:"binary,omitempty"`
}

type diffNameOut struct {
	Path   string `json:"path"`
	Status string `json:"status"`
}

type diffPatchesOut struct {
	diffHeader
	Files []diffPatchOut `json:"files"`
	// Asked for, and not in the diff: unchanged since the base, or no such file.
	NotChanged []string `json:"notChanged,omitempty"`
	LineCap    int      `json:"lineCap"`
	// Some file's lines stopped at lineCap; ask for fewer paths at once.
	Cut bool `json:"cut,omitempty"`
}

type diffPatchOut struct {
	diffFileOut
	Patch string `json:"patch"`
	// This call's line cap left out the rest of this file's lines.
	Cut bool `json:"cut,omitempty"`
}

// storedDiffFile is phases.DiffFile as stored (phases imports this package,
// so it is read by shape here).
type storedDiffFile struct {
	diffFileOut
	Hunks []struct {
		Header string `json:"header"`
		Lines  []struct {
			Kind string `json:"kind"`
			Text string `json:"text"`
		} `json:"lines"`
	} `json:"hunks"`
}

func runDiff(ctx context.Context, tx pgx.Tx, c Caller, in runDiffIn) (any, error) {
	paths := uniquePaths(in.Paths)
	switch {
	case in.Limit < 0 || in.Offset < 0:
		return nil, refuse("limit and offset cannot be negative")
	case len(paths) > 0 && (in.NameStatus || in.Limit != 0 || in.Offset != 0):
		return nil, refuse("nameStatus, limit and offset page the file list; with paths, leave them out")
	}
	runID := strings.TrimSpace(in.Run)
	if runID == "" {
		runID = c.RunID
	}
	// RLS keeps another organization's Runs out; the task check keeps out
	// the rest of this one.
	var task string
	err := tx.QueryRow(ctx, `SELECT task_id FROM runs WHERE id = $1 AND task_id IS NOT NULL`, runID).Scan(&task)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, refuse("no run %q", runID)
	}
	if err != nil {
		return nil, err
	}
	if task != c.TaskID {
		return nil, refuse("run %s is not of your task: only your task's runs can be read", runID)
	}

	head := diffHeader{Run: runID}
	listed, picked := []byte("[]"), []byte("[]")
	var updated time.Time
	// The list without its hunks, and the hunks only of the files asked for:
	// a join, so many paths are hashed rather than each searched for.
	err = tx.QueryRow(ctx, `SELECT d.base, d.final, d.updated_at,
			COALESCE((SELECT jsonb_agg(f - 'hunks') FROM jsonb_array_elements(d.files) f), '[]'),
			COALESCE((SELECT jsonb_agg(f) FROM jsonb_array_elements(d.files) f
				JOIN (SELECT DISTINCT unnest($2::text[]) AS path) asked ON asked.path = f->>'path'), '[]')
		FROM run_diffs d WHERE d.run_id = $1`, runID, paths).Scan(&head.Base, &head.Final, &updated, &listed, &picked)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// Not read yet: nothing changed, as the API says too.
	case err != nil:
		return nil, err
	default:
		head.UpdatedAt = &updated
	}
	var files []diffFileOut
	if err := json.Unmarshal(listed, &files); err != nil {
		return nil, err
	}
	for _, f := range files {
		head.Additions += f.Additions
		head.Deletions += f.Deletions
	}
	head.TotalFiles = len(files)

	if len(paths) > 0 {
		var stored []storedDiffFile
		if err := json.Unmarshal(picked, &stored); err != nil {
			return nil, err
		}
		return patches(head, paths, stored), nil
	}

	slices.SortFunc(files, func(a, b diffFileOut) int {
		return cmp.Or(cmp.Compare(b.Additions+b.Deletions, a.Additions+a.Deletions), strings.Compare(a.Path, b.Path))
	})
	limit := in.Limit
	if limit == 0 {
		limit = diffListDefault
	}
	limit = min(limit, diffListMax)
	// Clamped before adding: offset+limit overflows for offsets near MaxInt.
	start := min(in.Offset, len(files))
	end := start + min(limit, len(files)-start)
	page := files[start:end]
	out := diffListOut{diffHeader: head, Offset: in.Offset, Limit: limit, HasMore: end < len(files)}
	if in.NameStatus {
		names := make([]diffNameOut, len(page))
		for i, f := range page {
			names[i] = diffNameOut{Path: f.Path, Status: f.Status}
		}
		out.Files = names
	} else {
		out.Files = page
	}
	return out, nil
}

// patches renders the files asked for as unified diff text, in the order
// asked, until diffPatchLines lines in all: hunk headers count as lines.
func patches(head diffHeader, paths []string, stored []storedDiffFile) diffPatchesOut {
	out := diffPatchesOut{diffHeader: head, Files: []diffPatchOut{}, LineCap: diffPatchLines}
	byPath := map[string]storedDiffFile{}
	for _, f := range stored {
		byPath[f.Path] = f
	}
	left := diffPatchLines
	for _, p := range paths {
		f, ok := byPath[p]
		if !ok {
			out.NotChanged = append(out.NotChanged, p)
			continue
		}
		file := diffPatchOut{diffFileOut: f.diffFileOut}
		var b strings.Builder
		for _, h := range f.Hunks {
			// A nonempty hunk needs room for its header and first line.
			if left < 1+min(len(h.Lines), 1) {
				file.Cut = true
				break
			}
			b.WriteString(h.Header)
			b.WriteByte('\n')
			left--
			lines := h.Lines[:min(left, len(h.Lines))]
			for _, l := range lines {
				b.WriteString(l.Kind)
				b.WriteString(l.Text)
				b.WriteByte('\n')
			}
			left -= len(lines)
			if len(lines) < len(h.Lines) {
				file.Cut = true
				break
			}
		}
		file.Patch = b.String()
		out.Cut = out.Cut || file.Cut
		out.Files = append(out.Files, file)
	}
	return out
}

// uniquePaths keeps each path once, as given: a path matches exactly.
func uniquePaths(in []string) []string {
	out := []string{}
	seen := make(map[string]struct{}, len(in))
	for _, p := range in {
		if _, dup := seen[p]; p == "" || dup {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	return out
}
