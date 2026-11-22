package delivery

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/netguard"
)

// url: a public URL's response status matching a code or a class, or not
// matching it. wait_for checks the URL and its host's addresses (netguard)
// and fetches nothing; the sweep's ObserveWaits fetches it at most every
// urlObserveEvery, through the guard, and stores the status or the error.
// A refused address is stored as one and never requested.

// How long a URL's read may take.
var urlTimeout = 10 * time.Second

// waitGuard is the address guard url conditions go through; tests let
// their local server through it (waits_url_test.go), nothing else does.
var waitGuard = netguard.New()

// urlCond checks a url condition at wait_for: a status of three digits or
// a class (2xx), 2xx by default; an http(s) URL with no credentials whose
// host resolves only to public addresses. Nothing is fetched.
func urlCond(ctx context.Context, ask URLAsk) (waitCond, error) {
	want := strings.ToLower(strings.TrimSpace(ask.Status))
	if want == "" {
		want = "2xx"
	}
	if !validWant(want) {
		return waitCond{}, refusef(rWaitURLStatusUnknown, ask.Status)
	}
	raw := strings.TrimSpace(ask.URL)
	u, _, err := waitGuard.CheckURL(ctx, raw)
	if err != nil {
		var re *netguard.RefusedError
		if errors.As(err, &re) {
			return waitCond{}, refusef(rWaitURLRefused, hostPathOf(raw), re.Error())
		}
		return waitCond{}, err
	}
	return waitCond{Kind: condURL, URL: raw, Where: netguard.HostPath(u), Want: want, Not: ask.Not}, nil
}

// hostPathOf is a URL's host and path for a refusal: never its query.
func hostPathOf(raw string) string {
	if u, err := url.Parse(raw); err == nil {
		return netguard.HostPath(u)
	}
	return "the URL"
}

func validWant(w string) bool {
	if len(w) != 3 || w[0] < '1' || w[0] > '5' {
		return false
	}
	if w[1:] == "xx" {
		return true
	}
	n, err := strconv.Atoi(w)
	return err == nil && n >= 100 && n <= 599
}

// statusMatches says whether a response's status is want: the code itself,
// or in the class.
func statusMatches(want string, status int) bool {
	if status == 0 {
		return false
	}
	if strings.HasSuffix(want, "xx") {
		return strconv.Itoa(status/100) == want[:1]
	}
	return strconv.Itoa(status) == want
}

// observeURL reads c's URL once, through the guard, with no transaction
// open: its final status, or why there is none.
func observeURL(ctx context.Context, c waitCond) waitObs {
	status, err := waitGuard.Get(ctx, c.URL, urlTimeout)
	if err == nil {
		return waitObs{Status: status}
	}
	return waitObs{Error: urlError(err)}
}

// urlError is why a URL's read gave no status, in a few words, with no
// part of the URL's query.
func urlError(err error) string {
	var redirect *netguard.RedirectError
	var nerr net.Error
	switch {
	case errors.Is(err, netguard.ErrRefused) && errors.As(err, &redirect):
		return obsRefused + " (redirect to " + redirect.To + ")"
	case errors.Is(err, netguard.ErrRefused):
		return obsRefused
	case errors.As(err, &redirect):
		return "redirect to " + redirect.To + ": " + redirect.Err.Error()
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &nerr) && nerr.Timeout():
		return "timeout"
	}
	return "connection failed"
}

// urlItem reads a url condition from its last observation: it holds when
// the status matches (not: when it does not, an error included); unread,
// it waits.
func urlItem(c waitCond) waitItem {
	it := waitItem{Kind: "url", Where: c.Where, Want: c.Want, Not: c.Not, rank: rankWaiting}
	o := c.Obs
	if o == nil {
		return it
	}
	it.Read, it.Code, it.Error = o.At.UTC().Format("15:04"), o.Status, o.Error
	if statusMatches(c.Want, o.Status) != c.Not {
		it.Ended, it.rank = true, rankGood
	}
	return it
}
