package memory_test

import (
	"os"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/embeddings"
	"github.com/marciomartins/dude/orchestrator/internal/memory"
)

// Against the real embedder (llm-proxy, gemini-embedding-2): only when
// DUDE_EMBEDDINGS_URL and DUDE_EMBEDDINGS_KEY are set, as the release
// smoke would set them. It proves what the fake cannot: that a query sharing
// no word with a memory finds it by meaning.
func TestRealEmbedderFindsByMeaning(t *testing.T) {
	url, key := os.Getenv("DUDE_EMBEDDINGS_URL"), os.Getenv("DUDE_EMBEDDINGS_KEY")
	if url == "" || key == "" {
		t.Skip("DUDE_EMBEDDINGS_URL / DUDE_EMBEDDINGS_KEY not set")
	}
	model := os.Getenv("DUDE_EMBEDDINGS_MODEL")
	if model == "" {
		model = "gemini-embedding-2"
	}
	e := &embeddings.Client{BaseURL: url, Key: key, ModelName: model, Dims: 768}

	app, owner := dbtest.Open(t)
	org, project, _, _ := seed(t, owner)
	remember(t, owner, org, "mem_pages", project, "The billing API paginates by cursor",
		"Never use offset pagination against billing: pass the next_cursor from the last response.")
	remember(t, owner, org, "mem_fonts", project, "Fonts are bundled with the web app",
		"The transcript page loads no webfont; the files ship in the build.")
	x := &memory.Indexer{DB: app, Embedder: e}
	drain(t, x)
	if h := x.Health(); h.Error != "" {
		t.Fatalf("the embedder failed: %s", h.Error)
	}

	// No word in common with either memory's text (after stop words).
	out := search(t, app, org, e, memory.Query{Text: "getting more invoices after the first batch", Project: project, Types: []string{"memory"}})
	if out.Mode != "hybrid" {
		t.Fatalf("mode %s (%s)", out.Mode, out.Degraded)
	}
	if len(out.Results) == 0 || out.Results[0].ID != "mem_pages" || out.Results[0].VectorRank != 1 {
		t.Fatalf("by meaning, found %+v", out.Results)
	}
	if out.Results[0].TextRank != 0 {
		t.Errorf("the query was meant to share no words, but words found it too")
	}
	// A tie between two documents each first in one list goes to meaning.
	tie := search(t, app, org, e, memory.Query{Text: "how do I fetch the next page of invoices", Project: project, Types: []string{"memory"}})
	if len(tie.Results) == 0 || tie.Results[0].ID != "mem_pages" {
		t.Errorf("a tie was not settled by meaning: %+v", tie.Results)
	}
}
