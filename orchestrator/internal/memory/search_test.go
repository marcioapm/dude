package memory_test

import (
	"context"
	"fmt"
	"strings"
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
		out, err = memory.Ranked(context.Background(), tx, memory.EmbedQuery(context.Background(), e, q.Text), q)
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
	if h := x.Health(); h.Error != "" {
		t.Errorf("one refused document made the embedder look broken: %+v", h)
	}

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

	// Its backoff ends on the indexer's clock, and it is taken then.
	fake.Fail = ""
	now = next.Add(-time.Millisecond)
	if n, err := x.Sweep(context.Background()); err != nil || n != 0 {
		t.Errorf("before its backoff ends the sweep embedded %d (%v)", n, err)
	}
	now = next
	if n, err := x.Sweep(context.Background()); err != nil || n != 1 {
		t.Errorf("once its backoff ends the sweep embedded %d (%v)", n, err)
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

// Refused documents at the head of the queue, however many, and a refused
// document alone: each is blamed alone, the rest embed, and the embedder
// is not reported broken.
func TestRefusedDocumentsNeverWedgeTheQueue(t *testing.T) {
	for _, n := range []int{1, 4} {
		app, owner := dbtest.Open(t)
		org := dbtest.Org(t, owner)
		for i := 0; i < n; i++ {
			remember(t, owner, org, fmt.Sprintf("mem_bad%d", i), nil, "Poison", "refused")
		}
		now := time.Now()
		x := &memory.Indexer{DB: app, Embedder: &permanent{&embeddings.Fake{Dims: 768, Fail: "Poison"}}, Now: func() time.Time { return now }}
		drain(t, x)
		if h := x.Health(); h.Error != "" {
			t.Errorf("%d refused alone: the embedder is reported broken: %+v", n, h)
		}
		var backedOff int
		if err := owner.QueryRow(context.Background(), `SELECT count(*) FROM search_documents
			WHERE organization_id = $1 AND last_error IS NOT NULL AND next_attempt_at > $2`, org, now).Scan(&backedOff); err != nil {
			t.Fatal(err)
		}
		if backedOff != n {
			t.Errorf("%d refused: %d backed off", n, backedOff)
		}
		// Behind them, new work embeds.
		remember(t, owner, org, "mem_good", nil, "Fine", "embeds")
		drain(t, x)
		var good bool
		if err := owner.QueryRow(context.Background(), `SELECT embedding IS NOT NULL FROM search_documents WHERE source_id = 'mem_good'`).Scan(&good); err != nil {
			t.Fatal(err)
		}
		if !good {
			t.Errorf("%d refused: a new memory behind them was never embedded", n)
		}
	}
}

// A person's Retry ends the indexer's wait at once.
func TestRetryEndsTheWait(t *testing.T) {
	app, owner := dbtest.Open(t)
	seed(t, owner)
	now := time.Now()
	failing := &statusFake{Fake: &embeddings.Fake{Dims: 768}, status: 502}
	x := &memory.Indexer{DB: app, Embedder: failing, Now: func() time.Time { return now }}
	_, _ = x.Sweep(context.Background())
	failing.status = 0
	x.Resume()
	if n, err := x.Sweep(context.Background()); err != nil || n == 0 {
		t.Errorf("after Resume the sweep embedded %d (%v)", n, err)
	}
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
	if !strings.Contains(out.Results[0].Snippet, "Dedupe webhook deliveries") || strings.ContainsAny(out.Results[0].Snippet, "⟦*`") {
		t.Errorf("snippet is not the plain text around the words: %q", out.Results[0].Snippet)
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

// When no document embeds, the embedder is to blame, not them: whatever it
// said — a bad key, a wrong model, a rate limit, the endpoint down — no
// document is backed off, the indexer waits as a whole and says why, and
// once it works again everything embeds.
func TestAnEmbedderThatFailsBacksOffNoDocument(t *testing.T) {
	for _, status := range []int{401, 400, 429, 502} {
		app, owner := dbtest.Open(t)
		org, _, _, _ := seed(t, owner)
		now := time.Now()
		failing := &statusFake{Fake: &embeddings.Fake{Dims: 768}, status: status}
		x := &memory.Indexer{DB: app, Embedder: failing, Now: func() time.Time { return now }}
		if _, err := x.Sweep(context.Background()); err != nil {
			t.Fatal(err)
		}
		var backedOff int
		if err := owner.QueryRow(context.Background(), `SELECT count(*) FROM search_documents
			WHERE organization_id = $1 AND (attempts > 0 OR last_error IS NOT NULL)`, org).Scan(&backedOff); err != nil {
			t.Fatal(err)
		}
		if backedOff != 0 {
			t.Errorf("%d: backed off %d documents", status, backedOff)
		}
		if h := x.Health(); h.Error == "" || h.Retry == nil || strings.Contains(h.Error, "no") && status == 400 && strings.Contains(h.Error, ": no") {
			t.Errorf("%d: health does not say it is failing, or quotes the endpoint: %+v", status, h)
		}
		// It waits as a whole: the next sweep makes no call.
		calls := failing.Calls
		_, _ = x.Sweep(context.Background())
		if failing.Calls != calls {
			t.Errorf("%d: called the embedder again before the wait was over", status)
		}
		// Fixed, and the wait over: everything embeds, and it says so.
		failing.status = 0
		now = now.Add(11 * time.Minute)
		drain(t, x)
		var left int
		if err := owner.QueryRow(context.Background(), `SELECT count(*) FROM search_documents
			WHERE organization_id = $1 AND embedding IS NULL`, org).Scan(&left); err != nil {
			t.Fatal(err)
		}
		if left != 0 || x.Health().Error != "" {
			t.Errorf("%d: once fixed, %d left unembedded, health %+v", status, left, x.Health())
		}
	}
}

// statusFake answers every call with status, until it is 0.
type statusFake struct {
	*embeddings.Fake
	status int
}

func (f *statusFake) Embed(ctx context.Context, texts []string, p embeddings.Purpose) ([][]float32, error) {
	if f.status != 0 {
		f.Calls++
		return nil, &embeddings.Error{Status: f.status, Body: "no"}
	}
	return f.Fake.Embed(ctx, texts, p)
}

// The orchestrator's clock is compared with next_attempt_at, and the
// database's may be ahead of it (Docker on a Mac runs tens of milliseconds
// ahead). What is due at once must not wait for the two to agree: here the
// orchestrator's clock is an hour behind the database's.
func TestWorkDueAtOnceIsDueWhateverTheClocksSay(t *testing.T) {
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	behind := time.Now().Add(-time.Hour)
	at := func() time.Time { return behind }
	embedded := func(id string) bool {
		t.Helper()
		var ok bool
		if err := owner.QueryRow(context.Background(), `SELECT embedding IS NOT NULL FROM search_documents WHERE source_id = $1`, id).Scan(&ok); err != nil {
			t.Fatal(err)
		}
		return ok
	}
	fake := &embeddings.Fake{Dims: 768, Fail: "Poison"}
	x := &memory.Indexer{DB: app, Embedder: &permanent{fake}, Now: at}

	remember(t, owner, org, "mem_new", nil, "New", "just written")
	remember(t, owner, org, "mem_bad", nil, "Poison", "refused")
	drain(t, x)
	if !embedded("mem_new") {
		t.Error("a new document waited for the database's clock")
	}

	// New words.
	exec(t, owner, `UPDATE memories SET content = 'rewritten' WHERE id = 'mem_new'`)
	drain(t, x)
	if !embedded("mem_new") {
		t.Error("a document whose words changed waited for the database's clock")
	}

	// Retry.
	fake.Fail = ""
	if err := app.InOrg(context.Background(), org, func(tx pgx.Tx) error {
		n, err := memory.Retry(context.Background(), tx, "", "")
		if err == nil && n != 1 {
			t.Errorf("Retry made %d documents due, want 1", n)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	drain(t, x)
	if !embedded("mem_bad") {
		t.Error("a retried document waited for the database's clock")
	}

	// Reindex.
	if err := app.InOrg(context.Background(), org, func(tx pgx.Tx) error {
		_, err := memory.Reindex(context.Background(), tx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	drain(t, x)
	if !embedded("mem_new") || !embedded("mem_bad") {
		t.Error("a reindexed document waited for the database's clock")
	}

	// Another model.
	drain(t, &memory.Indexer{DB: app, Embedder: &renamed{&embeddings.Fake{Dims: 768}}, Now: at})
	var left int
	if err := owner.QueryRow(context.Background(), `SELECT count(*) FROM search_documents
		WHERE organization_id = $1 AND (embedding IS NULL OR embedding_model <> 'other')`, org).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Errorf("after a change of model %d documents waited for the database's clock", left)
	}
}
