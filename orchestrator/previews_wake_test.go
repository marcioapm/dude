package orchestrator_test

// Wakeable branch previews (lux#41) through the orchestrator's real code:
// the internal API, the preview loop, the feed follower, the lux client,
// against the fake lux's servers and feed.

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
	"github.com/marciomartins/dude/orchestrator/internal/servers"
)

const previewDomain = "preview-absmartly.test"

// wakeable turns the world's previews into wakeable ones and starts a feed
// follower, as the orchestrator does when lux has a preview domain.
func (w *world) wakeable() *servers.Feed {
	w.t.Helper()
	w.lux.PreviewDomain = previewDomain
	w.lux.IdleCheck = time.Hour // idleness only when a test says so
	w.previews.PreviewDomain = previewDomain
	feed := w.newFeed()
	w.startFeed(feed)
	return feed
}

// newFeed is a follower that settles and writes its cursor at once.
func (w *world) newFeed() *servers.Feed {
	return &servers.Feed{DB: w.app, Lux: w.previews.Lux, Log: quiet, Settle: time.Millisecond,
		CursorEvery: time.Millisecond, Retry: 10 * time.Millisecond}
}

func (w *world) startFeed(feed *servers.Feed) context.CancelFunc {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { feed.Run(ctx); close(done) }()
	w.t.Cleanup(func() { cancel(); <-done })
	return func() { cancel(); <-done }
}

