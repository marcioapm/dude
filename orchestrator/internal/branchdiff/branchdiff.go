// Package branchdiff pins a task attempt's committed branch, one
// repository at a time, as a snapshot (task_branch_snapshots, migration
// 230): the branch head against its target, `git diff <target>...<head>`,
// read from the repository's object graph through GitHub's compare. The
// merge base is computed at every capture, so merging the target into the
// branch moves it forward and the diff stays the branch's own changes.
//
// A snapshot names the one it supersedes and how its head relates to that
// one's (Classify). A move of the target alone makes none. Nothing a file
// could not show is dropped: a file GitHub gave no lines for though lines
// changed is truncated, a binary file binary, and a compare at GitHub's
// file cap partial.
package branchdiff

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/phases"
)

// Relations of a snapshot to the one before it.
const (
	Initial      = "initial"
	FastForward  = "fast-forward"
	MergedTarget = "merged-target"
	Rewritten    = "rewritten"
)

// Source is where a snapshot's files were read.
const Source = "github-compare"

// Classify is a new snapshot's relation to the previous one: none before
// it is initial; a head that descends from the previous head is a
// fast-forward when the merge base is the previous one's, and the target
// merged in when it moved; a head that does not descend was rewritten (a
// rebase or a force push).
func Classify(hasPrevious bool, previousMergeBase, mergeBase string, descends bool) string {
	switch {
	case !hasPrevious:
		return Initial
	case !descends:
		return Rewritten
	case mergeBase == previousMergeBase:
		return FastForward
	default:
		return MergedTarget
	}
}

// Stats is a snapshot's totals. Additions and deletions are GitHub's whole
// counts, whatever lines were kept.
type Stats struct {
	Files          int  `json:"files"`
	Additions      int  `json:"additions"`
	Deletions      int  `json:"deletions"`
	TruncatedFiles int  `json:"truncatedFiles"`
	BinaryFiles    int  `json:"binaryFiles"`
	FileCapReached bool `json:"fileCapReached"`
}

// maxSnapshotLines bounds the lines one snapshot keeps over all its files:
// past it a file keeps its counts and is truncated. Each file keeps at most
// phases' own per-file bound.
const maxSnapshotLines = 20_000

// emptyBlob is git's blob of an empty file: added or removed with no lines,
// and no patch, it is not binary.
const emptyBlob = "e69de29bb2d1d6434b8b29ae775ad8c2e48c5391"

// Files converts GitHub's compared files into the RunDiffFile shape a Run's
// diff has (phases.DiffFile), each patch read by phases.ParseDiff.
func Files(compared []forge.ComparedFile) ([]phases.DiffFile, Stats) {
	out := make([]phases.DiffFile, 0, len(compared))
	st := Stats{Files: len(compared), FileCapReached: len(compared) >= forge.CompareFileCap}
	kept := 0
	for _, c := range compared {
		f := phases.DiffFile{Path: c.Filename, Status: status(c.Status), Additions: c.Additions, Deletions: c.Deletions,
			Hunks: []phases.DiffHunk{}}
		st.Additions += c.Additions
		st.Deletions += c.Deletions
		changed := c.Additions+c.Deletions > 0
		switch {
		case c.Patch != "":
			parsed := phases.ParseDiff("diff --git a/" + c.Filename + " b/" + c.Filename + "\n" + c.Patch + "\n")
			if len(parsed) == 1 {
				p := parsed[0]
				lines := 0
				for _, h := range p.Hunks {
					lines += len(h.Lines)
				}
				if kept+lines > maxSnapshotLines {
					f.Truncated = true
				} else {
					f.Hunks = p.Hunks
					kept += lines
					// The patch's lines must account for GitHub's counts.
					f.Truncated = p.Truncated || p.Additions != c.Additions || p.Deletions != c.Deletions
				}
			} else {
				f.Truncated = changed
			}
		case changed:
			// Lines changed and GitHub sent none: too large to show.
			f.Truncated = true
		case binaryWithoutLines(c):
			f.Binary = true
		}
		if f.Truncated {
			st.TruncatedFiles++
		}
		if f.Binary {
			st.BinaryFiles++
		}
		out = append(out, f)
	}
	return out, st
}

// binaryWithoutLines: no lines, no patch, and not a rename, a mode change
// or an empty file: GitHub shows a binary file so.
func binaryWithoutLines(c forge.ComparedFile) bool {
	switch c.Status {
	case "renamed", "changed", "unchanged":
		return false
	}
	return c.SHA != emptyBlob
}

// status maps GitHub's file status onto a Run diff's: M, A, D or R. A copy
// is a new file to a person.
func status(s string) string {
	switch s {
	case "added", "copied":
		return "A"
	case "removed":
		return "D"
	case "renamed":
		return "R"
	}
	return "M"
}

// Checksum identifies a snapshot's diff: its merge base, head and files.
func Checksum(mergeBase, head string, files []phases.DiffFile) string {
	raw, _ := json.Marshal(files)
	h := sha256.New()
	fmt.Fprintf(h, "%s\n%s\n", mergeBase, head)
	h.Write(raw)
	return hex.EncodeToString(h.Sum(nil))
}

// partialReason is why a snapshot at GitHub's file cap is partial.
func partialReason(listed int) string {
	return fmt.Sprintf("GitHub's compare lists at most %d files: %d listed, more may have changed", forge.CompareFileCap, listed)
}
