package memory

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/embeddings"
)

// Status is the Index page: the embedder, what is indexed, what failed.
type Status struct {
	Model      string `json:"model,omitempty"`
	Dimensions int    `json:"dimensions,omitempty"`
	Endpoint   string `json:"endpoint,omitempty"`
	// The embedder as the indexer last found it: failing, since when.
	Health Health `json:"health"`
	// All documents, those waiting, and those that failed on their own (all
	// of them: Failures lists the first 50).
	Total    int       `json:"total"`
	Waiting  int       `json:"waiting"`
	Failed   int       `json:"failed"`
	Kinds    []Kind    `json:"kinds"`
	Failures []Failure `json:"failures"`
}

type Kind struct {
	Type     string `json:"type"`
	Total    int    `json:"total"`
	Embedded int    `json:"embedded"`
	Waiting  int    `json:"waiting"`
	Failed   int    `json:"failed"`
}

type Failure struct {
	Type     string    `json:"type"`
	ID       string    `json:"id"`
	Title    string    `json:"title"`
	Error    string    `json:"error"`
	Attempts int       `json:"attempts"`
	LastTry  time.Time `json:"lastTry"`
}

func IndexStatus(ctx context.Context, tx pgx.Tx, e embeddings.Embedder, health Health, project string) (Status, error) {
	s := Status{Kinds: []Kind{}, Failures: []Failure{}, Health: health}
	if e != nil {
		s.Model, s.Dimensions = e.Model(), e.Dimensions()
		// Where text is sent, when the embedder is a client that says (not the fake).
		if c, ok := e.(*embeddings.Client); ok {
			s.Endpoint = c.Endpoint()
		}
	}
	rows, err := tx.Query(ctx, `SELECT t.type,
			count(d.source_id),
			count(d.source_id) FILTER (WHERE d.embedding IS NOT NULL AND d.embedding_model = $1),
			count(d.source_id) FILTER (WHERE (d.embedding IS NULL OR d.embedding_model IS DISTINCT FROM $1) AND d.last_error IS NULL),
			count(d.source_id) FILTER (WHERE (d.embedding IS NULL OR d.embedding_model IS DISTINCT FROM $1) AND d.last_error IS NOT NULL)
		FROM unnest($3::text[]) WITH ORDINALITY AS t(type, n)
		LEFT JOIN search_documents d ON d.source_type = t.type AND ($2 = '' OR d.project_id = $2 OR d.project_id IS NULL)
		GROUP BY t.type, t.n ORDER BY t.n`, s.Model, project, Types)
	if err != nil {
		return s, err
	}
	for rows.Next() {
		var k Kind
		if err := rows.Scan(&k.Type, &k.Total, &k.Embedded, &k.Waiting, &k.Failed); err != nil {
			rows.Close()
			return s, err
		}
		s.Kinds = append(s.Kinds, k)
		s.Total += k.Total
		s.Waiting += k.Waiting
		s.Failed += k.Failed
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return s, err
	}
	rows, err = tx.Query(ctx, `SELECT source_type, source_id, title, last_error, attempts, coalesce(last_attempt_at, updated_at)
		FROM search_documents
		WHERE embedding IS NULL AND last_error IS NOT NULL AND ($1 = '' OR project_id = $1 OR project_id IS NULL)
		ORDER BY next_attempt_at LIMIT 50`, project)
	if err != nil {
		return s, err
	}
	failures, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Failure, error) {
		var f Failure
		return f, r.Scan(&f.Type, &f.ID, &f.Title, &f.Error, &f.Attempts, &f.LastTry)
	})
	if err != nil {
		return s, err
	}
	s.Failures = append(s.Failures, failures...)
	return s, nil
}
