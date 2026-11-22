package netguard

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRefusedRanges(t *testing.T) {
	for _, tc := range []struct{ addr, why string }{
		{"169.254.169.254", "link-local"},
		{"169.254.0.1", "link-local"},
		{"fe80::1", "link-local"},
		{"127.0.0.1", "loopback"},
		{"127.8.9.10", "loopback"},
		{"::1", "loopback"},
		{"10.1.2.3", "private"},
		{"172.16.0.1", "private"},
		{"192.168.1.1", "private"},
		{"fd00::1", "private"},
		{"fc00::1", "private"},
		{"100.64.0.1", "carrier-grade NAT"},
		{"100.127.255.254", "carrier-grade NAT"},
		{"0.0.0.0", "unspecified"},
		{"::", "unspecified"},
		{"0.1.2.3", "this network (0.0.0.0/8)"},
		{"224.0.0.1", "multicast"},
		{"239.1.1.1", "multicast"},
		{"ff02::1", "multicast"},
		{"255.255.255.255", "broadcast"},
		{"::ffff:127.0.0.1", "loopback"},
		{"::ffff:169.254.169.254", "link-local"},
		{"::ffff:10.0.0.1", "private"},
		{"::ffff:100.64.0.1", "carrier-grade NAT"},
	} {
		if got := Refused(netip.MustParseAddr(tc.addr)); got != tc.why {
			t.Errorf("%s: %q, want %q", tc.addr, got, tc.why)
		}
	}
	for _, ok := range []string{"8.8.8.8", "1.1.1.1", "100.63.255.255", "100.128.0.1", "2606:4700::1111", "172.32.0.1"} {
		if got := Refused(netip.MustParseAddr(ok)); got != "" {
			t.Errorf("%s refused: %s", ok, got)
		}
	}
}

func fixed(m map[string][]string) Resolver {
	return func(_ context.Context, host string) ([]netip.Addr, error) {
		ss, ok := m[host]
		if !ok {
			return nil, errors.New("no such host")
		}
		var out []netip.Addr
		for _, s := range ss {
			out = append(out, netip.MustParseAddr(s))
		}
		return out, nil
	}
}

func TestCheckURL(t *testing.T) {
	g := New(WithResolver(fixed(map[string][]string{
		"public.test":   {"93.184.216.34"},
		"mixed.test":    {"93.184.216.34", "10.0.0.7"},
		"mixed6.test":   {"2606:4700::1111", "fd00::1"},
		"metadata.test": {"169.254.169.254"},
		"localhost":     {"127.0.0.1", "::1"},
	})))
	ctx := context.Background()
	for _, tc := range []struct {
		url    string
		reason string // "" for allowed
	}{
		{"https://public.test/health", ""},
		{"http://public.test:8443/x?token=1", ""},
		{"https://mixed.test/", "private"},
		{"https://mixed6.test/", "private"},
		{"http://metadata.test/latest/meta-data/", "link-local"},
		{"http://169.254.169.254/latest/meta-data/", "link-local"},
		{"http://localhost:8080", "loopback"},
		{"http://127.0.0.1/", "loopback"},
		{"http://[::1]/", "loopback"},
		{"http://[::ffff:127.0.0.1]/", "loopback"},
		{"http://10.2.3.4/", "private"},
		{"http://100.64.1.1/", "carrier-grade NAT"},
		{"http://[fd00::1]/", "private"},
		{"https://user:pw@public.test/", "no credentials in the URL"},
		{"https://user@public.test/", "no credentials in the URL"},
		{"ftp://public.test/", "only http and https URLs"},
		{"file:///etc/passwd", "only http and https URLs"},
		{"gopher://public.test/", "only http and https URLs"},
		{"https://nowhere.test/", "it does not resolve"},
	} {
		_, _, err := g.CheckURL(ctx, tc.url)
		var re *RefusedError
		switch {
		case tc.reason == "" && err != nil:
			t.Errorf("%s refused: %v", tc.url, err)
		case tc.reason != "" && (!errors.As(err, &re) || re.Reason != tc.reason || !errors.Is(err, ErrRefused)):
			t.Errorf("%s: %v, want refused %q", tc.url, err, tc.reason)
		}
	}
}

// local is a guard that lets the test servers' loopback address through.
func local(opts ...Option) *Guard {
	return New(append([]Option{AllowForTests(netip.MustParsePrefix("127.0.0.1/32"))}, opts...)...)
}

func TestGetFollowsCheckedRedirects(t *testing.T) {
	final := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != UserAgent || r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer final.Close()
	hop := httptest.NewServer(http.RedirectHandler(final.URL+"/done", http.StatusFound))
	defer hop.Close()
	status, err := local().Get(context.Background(), hop.URL, 5*time.Second)
	if err != nil || status != http.StatusNoContent {
		t.Fatalf("status %d, %v", status, err)
	}
}

