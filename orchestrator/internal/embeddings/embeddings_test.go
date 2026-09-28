package embeddings_test

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/embeddings"
)

func TestTheClientSpeaksOpenAIsEmbeddingsAndKeepsTheOrder(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" || r.Header.Get("Authorization") != "Bearer k" {
			http.Error(w, "wrong path or key", http.StatusBadRequest)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		// Out of order, and not unit length, as a provider may answer.
		_, _ = w.Write([]byte(`{"data":[{"index":1,"embedding":[0,3,4]},{"index":0,"embedding":[2,0,0]}]}`))
	}))
	defer srv.Close()

	c := &embeddings.Client{BaseURL: srv.URL + "/v1/", Key: "k", ModelName: "gemini-embedding-2", Dims: 3}
	vecs, err := c.Embed(context.Background(), []string{"a", "b"}, embeddings.Query)
	if err != nil {
		t.Fatal(err)
	}
	if got["model"] != "gemini-embedding-2" || got["dimensions"] != float64(3) || got["task_type"] != "RETRIEVAL_QUERY" {
		t.Errorf("request = %v", got)
	}
	if vecs[0][0] != 1 || math.Abs(float64(vecs[1][1])-0.6) > 1e-6 || math.Abs(float64(vecs[1][2])-0.8) > 1e-6 {
		t.Errorf("vectors not in input order at unit length: %v", vecs)
	}
}

func TestARefusalSaysWhetherToTryAgain(t *testing.T) {
	for status, retry := range map[int]bool{429: true, 502: true, 400: false, 401: false} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "no", status)
		}))
		c := &embeddings.Client{BaseURL: srv.URL, Key: "k", ModelName: "m", Dims: 3}
		_, err := c.Embed(context.Background(), []string{"a"}, embeddings.Document)
		srv.Close()
		if err == nil || embeddings.Retryable(err) != retry {
			t.Errorf("%d: err %v, retryable %v, want %v", status, err, embeddings.Retryable(err), retry)
		}
	}
}

func TestAWrongSizeIsRefusedNotStored(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"index":0,"embedding":[1,0]}]}`))
	}))
	defer srv.Close()
	c := &embeddings.Client{BaseURL: srv.URL, Key: "k", ModelName: "m", Dims: 3}
	if _, err := c.Embed(context.Background(), []string{"a"}, embeddings.Document); err == nil {
		t.Error("a 2-dimensional vector was accepted for a 3-dimensional index")
	}
}

func TestTheFakePutsSharedWordsNear(t *testing.T) {
	f := &embeddings.Fake{Dims: 64}
	v, _ := f.Embed(context.Background(), []string{"webhook delivery retries", "retries of a webhook delivery", "font loading"}, embeddings.Document)
	dot := func(a, b []float32) (s float32) {
		for i := range a {
			s += a[i] * b[i]
		}
		return s
	}
	if dot(v[0], v[1]) <= dot(v[0], v[2]) {
		t.Errorf("shared words are no nearer: %.2f vs %.2f", dot(v[0], v[1]), dot(v[0], v[2]))
	}
}
