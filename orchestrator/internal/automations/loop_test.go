package automations

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// gate is a file a test's commands wait on: a command made by hold runs
// until release creates it.
type gate string

func newGate(t *testing.T) gate { return gate(filepath.Join(t.TempDir(), "go")) }

func (g gate) hold(then string) string {
	return fmt.Sprintf("while [ ! -e %s ]; do sleep 0.05; done; %s", string(g), then)
}

func (g gate) release(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(string(g), nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

// A webhook endpoint that does not answer and live agents whose output is
// still open cost the sweep nothing: a pass returns while the endpoint is
// still silent, having matched an unrelated delivery and started its run.
func TestAutomationSlowWorkStaysOffTheSweep(t *testing.T) {
	h := newHarness(t)
	answer := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(answer) }) }
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-answer
		w.WriteHeader(200)
	}))
	t.Cleanup(hook.Close)
	// Runs before hook.Close, which waits for the held request.
	t.Cleanup(release)
	h.automation("aut_live", "agent", map[string]any{"kind": "github", "event": "push"},
		map[string]any{"concurrency": "parallel", "agent": map[string]any{"own": map[string]any{"tierId": h.hangTier(), "prompt": "Work"}}})
	exec(t, h.owner, `UPDATE automations SET parallel_max = 4 WHERE id = 'aut_live'`)
	h.automation("aut_slowhook", "webhook", map[string]any{"kind": "github", "event": "release"},
		map[string]any{"webhook": map[string]any{"url": hook.URL}})
	for i := range 4 {
		h.deliver(fmt.Sprintf("p-%d", i), "push", map[string]any{"ref": "refs/heads/main", "repository": map[string]any{"full_name": "acme/web"}})
	}
	h.deliver("rel-1", "release", map[string]any{"action": "published", "repository": map[string]any{"full_name": "acme/web"}})
	h.until("four live agents and the webhook POSTing", func() bool {
		live, hk := 0, h.runs("aut_slowhook")
		for _, r := range h.runs("aut_live") {
			if r.Status == "running" {
				live++
			}
		}
		return live == 4 && len(hk) == 1 && hk[0].Status == "running"
	})

	h.automation("aut_other", "command", map[string]any{"kind": "github", "event": "issues"}, map[string]any{"command": "echo other"})
	h.deliver("i-1", "issues", map[string]any{"action": "opened", "repository": map[string]any{"full_name": "acme/web"}})
	done := make(chan error, 1)
	go func() { _, err := h.runner.Sweep(context.Background()); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		release()
		<-done
		t.Fatal("a pass waited on the silent webhook endpoint")
	}
	if r := h.runs("aut_other"); len(r) != 1 || r[0].Status != "running" {
		t.Fatalf("after one pass the unrelated run is %+v", r)
	}
	release()
	h.until("the webhook and the unrelated run succeed", func() bool {
		hk, o := h.runs("aut_slowhook"), h.runs("aut_other")
		return hk[0].Status == "succeeded" && o[0].Status == "succeeded"
	})
}

// With more live runs than workers, the newest is still followed: a command
// started after Workers live agents ends, and a webhook is POSTed while
// every follower is busy.
func TestAutomationNewestLiveRunIsFollowed(t *testing.T) {
	h := newHarness(t)
	h.runner.Workers = 2
	var posts atomic.Int32
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts.Add(1)
		w.WriteHeader(200)
	}))
	t.Cleanup(hook.Close)
	h.automation("aut_busy_agents", "agent", map[string]any{"kind": "github", "event": "push"},
		map[string]any{"concurrency": "parallel", "agent": map[string]any{"own": map[string]any{"tierId": h.hangTier(), "prompt": "Work"}}})
	exec(t, h.owner, `UPDATE automations SET parallel_max = 2 WHERE id = 'aut_busy_agents'`)
	for i := range 2 {
		h.deliver(fmt.Sprintf("busy-%d", i), "push", map[string]any{"ref": "refs/heads/main", "repository": map[string]any{"full_name": "acme/web"}})
	}
	h.until("two live agents", func() bool {
		live := 0
		for _, r := range h.runs("aut_busy_agents") {
			if r.Status == "running" && r.Session != "" {
				live++
			}
		}
		return live == 2
	})
	h.automation("aut_newest", "command", map[string]any{"kind": "github", "event": "issues"}, map[string]any{"command": "echo hi"})
	h.automation("aut_newest_hook", "webhook", map[string]any{"kind": "github", "event": "issues"},
		map[string]any{"webhook": map[string]any{"url": hook.URL}})
	h.deliver("newest-1", "issues", map[string]any{"action": "opened", "repository": map[string]any{"full_name": "acme/web"}})
	h.until("the newest command and webhook succeed", func() bool {
		c, w := h.runs("aut_newest"), h.runs("aut_newest_hook")
		return len(c) == 1 && c[0].Status == "succeeded" && len(w) == 1 && w[0].Status == "succeeded"
	})
	if n := posts.Load(); n != 1 {
		t.Errorf("webhook POSTs %d", n)
	}
	for _, r := range h.runs("aut_busy_agents") {
		if r.Status != "running" {
			t.Errorf("a live agent ended: %+v", r)
		}
	}
}

