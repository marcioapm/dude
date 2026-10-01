package servers

import (
	"github.com/marciomartins/dude/orchestrator/internal/lux"
	"testing"
	"time"
)

func TestRecentUseOfANeverReadyServerPreventsIdle(t *testing.T) {
	now := time.Now()
	old := now.Add(-2 * time.Hour)
	recent := now.Add(-time.Minute)
	s := lux.TenantServer{Since: old, LastRequestAt: &recent}
	// The field accepts the same duration shape lux returns.
	s.IdleAfter = []byte(`"10m"`)
	if unusedFor(s, now) {
		t.Fatal("a recently used server counted as idle")
	}
	s.LastRequestAt = &old
	if !unusedFor(s, now) {
		t.Fatal("a long-unused server did not count as idle")
	}
}