// declare starts a preview of a new task and waits for it to be asleep.
func (w *world) declare() (task, runID string) {
	w.t.Helper()
	task = w.task()
	code, out := w.do("POST", "/internal/tasks/"+task+"/preview", nil)
	if code != 201 {
		w.t.Fatalf("start = %d %v", code, out)
	}
	runID = out["run"].(map[string]any)["id"].(string)
	w.until("the preview to be asleep", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND wakeable`, runID) == 1
	})
	return task, runID
}

// serverID is the lux id dude stored for a preview's server.
func (w *world) serverID(runID, name string) string {
	w.t.Helper()
	var id string
	if err := w.owner.QueryRow(context.Background(), `SELECT lux_server_id FROM preview_servers WHERE run_id = $1 AND name = $2`,
		runID, name).Scan(&id); err != nil {
		w.t.Fatal(err)
	}
	return id
}

func (w *world) str(sql string, args ...any) string {
	w.t.Helper()
	var s string
	if err := w.owner.QueryRow(context.Background(), sql, args...).Scan(&s); err != nil {
		w.t.Fatal(err)
	}
	return s
}

// open is a signed-in browser opening a server's URL until it is served.
func (w *world) open(serverID string) {
	w.t.Helper()
	w.until("the server to serve", func() bool { return w.lux.RequestServer(serverID, "/") })
}

func (w *world) luxRuns() []*fakelux.Run { return w.lux.Runs() }

// heard waits, without sweeping, for the feed follower to record a wake.
func (w *world) heard(runID string) {
	w.t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if w.count(`SELECT count(*) FROM runs WHERE id = $1 AND wake_wanted_at IS NOT NULL`, runID) == 1 {
			return
		}
	}
	w.t.Fatal("the wake was never heard")
}

func TestDeclaringAPreviewCreatesItsServersAndNothingElse(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	w.recipe("api", 4000, "go run .", "api", nil, true)
	w.recipe("docs", 5000, "npm run docs", "docs", nil, false)
	mustExec(t, w.owner, `UPDATE projects SET preview_settings = '{"idleTimeoutMinutes":45}' WHERE id = $1`, w.project)
	task, runID := w.declare()

	if n := len(w.luxRuns()); n != 0 {
		t.Fatalf("declaring submitted %d lux runs", n)
	}
	hosts := w.lux.TenantServers()
	want := []string{
		servers.PreviewHostname(previewDomain, "api", task, w.project, ""),
		servers.PreviewHostname(previewDomain, "web", task, w.project, ""),
	}
	var got []string
	for h := range hosts {
		got = append(got, h)
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("lux has %v, want %v", got, want)
	}
	for _, name := range []string{"web", "api"} {
		sv, ok := w.lux.TenantServer(w.serverID(runID, name))
		if !ok {
			t.Fatalf("%s not in lux", name)
		}
		host := servers.PreviewHostname(previewDomain, name, task, w.project, "")
		if strings.Count(strings.TrimSuffix(host, "."+previewDomain), ".") != 0 {
			t.Errorf("%s is more than one label under the domain", host)
		}
		if sv.State != lux.SrvAsleep || *sv.Hostname != host || *sv.URL != "https://"+host || sv.RunID != nil ||
			sv.Labels["dude.preview"] != runID || sv.Labels["dude.task"] != task || sv.Labels["dude.project"] != w.project ||
			sv.Labels["dude.org"] != w.org {
			t.Errorf("%s = %+v", name, sv)
		}
		raw := string(sv.Raw)
		for _, frag := range []string{`"wake":"request"`, `"lifetime":"owner"`, `"idleAfter":"45m0s"`, `"expireAfter":"720h0m0s"`} {
			if !strings.Contains(raw, frag) {
				t.Errorf("%s lacks %s: %s", name, frag, raw)
			}
		}
		if w.str(`SELECT url FROM preview_servers WHERE run_id = $1 AND name = $2`, runID, name) != "https://"+host {
			t.Errorf("dude's url for %s", name)
		}
	}
	// The task's servers show lux's servers, at their stable URLs, asleep.
	code, out := w.do("GET", "/internal/tasks/"+task+"/servers", nil)
	if code != 200 {
		t.Fatal(code, out)
	}
	run := out["run"].(map[string]any)
	if run["asleep"] != true || run["wakeable"] != true || run["previewStage"] != nil {
		t.Errorf("run view %v", run)
	}
	web := serverNamed(out, "web")
	if web == nil || web["url"] != "https://"+servers.PreviewHostname(previewDomain, "web", task, w.project, "") ||
		web["serverState"] != "asleep" || web["state"] != "stopped" {
		t.Errorf("web = %v", web)
	}
}

// A running wakeable preview's view shows lux's servers of it, read by
// label: its Run's own server list is not asked for.
func TestARunningWakeablePreviewsViewReadsServersByLabel(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	task, runID := w.declare()
	w.open(w.serverID(runID, "web"))
	w.running(runID, "web")
	calls := &countingLux{Client: w.previews.Lux}
	w.previews.Lux = calls
	code, out := w.do("GET", "/internal/tasks/"+task+"/servers", nil)
	if web := serverNamed(out, "web"); code != 200 || web == nil || web["serverState"] != "ready" {
		t.Fatalf("view = %d %v", code, out)
	}
	calls.mu.Lock()
	defer calls.mu.Unlock()
	if calls.runServers != 0 || calls.serverLists != 1 {
		t.Errorf("the view asked lux %d run server lists and %d label lists; want 0 and 1", calls.runServers, calls.serverLists)
	}
}

// The first request wakes a preview that never ran: dude submits its Run
// and attaches every server; later requests while it comes up ask nothing.
// Idle on one of two servers keeps it; both park it. The next request
// resumes it with every repository synced to the task's branch.
func TestAPreviewWakesSleepsAndWakesOnTheLatestCommit(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	w.recipe("api", 4000, "go run .", "api", nil, true)
	_, runID := w.declare()
	web, api := w.serverID(runID, "web"), w.serverID(runID, "api")

	w.lux.RequestServer(web, "/a")
	w.lux.RequestServer(api, "/b")
	w.open(web)
	runs := w.luxRuns()
	if len(runs) != 1 {
		t.Fatalf("%d lux runs after a wake", len(runs))
	}
	r := runs[0]
	if spec := submitted(t, r); len(spec.Workload.Servers) != 0 || spec.Labels["dude.preview"] != runID {
		t.Errorf("submitted spec servers %v labels %v", spec.Workload.Servers, spec.Labels)
	}
	if calls := w.lux.CallsOf(r.ID); !slices.Contains(calls, "server.attach "+web) || !slices.Contains(calls, "server.attach "+api) {
		t.Errorf("calls %v", calls)
	}
	w.open(api)
	w.until("dude to see it running", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running'`, runID) == 1
	})

	// One idle: kept.
	if !w.lux.Idle(web) {
		sv, _ := w.lux.TenantServer(web)
		t.Fatalf("web not idle-able: %s", sv.Raw)
	}
	w.until("dude to hear the idle", func() bool {
		return w.count(`SELECT count(*) FROM preview_servers WHERE lux_server_id = $1 AND idle_event_id > 0`, web) == 1
	})
	for range 3 {
		w.pump()
	}
	if slices.Contains(w.lux.CallsOf(r.ID), "stop") {
		t.Fatal("stopped with one of two servers in use")
	}
	// Both: parked.
	w.lux.Idle(api)
	w.until("the preview to be parked", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND lux_state = 'stopped'`, runID) == 1
	})
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.parked'`, runID); n != 1 {
		t.Errorf("%d run.parked", n)
	}

	// Asleep; a request wakes it: the same Run, resumed, synced.
	w.lux.RequestServer(api, "/")
	w.open(web)
	if n := len(w.luxRuns()); n != 1 {
		t.Fatalf("%d lux runs: a new one was submitted instead of a resume", n)
	}
	if r.Resumed != 1 || len(r.ResumeSyncs) != 1 || !slices.Equal(r.ResumeSyncs[0], []lux.SyncRef{{Repo: "target", Ref: "main"}}) {
		t.Fatalf("resumed %d with %v", r.Resumed, r.ResumeSyncs)
	}
	if _, ok := tokenIn(r.ResumeSecrets[0]); !ok {
		t.Errorf("resumed without the forge token: %v", r.ResumeSecrets[0])
	}
}