// stuckGet is lux whose Get never answers until released: a follower that
// holds its worker for as long as the test says.
type stuckGet struct {
	lux.Client
	held chan struct{}
}

func (s *stuckGet) Get(ctx context.Context, id string) (lux.Run, error) {
	select {
	case <-s.held:
	case <-ctx.Done():
		return lux.Run{}, ctx.Err()
	}
	return s.Client.Get(ctx, id)
}

// A webhook is POSTed while every follower worker is held by a live run.
func TestAutomationWebhookDoesNotWaitForFollowers(t *testing.T) {
	h := newHarness(t)
	h.runner.Workers = 1
	stuck := &stuckGet{Client: h.lux, held: make(chan struct{})}
	h.runner.Lux = stuck
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(stuck.held) }) })
	var posts atomic.Int32
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts.Add(1)
		w.WriteHeader(200)
	}))
	t.Cleanup(hook.Close)
	h.automation("aut_held", "agent", map[string]any{"kind": "github", "event": "push"},
		map[string]any{"agent": map[string]any{"own": map[string]any{"tierId": h.hangTier(), "prompt": "Work"}}})
	h.deliver("held-1", "push", map[string]any{"ref": "refs/heads/main", "repository": map[string]any{"full_name": "acme/web"}})
	h.until("the agent is live", func() bool { r := h.runs("aut_held"); return len(r) == 1 && r[0].Session != "" })
	h.automation("aut_held_hook", "webhook", map[string]any{"kind": "github", "event": "issues"},
		map[string]any{"webhook": map[string]any{"url": hook.URL}})
	h.deliver("held-hook", "issues", map[string]any{"action": "opened", "repository": map[string]any{"full_name": "acme/web"}})
	h.until("the webhook is POSTed", func() bool { r := h.runs("aut_held_hook"); return len(r) == 1 && r[0].Status == "succeeded" })
	if n := posts.Load(); n != 1 {
		t.Errorf("webhook POSTs %d", n)
	}
	once.Do(func() { close(stuck.held) })
}

// More live webhook runs than a pass reads, all POSTing to an endpoint that
// holds them, take no place from other runs: a command started after them
// is still followed and ends.
func TestAutomationWebhookBurstLeavesFollowersTheirPlaces(t *testing.T) {
	h := newHarness(t)
	h.runner.Workers, h.runner.FollowLimit = 2, 3
	answer := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(answer) }) }
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-answer:
		case <-r.Context().Done():
		}
		w.WriteHeader(200)
	}))
	t.Cleanup(hook.Close)
	t.Cleanup(release)
	const burst = 6
	for i := range burst {
		h.automation(fmt.Sprintf("aut_burst_%d", i), "webhook", map[string]any{"kind": "github", "event": "issues"},
			map[string]any{"webhook": map[string]any{"url": hook.URL}})
	}
	h.deliver("burst-1", "issues", map[string]any{"action": "opened", "repository": map[string]any{"full_name": "acme/web"}})
	h.until("every webhook run is live", func() bool {
		var n int
		if err := h.owner.QueryRow(context.Background(), `SELECT count(*) FROM automation_runs
			WHERE automation_id LIKE 'aut_burst_%' AND status = 'running' AND session IS NOT NULL`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n == burst
	})
	h.automation("aut_after_burst", "command", map[string]any{"kind": "github", "event": "push"}, map[string]any{"command": "echo hi"})
	h.deliver("after-burst-1", "push", map[string]any{"ref": "refs/heads/main", "repository": map[string]any{"full_name": "acme/web"}})
	h.until("the command is followed and ends", func() bool {
		r := h.runs("aut_after_burst")
		return len(r) == 1 && r[0].Status == "succeeded"
	})
}

