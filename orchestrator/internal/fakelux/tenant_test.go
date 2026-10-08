package fakelux_test

// The fake's tenant servers and event feed (lux#41), through dude's real
// lux client: what dude relies on, as lux answers it.

import (
	"context"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// feedLog collects the feed's events from after an id.
type feedLog struct {
	mu  sync.Mutex
	evs []lux.FeedEvent
}

func (f *feedLog) of(typ, server string) []lux.FeedEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []lux.FeedEvent
	for _, e := range f.evs {
		if e.Type == typ && (server == "" || e.ServerID == server) {
			out = append(out, e)
		}
	}
	return out
}

func follow(t *testing.T, c *lux.HTTPClient, after int64) *feedLog {
	t.Helper()
	f := &feedLog{}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		_ = c.Feed(ctx, after, func(e lux.FeedEvent) error {
			f.mu.Lock()
			f.evs = append(f.evs, e)
			f.mu.Unlock()
			return nil
		})
	}()
	return f
}

// servesOnly is a preview's Run with no servers of its own.
var servesOnly = lux.Spec{Image: lux.Image{Ref: "node:22"}, Workload: lux.Workload{Adapter: "generic", Command: []string{"sleep", "infinity"}}}

func TestAWakeableServerAsksItsOwnerOncePerWake(t *testing.T) {
	ctx := context.Background()
	fake, c, runID := started(t, servesOnly)
	feed := follow(t, c, 0)
	sv, err := c.CreateServer(ctx, lux.CreateServer{Name: "web", Port: 3000, Command: []string{"npm", "start"},
		Hostname: "web-t1-p1.lux.test", Wake: "request", Lifetime: "owner", IdleAfter: "10m", ExpireAfter: "720h",
		Labels: map[string]string{"dude.preview": "run_1"}})
	if err != nil {
		t.Fatal(err)
	}
	if sv.State != lux.SrvAsleep || sv.Hostname == nil || *sv.Hostname != "web-t1-p1.lux.test" || sv.URL == nil ||
		*sv.URL != "https://web-t1-p1.lux.test" || sv.RunID != nil {
		t.Fatalf("created %+v", sv)
	}
	if _, err := c.CreateServer(ctx, lux.CreateServer{Name: "web", Port: 3000, Hostname: "WEB-t1-p1.lux.test.", Wake: "request"}); !isLux(err, 409, "hostname_taken") {
		t.Errorf("a second server at the hostname: %v", err)
	}
	if _, err := c.CreateServer(ctx, lux.CreateServer{Name: "web", Port: 3000, Hostname: "web.elsewhere.test"}); !isLux(err, 422, "invalid_server") {
		t.Errorf("a hostname outside the domain: %v", err)
	}
	list, err := c.ListServers(ctx, "web-t1-p1.lux.test")
	if err != nil || len(list) != 1 || list[0].ID != sv.ID {
		t.Fatalf("by hostname: %v %+v", err, list)
	}
	if list, _ := c.ListServers(ctx, "", "dude.preview=run_2"); len(list) != 0 {
		t.Errorf("label filter: %+v", list)
	}

	// Three requests while nothing serves it: one wake.
	for range 3 {
		if fake.RequestServer(sv.ID, "/x") {
			t.Fatal("served while asleep")
		}
	}
	waitFor(t, "the wake on the feed", func() bool { return len(feed.of("server.wake_requested", sv.ID)) == 1 })
	if got, _ := c.GetServer(ctx, sv.ID); got.State != lux.SrvWaking || got.Wakes != 1 {
		t.Errorf("after the wake: %+v", got)
	}
	w := feed.of("server.wake_requested", sv.ID)[0]
	if w.RunID != nil || w.Data["path"] != "/x" || w.Data["hostname"] != "web-t1-p1.lux.test" || w.Data["serverId"] != sv.ID {
		t.Errorf("wake event %+v", w)
	}

	// The owner attaches it to a running Run: it starts, becomes ready,
	// and the wake is resolved.
	if _, err := c.AttachServer(ctx, sv.ID, runID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "ready", func() bool { got, _ := c.GetServer(ctx, sv.ID); return got.State == lux.SrvReady })
	if !fake.RequestServer(sv.ID, "/") {
		t.Error("not served when ready")
	}
	if len(feed.of("server.attached", sv.ID)) != 1 {
		t.Error("no server.attached")
	}
	if !fake.Idle(sv.ID) {
		t.Fatal("not idle")
	}
	waitFor(t, "server.idle", func() bool { return len(feed.of("server.idle", sv.ID)) == 1 })
	if e := feed.of("server.idle", sv.ID)[0]; e.RunID == nil || *e.RunID != runID {
		t.Errorf("idle event %+v", e)
	}

	// Stopped: asleep again, and a request asks again.
	if err := c.Stop(ctx, runID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "asleep", func() bool { got, _ := c.GetServer(ctx, sv.ID); return got.State == lux.SrvAsleep })
	fake.RequestServer(sv.ID, "/")
	waitFor(t, "a second wake", func() bool { return len(feed.of("server.wake_requested", sv.ID)) == 2 })
	if e := feed.of("server.wake_requested", sv.ID)[1]; e.RunID == nil || *e.RunID != runID {
		t.Errorf("second wake names its Run: %+v", e)
	}

	// Deleted: gone from the list, server.deleted on the feed.
	if err := c.DeleteServer(ctx, sv.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetServer(ctx, sv.ID); !lux.IsNotFound(err) {
		t.Errorf("deleted server: %v", err)
	}
	waitFor(t, "server.deleted", func() bool { return len(feed.of("server.deleted", sv.ID)) == 1 })
}

// A follower that reconnects with Last-Event-ID gets every event after it,
// and none before.
func TestTheFeedResumesAfterLastEventID(t *testing.T) {
	ctx := context.Background()
	fake, c, _ := started(t, preview)
	first := follow(t, c, 0)
	var ids []string
	for _, h := range []string{"a-1", "b-1", "c-1"} {
		sv, err := c.CreateServer(ctx, lux.CreateServer{Name: "web", Port: 1, Hostname: h + ".lux.test", Wake: "request"})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, sv.ID)
	}
	waitFor(t, "three created", func() bool { return len(first.of("server.created", "")) == 3 })
	mid := first.of("server.created", ids[0])[0].ID
	fake.DropFeeds()
	again := follow(t, c, mid)
	waitFor(t, "the two after", func() bool { return len(again.of("server.created", "")) == 2 })
	time.Sleep(50 * time.Millisecond)
	if got := again.of("server.created", ids[0]); len(got) != 0 {
		t.Errorf("replayed %v", got)
	}
}