// tokenIn finds the forge token among a resume's secrets.
func tokenIn(secrets []lux.Secret) (string, bool) {
	for _, s := range secrets {
		if s.Name == "GIT_TOKEN" && s.Value != "" {
			return s.Value, true
		}
	}
	return "", false
}

// A server.idle that a request has overtaken (lux's lastRequestAt is later
// than the one the event carried) does not count: the Run is kept.
func TestARequestAfterAnIdleKeepsTheRun(t *testing.T) {
	w := newWorld(t)
	feed := w.wakeable()
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	_, runID := w.declare()
	web := w.serverID(runID, "web")
	w.open(web)
	w.until("running", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running'`, runID) == 1
	})
	r := w.luxRuns()[0]
	w.lux.RequestServer(web, "/") // lastRequestAt now
	stale := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
	if err := feed.Apply(context.Background(), lux.FeedEvent{ID: 1 << 40, Type: "server.idle", ServerID: web,
		Data: map[string]any{"lastRequestAt": stale}}); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		w.pump()
	}
	if slices.Contains(w.lux.CallsOf(r.ID), "stop") {
		t.Fatal("stopped although a request came after its idle")
	}
	if n := w.count(`SELECT count(*) FROM preview_servers WHERE lux_server_id = $1 AND idle_at IS NULL`, web); n != 1 {
		t.Error("the overtaken idle mark was kept")
	}
}

