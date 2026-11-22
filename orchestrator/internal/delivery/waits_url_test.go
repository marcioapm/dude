package delivery

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/netguard"
)

// url (waits_url.go): checked through the address guard at wait_for,
// fetched by the observation step through it at most every 2 minutes.

// statusServer is a local server answering a status that can change,
// let through the guard for the test (and only for it).
type statusServer struct {
	status atomic.Int32
	sleep  atomic.Int64
	hits   atomic.Int32
	srv    *httptest.Server
}

func newStatusServer(t *testing.T, status int) *statusServer {
	s := &statusServer{}
	s.status.Store(int32(status))
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.hits.Add(1)
		if d := time.Duration(s.sleep.Load()); d > 0 {
			select {
			case <-time.After(d):
			case <-r.Context().Done():
				return
			}
		}
		w.WriteHeader(int(s.status.Load()))
	}))
	t.Cleanup(s.srv.Close)
	guard := waitGuard
	waitGuard = netguard.New(netguard.AllowForTests(netip.MustParsePrefix("127.0.0.1/32")))
	t.Cleanup(func() { waitGuard = guard })
	return s
}

func urlCondOf(u, status string, not bool) WaitCondition {
	return WaitCondition{URL: &URLAsk{URL: u, Status: status, Not: not}}
}

func TestWaitForURL2xxHolds(t *testing.T) {
	w := newWaitWorld(t)
	s := newStatusServer(t, 204)
	opened := w.mustWait("c1", WaitAsk{Conditions: []WaitCondition{urlCondOf(s.srv.URL+"/health", "", false)}, Deadline: "1h"})
	if s.hits.Load() != 0 {
		t.Fatal("wait_for fetched the URL")
	}
	w.mustStillWait(opened.WaitID, "not read yet")
	w.observe(nil)
	w.mustHold(opened.WaitID, "204 is 2xx")
}

func TestWaitForURLNot2xxHoldsOnceItStopsMatching(t *testing.T) {
	w := newWaitWorld(t)
	s := newStatusServer(t, 200)
	opened := w.mustWait("c1", WaitAsk{Conditions: []WaitCondition{urlCondOf(s.srv.URL, "2xx", true)}, Deadline: "1h"})
	w.observe(nil)
	w.mustStillWait(opened.WaitID, "200 while waiting for not 2xx")
	s.status.Store(503)
	// Read under 2 minutes ago: not read again.
	if n := w.observe(nil); n != 0 || s.hits.Load() != 1 {
		t.Fatalf("read again within 2 minutes: %d stored, %d hits", n, s.hits.Load())
	}
	w.ageObservations(time.Minute)
	if n := w.observe(nil); n != 0 || s.hits.Load() != 1 {
		t.Fatal("read again at 1 minute")
	}
	w.mustStillWait(opened.WaitID, "503 not yet read")
	w.ageObservations(time.Minute)
	if n := w.observe(nil); n != 1 || s.hits.Load() != 2 {
		t.Fatalf("at 2 minutes: %d stored, %d hits", n, s.hits.Load())
	}
	w.mustHold(opened.WaitID, "503 is not 2xx")
}

func TestWaitForURL5xxHolds(t *testing.T) {
	w := newWaitWorld(t)
	s := newStatusServer(t, 503)
	opened := w.mustWait("c1", WaitAsk{Conditions: []WaitCondition{urlCondOf(s.srv.URL, "5xx", false)}, Deadline: "1h"})
	w.observe(nil)
	w.mustHold(opened.WaitID, "503 is 5xx")
}

func TestWaitForURLExactCode(t *testing.T) {
	w := newWaitWorld(t)
	s := newStatusServer(t, 204)
	opened := w.mustWait("c1", WaitAsk{Conditions: []WaitCondition{urlCondOf(s.srv.URL, "200", false)}, Deadline: "1h"})
	w.observe(nil)
	w.mustStillWait(opened.WaitID, "204 is not 200")
	s.status.Store(200)
	w.ageObservations(2 * time.Minute)
	w.observe(nil)
	w.mustHold(opened.WaitID, "200")
}