// A webhook run started after more live runs than a pass reads, none of
// them followed yet, is POSTed on the first pass that follows.
func TestAutomationNewWebhookIsPostedPastAFullFollowList(t *testing.T) {
	h := newHarness(t)
	h.runner.Workers, h.runner.FollowLimit = 2, 2
	g := newGate(t)
	t.Cleanup(func() { g.release(t) })
	var posts atomic.Int32
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts.Add(1)
		w.WriteHeader(200)
	}))
	t.Cleanup(hook.Close)
	ctx := context.Background()
	// Matched and started directly, never followed: their followed_at stays NULL.
	pass := func() {
		t.Helper()
		if _, err := h.runner.matchDeliveries(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := h.runner.startQueued(ctx); err != nil {
			t.Fatal(err)
		}
	}
	const live = 3
	for i := range live {
		h.automation(fmt.Sprintf("aut_unfollowed_%d", i), "command", map[string]any{"kind": "github", "event": "push"},
			map[string]any{"command": g.hold("echo done")})
	}
	h.deliver("unfollowed-1", "push", map[string]any{"ref": "refs/heads/main", "repository": map[string]any{"full_name": "acme/web"}})
	pass()
	var n int
	if err := h.owner.QueryRow(ctx, `SELECT count(*) FROM automation_runs WHERE automation_id LIKE 'aut_unfollowed_%'
		AND status = 'running' AND session IS NOT NULL AND followed_at IS NULL`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != live {
		t.Fatalf("live unfollowed runs %d, want %d", n, live)
	}
	h.automation("aut_late_hook", "webhook", map[string]any{"kind": "github", "event": "issues"},
		map[string]any{"webhook": map[string]any{"url": hook.URL}})
	h.deliver("late-hook-1", "issues", map[string]any{"action": "opened", "repository": map[string]any{"full_name": "acme/web"}})
	pass()
	if _, err := h.runner.followRunning(ctx); err != nil {
		t.Fatal(err)
	}
	// Only the POST is waited for; the held commands' followers return at once.
	for deadline := time.Now().Add(10 * time.Second); posts.Load() == 0 && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
	}
	if n := posts.Load(); n != 1 {
		t.Fatalf("webhook POSTs after one following pass: %d", n)
	}
}

