package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/embeddings"
)

type embeddingRequest struct {
	Model, Auth string
	Input       []string
}

// embeddingServer answers every /embeddings call with one 768-dimension
// unit vector per input and records what it was asked.
func embeddingServer(t *testing.T) (*httptest.Server, func() []embeddingRequest) {
	t.Helper()
	var mu sync.Mutex
	var got []embeddingRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			http.NotFound(w, r)
			return
		}
		var body struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		got = append(got, embeddingRequest{Model: body.Model, Auth: r.Header.Get("Authorization"), Input: body.Input})
		mu.Unlock()
		type datum struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		}
		var data []datum
		for i := range body.Input {
			v := make([]float32, indexDimensions)
			v[i%indexDimensions] = 1
			data = append(data, datum{Index: i, Embedding: v})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	t.Cleanup(srv.Close)
	return srv, func() []embeddingRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]embeddingRequest(nil), got...)
	}
}

func TestEmbeddingSettingsReachTheEmbedder(t *testing.T) {
	for name, tc := range map[string]struct {
		vars          map[string]string
		model, bearer string
	}{
		"file": {model: "file-embedding-model", bearer: "Bearer file-emb-key"},
		"env override": {vars: map[string]string{"DUDE_EMBEDDINGS_MODEL": "env-embedding-model", "DUDE_EMBEDDINGS_KEY": "env-emb-key"},
			model: "env-embedding-model", bearer: "Bearer env-emb-key"},
	} {
		srv, requests := embeddingServer(t)
		set := mustSettings(t, loadConfig(t, required+`[embeddings]
url = "`+srv.URL+`/v1"
key = "file-emb-key"
model = "file-embedding-model"
`, 0o600, tc.vars))
		embedder, _ := memoryAndPush(set, nil, slog.New(slog.DiscardHandler))
		if embedder == nil {
			t.Fatalf("%s: no embedder", name)
		}
		vecs, err := embedder.Embed(context.Background(), []string{"hello memory"}, embeddings.Query)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		want := make([]float32, indexDimensions)
		want[0] = 1
		if len(vecs) != 1 || !reflect.DeepEqual(vecs[0], want) {
			t.Errorf("%s: vectors = %d, first %v; want the server's one", name, len(vecs), vecs)
		}
		got := requests()
		wantReq := []embeddingRequest{{Model: tc.model, Auth: tc.bearer, Input: []string{"hello memory"}}}
		if !reflect.DeepEqual(got, wantReq) {
			t.Errorf("%s: server received %+v, want %+v", name, got, wantReq)
		}
		if embedder.Model() != tc.model {
			t.Errorf("%s: model = %q, want %q", name, embedder.Model(), tc.model)
		}
	}
}

func TestVAPIDSettingsReachTheNotifier(t *testing.T) {
	for name, tc := range map[string]struct {
		vars                     map[string]string
		public, private, subject string
	}{
		"file": {public: "BFileKey", private: "file-private", subject: "mailto:file@example.com"},
		"env override": {vars: map[string]string{"DUDE_VAPID_PUBLIC_KEY": "BEnvKey",
			"DUDE_VAPID_PRIVATE_KEY": "env-private", "DUDE_VAPID_SUBJECT": "https://env.example.com"},
			public: "BEnvKey", private: "env-private", subject: "https://env.example.com"},
	} {
		set := mustSettings(t, loadConfig(t, required+`[vapid]
public_key = "BFileKey"
private_key = "file-private"
subject = "mailto:file@example.com"
`, 0o600, tc.vars))
		embedder, n := memoryAndPush(set, nil, slog.New(slog.DiscardHandler))
		if embedder != nil {
			t.Errorf("%s: an embedder with no embeddings URL", name)
		}
		if n.PublicKey != tc.public || n.PrivateKey != tc.private || n.Subject != tc.subject {
			t.Errorf("%s: notifier keys %q %q subject %q, want %q %q %q", name,
				n.PublicKey, n.PrivateKey, n.Subject, tc.public, tc.private, tc.subject)
		}
	}
}
