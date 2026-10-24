package api

import (
	"crypto/subtle"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/marciomartins/dude/orchestrator/internal/chat"
)

// chatProviderRoutes: chat providers (Slack first). The backend receives
// every request a provider or a browser sends (the only public surface) and
// passes it here; the orchestrator holds the providers' secrets and tokens
// and makes every call to them. A provider's webhook and an OAuth callback
// name no organisation: those routes take the service token alone, and the
// organisation is found from the workspace or the state.
func (s *Server) chatProviderRoutes(mux *http.ServeMux) {
	mux.Handle("GET /internal/chat/providers", s.auth(func(w http.ResponseWriter, r *http.Request, _ string) error {
		type entry struct {
			chat.Info
			Configured bool `json:"configured"`
		}
		var out []entry
		if s.Chat != nil {
			for _, info := range s.Chat.Providers.Infos() {
				p, _ := s.Chat.Providers.Get(info.ID)
				c, ok := p.(interface{ Configured() bool })
				out = append(out, entry{Info: info, Configured: !ok || c.Configured()})
			}
		}
		if out == nil {
			out = []entry{}
		}
		write(w, http.StatusOK, map[string]any{"providers": out, "builtIn": chat.BuiltInInstructions,
			"facts": map[string]any{"confidenceBar": chat.ConfidenceBar, "clarifyRounds": chat.ClarifyRounds, "epicTasksMax": chat.EpicTasksMax}})
		return nil
	}))
	mux.Handle("POST /internal/chat/{provider}/events", s.serviceOnly(s.chatReceive(false)))
	mux.Handle("POST /internal/chat/{provider}/interactions", s.serviceOnly(s.chatReceive(true)))
	mux.Handle("POST /internal/chat/{provider}/install", s.auth(s.chatBegin(true)))
	mux.Handle("POST /internal/chat/{provider}/connect", s.auth(s.chatBegin(false)))
	mux.Handle("POST /internal/chat/{provider}/install/callback", s.serviceOnly(s.chatCallback(true)))
	mux.Handle("POST /internal/chat/{provider}/connect/callback", s.serviceOnly(s.chatCallback(false)))
	mux.Handle("GET /internal/chat/{provider}/connect/state", s.auth(func(w http.ResponseWriter, r *http.Request, org string) error {
		p, err := sessionPrincipal(r)
		if err != nil {
			return err
		}
		out, err := s.chatService().ConnectState(r.Context(), org, r.PathValue("provider"), p.Person, r.URL.Query().Get("state"))
		if err != nil {
			return chatError(err)
		}
		write(w, http.StatusOK, out)
		return nil
	}))
	mux.Handle("DELETE /internal/chat/{provider}", s.auth(func(w http.ResponseWriter, r *http.Request, org string) error {
		p := principalOf(r)
		if err := s.chatService().Disconnect(r.Context(), org, r.PathValue("provider"), p.Person, p.Admin); err != nil {
			return chatError(err)
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	}))
	mux.Handle("DELETE /internal/me/chat/{provider}", s.auth(func(w http.ResponseWriter, r *http.Request, org string) error {
		p, err := sessionPrincipal(r)
		if err != nil {
			return err
		}
		if err := s.chatService().Unlink(r.Context(), org, r.PathValue("provider"), p.Person); err != nil {
			return chatError(err)
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	}))
}

// chatService is the configured service, or one with no providers (every
// {provider} a 404).
func (s *Server) chatService() *chat.Service {
	if s.Chat != nil {
		return s.Chat
	}
	return &chat.Service{DB: s.DB}
}

// chatError is a chat.Refusal as this API's error.
func chatError(err error) error {
	var ref *chat.Refusal
	if errors.As(err, &ref) {
		return fail(ref.Status, ref.Code, "%s", ref.Msg)
	}
	return err
}

// serviceOnly authenticates the service token alone, for requests that
// name no organisation yet.
func (s *Server) serviceOnly(h func(w http.ResponseWriter, r *http.Request) error) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if s.Token == "" || subtle.ConstantTimeCompare([]byte(token), []byte(s.Token)) != 1 {
			write(w, http.StatusUnauthorized, errBody("unauthorized", "missing or invalid service token"))
			return
		}
		if err := h(w, r); err != nil {
			var he *httpError
			if errors.As(chatError(err), &he) {
				write(w, he.status, errBody(he.code, he.message))
				return
			}
			s.Log.Error("internal request failed", "path", r.URL.Path, "error", err)
			write(w, http.StatusInternalServerError, errBody("internal", "internal error"))
		}
	})
}

// chatReceive takes a provider's delivery as the backend received it: its
// body as sent, and the provider's headers (X-Chat-Header-* carry them, so
// the backend's own headers are not mistaken for the provider's).
func (s *Server) chatReceive(interaction bool) func(w http.ResponseWriter, r *http.Request) error {
	return func(w http.ResponseWriter, r *http.Request) error {
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			return err
		}
		header := http.Header{}
		for k, v := range r.Header {
			if name, ok := strings.CutPrefix(k, "X-Chat-Header-"); ok {
				header[http.CanonicalHeaderKey(name)] = v
			}
		}
		out, err := s.chatService().Receive(r.Context(), r.PathValue("provider"), interaction, header, body)
		if err != nil {
			return err
		}
		write(w, http.StatusOK, out)
		return nil
	}
}

// chatBegin is an admin's install, or a member's link, starting: the URL
// the browser goes to.
func (s *Server) chatBegin(install bool) handler {
	return func(w http.ResponseWriter, r *http.Request, org string) error {
		p, err := sessionPrincipal(r)
		if err != nil {
			return err
		}
		var in struct {
			BrowserHash string `json:"browserHash"`
			Redirect    string `json:"redirect"`
			Prompt      string `json:"prompt"`
		}
		if err := read(r, &in); err != nil {
			return err
		}
		b := chat.Begin{Provider: r.PathValue("provider"), Person: p.Person, Admin: p.Admin, BrowserHash: in.BrowserHash,
			Redirect: in.Redirect, Prompt: in.Prompt}
		var url string
		if install {
			url, err = s.chatService().BeginInstall(r.Context(), org, b)
		} else {
			url, err = s.chatService().BeginConnect(r.Context(), org, b)
		}
		if err != nil {
			return chatError(err)
		}
		write(w, http.StatusOK, map[string]string{"url": url})
		return nil
	}
}

// chatCallback finishes an install or a link: where the browser lands.
func (s *Server) chatCallback(install bool) func(w http.ResponseWriter, r *http.Request) error {
	return func(w http.ResponseWriter, r *http.Request) error {
		var in struct {
			State, Code, Error, BrowserHash string
		}
		if err := read(r, &in); err != nil {
			return err
		}
		c := chat.Callback{Provider: r.PathValue("provider"), State: in.State, Code: in.Code, Error: in.Error, BrowserHash: in.BrowserHash}
		var land chat.Landing
		var err error
		if install {
			land, err = s.chatService().CompleteInstall(r.Context(), c)
		} else {
			land, err = s.chatService().CompleteConnect(r.Context(), c)
		}
		if err != nil {
			return err
		}
		write(w, http.StatusOK, map[string]string{"outcome": land.Outcome, "slug": land.Slug, "returnTo": land.ReturnTo})
		return nil
	}
}