// A resume's sync moves the checkout before its servers start; a running
// Run's sync moves it now; a stopped one's is refused.
func TestSyncOnResumeAndWhileRunning(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	gitInit(t, repo)
	fake := fakelux.New(repo, "k", nil)
	_, c, runID := startedWith(t, fake, lux.Spec{Image: lux.Image{Ref: "x"}, Workload: lux.Workload{Adapter: "generic"},
		Git: &lux.Git{Repositories: []lux.Repository{{Name: "app", URL: "file://" + repo, Ref: "main"}}}})
	if err := c.SyncRun(ctx, runID, "s1", []lux.SyncRef{{Repo: "app", Ref: "main"}}); err != nil {
		t.Fatal(err)
	}
	if err := c.Stop(ctx, runID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "stopped", func() bool { r, _ := c.Get(ctx, runID); return r.State == "stopped" })
	if err := c.SyncRun(ctx, runID, "s2", []lux.SyncRef{{Repo: "app", Ref: "main"}}); !isLux(err, 409, "not_running") {
		t.Errorf("sync of a stopped run: %v", err)
	}
	if _, err := c.Resume(ctx, runID, lux.ResumeInput{Sync: []lux.SyncRef{{Repo: "app", Ref: "main"}}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "running", func() bool { r, _ := c.Get(ctx, runID); return r.State == "running" })
	r := fake.Runs()[0]
	if len(r.Syncs) != 1 || len(r.ResumeSyncs) != 1 || r.ResumeSyncs[0][0] != (lux.SyncRef{Repo: "app", Ref: "main"}) {
		t.Errorf("syncs %v, resume syncs %v", r.Syncs, r.ResumeSyncs)
	}
}

func isLux(err error, status int, code string) bool {
	le, ok := lux.AsError(err)
	return ok && le.Status == status && le.Code == code
}

// Attach refusals carry lux's codes and messages: a Run it does not have
// (not_found "not found", no ids), a server another Run holds (attached),
// a name the Run has (name_taken), a Run that is over (finished). A resume
// of a Run resuming already answers as the first did; one that cannot be
// resumed is 409 not_resumable.
func TestAttachAndResumeRefusalsAreLuxs(t *testing.T) {
	ctx := context.Background()
	fake, c, runA := started(t, servesOnly)
	_, _, runB := startedWith(t, fake, servesOnly)
	mk := func(host string) string {
		sv, err := c.CreateServer(ctx, lux.CreateServer{Name: "web", Port: 3000, Hostname: host + ".lux.test", Wake: "request"})
		if err != nil {
			t.Fatal(err)
		}
		return sv.ID
	}
	one, two := mk("one"), mk("two")
	_, err := c.AttachServer(ctx, one, "lrun_nope")
	if le, _ := lux.AsError(err); !isLux(err, 404, "not_found") || le.Message != "not found" {
		t.Errorf("attach to an unknown Run: %v", err)
	}
	if _, err := c.AttachServer(ctx, one, runA); err != nil {
		t.Fatal(err)
	}
	if _, err := c.AttachServer(ctx, one, runB); !isLux(err, 409, "attached") {
		t.Errorf("attach of a server another Run holds: %v", err)
	}
	if _, err := c.AttachServer(ctx, two, runA); !isLux(err, 409, "name_taken") {
		t.Errorf("attach of a name the Run has: %v", err)
	}
	if err := c.Cancel(ctx, runB); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "cancelled", func() bool { r, _ := c.Get(ctx, runB); return r.State == "terminated" })
	if _, err := c.AttachServer(ctx, two, runB); !isLux(err, 409, "finished") {
		t.Errorf("attach to a cancelled Run: %v", err)
	}
	if _, err := c.Resume(ctx, runB, lux.ResumeInput{}); !isLux(err, 409, "not_resumable") {
		t.Errorf("resume of a cancelled Run: %v", err)
	}
	if err := c.Stop(ctx, runA); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "stopped", func() bool { r, _ := c.Get(ctx, runA); return r.State == "stopped" })
	fake.StartAfter = time.Hour
	if _, err := c.Resume(ctx, runA, lux.ResumeInput{}); err != nil {
		t.Fatal(err)
	}
	if r, err := c.Resume(ctx, runA, lux.ResumeInput{}); err != nil || r.State != "resuming" {
		t.Errorf("a second resume of a resuming Run: %+v %v", r, err)
	}
}

// gitInit makes a repository with one commit on main.
func gitInit(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"-c", "user.name=t", "-c", "user.email=t@x", "commit", "-q", "--allow-empty", "-m", "A"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
}
