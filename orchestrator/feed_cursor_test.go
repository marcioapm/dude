package orchestrator_test

// The feed follower's cursor against a scripted lux feed: which id it
// stores, when, and that a restart misses nothing lux commits late.

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
	"github.com/marciomartins/dude/orchestrator/internal/servers"
)

// scriptedLux is lux's tenant feed over a list of committed events: a
// connection delivers, in id order, those above its Last-Event-ID, then
// the stream ends (a dropped connection).
type scriptedLux struct {
	lux.Servers
	mu        sync.Mutex
	events    []lux.FeedEvent
	afters    []int64
	delivered []int64
}

func (s *scriptedLux) commit(e ...lux.FeedEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, e...)
	slices.SortFunc(s.events, func(a, b lux.FeedEvent) int { return int(a.ID - b.ID) })
}

func (s *scriptedLux) Feed(_ context.Context, after int64, fn func(lux.FeedEvent) error) error {
	s.mu.Lock()
	s.afters = append(s.afters, after)
	var list []lux.FeedEvent
	for _, e := range s.events {
		if e.ID > after {
			list = append(list, e)
		}
	}
	s.mu.Unlock()
	for _, e := range list {
		if err := fn(e); err != nil {
			return err
		}
		s.mu.Lock()
		s.delivered = append(s.delivered, e.ID)
		s.mu.Unlock()
	}
	return errors.New("lux's feed ended")
}

func feedEvent(id int64, at time.Time) lux.FeedEvent {
	return lux.FeedEvent{ID: id, Type: "run.state", Time: at}
}

func (w *world) feedCursor() int {
	w.t.Helper()
	return w.count(`SELECT after_event_id FROM lux_feed WHERE id = 'default'`)
}

// lux takes ids 5 and 6; 6 commits first. dude sees 6 alone, the stream
// drops, 5 commits: the reconnect still gets 5.
func TestAFeedEventCommittedLateIsNotLostOnReconnect(t *testing.T) {
	w := newWorld(t)
	mustExec(t, w.owner, `INSERT INTO lux_feed (id, after_event_id) VALUES ('default', 4)`)
	l := &scriptedLux{}
	f := &servers.Feed{DB: w.app, Lux: l, Log: quiet, CursorEvery: time.Nanosecond}
	l.commit(feedEvent(6, time.Now()))
	_ = f.Once(context.Background())
	l.commit(feedEvent(5, time.Now().Add(-time.Second)))
	_ = f.Once(context.Background())
	if l.afters[1] >= 5 {
		t.Fatalf("reconnected after %d: event 5, committed after 6, is lost", l.afters[1])
	}
	if !slices.Contains(l.delivered, 5) {
		t.Fatalf("delivered %v, want 5 among them", l.delivered)
	}
}

// The cursor is the highest id whose lux time is older than Settle, not
// the highest received.
func TestTheFeedCursorIsTheHighestSettledId(t *testing.T) {
	w := newWorld(t)
	old, young := time.Now().Add(-2*time.Minute), time.Now()
	l := &scriptedLux{}
	l.commit(feedEvent(10, old), feedEvent(12, old), feedEvent(11, young), feedEvent(13, young))
	mustExec(t, w.owner, `INSERT INTO lux_feed (id, after_event_id) VALUES ('default', 9)`)
	f := &servers.Feed{DB: w.app, Lux: l, Log: quiet, Settle: time.Minute, CursorEvery: time.Nanosecond}
	_ = f.Once(context.Background())
	if c := w.feedCursor(); c != 12 {
		t.Fatalf("cursor %d after 10 and 12 settled, 11 and 13 not; want 12", c)
	}
}

// The cursor is written when it moved and at most once per CursorEvery,
// not once per event.
func TestTheFeedCursorIsWrittenAtMostOncePerInterval(t *testing.T) {
	w := newWorld(t)
	mustExec(t, w.owner, `INSERT INTO lux_feed (id, after_event_id) VALUES ('default', 99)`)
	l := &scriptedLux{}
	for id := int64(100); id < 110; id++ {
		l.commit(feedEvent(id, time.Now().Add(-time.Hour)))
	}
	f := &servers.Feed{DB: w.app, Lux: l, Log: quiet, CursorEvery: time.Hour}
	_ = f.Once(context.Background())
	if c := w.feedCursor(); c != 100 {
		t.Fatalf("cursor %d; want 100, the first write, and none other within the hour", c)
	}
}

// A follower behind another does not move the shared cursor back.
func TestAFeedFollowerBehindLeavesTheCursor(t *testing.T) {
	w := newWorld(t)
	mustExec(t, w.owner, `INSERT INTO lux_feed (id, after_event_id) VALUES ('default', 10)`)
	l := &scriptedLux{}
	l.commit(feedEvent(11, time.Now().Add(-time.Hour)))
	// Another replica moves the cursor on after this one read it.
	ahead := &aheadLux{scriptedLux: l, before: func() { mustExec(t, w.owner, `UPDATE lux_feed SET after_event_id = 5000`) }}
	f := &servers.Feed{DB: w.app, Lux: ahead, Log: quiet, CursorEvery: time.Nanosecond}
	_ = f.Once(context.Background())
	if l.afters[0] != 10 {
		t.Fatalf("followed from %d, want the stored 10", l.afters[0])
	}
	if c := w.feedCursor(); c != 5000 {
		t.Fatalf("a follower behind moved the cursor to %d", c)
	}
}

// aheadLux runs before as the connection opens: another replica moving
// the cursor after this one read it.
type aheadLux struct {
	*scriptedLux
	before func()
}

func (a *aheadLux) Feed(ctx context.Context, after int64, fn func(lux.FeedEvent) error) error {
	a.before()
	return a.scriptedLux.Feed(ctx, after, fn)
}