// Twenty automations whose queued run waits behind their own live run hold
// back no other automation: an unrelated run starts at once.
func TestAutomationQueueHeadsWithoutRoomBlockNothing(t *testing.T) {
	h := newHarness(t)
	g := newGate(t)
	t.Cleanup(func() { g.release(t) })
	push := map[string]any{"ref": "refs/heads/main", "repository": map[string]any{"full_name": "acme/web"}}
	const blocked = 20
	for i := range blocked {
		h.automation(fmt.Sprintf("aut_blocked_%02d", i), "command", map[string]any{"kind": "github", "event": "push"},
			map[string]any{"command": g.hold("echo done"), "concurrency": "queue"})
	}
	count := func(status string) int {
		var n int
		if err := h.owner.QueryRow(context.Background(), `SELECT count(*) FROM automation_runs
			WHERE automation_id LIKE 'aut_blocked_%' AND status = $1`, status).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	h.deliver("blocked-1", "push", push)
	h.until("twenty runs are running", func() bool { return count("running") == blocked })
	h.deliver("blocked-2", "push", push)
	h.until("twenty runs are queued behind them", func() bool { return count("queued") == blocked })
	h.automation("aut_unblocked", "command", map[string]any{"kind": "github", "event": "issues"}, map[string]any{"command": g.hold("echo other")})
	h.deliver("unblocked-1", "issues", map[string]any{"action": "opened", "repository": map[string]any{"full_name": "acme/web"}})
	// One pass matches and starts it; three leave room for a slow host.
	for range 3 {
		if _, err := h.runner.Sweep(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if r := h.runs("aut_unblocked"); len(r) != 1 || r[0].Status != "running" {
		t.Fatalf("after three passes the unrelated run is %+v", r)
	}
	if n := count("queued"); n != blocked {
		t.Errorf("queued runs %d, want %d: %+v", n, blocked, h.runs("aut_blocked_00"))
	}
}

// turnEndHook is lux that runs late once, right after an Output read in
// which the Run's agent went idle after busy: as a follower decides the
// turn is done.
type turnEndHook struct {
	lux.Client
	late func()
	busy bool
	once sync.Once
}

func (h *turnEndHook) Output(ctx context.Context, runID, cursor string, after int64, fn func(lux.Frame) error) error {
	ended := false
	err := h.Client.Output(ctx, runID, cursor, after, func(f lux.Frame) error {
		if f.Event != nil && f.Event.Type == "lux.activity" {
			var d map[string]any
			_ = json.Unmarshal(f.Event.Data, &d)
			switch d["activity"] {
			case "busy":
				h.busy = true
			case "idle":
				ended = ended || h.busy
			}
		}
		return fn(f)
	})
	if ended {
		h.once.Do(h.late)
	}
	return err
}

// Concurrency send: an event sent into an agent run as its turn ends is
// given to the agent; the run ends only after that turn.
func TestAutomationSendAtTurnEndReachesTheAgent(t *testing.T) {
	h := newHarness(t)
	push := map[string]any{"ref": "refs/heads/main", "repository": map[string]any{"full_name": "acme/web"}}
	hook := &turnEndHook{Client: h.lux}
	hook.late = func() {
		ctx := context.Background()
		if err := h.app.InOrg(ctx, h.org, func(tx pgx.Tx) error {
			a, err := Load(ctx, tx, "aut_send_late")
			if err != nil {
				return err
			}
			body, _ := json.Marshal(push)
			_, err = Fire(ctx, tx, a, SourceEvent, []Payload{{DeliveryID: "late", Event: "push", Body: body}}, "", nil)
			return err
		}); err != nil {
			t.Error(err)
		}
	}
	h.runner.Lux = hook
	h.automation("aut_send_late", "agent", map[string]any{"kind": "github", "event": "push"},
		map[string]any{"concurrency": "send", "session_mode": "resumable", "expiry_days": 7,
			"agent": map[string]any{"own": map[string]any{"tierId": h.tier, "prompt": "Look at {{event.delivery_id}}"}}})
	h.deliver("first", "push", push)
	h.until("the run ends", func() bool {
		r := h.runs("aut_send_late")
		return len(r) > 0 && r[0].Status != "running" && r[0].Status != "queued"
	})
	h.runner.Wait()
	r := h.runs("aut_send_late")
	if len(r) != 1 || r[0].Status != "succeeded" || r[0].Payloads != 2 {
		t.Fatalf("runs %+v", r)
	}
	if in := h.fake.InputBodies(r[0].LuxRunID, "automation-"+r[0].ID+"-1"); len(in) != 1 || !strings.Contains(in[0], "Look at late") {
		t.Errorf("the late event's input %q", in)
	}
}

// submitHook is lux with a hook run inside Submit, before lux answers.
type submitHook struct {
	lux.Client
	before func()
}

func (s *submitHook) Submit(ctx context.Context, spec lux.Spec, key string) (lux.Run, error) {
	if s.before != nil {
		s.before()
	}
	return s.Client.Submit(ctx, spec, key)
}

// A run is claimed before it is submitted: an event cancelling it while lux
// takes the submit leaves it cancelled and its new lux Run ended, never
// running beside the newer run; with merge, an event arriving then queues
// its own run rather than joining payloads the start already read.
func TestAutomationRunIsClaimedBeforeItStarts(t *testing.T) {
	h := newHarness(t)
	g := newGate(t)
	t.Cleanup(func() { g.release(t) })
	push := map[string]any{"ref": "refs/heads/main", "repository": map[string]any{"full_name": "acme/web"}}
	hook := &submitHook{Client: h.lux}
	h.runner.Lux = hook
	fireDuringSubmit := func(id, delivery string) {
		var once sync.Once
		hook.before = func() {
			once.Do(func() {
				ctx := context.Background()
				var fired Fired
				if err := h.app.InOrg(ctx, h.org, func(tx pgx.Tx) error {
					a, err := Load(ctx, tx, id)
					if err != nil {
						return err
					}
					body, _ := json.Marshal(push)
					fired, err = Fire(ctx, tx, a, SourceEvent, []Payload{{DeliveryID: delivery, Event: "push", Body: body}}, "", nil)
					return err
				}); err != nil {
					t.Error(err)
				}
				h.runner.EndCancelled(ctx, fired.Cancelled)
			})
		}
	}

	h.automation("aut_claim_cancel", "command", map[string]any{"kind": "github", "event": "push"},
		map[string]any{"command": g.hold("echo done"), "concurrency": "cancel"})
	fireDuringSubmit("aut_claim_cancel", "late-1")
	h.deliver("first-1", "push", push)
	h.until("the newer run is running", func() bool {
		r := h.runs("aut_claim_cancel")
		return len(r) == 2 && r[1].Status == "running"
	})
	r := h.runs("aut_claim_cancel")
	if r[0].Status != "cancelled" || r[0].LuxRunID != "" {
		t.Fatalf("the run cancelled while it was submitted: %+v", r[0])
	}
	for _, run := range h.fake.Runs() {
		if strings.Contains(string(run.Spec), r[0].ID) {
			h.until("its lux Run is ended", func() bool { return lux.Terminated(h.luxState(run.ID)) })
		}
	}

	h.automation("aut_claim_merge", "command", map[string]any{"kind": "github", "event": "push"},
		map[string]any{"command": g.hold("echo done"), "concurrency": "merge"})
	exec(t, h.owner, `UPDATE automations SET enabled = false WHERE id = 'aut_claim_cancel'`)
	fireDuringSubmit("aut_claim_merge", "late-2")
	h.deliver("first-2", "push", push)
	h.until("the first merge run is running and the late event waits", func() bool {
		r := h.runs("aut_claim_merge")
		return len(r) == 2 && r[0].Status == "running" && r[1].Status == "queued"
	})
	if m := h.runs("aut_claim_merge"); m[0].Payloads != 1 || m[1].Payloads != 1 {
		t.Errorf("merge runs %+v", m)
	}
}

// A Server run is not ended idle while an event sent into it is still to be
// POSTed: one arriving as the idle stop is decided is POSTed to the same
// container, then the run ends.
func TestAutomationServerKeepsAnEventThatArrivesAsItIdles(t *testing.T) {
	h := newHarness(t)
	var mu sync.Mutex
	var deliveries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		deliveries = append(deliveries, r.Header.Get("X-GitHub-Delivery"))
		mu.Unlock()
		w.WriteHeader(202)
	}))
	t.Cleanup(srv.Close)
	var inject atomic.Bool
	var once sync.Once
	h.runner.ServerURL = func(ctx context.Context, _ string, _ int) (string, error) {
		if inject.Load() {
			once.Do(func() {
				if err := h.app.InOrg(ctx, h.org, func(tx pgx.Tx) error {
					a, err := Load(ctx, tx, "aut_srv_late")
					if err != nil {
						return err
					}
					body, _ := json.Marshal(prBody("synchronize", "acme/web", "ana"))
					_, err = Fire(ctx, tx, a, SourceEvent, []Payload{{DeliveryID: "d-late", Event: "pull_request", Body: body}}, "", nil)
					return err
				}); err != nil {
					t.Error(err)
				}
			})
		}
		return srv.URL, nil
	}
	h.automation("aut_srv_late", "server", map[string]any{"kind": "github", "event": "pull_request"},
		map[string]any{"server": map[string]any{"command": "bun run serve", "port": 8080, "path": "/events", "health": nil}})
	h.deliver("d-1", "pull_request", prBody("opened", "acme/web", "ana"))
	h.until("the first POST", func() bool { mu.Lock(); defer mu.Unlock(); return len(deliveries) == 1 })
	h.runner.Wait()
	inject.Store(true)
	h.advance(ServerIdle + time.Second)
	h.until("the run ends", func() bool { r := h.runs("aut_srv_late"); return len(r) == 1 && r[0].Status != "running" })
	r := h.runs("aut_srv_late")[0]
	mu.Lock()
	defer mu.Unlock()
	if r.Status != "succeeded" || r.Payloads != 2 || len(deliveries) != 2 || deliveries[1] != "d-late" {
		t.Errorf("run %+v, POSTs %v", r, deliveries)
	}
}

