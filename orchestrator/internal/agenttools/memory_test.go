package agenttools_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/agenttools"
	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/embeddings"
	"github.com/marciomartins/dude/orchestrator/internal/memory"
)

// raw calls a tool over the CLI's JSON API and returns the body as text.
func (f *fixture) raw(t *testing.T, token, tool, body string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest("POST", f.url+"/tools/"+tool, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

func TestAnAgentRemembersAndAnotherFindsIt(t *testing.T) {
	f := setup(t)
	token := f.run(t, "run_mem", "implementer", "running")

	status, body := f.raw(t, token, "remember", `{"title": "GitHub redelivers a webhook for 3 days",
		"content": "Dedupe on X-GitHub-Delivery, not the payload.", "about": ["TEXT-1", "text utilities"]}`)
	if status != 200 {
		t.Fatalf("remember: %d %s", status, body)
	}
	var saved struct{ ID string }
	_ = json.Unmarshal([]byte(body), &saved)

	var kind, run, source string
	var refs int
	if err := f.owner.QueryRow(context.Background(), `SELECT m.author_kind, m.created_by_run_id, m.source_id,
		(SELECT count(*) FROM memory_refs r WHERE r.memory_id = m.id) FROM memories m WHERE m.id = $1`, saved.ID).
		Scan(&kind, &run, &source, &refs); err != nil {
		t.Fatal(err)
	}
	if kind != "agent" || run != "run_mem" || source != f.item || refs != 2 {
		t.Errorf("saved as %s by %s, learned on %s, about %d things", kind, run, source, refs)
	}

	status, body = f.raw(t, token, "search_memory", `{"query": "webhook redelivery"}`)
	if status != 200 || !strings.Contains(body, saved.ID) {
		t.Fatalf("search_memory: %d %s", status, body)
	}
	status, body = f.raw(t, token, "search_memory", `{"query": "truncate"}`)
	if status != 200 || !strings.Contains(body, `"key":"TEXT-1"`) || !strings.Contains(body, `"title":"Truncate long words"`) {
		t.Errorf("a task is found with its key apart from its title: %s", body)
	}
	status, body = f.raw(t, token, "get_memory", `{"id": "`+saved.ID+`"}`)
	if status != 200 || !strings.Contains(body, "X-GitHub-Delivery") || !strings.Contains(body, `"label":"TEXT-1"`) {
		t.Errorf("get_memory: %d %s", status, body)
	}
}

func TestRememberRefusesWhatItCannotPlace(t *testing.T) {
	f := setup(t)
	token := f.run(t, "run_mem", "implementer", "running")
	for args, want := range map[string]string{
		`{"title": "", "content": "x"}`:                         "title is required",
		`{"title": "t", "content": "x", "about": ["NOPE-9"]}`:   "no task",
		`{"title": "t", "content": "x", "scope": "everywhere"}`: "scope is",
		`{"title": "t", "content": "x", "kind": "rumour"}`:      "kind is",
	} {
		status, body := f.raw(t, token, "remember", args)
		if status != 422 || !strings.Contains(body, want) {
			t.Errorf("%s: %d %s, want a refusal saying %q", args, status, body, want)
		}
	}
}

func TestAnotherProjectsMemoryIsNotARunsToRead(t *testing.T) {
	f := setup(t)
	token := f.run(t, "run_mem", "implementer", "running")
	mustExec(t, f.owner, `INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_x', $1, 'X', 'x', 'X')`, f.org)
	mustExec(t, f.owner, `INSERT INTO memories (id, organization_id, project_id, title, content, author_kind, system_reason)
		VALUES ('mem_x', $1, 'prj_x', 'Elsewhere webhook note', 'webhook', 'system', 'test')`, f.org)

	if status, body := f.raw(t, token, "get_memory", `{"id": "mem_x"}`); status != 422 {
		t.Errorf("read another project's memory: %d %s", status, body)
	}
	if _, body := f.raw(t, token, "search_memory", `{"query": "webhook"}`); strings.Contains(body, "mem_x") {
		t.Errorf("search found another project's memory: %s", body)
	}
}

func TestSearchByMeaningWhenTheServerHasAnEmbedder(t *testing.T) {
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	mustExec(t, owner, `INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_e', $1, 'P', 'p', 'P')`, org)
	mustExec(t, owner, `INSERT INTO tasks (id, organization_id, project_id, number, title, status) VALUES ('wi_e', $1, 'prj_e', 1, 'Anything', 'running')`, org)
	mustExec(t, owner, `INSERT INTO memories (id, organization_id, project_id, title, content, author_kind, system_reason)
		VALUES ('mem_e', $1, 'prj_e', 'Cursor pagination', 'The billing API pages by cursor.', 'system', 'test')`, org)
	fake := &embeddings.Fake{Dims: 768}
	for {
		n, err := (&memory.Indexer{DB: app, Embedder: fake}).Sweep(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			break
		}
	}
	srv := httptest.NewServer((&agenttools.Server{DB: app, Embedder: fake}).Handler())
	defer srv.Close()
	f := &fixture{owner: owner, org: org, project: "prj_e", item: "wi_e", url: srv.URL}
	token := f.run(t, "run_e", "reviewer", "running")
	// The tool searches by meaning when the server has an embedder: the
	// query is embedded once.
	calls := fake.Calls
	status, body := f.raw(t, token, "search_memory", `{"query": "billing cursor", "types": ["memory"]}`)
	if fake.Calls != calls+1 {
		t.Errorf("the query was not embedded: %d calls", fake.Calls-calls)
	}
	if status != 200 || !strings.Contains(body, "mem_e") {
		t.Errorf("search_memory with an embedder: %d %s", status, body)
	}
}