// One wake, however many times its event is seen (a replay, a second
// follower) and however many servers ask: one resume.
func TestOneWakeIsOneResume(t *testing.T) {
	w := newWorld(t)
	feed := w.wakeable()
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	w.recipe("api", 4000, "go run .", "api", nil, true)
	_, runID := w.declare()
	web, api := w.serverID(runID, "web"), w.serverID(runID, "api")
	w.open(web)
	r := w.luxRuns()[0]
	w.lux.Idle(web)
	w.lux.Idle(api)
	w.until("parked", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND lux_state = 'stopped'`, runID) == 1
	})

	// A second follower, as on another orchestrator, and the same event
	// applied again by hand.
	w.startFeed(w.newFeed())
	w.lux.RequestServer(web, "/")
	w.lux.RequestServer(api, "/")
	var wakeID int64
	w.until("the wake to be heard", func() bool {
		wakeID = int64(w.count(`SELECT wake_event_id FROM runs WHERE id = $1`, runID))
		return wakeID > 0
	})
	ev := lux.FeedEvent{ID: wakeID, Type: "server.wake_requested", ServerID: web}
	if err := feed.Apply(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	w.open(web)
	w.open(api)
	for range 3 {
		w.pump()
	}
	if err := feed.Apply(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND wake_wanted_at IS NOT NULL`, runID); n != 0 {
		t.Fatal("a wake event seen again asked for a wake again")
	}
	for range 3 {
		w.pump()
	}
	if r.Resumed != 1 || len(w.luxRuns()) != 1 {
		t.Fatalf("one wake: %d resumes, %d runs", r.Resumed, len(w.luxRuns()))
	}
	// Asleep again, the latest wake event replayed (a follower restarting
	// behind it): still nothing.
	w.lux.Idle(web)
	w.lux.Idle(api)
	w.until("parked again", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND lux_state = 'stopped'`, runID) == 1
	})
	ev.ID = int64(w.count(`SELECT wake_event_id FROM runs WHERE id = $1`, runID))
	if err := feed.Apply(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		w.pump()
	}
	if r.Resumed != 1 {
		t.Fatalf("a replayed wake resumed the preview: %d resumes", r.Resumed)
	}
}

// Two orchestrators sweeping the same wake at once: one resume, one submit.
func TestTwoOrchestratorsActOnAWakeOnce(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	_, runID := w.declare()
	web := w.serverID(runID, "web")
	// Both orchestrators' lux requests, counted as sent: lux would answer
	// a second submit or resume harmlessly, but a wake is one orchestrator's.
	calls := &countingLux{Client: w.previews.Lux}
	w.previews.Lux = calls
	other := &servers.Previews{Service: &servers.Service{DB: w.app, Lux: calls, Log: quiet, PreviewDomain: previewDomain},
		Forges: w.previews.Forges, DefaultImage: "default:img"}
	t.Cleanup(other.Stop)
	sweepBoth := func() {
		var wg sync.WaitGroup
		for _, p := range []*servers.Previews{w.previews, other} {
			wg.Add(1)
			go func() { defer wg.Done(); _, _ = p.Sweep(context.Background()) }()
		}
		wg.Wait()
	}
	w.lux.RequestServer(web, "/")
	w.heard(runID)
	for range 10 {
		sweepBoth()
	}
	w.open(web)
	if n := len(w.luxRuns()); n != 1 {
		t.Fatalf("%d lux runs submitted for one wake", n)
	}
	if calls.submits != 1 {
		t.Fatalf("%d submits asked of lux for one wake", calls.submits)
	}
	r := w.luxRuns()[0]
	w.lux.Idle(web)
	w.until("parked", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND lux_state = 'stopped'`, runID) == 1
	})
	w.lux.RequestServer(web, "/")
	w.heard(runID)
	for range 10 {
		sweepBoth()
	}
	w.open(web)
	if _, resumes := calls.counts(); r.Resumed != 1 || resumes != 1 {
		t.Fatalf("%d resumes accepted, %d asked, for one wake on two orchestrators", r.Resumed, resumes)
	}
}

