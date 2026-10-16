package servers

import (
	"context"
	"errors"
	"html"
	"net/url"
	"strings"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// Commenting on a preview: each wakeable preview server lux serves carries
// inject, one script tag loading dude's overlay from dude's own origin
// (auth.public_url). lux adds it only to a document navigation answered
// 200 text/html, so a server that serves no pages is never touched. The
// snippet is derived from the origin alone whenever a server is created or
// synced, never stored: it holds no task, run, person or key.

// maxInject is lux's bound on a server's inject, in bytes.
const maxInject = 8 << 10

// PublicOrigin is auth.public_url as the snippet names it: an https origin
// with no path, query, fragment or credentials, and no trailing slash.
func PublicOrigin(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		(u.Path != "" && u.Path != "/") || u.Opaque != "" {
		return "", errors.New("an https origin with no path, query or credentials")
	}
	return "https://" + strings.ToLower(u.Host), nil
}

// OverlaySnippet is the inject that loads the overlay from origin, or ""
// when the origin is empty or the tag would pass lux's bound.
func OverlaySnippet(origin string) string {
	if origin == "" {
		return ""
	}
	o := html.EscapeString(strings.TrimRight(origin, "/"))
	s := `<script src="` + o + `/feedback/overlay.js" data-dude-origin="` + o + `" async></script>`
	if len(s) > maxInject {
		return ""
	}
	return s
}

// Why commenting on previews is unavailable (Commenting.Reason).
const (
	CommentingNeedsPublicURL = "public_url"
	CommentingNeedsLux       = "lux"
)

// Commenting says whether a preview server's pages load the overlay.
type Commenting struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

// commenting is this deployment's: auth.public_url first, as it is the
// operator's to set; then lux's serverInject.
func (s *Service) commenting() Commenting {
	switch {
	case s.PublicURL == "":
		return Commenting{Reason: CommentingNeedsPublicURL}
	case !s.ServerInject:
		return Commenting{Reason: CommentingNeedsLux}
	}
	return Commenting{Available: true}
}

// inject is what a preview server is created or synced with: the overlay
// snippet when commenting is available, else "" (the field is not sent).
func (s *Service) inject() string {
	if !s.commenting().Available {
		return ""
	}
	return OverlaySnippet(s.PublicURL)
}

// injectPatch is the inject to PATCH onto cur, nil when it has it already
// or commenting is off: an inject set while it was on is left as it is.
func (s *Service) injectPatch(cur lux.TenantServer) *string {
	want := s.inject()
	if want == "" || cur.Inject == want {
		return nil
	}
	return &want
}

// needsRestart: a preview server whose Run is up serves its pages without
// the overlay snippet wanted now (want, "" when commenting is off): made
// before commenting, or for another origin. One asleep is not: its next
// wake syncs it before lux starts it (updateServer).
func needsRestart(ts lux.TenantServer, want string) bool {
	if want == "" || ts.Inject == want {
		return false
	}
	switch ts.RunState {
	case "submitted", "scheduled", "starting", "resuming", "running":
		return true
	}
	return false
}

// syncInject puts the overlay snippet wanted now on one of a wakeable
// preview's servers, before a person's start or restart of it: the
// restart that needsRestart asks for. lux applies it to the next request.
func (s *Service) syncInject(ctx context.Context, r runRow, name string) error {
	if s.inject() == "" {
		return nil
	}
	list, err := s.Lux.ListServers(ctx, "", "dude.preview="+r.ID)
	if err != nil {
		return luxError(err)
	}
	for _, ts := range list {
		if ts.Name != name {
			continue
		}
		if inject := s.injectPatch(ts); inject != nil {
			if _, err := s.Lux.PatchServer(ctx, ts.ID, lux.PatchServer{Inject: inject}); err != nil && !lux.IsNotFound(err) {
				return luxError(err)
			}
		}
	}
	return nil
}
