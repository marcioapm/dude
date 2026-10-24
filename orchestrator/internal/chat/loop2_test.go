package chat_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/fakeslack"
)

// The provider's Retry-After, longer than the backoff, is waited for; and
// no wait is longer than the 10-minute cap, whatever the provider asks.
func TestChatWaitsForTheProvidersRetryAfterUpToTheCap(t *testing.T) {
	w := setup(t)
	w.fake.FailRetryAfter("conversations.info", "ratelimited", 1, 40)
	_, ev := w.receive("Eli", "C1", "", "hello")
	w.once()
	if e := w.event(ev); e.Processed || e.NextIn < 37*time.Second || e.NextIn > 41*time.Second {
		t.Fatalf("after a 429 with Retry-After: 40: %+v", e)
	}
	w.fake.FailRetryAfter("conversations.info", "ratelimited", 1, 3600)
	_, ev = w.receive("Eli", "C1", "", "again")
	w.once()
	if e := w.event(ev); e.Processed || e.NextIn < 9*time.Minute || e.NextIn > 10*time.Minute+time.Second {
		t.Fatalf("after a 429 with Retry-After: 3600: %+v", e)
	}
}

// A worker that dies holding a delivery's lease keeps it from others until
// the lease runs out, then it is claimed again and routed once.
func TestChatADeadWorkersLeaseIsReclaimedOnceItRunsOut(t *testing.T) {
	w := setup(t)
	clock := time.Now()
	var mu sync.Mutex
	w.svc.Now = func() time.Time { mu.Lock(); defer mu.Unlock(); return clock }
	advance := func(d time.Duration) { mu.Lock(); clock = clock.Add(d); mu.Unlock() }
	w.model.queue(answer(map[string]any{"route": "task", "confidence": 0.9, "project": "ABS", "title": "T", "goal": "g"}))
	// The first worker hangs after the routing decision, as a dead process would.
	dead := make(chan struct{})
	first := true
	w.svc.SetBeforeMake(func(context.Context) error {
		mu.Lock()
		hang := first
		first = false
		mu.Unlock()
		if hang {
			<-dead
		}
		return nil
	})
	_, ev := w.receive("Eli", "C1", "", "file it")
	if n, err := w.svc.Sweep(w.ctx); err != nil || n != 1 {
		t.Fatalf("claimed %d (%v)", n, err)
	}
	until := func() bool { return w.event(ev).Decision }
	for deadline := time.Now().Add(10 * time.Second); !until(); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the first worker never decided")
		}
	}
	advance(2 * time.Minute)
	if n, err := w.svc.Sweep(w.ctx); err != nil || n != 0 {
		t.Fatalf("claimed %d (%v) within the lease", n, err)
	}
	advance(2 * time.Minute)
	if n, err := w.svc.Sweep(w.ctx); err != nil || n != 1 {
		t.Fatalf("claimed %d (%v) after the lease ran out", n, err)
	}
	for deadline := time.Now().Add(10 * time.Second); !w.event(ev).Processed; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the reclaimed delivery was not routed")
		}
	}
	// The dead worker wakes: it finds the request made and makes nothing.
	close(dead)
	w.svc.Wait()
	if got := w.requests(); len(got) != 1 || got[0].Route != "task" || w.model.calls() != 1 ||
		w.count(`SELECT count(*) FROM tasks WHERE organization_id = $1`, w.org) != 1 {
		t.Fatalf("requests %+v, %d model calls", got, w.model.calls())
	}
}