// The task's branch moving while the preview runs syncs it; while it
// sleeps, nothing is asked (its next wake syncs).
func TestAPushSyncsARunningPreviewOnly(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	task, runID := w.declare()
	web := w.serverID(runID, "web")
	w.open(web)
	r := w.luxRuns()[0]
	w.until("running", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running'`, runID) == 1
	})

	// An agent of the task publishes (runs.heads), as the phase syncer does.
	publish := func(n int) {
		mustExec(t, w.owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, phase, status, branch, heads)
			VALUES ($1, $2, $3, $4, 1, 'implement', 'running', 'dude/x', '{}')`, fmt.Sprintf("run_agent_%d_%s", n, w.org), w.org, w.project, task)
		mustExec(t, w.owner, `UPDATE runs SET heads = '{"target":{"sha":"abc"}}', status = 'completed' WHERE id = $1`,
			fmt.Sprintf("run_agent_%d_%s", n, w.org))
	}
	publish(1)
	w.until("the running preview to sync", func() bool { return len(r.Syncs) == 1 })
	if want := []lux.SyncRef{{Repo: "target", Ref: "dude/x"}}; !slices.Equal(r.Syncs[0], want) {
		t.Errorf("synced %v, want %v", r.Syncs[0], want)
	}

	// A person pushes to the task's pull request's branch: the forge's new
	// head (webhook or reconciler) syncs it too.
	mustExec(t, w.owner, `INSERT INTO pull_requests (id, organization_id, project_id, task_id, repository_id, number, url,
		head_branch, base_branch, head_sha, title) VALUES ($1, $2, $3, $4, $5, 1, 'u', 'dude/x', 'main', 'a1', 't')`,
		"pr_"+w.org, w.org, w.project, task, w.repoID)
	mustExec(t, w.owner, `UPDATE pull_requests SET head_sha = 'b2' WHERE id = $1`, "pr_"+w.org)
	w.until("the forge push to sync", func() bool { return len(r.Syncs) == 2 })

	w.lux.Idle(web)
	w.until("parked", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND lux_state = 'stopped'`, runID) == 1
	})
	publish(2)
	w.until("the sync to be dropped", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND sync_wanted_at IS NULL`, runID) == 1
	})
	syncs := 0
	for _, c := range w.lux.CallsOf(r.ID) {
		if c == "sync" {
			syncs++
		}
	}
	if len(r.Syncs) != 2 || syncs != 2 {
		t.Errorf("a sleeping preview was synced: %d accepted, %d asked", len(r.Syncs), syncs)
	}
}

// Previews nobody has woken for longer than the reap age end, their lux
// servers deleted and their Run cancelled; younger ones stay.
func TestTheReaperEndsPreviewsUnusedPastItsAge(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.previews.ReapAfter = 7 * 24 * time.Hour
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	_, old := w.declare()
	_, young := w.declare()
	oldWeb := w.serverID(old, "web")
	w.open(oldWeb)
	r := w.luxRuns()[0]
	w.lux.Idle(oldWeb)
	w.until("parked", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND lux_state = 'stopped'`, old) == 1
	})
	mustExec(t, w.owner, `UPDATE preview_servers SET last_woken_at = now() - interval '7 days 1 hour' WHERE run_id = $1`, old)
	mustExec(t, w.owner, `UPDATE runs SET created_at = now() - interval '6 days 23 hours' WHERE id = $1`, young)
	w.until("the old preview to be reaped", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'completed' AND lux_stop_reason = 'cancel'`, old) == 1
	})
	if !slices.Contains(w.lux.DeletedServers, oldWeb) || !r.Cancelled {
		t.Errorf("deleted %v, cancelled %v", w.lux.DeletedServers, r.Cancelled)
	}
	for range 3 {
		w.pump()
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused'`, young); n != 1 {
		t.Errorf("a preview younger than the reap age was reaped:\n%s", w.describeRuns())
	}
	if _, ok := w.lux.TenantServer(w.serverID(young, "web")); !ok {
		t.Error("the young preview's server was deleted")
	}
}

