package orchestrator_test

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
)

// Searching a passing job is the same GitHub read, checked and counted
// once, with the result withheld. It finds the proof in the middle, which
// the old failure excerpt cannot show. Both session and conductor use the
// same tool definition.
func TestGitHubCheckLogSearchReadsProofAndSpendsOneRead(t *testing.T) {
	for _, role := range []string{"brainstorm", "conductor"} {
		t.Run(role, func(t *testing.T) {
			var w *world
			var read func(string, string) map[string]any
			var scope, column string
			if role == "brainstorm" {
				s, run := brainstorming(t)
				w, scope, column = s.world, s.sessionOf(run), "session_id"
				read = func(tool, args string) map[string]any { return s.read(run, tool, args) }
			} else {
				var task string
				w, task = conductingWithAnotherProject(t)
				scope, column = task, "task_id"
				read = func(tool, args string) map[string]any { return w.must(task, tool, args) }
			}
			downloads := httptest.NewServer(w.gh.LogDownloads())
			t.Cleanup(downloads.Close)
			const proof = "test_goal_description_replaces_nul_in_event_metadata PASSED"
			const proof2 = "test_goal_with_nul_in_name_is_forwarded_without_creating_a_goal PASSED"
			w.gh.Set(func(s *fakegithub.Server) {
				s.CheckRuns = map[int64]fakegithub.CheckRun{50000000077: {App: "github-actions"}}
			})
			w.gh.SetJobLog(50000000077, strings.Repeat("setup\n", 1100)+proof+"\n"+proof2+"\n51 passed, 8 skipped\n"+strings.Repeat("cleanup\n", 1200), downloads.URL)
			args := `{"repo":"acme/target","checkId":50000000077,"match":"test_goal.*nul.*PASSED"}`
			out := read("github_check_log", args)
			text, ok := quoted(field(out, "log").(string))
			if !ok || text != "1099: setup\n1100: setup\n1101: "+proof+"\n1102: "+proof2+"\n1103: 51 passed, 8 skipped\n1104: cleanup" ||
				out["matches"] != float64(2) || out["shown"] != float64(2) || out["source"] != "log" {
				t.Fatalf("proof: %v", out)
			}
			// The result is withheld: the proof the search found is in no
			// event of the scope, of any type, though its call is recorded.
			if n := w.count(`SELECT count(*) FROM events WHERE `+column+` = $1 AND event_type = 'agent.tool.dude' AND payload->>'tool' = 'github_check_log'`, scope); n != 1 {
				t.Fatalf("search recorded %d calls, want one", n)
			}
			if n := w.count(`SELECT count(*) FROM events WHERE `+column+` = $1 AND (payload::text LIKE '%' || $2 || '%' OR payload::text LIKE '%' || $3 || '%')`,
				scope, "replaces_nul_in_event_metadata", "forwarded_without_creating_a_goal"); n != 0 {
				t.Fatalf("the search's result reached %d events of the scope", n)
			}
			if n := w.count(`SELECT count(*) FROM events WHERE `+column+` = $1 AND event_type = 'agent.tool.attempted' AND payload->>'tool' = 'github_check_log'`, scope); n != 1 {
				t.Fatalf("search counted %d attempts, want one", n)
			}
			var payload string
			if err := w.owner.QueryRow(context.Background(), `SELECT payload::text FROM events WHERE `+column+` = $1 AND event_type = 'github.read'`, scope).Scan(&payload); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(payload, proof) || !strings.Contains(payload, `"context": 2`) || !strings.Contains(payload, `"match": "test_goal.*nul.*PASSED"`) {
				t.Fatalf("search ledger: %s", payload)
			}
			// Omitting match still uses the original failure excerpt, not
			// numbered search output or a synthetic empty search result.
			out = read("github_check_log", `{"repo":"acme/target","checkId":50000000077}`)
			text, _ = quoted(out["log"].(string))
			if strings.Contains(text, proof) || out["matches"] != nil || !strings.Contains(text, "cleanup") {
				t.Fatalf("no-match behaviour changed: %v", out)
			}
			out = read("github_check_log", `{"repo":"acme/target","checkId":50000000077,"match":"test_never_ran","context":0}`)
			if out["matches"] != float64(0) || out["log"] != nil || out["found"] != "no line of the 2303 read matched" {
				t.Fatalf("no match: %v", out)
			}
		})
	}
}

