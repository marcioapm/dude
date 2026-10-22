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

	page, _ := guide.PageBy("delivery")
	want, _ := page.Section("which-reviewers-run")
	wantID, wantTitle := "doc:delivery#which-reviewers-run", page.Title+" — "+want.Heading
	wantLink := "https://dude.example/#/o/" + slug + "/help/delivery/which-reviewers-run"
	for _, args := range []string{`{"query": "delivery policy reviewers"}`, `{"query": "delivery policy reviewers", "types": ["doc"]}`} {
		status, body := f.raw(t, token, "search_memory", args)
		if status != 200 {
			t.Fatalf("%s: %d %s", args, status, body)
		}
		var doc *docHit
		for _, h := range hits(t, body) {
			if h.ID == wantID {
				doc = &h
			}
		}
		if doc == nil || doc.Type != "doc" || doc.Title != wantTitle || doc.Snippet == "" || doc.Link != wantLink {
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

	status, body = f.raw(t, token, "get_memory", `{"id": "`+wantID+`"}`)
	var got struct{ ID, Type, Title, Text, Link string }
	_ = json.Unmarshal([]byte(body), &got)
	if status != 200 || got.ID != wantID || got.Type != "doc" || got.Title != wantTitle ||
		got.Text != guide.Resolve(want.Text, "https://dude.example", slug) || got.Link != wantLink || strings.Contains(body, "⟦external") {
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

func TestGuideCrossLinksInAgentOutputs(t *testing.T) {
	for _, base := range []string{"", "https://dude.example"} {
		t.Run(base, func(t *testing.T) {
			f := setupWith(t, &agenttools.Server{PublicURL: base})
			p, err := guide.Parse("overview", []byte("---\ntitle: Overview\n---\n## Links\nCrosslinks [memory](help:memory#searching) and [intro](help:overview#)."))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := guide.Sync(context.Background(), f.app, "test", []guide.Page{p}); err != nil {
				t.Fatal(err)
			}
			slug := orgSlug(t, f)
			token := f.session(t, "ssn_links", "", "")
			status, body := f.raw(t, token, "search_memory", `{"query":"Crosslinks", "types":["doc"]}`)
			if status != 200 {
				t.Fatalf("search: %d %s", status, body)
			}
			found := hits(t, body)
			if len(found) != 1 || found[0].ID != "doc:overview#links" || found[0].Title != "Overview — Links" ||
				found[0].Link != guide.Link(base, slug, "overview", "links") ||
				!strings.Contains(found[0].Snippet, "]("+guide.Link(base, slug, "memory", "searching")+")") ||
				strings.Contains(found[0].Snippet, "](help:") {
				t.Fatalf("search links: %+v", found)
			}
			status, body = f.raw(t, token, "get_memory", `{"id":"doc:overview#links"}`)
			var got struct{ ID, Title, Text, Link string }
			if err := json.Unmarshal([]byte(body), &got); err != nil {
				t.Fatal(err)
			}
			if status != 200 || got.ID != found[0].ID || got.Title != found[0].Title || got.Link != found[0].Link ||
				got.Text != guide.Resolve(p.Sections[0].Text, base, slug) || strings.Contains(got.Text, "](help:") {
				t.Fatalf("get links: %d %+v", status, got)
			}
		})
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
		page, anchor, ok := strings.Cut(strings.TrimPrefix(h.ID, "doc:"), "#")
		if h.Type == "doc" && ok {
			found = true
			if want := guide.Link("", slug, page, anchor); h.Link != want {
				t.Errorf("%s linked %q, want %q", h.ID, h.Link, want)
			}
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