// A preview declared and never opened is reaped by its age.
func TestANeverWokenPreviewIsReaped(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.previews.ReapAfter = 7 * 24 * time.Hour
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	_, runID := w.declare()
	web := w.serverID(runID, "web")
	mustExec(t, w.owner, `UPDATE runs SET created_at = now() - interval '7 days 1 hour' WHERE id = $1`, runID)
	w.until("reaped", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'completed'`, runID) == 1
	})
	w.until("its server deleted", func() bool { _, ok := w.lux.TenantServer(web); return !ok })
}

// The reaper's clock is the latest wake of any of the preview's servers,
// and a running preview is not reaped however long ago it was woken.
func TestTheReaperGoesByTheLatestWakeAndSparesARunningPreview(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.previews.ReapAfter = 7 * 24 * time.Hour
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	w.recipe("api", 4000, "go run .", "api", nil, true)
	_, runID := w.declare()
	web := w.serverID(runID, "web")
	mustExec(t, w.owner, `UPDATE runs SET created_at = now() - interval '30 days' WHERE id = $1`, runID)
	mustExec(t, w.owner, `UPDATE preview_servers SET last_woken_at = CASE name WHEN 'api' THEN now() - interval '8 days'
		ELSE now() - interval '1 hour' END WHERE run_id = $1`, runID)
	for range 3 {
		w.pump()
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused'`, runID); n != 1 || w.labelled(runID) != 2 {
		t.Fatalf("a preview whose web was woken an hour ago was reaped:\n%s", w.describeRuns())
	}

	w.open(web)
	w.running(runID, "web")
	mustExec(t, w.owner, `UPDATE preview_servers SET last_woken_at = now() - interval '8 days' WHERE run_id = $1`, runID)
	for range 3 {
		w.pump()
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running'`, runID); n != 1 {
		t.Fatalf("a running preview was reaped:\n%s", w.describeRuns())
	}
}

// deleteGone deletes a server in lux before dude does, as lux would have
// had dude crashed after its DELETE: dude's own DELETE then answers 404.
type deleteGone struct {
	lux.Client
}

func (d deleteGone) DeleteServer(ctx context.Context, id string) error {
	_ = d.Client.DeleteServer(ctx, id)
	return d.Client.DeleteServer(ctx, id)
}

// A server lux has deleted already (404 on dude's DELETE), with no
// server.deleted heard (dude crashed after its DELETE, before recording
// it): the server is done, and the Run is cancelled after it.
func TestAnEndWhoseServerIsGoneStillCancelsTheRun(t *testing.T) {
	w := newWorld(t)
	w.lux.PreviewDomain, w.previews.PreviewDomain = previewDomain, previewDomain
	w.lux.IdleCheck = time.Hour
	stopFeed := w.startFeed(w.newFeed())
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	task, runID := w.declare()
	w.open(w.serverID(runID, "web"))
	r := w.luxRuns()[0]
	stopFeed()
	w.previews.Lux = deleteGone{Client: w.previews.Lux}
	mustExec(t, w.owner, `UPDATE tasks SET status = 'done' WHERE id = $1`, task)
	w.until("its Run cancelled", func() bool {
		mustExec(t, w.owner, `UPDATE runs SET next_attempt_at = NULL WHERE id = $1`, runID)
		return r.Cancelled
	})
}

