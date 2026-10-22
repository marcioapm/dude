package guide_test

import (
	"context"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/guide"
)

func page(t *testing.T, slug, src string) guide.Page {
	t.Helper()
	p, err := guide.Parse(slug, []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

type stored struct {
	Title, Body, Version string
	Embedded            bool
}

func sections(t *testing.T, owner *pgx.Conn) map[string]stored {
	t.Helper()
	rows, err := owner.Query(context.Background(), `SELECT id, title, body, version, embedding IS NOT NULL FROM guide_sections`)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]stored{}
	for rows.Next() {
		var id string
		var s stored
		if err := rows.Scan(&id, &s.Title, &s.Body, &s.Version, &s.Embedded); err != nil {
			t.Fatal(err)
		}
		out[id] = s
	}
	return out
}

func embedAll(t *testing.T, owner *pgx.Conn) {
	t.Helper()
	if _, err := owner.Exec(context.Background(), `UPDATE guide_sections
		SET embedding = array_fill(0.5, ARRAY[768])::halfvec, embedding_model = 'fake'`); err != nil {
		t.Fatal(err)
	}
}

func TestSyncWritesChangesAndKeepsWhatIsUnchanged(t *testing.T) {
	app, owner := dbtest.Open(t)
	ctx := context.Background()
	v1 := []guide.Page{
		page(t, "a", "---\ntitle: A\n---\nIntro.\n## Kept\nSame words.\n## Changed\nOld words.\n## Removed\nGone soon.\n"),
		page(t, "b", "---\ntitle: B\n---\n## Only\nB's words.\n"),
	}
	res, err := guide.Sync(ctx, app, "v1", v1)
	if err != nil {
		t.Fatal(err)
	}
	got := sections(t, owner)
	if res.Written != 5 || res.Deleted != 0 || len(got) != 5 || got["a#kept"].Title != "A — Kept" || got["a#"].Title != "A" ||
		got["b#only"].Body != "B's words." {
		t.Fatalf("first sync %+v: %+v", res, got)
	}
	embedAll(t, owner)

	v2 := []guide.Page{
		page(t, "a", "---\ntitle: A\n---\nIntro.\n## Kept\nSame words.\n## Changed\nNew words.\n## Added\nNew section.\n"),
	}
	if res, err = guide.Sync(ctx, app, "v2", v2); err != nil {
		t.Fatal(err)
	}
	got = sections(t, owner)
	want := map[string]stored{
		"a#":        {"A", "Intro.", "v1", true},
		"a#kept":    {"A — Kept", "Same words.", "v1", true},
		"a#changed": {"A — Changed", "New words.", "v2", false},
		"a#added":   {"A — Added", "New section.", "v2", false},
	}
	if res.Written != 2 || res.Deleted != 2 || len(got) != len(want) {
		t.Fatalf("second sync %+v: %+v", res, got)
	}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("%s: %+v, want %+v", id, got[id], w)
		}
	}
	// Again: nothing to do.
	if res, err = guide.Sync(ctx, app, "v2", v2); err != nil || res != (guide.SyncResult{}) {
		t.Errorf("third sync %+v %v", res, err)
	}
}

// Two orchestrators starting at once on the same build end with the guide
// once, neither failing.
func TestSyncsInParallelEndCorrect(t *testing.T) {
	app, owner := dbtest.Open(t)
	ps := []guide.Page{page(t, "a", "---\ntitle: A\n---\n## One\n1\n## Two\n2\n"), page(t, "b", "---\ntitle: B\n---\nIntro\n")}
	if _, err := guide.Sync(context.Background(), app, "old", []guide.Page{page(t, "c", "---\ntitle: C\n---\nold\n")}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = guide.Sync(context.Background(), app, "new", ps)
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("sync %d: %v", i, err)
		}
	}
	got := sections(t, owner)
	if len(got) != 3 || got["a#one"].Body != "1" || got["a#two"].Body != "2" || got["b#"].Body != "Intro" {
		t.Errorf("after parallel syncs: %+v", got)
	}
}

// The app role reads the guide in any organisation's transaction: it has
// no organization_id and no row-level security. (Its writes are not
// refused by grants: dude_app inherits dude_sweeper's, migration 008.)
func TestTheAppReadsTheGuide(t *testing.T) {
	app, _ := dbtest.Open(t)
	ctx := context.Background()
	if _, err := guide.SyncBuiltIn(ctx, app, "test"); err != nil {
		t.Fatal(err)
	}
	for _, org := range []string{"org_a", "org_b"} {
		var n int
		err := app.InOrg(ctx, org, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*) FROM guide_sections`).Scan(&n)
		})
		if err != nil || n != len(guide.Rows(guide.Pages())) {
			t.Errorf("%s reads %d sections (%v), want %d", org, n, err, len(guide.Rows(guide.Pages())))
		}
	}
}
