// Package memory is dude's memory: the records people, dude and agents
// keep, and one search over them and the work (docs/design/memory.md).
//
// The index itself is kept by triggers (migration 054); this package embeds
// it (Indexer) and searches it (Search), for the agents' tools and the
// settings alike.
package memory

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/embeddings"
)

// Indexer embeds what the triggers left without an embedding, across
// organizations, a batch at a time. Without an Embedder it does nothing and
// search is by words.
type Indexer struct {
	DB       *db.DB
	Embedder embeddings.Embedder
	Log      *slog.Logger
	// Now is for tests.
	Now func() time.Time
}

// backoff is how long a document waits after its nth failure: soon at
// first (a rate limit passes), then daily, never given up on.
var backoff = []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour, 24 * time.Hour}

func after(attempts int) time.Duration {
	return backoff[min(attempts, len(backoff))-1]
}

type pending struct {
	typ, id, hash, text string
	attempts            int
}

// Sweep embeds one batch and reports how many it embedded, so the loop runs
// again at once while there is more.
func (x *Indexer) Sweep(ctx context.Context) (int, error) {
	if x.Embedder == nil {
		return 0, nil
	}
	now := time.Now
	if x.Now != nil {
		now = x.Now
	}
	model := x.Embedder.Model()
	var batch []pending
	// A document embedded with another model counts as not embedded: a
	// change of model is a reindex.
	err := x.DB.InSystem(ctx, "indexer", func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT source_type, source_id, content_hash, title || E'\n\n' || body, attempts
			FROM search_documents
			WHERE (embedding IS NULL OR embedding_model IS DISTINCT FROM $1) AND next_attempt_at <= $2
			ORDER BY next_attempt_at LIMIT $3`, model, now(), embeddings.MaxBatch)
		if err != nil {
			return err
		}
		batch, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (pending, error) {
			var p pending
			return p, r.Scan(&p.typ, &p.id, &p.hash, &p.text, &p.attempts)
		})
		return err
	})
	if err != nil || len(batch) == 0 {
		return 0, err
	}
	texts := make([]string, len(batch))
	for i, p := range batch {
		texts[i] = clip(p.text)
	}
	vecs, err := x.Embedder.Embed(ctx, texts, embeddings.Document)
	if err != nil && len(batch) > 1 && !embeddings.Retryable(err) {
		// One document the endpoint will never take must not hold back the
		// other ninety-nine: embed them one by one to find it.
		return x.oneByOne(ctx, batch, texts, model, now)
	}
	if err != nil {
		return 0, x.failed(ctx, batch, err, now())
	}
	return len(batch), x.store(ctx, batch, vecs, model, now())
}

func (x *Indexer) oneByOne(ctx context.Context, batch []pending, texts []string, model string, now func() time.Time) (int, error) {
	done := 0
	for i, p := range batch {
		vecs, err := x.Embedder.Embed(ctx, texts[i:i+1], embeddings.Document)
		if err != nil {
			if err := x.failed(ctx, []pending{p}, err, now()); err != nil {
				return done, err
			}
			continue
		}
		if err := x.store(ctx, []pending{p}, vecs, model, now()); err != nil {
			return done, err
		}
		done++
	}
	return done, nil
}

// store writes each vector back unless the words changed while it was
// being made: then the new words are what is pending.
func (x *Indexer) store(ctx context.Context, batch []pending, vecs [][]float32, model string, at time.Time) error {
	return x.DB.InSystem(ctx, "indexer", func(tx pgx.Tx) error {
		for i, p := range batch {
			if _, err := tx.Exec(ctx, `UPDATE search_documents
				SET embedding = $4::halfvec, embedding_model = $5, embedded_at = $6, attempts = 0, last_error = NULL
				WHERE source_type = $1 AND source_id = $2 AND content_hash = $3`,
				p.typ, p.id, p.hash, Vector(vecs[i]), model, at); err != nil {
				return err
			}
		}
		return nil
	})
}

func (x *Indexer) failed(ctx context.Context, batch []pending, cause error, at time.Time) error {
	if x.Log != nil {
		x.Log.Warn("embedding failed", "documents", len(batch), "error", cause)
	}
	msg := cause.Error()
	if len(msg) > 500 {
		msg = msg[:500] + "…"
	}
	return x.DB.InSystem(ctx, "indexer", func(tx pgx.Tx) error {
		for _, p := range batch {
			if _, err := tx.Exec(ctx, `UPDATE search_documents
				SET attempts = attempts + 1, last_error = $4, next_attempt_at = $5
				WHERE source_type = $1 AND source_id = $2 AND content_hash = $3`,
				p.typ, p.id, p.hash, msg, at.Add(after(p.attempts+1))); err != nil {
				return err
			}
		}
		return nil
	})
}

// clip keeps a text within what one embedding reads well: the start of a
// long task says what it is. Cut on a rune boundary.
func clip(s string) string {
	const max = 8000
	if len(s) <= max {
		return s
	}
	s = s[:max]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

// Vector is pgvector's text form: [0.1,0.2,…].
func Vector(v []float32) string {
	var b strings.Builder
	b.Grow(len(v) * 8)
	b.WriteByte('[')
	for i, x := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(x), 'g', 6, 32))
	}
	b.WriteByte(']')
	return b.String()
}

// Retry makes the failed documents of an organization due now: the Index
// page's Retry and Retry all.
func Retry(ctx context.Context, tx pgx.Tx, typ, id string) (int64, error) {
	tag, err := tx.Exec(ctx, `UPDATE search_documents SET next_attempt_at = now()
		WHERE embedding IS NULL AND last_error IS NOT NULL
		  AND ($1 = '' OR (source_type = $1 AND source_id = $2))`, typ, id)
	if err != nil {
		return 0, fmt.Errorf("retry: %w", err)
	}
	return tag.RowsAffected(), nil
}

// Reindex drops an organization's embeddings so every document is embedded
// again, by words meanwhile.
func Reindex(ctx context.Context, tx pgx.Tx) (int64, error) {
	tag, err := tx.Exec(ctx, `UPDATE search_documents
		SET embedding = NULL, embedding_model = NULL, embedded_at = NULL, attempts = 0, last_error = NULL, next_attempt_at = now()`)
	if err != nil {
		return 0, fmt.Errorf("reindex: %w", err)
	}
	return tag.RowsAffected(), nil
}
