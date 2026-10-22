package agenttools_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/agenttools"
	"github.com/marciomartins/dude/orchestrator/internal/guide"
)

type docHit struct {
	Type, ID, Title, Snippet, Link string
}

func hits(t *testing.T, body string) []docHit {
	t.Helper()
	var out []docHit
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	return out
}

func orgSlug(t *testing.T, f *fixture) string {
	t.Helper()
	var slug string
	if err := f.owner.QueryRow(context.Background(), `SELECT slug FROM organizations WHERE id = $1`, f.org).Scan(&slug); err != nil {
		t.Fatal(err)
	}
	return slug
}

// search_memory finds dude's guide by default and with types ["doc"],
// titled page — section, with the link to it on the deployment's origin;
// get_memory reads the section whole, as dude's text (not quoted as
// external text).
func TestAnAgentFindsAndReadsTheGuide(t *testing.T) {
	f := setupWith(t, &agenttools.Server{PublicURL: "https://dude.example"})
	if _, err := guide.SyncBuiltIn(context.Background(), f.app, "test"); err != nil {
		t.Fatal(err)
	}
	slug := orgSlug(t, f)
	mustExec(t, f.owner, `INSERT INTO memories (id, organization_id, project_id, title, content, author_kind, system_reason)
		VALUES ('mem_pol', $1, NULL, 'Our delivery policy', 'We review with two reviewers.', 'system', 'test')`, f.org)
	token := f.run(t, "run_doc", "implementer", "running")

	for _, args := range []string{`{"query": "delivery policy reviewers"}`, `{"query": "delivery policy reviewers", "types": ["doc"]}`} {
		status, body := f.raw(t, token, "search_memory", args)
		if status != 200 {
			t.Fatalf("%s: %d %s", args, status, body)
		}
		var doc *docHit
		for _, h := range hits(t, body) {
			if h.ID == "doc:settings#delivery" {
				doc = &h
			}
		}
		if doc == nil || doc.Type != "doc" || doc.Title != "Settings — Delivery" || doc.Snippet == "" ||
			doc.Link != "https://dude.example/#/o/"+slug+"/help/settings/delivery" {
			t.Fatalf("%s: the delivery section as %+v in %s", args, doc, body)
		}
		if strings.Contains(body, "⟦external") {
			t.Errorf("%s: the guide was quoted as external text: %s", args, body)
		}
	}
	status, body := f.raw(t, token, "search_memory", `{"query": "delivery policy reviewers", "types": ["doc"]}`)
	if status != 200 || strings.Contains(body, "mem_pol") {
		t.Errorf("types doc found a memory: %s", body)
	}

	status, body = f.raw(t, token, "get_memory", `{"id": "doc:settings#delivery"}`)
	var got struct{ ID, Type, Title, Text, Link string }
	_ = json.Unmarshal([]byte(body), &got)
	p, _ := guide.PageBy("settings")
	want, _ := p.Section("delivery")
	if status != 200 || got.ID != "doc:settings#delivery" || got.Type != "doc" || got.Title != "Settings — Delivery" ||
		got.Text != want.Text || got.Link != "https://dude.example/#/o/"+slug+"/help/settings/delivery" || strings.Contains(body, "⟦external") {
		t.Errorf("get_memory doc: %d %s", status, body)
	}
	// The intro's anchor is empty: the page's link alone.
	if status, body = f.raw(t, token, "get_memory", `{"id": "doc:settings#"}`); status != 200 ||
		!strings.Contains(body, `"link":"https://dude.example/#/o/`+slug+`/help/settings"`) {
		t.Errorf("get_memory intro: %d %s", status, body)
	}
	for _, id := range []string{"doc:settings#nope", "doc:nope#", "doc:"} {
		if status, body := f.raw(t, token, "get_memory", `{"id": "`+id+`"}`); status != 422 || !strings.Contains(body, "no section "+id+" in dude's guide") {
			t.Errorf("get_memory %s: %d %s", id, status, body)
		}
	}
	if status, body := f.raw(t, token, "search_memory", `{"query": "x", "types": ["docs"]}`); status != 422 || !strings.Contains(body, "and doc") {
		t.Errorf("bad type: %d %s", status, body)
	}
}

// Without a public URL the link is the web app's hash path; a session's
// agent finds and reads the guide too, linked project or none.
func TestASessionsAgentFindsTheGuideWithARelativeLink(t *testing.T) {
	f := setup(t)
	if _, err := guide.SyncBuiltIn(context.Background(), f.app, "test"); err != nil {
		t.Fatal(err)
	}
	slug := orgSlug(t, f)
	token := f.session(t, "ssn_doc", "", "")
	status, body := f.raw(t, token, "search_memory", `{"query": "embedding model dimensions", "types": ["doc"]}`)
	if status != 200 {
		t.Fatalf("%d %s", status, body)
	}
	found := false
	for _, h := range hits(t, body) {
		if h.ID == "doc:settings#memory" {
			found = h.Link == "#/o/"+slug+"/help/settings/memory"
		}
	}
	if !found {
		t.Errorf("session search: %s", body)
	}
	if status, body := f.raw(t, token, "get_memory", `{"id": "doc:memory#searching"}`); status != 200 || !strings.Contains(body, `"link":"#/o/`+slug+`/help/memory/searching"`) {
		t.Errorf("session get_memory: %d %s", status, body)
	}
	if status, body := f.raw(t, token, "get_memory", `{"id": "doc:memory#nope"}`); status != 422 {
		t.Errorf("session get_memory unknown: %d %s", status, body)
	}
}
