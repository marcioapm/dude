// Package memory is dude's memory: the records people, dude and agents
// keep, and one search over them and the work (docs/design/memory.md).
//
// The index itself is kept by triggers (migration 054); this package embeds
// it (Indexer) and searches it (Search), for the agents' tools and the
// settings alike.
package memory

import (
	"context"
	"errors"
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
// One rule for failures, decided by a probe: when a batch fails, a short
// text every model takes is embedded. If that fails too, the embedder is
// to blame (a bad key, a wrong model, a rate limit, the endpoint down): the
// indexer waits as a whole (at most ten minutes, or until a person asks it
// to retry) and says why (Health), and no document's backoff moves. If the
// probe embeds, the batch's documents are to blame: they are embedded one
// by one, and each one refused backs off on its own.
type Indexer struct {
	DB       *db.DB
	Embedder embeddings.Embedder
	Log      *slog.Logger
	// Now overrides the indexer's clock for tests. Refusal backoff uses this
	// clock; immediately due work uses -infinity to avoid database clock skew.
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
	if !x.probe(ctx) {
		x.broke(err, now())
		return 0, nil
	}
	x.worked()
	return x.oneByOne(ctx, batch, texts, model, now)
}

// probeText is what every embedding model takes: the probe that tells the
// embedder's failure from a document's.
const probeText = "dude"

func (x *Indexer) probe(ctx context.Context) bool {
	_, err := x.Embedder.Embed(ctx, []string{probeText}, embeddings.Document)
	return err == nil
}

// oneByOne embeds a batch the embedder refused, knowing the embedder works:
// each document it refuses is that document's fault and backs off. A
// failure that is not a refusal (a rate limit, the endpoint going down)
// stops it: what is done is kept, and the next sweep decides again. What it
// learned is written in one transaction at the end.
func (x *Indexer) oneByOne(ctx context.Context, batch []pending, texts []string, model string, now func() time.Time) (int, error) {
	var done []pending
	var vecs [][]float32
	var refused []pending
	var causes []error
	for i, p := range batch {
		v, err := x.Embedder.Embed(ctx, texts[i:i+1], embeddings.Document)
		if err != nil && !embeddings.OneBad(err) {
			break
		}
		if err != nil {
			refused, causes = append(refused, p), append(causes, err)
			continue
		}
		done, vecs = append(done, p), append(vecs, v[0])
	}
	at := now()
	return len(done), x.DB.InSystem(ctx, "indexer", func(tx pgx.Tx) error {
		if err := storeIn(ctx, tx, done, vecs, model, at); err != nil {
			return err
		}
		for i, p := range refused {
			if _, err := tx.Exec(ctx, `UPDATE search_documents
				SET attempts = attempts + 1, last_error = $4, last_attempt_at = $6, next_attempt_at = $5
				WHERE source_type = $1 AND source_id = $2 AND content_hash = $3`,
				p.typ, p.id, p.hash, truncate(causes[i].Error(), 500), at.Add(after(p.attempts+1)), at); err != nil {
				return err
			}
		}
		return nil
	})
}

// Resume ends a wait: a person pressed Retry or Reindex, and expects it now.
func (x *Indexer) Resume() {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.health.Retry = nil
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
	// UTC: Since and Retry reach the Index page as JSON.
	at = at.UTC()
	retry := at.Add(down(x.failures))
	since := at
	if x.health.Since != nil {
		since = *x.health.Since
	}
	x.health = Health{Error: describe(cause), Since: &since, Retry: &retry}
	if x.Log != nil {
		x.Log.Warn("embeddings failed", "error", cause, "retry", retry)
	}
}

func (x *Indexer) clearOtherModels(ctx context.Context, model string) error {
	return x.DB.InSystem(ctx, "indexer", func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE search_documents
			SET embedding = NULL, embedding_model = NULL, embedded_at = NULL, attempts = 0, last_error = NULL, next_attempt_at = '-infinity'
			WHERE embedding IS NOT NULL AND embedding_model IS DISTINCT FROM $1`, model)
		if err == nil && tag.RowsAffected() > 0 && x.Log != nil {
			x.Log.Info("embeddings from another model cleared, to embed again", "model", model, "documents", tag.RowsAffected())
		}
		return err
	})
}

// describe says what went wrong without the endpoint's body: a batch spans
// organisations, and a body may echo their text (or the key), while Health
// is shown to every organisation. The body is in the orchestrator's log.
func describe(err error) string {
	var e *embeddings.Error
	if !errors.As(err, &e) {
		if errors.Is(err, context.DeadlineExceeded) {
			return "the embedder did not answer in time"
		}
		return "the embedder could not be reached"
	}
	switch {
	case e.Status == 401 || e.Status == 403:
		return fmt.Sprintf("the embedder refused the key (%d)", e.Status)
	case e.Status == 404 || e.Status == 405:
		return fmt.Sprintf("no embeddings API at the configured URL (%d)", e.Status)
	case e.Status == 429:
		return "the embedder is rate limiting (429)"
	case e.Status >= 500:
		return fmt.Sprintf("the embedder failed (%d)", e.Status)
	default:
		return fmt.Sprintf("the embedder refused the request (%d): check the model and dimensions", e.Status)
	}
}

// store writes each vector back unless the words changed while it was
// being made: then the new words are what is pending.
func (x *Indexer) store(ctx context.Context, batch []pending, vecs [][]float32, model string, at time.Time) error {
	return x.DB.InSystem(ctx, "indexer", func(tx pgx.Tx) error { return storeIn(ctx, tx, batch, vecs, model, at) })
}

// storeIn writes a batch's vectors in one statement.
func storeIn(ctx context.Context, tx pgx.Tx, batch []pending, vecs [][]float32, model string, at time.Time) error {
	if len(batch) == 0 {
		return nil
	}
	types, ids, hashes, vs := make([]string, len(batch)), make([]string, len(batch)), make([]string, len(batch)), make([]string, len(batch))
	for i, p := range batch {
		types[i], ids[i], hashes[i], vs[i] = p.typ, p.id, p.hash, Vector(vecs[i])
	}
	_, err := tx.Exec(ctx, `UPDATE search_documents d
		SET embedding = u.v::halfvec, embedding_model = $5, embedded_at = $6, attempts = 0, last_error = NULL
		FROM unnest($1::text[], $2::text[], $3::text[], $4::text[]) u(typ, id, hash, v)
		WHERE d.source_type = u.typ AND d.source_id = u.id AND d.content_hash = u.hash`,
		types, ids, hashes, vs, model, at)
	return err
}

// clip keeps a text within what one embedding reads well: the start of a
// long task says what it is.
func clip(s string) string { return truncate(s, 8000) }

// truncate cuts s to at most n bytes, on a rune boundary, marking the cut
// with … when it is text people read (n below the clip size).
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	if n < 8000 {
		s += "…"
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
	tag, err := tx.Exec(ctx, `UPDATE search_documents SET next_attempt_at = '-infinity'
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
		SET embedding = NULL, embedding_model = NULL, embedded_at = NULL, attempts = 0, last_error = NULL, next_attempt_at = '-infinity'`)
	if err != nil {
		return 0, fmt.Errorf("reindex: %w", err)
	}
	return tag.RowsAffected(), nil
}
