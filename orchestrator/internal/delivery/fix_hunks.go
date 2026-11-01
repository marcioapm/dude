package delivery

import (
	"context"
	"encoding/json"
	"maps"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

// A fix Run's change as GitHub's compare showed it, kept when it publishes
// (runs.fix_hunks, by repository name): the facts a round wake says about
// the fix before it — which lines it changed (the spiral fact), its
// production-line delta, the test declarations it removed, and the paths
// it changed — are read from here, never from a compare made later. A
// repository the fix published that is not here, one whose compare was cut
// short (Truncated), or a file GitHub showed no patch for (Unknown), is a
// fact not known: the wake says nothing of it.

// FixHunks is runs.fix_hunks: per repository name.
type FixHunks map[string]RepoHunks

// RepoHunks is one repository's change.
type RepoHunks struct {
	// GitHub's compare listed its most files: the change may hold more.
	Truncated bool       `json:"truncated"`
	Files     []HunkFile `json:"files"`
}

// HunkFile is one file the fix changed: its new-side line ranges, its
// lines added and removed, whether it is a test file, and the test
// declarations it removed and did not add again under the same name.
type HunkFile struct {
	File string   `json:"file"`
	New  [][2]int `json:"new"`
	Add  int      `json:"add"`
	Del  int      `json:"del"`
	Test bool     `json:"test"`
	// GitHub showed no patch for lines it changed (a binary, a diff too
	// large): its ranges and removals are not known.
	Unknown      bool     `json:"unknown,omitempty"`
	RemovedTests []string `json:"removed_tests,omitempty"`
}

// SummarizeFix is a repository's change from its compare's files.
func SummarizeFix(files []forge.ComparedFile, truncated bool) RepoHunks {
	out := RepoHunks{Truncated: truncated, Files: []HunkFile{}}
	removed, added := map[string][]string{}, map[string]bool{}
	for _, f := range files {
		h := HunkFile{File: f.Filename, New: [][2]int{}, Add: f.Additions, Del: f.Deletions, Test: IsTestPath(f.Filename)}
		if f.Patch == "" {
			// No lines changed (a rename, an empty file) is known; lines
			// GitHub would not show are not.
			h.Unknown = f.Additions+f.Deletions > 0
		} else {
			h.New = newRanges(f.Patch)
			for _, line := range strings.Split(f.Patch, "\n") {
				switch {
				case strings.HasPrefix(line, "+"):
					for _, name := range testDecls(line[1:]) {
						added[name] = true
					}
				case strings.HasPrefix(line, "-"):
					for _, name := range testDecls(line[1:]) {
						removed[f.Filename] = append(removed[f.Filename], name)
					}
				}
			}
		}
		out.Files = append(out.Files, h)
	}
	for i, f := range out.Files {
		for _, name := range removed[f.File] {
			if !added[name] && !slices.Contains(out.Files[i].RemovedTests, name) {
				out.Files[i].RemovedTests = append(out.Files[i].RemovedTests, name)
			}
		}
	}
	return out
}

// hunkHeader is a unified diff hunk's header: its new side, start and count.
var hunkHeader = regexp.MustCompile(`(?m)^@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@`)

// newRanges is a patch's hunks' new-side line ranges, first and last line.
// A hunk adding no line (a pure deletion) is the lines either side of
// where the lines were: its start is the line before them, and a finding
// on the line after them is in it too.
func newRanges(patch string) [][2]int {
	out := [][2]int{}
	for _, m := range hunkHeader.FindAllStringSubmatch(patch, -1) {
		start, _ := strconv.Atoi(m[1])
		count := 1
		if m[2] != "" {
			count, _ = strconv.Atoi(m[2])
		}
		if count == 0 {
			out = append(out, [2]int{max(start, 1), start + 1})
			continue
		}
		out = append(out, [2]int{start, start + count - 1})
	}
	return out
}

// testDecl is a test's declaration on a diff line: Go's func TestX(,
// Python's def test_x, and a TS/JS it("…" or test("…".
var testDecl = []*regexp.Regexp{
	regexp.MustCompile(`^\s*func\s+(Test\w*)\s*\(`),
	regexp.MustCompile(`^\s*(?:async\s+)?def\s+(test\w*)\s*\(`),
	regexp.MustCompile(`^\s*(?:it|test)(?:\.\w+)?\(\s*(?:"([^"]+)"|'([^']+)'|` + "`([^`]+)`" + `)`),
}

func testDecls(line string) []string {
	var out []string
	for _, re := range testDecl {
		if m := re.FindStringSubmatch(line); m != nil {
			for _, g := range m[1:] {
				if g != "" {
					out = append(out, g)
					break
				}
			}
		}
	}
	return out
}

// IsTestPath says a repository path is a test's: Go's _test.go, a TS/JS
// .test or .spec file, Python's test_*.py and *_test.py, or anything under
// a tests, testdata or __tests__ directory.
func IsTestPath(p string) bool {
	base := path.Base(p)
	switch {
	case strings.HasSuffix(base, "_test.go"),
		strings.HasPrefix(base, "test_") && strings.HasSuffix(base, ".py"),
		strings.HasSuffix(base, "_test.py"):
		return true
	}
	for _, ext := range []string{".ts", ".tsx", ".js", ".jsx", ".mts", ".cts", ".mjs", ".cjs"} {
		stem, ok := strings.CutSuffix(base, ext)
		if ok && (strings.HasSuffix(stem, ".test") || strings.HasSuffix(stem, ".spec")) {
			return true
		}
	}
	for _, dir := range strings.Split(path.Dir(p), "/") {
		if dir == "tests" || dir == "testdata" || dir == "__tests__" {
			return true
		}
	}
	return false
}

// fixFacts is what a fix's kept change says, per the repositories it
// published (published): nil when any of them is not known (not kept, or
// its compare cut short).
func (h FixHunks) known(published []string) bool {
	if len(published) == 0 {
		return false
	}
	for _, repo := range published {
		r, ok := h[repo]
		if !ok || r.Truncated {
			return false
		}
	}
	return true
}

// repos is h's repositories in name order.
func (h FixHunks) repos() []string { return slices.Sorted(maps.Keys(h)) }

// Touches says whether a finding at repo/file:line is on lines the fix
// changed: false for a finding not known to be (no line, no repository
// that can be told, a file whose patch GitHub did not show, a compare cut
// short). published is the repositories the fix published; a finding that
// names no repository is placed only when the fix published one.
func (h FixHunks) Touches(published []string, repo, file string, line int) bool {
	if line <= 0 || file == "" {
		return false
	}
	if repo == "" {
		if len(published) != 1 {
			return false
		}
		repo = published[0]
	}
	r, ok := h[repo]
	if !ok || r.Truncated || !slices.Contains(published, repo) {
		return false
	}
	file = strings.TrimPrefix(file, "./")
	for _, f := range r.Files {
		if f.File != file || f.Unknown {
			continue
		}
		for _, rg := range f.New {
			if line >= rg[0] && line <= rg[1] {
				return true
			}
		}
	}
	return false
}

// ReviewedFix is the fix a review Run follows (its parent_run_id, a fix),
// with its kept change; its zero value for none.
type ReviewedFix struct {
	RunID string
	Hunks FixHunks
	// The repositories it published (runs.heads).
	Published []string
}

// ReviewedFixTx is the fix review Run runID follows: its parent, when that
// is a fix Run whose change was kept (runs.fix_hunks). None otherwise: a
// first review, one after a simplifier, or a fix whose change is not known.
func ReviewedFixTx(ctx context.Context, tx pgx.Tx, runID string) (ReviewedFix, error) {
	var fix ReviewedFix
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT p.id, p.fix_hunks, ARRAY(SELECT jsonb_object_keys(COALESCE(p.heads, '{}'::jsonb)) ORDER BY 1)
		FROM runs r JOIN runs p ON p.id = r.parent_run_id
		WHERE r.id = $1 AND p.phase = 'fix' AND p.fix_hunks IS NOT NULL`, runID).Scan(&fix.RunID, &raw, &fix.Published)
	if db.IsNotFound(err) {
		return ReviewedFix{}, nil
	}
	if err != nil {
		return ReviewedFix{}, err
	}
	if err := json.Unmarshal(raw, &fix.Hunks); err != nil {
		return ReviewedFix{}, nil
	}
	return fix, nil
}

// Touches says a finding at repo/file:line is on lines the fix changed.
func (f ReviewedFix) Touches(repo, file string, line int) bool {
	return f.RunID != "" && f.Hunks.Touches(f.Published, repo, file, line)
}
