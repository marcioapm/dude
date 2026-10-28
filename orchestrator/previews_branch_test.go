package orchestrator_test

// Branch previews (a project's branch, no task) through the preview loop,
// the feed and the fake lux and GitHub.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
	"github.com/marciomartins/dude/orchestrator/internal/servers"
)

// startBranch starts a preview of branch and waits for it to be asleep.
func (w *world) startBranch(branch string) string {
	w.t.Helper()
	gh, err := w.previews.Forges.For(context.Background(), w.org)
	if err != nil {
		w.t.Fatal(err)
	}
	b, err := w.previews.StartBranchPreview(context.Background(), gh, w.org, w.project, branch, "")
	if err != nil {
		w.t.Fatalf("start %s: %v", branch, err)
	}
	w.until("the branch preview to be asleep", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND wakeable`, b.ID) == 1
	})
	return b.ID
}

func refusedCode(err error) string {
	var se *servers.Error
	if errors.As(err, &se) {
		return se.Code
	}
	return ""
}

// A branch preview serves the primary repository at its branch, every
// other repository at the branch where it has it and its default branch
// where not; its servers are named by the branch and carry no overlay.
func TestABranchPreviewServesItsBranchWhereEachRepositoryHasIt(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.previews.PublicURL, w.previews.ServerInject = "https://dude.example.com", true
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	mustExec(t, w.owner, `INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch)
		VALUES ($1, $2, $3, 'web', 'git://127.0.0.1/acme/web.git', 'main')`, "repo_web_"+w.org, w.org, w.project)
	w.gh.Branch("feature/login", "main", "login page")
	runID := w.startBranch("feature/login")

	hosts := w.lux.TenantServers()
	want := servers.PreviewHostname(previewDomain, "web", servers.PreviewOf{Branch: "feature/login", ProjectID: w.project,
		ProjectSlug: w.str(`SELECT slug FROM projects WHERE id = $1`, w.project)}, "")
	if _, ok := hosts[want]; !ok || len(hosts) != 1 {
		t.Fatalf("lux has %v, want %s", hosts, want)
	}
	sv, _ := w.lux.TenantServer(w.serverID(runID, "web"))
	if sv.Inject != "" || sv.Labels["dude.task"] != "" || sv.Labels["dude.project"] != w.project {
		t.Errorf("server inject %q labels %v", sv.Inject, sv.Labels)
	}
	// lux's own expiry is past the 30-day reap, so dude's reaper ends it.
	var expiry struct {
		ExpireAfter any `json:"expireAfter"`
	}
	_ = json.Unmarshal(sv.Raw, &expiry)
	if got := fmt.Sprint(expiry.ExpireAfter); got != "1080h0m0s" {
		t.Errorf("the server expires after %v", expiry.ExpireAfter)
	}

	w.open(w.serverID(runID, "web"))
	runs := w.luxRuns()
	if len(runs) != 1 {
		t.Fatalf("%d lux runs", len(runs))
	}
	var spec lux.Spec
	if err := json.Unmarshal(runs[0].Spec, &spec); err != nil || spec.Git == nil {
		t.Fatalf("spec: %v", err)
	}
	refs := map[string]string{}
	for _, rp := range spec.Git.Repositories {
		refs[rp.Name] = rp.Ref
	}
	if refs["target"] != "feature/login" || refs["web"] != "main" {
		t.Errorf("checked out %v, want target at feature/login and web at main", refs)
	}
	if spec.Labels["dude.task"] != "" {
		t.Errorf("the lux run names a task: %v", spec.Labels)
	}

	// The branch reaches web too: the next sync takes it there.
	w.web.Branch("feature/login", "main", "web side")
	if err := w.previews.BranchEvent(context.Background(), w.org, "acme/web", "feature/login", false); err != nil {
		t.Fatal(err)
	}
	w.until("the running preview to sync", func() bool { return len(runs[0].Syncs) == 1 })
	if want := []lux.SyncRef{{Repo: "target", Ref: "feature/login"}, {Repo: "web", Ref: "feature/login"}}; !slices.Equal(runs[0].Syncs[0], want) {
		t.Errorf("synced %v, want %v", runs[0].Syncs[0], want)
	}
}

// A branch the primary repository does not have is refused; a second start
// of a live branch returns the first.
func TestStartingABranchPreviewChecksTheBranchAndIsIdempotent(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	ctx := context.Background()
	gh, _ := w.previews.Forges.For(ctx, w.org)
	if _, err := w.previews.StartBranchPreview(ctx, gh, w.org, w.project, "nope", ""); refusedCode(err) != "branch_not_found" {
		t.Fatalf("a missing branch: %v", err)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE preview_branch IS NOT NULL AND organization_id = $1`, w.org); n != 0 {
		t.Fatalf("%d previews made of a missing branch", n)
	}
	w.gh.Branch("feature/x", "main", "x")
	first, err := w.previews.StartBranchPreview(ctx, gh, w.org, w.project, "feature/x", "")
	if err != nil || !first.Created {
		t.Fatalf("first start: %+v %v", first, err)
	}
	second, err := w.previews.StartBranchPreview(ctx, gh, w.org, w.project, "feature/x", "")
	if err != nil || second.ID != first.ID || second.Created {
		t.Fatalf("second start %+v (%v), first %s", second, err, first.ID)
	}
	if _, err := w.previews.StartBranchPreview(ctx, gh, w.org, w.project, "bad..name", ""); refusedCode(err) != "bad_request" {
		t.Errorf("a bad branch name: %v", err)
	}
}

