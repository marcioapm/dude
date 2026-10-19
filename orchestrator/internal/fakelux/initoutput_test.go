package fakelux_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// initFrames are the Run's records so far.
func initFrames(t *testing.T, c *lux.HTTPClient, run string) []lux.Frame {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	var out []lux.Frame
	_ = c.Output(ctx, run, "", 0, func(f lux.Frame) error {
		if f.Kind == "record" {
			out = append(out, f)
		}
		return nil
	})
	return out
}

// An init that prints more than the fake keeps leaves only the end of its
// output on the record stream, with the line that says why it failed.
func TestAnInitsOutputIsKeptToItsEnd(t *testing.T) {
	repo := t.TempDir()
	gitInit(t, repo)
	fake := fakelux.New(repo, "k", nil)
	srv := httptest.NewServer(fake.Handler())
	t.Cleanup(srv.Close)
	t.Cleanup(fake.Close)
	c := lux.New(srv.URL, "k")
	run, err := c.Submit(context.Background(), lux.Spec{
		Image:    lux.Image{Ref: "node:22"},
		Workload: lux.Workload{Adapter: "generic", Command: []string{"sleep", "infinity"}},
		Git:      &lux.Git{Repositories: []lux.Repository{{Name: "app", URL: "https://github.com/acme/app.git"}}},
		Init:     &lux.Init{Script: "head -c 4194304 /dev/zero | tr '\\0' x >&2; echo >&2; echo 'the last line' >&2; exit 5"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the run to fail", func() bool { r, _ := c.Get(context.Background(), run.ID); return r.State == "failed" })
	var stderr []string
	for _, f := range initFrames(t, c, run.ID) {
		if f.Channel == "stderr" {
			stderr = append(stderr, f.Data)
		}
	}
	if len(stderr) != 1 || len(stderr[0]) > 64<<10 || !strings.HasSuffix(stderr[0], "x\nthe last line\n") {
		t.Fatalf("%d stderr records, the first %d bytes", len(stderr), len(strings.Join(stderr, "")))
	}
}
