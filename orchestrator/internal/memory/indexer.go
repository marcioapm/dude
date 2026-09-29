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
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/embeddings"
)

// Indexer embeds what the triggers left without an embedding, across
// organizations, a batch at a time. Without an Embedder it does nothing and
// search is by words.
//
// One rule for failures: a document is blamed only when its neighbours
// embed. Anything else — a bad key, a wrong model, a rate limit, the
// endpoint down — is the embedder's: the indexer backs off as a whole (at
// most ten minutes, so a fix is picked up soon) and says why (Health), and
// no document's own backoff moves.
type Indexer struct {
	DB       *db.DB
	Embedder embeddings.Embedder
	Log      *slog.Logger
	// Now is for tests.
	Now func() time.Time

	mu       sync.Mutex
	health   Health
	failures int
	// When the last check for another model's embeddings ran.
	modelChecked time.Time
}

// Health is the embedder as the indexer last found it, for the Index page.
type Health struct {
	// Empty when the last call worked.
	Error string `json:"error,omitempty"`
	// Since when it has failed, and when it is tried next.
	Since *time.Time `json:"since,omitempty"`
	Retry *time.Time `json:"retry,omitempty"`
}

func (x *Indexer) Health() Health {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.health
}

// backoff is how long a document the endpoint refused waits after its nth
// refusal: soon at first, then daily, never given up on.
var backoff = []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour, 24 * time.Hour}

func after(attempts int) time.Duration {
	return backoff[min(attempts, len(backoff))-1]
}

// down is how long the whole indexer waits after its nth failure in a row.
func down(n int) time.Duration {
	return min(10*time.Minute, 15*time.Second<<min(n-1, 6))
}

// How often embeddings another model made are looked for: a change of
// model is picked up at start, and an orchestrator that still has the old
// one (a rolling deploy) cannot undo it for long.
const modelCheckEvery = time.Hour

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
	x.mu.Lock()
	waiting := x.health.Retry != nil && now().Before(*x.health.Retry)
	checkModel := now().Sub(x.modelChecked) >= modelCheckEvery
	x.mu.Unlock()
	if waiting {
		return 0, nil
	}
	model := x.Embedder.Model()
	if checkModel {
		if err := x.clearOtherModels(ctx, model); err != nil {
			return 0, err
		}
		x.mu.Lock()
		x.modelChecked = now()
		x.mu.Unlock()
	}
	var batch []pending
	err := x.DB.InSystem(ctx, "indexer", func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT source_type, source_id, content_hash, title || E'\n\n' || body, attempts
			FROM search_documents
			WHERE embedding IS NULL AND next_attempt_at <= $1
			ORDER BY next_attempt_at LIMIT $2`, now(), embeddings.MaxBatch)
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
	if err == nil {
		x.worked()
		return len(batch), x.store(ctx, batch, vecs, model, now())
	}
	if len(batch) > 1 && embeddings.OneBad(err) {
		return x.oneByOne(ctx, batch, texts, model, now, err)
	}
	x.broke(err, now())
	return 0, nil
}

// oneByOne finds the document the endpoint will not take, so it does not
// hold back the rest. When the first few fail alone too, no document is to
// blame: the embedder is, and the rest are not tried.
func (x *Indexer) oneByOne(ctx context.Context, batch []pending, texts []string, model string, now func() time.Time, cause error) (int, error) {
	const giveUpAfter = 3
	done := 0
	var refused []int
	for i, p := range batch {
		if done == 0 && len(refused) == giveUpAfter {
			x.broke(cause, now())
			return 0, nil
		}
		vecs, err := x.Embedder.Embed(ctx, texts[i:i+1], embeddings.Document)
		if err != nil {
			if !embeddings.OneBad(err) {
				x.broke(err, now())
				return done, nil
			}
			refused = append(refused, i)
			continue
		}
		if err := x.store(ctx, []pending{p}, vecs, model, now()); err != nil {
			return done, err
		}
		done++
	}
	if done == 0 {
		x.broke(cause, now())
		return 0, nil
	}
	x.worked()
	for _, i := range refused {
		if err := x.refused(ctx, batch[i], cause, now()); err != nil {
			return done, err
		}
	}
	return done, nil
}

func (x *Indexer) worked() {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.failures, x.health = 0, Health{}
}

func (x *Indexer) broke(cause error, at time.Time) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.failures++
	retry := at.Add(down(x.failures))
	since := at
	if x.health.Since != nil {
		since = *x.health.Since
	}
	x.health = Health{Error: short(cause.Error()), Since: &since, Retry: &retry}
	if x.Log != nil {
		x.Log.Warn("embeddings failed", "error", cause, "retry", retry)
	}
}

func (x *Indexer) clearOtherModels(ctx context.Context, model string) error {
	return x.DB.InSystem(ctx, "indexer", func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE search_documents
			SET embedding = NULL, embedding_model = NULL, embedded_at = NULL, attempts = 0, last_error = NULL, next_attempt_at = now()
			WHERE embedding IS NOT NULL AND embedding_model IS DISTINCT FROM $1`, model)
		if err == nil && tag.RowsAffected() > 0 && x.Log != nil {
			x.Log.Info("embeddings from another model cleared, to embed again", "model", model, "documents", tag.RowsAffected())
		}
		return err
	})
}

func short(msg string) string {
	if len(msg) > 500 {
		return msg[:500] + "…"
	}
	return msg
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

// refused backs one document off: the endpoint took its neighbours, not it.
func (x *Indexer) refused(ctx context.Context, p pending, cause error, at time.Time) error {
	return x.DB.InSystem(ctx, "indexer", func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE search_documents
			SET attempts = attempts + 1, last_error = $4, last_attempt_at = $6, next_attempt_at = $5
			WHERE source_type = $1 AND source_id = $2 AND content_hash = $3`,
			p.typ, p.id, p.hash, short(cause.Error()), at.Add(after(p.attempts+1)), at)
		return err
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