// A push to the branch syncs only that project's preview of that branch;
// its deletion (a push with deleted, or a delete delivery) ends it.
func TestBranchEventsSyncAndEndOnlyTheirPreview(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	ctx := context.Background()
	w.gh.Branch("feature/a", "main", "a")
	w.gh.Branch("feature/b", "main", "b")
	a, b := w.startBranch("feature/a"), w.startBranch("feature/b")

	if err := w.previews.BranchEvent(ctx, w.org, "acme/target", "feature/a", false); err != nil {
		t.Fatal(err)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND sync_wanted_at IS NOT NULL`, a); n != 1 {
		t.Errorf("a push to feature/a set no sync on its preview")
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND sync_wanted_at IS NOT NULL`, b); n != 0 {
		t.Errorf("a push to feature/a asked feature/b's preview to sync")
	}
	// Another repository's branch of the same name: not this project's.
	if err := w.previews.BranchEvent(ctx, w.org, "acme/elsewhere", "feature/b", true); err != nil {
		t.Fatal(err)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused'`, b); n != 1 {
		t.Errorf("another repository's deletion ended feature/b's preview")
	}
	web := w.serverID(b, "web")
	if err := w.previews.BranchEvent(ctx, w.org, "acme/target", "feature/b", true); err != nil {
		t.Fatal(err)
	}
	w.until("feature/b's preview to end", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'completed' AND lux_stop_reason = 'cancel'`, b) == 1
	})
	if !slices.Contains(w.lux.DeletedServers, web) {
		t.Errorf("its server was not deleted: %v", w.lux.DeletedServers)
	}
	if reason := w.str(`SELECT payload->>'reason' FROM events WHERE run_id = $1 AND event_type = 'run.completed'`, b); reason != "branch deleted" {
		t.Errorf("ended for %q", reason)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused'`, a); n != 1 {
		t.Errorf("feature/a's preview ended with feature/b's")
	}
}

