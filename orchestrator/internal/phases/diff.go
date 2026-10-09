package phases

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// A Run's live diff, as the API serves it (GET /v1/runs/:id/diff) and the
// run.diff.updated event carries it: the agent's checkout now against the
// commit it started from.

type DiffFile struct {
	Path string `json:"path"`
	// M, A, D or R. An untracked file is A: to a person it is new.
	Status    string     `json:"status"`
	Additions int        `json:"additions"`
	Deletions int        `json:"deletions"`
	Hunks     []DiffHunk `json:"hunks"`
	// Lines past maxFileLines were left out; the counts are still whole.
	Truncated bool `json:"truncated,omitempty"`
	// A binary file: changed, with nothing to show line by line.
	Binary bool `json:"binary,omitempty"`
}

type DiffHunk struct {
	Header string     `json:"header"`
	Lines  []DiffLine `json:"lines"`
}

type DiffLine struct {
	// ' ', '+' or '-'.
	Kind string `json:"kind"`
	Old  *int   `json:"old"`
	New  *int   `json:"new"`
	Text string `json:"text"`
}

// What a diff keeps of a file, and of all of them: a Run that rewrote a
// lockfile must not put megabytes in the ledger each time it saves.
const (
	maxFileLines  = 1000
	maxTotalLines = 5000
)

// ParseDiff reads `git diff` output — several diffs one after another, as
// the diff script prints them — into files. Paths are as git prints them
// with core.quotePath off; one git still quotes (a tab, a quote) is
// unquoted.
func ParseDiff(text string) []DiffFile {
	var files []DiffFile
	var cur *DiffFile
	var hunk *DiffHunk
	var oldNo, newNo, total int
	flushHunk := func() {
		if cur != nil && hunk != nil {
			cur.Hunks = append(cur.Hunks, *hunk)
		}
		hunk = nil
	}
	flush := func() {
		flushHunk()
		if cur != nil {
			if cur.Hunks == nil {
				cur.Hunks = []DiffHunk{}
			}
			files = append(files, *cur)
		}
		cur = nil
	}
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "diff --git ") {
			flush()
			cur = &DiffFile{Status: "M", Path: gitHeaderPath(line[len("diff --git "):])}
			continue
		}
		if cur == nil {
			continue
		}
		if hunk == nil || !isHunkLine(line) {
			switch {
			case strings.HasPrefix(line, "new file mode"):
				cur.Status = "A"
				continue
			case strings.HasPrefix(line, "deleted file mode"):
				cur.Status = "D"
				continue
			case strings.HasPrefix(line, "rename to "):
				cur.Status, cur.Path = "R", unquotePath(line[len("rename to "):])
				continue
			case strings.HasPrefix(line, "Binary files "):
				cur.Binary = true
				continue
			case strings.HasPrefix(line, "+++ "):
				if p := line[4:]; p != "/dev/null" {
					cur.Path = strings.TrimPrefix(unquotePath(p), "b/")
				}
				continue
			case strings.HasPrefix(line, "--- "):
				if p := line[4:]; p != "/dev/null" && cur.Status == "D" {
					cur.Path = strings.TrimPrefix(unquotePath(p), "a/")
				}
				continue
			case strings.HasPrefix(line, "@@"):
				flushHunk()
				oldNo, newNo = hunkStarts(line)
				hunk = &DiffHunk{Header: line, Lines: []DiffLine{}}
				continue
			}
			if hunk == nil {
				continue // index, mode and similarity lines
			}
		}
		// Inside a hunk.
		kind := " "
		if line != "" {
			kind = line[:1]
		}
		if kind == `\` {
			continue // "\ No newline at end of file"
		}
		var l DiffLine
		switch kind {
		case "+":
			cur.Additions++
			l = DiffLine{Kind: "+", New: num(newNo), Text: line[1:]}
			newNo++
		case "-":
			cur.Deletions++
			l = DiffLine{Kind: "-", Old: num(oldNo), Text: line[1:]}
			oldNo++
		default:
			l = DiffLine{Kind: " ", Old: num(oldNo), New: num(newNo), Text: strings.TrimPrefix(line, " ")}
			oldNo++
			newNo++
		}
		if fileLines(cur, hunk) >= maxFileLines || total >= maxTotalLines {
			cur.Truncated = true
			continue
		}
		hunk.Lines = append(hunk.Lines, l)
		total++
	}
	flush()
	return files
}

// isHunkLine: a line that belongs to the hunk being read. A hunk's lines
// all start with one of these; the next file or hunk starts otherwise.
func isHunkLine(line string) bool {
	if line == "" {
		return true // a blank context line, as some tools write it
	}
	switch line[0] {
	case ' ', '+', '-', '\\':
		// "--- a/x" and "+++ b/x" only come after "diff --git", which ends
		// the hunk first.
		return true
	}
	return false
}

func fileLines(f *DiffFile, h *DiffHunk) int {
	n := len(h.Lines)
	for _, x := range f.Hunks {
		n += len(x.Lines)
	}
	return n
}

// hunkStarts reads "@@ -a,b +c,d @@" into a and c.
func hunkStarts(header string) (int, int) {
	fields := strings.Fields(header)
	start := func(s string) int {
		s, _, _ = strings.Cut(s[1:], ",")
		n, _ := strconv.Atoi(s)
		return n
	}
	if len(fields) < 3 || len(fields[1]) < 2 || len(fields[2]) < 2 {
		return 1, 1
	}
	return start(fields[1]), start(fields[2])
}

// gitHeaderPath is the new path in "a/x b/x": the header is ambiguous when
// a path holds " b/", so the ---/+++ and rename lines, read after it, have
// the last word.
func gitHeaderPath(rest string) string {
	if strings.HasPrefix(rest, `"`) {
		// Quoted: "a/x" "b/y".
		if i := strings.Index(rest, `" "`); i >= 0 {
			return strings.TrimPrefix(unquotePath(rest[i+2:]), "b/")
		}
	}
	if i := strings.LastIndex(rest, " b/"); i >= 0 {
		return rest[i+3:]
	}
	return rest
}

