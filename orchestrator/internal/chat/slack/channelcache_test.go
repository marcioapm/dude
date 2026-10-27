package slack

import (
	"testing"
	"time"
)

// A remembered channel is kept for channelTTL (5 minutes), then looked up
// again; expired entries are dropped when another is kept.
func TestChannelCacheForgetsAChannelAfterFiveMinutes(t *testing.T) {
	now := time.Date(2026, 10, 23, 15, 0, 0, 0, time.UTC)
	c := channelCache{now: func() time.Time { return now }}
	key := channelKey{"chi_1", "engineering"}
	c.put(key, "C0ENGINEER")
	now = now.Add(5*time.Minute - time.Second)
	if id, ok := c.get(key); !ok || id != "C0ENGINEER" {
		t.Fatalf("at 4:59: %q %v", id, ok)
	}
	now = now.Add(time.Second)
	if id, ok := c.get(key); ok {
		t.Fatalf("at 5:00 still remembered: %q", id)
	}
	c.put(channelKey{"chi_1", "random"}, "C0RANDOMXX")
	if _, kept := c.found[key]; kept || len(c.found) != 1 {
		t.Errorf("expired entries kept: %v", c.found)
	}
}
