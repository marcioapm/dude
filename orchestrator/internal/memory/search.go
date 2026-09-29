package memory

import (
	"context"
	"sort"
	"strings"
	"time"

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
// It embeds the query itself: for a caller without a transaction open.
// Inside one, embed first (EmbedQuery) and call Ranked, so no connection
// is held while the embedder answers.
func Search(ctx context.Context, tx pgx.Tx, e embeddings.Embedder, q Query) (Outcome, error) {
	return Ranked(ctx, tx, EmbedQuery(ctx, e, q.Text), q)
}

// Embedded is a query's vector, or why there is none.
type Embedded struct {
	Model  string
	Vector []float32
	// Why meaning is not searched, when it was meant to be.
	Degraded string
}

// queryTimeout is how long a search waits for its query's embedding before
// searching by words alone: well under the backend's 15s for the whole
// request, and short for an agent waiting on it.
const queryTimeout = 5 * time.Second

// EmbedQuery embeds the words searched for. With no embedder, or when it
// fails or is slow, search is by words alone and says why.
func EmbedQuery(ctx context.Context, e embeddings.Embedder, text string) Embedded {
	text = strings.TrimSpace(text)
	if e == nil || text == "" {
		return Embedded{}
	}
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	vecs, err := e.Embed(ctx, []string{text}, embeddings.Query)
	if err != nil {
		return Embedded{Degraded: err.Error()}
	}
	return Embedded{Model: e.Model(), Vector: vecs[0]}
}

// wordQuery is the query's words for Postgres: any of the plain words and
// quoted phrases (an agent asks in sentences; ranking puts documents with
// more of them first), and none of the -words. In English stems
// (deliveries → deliveri) and as the simple words (keys, names) both.
func wordQuery(text string) (any string, none string) {
	var pos, neg []string
	for _, t := range terms(text) {
		if strings.HasPrefix(t, "-") && len(t) > 1 {
			neg = append(neg, t[1:])
		} else {
			pos = append(pos, t)
		}
	}
	return strings.Join(pos, " or "), strings.Join(neg, " or ")
}

// terms splits websearch syntax: "quoted phrases" stay whole (with their
// quotes, and a leading - with them), the rest is split on spaces.
func terms(text string) []string {
	var out []string
	for text = strings.TrimSpace(text); text != ""; text = strings.TrimSpace(text) {
		neg := strings.HasPrefix(text, "-\"")
		if neg || strings.HasPrefix(text, "\"") {
			start := 1
			if neg {
				start = 2
			}
			if end := strings.Index(text[start:], "\""); end >= 0 {
				out = append(out, text[:start+end+1])
				text = text[start+end+1:]
				continue
			}
		}
		word, rest, _ := strings.Cut(text, " ")
		if w := strings.Trim(word, "\""); w != "" && w != "-" && !strings.EqualFold(w, "or") {
			out = append(out, word)
		}
		text = rest
	}
	return out
}

// Ranked searches with a query already embedded (or not): words and
// meaning apart, fused by rank.
func Ranked(ctx context.Context, tx pgx.Tx, emb Embedded, q Query) (Outcome, error) {
	out := Outcome{Mode: "words", Degraded: emb.Degraded}
	q.Text = strings.TrimSpace(q.Text)
	if q.Limit <= 0 {
		q.Limit = 10
	}
	q.Limit = min(q.Limit, 50)
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
	any, none := wordQuery(q.Text)

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

	// $6 excludes: a document with any -word is out of both lists.
	const excluded = `($6 = '' OR NOT tsv @@ (websearch_to_tsquery('english', $6) || websearch_to_tsquery('simple', $6)))`
	rows, err := tx.Query(ctx, `WITH q AS (SELECT websearch_to_tsquery('english', $1) || websearch_to_tsquery('simple', $1) AS q)
		SELECT source_type, source_id, coalesce(project_id, ''), title,
			ts_headline('english', body, q.q, 'MaxFragments=1,MaxWords=24,MinWords=8,StartSel=⟦,StopSel=⟧'),
			ts_rank_cd(tsv, q.q), embedding IS NOT NULL
		FROM search_documents d, q
		WHERE numnode(q.q) > 0 AND tsv @@ q.q AND `+excluded+` AND `+scope+`
		ORDER BY 6 DESC, updated_at DESC LIMIT $5`, any, types, q.Project, q.About, candidates, none)
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

	if emb.Vector != nil {
		out.Mode, out.Model = "hybrid", emb.Model
		// The HNSW index returns its nearest before the organization and
		// scope filter them; let it keep looking until enough are left.
		// pgvector before 0.8 has no such setting: without it, search still
		// works, and may find fewer by meaning in a large index.
		if _, err := tx.Exec(ctx, `DO $$ BEGIN
				IF (SELECT string_to_array(extversion, '.')::int[] >= '{0,8}' FROM pg_extension WHERE extname = 'vector') THEN
					SET LOCAL hnsw.iterative_scan = relaxed_order;
				END IF;
			END $$`); err != nil {
			return out, err
		}
		rows, err := tx.Query(ctx, `SELECT source_type, source_id, coalesce(project_id, ''), title, left(body, 200),
				embedding <=> $1::halfvec
			FROM search_documents d
			WHERE embedding IS NOT NULL AND embedding_model = $7 AND `+excluded+` AND `+scope+`
			ORDER BY embedding <=> $1::halfvec LIMIT $5`, Vector(emb.Vector), types, q.Project, q.About, candidates, none, emb.Model)
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