// unquotePath reads a path as git prints it: quoted when it holds a
// character git escapes, and followed by a tab on ---/+++ lines when it
// holds a space.
func unquotePath(p string) string {
	p = strings.TrimSuffix(p, "\t")
	if strings.HasPrefix(p, `"`) {
		if s, err := strconv.Unquote(p); err == nil {
			return s
		}
	}
	return p
}

func num(n int) *int { return &n }

// diffScript prints what each checkout changed since the commit it started
// from: tracked files against it, and untracked ones (not ignored) as new —
// a file the agent wrote and has not added is still its work. Each
// repository's section starts "# dude-diff <name> <base>".
//
// It is POSIX sh, for any image, and read-only: untracked files are marked
// intent-to-add in a copy of the index, so the agent's own index is left as
// it was. Output stops at 8 MB.
//
// $1 is where the diff goes: "-" for standard output (the live read,
// through exec), or "artifacts" for the final diff, one <repository>.patch
// each published as .dude-final-diff/<repository>.patch (the beforeStop
// hook; together, in name order, they are what a live read prints). Then,
// per repository: its name, its checkout, and the commit it started from —
// a sha, or the branch lux checked out, whose first reflog entry is where
// it started (lux checks a branch out with checkout -B, which records it).
//
// A patch is written to a temporary directory and published with lux-shim
// publish, which keeps its own copy. A failed publish exits 3 after trying
// every repository.
const diffScript = `set -u
dest=$1; shift
base_of() {
  if first=$(git -C "$1" reflog show --format=%H "refs/heads/$2" -- 2>/dev/null | tail -n 1) && [ -n "$first" ]; then
    echo "$first"
  else
    git -C "$1" rev-parse -q --verify "$2^{commit}" 2>/dev/null || git -C "$1" rev-parse -q --verify "origin/$2^{commit}"
  fi
}
diffs() {
  while [ $# -ge 3 ]; do
    name=$1 dir=$2 ref=$3; shift 3
    [ -d "$dir" ] || continue
    base=$(base_of "$dir" "$ref") || continue
    echo "# dude-diff $name $base"
    idx=$(mktemp) || exit 3
    real=$(git -C "$dir" rev-parse --git-path index)
    case $real in /*) ;; *) real=$dir/$real ;; esac
    if [ -f "$real" ]; then cp "$real" "$idx"; else rm -f "$idx"; fi
    GIT_INDEX_FILE=$idx git -C "$dir" add -A -N . 2>/dev/null
    GIT_INDEX_FILE=$idx git -C "$dir" -c core.quotePath=off diff --no-color --no-ext-diff --find-renames "$base" --
    rm -f "$idx"
  done
}
if [ "$dest" = - ]; then
  diffs "$@" | head -c 8388608
elif [ -n "${LUX_ARTIFACTS:-}" ]; then
` + legacyFinalDiff + `
else
  tmp=$(mktemp -d) || exit 3
  status=0
  while [ $# -ge 3 ]; do
    diffs "$1" "$2" "$3" | head -c 8388608 > "$tmp/$1.patch" &&
      ` + lux.ShimBinary + ` publish "$tmp/$1.patch" --name "` + finalDiffDir + `/$1.patch" > /dev/null || status=3
    shift 3
  done
  rm -rf "$tmp"
  exit $status
fi`

