package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// A lux from before the server resource (no /v1/servers) stops startup,
// naming the lux release dude needs.
func TestALuxWithoutServersStopsStartup(t *testing.T) {
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"error":{"code":"not_found","message":"no route"}}`))
	}))
	defer old.Close()
	_, err := previewDomainOf(context.Background(), lux.New(old.URL, "k"), "")
	if err == nil || !strings.Contains(err.Error(), "/v1/servers") || !strings.Contains(err.Error(), "lux#41") {
		t.Fatalf("err = %v", err)
	}
}

func TestThePreviewDomainIsLuxsOrMatchesIt(t *testing.T) {
	fake := fakelux.New(t.TempDir(), "k", nil)
	defer fake.Close()
	srv := httptest.NewServer(fake.Handler())
	defer srv.Close()
	c := lux.New(srv.URL, "k")
	if d, err := previewDomainOf(context.Background(), c, ""); err != nil || d != "" {
		t.Errorf("previews off in lux, none configured: %q %v", d, err)
	}
	fake.PreviewDomain = "Preview-Absmartly.dev"
	if d, err := previewDomainOf(context.Background(), c, ""); err != nil || d != "preview-absmartly.dev" {
		t.Errorf("lux's: %q %v", d, err)
	}
	if d, err := previewDomainOf(context.Background(), c, "preview-absmartly.dev"); err != nil || d != "preview-absmartly.dev" {
		t.Errorf("matching: %q %v", d, err)
	}
	if _, err := previewDomainOf(context.Background(), c, "other.dev"); err == nil || !strings.Contains(err.Error(), "previews.domain") {
		t.Errorf("another domain: %v", err)
	}
}
