package lux_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// Every version of each artifact is asked for, and each comes with its
// version and description, as lux#77 lists them.
func TestArtifactsListsEveryVersion(t *testing.T) {
	var asked string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = r.Method + " " + r.URL.RequestURI()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"artifacts":[
			{"id":"art_aaaaaaaaaaaaaaaa","epoch":1,"path":"/.lux/artifacts/notes.md","version":1,"description":"First draft","size":3,"sha256":"a","available":true},
			{"id":"art_bbbbbbbbbbbbbbbb","epoch":1,"path":"/.lux/artifacts/notes.md","version":2,"description":"With the numbers","size":4,"sha256":"b","available":true}]}`))
	}))
	t.Cleanup(srv.Close)
	got, err := lux.New(srv.URL, "k").Artifacts(context.Background(), "run_1")
	if err != nil {
		t.Fatal(err)
	}
	if asked != "GET /v1/runs/run_1/artifacts?versions=all" {
		t.Errorf("asked %s", asked)
	}
	if len(got) != 2 || got[0].Version != 1 || got[1].Version != 2 || got[1].Description != "With the numbers" {
		t.Errorf("listed %+v", got)
	}
}
