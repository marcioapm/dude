package memory

import (
	"context"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/embeddings"
)

// Query is one search. The organization is the transaction's (RLS); a
// project narrows to that project and its organization's memories.
type Query struct {
	Text    string
	Project string
	// Types to include: memory, task, epic, project. Empty: all.
	Types []string
	// About narrows memories to those about any of these ids (tasks, epics,
	// projects); other types are unaffected.
	About []string
	Limit int
}

// Result is one found document, with why it ranked where it did.
type Result struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	ProjectID string `json:"projectId,omitempty"`
	// A task's key and status, filled by callers that show them.
	Key     string `json:"key,omitempty"`
	Status  string `json:"status,omitempty"`
	Title   string `json:"title"`
	Snippet string `json:"snippet"`
	// 1-based rank in each list, 0 when not in it.
	TextRank   int     `json:"textRank"`
	TextScore  float64 `json:"textScore,omitempty"`
	VectorRank int     `json:"vectorRank"`
	Distance   float64 `json:"distance,omitempty"`
	Score      float64 `json:"score"`
	// Embedded: its meaning is searchable too; not yet when false.
	Embedded bool `json:"embedded"`
}

// Outcome is the list and how it was searched.
type Outcome struct {
	Results []Result `json:"results"`
	// "words" when meaning was not searched: no embedder, or it failed.
	Mode  string `json:"mode"`
	Model string `json:"model,omitempty"`
	// Why meaning was not searched, when it was meant to be.
	Degraded string `json:"degraded,omitempty"`
}

// rrfK is reciprocal-rank fusion's constant: large enough that being first
// in one list does not outweigh being found by both.
const rrfK = 60

// candidates is how far down each list fusion looks.
const candidates = 50

// Search ranks by words and by meaning apart, then fuses the two by rank.
// With no embedder, or when the query cannot be embedded, it is by words
// alone and says so.
func Search(ctx context.Context, tx pgx.Tx, e embeddings.Embedder, q Query) (Outcome, error) {
	out := Outcome{Mode: "words"}
	q.Text = strings.TrimSpace(q.Text)
	if q.Limit <= 0 || q.Limit > 50 {
		q.Limit = 10
	}
	if q.Text == "" {
		out.Results = []Result{}
		return out, nil
	}
	types := q.Types
	if len(types) == 0 {
		types = []string{"memory", "task", "epic", "project"}
	}
	// A document is in scope when it is the project's, or the whole
	// organization's (memories only), and a memory, when About is given, is
	// about one of those.
	const scope = `source_type = ANY($2) AND ($3 = '' OR project_id = $3 OR project_id IS NULL)
		AND (coalesce(cardinality($4::text[]), 0) = 0 OR source_type <> 'memory' OR EXISTS (
			SELECT 1 FROM memory_refs r WHERE r.memory_id = d.source_id AND r.ref_id = ANY($4)))`

	byKey := map[string]*Result{}
	take := func(typ, id string) *Result {
		k := typ + "/" + id
		if r := byKey[k]; r != nil {
			return r
		}
		r := &Result{Type: typ, ID: id}
		byKey[k] = r
		return r
	}

	// Any of the words, not all: an agent asks in sentences, and ranking
	// puts the documents with more of them first. Quoted phrases and -words
	// keep their meaning: only an AND before a plain word becomes an OR, not
	// one before a negation. English stems (deliveries → deliveri) and the
	// simple words (keys, names) both.
	rows, err := tx.Query(ctx, `WITH q AS (SELECT
			regexp_replace(websearch_to_tsquery('english', $1)::text, ' & (?!!)', ' | ', 'g')::tsquery
			|| regexp_replace(websearch_to_tsquery('simple', $1)::text, ' & (?!!)', ' | ', 'g')::tsquery AS q)
		SELECT source_type, source_id, coalesce(project_id, ''), title,
			ts_headline('english', body, q.q, 'MaxFragments=1,MaxWords=24,MinWords=8,StartSel=⟦,StopSel=⟧'),
			ts_rank_cd(tsv, q.q), embedding IS NOT NULL
		FROM search_documents d, q
		WHERE numnode(q.q) > 0 AND tsv @@ q.q AND `+scope+`
		ORDER BY 6 DESC, updated_at DESC LIMIT $5`, q.Text, types, q.Project, q.About, candidates)
	if err != nil {
		return out, err
	}
	rank := 0
	for rows.Next() {
		var typ, id, project, title, snippet string
		var score float64
		var embedded bool
		if err := rows.Scan(&typ, &id, &project, &title, &snippet, &score, &embedded); err != nil {
			rows.Close()
			return out, err
		}
		rank++
		r := take(typ, id)
		r.ProjectID, r.Title, r.Snippet, r.Embedded = project, title, snippet, embedded
		r.TextRank, r.TextScore = rank, score
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}

	if e != nil {
		vecs, err := e.Embed(ctx, []string{q.Text}, embeddings.Query)
		if err != nil {
			out.Degraded = err.Error()
		} else {
			out.Mode, out.Model = "hybrid", e.Model()
			// The HNSW index returns its nearest before the organization and
			// scope filter them; let it keep looking until enough are left.
			if _, err := tx.Exec(ctx, `SET LOCAL hnsw.iterative_scan = relaxed_order`); err != nil {
				return out, err
			}
			rows, err := tx.Query(ctx, `SELECT source_type, source_id, coalesce(project_id, ''), title, left(body, 200),
					embedding <=> $1::halfvec
				FROM search_documents d
				WHERE embedding IS NOT NULL AND embedding_model = $6 AND `+scope+`
				ORDER BY embedding <=> $1::halfvec LIMIT $5`, Vector(vecs[0]), types, q.Project, q.About, candidates, e.Model())
			if err != nil {
				return out, err
			}
			rank := 0
			for rows.Next() {
				var typ, id, project, title, body string
				var dist float64
				if err := rows.Scan(&typ, &id, &project, &title, &body, &dist); err != nil {
					rows.Close()
					return out, err
				}
				rank++
				r := take(typ, id)
				if r.TextRank == 0 {
					r.ProjectID, r.Title, r.Snippet = project, title, body
				}
				r.Embedded, r.VectorRank, r.Distance = true, rank, dist
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return out, err
			}
		}
	}

	out.Results = make([]Result, 0, len(byKey))
	for _, r := range byKey {
		if r.TextRank > 0 {
			r.Score += 1 / float64(rrfK+r.TextRank)
		}
		if r.VectorRank > 0 {
			r.Score += 1 / float64(rrfK+r.VectorRank)
		}
		out.Results = append(out.Results, *r)
	}
	sort.Slice(out.Results, func(i, j int) bool {
		a, b := out.Results[i], out.Results[j]
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		return a.Type+a.ID < b.Type+b.ID
	})
	if len(out.Results) > q.Limit {
		out.Results = out.Results[:q.Limit]
	}
	return out, nil
}