// A timeout is no match: it does not hold 2xx, and holds not 2xx.
func TestAURLTimeoutIsNoMatch(t *testing.T) {
	timeout := urlTimeout
	urlTimeout = 200 * time.Millisecond
	t.Cleanup(func() { urlTimeout = timeout })
	for _, not := range []bool{false, true} {
		w := newWaitWorld(t)
		s := newStatusServer(t, 200)
		s.sleep.Store(int64(2 * time.Second))
		opened := w.mustWait("c1", WaitAsk{Conditions: []WaitCondition{urlCondOf(s.srv.URL, "2xx", not)}, Deadline: "1h"})
		w.observe(nil)
		if got := w.obsField(opened.WaitID, 0, "error"); got != "timeout" {
			t.Fatalf("obs %s", w.obs(opened.WaitID, 0))
		}
		if not {
			w.mustHold(opened.WaitID, "a timeout is not 2xx")
		} else {
			w.mustStillWait(opened.WaitID, "a timeout is not 2xx")
		}
	}
}

// A host that resolves inside once the wait is open is read as a refused
// address, never requested.
func TestAURLThatTurnsInsideIsNeverRequested(t *testing.T) {
	w := newWaitWorld(t)
	s := newStatusServer(t, 200)
	opened := w.mustWait("c1", WaitAsk{Conditions: []WaitCondition{urlCondOf(s.srv.URL, "5xx", true)}, Deadline: "1h"})
	waitGuard = netguard.New() // the test's allowance gone: 127.0.0.1 is loopback again
	w.observe(nil)
	if s.hits.Load() != 0 || w.obsField(opened.WaitID, 0, "error") != obsRefused {
		t.Fatalf("%d hits, obs %s", s.hits.Load(), w.obs(opened.WaitID, 0))
	}
}

// The URL's query (it may hold a token) is in neither the digest nor the
// observation nor the ledger.
func TestAURLsQueryIsNeitherShownNorObserved(t *testing.T) {
	w := newWaitWorld(t)
	s := newStatusServer(t, 200)
	opened := w.mustWait("c1", WaitAsk{Conditions: []WaitCondition{urlCondOf(s.srv.URL+"/status?token=s3cr3t", "", false)}, Deadline: "1h"})
	w.observe(nil)
	w.mustHold(opened.WaitID, "200")
	var ledger string
	if err := w.owner.QueryRow(w.ctx, `SELECT COALESCE(string_agg(payload::text, ' '), '') FROM events`).Scan(&ledger); err != nil {
		t.Fatal(err)
	}
	for what, text := range map[string]string{"obs": w.obs(opened.WaitID, 0), "digest": w.digest(opened.WaitID), "ledger": ledger} {
		if strings.Contains(text, "s3cr3t") {
			t.Errorf("the query is in the %s: %s", what, text)
		}
	}
}

func TestWaitForURLRefusals(t *testing.T) {
	w := newWaitWorld(t)
	refused := func(where, why string) string {
		return fmt.Sprintf(rWaitURLRefused, where, why)
	}
	for _, tc := range []struct {
		cond WaitCondition
		want string
	}{
		{urlCondOf("http://169.254.169.254/latest/meta-data/", "", false),
			refused("169.254.169.254/latest/meta-data/", "169.254.169.254 resolves to 169.254.169.254, link-local: public addresses only")},
		{urlCondOf("http://localhost:8080", "", false), ""},
		{urlCondOf("http://10.0.0.1/", "", false), refused("10.0.0.1/", "10.0.0.1 resolves to 10.0.0.1, private: public addresses only")},
		{urlCondOf("https://user:pw@example.com/", "", false), refused("example.com/", "example.com: no credentials in the URL")},
		{urlCondOf("ftp://example.com/x", "", false), refused("example.com/x", "example.com: only http and https URLs")},
		{urlCondOf("https://example.com/", "3xy", false), fmt.Sprintf(rWaitURLStatusUnknown, "3xy")},
		{urlCondOf("https://example.com/", "600", false), fmt.Sprintf(rWaitURLStatusUnknown, "600")},
	} {
		_, err := w.waitFor("c1", WaitAsk{Conditions: []WaitCondition{tc.cond}, Deadline: "1h"})
		switch {
		case !IsRefusal(err):
			t.Errorf("%s: %v, want refused", tc.cond.URL.URL, err)
		case tc.want != "" && err.Error() != tc.want:
			t.Errorf("%s: %q, want %q", tc.cond.URL.URL, err, tc.want)
		case tc.want == "" && !strings.HasPrefix(err.Error(), "dude does not wait on localhost:8080: localhost resolves to "):
			t.Errorf("%s: %q", tc.cond.URL.URL, err)
		}
	}
	if w.count("conductor_waits") != 0 {
		t.Fatal("a refused call wrote")
	}
}

