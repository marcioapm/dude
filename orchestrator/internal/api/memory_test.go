package api_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/api"
	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/embeddings"
)

type memoryAPI struct {
	url, org string
	owner    *pgx.Conn
}

func (m memoryAPI) do(t *testing.T, method, path, body string, headers ...string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(method, m.url+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer svc")
	req.Header.Set("X-Dude-Organization", m.org)
	req.Header.Set("X-Dude-Actor", "key_1")
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	if body != "" {
		req.ContentLength = int64(len(body))
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return res.StatusCode, out
}

func setupMemory(t *testing.T) memoryAPI {
	t.Helper()
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	for _, q := range []string{
		`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_m', $1, 'control-plane', 'cp', 'CP')`,
		`INSERT INTO tasks (id, organization_id, project_id, number, title, status) VALUES ('wi_m', $1, 'prj_m', 1, 'Dedupe deliveries', 'running')`,
		`INSERT INTO people (id, organization_id, name) VALUES ('per_ana', $1, 'Ana Ribeiro')`,
		`INSERT INTO people (id, organization_id, name) VALUES ('per_bo', $1, 'Bo Lind')`,
	} {
		if _, err := owner.Exec(context.Background(), q, org); err != nil {
			t.Fatal(err)
		}
	}
	srv := httptest.NewServer((&api.Server{DB: app, Token: "svc", Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Embedder: &embeddings.Fake{Dims: 768}}).Handler())
	t.Cleanup(srv.Close)
	return memoryAPI{url: srv.URL, org: org, owner: owner}
}

func TestAPersonAddsAMemoryAndOnlyTheyOrAnAdminChangeIt(t *testing.T) {
	m := setupMemory(t)
	status, created := m.do(t, "POST", "/internal/memory/memories",
		`{"projectId": "prj_m", "title": "Run tests on a throwaway database", "content": "docker run …", "kind": "procedure",
		  "about": [{"type": "task", "id": "wi_m"}]}`, "X-Dude-Person", "per_ana")
	if status != 201 {
		t.Fatalf("create: %d %v", status, created)
	}
	id := created["id"].(string)
	author := created["author"].(map[string]any)
	if author["kind"] != "person" || author["personName"] != "Ana Ribeiro" {
		t.Errorf("author = %v", author)
	}
	if about := created["about"].([]any); len(about) != 1 || about[0].(map[string]any)["label"] != "CP-1" {
		t.Errorf("about = %v", created["about"])
	}

	if status, _ := m.do(t, "PATCH", "/internal/memory/memories/"+id, `{"title": "Bo's edit"}`, "X-Dude-Person", "per_bo"); status != 403 {
		t.Errorf("another member edited Ana's memory: %d", status)
	}
	if status, out := m.do(t, "PATCH", "/internal/memory/memories/"+id, `{"title": "Ana's edit"}`, "X-Dude-Person", "per_ana"); status != 200 || out["title"] != "Ana's edit" {
		t.Errorf("Ana's own edit: %d %v", status, out)
	}
	if status, out := m.do(t, "POST", "/internal/memory/memories/"+id+"/archive", "", "X-Dude-Person", "per_bo", "X-Dude-Admin", "true"); status != 200 || out["archivedAt"] == nil {
		t.Errorf("an admin's archive: %d %v", status, out)
	}
	if _, out := m.do(t, "GET", "/internal/memory/memories?project=prj_m", ""); len(out["memories"].([]any)) != 0 {
		t.Errorf("an archived memory is listed without asking: %v", out)
	}
	if _, out := m.do(t, "GET", "/internal/memory/memories?project=prj_m&archived=true", ""); len(out["memories"].([]any)) != 1 {
		t.Errorf("an archived memory is not listed when asked: %v", out)
	}
}

func TestTheSearchPageSeesKeysScoresAndHowItSearched(t *testing.T) {
	m := setupMemory(t)
	status, out := m.do(t, "GET", "/internal/memory/search?q=dedupe&project=prj_m", "")
	if status != 200 {
		t.Fatalf("search: %d %v", status, out)
	}
	results := out["results"].([]any)
	if len(results) == 0 {
		t.Fatalf("nothing found: %v", out)
	}
	first := results[0].(map[string]any)
	if first["key"] != "CP-1" || first["title"] != "Dedupe deliveries" || first["status"] != "running" || first["textRank"] != float64(1) {
		t.Errorf("first result = %v", first)
	}
	// Nothing is embedded yet: the query is, the documents are not.
	if out["mode"] != "hybrid" || first["embedded"] != false {
		t.Errorf("mode %v, embedded %v", out["mode"], first["embedded"])
	}
}

func TestInvalidMemoriesAreRefusedWithAReason(t *testing.T) {
	m := setupMemory(t)
	for body, want := range map[string]string{
		`{"title": "", "content": "x"}`:                                         "title is required",
		`{"title": "t", "content": "x", "kind": "rumour"}`:                      "kind is",
		`{"title": "t", "content": "x", "projectId": "prj_nope"}`:               "no project",
		`{"title": "t", "content": "x", "about": [{"type": "run", "id": "x"}]}`: "not a task",
	} {
		status, out := m.do(t, "POST", "/internal/memory/memories", body, "X-Dude-Person", "per_ana")
		msg, _ := out["error"].(map[string]any)["message"].(string)
		if status != 422 || !strings.Contains(msg, want) {
			t.Errorf("%s: %d %v, want %q", body, status, out, want)
		}
	}
}

func TestOnlyAnAdminReindexes(t *testing.T) {
	m := setupMemory(t)
	if status, _ := m.do(t, "POST", "/internal/memory/index/reindex", ""); status != 403 {
		t.Errorf("a member reindexed: %d", status)
	}
	status, out := m.do(t, "POST", "/internal/memory/index/reindex", "", "X-Dude-Admin", "true")
	if status != 200 || out["due"] == nil {
		t.Errorf("reindex: %d %v", status, out)
	}
	status, out = m.do(t, "GET", "/internal/memory/index", "")
	if status != 200 || out["model"] != "fake" || len(out["kinds"].([]any)) != 4 {
		t.Errorf("index: %d %v", status, out)
	}
}
