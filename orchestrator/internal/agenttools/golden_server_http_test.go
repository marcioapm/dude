package agenttools

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

// What the tool server's HTTP layer answers a client it cannot serve: no
// token, a token of no live Run, a lookup that fails, and a co-author read
// that fails.
func TestGoldenServerHTTPErrors(t *testing.T) {
	ctx := context.Background()
	if *updateGoldens {
		if err := dbtest.Reachable(ctx); err != nil {
			t.Fatalf("-update needs the test database: %v", err)
		}
	}
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	var b strings.Builder
	say := func(name string, res *http.Response) {
		t.Helper()
		defer res.Body.Close()
		body, _ := io.ReadAll(res.Body)
		fmt.Fprintf(&b, "## %s\n\n%d %s", name, res.StatusCode, body)
		b.WriteString("\n")
	}
	get := func(url, token string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest("GET", url+"/tools", nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}

	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	token, hash := NewToken()
	for _, q := range []string{
		`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_'||$1, $1, 'P', 'prj_'||$1, 'P')`,
		`INSERT INTO tasks (id, organization_id, project_id, number, title) VALUES ('wi_'||$1, $1, 'prj_'||$1, 1, 'T')`,
		`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, mcp_token_hash)
			VALUES ('run_'||$1, $1, 'prj_'||$1, 'wi_'||$1, 1, 'completed', 'implement', 'implementer', $2)`,
	} {
		args := []any{org}
		if strings.Contains(q, "$2") {
			args = append(args, hash)
		}
		if _, err := owner.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	live := httptest.NewServer((&Server{DB: app, Log: quiet}).Handler())
	t.Cleanup(live.Close)
	say("no-token", get(live.URL, ""))
	say("unknown-token", get(live.URL, "dude_run_nobody"))
	say("ended-run", get(live.URL, token))

	pool, err := pgxpool.New(ctx, "postgres://dude:dude@127.0.0.1:1/none?connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	down := httptest.NewServer((&Server{DB: &db.DB{Pool: pool}, Log: quiet}).Handler())
	t.Cleanup(down.Close)
	say("lookup-failed", get(down.URL, "dude_run_any"))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/git/coauthors", nil)
	(&Server{DB: &db.DB{Pool: pool}, Log: quiet}).coauthors(rec, req.WithContext(context.WithValue(req.Context(), callerKey{}, Caller{TaskID: "wi_x"})))
	say("coauthors-failed", rec.Result())
	golden(t, "results-server-http", b.String())
}