// A task that ends takes its preview with it: its lux servers deleted
// first, then its Run cancelled.
func TestAFinishedTasksPreviewDeletesItsServersThenCancelsItsRun(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	task, runID := w.declare()
	web := w.serverID(runID, "web")
	w.open(web)
	r := w.luxRuns()[0]
	mustExec(t, w.owner, `UPDATE tasks SET status = 'done' WHERE id = $1`, task)
	w.until("the preview to end", func() bool { return r.Cancelled })
	calls := w.lux.CallsOf(r.ID)
	del, cancel := slices.Index(calls, "server.delete "+web), slices.Index(calls, "cancel")
	if del < 0 || cancel < 0 || del > cancel {
		t.Errorf("calls %v: want the server deleted before the Run cancelled", calls)
	}
	if n := w.count(`SELECT count(*) FROM preview_servers WHERE run_id = $1 AND deleted_at IS NOT NULL`, runID); n != 1 {
		t.Error("dude did not record the server gone")
	}
	// A person's DELETE of an asleep one, never run: its servers go too.
	task2, run2 := w.declare()
	web2 := w.serverID(run2, "web")
	if code, _ := w.do("DELETE", "/internal/tasks/"+task2+"/preview", nil); code != 200 {
		t.Fatal(code)
	}
	w.until("its server deleted", func() bool { return slices.Contains(w.lux.DeletedServers, web2) })
	// Nothing of it is left in lux: marked so, it leaves the sweep.
	w.until("the never-run preview marked done in lux", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND lux_stop_reason = 'cancel' AND lux_run_id IS NULL`, run2) == 1
	})
}

// More busy previews than one sweep takes do not push a due wake off the
// page: a wake is taken first, ahead of previews only re-followed and of
// park checks due; one sweep acts on it.
func TestADueWakeIsTakenPastAFullPageOfBusyPreviews(t *testing.T) {
	for _, busy := range []string{"re-followed", "park check due"} {
		t.Run(busy, func(t *testing.T) {
			w := newWorld(t)
			w.wakeable()
			w.previews.SweepLimit = 3
			w.recipe("web", 3000, "npm run dev", "", nil, true)
			_, runID := w.declare()
			web := w.serverID(runID, "web")
			// Older than the asleep one, so a page in created_at order is all theirs.
			for i := range 5 {
				id := fmt.Sprintf("run_busy_%d_%s", i, w.org)
				mustExec(t, w.owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, kind, wakeable,
					lux_run_id, lux_state, created_at) VALUES ($1, $2, $3, $4, 1, 'running', 'preview', true, $5, 'running', now() - interval '1 day')`,
					id, w.org, w.project, w.task(), fmt.Sprintf("lux_busy_%d_%s", i, w.org))
				if busy == "park check due" {
					mustExec(t, w.owner, `INSERT INTO preview_servers (run_id, organization_id, name, lux_server_id, hostname, idle_at)
						VALUES ($1, $2, 'web', $3, $3, now())`, id, w.org, "srv_busy_"+id)
				}
			}
			w.lux.RequestServer(web, "/")
			w.heard(runID)
			if _, err := w.previews.Sweep(context.Background()); err != nil {
				t.Fatal(err)
			}
			if n := len(w.luxRuns()); n != 1 {
				t.Fatalf("%d lux runs after one sweep with a wake due; want the wake's", n)
			}
			w.open(web)
		})
	}
}

// lux expiring a preview's server, or someone deleting it in lux, ends the
// preview, in the ledger too.
func TestAServerGoneInLuxEndsItsPreview(t *testing.T) {
	for _, why := range []string{"expired", "deleted"} {
		t.Run(why, func(t *testing.T) {
			w := newWorld(t)
			w.wakeable()
			w.recipe("web", 3000, "npm run dev", "", nil, true)
			_, runID := w.declare()
			web := w.serverID(runID, "web")
			if why == "expired" {
				w.lux.Expire(web)
			} else if err := w.previews.Lux.DeleteServer(context.Background(), web); err != nil {
				t.Fatal(err)
			}
			w.until("the preview to end", func() bool {
				return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'completed' AND error LIKE '%'||$2||'%'`, runID, why) == 1
			})
			if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.completed'
				AND payload->>'reason' = $2`, runID, why); n != 1 {
				t.Errorf("%d run.completed events with reason %s", n, why)
			}
		})
	}
}

// A hostname another server of lux holds is chosen again, salted.
func TestATakenHostnameIsChosenAgain(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	task := w.task()
	taken := servers.PreviewHostname(previewDomain, "web", task, w.project, "")
	if _, err := w.previews.Lux.CreateServer(context.Background(), lux.CreateServer{Name: "web", Port: 1, Hostname: taken}); err != nil {
		t.Fatal(err)
	}
	code, out := w.do("POST", "/internal/tasks/"+task+"/preview", nil)
	if code != 201 {
		t.Fatal(code, out)
	}
	runID := out["run"].(map[string]any)["id"].(string)
	w.until("asleep", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused'`, runID) == 1
	})
	got := w.str(`SELECT hostname FROM preview_servers WHERE run_id = $1`, runID)
	if want := servers.PreviewHostname(previewDomain, "web", task, w.project, runID); got != want || got == taken {
		t.Errorf("hostname %s, want %s", got, want)
	}
}

