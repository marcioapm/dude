package delivery

import (
	"context"
	"encoding/json"
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
// the fix before it — which lines it changed (the spiral fact), its line
// delta and the recognised tests' share of it, the test declarations it
// removed from recognised test files, and the paths
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
				case strings.HasPrefix(line, "-") && h.Test:
					// A test removed is a test file's: a production file's
					// test-like name (def test_connection) is not one.
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

// newRanges is the new-side line ranges a patch changed, first and last
// line: its + lines, not the context GitHub shows around them. A run of -
// lines with no + line beside it (a deletion) is the lines either side of
// where the lines were: the line before them, and the line after them.
func newRanges(patch string) [][2]int {
	var lines []int
	mark := func(l int) {
		if l >= 1 && (len(lines) == 0 || lines[len(lines)-1] < l) {
			lines = append(lines, l)
		}
	}
	// next is the new side's next line; deleted, a run of - lines not yet
	// placed; plus, whether the change lines since the last context line
	// hold a + line.
	next, deleted, plus, in := 0, false, false, false
	place := func() {
		if deleted && !plus {
			mark(next - 1)
			mark(next)
		}
		deleted, plus = false, false
	}
	for _, line := range strings.Split(patch, "\n") {
		if m := hunkHeader.FindStringSubmatch(line); m != nil {
			place()
			start, _ := strconv.Atoi(m[1])
			next, in = start, true
			if m[2] == "0" {
				// No new line: start is the line before the hunk.
				next = start + 1
			}
			continue
		}
		switch {
		case !in, strings.HasPrefix(line, "\\"):
		case strings.HasPrefix(line, "+"):
			plus = true
			mark(next)
			next++
		case strings.HasPrefix(line, "-"):
			deleted = true
		default:
			place()
			next++
		}
	}
	place()
	out := [][2]int{}
	for _, l := range lines {
		if n := len(out); n > 0 && out[n-1][1]+1 >= l {
			out[n-1][1] = l
			continue
		}
		out = append(out, [2]int{l, l})
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
// .test or .spec file, Python's test_*.py, *_test.py and tests.py, Ruby's
// *_spec.rb, or anything under a test, tests, testdata or __tests__
// directory, or a Gradle or Android test source set (androidTest,
// integrationTest, functionalTest, testFixtures). Not a *Test.java (a
// production class may be named so: Maven's and Gradle's tests are under
// src/test/), nor a *Spec.js or a spec/ directory (an API's
// spec/openapi.yaml is production).
//
// The rule: recognise only layouts that are never production; a miss only
// moves lines out of the recognised share, and the wake's wording stays
// true. A false positive would put production lines in the share and make
// it false, so a layout that may be production stays unrecognised.
func IsTestPath(p string) bool {
	base := path.Base(p)
	switch {
	case strings.HasSuffix(base, "_test.go"),
		strings.HasPrefix(base, "test_") && strings.HasSuffix(base, ".py"),
		strings.HasSuffix(base, "_test.py"),
		base == "tests.py",
		strings.HasSuffix(base, "_spec.rb"):
		return true
	}
	for _, ext := range []string{".ts", ".tsx", ".js", ".jsx", ".mts", ".cts", ".mjs", ".cjs"} {
		stem, ok := strings.CutSuffix(base, ext)
		if ok && (strings.HasSuffix(stem, ".test") || strings.HasSuffix(stem, ".spec")) {
			return true
		}
	}
	for _, dir := range strings.Split(path.Dir(p), "/") {
		switch dir {
		case "test", "tests", "testdata", "__tests__",
			"androidTest", "integrationTest", "functionalTest", "testFixtures":
			return true
		}
	}
	return false
}

// known says the fix's change is kept for every repository it published,
// none cut short at GitHub's file cap.
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

// changeListed is how many names a round wake's change fact lists before
// it counts the rest.
const changeListed = 3

// roundChange is what a round wake says of the fix before it, from its
// kept change (wakes/facts/round-change): each part nil or empty when not
// known, or when there is nothing to say.
type roundChange struct {
	// Lines added and removed in every file it changed, summed over the
	// repositories it published, and the part of them in files IsTestPath
	// recognises: true whatever the heuristic misses, for a test file it
	// does not recognise is in the total and only out of the share.
	Delta *lineDelta
	// Test declarations it removed and did not add again under the same
	// name, listed up to changeListed, the rest counted.
	Removed     []string
	RemovedMore int
	// Files it changed that its brief did not name, likewise.
	Outside     []string
	OutsideMore int
}

// lineDelta is a fix's whole delta (Add, Del), and the part of it in
// recognised test files (TestAdd, TestDel).
type lineDelta struct{ Add, Del, TestAdd, TestDel int }

// brief is what a fix was asked to change: the places of the findings it
// was handed, and its conductor's words (its note, the queued review's
// note, the round's bar).
type brief struct {
	Findings []briefPlace
	Notes    []string
}

type briefPlace struct{ Repo, File string }

// change is what the fix's kept change says, as roundChange: unknown, nil
// parts, unless every repository it published (published) was kept in full.
func (h FixHunks) change(published []string, b brief) roundChange {
	var out roundChange
	if !h.known(published) {
		return out
	}
	allKnown := true
	delta := lineDelta{}
	var removed []string
	type changed struct{ repo, file string }
	var files []changed
	for _, repo := range published {
		for _, f := range h[repo].Files {
			files = append(files, changed{repo, f.File})
			if f.Unknown {
				allKnown = false
				continue
			}
			delta.Add += f.Add
			delta.Del += f.Del
			if f.Test {
				delta.TestAdd += f.Add
				delta.TestDel += f.Del
			}
			removed = append(removed, f.RemovedTests...)
		}
	}
	if allKnown {
		out.Delta = &delta
		out.Removed, out.RemovedMore = listed(removed)
	}
	// The brief's paths: its findings' files, and each token of its words
	// that is a changed file's path or a directory of one ("dir/").
	named := func(repo, file string) bool {
		for _, p := range b.Findings {
			if p.File != "" && strings.TrimPrefix(p.File, "./") == file && (p.Repo == "" || p.Repo == repo) {
				return true
			}
		}
		for _, tok := range noteTokens(b.Notes) {
			// A directory is named with its trailing slash or without
			// one, whole segments only: "internal/del" names nothing
			// under internal/delivery/. A word with no slash in it
			// ("api", "app") is not a directory.
			tok = strings.TrimRight(tok, ".,;:)`")
			dir := strings.TrimSuffix(tok, "/") + "/"
			for _, path := range []string{file, repo + "/" + file} {
				if tok == path || strings.Contains(tok, "/") && strings.HasPrefix(path, dir) {
					return true
				}
			}
		}
		return false
	}
	var outside []string
	anyNamed := false
	for _, c := range files {
		if named(c.repo, c.file) {
			anyNamed = true
			continue
		}
		where := c.file
		if len(published) > 1 {
			where = c.repo + "/" + c.file
		}
		outside = append(outside, where)
	}
	for _, p := range b.Findings {
		anyNamed = anyNamed || p.File != ""
	}
	if anyNamed {
		out.Outside, out.OutsideMore = listed(outside)
	}
	return out
}

// listed is names up to changeListed, and how many more.
func listed(names []string) ([]string, int) {
	if len(names) <= changeListed {
		return names, 0
	}
	return names[:changeListed], len(names) - changeListed
}

// noteTokens is the words of notes that may be paths: split on space,
// stripped of quoting and punctuation around them, and of a :line after.
func noteTokens(notes []string) []string {
	var out []string
	for _, note := range notes {
		for _, w := range strings.FieldsFunc(note, func(r rune) bool {
			return r == ' ' || r == '\n' || r == '\t' || r == ',' || r == ';' || r == '(' || r == ')' || r == '[' || r == ']'
		}) {
			w = strings.TrimRight(strings.TrimLeft(w, "`'\"*<>{}"), "`'\"*<>{}.:!?")
			if i := strings.LastIndex(w, ":"); i > 0 && strings.Trim(w[i+1:], "0123456789-") == "" {
				w = w[:i]
			}
			w = strings.TrimPrefix(w, "./")
			if w != "" {
				out = append(out, w)
			}
		}
	}
	return out
}

// fixChangeTx is the round wake's change fact for fix Run fix, whose
// round's review Runs are round: unknown parts nil.
func fixChangeTx(ctx context.Context, tx pgx.Tx, fix string, round []string) (roundChange, error) {
	var raw []byte
	var published, findingIDs []string
	var note string
	if err := tx.QueryRow(ctx, `SELECT fix_hunks, ARRAY(SELECT jsonb_object_keys(COALESCE(heads, '{}'::jsonb)) ORDER BY 1),
			COALESCE(finding_ids, '{}'), COALESCE(conductor_note, '')
		FROM runs WHERE id = $1 AND phase = 'fix'`, fix).Scan(&raw, &published, &findingIDs, &note); err != nil {
		if db.IsNotFound(err) {
			return roundChange{}, nil
		}
		return roundChange{}, err
	}
	var h FixHunks
	if raw == nil || json.Unmarshal(raw, &h) != nil {
		return roundChange{}, nil
	}
	b := brief{Notes: []string{note}}
	rows, err := tx.Query(ctx, `SELECT COALESCE(repo, ''), COALESCE(file, '') FROM review_findings WHERE id = ANY($1)`, findingIDs)
	if err != nil {
		return roundChange{}, err
	}
	if b.Findings, err = pgx.CollectRows(rows, pgx.RowToStructByPos[briefPlace]); err != nil {
		return roundChange{}, err
	}
	// The queued review's note (then.note) is on the round's review Runs.
	rows, err = tx.Query(ctx, `SELECT DISTINCT COALESCE(conductor_note, '') FROM runs WHERE id = ANY($1)`, round)
	if err != nil {
		return roundChange{}, err
	}
	notes, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return roundChange{}, err
	}
	b.Notes = append(b.Notes, notes...)
	return h.change(published, b), nil
}
