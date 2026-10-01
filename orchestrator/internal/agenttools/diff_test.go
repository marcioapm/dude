package agenttools_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/phases"
)

// gitDiff is one file's `git diff` text: a hunk of adds added lines and dels
// removed ones around a context line.
func gitDiff(path string, adds, dels int) (text, hunks string) {
	var h strings.Builder
	fmt.Fprintf(&h, "@@ -1,%d +1,%d @@ func x()\n", dels+1, adds+1)
	h.WriteString(" context\n")
	for i := range dels {
		fmt.Fprintf(&h, "-old %d\n", i)
	}
	for i := range adds {
		fmt.Fprintf(&h, "+new %d\n", i)
	}
	return fmt.Sprintf("diff --git a/%[1]s b/%[1]s\nindex 1..2 100644\n--- a/%[1]s\n+++ b/%[1]s\n", path) + h.String(), h.String()
}

// storeDiff records a Run's diff as the orchestrator does: git's text,
// parsed by phases.ParseDiff, in run_diffs.
func (f *fixture) storeDiff(t *testing.T, run, text string) {
	t.Helper()
	files, _ := json.Marshal(phases.ParseDiff(text))
	mustExec(t, f.owner, `INSERT INTO run_diffs (run_id, organization_id, base, files, checksum, final)
		VALUES ($1, $2, 'abc123', $3::jsonb, 'sum', true)`, run, f.org, string(files))
}