// A redirect never carries the previous URL (its query may hold a token)
// to the next host in Referer.
func TestARedirectSendsNoReferer(t *testing.T) {
	var got atomic.Value
	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.Store(r.Header.Get("Referer"))
		w.WriteHeader(http.StatusNoContent)
	}))
	defer b.Close()
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, b.URL+"/landing", http.StatusFound)
	}))
	defer a.Close()
	status, err := local().Get(context.Background(), a.URL+"/status?token=SECRET", 5*time.Second)
	if err != nil || status != http.StatusNoContent {
		t.Fatalf("status %d, %v", status, err)
	}
	ref, ok := got.Load().(string)
	if !ok {
		t.Fatal("the redirect target was not reached")
	}
	if ref != "" {
		t.Fatalf("Referer at the redirect target: %q", ref)
	}
}

func TestARedirectToAPrivateAddressIsRefusedAtTheRedirect(t *testing.T) {
	var reached atomic.Int32
	inside := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached.Add(1) }))
	l, err := net.Listen("tcp", "127.0.0.2:0")
	if err != nil {
		t.Skipf("127.0.0.2 not bindable: %v", err)
	}
	inside.Listener = l
	inside.Start()
	defer inside.Close()
	for _, to := range []string{inside.URL + "/secret", "http://169.254.169.254/latest/meta-data/", "http://inside.test/"} {
		hop := httptest.NewServer(http.RedirectHandler(to, http.StatusFound))
		g := local(WithResolver(fixed(map[string][]string{"inside.test": {"10.0.0.9"}})))
		_, err := g.Get(context.Background(), hop.URL, 5*time.Second)
		hop.Close()
		var re *RedirectError
		if !errors.As(err, &re) || !errors.Is(err, ErrRefused) {
			t.Fatalf("redirect to %s: %v, want refused at the redirect", to, err)
		}
	}
	if reached.Load() != 0 {
		t.Fatal("a refused redirect was followed")
	}
}

func TestAtMostThreeRedirects(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := len(r.URL.Path)
		if n > 4 {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Redirect(w, r, srv.URL+r.URL.Path+"x", http.StatusFound)
	}))
	defer srv.Close()
	// "/" → /x → /xx → /xxx: three redirects, then 200 at /xxxx's? No: /xxxx is a fourth.
	if _, err := local().Get(context.Background(), srv.URL+"/", 5*time.Second); err == nil {
		t.Fatal("a fourth redirect was followed")
	}
	if status, err := local().Get(context.Background(), srv.URL+"/x", 5*time.Second); err != nil || status != http.StatusOK {
		t.Fatalf("three redirects: %d, %v", status, err)
	}
}

// The dial goes to the address the guard resolved and checked, by IP: a
// resolver answering differently on a later call is never asked again for
// the connection, and the name is never handed to the system resolver.
func TestTheDialledAddressIsTheCheckedOne(t *testing.T) {
	var first, second atomic.Int32
	checked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { first.Add(1) }))
	defer checked.Close()
	_, port, _ := net.SplitHostPort(checked.Listener.Addr().String())
	l, err := net.Listen("tcp", "127.0.0.2:"+port)
	if err != nil {
		t.Skipf("127.0.0.2 not bindable: %v", err)
	}
	other := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { second.Add(1) }))
	other.Listener = l
	other.Start()
	defer other.Close()
	var mu sync.Mutex
	calls := 0
	rebind := func(_ context.Context, host string) ([]netip.Addr, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls == 1 {
			return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
		}
		return []netip.Addr{netip.MustParseAddr("127.0.0.2")}, nil
	}
	// Both answers pass the guard here: only the dial decides which is reached.
	g := New(WithResolver(rebind), AllowForTests(netip.MustParsePrefix("127.0.0.1/32"), netip.MustParsePrefix("127.0.0.2/32")))
	status, err := g.Get(context.Background(), "http://rebind.invalid:"+port+"/", 5*time.Second)
	if err != nil || status != http.StatusOK {
		t.Fatalf("status %d, %v", status, err)
	}
	if first.Load() != 1 || second.Load() != 0 || calls != 1 {
		t.Fatalf("checked reached %d, second answer reached %d, resolved %d times", first.Load(), second.Load(), calls)
	}
}

func TestATimeoutIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(2 * time.Second):
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	if _, err := local().Get(context.Background(), srv.URL, 200*time.Millisecond); err == nil {
		t.Fatal("a timeout read as a status")
	}
}

func TestNoProxy(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://10.0.0.1:3128")
	t.Setenv("HTTPS_PROXY", "http://10.0.0.1:3128")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	// httptest's 127.0.0.1 is exempt from env proxies anyway; ask through a name.
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	g := local(WithResolver(fixed(map[string][]string{"site.test": {"127.0.0.1"}})))
	if status, err := g.Get(context.Background(), "http://site.test:"+port+"/", 5*time.Second); err != nil || status != 200 {
		t.Fatalf("status %d, %v", status, err)
	}
}

func TestHostPathDropsTheQuery(t *testing.T) {
	u, _ := checkURL("https://example.com/a/b?token=secret#frag")
	if got := HostPath(u); got != "example.com/a/b" || strings.Contains(got, "secret") {
		t.Fatal(got)
	}
}