// Invalid search parameters are refused before reaching GitHub, with a
// usable explanation, not a silent fallback to an unsearched excerpt.
func TestGitHubCheckLogSearchParametersAreChecked(t *testing.T) {
	w, task := conductingWithAnotherProject(t)
	before := w.requests()
	for _, tc := range []struct{ extra, want string }{
		{`,"match":"test_goal("`, "match is not a valid regular expression (RE2): missing closing )"},
		{`,"match":"` + strings.Repeat("x", 201) + `"`, "match: a regular expression (RE2, case-insensitive) of 1 to 200 characters"},
		{`,"match":" "`, "match: a regular expression"},
		{`,"match":"PASSED","context":-1`, "context: 0 to 5 lines"},
		{`,"match":"PASSED","context":6`, "context: 0 to 5 lines"},
		{`,"context":0`, "given only with match"},
		{`,"match":"[a-z]{200}q"`, "match would take too long to search a whole log: it repeats a part 200 times. Repeat a part at most 50 times"},
	} {
		w.refused(task, "github_check_log", fmt.Sprintf(`{"repo":"acme/target","checkId":50000000077%s}`, tc.extra), tc.want)
	}
	if w.requests() != before {
		t.Fatal("invalid search reached GitHub")
	}
	// Each bound itself is accepted: context 5, and a match of exactly
	// 200 characters.
	for _, extra := range []string{`,"match":"PASSED","context":5`, `,"match":"` + strings.Repeat("x", 200) + `"`} {
		w.must(task, "github_check_log", fmt.Sprintf(`{"repo":"acme/target","checkId":50000000077%s}`, extra))
	}
}

// A search with more matches than one read shows is cut by the tool's
// budget: as many as fit in 10 KiB of log text, saying how many of all.
func TestGitHubCheckLogSearchStaysWithinItsBudget(t *testing.T) {
	w, task := conductingWithAnotherProject(t)
	downloads := httptest.NewServer(w.gh.LogDownloads())
	t.Cleanup(downloads.Close)
	w.gh.Set(func(s *fakegithub.Server) {
		s.CheckRuns = map[int64]fakegithub.CheckRun{50000000077: {App: "github-actions"}}
	})
	var b strings.Builder
	for i := range 5000 {
		fmt.Fprintf(&b, "tests/test_x.py::test_%04d PASSED\n", i)
	}
	w.gh.SetJobLog(50000000077, b.String(), downloads.URL)
	out := w.must(task, "github_check_log", `{"repo":"acme/target","checkId":50000000077,"match":"PASSED","context":0}`)
	log, _ := out["log"].(string)
	text, ok := quoted(log)
	shown, _ := out["shown"].(float64)
	if !ok || out["matches"] != float64(5000) || shown == 0 || shown >= 5000 || len(text) > 10<<10 ||
		!strings.HasSuffix(log, "⟧") || strings.Count(text, "PASSED") != int(shown) {
		t.Fatalf("shown %v of %v, %d bytes of log: %.300v", out["shown"], out["matches"], len(text), out)
	}
	if found, _ := out["found"].(string); !strings.HasPrefix(found, fmt.Sprintf("5000 lines of 5000 matched; the first %d are shown", int(shown))) {
		t.Fatalf("found: %q", found)
	}
}

// A check with no log to search is not searched, and says so: what its
// check run reported is the log, and there are no counts, as nothing was
// counted. Both a check that is not an Actions job and an Actions job
// whose log GitHub no longer gives.
func TestGitHubCheckLogSearchOfACheckWithoutALogSaysNotSearched(t *testing.T) {
	w, task := conductingWithAnotherProject(t)
	downloads := httptest.NewServer(w.gh.LogDownloads())
	t.Cleanup(downloads.Close)
	const notSearched = "not searched: this check is not a GitHub Actions job, or GitHub gave no log; what its check run reported is shown instead"
	w.gh.Set(func(s *fakegithub.Server) {
		s.CheckRuns = map[int64]fakegithub.CheckRun{
			50000000077: {App: "circleci", Title: "3 tests failed", Summary: "test_goal_x FAILED"},
			50000000078: {App: "github-actions", Title: "Job summary", Summary: "the job's own summary"},
		}
	})
	w.gh.SetJobLog(50000000077, "test_goal_x PASSED\n", downloads.URL)
	w.gh.SetJobLog(50000000078, "", downloads.URL) // expired: 410
	for _, tc := range []struct {
		id         int64
		log, extra string
	}{
		{50000000077, "3 tests failed\ntest_goal_x FAILED", ""},
		{50000000078, "Job summary\nthe job's own summary", "the log has expired"},
	} {
		out := w.must(task, "github_check_log", fmt.Sprintf(`{"repo":"acme/target","checkId":%d,"match":"test_goal"}`, tc.id))
		text, ok := quoted(field(out, "log").(string))
		_, matches := out["matches"]
		_, shown := out["shown"]
		if out["found"] != notSearched || !ok || text != tc.log || matches || shown || out["source"] != "check_run" ||
			(tc.extra != "" && out["unread"] != tc.extra) {
			t.Fatalf("%d: %v", tc.id, out)
		}
	}
}