// An organisation automation whose repository filter cannot be read covers
// no repository: it never widens to all of them.
func TestAutomationUnreadableRepoFilterCoversNothing(t *testing.T) {
	h := newHarness(t)
	h.automation("aut_badfilter", "command", map[string]any{"kind": "github", "event": "push"},
		map[string]any{"command": "echo fired", "project_id": nil})
	h.automation("aut_goodfilter", "command", map[string]any{"kind": "github", "event": "push"},
		map[string]any{"command": "echo fired", "project_id": nil})
	exec(t, h.owner, `UPDATE automations SET repo_filter = '{"mode": 7, "repos": "acme/*"}' WHERE id = 'aut_badfilter'`)
	exec(t, h.owner, `UPDATE automations SET repo_filter = '{"mode": "only", "repos": ["acme/*"]}' WHERE id = 'aut_goodfilter'`)
	h.deliver("p-f", "push", map[string]any{"ref": "refs/heads/main", "repository": map[string]any{"full_name": "acme/web"}})
	// Fired is what is under test: the fake cannot clone the event's remote
	// repository, so how the run ends is not.
	h.until("the readable filter fires", func() bool { return len(h.runs("aut_goodfilter")) == 1 })
	h.until("the delivery is read", func() bool {
		var read bool
		_ = h.owner.QueryRow(context.Background(), `SELECT automations_at IS NOT NULL FROM webhook_deliveries WHERE id = 'p-f'`).Scan(&read)
		return read
	})
	if r := h.runs("aut_badfilter"); len(r) != 0 {
		t.Errorf("an unreadable filter fired: %+v", r)
	}
}

