// Package embeddings turns text into vectors for search by meaning.
//
// One interface, two implementations: OpenAI-compatible /v1/embeddings,
// which is what llm-proxy serves whatever the model behind it (Gemini's are
// translated there), and a deterministic fake for tests. The deployment
// configures it (DUDE_EMBEDDINGS_*); without it dude searches by words
// alone.
package embeddings

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
)

// Purpose tells a model whether it is embedding what will be searched or
// what is searched for. Gemini ranks better told; others ignore it.
type Purpose string

const (
	Document Purpose = "RETRIEVAL_DOCUMENT"
	Query    Purpose = "RETRIEVAL_QUERY"
)

// MaxBatch is the most texts one call may carry: Gemini's batch limit,
// which llm-proxy enforces for every Google-routed model.
const MaxBatch = 100

type Embedder interface {
	// Embed returns one vector per text, in order, each of Dimensions().
	Embed(ctx context.Context, texts []string, purpose Purpose) ([][]float32, error)
	// Model names what made the vectors: a change of model is a reindex.
	Model() string
	Dimensions() int
}

// Client is an OpenAI-compatible embeddings endpoint.
type Client struct {
	// BaseURL ends before /embeddings: https://llm.example/v1.
	BaseURL    string
	Key        string
	ModelName  string
	Dims       int
	HTTPClient *http.Client
}

func (c *Client) Model() string   { return c.ModelName }
func (c *Client) Dimensions() int { return c.Dims }

// Endpoint is where text is sent, for the Index page. Never the key.
func (c *Client) Endpoint() string { return strings.TrimRight(c.BaseURL, "/") + "/embeddings" }

type request struct {
	Model      string   `json:"model"`
	Input      []string `json:"input"`
	Dimensions int      `json:"dimensions,omitempty"`
	// Passed through to Gemini as taskType by llm-proxy; ignored elsewhere.
	TaskType string `json:"task_type,omitempty"`
}

type response struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
}

// Error is what the endpoint said when it refused: kept for the Index page.
type Error struct {
	Status int
	Body   string
}

func (e *Error) Error() string { return fmt.Sprintf("%d: %s", e.Status, e.Body) }

// OneBad reports a refusal that may be one text's fault: a 4xx that is not
// the key (401, 403), the address (404, 405) or a rate limit (429). The
// indexer finds out whose by embedding the batch one by one.
func OneBad(err error) bool {
	var e *Error
	if !errors.As(err, &e) || e.Status < 400 || e.Status >= 500 {
		return false
	}
	switch e.Status {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusTooManyRequests:
		return false
	}
	return true
}

func (c *Client) Embed(ctx context.Context, texts []string, purpose Purpose) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	if len(texts) > MaxBatch {
		return nil, fmt.Errorf("embeddings: %d texts, at most %d a call", len(texts), MaxBatch)
	}
	body, err := json.Marshal(request{Model: c.ModelName, Input: texts, Dimensions: c.Dims, TaskType: string(purpose)})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.BaseURL, "/")+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.Key)
	hc := c.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 60 * time.Second}
	}
	res, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 64<<20))
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		msg := strings.TrimSpace(string(raw))
		if len(msg) > 300 {
			msg = msg[:300] + "…"
		}
		return nil, &Error{Status: res.StatusCode, Body: msg}
	}
	var out response
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("embeddings: reading the answer: %w", err)
	}
	if len(out.Data) != len(texts) {
		return nil, fmt.Errorf("embeddings: %d vectors for %d texts", len(out.Data), len(texts))
	}
	vecs := make([][]float32, len(texts))
	for _, d := range out.Data {
		if d.Index < 0 || d.Index >= len(texts) || vecs[d.Index] != nil {
			return nil, fmt.Errorf("embeddings: vector index %d out of place", d.Index)
		}
		if len(d.Embedding) != c.Dims {
			return nil, fmt.Errorf("embeddings: %d dimensions, want %d (DUDE_EMBEDDINGS_DIMENSIONS)", len(d.Embedding), c.Dims)
		}
		vecs[d.Index] = normalize(d.Embedding)
	}
	return vecs, nil
}

// Gemini returns unit vectors only at its full size; below it they must be
// normalized, or cosine distance would still work but halfvec would lose
// precision on large components. Every vector is stored at unit length.
func normalize(v []float32) []float32 {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	if sum == 0 {
		return v
	}
	n := float32(1 / math.Sqrt(sum))
	for i := range v {
		v[i] *= n
	}
	return v
}

// Fake embeds by words: each word adds to a few hashed dimensions, so texts
// that share words are near and the rest are far. Deterministic, offline.
type Fake struct {
	Dims int
	// Fail, when set, is returned for any batch containing this text.
	Fail string
	// Calls counts batches, for tests that care how many were made.
	Calls int
}

func (f *Fake) Model() string   { return "fake" }
func (f *Fake) Dimensions() int { return f.Dims }

func (f *Fake) Embed(_ context.Context, texts []string, _ Purpose) ([][]float32, error) {
	f.Calls++
	out := make([][]float32, len(texts))
	for i, t := range texts {
		if f.Fail != "" && strings.Contains(t, f.Fail) {
			return nil, &Error{Status: http.StatusTooManyRequests, Body: "fake: refused " + f.Fail}
		}
		v := make([]float32, f.Dims)
		for _, w := range strings.FieldsFunc(strings.ToLower(t), func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
		}) {
			h := sha256.Sum256([]byte(w))
			for k := 0; k < 3; k++ {
				v[binary.BigEndian.Uint32(h[k*4:])%uint32(f.Dims)] += 1
			}
		}
		out[i] = normalize(v)
	}
	return out, nil
}