// Until every lux has artifact-publish (lux#77): drop the copy.
const legacyFinalDiff = `  out=$LUX_ARTIFACTS/` + finalDiffDir + `
  mkdir -p "$out" || exit 3
  while [ $# -ge 3 ]; do
    diffs "$1" "$2" "$3" | head -c 8388608 > "$out/.$1.tmp" && mv "$out/.$1.tmp" "$out/$1.patch"
    shift 3
  done`

// The name the beforeStop hook publishes the final diff under: a
// <repository>.patch each, headed by its base like a live read's section.
// dude's own: nothing under it is listed as a file for people. lux takes a
// leading-dot segment (proto.ValidArtifactName refuses only . and ..).
const finalDiffDir = ".dude-final-diff"

// FinalDiffPrefix is where the final diff's artifacts are, as lux lists them.
const FinalDiffPrefix = lux.PublishedPrefix + finalDiffDir + "/"

// finalDiffRepo is the repository a final diff artifact is for, if it is
// one of the patches the hook leaves (not a file it had not finished).
func finalDiffRepo(path string) (string, bool) {
	name, ok := strings.CutPrefix(path, FinalDiffPrefix)
	if !ok {
		return "", false
	}
	repo, ok := strings.CutSuffix(name, ".patch")
	if !ok || repo == "" || strings.Contains(repo, "/") {
		return "", false
	}
	return repo, true
}

// diffCommand is the command that prints (dest "-") or saves (dest
// "artifacts") the diff of each repository, given as name → the commit or
// branch it started from.
func diffCommand(dest string, bases map[string]string) []string {
	cmd := []string{"sh", "-c", diffScript, "dude-diff", dest}
	for _, name := range slices.Sorted(maps.Keys(bases)) {
		cmd = append(cmd, name, RepoPath(name), bases[name])
	}
	return cmd
}

// diffChecksum identifies what diffScript printed — every repository's
// name, base and diff — so an identical read is known without parsing it.
func diffChecksum(text []byte) string {
	sum := sha256.Sum256(text)
	return hex.EncodeToString(sum[:])
}

// diffSection is one repository's part of what diffScript printed.
type diffSection struct {
	Repo, Base, Text string
}

// splitDiff splits diffScript's output into its repositories' sections.
func splitDiff(text string) []diffSection {
	var out []diffSection
	for _, part := range strings.Split("\n"+text, "\n# dude-diff ")[1:] {
		head, body, _ := strings.Cut(part, "\n")
		repo, base, _ := strings.Cut(head, " ")
		out = append(out, diffSection{Repo: repo, Base: base, Text: body})
	}
	return out
}

// parseRunDiff reads diffScript's output into a Run's diff: its files —
// each path prefixed with its repository when there are several — against
// the first repository's base, and a checksum of it all, which is how a
// read identical to the last is known before anything else is done.
func parseRunDiff(text string) RunDiff {
	diff := RunDiff{Files: []DiffFile{}, Checksum: diffChecksum([]byte(text))}
	sections := splitDiff(text)
	for i, sec := range sections {
		if i == 0 {
			diff.Base = sec.Base
		}
		for _, f := range ParseDiff(sec.Text) {
			if len(sections) > 1 {
				f.Path = sec.Repo + "/" + f.Path
			}
			diff.Files = append(diff.Files, f)
		}
	}
	return diff
}