// GitHub's deliveries reach branch previews: a push to the branch syncs a
// running one; a push elsewhere, or a tag's, does nothing; a push that
// deletes the branch, or a delete of it, ends it.
func TestPushAndDeleteDeliveriesReachBranchPreviews(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	w.gh.Branch("feature/p", "main", "p")
	w.gh.Branch("feature/q", "main", "q")
	p, q := w.startBranch("feature/p"), w.startBranch("feature/q")
	w.open(w.serverID(p, "web"))
	run := w.luxRuns()[0]
	w.until("running", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running'`, p) == 1
	})
	repo := map[string]any{"full_name": "acme/target"}
	w.webhook("push", map[string]any{"ref": "refs/heads/main", "repository": repo})
	w.webhook("push", map[string]any{"ref": "refs/tags/feature/p", "repository": repo})
	w.webhook("delete", map[string]any{"ref": "feature/p", "ref_type": "tag", "repository": repo})
	w.webhook("push", map[string]any{"ref": "refs/heads/feature/p", "repository": map[string]any{"full_name": "acme/other"}})
	if n := w.count(`SELECT count(*) FROM runs WHERE id = ANY($1) AND sync_wanted_at IS NOT NULL`, []string{p, q}); n != 0 {
		t.Fatalf("pushes elsewhere asked %d previews to sync", n)
	}
	w.gh.CommitOnTop("feature/p", "more")
	w.webhook("push", map[string]any{"ref": "refs/heads/feature/p", "repository": repo})
	w.until("the running preview to sync", func() bool { return len(run.Syncs) == 1 })
	if n := w.count(`SELECT count(*) FROM webhook_deliveries WHERE organization_id = $1 AND processed_at IS NULL`, w.org); n != 0 {
		t.Errorf("%d deliveries left unprocessed", n)
	}

	w.webhook("push", map[string]any{"ref": "refs/heads/feature/p", "deleted": true, "repository": repo})
	w.webhook("delete", map[string]any{"ref": "feature/q", "ref_type": "branch", "repository": repo})
	w.until("both previews to end in lux", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = ANY($1) AND status = 'completed' AND lux_stop_reason = 'cancel'`,
			[]string{p, q}) == 2
	})
	if !run.Cancelled {
		t.Error("the running preview's lux run was not cancelled")
	}
}

// The internal API's round trip: start (idempotent), list, show, wake by
// starting a server with no browser, stop; refusals for a missing branch.
func TestTheBranchPreviewAPIRoundTrip(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	w.gh.Branch("feature/api", "main", "api")
	base := "/internal/projects/" + w.project + "/branch-previews"
	if code, out := w.do("POST", base, map[string]any{"branch": "missing"}); code != 422 || out["error"].(map[string]any)["code"] != "branch_not_found" {
		t.Fatalf("a missing branch: %d %v", code, out)
	}
	code, out := w.do("POST", base, map[string]any{"branch": "feature/api"})
	if code != 201 || out["branch"] != "feature/api" {
		t.Fatalf("start: %d %v", code, out)
	}
	id := out["id"].(string)
	if code, again := w.do("POST", base, map[string]any{"branch": "feature/api"}); code != 200 || again["id"] != id {
		t.Fatalf("second start: %d %v", code, again)
	}
	w.until("asleep, its server made", func() bool {
		return w.count(`SELECT count(*) FROM runs r JOIN preview_servers s ON s.run_id = r.id WHERE r.id = $1 AND r.status = 'paused'`, id) == 1
	})
	code, list := w.do("GET", base, nil)
	previews, _ := list["previews"].([]any)
	if code != 200 || len(previews) != 1 {
		t.Fatalf("list: %d %v", code, list)
	}
	first := previews[0].(map[string]any)
	svs, _ := first["servers"].([]any)
	if first["id"] != id || len(svs) != 1 || svs[0].(map[string]any)["url"] == nil {
		t.Errorf("listed %v", first)
	}

	// Woken without a browser, as `dude preview` users do.
	if code, out := w.do("POST", "/internal/runs/"+id+"/servers/web/start", nil); code != 200 {
		t.Fatalf("start web: %d %v", code, out)
	}
	w.until("woken", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running'`, id) == 1
	})
	if code, got := w.do("GET", "/internal/runs/"+id+"/servers", nil); code != 200 || got["run"] == nil {
		t.Errorf("its servers: %d %v", code, got)
	}

	code, stopped := w.do("DELETE", "/internal/branch-previews/"+id, nil)
	if code != 200 || stopped["status"] != "completed" {
		t.Fatalf("stop: %d %v", code, stopped)
	}
	if code, shown := w.do("GET", "/internal/branch-previews/"+id, nil); code != 200 || shown["status"] != "completed" || shown["endedAt"] == nil {
		t.Errorf("show after stop: %d %v", code, shown)
	}
	if _, list := w.do("GET", base, nil); len(list["previews"].([]any)) != 0 {
		t.Errorf("a stopped preview is listed: %v", list)
	}
	if code, _ := w.do("GET", "/internal/branch-previews/run_nope", nil); code != 404 {
		t.Errorf("an unknown id: %d", code)
	}
}

// The daily check ends a preview only when GitHub says the branch is gone;
// a failure to ask keeps it.
func TestTheDailyCheckEndsOnlyOnAMissingBranch(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	ctx := context.Background()
	w.gh.Branch("feature/c", "main", "c")
	runID := w.startBranch("feature/c")
	mustExec(t, w.owner, `UPDATE runs SET preview_branch_checked_at = now() - interval '25 hours' WHERE id = $1`, runID)
	if err := w.gh.DeleteBranch("feature/c"); err != nil {
		t.Fatal(err)
	}
	w.gh.RefStatus = map[string]int{"feature/c": 502}
	if n, err := w.previews.CheckBranches(ctx); err != nil || n != 0 {
		t.Fatalf("a forge error: ended %d, %v", n, err)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused'
		AND preview_branch_checked_at < now() - interval '22 hours'`, runID); n != 1 {
		t.Fatalf("a forge error ended the preview or put off its next check a day:\n%s", w.describeRuns())
	}
	w.gh.RefStatus = nil
	mustExec(t, w.owner, `UPDATE runs SET preview_branch_checked_at = now() - interval '25 hours' WHERE id = $1`, runID)
	if n, err := w.previews.CheckBranches(ctx); err != nil || n != 1 {
		t.Fatalf("a missing branch: ended %d, %v", n, err)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'completed'`, runID); n != 1 {
		t.Error("the preview of a missing branch was kept")
	}
}

// A branch preview goes unwoken 30 days before it is reaped; a task's 7.
func TestABranchPreviewIsReapedAfterThirtyDays(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	w.gh.Branch("feature/r", "main", "r")
	w.gh.Branch("feature/s", "main", "s")
	young, old := w.startBranch("feature/r"), w.startBranch("feature/s")
	_, task := w.declare()
	mustExec(t, w.owner, `UPDATE runs SET created_at = now() - interval '8 days' WHERE id = ANY($1)`, []string{young, task})
	mustExec(t, w.owner, `UPDATE runs SET created_at = now() - interval '30 days 1 hour' WHERE id = $1`, old)
	w.until("the task preview and the old branch preview to be reaped", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = ANY($1) AND status = 'completed'`, []string{task, old}) == 2
	})
	for range 3 {
		w.pump()
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused'`, young); n != 1 {
		t.Errorf("an 8-day-old branch preview was reaped:\n%s", w.describeRuns())
	}
}