// runNowTest fires a written test event on an event automation.
func (h *harness) runNowTest(id, delivery string, body map[string]any, event string) string {
	h.t.Helper()
	b, _ := json.Marshal(body)
	runID, v, err := h.runner.RunNow(context.Background(), h.org, id, h.person, "", &Payload{DeliveryID: delivery, Event: event, Body: b})
	if err != nil || !v.Matches {
		h.t.Fatalf("run now: %v %+v", err, v)
	}
	return runID
}

func (h *harness) run(automation, id string) runRow {
	h.t.Helper()
	for _, r := range h.runs(automation) {
		if r.ID == id {
			return r
		}
	}
	h.t.Fatalf("no run %s", id)
	return runRow{}
}

// A test run shares nothing with event runs (Q21), in the database's
// decisions too: it starts while an event run is live; a live test run
// neither makes an event skip (skip), nor is cancelled by one (cancel),
// nor is sent one (send), nor holds an event run in the queue (queue).
func TestAutomationTestRunSharesNothingWithLiveRuns(t *testing.T) {
	h := newHarness(t)
	g := newGate(t)
	t.Cleanup(func() { g.release(t) })
	push := map[string]any{"ref": "refs/heads/main", "repository": map[string]any{"full_name": "acme/web"}}
	h.automation("aut_q21", "command", map[string]any{"kind": "github", "event": "push"},
		map[string]any{"command": g.hold("echo done"), "concurrency": "skip"})
	set := func(c string) { exec(t, h.owner, `UPDATE automations SET concurrency = $1 WHERE id = 'aut_q21'`, c) }

	// An event run is live: a test run starts beside it, never queued.
	h.deliver("q-1", "push", push)
	h.until("the event run is running", func() bool { r := h.runs("aut_q21"); return len(r) == 1 && r[0].Status == "running" })
	event1 := h.runs("aut_q21")[0].ID
	test1 := h.runNowTest("aut_q21", "test-1", push, "push")
	h.until("the test run is running beside it", func() bool { return h.run("aut_q21", test1).Status == "running" })

	// cancel: an event cancels the event run, not the test run.
	set("cancel")
	h.deliver("q-2", "push", push)
	h.until("the second event run is running", func() bool { r := h.runs("aut_q21"); return len(r) == 3 && r[2].Status == "running" })
	if e, tr := h.run("aut_q21", event1), h.run("aut_q21", test1); e.Status != "cancelled" || tr.Status != "running" {
		t.Fatalf("cancel: event %+v test %+v", e, tr)
	}
	event2 := h.runs("aut_q21")[2].ID

	// skip with only the test run live (the event run cancelled by hand): an event starts.
	set("skip")
	exec(t, h.owner, `UPDATE automation_runs SET status = 'cancelled', ended_at = now() WHERE id = $1`, event2)
	h.deliver("q-3", "push", push)
	h.until("the third event is read", func() bool { return len(h.runs("aut_q21")) == 4 })
	if r := h.runs("aut_q21")[3]; r.Status == "skipped" {
		t.Fatalf("skip with only a test run live: %+v", r)
	}
	event3 := h.runs("aut_q21")[3].ID
	h.until("the third event run is running", func() bool { return h.run("aut_q21", event3).Status == "running" })

	// queue with only the test run live: an event starts, never waits behind it.
	set("queue")
	exec(t, h.owner, `UPDATE automation_runs SET status = 'cancelled', ended_at = now() WHERE id = $1`, event3)
	h.deliver("q-4", "push", push)
	h.until("the fourth event run is running", func() bool { r := h.runs("aut_q21"); return len(r) == 5 && r[4].Status == "running" })
	if h.run("aut_q21", test1).Payloads != 1 {
		t.Errorf("the test run was given an event: %+v", h.run("aut_q21", test1))
	}
}

