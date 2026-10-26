package fakeslack

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// WithChannelList is the fake with conversations.list as well, as Slack
// answers it to a bot: the workspace's public channels, and the private
// ones the bot is in, each with is_member, paged by the call's limit and
// cursor. Its chat.postMessage also refuses a section block longer than
// Slack takes. Every other path is the fake's own.
func (s *Server) WithChannelList() http.Handler { return s.WithChannelPages(0) }

// WithChannelPages is WithChannelList whose pages hold at most pageSize
// channels whatever limit the call asks (0: the call's limit), for a test
// of a walk over many pages.
func (s *Server) WithChannelPages(pageSize int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/chat.postMessage" && s.refuseLongSection(w, r) {
			return
		}
		if r.URL.Path != "/api/conversations.list" {
			s.ServeHTTP(w, r)
			return
		}
		a, err := args(r)
		if err != nil {
			refuse(w, "invalid_arguments")
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
			if f.code == "ratelimited" {
				w.Header().Set("Retry-After", strconv.Itoa(f.after))
				w.WriteHeader(http.StatusTooManyRequests)
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
		var channels []any
		for _, id := range ids {
			c := s.convs[id]
			member := contains(c.Members, "UB"+team)
			if c.Team != team || (c.Kind != "public" && c.Kind != "private") || (c.Kind == "private" && !member) {
				continue
			}
			channels = append(channels, map[string]any{"id": c.ID, "name": c.Name, "is_private": c.Kind == "private", "is_member": member})
		}
		// Slack's limit is 1 to 1000, 100 when not given; its cursor is
		// opaque (here, the offset of the page's first channel).
		limit, _ := strconv.Atoi(str(a, "limit"))
		if limit <= 0 || limit > 1000 {
			limit = 100
		}
		if pageSize > 0 && pageSize < limit {
			limit = pageSize
		}
		from := 0
		if cur := str(a, "cursor"); cur != "" {
			if from, err = strconv.Atoi(strings.TrimPrefix(cur, "offset:")); err != nil || from < 0 || from > len(channels) {
				refuse(w, "invalid_cursor")
				return
			}
		}
		to, next := min(from+limit, len(channels)), ""
		if to < len(channels) {
			next = "offset:" + strconv.Itoa(to)
		}
		page := append([]any{}, channels[from:to]...)
		reply(w, map[string]any{"channels": page, "response_metadata": map[string]any{"next_cursor": next}})
	})
}

// sectionMax is what Slack takes in a section block's text, in characters.
const sectionMax = 3000

// refuseLongSection answers a chat.postMessage whose blocks hold a section
// longer than sectionMax with invalid_blocks, as Slack does, counting the
// call; false, with r's body left to read, for any other.
func (s *Server) refuseLongSection(w http.ResponseWriter, r *http.Request) bool {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return false
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	var call struct {
		Blocks []struct {
			Type string `json:"type"`
			Text struct {
				Text string `json:"text"`
			} `json:"text"`
		} `json:"blocks"`
	}
	if json.Unmarshal(raw, &call) != nil {
		return false
	}
	for _, b := range call.Blocks {
		if b.Type == "section" && utf8.RuneCountInString(b.Text.Text) > sectionMax {
			s.mu.Lock()
			s.calls["chat.postMessage"]++
			s.mu.Unlock()
			refuse(w, "invalid_blocks")
			return true
		}
	}
	return false
}
