package fakeslack

import (
	"net/http"
	"sort"
	"strings"
)

// WithChannelList is the fake with conversations.list as well, as Slack
// answers it to a bot: the workspace's public channels, and the private
// ones the bot is in, each with is_member; one page. Every other path is
// the fake's own.
func (s *Server) WithChannelList() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/conversations.list" {
			s.ServeHTTP(w, r)
			return
		}
		token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		s.mu.Lock()
		defer s.mu.Unlock()
		s.calls["conversations.list"]++
		if f, ok := s.failNext["conversations.list"]; ok {
			if f.left--; f.left <= 0 {
				delete(s.failNext, "conversations.list")
			} else {
				s.failNext["conversations.list"] = f
			}
			refuse(w, f.code)
			return
		}
		team, ok := s.tokens[token]
		if !ok {
			refuse(w, "invalid_auth")
			return
		}
		if !containsAny(s.scopes[token], []string{"channels:read", "groups:read"}) {
			refuse(w, "missing_scope")
			return
		}
		var ids []string
		for id := range s.convs {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		channels := []any{}
		for _, id := range ids {
			c := s.convs[id]
			member := contains(c.Members, "UB"+team)
			if c.Team != team || (c.Kind != "public" && c.Kind != "private") || (c.Kind == "private" && !member) {
				continue
			}
			channels = append(channels, map[string]any{"id": c.ID, "name": c.Name, "is_private": c.Kind == "private", "is_member": member})
		}
		reply(w, map[string]any{"channels": channels, "response_metadata": map[string]any{"next_cursor": ""}})
	})
}
