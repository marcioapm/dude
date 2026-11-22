// Package netguard keeps a request dude makes for someone else (a
// conductor's url wait) off the network dude runs in: only http and https,
// no credentials in the URL, and only hosts whose every address is public.
// The HTTP client it builds dials only the address it checked, resolving
// and checking again at each dial, so a DNS answer that changes between a
// check and the dial (a rebind) cannot reach the inside; each redirect is
// checked before it is followed, and no proxy is used.
package netguard

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

// MaxRedirects is how many redirects a Client follows.
const MaxRedirects = 3

// ErrRefused is what every refusal wraps.
var ErrRefused = errors.New("refused address")

// RefusedError is a URL or address the guard will not let a request reach.
type RefusedError struct {
	Host   string
	Addr   netip.Addr // the address refused; invalid when the URL itself was
	Reason string
}

func (e *RefusedError) Error() string {
	if e.Addr.IsValid() {
		return fmt.Sprintf("%s resolves to %s, %s: public addresses only", e.Host, e.Addr, e.Reason)
	}
	return fmt.Sprintf("%s: %s", e.Host, e.Reason)
}

func (e *RefusedError) Unwrap() error { return ErrRefused }

// RedirectError is a redirect the guard refused before following it.
type RedirectError struct {
	To  string // the redirect's host and path
	Err error
}

func (e *RedirectError) Error() string { return "redirect to " + e.To + ": " + e.Err.Error() }
func (e *RedirectError) Unwrap() error { return e.Err }

// Resolver answers a host's addresses.
type Resolver func(ctx context.Context, host string) ([]netip.Addr, error)

// Guard checks URLs and addresses. Its zero value is not usable: use New.
type Guard struct {
	resolve Resolver
	// Addresses a test lets through (AllowForTests); none otherwise.
	allow []netip.Prefix
}

// Option configures a Guard.
type Option func(*Guard)

// WithResolver answers hosts with r instead of the system resolver.
func WithResolver(r Resolver) Option { return func(g *Guard) { g.resolve = r } }

// AllowForTests lets the prefixes through, for a test's local server. Only
// tests set it: no configuration or environment reaches it.
func AllowForTests(p ...netip.Prefix) Option {
	return func(g *Guard) { g.allow = append(g.allow, p...) }
}

// New is a guard over the system resolver.
func New(opts ...Option) *Guard {
	g := &Guard{resolve: func(ctx context.Context, host string) ([]netip.Addr, error) {
		return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	}}
	for _, o := range opts {
		o(g)
	}
	return g
}

var (
	cgnat     = netip.MustParsePrefix("100.64.0.0/10")
	thisNet   = netip.MustParsePrefix("0.0.0.0/8")
	broadcast = netip.MustParseAddr("255.255.255.255")
)

// Refused is why an address is refused, "" for a public one. An
// IPv4-mapped IPv6 address is read as its IPv4 address.
func Refused(a netip.Addr) string {
	a = a.Unmap()
	switch {
	case !a.IsValid():
		return "not an address"
	case a.IsUnspecified():
		return "unspecified"
	case a.IsLoopback():
		return "loopback"
	case a.IsPrivate():
		return "private"
	case a.IsLinkLocalUnicast():
		return "link-local"
	case a.IsMulticast(), a.IsLinkLocalMulticast(), a.IsInterfaceLocalMulticast():
		return "multicast"
	case a == broadcast:
		return "broadcast"
	case cgnat.Contains(a):
		return "carrier-grade NAT"
	case thisNet.Contains(a):
		return "this network (0.0.0.0/8)"
	}
	return ""
}

func (g *Guard) allowed(a netip.Addr) bool {
	for _, p := range g.allow {
		if p.Contains(a.Unmap()) {
			return true
		}
	}
	return false
}

