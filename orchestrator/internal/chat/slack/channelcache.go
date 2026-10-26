package slack

import (
	"sync"
	"time"
)

// channelTTL is how long a channel FindChannel found the bot in is
// remembered: conversations.list is rate-limited (Tier 2, about 20 calls a
// minute), and a run posts to the same channel again and again.
const channelTTL = 5 * time.Minute

// channelCache is what a Provider's FindChannel remembers: by installation
// id and channel name, the channels the bot is in. The zero value is ready.
type channelCache struct {
	mu    sync.Mutex
	found map[channelKey]foundChannel
}

type channelKey struct{ installation, name string }

type foundChannel struct {
	id string
	at time.Time
}

func (c *channelCache) get(key channelKey) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	hit, ok := c.found[key]
	if !ok || time.Since(hit.at) >= channelTTL {
		return "", false
	}
	return hit.id, true
}

// put keeps a found channel's id, dropping the expired ones.
func (c *channelCache) put(key channelKey, id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	for k, v := range c.found {
		if now.Sub(v.at) >= channelTTL {
			delete(c.found, k)
		}
	}
	if c.found == nil {
		c.found = map[channelKey]foundChannel{}
	}
	c.found[key] = foundChannel{id: id, at: now}
}

func (c *channelCache) forget(key channelKey) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.found, key)
}
