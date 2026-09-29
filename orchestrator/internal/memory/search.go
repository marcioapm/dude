package memory

import (
	"context"
	"sort"
	"strings"
	"sync"
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
		types = Types
	}
	// A document is in scope when it is the project's, or the whole
	// organization's (memories only).
	const scope = `source_type = ANY($2) AND ($3 = '' OR project_id = $3 OR project_id IS NULL)`
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

	// $4 excludes: a document with any -word is out of both lists.
	const excluded = `($4 = '' OR NOT tsv @@ (websearch_to_tsquery('english', $4) || websearch_to_tsquery('simple', $4)))`
	// Ranked by rank alone: what is shown (snippet, key) is read for the
	// few results that survive fusion, not for every candidate.
	rows, err := tx.Query(ctx, `WITH q AS (SELECT websearch_to_tsquery('english', $1) || websearch_to_tsquery('simple', $1) AS q)
		SELECT source_type, source_id, ts_rank_cd(tsv, q.q)
		FROM search_documents d, q
		WHERE numnode(q.q) > 0 AND tsv @@ q.q AND `+excluded+` AND `+scope+`
		ORDER BY 3 DESC, updated_at DESC LIMIT $5`, any, types, q.Project, none, candidates)
	if err != nil {
		return out, err
	}
	rank := 0
	for rows.Next() {
		var typ, id string
		var score float64
		if err := rows.Scan(&typ, &id, &score); err != nil {
			rows.Close()
			return out, err
		}
		rank++
		r := take(typ, id)
		r.TextRank, r.TextScore = rank, score
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}

	if emb.Vector != nil {
		out.Mode, out.Model = "hybrid", emb.Model
		// The HNSW index returns its nearest before the organization and
		// scope filter them; let it keep looking until enough are left
		// (pgvector 0.8+; before it, search works and finds fewer).
		if iterativeScan(ctx, tx) {
			if _, err := tx.Exec(ctx, `SET LOCAL hnsw.iterative_scan = relaxed_order`); err != nil {
				return out, err
			}
		}
		rows, err := tx.Query(ctx, `SELECT source_type, source_id, embedding <=> $1::halfvec
			FROM search_documents d
			WHERE embedding IS NOT NULL AND embedding_model = $6 AND `+excluded+` AND `+scope+`
			ORDER BY embedding <=> $1::halfvec LIMIT $5`, Vector(emb.Vector), types, q.Project, none, candidates, emb.Model)
		if err != nil {
			return out, err
		}
		rank := 0
		for rows.Next() {
			var typ, id string
			var dist float64
			if err := rows.Scan(&typ, &id, &dist); err != nil {
				rows.Close()
				return out, err
			}
			rank++
			r := take(typ, id)
			r.VectorRank, r.Distance = rank, dist
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
	return out, show(ctx, tx, any, out.Results)
}

// show reads what is shown of the results that survived: title, project,
// a snippet around the words (or the start, found by meaning alone), and a
// task's key and status. One query, for at most Limit documents.
func show(ctx context.Context, tx pgx.Tx, words string, results []Result) error {
	if len(results) == 0 {
		return nil
	}
	types, ids := make([]string, len(results)), make([]string, len(results))
	at := map[string]int{}
	for i, r := range results {
		types[i], ids[i] = r.Type, r.ID
		at[r.Type+"/"+r.ID] = i
	}
	rows, err := tx.Query(ctx, `WITH q AS (SELECT websearch_to_tsquery('english', $3) || websearch_to_tsquery('simple', $3) AS q)
		SELECT d.source_type, d.source_id, coalesce(d.project_id, ''), d.title, d.embedding IS NOT NULL,
			CASE WHEN numnode(q.q) > 0 AND d.tsv @@ q.q
				THEN ts_headline('english', d.body, q.q, 'MaxFragments=1,MaxWords=24,MinWords=8,StartSel="",StopSel=""')
				ELSE left(d.body, 200) END,
			coalesce(p.key_prefix || '-' || t.number, ''), coalesce(t.status::text, '')
		FROM unnest($1::text[], $2::text[]) u(typ, id)
		JOIN search_documents d ON d.source_type = u.typ AND d.source_id = u.id
		LEFT JOIN tasks t ON u.typ = 'task' AND t.id = u.id
		LEFT JOIN projects p ON p.id = t.project_id, q`, types, ids, words)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var typ, id, project, title, snippet, key, status string
		var embedded bool
		if err := rows.Scan(&typ, &id, &project, &title, &embedded, &snippet, &key, &status); err != nil {
			return err
		}
		r := &results[at[typ+"/"+id]]
		r.ProjectID, r.Snippet, r.Embedded, r.Key, r.Status = project, markdownless.Replace(snippet), embedded, key, status
		// The index titles a task "KEY title"; the key has its own field.
		r.Title = strings.TrimPrefix(title, key+" ")
	}
	return rows.Err()
}

// markdownless drops Markdown's emphasis and code marks from a snippet: a
// memory is Markdown, and a snippet is read as a line of text.
var markdownless = strings.NewReplacer("**", "", "__", "", "`", "")

// Types are what the index holds, in the order pages list them.
var Types = []string{"memory", "task", "epic", "project"}

var (
	scanOnce sync.Once
	scanOK   bool
)

// iterativeScan: whether this pgvector has hnsw.iterative_scan (0.8+).
// Asked once; the extension does not change under a running process.
func iterativeScan(ctx context.Context, tx pgx.Tx) bool {
	scanOnce.Do(func() {
		_ = tx.QueryRow(ctx, `SELECT coalesce((SELECT string_to_array(extversion, '.')::int[] >= '{0,8}'
			FROM pg_extension WHERE extname = 'vector'), false)`).Scan(&scanOK)
	})
	return scanOK
}