// CheckURL reads raw as a URL a request may be made to: http or https, any
// port, no userinfo, a host whose every address is public. It returns the
// URL and the first address, which a request would dial.
func (g *Guard) CheckURL(ctx context.Context, raw string) (*url.URL, netip.Addr, error) {
	u, err := checkURL(raw)
	if err != nil {
		return nil, netip.Addr{}, err
	}
	a, err := g.Resolve(ctx, u.Hostname())
	return u, a, err
}

// checkURL is CheckURL's reading of the URL alone, no resolution.
func checkURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, &RefusedError{Host: raw, Reason: "not a URL"}
	}
	switch {
	case u.Scheme != "http" && u.Scheme != "https":
		return nil, &RefusedError{Host: u.Host, Reason: "only http and https URLs"}
	case u.User != nil:
		return nil, &RefusedError{Host: u.Hostname(), Reason: "no credentials in the URL"}
	case u.Hostname() == "":
		return nil, &RefusedError{Host: raw, Reason: "no host"}
	}
	return u, nil
}

// Resolve resolves host and checks every address it has: one refused
// refuses the host. It returns the first, the one to dial.
func (g *Guard) Resolve(ctx context.Context, host string) (netip.Addr, error) {
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	var addrs []netip.Addr
	if a, err := netip.ParseAddr(host); err == nil {
		addrs = []netip.Addr{a}
	} else {
		if addrs, err = g.resolve(ctx, host); err != nil {
			return netip.Addr{}, &RefusedError{Host: host, Reason: "it does not resolve"}
		}
	}
	if len(addrs) == 0 {
		return netip.Addr{}, &RefusedError{Host: host, Reason: "it does not resolve"}
	}
	for _, a := range addrs {
		if why := Refused(a); why != "" && !g.allowed(a) {
			return netip.Addr{}, &RefusedError{Host: host, Addr: a, Reason: why}
		}
	}
	return addrs[0].Unmap(), nil
}

// DialContext resolves the address's host, checks every answer and dials
// the first checked one: never the host name, so a resolver answering
// differently later is never asked again for this connection.
func (g *Guard) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	a, err := g.Resolve(ctx, host)
	if err != nil {
		return nil, err
	}
	var d net.Dialer
	return d.DialContext(ctx, network, net.JoinHostPort(a.String(), port))
}

// Client is an HTTP client through the guard: every dial checked and
// pinned (DialContext), no proxy, no cookies, at most MaxRedirects
// redirects, each checked before it is followed, and timeout overall.
func (g *Guard) Client(timeout time.Duration) *http.Client {
	tr := &http.Transport{
		Proxy:                 nil,
		DialContext:           g.DialContext,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   timeout,
		ResponseHeaderTimeout: timeout,
		DisableKeepAlives:     true,
	}
	return &http.Client{Transport: tr, Timeout: timeout, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > MaxRedirects {
			return &RedirectError{To: HostPath(req.URL), Err: errors.New("more than 3 redirects")}
		}
		if _, err := checkURL(req.URL.String()); err != nil {
			return &RedirectError{To: HostPath(req.URL), Err: err}
		}
		if _, err := g.Resolve(req.Context(), req.URL.Hostname()); err != nil {
			return &RedirectError{To: HostPath(req.URL), Err: err}
		}
		return nil
	}}
}

// UserAgent is what a Get says it is.
const UserAgent = "dude-wait/1"

// BodyMax is how much of a response's body a Get reads, and discards.
const BodyMax = 64 << 10

// Get makes a GET to raw through the guard (Client): no credentials, no
// cookies, User-Agent UserAgent, at most BodyMax of the body read and
// discarded. It returns the final response's status.
func (g *Guard) Get(ctx context.Context, raw string, timeout time.Duration) (int, error) {
	u, err := checkURL(raw)
	if err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", UserAgent)
	resp, err := g.Client(timeout).Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, BodyMax))
	return resp.StatusCode, nil
}

// HostPath is a URL's host and path, never its userinfo, query or
// fragment (a query may hold a token).
func HostPath(u *url.URL) string {
	if u == nil {
		return ""
	}
	return u.Host + u.EscapedPath()
}
