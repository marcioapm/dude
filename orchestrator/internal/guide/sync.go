package guide

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
)

// Row is one section as guide_sections holds it.
type Row struct {
	ID, Page, Anchor, Title, Body, Hash string
}

// ID is a section's id: <page>#<anchor>.
func ID(page, anchor string) string { return page + "#" + anchor }

// Rows are ps's sections as guide_sections rows, ordered by id.
func Rows(ps []Page) []Row {
	var out []Row
	for _, p := range ps {
		for _, s := range p.Sections {
			title := p.SectionTitle(s)
			sum := sha256.Sum256([]byte(title + "\n" + s.Text))
			out = append(out, Row{ID: ID(p.Slug, s.Anchor), Page: p.Slug, Anchor: s.Anchor, Title: title, Body: s.Text,
				Hash: hex.EncodeToString(sum[:])})
		}
	}
	slices.SortFunc(out, func(a, b Row) int {
		switch {
		case a.ID < b.ID:
			return -1
		case a.ID > b.ID:
			return 1
		}
		return 0
	})
	return out
}

// SyncResult is what a sync changed.
type SyncResult struct {
	Written, Deleted int64
}

// Sync makes guide_sections hold ps's sections, in one transaction: a new
// or changed section is written and waits for an embedding; a section no
// page has is deleted; an unchanged one keeps its row and embedding.
// The supported single orchestrator uses its build's guide on each start,
// including rollback: the last-started build's content wins, not the newest version.
// Concurrent syncs of identical pages lock rows in id order; unlike page
// sets are not supported concurrently.
func Sync(ctx context.Context, d *db.DB, version string, ps []Page) (SyncResult, error) {
	rows := Rows(ps)
	n := len(rows)
	ids, pages, anchors, titles, bodies, hashes := make([]string, n), make([]string, n), make([]string, n),
		make([]string, n), make([]string, n), make([]string, n)
	for i, r := range rows {
		ids[i], pages[i], anchors[i], titles[i], bodies[i], hashes[i] = r.ID, r.Page, r.Anchor, r.Title, r.Body, r.Hash
	}
	var out SyncResult
	err := d.InSystem(ctx, "guide-sync", func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `INSERT INTO guide_sections AS g (id, page, anchor, title, body, content_hash, version)
			SELECT u.*, $7::text FROM unnest($1::text[], $2::text[], $3::text[], $4::text[], $5::text[], $6::text[]) u
			ORDER BY 1
			ON CONFLICT (id) DO UPDATE SET
				page = EXCLUDED.page, anchor = EXCLUDED.anchor, title = EXCLUDED.title, body = EXCLUDED.body,
				content_hash = EXCLUDED.content_hash, version = EXCLUDED.version, updated_at = now(),
				embedding = NULL, embedding_model = NULL, embedded_at = NULL,
				attempts = 0, last_error = NULL, last_attempt_at = NULL, next_attempt_at = '-infinity'
			WHERE g.content_hash IS DISTINCT FROM EXCLUDED.content_hash`,
			ids, pages, anchors, titles, bodies, hashes, version)
		if err != nil {
			return err
		}
		out.Written = tag.RowsAffected()
		tag, err = tx.Exec(ctx, `DELETE FROM guide_sections WHERE id <> ALL($1::text[])`, ids)
		out.Deleted = tag.RowsAffected()
		return err
	})
	return out, err
}

// SyncBuiltIn is Sync of the guide this build embeds.
func SyncBuiltIn(ctx context.Context, d *db.DB, version string) (SyncResult, error) {
	return Sync(ctx, d, version, pages)
}
