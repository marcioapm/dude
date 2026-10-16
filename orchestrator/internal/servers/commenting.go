package servers

import (
	"context"
	"errors"
	"html"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
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
// with no path, query, fragment or credentials, and no trailing slash. It
// is exactly the browser's serialisation (WHATWG URL origin), as the
// overlay compares it with event.origin: lower case, no :443. A host is an
// ASCII DNS name, a dotted-decimal IPv4 address or a bracketed IPv6
// address; what WHATWG would rewrite otherwise (IDN, 0x7f.1, ::ffff:a.b.c.d)
// is refused rather than guessed.
func PublicOrigin(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		(u.Path != "" && u.Path != "/") || u.Opaque != "" {
		return "", errors.New("an https origin with no path, query, fragment or credentials")
	}
	host, err := originHost(u.Hostname())
	if err != nil {
		return "", err
	}
	if strings.HasSuffix(u.Host, ":") {
		return "", errors.New("an empty port")
	}
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n > 65535 {
			return "", errors.New("a port past 65535")
		}
		if n != 443 {
			host += ":" + strconv.Itoa(n)
		}
	}
	return "https://" + host, nil
}

var originLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// WHATWG parses a host whose last label is a number (decimal or 0x hex) as IPv4.
var numericLabel = regexp.MustCompile(`^([0-9]+|0x[0-9a-f]*)$`)

// originHost is h (url.Hostname: no brackets, no port) as an origin names it.
func originHost(h string) (string, error) {
	h = strings.ToLower(h)
	if strings.Contains(h, ":") {
		a, err := netip.ParseAddr(h)
		// An IPv4-mapped address is written ::ffff:a.b.c.d by Go and in hex by WHATWG.
		if err != nil || !a.Is6() || a.Zone() != "" || a.Is4In6() {
			return "", errors.New("a host that is not an IPv6 address WHATWG writes as Go does")
		}
		return "[" + a.String() + "]", nil
	}
	labels := strings.Split(h, ".")
	if numericLabel.MatchString(labels[len(labels)-1]) {
		a, err := netip.ParseAddr(h)
		if err != nil || !a.Is4() || a.String() != h {
			return "", errors.New("a host that ends in a number but is not a dotted-decimal IPv4 address")
		}
		return h, nil
	}
	for _, l := range labels {
		if !originLabel.MatchString(l) {
			return "", errors.New("a host that is not an ASCII DNS name, IPv4 or bracketed IPv6 address; give an internationalised name as punycode (xn--…)")
		}
	}
	return h, nil
}

// OverlaySnippet is the inject that loads the overlay from origin, or ""
// when the origin is empty or the tag would pass lux's bound.
func OverlaySnippet(origin string) string {
	if origin == "" {
		return ""
	}
	o := html.EscapeString(strings.TrimRight(origin, "/"))
	// Never add crossorigin: it makes the load a CORS request, and dude sends no Access-Control-Allow-Origin.
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