// diffCall calls run_diff over the JSON API: the status and the raw body.
func (f *fixture) diffCall(t *testing.T, token, body string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest("POST", f.url+"/tools/run_diff", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(raw)
}

type listed struct {
	Run        string           `json:"run"`
	Base       string           `json:"base"`
	Final      bool             `json:"final"`
	UpdatedAt  *string          `json:"updatedAt"`
	TotalFiles int              `json:"totalFiles"`
	Additions  int              `json:"additions"`
	Deletions  int              `json:"deletions"`
	Offset     int              `json:"offset"`
	Limit      int              `json:"limit"`
	HasMore    bool             `json:"hasMore"`
	Files      []map[string]any `json:"files"`
	NotChanged []string         `json:"notChanged"`
	Cut        bool             `json:"cut"`
}

func decode(t *testing.T, status int, raw string) listed {
	t.Helper()
	var out listed
	if status != 200 || json.Unmarshal([]byte(raw), &out) != nil {
		t.Fatalf("run_diff: %d %s", status, raw)
	}
	return out
}

// diff calls run_diff and decodes a successful answer.
func (f *fixture) diff(t *testing.T, token, body string) listed {
	t.Helper()
	status, raw := f.diffCall(t, token, body)
	return decode(t, status, raw)
}

func paths(files []map[string]any) []string {
	var out []string
	for _, f := range files {
		out = append(out, f["path"].(string))
	}
	return out
}

// A diff of five files: churn 7 (b.go), 7 (a.go), 3 (c.go), 1 (d.go), and
// a binary file with none.
func (f *fixture) fiveFiles(t *testing.T, run string) map[string]string {
	t.Helper()
	text, hunks := "", map[string]string{}
	for _, file := range []struct {
		path       string
		adds, dels int
	}{{"c.go", 2, 1}, {"b.go", 4, 3}, {"a.go", 7, 0}, {"d.go", 0, 1}} {
		full, h := gitDiff(file.path, file.adds, file.dels)
		text += full
		hunks[file.path] = h
	}
	text += "diff --git a/logo.png b/logo.png\nnew file mode 100644\nindex 0..1\nBinary files /dev/null and b/logo.png differ\n"
	f.storeDiff(t, run, text)
	return hunks
}

func TestRunDiffListsFilesByChurnWithTotalsAndNoLines(t *testing.T) {
	f := setup(t)
	token := f.run(t, "run_d", "reviewer", "running")
	f.fiveFiles(t, "run_d")

	status, raw := f.diffCall(t, token, `{}`)
	out := decode(t, status, raw)
	if want := []string{"a.go", "b.go", "c.go", "d.go", "logo.png"}; !slices.Equal(paths(out.Files), want) {
		t.Errorf("order %v, want %v", paths(out.Files), want)
	}
	if out.Run != "run_d" || out.Base != "abc123" || !out.Final || out.UpdatedAt == nil ||
		out.TotalFiles != 5 || out.Additions != 13 || out.Deletions != 5 || out.HasMore || out.Limit != 200 {
		t.Errorf("header: %s", raw)
	}
	// No lines in the list: only counts, and the flags only when set.
	if strings.Contains(raw, "hunks") || strings.Contains(raw, "new 0") || strings.Contains(raw, "@@") {
		t.Errorf("the list carries lines: %s", raw)
	}
	a := out.Files[0]
	if a["status"] != "M" || a["additions"] != 7.0 || a["deletions"] != 0.0 || len(a) != 4 {
		t.Errorf("a.go: %v", a)
	}
	if logo := out.Files[4]; logo["binary"] != true || logo["status"] != "A" {
		t.Errorf("logo.png: %v", logo)
	}
}

func TestRunDiffPagesTheList(t *testing.T) {
	f := setup(t)
	token := f.run(t, "run_p", "implementer", "running")
	f.fiveFiles(t, "run_p")

	out := f.diff(t, token, `{"limit":2}`)
	if !slices.Equal(paths(out.Files), []string{"a.go", "b.go"}) || !out.HasMore || out.TotalFiles != 5 {
		t.Errorf("first page: %v more=%v total=%d", paths(out.Files), out.HasMore, out.TotalFiles)
	}
	out = f.diff(t, token, `{"limit":2,"offset":2}`)
	if !slices.Equal(paths(out.Files), []string{"c.go", "d.go"}) || !out.HasMore {
		t.Errorf("second page: %v more=%v", paths(out.Files), out.HasMore)
	}
	out = f.diff(t, token, `{"limit":2,"offset":4}`)
	if !slices.Equal(paths(out.Files), []string{"logo.png"}) || out.HasMore || out.Offset != 4 {
		t.Errorf("last page: %v more=%v", paths(out.Files), out.HasMore)
	}
	out = f.diff(t, token, `{"offset":9}`)
	if len(out.Files) != 0 || out.HasMore || out.TotalFiles != 5 {
		t.Errorf("past the end: %v more=%v", paths(out.Files), out.HasMore)
	}
	if out = f.diff(t, token, `{"limit":5000}`); out.Limit != 1000 {
		t.Errorf("limit 5000 became %d, want the cap of 1000", out.Limit)
	}
	if status, raw := f.diffCall(t, token, `{"offset":-1}`); status != 422 {
		t.Errorf("a negative offset: %d %s", status, raw)
	}
}

func TestRunDiffNameStatusIsPathAndStatusOnly(t *testing.T) {
	f := setup(t)
	token := f.run(t, "run_n", "implementer", "running")
	f.fiveFiles(t, "run_n")

	out := f.diff(t, token, `{"nameStatus":true,"limit":3,"offset":1}`)
	if !slices.Equal(paths(out.Files), []string{"b.go", "c.go", "d.go"}) || !out.HasMore {
		t.Errorf("page: %v more=%v", paths(out.Files), out.HasMore)
	}
	for _, file := range out.Files {
		if len(file) != 2 || file["status"] != "M" {
			t.Errorf("entry %v, want only path and status", file)
		}
	}
}

func TestRunDiffShowsTheNamedFilesAsUnifiedText(t *testing.T) {
	f := setup(t)
	token := f.run(t, "run_u", "implementer", "running")
	hunks := f.fiveFiles(t, "run_u")

	status, raw := f.diffCall(t, token, `{"paths":["c.go","nope.go","a.go"]}`)
	out := decode(t, status, raw)
	if !slices.Equal(paths(out.Files), []string{"c.go", "a.go"}) {
		t.Fatalf("files %v, want c.go and a.go in the order asked", paths(out.Files))
	}
	for _, file := range out.Files {
		if file["patch"] != hunks[file["path"].(string)] {
			t.Errorf("%s's patch:\n%s\nwant:\n%s", file["path"], file["patch"], hunks[file["path"].(string)])
		}
	}
	if !slices.Equal(out.NotChanged, []string{"nope.go"}) || out.Cut || out.TotalFiles != 5 {
		t.Errorf("notChanged %v cut %v total %d", out.NotChanged, out.Cut, out.TotalFiles)
	}
	if strings.Contains(raw, `"kind"`) || strings.Contains(raw, "b.go") {
		t.Errorf("more than the named files' text: %s", raw)
	}
	// Exact: a path is not a prefix or a pattern.
	out = f.diff(t, token, `{"paths":["a"]}`)
	if len(out.Files) != 0 || !slices.Equal(out.NotChanged, []string{"a"}) {
		t.Errorf("a matched %v", paths(out.Files))
	}
	if status, _ := f.diffCall(t, token, `{"paths":["a.go"],"nameStatus":true}`); status != 422 {
		t.Errorf("paths with nameStatus: %d", status)
	}
}

func TestRunDiffCapsTheLinesOfOneCallAndKeepsTruncated(t *testing.T) {
	f := setup(t)
	token := f.run(t, "run_c", "implementer", "running")
	// 1,200 added lines: stored cut at 1,000 (truncated); then 900, and 300.
	big, _ := gitDiff("big.go", 1200, 0)
	mid, _ := gitDiff("mid.go", 899, 0)
	small, _ := gitDiff("small.go", 299, 0)
	f.storeDiff(t, "run_c", big+mid+small)

	status, raw := f.diffCall(t, token, `{"paths":["big.go","mid.go","small.go"]}`)
	out := decode(t, status, raw)
	lines := func(i int) int {
		return strings.Count(out.Files[i]["patch"].(string), "\n") - 1 // less the @@ header
	}
	if len(out.Files) != 3 || !out.Cut {
		t.Fatalf("files %v cut %v", paths(out.Files), out.Cut)
	}
	big0, mid1, small2 := out.Files[0], out.Files[1], out.Files[2]
	if lines(0) != 1000 || big0["truncated"] != true || big0["cut"] != nil || big0["additions"] != 1200.0 {
		t.Errorf("big.go: %d lines, truncated %v, cut %v, additions %v", lines(0), big0["truncated"], big0["cut"], big0["additions"])
	}
	if lines(1) != 900 || mid1["cut"] != nil || mid1["truncated"] != nil {
		t.Errorf("mid.go: %d lines, cut %v", lines(1), mid1["cut"])
	}
	if lines(2) != 100 || small2["cut"] != true {
		t.Errorf("small.go: %d lines, cut %v; want the 100 left of 2,000", lines(2), small2["cut"])
	}
	// Alone, it is whole.
	out = f.diff(t, token, `{"paths":["small.go"]}`)
	if out.Cut || strings.Count(out.Files[0]["patch"].(string), "\n") != 301 {
		t.Errorf("small.go alone: cut %v", out.Cut)
	}
	// The list says truncated too.
	out = f.diff(t, token, `{}`)
	if out.Files[0]["path"] != "big.go" || out.Files[0]["truncated"] != true || out.Files[1]["truncated"] != nil {
		t.Errorf("list: %v", out.Files)
	}
}

func TestRunDiffOfARunNotReadYetIsEmpty(t *testing.T) {
	f := setup(t)
	token := f.run(t, "run_e", "implementer", "running")
	status, raw := f.diffCall(t, token, `{}`)
	out := decode(t, status, raw)
	if out.Run != "run_e" || out.TotalFiles != 0 || out.Files == nil || len(out.Files) != 0 || out.UpdatedAt != nil || out.HasMore {
		t.Errorf("no diff yet: %s", raw)
	}
	out = f.diff(t, token, `{"paths":["a.go"]}`)
	if len(out.Files) != 0 || !slices.Equal(out.NotChanged, []string{"a.go"}) {
		t.Errorf("a path of no diff: %v %v", paths(out.Files), out.NotChanged)
	}
}

func TestRunDiffReadsOnlyTheCallersTask(t *testing.T) {
	f := setup(t)
	token := f.run(t, "run_me", "reviewer", "running")
	// Another Run of the same task (the implementer before this reviewer).
	f.run(t, "run_sib", "implementer", "completed")
	f.fiveFiles(t, "run_sib")
	// Another task's Run, in the same project and organization.
	mustExec(t, f.owner, `INSERT INTO tasks (id, organization_id, project_id, number, title, status)
		VALUES ($1, $2, $3, 2, 'Other', 'running')`, "wi_other_"+f.org, f.org, f.project)
	mustExec(t, f.owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role)
		VALUES ('run_other', $1, $2, $3, 1, 'running', 'implement', 'implementer')`, f.org, f.project, "wi_other_"+f.org)
	f.fiveFiles(t, "run_other")

	if out := f.diff(t, token, `{"run":"run_sib"}`); out.Run != "run_sib" || out.TotalFiles != 5 {
		t.Errorf("a Run of the same task: %+v", out)
	}
	for _, body := range []string{`{"run":"run_other"}`, `{"run":"run_other","paths":["a.go"]}`} {
		status, raw := f.diffCall(t, token, body)
		if status != 422 || !strings.Contains(raw, "not of your task") || strings.Contains(raw, "a.go\"") {
			t.Errorf("another task's Run %s: %d %s", body, status, raw)
		}
	}
	if status, raw := f.diffCall(t, token, `{"run":"run_nowhere"}`); status != 422 || !strings.Contains(raw, "no run") {
		t.Errorf("an unknown Run: %d %s", status, raw)
	}
}
