package orchestrator_test

import (
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
)

// reviewCalls records when dude POSTs a review and when it lists the
// pull request's reviews (FindOwnReview), from now on.
type reviewCalls struct {
	mu            sync.Mutex
	posts, lookup []time.Time
}

func (w *world) recordReviewCalls() *reviewCalls {
	c := &reviewCalls{}
	w.gh.Set(func(s *fakegithub.Server) {
		s.Intercept = func(r *http.Request) int {
			if strings.HasSuffix(r.URL.Path, "/reviews") && strings.Contains(r.URL.Path, "/pulls/") {
				c.mu.Lock()
				if r.Method == "POST" {
					c.posts = append(c.posts, time.Now())
				} else if r.Method == "GET" {
					c.lookup = append(c.lookup, time.Now())
				}
				c.mu.Unlock()
			}
			return 0
		}
	})
	return c
}

func (c *reviewCalls) get() (posts, lookups []time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Time(nil), c.posts...), append([]time.Time(nil), c.lookup...)
}

// pumpFor advances the world's workflows for d, without syncing.
func (w *world) pumpFor(d time.Duration) {
	for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		w.advanced()
	}
}

// P-M2: GitHub answers every review submission 502. dude never POSTs
// again at a fixed pace: after each lost answer it looks for the review
// at increasing delays, resubmits only after the last miss, and the gap
// between submissions grows.
func TestPersistent5xxSubmitsAreBoundedAndSpacedIncreasingly(t *testing.T) {
	w := newWorld(t)
	w.gh.SetChecks("success")
	w.withDeliveries(&delivery.Store{ReviewRecoverDelay: 100 * time.Millisecond})
	w.gh.Set(func(s *fakegithub.Server) { s.ReviewFail5xx = 1 << 30 })
	calls := w.recordReviewCalls()
	wi, _ := w.reviewOnly()
	w.agentsDone(wi, "the first submission", func() bool { p, _ := calls.get(); return len(p) > 0 })
	// Base 100ms: lookups at +0.1s, +0.6s, +2.4s, resubmit (3.1s); then
	// +0.2s, +1.2s, +4.8s, resubmit (9.3s after the first).
	w.pumpFor(11 * time.Second)
	posts, lookups := calls.get()
	if len(posts) < 2 || len(posts) > 3 {
		t.Fatalf("%d review POSTs in 11s under persistent 502s, want 2 or 3 (looked up %d times)", len(posts), len(lookups))
	}
	between := 0
	for _, l := range lookups {
		if l.After(posts[0]) && l.Before(posts[1]) {
			between++
		}
	}
	if between < 3 {
		t.Fatalf("%d lookups before the resubmit, want 3: resubmitted after the first miss", between)
	}
	if len(posts) == 3 && posts[2].Sub(posts[1]) <= posts[1].Sub(posts[0]) {
		t.Fatalf("submissions at %v then %v: not spaced increasingly", posts[1].Sub(posts[0]), posts[2].Sub(posts[1]))
	}
	if r := w.dudeReviews(wi, w.gh.Pulls()[0].Number); len(r) != 0 {
		t.Fatalf("reviews %+v", r)
	}
}

// GitHub's Retry-After on a lost answer: nothing is asked of its reviews
// again before it.
func TestALostReviewAnswerWaitsGitHubsRetryAfter(t *testing.T) {
	w := newWorld(t)
	w.gh.SetChecks("success")
	w.withDeliveries(&delivery.Store{ReviewRecoverDelay: 100 * time.Millisecond})
	w.gh.Set(func(s *fakegithub.Server) { s.ReviewFail5xx, s.ReviewRetryAfter = 1<<30, 7 })
	calls := w.recordReviewCalls()
	wi, _ := w.reviewOnly()
	w.agentsDone(wi, "the first submission", func() bool { p, _ := calls.get(); return len(p) > 0 })
	w.pumpFor(8 * time.Second)
	posts, lookups := calls.get()
	for _, l := range lookups {
		if l.After(posts[0]) && l.Sub(posts[0]) < 7*time.Second {
			t.Fatalf("looked for the review %v after a 502 with Retry-After: 7", l.Sub(posts[0]))
		}
	}
	if len(lookups) == 0 || len(posts) != 1 {
		t.Fatalf("%d lookups, %d POSTs: want it looked for once Retry-After passed, not resubmitted", len(lookups), len(posts))
	}
}
