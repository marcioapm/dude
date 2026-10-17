package agenttools_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
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
		case "/repos/acme/target/issues/7":
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(message))
		case "/repos/acme/target/issues/8":
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(message))
		case "/repos/acme/target/issues/9":
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(message))
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
	// rate_reserve last: it leaves the token at delivery's reserve.
	want := []struct {
		number int
		kind   string
		status int
	}{
		{1, "moved", 301}, {2, "redirect", 302}, {3, "not_found", 404}, {4, "too_large", 413}, {5, "forbidden", 403},
		{7, "rate_limited", 429}, {8, "unavailable", 502}, {9, "refused", 422}, {6, "rate_reserve", 429},
	}
	for _, w := range want {
		status, out := f.post(t, token, "github_issue", fmt.Sprintf(`{"repo":"acme/target","number":%d}`, w.number))
		if status != 422 {
			t.Errorf("issue %d: %d %v, want refused", w.number, status, out)
		}
		// What the agent is told is dude's: nothing GitHub wrote.
		if told, _ := json.Marshal(out); strings.Contains(string(told), canary) || strings.Contains(string(told), "docs.github.test") {
			t.Errorf("issue %d: GitHub's text reached the agent: %s", w.number, told)
		}
	}
	payloads := func(sql string, args ...any) []map[string]any {
		t.Helper()
		rows, err := f.owner.Query(context.Background(), sql, args...)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var got []map[string]any
		for rows.Next() {
			var raw []byte
			if err := rows.Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var p map[string]any
			if err := json.Unmarshal(raw, &p); err != nil {
				t.Fatal(err)
			}
			got = append(got, p)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return got
	}
	got := payloads(`SELECT payload FROM events WHERE run_id = 'run_cond' AND event_type = 'github.read' ORDER BY cursor`)
	if len(got) != len(want) {
		t.Fatalf("%d github.read events, want %d: %v", len(got), len(want), got)
	}
	for i, w := range want {
		p := got[i]
		for k, v := range map[string]any{"tool": "github_issue", "repo": "acme/target", "number": float64(w.number),
			"refused": w.kind, "status": float64(w.status)} {
			if p[k] != v {
				t.Errorf("issue %d: %s = %v, want %v", w.number, k, p[k], v)
			}
		}
		if _, ok := p["bytes"]; ok {
			t.Errorf("issue %d: a refused read has bytes: %v", w.number, p)
		}
	}
	// Every event of the organisation, decoded: none holds GitHub's text.
	for _, p := range payloads(`SELECT payload FROM events WHERE organization_id = $1`, f.org) {
		if holds(p, canary) {
			t.Errorf("GitHub's text is in an event: %v", p)
		}
	}
}

// holds says a decoded JSON value has s in any of its keys or strings.
func holds(v any, s string) bool {
	switch x := v.(type) {
	case string:
		return strings.Contains(x, s)
	case []any:
		return slices.ContainsFunc(x, func(item any) bool { return holds(item, s) })
	case map[string]any:
		for k, item := range x {
			if strings.Contains(k, s) || holds(item, s) {
				return true
			}
		}
	}
	return false
}