// Two clicks on one question routed at once file one choice: the second
// to commit finds the question answered and rolls its work back.
func TestChatTwoClicksAtOnceFileOneChoice(t *testing.T) {
	w := setup(t)
	w.model.queue(answer(map[string]any{"route": "task", "confidence": 0.6, "project": "ABS", "title": "Fix the export", "goal": "Fix it."}))
	root := w.mention("Eli", "C1", "", "the export is broken")
	q := lastBot(t, w.fake)
	for i := range 2 {
		raw := mustJSON(fakeslack.Click("T1", "C1", q.TS, root, "U2", buttonValue(t, q, i)))
		body := []byte("payload=" + urlQueryEscape(string(raw)))
		req, _ := fakeslack.SignedRequest("http://x/", signingSecret, "application/x-www-form-urlencoded", body, time.Now())
		if _, err := w.svc.Receive(w.ctx, "slack", true, req.Header, body); err != nil {
			t.Fatal(err)
		}
	}
	// Each its own thread, so both are claimed in one sweep.
	w.exec(`UPDATE chat_events SET thread_key = event_id WHERE kind = 'interaction' AND organization_id = $1`, w.org)
	var mu sync.Mutex
	arrived := 0
	both := make(chan struct{})
	w.svc.SetBeforeMake(func(context.Context) error {
		mu.Lock()
		arrived++
		if arrived == 2 {
			close(both)
		}
		mu.Unlock()
		<-both
		return nil
	})
	if n, err := w.svc.Sweep(w.ctx); err != nil || n != 2 {
		t.Fatalf("claimed %d (%v)", n, err)
	}
	w.svc.Wait()
	made := w.count(`SELECT count(*) FROM tasks WHERE organization_id = $1`, w.org) +
		w.count(`SELECT count(*) FROM sessions WHERE organization_id = $1`, w.org)
	if made != 1 || len(w.fake.Updates()) != 1 || len(w.requests()) != 2 {
		t.Fatalf("made %d, %d updates, requests %+v", made, len(w.fake.Updates()), w.requests())
	}
}

// An attempt past its deadline keeps no decision: the retry asks the model
// again and routes the answer, rather than recording a failed route.
func TestChatADeadlineKeepsNothingAndTheRetryAsksAgain(t *testing.T) {
	w := setup(t)
	w.svc.SetDeliveryTimeout(500 * time.Millisecond)
	task := answer(map[string]any{"route": "task", "confidence": 0.9, "project": "ABS", "title": "T", "goal": "g"})
	w.model.queue(task, task)
	w.model.delay = 3 * time.Second
	_, ev := w.receive("Eli", "C1", "", "file it")
	w.once()
	if e := w.event(ev); e.Processed || e.Decision || e.Attempts != 1 || !strings.Contains(e.LastError, "deadline") {
		t.Fatalf("after the deadline: %+v", e)
	}
	if got := w.requests(); len(got) != 0 {
		t.Fatalf("requests %+v", got)
	}
	w.model.mu.Lock()
	w.model.delay = 0
	w.model.mu.Unlock()
	w.due()
	w.once()
	if got := w.requests(); len(got) != 1 || got[0].Route != "task" || w.model.calls() != 2 {
		t.Fatalf("requests %+v, %d model calls", got, w.model.calls())
	}
}

// A provider's retry of a delivery already routed is stored as nothing new:
// it is not queued again (whatever handled() would then say).
func TestChatAProvidersRetryOfARoutedDeliveryIsNotQueued(t *testing.T) {
	w := setup(t)
	ts := w.fake.Post("C1", "", "U1", "<@UBT1> hello")
	raw := mustJSON(fakeslack.Mention("T1", "EvRetry", "C1", "", ts, "U1", "hello"))
	receive := func() {
		req, _ := fakeslack.SignedRequest("http://x/", signingSecret, "application/json", raw, time.Now())
		if _, err := w.svc.Receive(w.ctx, "slack", false, req.Header, raw); err != nil {
			t.Fatal(err)
		}
	}
	w.model.queue(answer(map[string]any{"route": "brainstorm", "confidence": 0.9, "title": "Hello"}))
	receive()
	w.once()
	if !w.event("EvRetry").Processed {
		t.Fatal("not routed")
	}
	receive()
	if e := w.event("EvRetry"); !e.Processed {
		t.Fatalf("the retry queued the delivery again: %+v", e)
	}
	if n := w.once(); n != 0 {
		t.Fatalf("claimed %d after the provider's retry", n)
	}
}