// A first follower (no cursor) stores one from its first connection, with
// no new event needed: a restart before lux says anything misses nothing.
func TestAFirstFeedFollowerStoresACursorAtOnce(t *testing.T) {
	w := newWorld(t)
	w.lux.PreviewDomain, w.previews.PreviewDomain = previewDomain, previewDomain
	w.lux.IdleCheck = time.Hour
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	w.declare() // lux has events; nobody follows yet
	w.startFeed(w.newFeed())
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if w.count(`SELECT count(*) FROM lux_feed`) == 1 {
			return
		}
	}
	t.Fatal("no cursor stored by a first follower with nothing new on the feed")
}

// The follower resumes after the cursor it stored: what lux said while it
// was down is applied when it comes back.
func TestTheFeedResumesFromItsStoredCursor(t *testing.T) {
	w := newWorld(t)
	w.lux.PreviewDomain, w.previews.PreviewDomain = previewDomain, previewDomain
	w.lux.IdleCheck = time.Hour
	feed := w.newFeed()
	stop := w.startFeed(feed)
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	_, runID := w.declare()
	web := w.serverID(runID, "web")
	w.until("a cursor", func() bool { return w.count(`SELECT count(*) FROM lux_feed`) == 1 })
	stop()
	before := w.count(`SELECT after_event_id FROM lux_feed`)

	w.lux.RequestServer(web, "/") // while no one follows
	for range 3 {
		w.pump()
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND wake_wanted_at IS NOT NULL`, runID); n != 0 {
		t.Fatal("a wake was heard with no follower")
	}
	w.startFeed(w.newFeed())
	w.open(web)
	if after := w.count(`SELECT after_event_id FROM lux_feed`); after <= before {
		t.Errorf("cursor %d, was %d", after, before)
	}
}

// A preview from before (its servers in its Run's spec, parked by dude)
// keeps that path while the new ones wake on request.
func TestAnOldStylePreviewKeepsItsPath(t *testing.T) {
	w := newWorld(t)
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	wi := w.task()
	if code, _ := w.do("POST", "/internal/tasks/"+wi+"/preview", nil); code != 201 {
		t.Fatal(code)
	}
	w.until("the old preview to run", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND status = 'running' AND NOT wakeable`, wi) == 1
	})
	w.wakeable()
	for range 3 {
		w.pump()
	}
	old := w.luxRuns()[0]
	if spec := submitted(t, old); len(spec.Workload.Servers) != 1 || spec.Workload.Servers[0].Name != "web" {
		t.Errorf("old preview spec servers %v", spec.Workload.Servers)
	}
	if len(w.lux.TenantServers()) != 0 || w.count(`SELECT count(*) FROM preview_servers`) != 0 {
		t.Error("the old preview got lux servers")
	}
	// Stopped by a person, as before: cancelled, no servers to delete.
	if code, _ := w.do("DELETE", "/internal/tasks/"+wi+"/preview", nil); code != 200 {
		t.Fatal(code)
	}
	w.until("cancelled", func() bool { return old.Cancelled })
	_, runID := w.declare()
	if w.count(`SELECT count(*) FROM preview_servers WHERE run_id = $1`, runID) != 1 {
		t.Error("a new preview is not wakeable")
	}
}

// Starting a server of an asleep preview in dude wakes it, as a request
// to its URL would.
func TestStartingAServerOfAnAsleepPreviewWakesIt(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	_, runID := w.declare()
	if code, out := w.do("POST", "/internal/runs/"+runID+"/servers/web/start", nil); code != 200 {
		t.Fatalf("start = %d %v", code, out)
	}
	w.until("a Run serving it", func() bool {
		sv, _ := w.lux.TenantServer(w.serverID(runID, "web"))
		return sv.State == lux.SrvReady
	})
}

// Anything but a start on an asleep preview waits until it runs: 409
// asleep, and no wake asked for.
func TestStoppingAServerOfAnAsleepPreviewIsRefused(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	_, runID := w.declare()
	code, out := w.do("POST", "/internal/runs/"+runID+"/servers/web/stop", nil)
	if e, _ := out["error"].(map[string]any); code != 409 || e["code"] != "asleep" {
		t.Fatalf("stop = %d %v", code, out)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND wake_wanted_at IS NULL`, runID); n != 1 {
		t.Error("a stop asked for a wake")
	}
}
