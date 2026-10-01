package lux_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// Requests made 16 at a time, as the sweeps make them, reuse their
// connections: a second round dials none.
func TestConcurrentRequestsReuseTheirConnections(t *testing.T) {
	var dialled atomic.Int64
	var inFlight sync.WaitGroup
	release := make(chan struct{})
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inFlight.Done()
		<-release
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"servers":[]}`))
	}))
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			dialled.Add(1)
		}
	}
	srv.Start()
	defer srv.Close()
	c := lux.New(srv.URL, "k")
	round := func() {
		// All 16 held open at once, so each needs a connection of its own.
		inFlight.Add(16)
		var done sync.WaitGroup
		for range 16 {
			done.Add(1)
			go func() {
				defer done.Done()
				if _, err := c.ListServers(context.Background(), ""); err != nil {
					t.Error(err)
				}
			}()
		}
		inFlight.Wait()
		release <- struct{}{}
		for range 15 {
			release <- struct{}{}
		}
		done.Wait()
		time.Sleep(20 * time.Millisecond) // connections back in the pool
	}
	round()
	first := dialled.Load()
	round()
	if again := dialled.Load() - first; again != 0 {
		t.Fatalf("the second round of 16 dialled %d new connections (first: %d)", again, first)
	}
}
