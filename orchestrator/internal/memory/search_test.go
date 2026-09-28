package memory_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/embeddings"
	"github.com/marciomartins/dude/orchestrator/internal/memory"
)

func drain(t *testing.T, x *memory.Indexer) {
	t.Helper()
	for {
		n, err := x.Sweep(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			return
		}
	}
}

func search(t *testing.T, app *db.DB, org string, e embeddings.Embedder, q memory.Query) memory.Outcome {
	t.Helper()
	var out memory.Outcome
	if err := app.InOrg(context.Background(), org, func(tx pgx.Tx) error {
		var err error
		out, err = memory.Search(context.Background(), tx, e, q)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func remember(t *testing.T, owner *pgx.Conn, org, id string, project any, title, content string) {
	t.Helper()
	exec(t, owner, `INSERT INTO memories (id, organization_id, project_id, title, content, author_kind, system_reason)
		VALUES ($1, $2, $3, $4, $5, 'system', 'test')`, id, org, project, title, content)
}

func TestTheIndexerEmbedsAcrossOrganizationsAndKeepsTheModel(t *testing.T) {
	app, owner := dbtest.Open(t)
	a, _, _, _ := seed(t, owner)
	b, _, _, _ := seed(t, owner)
	fake := &embeddings.Fake{Dims: 768}
	drain(t, &memory.Indexer{DB: app, Embedder: fake})

	var left, models int
	if err := owner.QueryRow(context.Background(), `SELECT count(*) FILTER (WHERE embedding IS NULL),
		count(DISTINCT embedding_model) FROM search_documents WHERE organization_id IN ($1, $2)`, a, b).Scan(&left, &models); err != nil {
		t.Fatal(err)
	}
	if left != 0 || models != 1 {
		t.Errorf("%d documents left unembedded, %d models", left, models)
	}
	// A document is embedded once: with nothing new, no call is made.
	calls := fake.Calls
	drain(t, &memory.Indexer{DB: app, Embedder: fake})
	if fake.Calls != calls {
		t.Errorf("embedded again with nothing changed: %d calls", fake.Calls-calls)
	}
	// Another model: everything is due again.
	drain(t, &memory.Indexer{DB: app, Embedder: &renamed{fake}})
	if err := owner.QueryRow(context.Background(), `SELECT count(*) FROM search_documents
		WHERE organization_id IN ($1, $2) AND embedding_model <> 'other'`, a, b).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Errorf("%d documents kept the old model's embedding", left)
	}
}

type renamed struct{ *embeddings.Fake }

func (renamed) Model() string { return "other" }

func TestAFailureBacksOffOnlyTheDocumentThatFailed(t *testing.T) {
	app, owner := dbtest.Open(t)
	org, _, _, _ := seed(t, owner)
	remember(t, owner, org, "mem_bad", nil, "Poison", "this one the endpoint refuses")
	fake := &embeddings.Fake{Dims: 768, Fail: "Poison"}
	now := time.Now()
	x := &memory.Indexer{DB: app, Embedder: &permanent{fake}, Now: func() time.Time { return now }}
	drain(t, x)

	var embedded, failed int
	var errText string
	var next time.Time
	if err := owner.QueryRow(context.Background(), `SELECT count(*) FILTER (WHERE embedding IS NOT NULL),
		count(*) FILTER (WHERE last_error IS NOT NULL) FROM search_documents WHERE organization_id = $1`, org).Scan(&embedded, &failed); err != nil {
		t.Fatal(err)
	}
	if err := owner.QueryRow(context.Background(), `SELECT last_error, next_attempt_at FROM search_documents WHERE source_id = 'mem_bad'`).Scan(&errText, &next); err != nil {
		t.Fatal(err)
	}
	if embedded != 3 || failed != 1 {
		t.Errorf("one refused document held back the others: %d embedded, %d failed", embedded, failed)
	}
	if next.Before(now.Add(59 * time.Second)) {
		t.Errorf("the failed document is due again at once: %v", next.Sub(now))
	}
	if errText == "" {
		t.Error("the failure's reason was not kept")
	}
}

// permanent makes the fake's refusal one retrying cannot fix.
type permanent struct{ *embeddings.Fake }

func (p permanent) Embed(ctx context.Context, texts []string, purpose embeddings.Purpose) ([][]float32, error) {
	v, err := p.Fake.Embed(ctx, texts, purpose)
	if err != nil {
		return nil, &embeddings.Error{Status: 400, Body: err.Error()}
	}
	return v, nil
}

func TestWordsAndMeaningTogetherOutrankEitherAlone(t *testing.T) {
	app, owner := dbtest.Open(t)
	org, project, _, _ := seed(t, owner)
	remember(t, owner, org, "mem_hook", project, "GitHub redelivers a webhook for days", "Dedupe webhook deliveries on X-GitHub-Delivery.")
	remember(t, owner, org, "mem_font", project, "Fonts are bundled", "The transcript page loads no webfont.")
	fake := &embeddings.Fake{Dims: 768}
	drain(t, &memory.Indexer{DB: app, Embedder: fake})

	out := search(t, app, org, fake, memory.Query{Text: "webhook deliveries dedupe", Project: project})
	if out.Mode != "hybrid" {
		t.Fatalf("mode = %s (%s)", out.Mode, out.Degraded)
	}
	if len(out.Results) == 0 || out.Results[0].ID != "mem_hook" || out.Results[0].TextRank == 0 || out.Results[0].VectorRank == 0 {
		t.Fatalf("top result = %+v", out.Results)
	}
	for _, r := range out.Results {
		if r.ID == "mem_font" && r.Score >= out.Results[0].Score {
			t.Errorf("an unrelated memory scored as high: %+v", r)
		}
	}
}

func TestWithoutAnEmbedderSearchIsByWordsAndSaysSo(t *testing.T) {
	app, owner := dbtest.Open(t)
	org, project, _, task := seed(t, owner)
	out := search(t, app, org, nil, memory.Query{Text: "dedupe", Project: project})
	if out.Mode != "words" || len(out.Results) != 1 || out.Results[0].ID != task || out.Results[0].Embedded {
		t.Errorf("outcome = %+v", out)
	}
	// A -word excludes whatever has it, whichever of the other words matched.
	for _, q := range []string{"dedupe -deliveries", "-deliveries dedupe", "webhook dedupe -restart"} {
		for _, r := range search(t, app, org, nil, memory.Query{Text: q}).Results {
			if r.ID == task {
				t.Errorf("%q found the task it excludes", q)
			}
		}
	}
	if n := len(search(t, app, org, nil, memory.Query{Text: "-deliveries"}).Results); n != 0 {
		t.Errorf("only a -word found %d documents", n)
	}
	out = search(t, app, org, nil, memory.Query{Text: "TEXT-1"})
	if len(out.Results) == 0 || out.Results[0].ID != task {
		t.Errorf("a task is not found by its key: %+v", out.Results)
	}
}

func TestAProjectSearchesItselfAndItsOrganizationsMemoriesOnly(t *testing.T) {
	app, owner := dbtest.Open(t)
	org, project, _, _ := seed(t, owner)
	other := "prj_other_" + org
	exec(t, owner, `INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ($1, $2, 'web', $1, 'WEB')`, other, org)
	remember(t, owner, org, "mem_org", nil, "Commit messages are prose", "Everywhere at Acme.")
	remember(t, owner, org, "mem_here", project, "Commit here after tests", "control-plane only.")
	remember(t, owner, org, "mem_there", other, "Commit there with care", "web only.")

	got := map[string]bool{}
	for _, r := range search(t, app, org, nil, memory.Query{Text: "commit", Project: project, Types: []string{"memory"}}).Results {
		got[r.ID] = true
	}
	if !got["mem_org"] || !got["mem_here"] || got["mem_there"] {
		t.Errorf("control-plane's search found %v", got)
	}
}

func TestAboutNarrowsMemoriesToWhatTheyConcern(t *testing.T) {
	app, owner := dbtest.Open(t)
	org, project, epic, task := seed(t, owner)
	remember(t, owner, org, "mem_a", project, "Retries on this task", "about the task")
	remember(t, owner, org, "mem_b", project, "Retries elsewhere", "about nothing")
	exec(t, owner, `INSERT INTO memory_refs (memory_id, organization_id, ref_type, ref_id) VALUES ('mem_a', $1, 'task', $2)`, org, task)

	res := search(t, app, org, nil, memory.Query{Text: "retries", Types: []string{"memory"}, About: []string{task, epic}}).Results
	if len(res) != 1 || res[0].ID != "mem_a" {
		t.Errorf("about %s found %+v", task, res)
	}
}

func TestALimitAboveTheMostIsTheMost(t *testing.T) {
	app, owner := dbtest.Open(t)
	org, project, _, _ := seed(t, owner)
	for i := 0; i < 60; i++ {
		remember(t, owner, org, fmt.Sprintf("mem_%d", i), project, "Retries note", "retries")
	}
	if n := len(search(t, app, org, nil, memory.Query{Text: "retries", Limit: 100}).Results); n != 50 {
		t.Errorf("a limit of 100 gave %d results, want the most, 50", n)
	}
}

// A key that is wrong fails every document alike: none is backed off, so
// the key fixed, everything embeds at once.
func TestABadKeyBacksOffNothing(t *testing.T) {
	app, owner := dbtest.Open(t)
	org, _, _, _ := seed(t, owner)
	x := &memory.Indexer{DB: app, Embedder: unauthorized{&embeddings.Fake{Dims: 768}}}
	if _, err := x.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	var backedOff int
	if err := owner.QueryRow(context.Background(), `SELECT count(*) FROM search_documents
		WHERE organization_id = $1 AND (attempts > 0 OR next_attempt_at > now())`, org).Scan(&backedOff); err != nil {
		t.Fatal(err)
	}
	if backedOff != 0 {
		t.Errorf("a bad key backed off %d documents", backedOff)
	}
}

type unauthorized struct{ *embeddings.Fake }

func (unauthorized) Embed(context.Context, []string, embeddings.Purpose) ([][]float32, error) {
	return nil, &embeddings.Error{Status: 401, Body: "invalid key"}
}