// prEnded and url combine with every other kind under wake any.
func TestPREndedAndURLWithOtherKinds(t *testing.T) {
	w := newWaitWorld(t)
	w.phaseRun("run_a", "review")
	s := newStatusServer(t, 200)
	pulls := newFakePulls(t)
	pulls.set("other/lib#41", openPull(41, "Still open"))
	opened, err := w.waitForWithReads(pulls.forges(), "c1", WaitAsk{Conditions: []WaitCondition{
		runsEnded("run_a"), prChecks("repo#7", ""), afterCond("45m"),
		prEndedCond("other/lib#41", ""), prEndedCond("repo#7", "merged"), urlCondOf(s.srv.URL, "5xx", false),
	}, Wake: "all", Deadline: "2h"})
	if err != nil || opened.WaitID == "" {
		t.Fatalf("%+v, %v", opened, err)
	}
	w.observe(pulls.forges())
	w.mustStillWait(opened.WaitID, "nothing held")
	w.exec(`UPDATE runs SET status = 'completed', ended_at = now() WHERE id = 'run_a'`)
	w.exec(`UPDATE pull_requests SET checks = 'passing' WHERE id = 'pr_life'`)
	w.moveAfterIntoThePast()
	w.endPR("merged")
	pulls.set("other/lib#41", closedPull(41, "Still open"))
	w.ageObservations(10 * time.Minute)
	w.observe(pulls.forges())
	w.mustStillWait(opened.WaitID, "all but the URL")
	s.status.Store(502)
	w.ageObservations(10 * time.Minute)
	w.observe(pulls.forges())
	w.mustHold(opened.WaitID, "all")
}

// An idle wait on an untracked pull request and a URL, both read, is read
// from what the observations stored, not locked: its evaluation waits on
// neither the task's Chat lock, nor its wait's row, nor the GitHub read
// budget's lock, and reads nothing outside the database.
func TestEvaluatingAnIdlePREndedAndURLWaitTakesNoLock(t *testing.T) {
	w := newWaitWorld(t)
	s := newStatusServer(t, 200)
	pulls := newFakePulls(t)
	pulls.set("other/lib#41", openPull(41, "Still open"))
	opened, err := w.waitForWithReads(pulls.forges(), "c1", WaitAsk{Conditions: []WaitCondition{
		prEndedCond("other/lib#41", ""), prEndedCond("repo#7", ""), urlCondOf(s.srv.URL, "5xx", false)}, Deadline: "1h"})
	if err != nil {
		t.Fatal(err)
	}
	w.observe(pulls.forges())
	hits, reads := s.hits.Load(), pulls.reads.Load()
	holder, err := w.owner.Begin(w.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Rollback(w.ctx) }()
	if err := LockChat(w.ctx, holder, "wi_life"); err != nil {
		t.Fatal(err)
	}
	if err := LockGitHubReadsTx(w.ctx, holder, "task:wi_life"); err != nil {
		t.Fatal(err)
	}
	if _, err := holder.Exec(w.ctx, `SELECT 1 FROM conductor_waits WHERE id = $1 FOR UPDATE`, opened.WaitID); err != nil {
		t.Fatal(err)
	}
	if _, err := holder.Exec(w.ctx, `SELECT 1 FROM pull_requests WHERE id = 'pr_life' FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(w.ctx, 3*time.Second)
	defer cancel()
	if err := w.s.DB.InOrg(ctx, w.org, func(tx pgx.Tx) error {
		ended, err := EvaluateWaitTx(ctx, tx, w.org, "wi_life", opened.WaitID)
		if ended {
			t.Error("an idle wait ended")
		}
		return err
	}); err != nil {
		t.Fatalf("an idle evaluation waited on a lock: %v", err)
	}
	if s.hits.Load() != hits || pulls.reads.Load() != reads {
		t.Fatal("the evaluation read outside the database")
	}
}
