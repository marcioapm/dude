package agenttools_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/agenttools"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

type forgesOf struct{ gh *forge.GitHub }

func (f forgesOf) For(context.Context, string) (*forge.GitHub, error) { return f.gh, nil }

// Each read GitHub would not answer is recorded as a github.read event
// saying why in dude's words — the kind and the HTTP status — and nothing
// GitHub wrote: an operator sees why an agent keeps failing.
func TestARefusedReadIsRecordedWithItsKindAndNoGitHubText(t *testing.T) {
	const canary = "private-github-refusal-canary"
	reset := strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		message := `{"message":"` + canary + `","documentation_url":"https://docs.github.test/` + canary + `"}`
		switch r.URL.Path {
		case "/repos/acme/target/issues/1", "/repos/acme/target":
			// A renamed repository: its own path redirects too.
			w.Header().Set("Location", "/repositories/42/"+canary)
			w.WriteHeader(http.StatusMovedPermanently)
			_, _ = w.Write([]byte(message))
		case "/repos/acme/target/issues/2":
			w.Header().Set("Location", "/elsewhere/"+canary)
			w.WriteHeader(http.StatusFound)
			_, _ = w.Write([]byte(message))
		case "/repos/acme/target/issues/3":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(message))
		case "/repos/acme/target/issues/4":
			_, _ = w.Write([]byte(`{"number":4,"title":"` + canary + `","body":"` + strings.Repeat("x", 5<<20) + `"}`))
		case "/repos/acme/target/issues/5":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(message))
		case "/repos/acme/target/issues/6":
			// The answer that leaves a fifth of the limit: its comments are
			// delivery's.
			w.Header().Set("X-RateLimit-Remaining", "20")
			w.Header().Set("X-RateLimit-Limit", "100")
			w.Header().Set("X-RateLimit-Reset", reset)
			_, _ = w.Write([]byte(`{"number":6,"title":"` + canary + `","comments":3}`))
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(http.StatusTeapot)
		}
	}))
	t.Cleanup(srv.Close)
	gh := forge.NewGitHub(forge.Credential{Auth: "pat", Secret: "ghp_refused_" + strconv.FormatInt(time.Now().UnixNano(), 10), APIBaseURL: srv.URL})
	f := setupWith(t, &agenttools.Server{Forges: forgesOf{gh}})
	mustExec(t, f.owner, `INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch)
		VALUES ($1, $2, $3, 'target', 'https://github.com/acme/target.git', 'main')`, "repo_"+f.org, f.org, f.project)
	token := f.conductor(t)
	want := []struct {
		number int
		kind   string
		status int
	}{
		{1, "moved", 301}, {2, "redirect", 302}, {3, "not_found", 404}, {4, "too_large", 413}, {5, "forbidden", 403},
		{6, "rate_reserve", 429},
	}
	for _, w := range want {
		if status, out := f.post(t, token, "github_issue", fmt.Sprintf(`{"repo":"acme/target","number":%d}`, w.number)); status != 422 {
			t.Errorf("issue %d: %d %v, want refused", w.number, status, out)
		}
	}
	rows, err := f.owner.Query(context.Background(), `SELECT payload FROM events WHERE run_id = 'run_cond' AND event_type = 'github.read'
		ORDER BY cursor`)
	if err != nil {
		t.Fatal(err)
	}
	var got []map[string]any
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var p map[string]any
		_ = json.Unmarshal(raw, &p)
		got = append(got, p)
	}
	if len(got) != len(want) {
		t.Fatalf("%d github.read events, want %d: %v", len(got), len(want), got)
	}
	for i, w := range want {
		p := got[i]
		if p["tool"] != "github_issue" || p["repo"] != "acme/target" || p["number"] != float64(w.number) || p["refused"] != w.kind ||
			p["status"] != float64(w.status) || len(p) != 5 {
			t.Errorf("issue %d recorded as %v, want refused %s status %d", w.number, p, w.kind, w.status)
		}
	}
	if n := f.count(t, `SELECT count(*) FROM events WHERE organization_id = $1 AND payload::text LIKE '%' || $2 || '%'`, f.org, canary); n != 0 {
		t.Errorf("GitHub's text is in %d events", n)
	}
}
