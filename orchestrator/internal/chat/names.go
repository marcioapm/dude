package chat

import (
	"context"
	"slices"
	"sync"
	"time"
)

// Display names of provider users dude has no person for, read from the
// provider at most once per nameTTL per installation, at most
// nameFetchers at once, and at most namesMax per call (the rest read as
// their ids).
const (
	nameTTL      = 10 * time.Minute
	nameFetchers = 4
	namesMax     = 50
	namesKept    = 5000
)

type cachedName struct {
	name string
	at   time.Time
}

type nameCache struct {
	mu   sync.Mutex
	byID map[string]cachedName // installation id + "\x1f" + user id
}

func (c *nameCache) get(key string, now time.Time) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n, ok := c.byID[key]
	if !ok || now.Sub(n.at) > nameTTL {
		return "", false
	}
	return n.name, true
}

func (c *nameCache) put(key, name string, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.byID == nil || len(c.byID) >= namesKept {
		c.byID = map[string]cachedName{}
	}
	c.byID[key] = cachedName{name: name, at: now}
}

// names is each user's display name; a user whose read failed reads as
// their id, and is not cached. Only the first namesMax users in id order
// are named, so a large conversation reads the same ones each time rather
// than rotating through its members; the rest read as their ids.
func (s *Service) names(ctx context.Context, p Provider, inst Installation, users []string) map[string]string {
	out := make(map[string]string, len(users))
	var missing []string
	now := s.now()
	for i, u := range slices.Sorted(slices.Values(users)) {
		out[u] = u
		if n, ok := s.nameCache.get(inst.ID+"\x1f"+u, now); ok {
			out[u] = n
		} else if i < namesMax {
			missing = append(missing, u)
		}
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, nameFetchers)
	for _, u := range missing {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()
			who, err := p.User(ctx, inst, u)
			if err != nil {
				return
			}
			s.nameCache.put(inst.ID+"\x1f"+u, who.Name(), now)
			mu.Lock()
			out[u] = who.Name()
			mu.Unlock()
		}()
	}
	wg.Wait()
	return out
}
