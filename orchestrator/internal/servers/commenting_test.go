package servers

import (
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

func TestTheOverlaySnippetIsOneTagNamingTheOriginOnly(t *testing.T) {
	const want = `<script src="https://dude.example.com/feedback/overlay.js" data-dude-origin="https://dude.example.com" async></script>`
	for _, origin := range []string{"https://dude.example.com", "https://dude.example.com/"} {
		if got := OverlaySnippet(origin); got != want {
			t.Errorf("%q:\n got %s\nwant %s", origin, got, want)
		}
	}
	if got := OverlaySnippet(""); got != "" {
		t.Errorf("no origin: %q", got)
	}
	// An origin is the snippet's only input: what is not in it cannot be in the tag.
	got := OverlaySnippet("https://o.test")
	if rest := strings.ReplaceAll(got, "https://o.test", ""); rest != `<script src="/feedback/overlay.js" data-dude-origin="" async></script>` {
		t.Errorf("beyond the origin the snippet holds %q", rest)
	}
	// Quoted as an attribute: an origin cannot close the tag.
	if got := OverlaySnippet(`https://x.test"><img src=y>`); strings.Contains(got, `"><img`) {
		t.Errorf("unescaped: %s", got)
	}
	if got := OverlaySnippet("https://" + strings.Repeat("a", 8<<10)); got != "" {
		t.Errorf("past lux's 8 KiB: %d bytes sent", len(got))
	}
}

// publicOrigins are inputs PublicOrigin accepts, each with what a browser
// gives as new URL(input).origin (WHATWG), hard-coded: the overlay compares
// data-dude-origin with event.origin as strings. The control plane's
// config.test.ts checks its publicUrl parser against the same table.
var publicOrigins = map[string]string{
	"https://Dude.Example.com/":          "https://dude.example.com",
	"https://d.test:8443":                "https://d.test:8443",
	"https://DUDE.EXAMPLE.COM:443":       "https://dude.example.com",
	"https://d.test:443/":                "https://d.test",
	"https://d.test:0443":                "https://d.test",
	"https://d.test:08443":               "https://d.test:8443",
	" https://d.test\n":                  "https://d.test",
	"https://xn--bcher-kva.example":      "https://xn--bcher-kva.example",
	"https://127.0.0.1:8443":             "https://127.0.0.1:8443",
	"https://[::1]":                      "https://[::1]",
	"https://[2001:DB8:0:0:0:0:0:1]:443": "https://[2001:db8::1]",
	"https://a-b.c-d.test":               "https://a-b.c-d.test",
}

func TestPublicOriginIsAnHTTPSOrigin(t *testing.T) {
	for raw, want := range publicOrigins {
		if got, err := PublicOrigin(raw); err != nil || got != want {
			t.Errorf("%q: %q %v, want %q", raw, got, err, want)
		}
	}
	for _, raw := range []string{"http://d.test", "https://d.test/x", "https://u@d.test", "https://d.test?q", "https://d.test#f", "d.test", "https://",
		// An empty port: the browser drops it, so the origin would differ.
		"https://d.test:", "https://d.test:/",
		// Not ASCII DNS, IPv4 or bracketed IPv6: WHATWG would rewrite or refuse it.
		"https://Bücher.example", "https://x.test<b>", "https://x.test'y", "https://x.test&amp;", "https://a_b.test",
		"https://-a.test", "https://a..test", "https://0x7f.1", "https://1.2.3", "https://127.000.0.1", "https://d.test.1",
		"https://[::ffff:1.2.3.4]", "https://[fe80::1%25eth0]", "https://d.test:65536"} {
		if got, err := PublicOrigin(raw); err == nil {
			t.Errorf("%q accepted as %q", raw, got)
		}
	}
	if _, err := PublicOrigin("https://Bücher.example"); err == nil || !strings.Contains(err.Error(), "punycode") {
		t.Errorf("IDN: %v, want it to name punycode", err)
	}
	if _, err := PublicOrigin("https://d.test#f"); err == nil || !strings.Contains(err.Error(), "fragment") {
		t.Errorf("fragment: %v, want it named", err)
	}
}

// Commenting needs both: dude's origin and a lux whose servers take
// inject. public_url is named first, as the operator's to set.
func TestCommentingNeedsAnOriginAndALuxThatInjects(t *testing.T) {
	for _, tc := range []struct {
		url    string
		inject bool
		want   Commenting
	}{
		{"", false, Commenting{Reason: CommentingNeedsPublicURL}},
		{"", true, Commenting{Reason: CommentingNeedsPublicURL}},
		{"https://d.test", false, Commenting{Reason: CommentingNeedsLux}},
		{"https://d.test", true, Commenting{Available: true}},
	} {
		s := &Service{PublicURL: tc.url, ServerInject: tc.inject}
		if got := s.commenting(); got != tc.want {
			t.Errorf("%q %v: %+v", tc.url, tc.inject, got)
		}
		wantInject := ""
		if tc.want.Available {
			wantInject = OverlaySnippet(tc.url)
		}
		if got := s.inject(); got != wantInject {
			t.Errorf("%q %v: inject %q", tc.url, tc.inject, got)
		}
		// A patch only to put the current snippet on; never to clear one.
		if p := s.injectPatch(lux.TenantServer{Inject: "<script src=\"https://old.test/feedback/overlay.js\"></script>"}); (p != nil) != tc.want.Available || p != nil && *p != wantInject {
			t.Errorf("%q %v: patch %v", tc.url, tc.inject, p)
		}
		if p := s.injectPatch(lux.TenantServer{Inject: wantInject}); p != nil {
			t.Errorf("%q %v: patched a server that has it", tc.url, tc.inject)
		}
	}
}
