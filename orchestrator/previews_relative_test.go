package orchestrator_test

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
	"github.com/marciomartins/dude/orchestrator/internal/servers"
)

func TestFakeLuxRelativePreviewHostname(t *testing.T) {
	fake := fakelux.New(t.TempDir(), "key", nil)
	fake.PreviewDomain = previewDomain
	enabled := true
	fake.Previews = &enabled
	httpServer := httptest.NewServer(fake.Handler())
	t.Cleanup(httpServer.Close)
	t.Cleanup(fake.Close)
	client := lux.New(httpServer.URL, "key")
	ctx := context.Background()
	sv, err := client.CreateServer(ctx, lux.CreateServer{Name: "web", Port: 3000, Hostname: "web-task-project", Wake: "request"})
	if err != nil {
		t.Fatal(err)
	}
	full := "web-task-project." + previewDomain
	if sv.Hostname == nil || *sv.Hostname != full || sv.URL == nil || *sv.URL != "https://"+full {
		t.Fatalf("server = %+v", sv)
	}
	for _, hostname := range []string{"web-task-project", full} {
		found, err := client.ListServers(ctx, hostname)
		if err != nil || len(found) != 1 || found[0].ID != sv.ID {
			t.Fatalf("lookup %q = %+v, %v", hostname, found, err)
		}
	}
	if _, err := client.CreateServer(ctx, lux.CreateServer{Name: "web", Port: 3000, Hostname: full, Wake: "request"}); err == nil {
		t.Fatal("relative and full names did not conflict")
	} else if le, ok := lux.AsError(err); !ok || le.Status != 409 || le.Code != "hostname_taken" {
		t.Fatalf("conflict = %v", err)
	}
	// lux takes one relative label only: a dotted name must already be under the domain.
	for _, dotted := range []string{"api.task", "api."} {
		_, err := client.CreateServer(ctx, lux.CreateServer{Name: "api", Port: 3001, Hostname: dotted, Wake: "request"})
		if le, ok := lux.AsError(err); !ok || le.Status != 422 || le.Code != "invalid_server" {
			t.Fatalf("dotted relative %q = %v", dotted, err)
		}
	}
	if fake.RequestServer(sv.ID, "/relative") {
		t.Fatal("an asleep server served a request")
	}
	feedCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	var wake *lux.FeedEvent
	_ = client.Feed(feedCtx, 0, func(e lux.FeedEvent) error {
		if e.Type == "server.wake_requested" && e.ServerID == sv.ID {
			wake = &e
			cancel()
		}
		return nil
	})
	if wake == nil || wake.Data["hostname"] != full || wake.Data["url"] != "https://"+full || wake.Data["path"] != "/relative" {
		t.Fatalf("wake event = %+v", wake)
	}
}

func TestPreviewHostnameWithoutDomainIsBare(t *testing.T) {
	if got := servers.PreviewHostname("", "web", "task", "project", ""); got != "web-task-project" {
		t.Fatalf("hostname = %q", got)
	}
}

type relativePreviewLux struct {
	lux.Client
	creates, lookups []string
}

func (c *relativePreviewLux) CreateServer(ctx context.Context, in lux.CreateServer) (lux.TenantServer, error) {
	c.creates = append(c.creates, in.Hostname)
	if strings.Contains(in.Hostname, ".") {
		return lux.TenantServer{}, fmt.Errorf("expected relative hostname, got %q", in.Hostname)
	}
	return c.Client.CreateServer(ctx, in)
}

func (c *relativePreviewLux) ListServers(ctx context.Context, hostname string, labels ...string) ([]lux.TenantServer, error) {
	if hostname != "" {
		c.lookups = append(c.lookups, hostname)
		if strings.Contains(hostname, ".") {
			return nil, fmt.Errorf("expected relative lookup, got %q", hostname)
		}
	}
	return c.Client.ListServers(ctx, hostname, labels...)
}

func TestRelativePreviewCreationAdoptionAndSaltedRetry(t *testing.T) {
	for _, mode := range []string{"create", "both", "adopt", "race", "salt"} {
		t.Run(mode, func(t *testing.T) {
			w := newWorld(t)
			w.wakeable()
			enabled := true
			w.lux.Previews = &enabled
			w.previews.PreviewDomain = ""
			w.previews.PreviewRelative = true
			if mode == "both" {
				w.previews.PreviewDomain = previewDomain
			}
			w.recipe("web", 3000, "npm run dev", "", nil, true)
			task, runID := w.startPreview()
			plain := servers.PreviewLabel("web", task, w.project, "")
			want := plain
			var existing lux.TenantServer
			if mode != "create" && mode != "both" {
				owner := runID
				if mode == "salt" {
					owner = "another-preview"
					want = servers.PreviewLabel("web", task, w.project, runID)
				}
				var err error
				existing, err = w.previews.Lux.CreateServer(context.Background(), lux.CreateServer{Name: "web", Port: 3000, Command: []string{"sh", "-c", "npm run dev"},
					Hostname: plain + "." + previewDomain, Wake: "request", Labels: map[string]string{"dude.preview": owner}})
				if err != nil {
					t.Fatal(err)
				}
			}
			calls := &relativePreviewLux{Client: w.previews.Lux}
			if mode == "race" {
				calls.Client = &hiddenOnce{Client: calls.Client}
			}
			w.previews.Lux = calls
			w.until("relative preview asleep", func() bool {
				return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND wakeable`, runID) == 1
			})
			full := want + "." + previewDomain
			if got := w.str(`SELECT hostname FROM preview_servers WHERE run_id = $1`, runID); got != full {
				t.Fatalf("stored hostname = %q, want %q", got, full)
			}
			if got := w.str(`SELECT url FROM preview_servers WHERE run_id = $1`, runID); got != "https://"+full {
				t.Fatalf("stored URL = %q", got)
			}
			id := w.serverID(runID, "web")
			if (mode == "adopt" || mode == "race") && id != existing.ID {
				t.Fatalf("adopted %s, want %s", id, existing.ID)
			}
			if w.labelled(runID) != 1 || len(calls.lookups) == 0 || calls.lookups[0] != plain {
				t.Fatalf("servers or lookups: %+v", calls)
			}
			if mode == "salt" && (len(calls.creates) != 2 || calls.creates[0] != plain || calls.creates[1] != want) {
				t.Fatalf("create attempts = %v", calls.creates)
			}
			if mode == "adopt" && len(calls.creates) != 0 {
				t.Fatalf("adoption created %v", calls.creates)
			}
			if (mode == "create" || mode == "both" || mode == "race") && (len(calls.creates) != 1 || calls.creates[0] != plain) {
				t.Fatalf("create attempts = %v", calls.creates)
			}
			w.lux.RequestServer(id, "/")
			w.heard(runID)
			w.open(id)
			w.running(runID, "web")
			code, out := w.do("GET", "/internal/tasks/"+task+"/servers", nil)
			if web := serverNamed(out, "web"); code != 200 || web == nil || web["url"] != "https://"+full || web["hostname"] != full {
				t.Fatalf("woken view = %d %v", code, out)
			}
		})
	}
}
