package phases

import (
	"bufio"
	"strconv"
	"strings"
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
	maxFileLines  = 2000
	maxTotalLines = 20000
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

// diffScript prints what a checkout changed since a commit: tracked files
// against it, then each untracked file (not ignored) as new — a file the
// agent wrote and has not added is still its work. $1 is the checkout, $2
// the commit. Read-only: nothing is staged, so the agent's index is left as
// it was. At most 200 untracked files, and none over a megabyte: a build's
// output the project forgot to ignore is not the agent's change.
const diffScript = `cd "$1" || exit 3
git -c core.quotePath=off diff --no-color --no-ext-diff --find-renames "$2" -- || exit $?
n=0
git -c core.quotePath=off ls-files -z --others --exclude-standard |
while IFS= read -r -d '' f; do
  n=$((n + 1)); [ "$n" -le 200 ] || break
  [ -f "$f" ] && [ "$(wc -c < "$f")" -le 1048576 ] || continue
  git -c core.quotePath=off diff --no-color --no-ext-diff --no-index -- /dev/null "$f"
done
exit 0`

// diffCommand is the exec that prints a checkout's diff against base.
func diffCommand(checkout, base string) []string {
	return []string{"bash", "-c", diffScript, "dude-diff", checkout, base}
}
