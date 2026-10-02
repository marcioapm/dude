package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

var quietLog = slog.New(slog.NewTextHandler(io.Discard, nil))

func domainOf(c *lux.HTTPClient, configured string) (string, error) {
	mode, err := previewModeOf(context.Background(), c, configured, quietLog, time.Millisecond)
	return mode.Domain, err
}

func TestPreviewCapabilityModes(t *testing.T) {
	for _, tc := range []struct {
		name, whoami, configured, domain string
		relative, refused, ignored       bool
	}{
		{"relative", `{"previews":true,"previewDomain":null}`, "", "", true, false, false},
		{"stopgap", `{"previews":true,"previewDomain":null}`, "preview.test", "", true, false, true},
		{"mismatch", `{"previews":true,"previewDomain":"preview.test"}`, "other.test", "", false, true, false},
		{"matching", `{"previews":true,"previewDomain":"Preview.Test."}`, "preview.test", "", true, false, false},
		{"old-domain", `{"previewDomain":"Preview.Test."}`, "", "preview.test", false, false, false},
		{"old-env", `{"previewDomain":null}`, "preview.test", "preview.test", false, false, false},
		{"old-off", `{"previewDomain":null}`, "", "", false, false, false},
		{"off", `{"previews":false,"previewDomain":null}`, "preview.test", "", false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/v1/whoami" {
					_, _ = io.WriteString(w, tc.whoami)
				} else {
					_, _ = io.WriteString(w, `{"servers":[]}`)
				}
			}))
			defer srv.Close()
			var logs bytes.Buffer
			log := slog.New(slog.NewTextHandler(&logs, nil))
			mode, err := previewModeOf(context.Background(), lux.New(srv.URL, "k"), tc.configured, log, time.Millisecond)
			if (err != nil) != tc.refused {
				t.Fatalf("mode = %+v, error = %v", mode, err)
			}
			if err == nil && (mode.Domain != tc.domain || mode.Relative != tc.relative) {
				t.Fatalf("mode = %+v", mode)
			}
			if ignored := strings.Contains(logs.String(), "level=INFO") && strings.Contains(logs.String(), "DUDE_PREVIEW_DOMAIN ignored"); ignored != tc.ignored {
				t.Fatalf("logs = %s", logs.String())
			}
		})
	}
}

// A lux from before the server resource (no /v1/servers: 404, or 405 from
// a router that knows the path for another method) stops startup, naming
// the lux release dude needs.
func TestALuxWithoutServersStopsStartup(t *testing.T) {
	for _, status := range []int{404, 405} {
		old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":{"code":"not_found","message":"no route"}}`))
		}))
		// Bounded: a refusal read as "not answering" would be asked forever.
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, err := previewModeOf(ctx, lux.New(old.URL, "k"), "", quietLog, time.Millisecond)
		cancel()
		old.Close()
		if !errors.Is(err, lux.ErrNoServers) || !strings.Contains(err.Error(), "/v1/servers") || !strings.Contains(err.Error(), "lux#41") {
			t.Errorf("%d: err = %v", status, err)
		}
	}
}

// A lux not answering at startup (a 503 while it restarts) is asked again
// rather than stopping the orchestrator.
func TestALuxNotAnsweringAtStartupIsAskedAgain(t *testing.T) {
	fake := fakelux.New(t.TempDir(), "k", nil)
	defer fake.Close()
	fake.PreviewDomain = "preview.test"
	var asked atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if asked.Add(1) <= 2 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(503)
			_, _ = w.Write([]byte(`{"error":{"code":"unavailable","message":"restarting"}}`))
			return
		}
		fake.Handler().ServeHTTP(w, r)
	}))
	defer srv.Close()
	d, err := domainOf(lux.New(srv.URL, "k"), "")
	if err != nil || d != "preview.test" {
		t.Fatalf("%q %v after two 503s", d, err)
	}
	// Given up only when the orchestrator is stopping.
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer down.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := previewModeOf(ctx, lux.New(down.URL, "k"), "", quietLog, time.Millisecond); err == nil || errors.Is(err, lux.ErrNoServers) {
		t.Errorf("a lux down until shutdown: %v", err)
	}
}

func TestThePreviewDomainIsLuxsOrMatchesIt(t *testing.T) {
	fake := fakelux.New(t.TempDir(), "k", nil)
	defer fake.Close()
	srv := httptest.NewServer(fake.Handler())
	defer srv.Close()
	c := lux.New(srv.URL, "k")
	if d, err := domainOf(c, ""); err != nil || d != "" {
		t.Errorf("previews off in lux, none configured: %q %v", d, err)
	}
	fake.PreviewDomain = "Preview-Absmartly.dev."
	if d, err := domainOf(c, ""); err != nil || d != "preview-absmartly.dev" {
		t.Errorf("lux's: %q %v", d, err)
	}
	if d, err := domainOf(c, "preview-absmartly.dev"); err != nil || d != "preview-absmartly.dev" {
		t.Errorf("matching: %q %v", d, err)
	}
	if _, err := domainOf(c, "other.dev"); err == nil || !strings.Contains(err.Error(), "previews.domain") {
		t.Errorf("another domain: %v", err)
	}
}