// Q21: a test run fired while an event run waits in the queue starts at
// once; it never waits behind the queued event run.
func TestAutomationTestRunDoesNotWaitBehindAQueuedEventRun(t *testing.T) {
	h := newHarness(t)
	g := newGate(t)
	t.Cleanup(func() { g.release(t) })
	push := map[string]any{"ref": "refs/heads/main", "repository": map[string]any{"full_name": "acme/web"}}
	h.automation("aut_rq", "command", map[string]any{"kind": "github", "event": "push"},
		map[string]any{"command": g.hold("echo done"), "concurrency": "queue"})
	h.deliver("rq-1", "push", push)
	h.until("the first event run is running", func() bool { r := h.runs("aut_rq"); return len(r) == 1 && r[0].Status == "running" })
	h.deliver("rq-2", "push", push)
	h.until("the second event run is queued", func() bool { r := h.runs("aut_rq"); return len(r) == 2 && r[1].Status == "queued" })
	test := h.runNowTest("aut_rq", "test-rq", push, "push")
	h.until("the test run is running while an event run is queued", func() bool {
		r := h.runs("aut_rq")
		return h.run("aut_rq", test).Status == "running" && r[1].Status == "queued"
	})
}

// send: an event arriving while only a test run of a resumable agent is
// live starts its own run; the test run is sent nothing.
func TestAutomationSendSkipsALiveTestRun(t *testing.T) {
	h := newHarness(t)
	push := map[string]any{"ref": "refs/heads/main", "repository": map[string]any{"full_name": "acme/web"}}
	h.automation("aut_q21_send", "agent", map[string]any{"kind": "github", "event": "push"},
		map[string]any{"concurrency": "send", "session_mode": "resumable", "expiry_days": 7,
			"agent": map[string]any{"own": map[string]any{"tierId": h.hangTier(), "prompt": "Look at {{event.delivery_id}}"}}})
	test := h.runNowTest("aut_q21_send", "test-s", push, "push")
	h.until("the test run is running", func() bool { return h.run("aut_q21_send", test).Status == "running" })
	h.deliver("e-s", "push", push)
	h.until("the event run is running", func() bool { r := h.runs("aut_q21_send"); return len(r) == 2 && r[1].Status == "running" })
	if tr := h.run("aut_q21_send", test); tr.Payloads != 1 {
		t.Errorf("the test run was sent the event: %+v", tr)
	}
	if ev := h.runs("aut_q21_send")[1]; ev.Session != "new" || ev.LuxRunID == h.run("aut_q21_send", test).LuxRunID {
		t.Errorf("event run %+v", ev)
	}
}

