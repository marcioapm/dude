package slack

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/chat"
)

// channelPages bounds FindChannel's walk of conversations.list: 200 a page.
const channelPages = 50

// channelTTL is how long a channel FindChannel found the bot in is
// remembered: conversations.list is rate-limited (Tier 2, about 20 calls a
// minute), and a run posts to the same channel again and again.
const channelTTL = 5 * time.Minute

type channelKey struct {
	p           *Provider
	token, name string
}

type foundChannel struct {
	id string
	at time.Time
}

var (
	channelsMu sync.Mutex
	channels   = map[channelKey]foundChannel{}
)

// FindChannel looks a channel up by its name ("engineering", with or without
// the "#") in conversations.list: public channels, and the private ones the
// bot is in (Slack lists no other). found is false when none is called that;
// member says whether the bot is in it, which posting needs. A channel the
// bot is in is remembered for channelTTL; one it is not in, or none, is
// looked up again on the next call (the bot may have been invited since).
func (p *Provider) FindChannel(ctx context.Context, inst chat.Installation, name string) (id string, member, found bool, err error) {
	name = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(name), "#"))
	key := channelKey{p, inst.Token, name}
	channelsMu.Lock()
	hit, ok := channels[key]
	channelsMu.Unlock()
	if ok && time.Since(hit.at) < channelTTL {
		return hit.id, true, true, nil
	}
	cursor := ""
	for range channelPages {
		args := map[string]any{"types": "public_channel,private_channel", "exclude_archived": true, "limit": 200}
		if cursor != "" {
			args["cursor"] = cursor
		}
		var page struct {
			Channels []struct {
				ID       string `json:"id"`
				Name     string `json:"name"`
				IsMember bool   `json:"is_member"`
			} `json:"channels"`
			Meta struct {
				NextCursor string `json:"next_cursor"`
			} `json:"response_metadata"`
		}
		if err := p.call(ctx, inst.Token, "conversations.list", args, &page); err != nil {
			return "", false, false, err
		}
		for _, c := range page.Channels {
			if strings.ToLower(c.Name) == name {
				if c.IsMember {
					remember(key, c.ID)
				}
				return c.ID, c.IsMember, true, nil
			}
		}
		if cursor = page.Meta.NextCursor; cursor == "" {
			return "", false, false, nil
		}
	}
	return "", false, false, chat.ErrChannelLookupCut
}

// remember keeps a found channel's id, dropping the expired ones.
func remember(key channelKey, id string) {
	channelsMu.Lock()
	defer channelsMu.Unlock()
	now := time.Now()
	for k, v := range channels {
		if now.Sub(v.at) >= channelTTL {
			delete(channels, k)
		}
	}
	channels[key] = foundChannel{id: id, at: now}
}

// PostText posts text (mrkdwn) as a new message with no blocks, for text
// longer than one section block takes (3,000 characters; Slack's own limit
// on a message is 40,000), and returns its ts.
func (p *Provider) PostText(ctx context.Context, inst chat.Installation, conversation, text string) (string, error) {
	var out struct {
		TS string `json:"ts"`
	}
	err := p.call(ctx, inst.Token, "chat.postMessage", map[string]any{"channel": conversation, "text": text, "mrkdwn": true,
		"unfurl_links": false}, &out)
	return out.TS, err
}
