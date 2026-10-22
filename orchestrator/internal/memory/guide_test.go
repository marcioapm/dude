package memory_test

import (
	"context"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/embeddings"
	"github.com/marciomartins/dude/orchestrator/internal/guide"
	"github.com/marciomartins/dude/orchestrator/internal/memory"
)

func guidePages(t *testing.T) []guide.Page {
	t.Helper()
	var out []guide.Page
	for slug, src := range map[string]string{
		"previews": "---\ntitle: Branch previews\n---\n## Waking\nA sleeping preview wakes when someone opens its address.\n",
		"parking":  "---\ntitle: Parking\n---\n## Why a Run parks\nA Run waiting on a person is paused so it holds no host.\n",
	} {
		p, err := guide.Parse(slug, []byte(src))
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}

func TestTheIndexerEmbedsTheGuideAndAgainForAnotherModel(t *testing.T) {
	app, owner := dbtest.Open(t)
	ctx := context.Background()
	if _, err := guide.Sync(ctx, app, "v1", guidePages(t)); err != nil {
		t.Fatal(err)
	}
	fake := &embeddings.Fake{Dims: 768}
	drain(t, &memory.Indexer{DB: app, Embedder: fake})
	models := func() map[string]int {
		rows, err := owner.Query(ctx, `SELECT coalesce(embedding_model, ''), count(*) FROM guide_sections GROUP BY 1`)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]int{}
		for rows.Next() {
			var m string
			var n int
			if err := rows.Scan(&m, &n); err != nil {
				t.Fatal(err)
			}
			out[m] = n
		}
		return out
	}
	if got := models(); got["fake"] != 2 || len(got) != 1 {
		t.Fatalf("after the first sweep: %v", got)
	}
	calls := fake.Calls
	drain(t, &memory.Indexer{DB: app, Embedder: fake})
	if fake.Calls != calls {
		t.Errorf("the guide was embedded again with nothing changed: %d calls", fake.Calls-calls)
	}
	drain(t, &memory.Indexer{DB: app, Embedder: &renamed{fake}})
	if got := models(); got["other"] != 2 || len(got) != 1 {
		t.Errorf("after a change of model: %v", got)
	}
}

// A refused section backs off on guide_sections as a document does on the index.
func TestARefusedGuideSectionBacksOffAlone(t *testing.T) {
	app, owner := dbtest.Open(t)
	if _, err := guide.Sync(context.Background(), app, "v1", guidePages(t)); err != nil {
		t.Fatal(err)
	}
	drain(t, &memory.Indexer{DB: app, Embedder: &permanent{&embeddings.Fake{Dims: 768, Fail: "sleeping"}}})
	var embedded, failed int
	if err := owner.QueryRow(context.Background(), `SELECT count(*) FILTER (WHERE embedding IS NOT NULL),
		count(*) FILTER (WHERE last_error IS NOT NULL AND next_attempt_at > now()) FROM guide_sections`).Scan(&embedded, &failed); err != nil {
		t.Fatal(err)
	}
	if embedded != 1 || failed != 1 {
		t.Errorf("%d embedded, %d backed off", embedded, failed)
	}
}

func TestSearchFindsTheGuideByWordsAndByMeaning(t *testing.T) {
	app, owner := dbtest.Open(t)
	ctx := context.Background()
	a, project, _, _ := seed(t, owner)
	b := dbtest.Org(t, owner)
	remember(t, owner, b, "mem_b", nil, "Previews wake", "Another organisation's note on preview waking.")
	if _, err := guide.Sync(ctx, app, "v1", guidePages(t)); err != nil {
		t.Fatal(err)
	}

	// By words, with no embedder; only when doc is asked for.
	out := search(t, app, a, nil, memory.Query{Text: "preview wakes", Project: project, Types: memory.AgentTypes})
	if len(out.Results) == 0 || out.Results[0].Type != "doc" || out.Results[0].ID != "previews#waking" ||
		out.Results[0].Title != "Branch previews — Waking" || out.Results[0].Snippet == "" {
		t.Fatalf("by words: %+v", out.Results)
	}
	for _, r := range out.Results {
		if r.ID == "mem_b" {
			t.Errorf("another organisation's memory was found: %+v", r)
		}
	}
	if got := search(t, app, a, nil, memory.Query{Text: "preview wakes", Project: project}).Results; len(got) != 0 {
		t.Errorf("the default types found the guide: %+v", got)
	}
	if got := search(t, app, a, nil, memory.Query{Text: "preview person", Types: []string{"doc"}}).Results; len(got) != 2 {
		t.Fatalf("positive terms must match both sections: %+v", got)
	}
	if got := search(t, app, a, nil, memory.Query{Text: "preview person -host", Types: []string{"doc"}}).Results; len(got) != 1 || got[0].ID != "previews#waking" {
		t.Errorf("a -word did not leave the other section out: %+v", got)
	}

	// By meaning alone: the query is English stop words, which Postgres
	// drops (no words to match), but the fake's vectors keep them, and only
	// the parking section has them.
	fake := &embeddings.Fake{Dims: 768}
	drain(t, &memory.Indexer{DB: app, Embedder: fake})
	out = search(t, app, a, fake, memory.Query{Text: "so it no is", Types: []string{"doc"}})
	if out.Mode != "hybrid" || len(out.Results) == 0 || out.Results[0].ID != "parking#why-a-run-parks" || out.Results[0].VectorRank != 1 ||
		out.Results[0].TextRank != 0 || !out.Results[0].Embedded || out.Results[0].Title != "Parking — Why a Run parks" {
		t.Errorf("by meaning: %+v", out)
	}
	if got := search(t, app, a, fake, memory.Query{Text: "so it no is -host", Types: []string{"doc"}}); got.Mode != "hybrid" || len(got.Results) != 1 || got.Results[0].ID != "previews#waking" || got.Results[0].TextRank != 0 || got.Results[0].VectorRank == 0 {
		t.Errorf("vector exclusion must leave only the other section: %+v", got)
	}
}