// A delivery an automation cannot be matched to (a paths filter with no
// GitHub to read the pull request's files from) holds up nothing: a later
// push still fires its own automation, a Run now on a schedule still starts,
// and after MatchAttempts tries the stuck automation gets a skipped run
// saying why and the delivery is marked read.
func TestAutomationLoopGoesOnPastADeliveryItCannotMatch(t *testing.T) {
	h := newHarness(t)
	h.automation("aut_paths", "command", map[string]any{"kind": "github", "event": "pull_request",
		"filters": map[string]any{"paths": []string{"src/"}}}, map[string]any{"command": "echo paths"})
	h.automation("aut_push", "command", map[string]any{"kind": "github", "event": "push"}, map[string]any{"command": "echo pushed"})
	h.automation("aut_nightly", "command", map[string]any{"kind": "schedule", "cron": "0 3 * * *", "tz": "UTC"},
		map[string]any{"command": "echo nightly"})
	h.deliver("d-pr", "pull_request", prBody("opened", "acme/web", "ana"))
	h.deliver("d-push", "push", map[string]any{"ref": "refs/heads/main", "repository": map[string]any{"full_name": "acme/web"}})
	if _, _, err := h.runner.RunNow(context.Background(), h.org, "aut_nightly", h.person, "", nil); err != nil {
		t.Fatal(err)
	}
	h.until("the push and the Run now succeed", func() bool {
		p, s := h.runs("aut_push"), h.runs("aut_nightly")
		return len(p) == 1 && p[0].Status == "succeeded" && len(s) == 1 && s[0].Status == "succeeded"
	})
	if r := h.runs("aut_paths"); len(r) != 0 {
		t.Fatalf("paths automation fired on its first try: %+v", r)
	}
	var marked bool
	var attempts int
	read := func() {
		t.Helper()
		if err := h.owner.QueryRow(context.Background(), `SELECT automations_at IS NOT NULL, automations_attempts FROM webhook_deliveries WHERE id = 'd-pr'`).
			Scan(&marked, &attempts); err != nil {
			t.Fatal(err)
		}
	}
	read()
	if marked || attempts != 1 {
		t.Fatalf("after one try: marked %v attempts %d", marked, attempts)
	}
	for i := 0; i < MatchAttempts; i++ {
		h.advance(11 * time.Minute)
		if _, err := h.runner.Sweep(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	read()
	r := h.runs("aut_paths")
	if !marked || len(r) != 1 || r[0].Status != "skipped" ||
		r[0].Reason != "it could not be matched: no GitHub to read the pull request's files from" {
		t.Fatalf("after %d tries: marked %v runs %+v", attempts, marked, r)
	}
	// The push automation was not fired again by the retries.
	if p := h.runs("aut_push"); len(p) != 1 {
		t.Errorf("push runs %+v", p)
	}
	if !strings.Contains(h.runs("aut_push")[0].Log, "pushed") {
		t.Errorf("push log %q", h.runs("aut_push")[0].Log)
	}
}

// A delivery whose whole match fails with a database error on every try is
// marked read after MatchAttempts, and each automation it was for gets a
// skipped run saying why: the dropped event shows in its Runs.
func TestAutomationDeliveryDroppedAfterItsLastTryLeavesASkippedRun(t *testing.T) {
	h := newHarness(t)
	h.automation("aut_drop", "command", map[string]any{"kind": "github", "event": "push"}, map[string]any{"command": "echo pushed"})
	// The database refuses this delivery's queued run, failing its whole match.
	exec(t, h.owner, `CREATE FUNCTION refuse_drop() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'refused for the test'; END $$`)
	exec(t, h.owner, `CREATE TRIGGER refuse_drop BEFORE INSERT ON automation_runs FOR EACH ROW
		WHEN (NEW.status <> 'skipped' AND NEW.payloads @> '[{"deliveryId": "d-drop"}]') EXECUTE FUNCTION refuse_drop()`)
	h.deliver("d-drop", "push", map[string]any{"ref": "refs/heads/main", "repository": map[string]any{"full_name": "acme/web"}})
	for i := 0; i < MatchAttempts; i++ {
		_, _ = h.runner.Sweep(context.Background())
		if i == 0 {
			if r := h.runs("aut_drop"); len(r) != 0 {
				t.Fatalf("after the first try: %+v", r)
			}
		}
		h.advance(11 * time.Minute)
	}
	var marked bool
	if err := h.owner.QueryRow(context.Background(), `SELECT automations_at IS NOT NULL FROM webhook_deliveries WHERE id = 'd-drop'`).
		Scan(&marked); err != nil {
		t.Fatal(err)
	}
	r := h.runs("aut_drop")
	if !marked || len(r) != 1 || r[0].Status != "skipped" || r[0].Payloads != 1 ||
		!strings.HasPrefix(r[0].Reason, "it could not be matched: ") || !strings.Contains(r[0].Reason, "refused for the test") {
		t.Fatalf("marked %v runs %+v", marked, r)
	}
}
