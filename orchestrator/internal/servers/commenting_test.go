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

func TestPublicOriginIsAnHTTPSOrigin(t *testing.T) {
	for raw, want := range map[string]string{"https://Dude.Example.com/": "https://dude.example.com", "https://d.test:8443": "https://d.test:8443"} {
		if got, err := PublicOrigin(raw); err != nil || got != want {
			t.Errorf("%q: %q %v", raw, got, err)
		}
	}
	for _, raw := range []string{"http://d.test", "https://d.test/x", "https://u@d.test", "https://d.test?q", "https://d.test#f", "d.test", "https://"} {
		if got, err := PublicOrigin(raw); err == nil {
			t.Errorf("%q accepted as %q", raw, got)
		}
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
