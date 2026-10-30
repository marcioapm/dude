package lux_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// A Run's checkpoints as lux lists them: {"checkpoints": [...]}, with
// fields dude does not read left alone.
func TestCheckpointsDecodes(t *testing.T) {
	url, path := costServer(t, 200, `{"checkpoints":[
		{"repo":"api","head":"0123456789abcdef0123456789abcdef01234567","uploaded":true,"dirty":false,"branch":"dude/T/run-r","epoch":2},
		{"repo":"web","head":"89abcdef0123456789abcdef0123456789abcdef","uploaded":false,"dirty":true}]}`)
	cps, err := lux.New(url, "k").Checkpoints(context.Background(), "lrun_1")
	if err != nil {
		t.Fatal(err)
	}
	if *path != "GET /v1/runs/lrun_1/checkpoints" {
		t.Errorf("asked %s", *path)
	}
	want := []lux.Checkpoint{
		{Repo: "api", Head: "0123456789abcdef0123456789abcdef01234567", Uploaded: true, Branch: "dude/T/run-r"},
		{Repo: "web", Head: "89abcdef0123456789abcdef0123456789abcdef", Dirty: true},
	}
	if len(cps) != 2 || cps[0] != want[0] || cps[1] != want[1] {
		t.Errorf("checkpoints = %+v", cps)
	}
}

// lux refusing (an unknown Run) is lux's error, not an empty list.
func TestCheckpointsOfAnUnknownRunIsNotFound(t *testing.T) {
	url, _ := costServer(t, 404, `{"error":{"code":"not_found","message":"no run"}}`)
	_, err := lux.New(url, "k").Checkpoints(context.Background(), "lrun_x")
	if !lux.IsNotFound(err) {
		t.Errorf("err = %v", err)
	}
}

// A repository cloned from a checkpoint is sent as lux takes it: its name,
// from {run, repo}, its credential, and no url.
func TestSubmitSendsCheckpointRepository(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"id":"lrun_2","state":"submitted"}`))
	}))
	t.Cleanup(srv.Close)
	spec := lux.Spec{
		Workload:     lux.Workload{Adapter: "generic", Command: []string{"sleep", "infinity"}},
		ResumePolicy: "never",
		Secrets:      []lux.Secret{{Name: "GIT_TOKEN", Value: "t", As: "none"}},
		Git: &lux.Git{Repositories: []lux.Repository{{Name: "api", From: &lux.RepositoryFrom{Run: "lrun_1", Repo: "api"},
			Credential: "GIT_TOKEN"}}, Push: &lux.Push{Branch: "dude/T/recovered-r"}},
	}
	if _, err := lux.New(srv.URL, "k").Submit(context.Background(), spec, "recover:rcv_1"); err != nil {
		t.Fatal(err)
	}
	var got struct {
		ResumePolicy string `json:"resumePolicy"`
		Secrets      []map[string]any
		Git          struct {
			Repositories []map[string]any `json:"repositories"`
			Push         map[string]any   `json:"push"`
		} `json:"git"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	repo := got.Git.Repositories[0]
	from, _ := repo["from"].(map[string]any)
	if _, hasURL := repo["url"]; hasURL || repo["name"] != "api" || repo["credential"] != "GIT_TOKEN" ||
		from["run"] != "lrun_1" || from["repo"] != "api" {
		t.Errorf("repository sent as %v", repo)
	}
	if got.ResumePolicy != "never" || got.Git.Push["branch"] != "dude/T/recovered-r" || got.Secrets[0]["as"] != "none" {
		t.Errorf("sent %s", body)
	}
}
