package servers

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// placedTwice is a lux whose Run has been placed twice, each container given
// a different memory limit. Only Get and Servers are called by view.
type placedTwice struct{ lux.Client }

func (placedTwice) Get(context.Context, string) (lux.Run, error) {
	first, latest := int64(15<<30), int64(16_320_875_520)
	return lux.Run{ID: "lr_1", State: "running", Epoch: 2, Host: "host-2", Placements: []lux.Placement{
		{Epoch: 1, HostName: "host-1", State: "exited", MemoryLimit: &first},
		{Epoch: 2, HostName: "host-2", State: "running", MemoryLimit: &latest},
	}}, nil
}

func (placedTwice) Servers(context.Context, string) ([]lux.Server, error) { return nil, nil }

// The Run's memory limit is its latest placement's: the container it is in now.
func TestARunsMemoryLimitIsItsLatestPlacements(t *testing.T) {
	s := &Service{Lux: placedTwice{}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	out := s.view(context.Background(), &runRow{ID: "run_1", Kind: "agent", Phase: "implement", Status: "running", LuxRunID: "lr_1"}, nil)
	if out.Run == nil || out.Run.MemoryLimit == nil || *out.Run.MemoryLimit != 16_320_875_520 {
		t.Fatalf("memoryLimit = %v, want the second placement's 16320875520", out.Run.MemoryLimit)
	}
}
