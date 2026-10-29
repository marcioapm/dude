package orchestrator_test

// Branch previews (a project's branch, no task) through the preview loop,
// the feed and the fake lux and GitHub.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/forge"
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
// where not; its servers are named by the branch and carry the overlay.
func TestABranchPreviewServesItsBranchWhereEachRepositoryHasIt(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.commenting(true, "https://dude.example.com")
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
	if sv.Inject != servers.OverlaySnippet(w.previews.PublicURL) || sv.Labels["dude.task"] != "" || sv.Labels["dude.project"] != w.project {
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
	beforePrimary, beforeSecondary := len(w.gh.Requests()), len(w.web.Requests())
	if err := w.previews.BranchEvent(context.Background(), w.org, "acme/web", "feature/login", false, time.Now()); err != nil {
		t.Fatal(err)
	}
	w.until("the running preview to sync", func() bool { return len(runs[0].Syncs) == 1 })
	if want := []lux.SyncRef{{Repo: "target", Ref: "feature/login"}, {Repo: "web", Ref: "feature/login"}}; !slices.Equal(runs[0].Syncs[0], want) {
		t.Errorf("synced %v, want %v", runs[0].Syncs[0], want)
	}
	for name, calls := range map[string][]string{"primary": w.gh.Requests()[beforePrimary:], "secondary": w.web.Requests()[beforeSecondary:]} {
		n := 0
		for _, call := range calls {
			if strings.Contains(call, "/git/ref/heads/") {
				n++
			}
		}
		if n != 1 {
			t.Errorf("%s sync made %d branch reads, want 1: %v", name, n, calls)
		}
	}
	if err := w.web.DeleteBranch("feature/login"); err != nil {
		t.Fatal(err)
	}
	if err := w.previews.BranchEvent(context.Background(), w.org, "acme/web", "feature/login", true, time.Now()); err != nil {
		t.Fatal(err)
	}
	w.until("secondary deletion to sync its default", func() bool { return len(runs[0].Syncs) == 2 })
	if want := []lux.SyncRef{{Repo: "target", Ref: "feature/login"}, {Repo: "web", Ref: "main"}}; !slices.Equal(runs[0].Syncs[1], want) {
		t.Errorf("secondary deletion synced %v, want %v", runs[0].Syncs[1], want)
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

func TestConcurrentBranchPreviewStartsConverge(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	w.gh.Branch("feature/race", "main", "race")
	arrived, release := make(chan struct{}, 2), make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/git/ref/heads/") {
			arrived <- struct{}{}
			<-release
		}
		w.gh.Handler().ServeHTTP(rw, r)
	}))
	defer srv.Close()
	gh := forge.NewGitHub(forge.Credential{Auth: "pat", Secret: "example", APIBaseURL: srv.URL})
	var results [2]servers.BranchPreview
	var errs [2]error
	var wg sync.WaitGroup
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = w.previews.StartBranchPreview(context.Background(), gh, w.org, w.project, "feature/race", "")
		}()
	}
	<-arrived
	<-arrived
	close(release)
	wg.Wait()
	if errs[0] != nil || errs[1] != nil || results[0].ID != results[1].ID || results[0].Created == results[1].Created {
		t.Fatalf("concurrent starts: %+v errors %v", results, errs)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE project_id = $1 AND preview_branch = 'feature/race'`, w.project); n != 1 {
		t.Fatalf("created %d runs", n)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.created'`, results[0].ID); n != 1 {
		t.Fatalf("created %d run.created events", n)
	}
}

func TestDeletedPrimaryBranchNeverWakesDefaultCode(t *testing.T) {
	for _, previouslyWoken := range []bool{false, true} {
		t.Run(fmt.Sprint(previouslyWoken), func(t *testing.T) {
			w := newWorld(t)
			w.previews.RetryBackOff = time.Hour
			w.wakeable()
			w.recipe("web", 3000, "npm run dev", "", nil, true)
			w.gh.Branch("feature/gone", "main", "gone")
			id := w.startBranch("feature/gone")
			if previouslyWoken {
				w.open(w.serverID(id, "web"))
				w.until("running", func() bool { return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running'`, id) == 1 })
				w.lux.Idle(w.serverID(id, "web"))
				w.until("sleeping", func() bool { return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused'`, id) == 1 })
			}
			if err := w.gh.DeleteBranch("feature/gone"); err != nil {
				t.Fatal(err)
			}
			w.lux.RequestServer(w.serverID(id, "web"), "/")
			w.until("ended safely", func() bool {
				return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'completed' AND lux_stop_reason = 'cancel'`, id) == 1
			})
			want := 0
			if previouslyWoken {
				want = 1
			}
			if got := len(w.luxRuns()); got != want {
				t.Fatalf("submitted %d lux runs, want %d", got, want)
			}
		})
	}
}

func TestUnavailablePrimaryBranchRetriesWithoutDefaultCode(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		refStatus, repoStatus int
	}{
		{"ref unavailable", 502, 200},
		{"repository unavailable", 404, 502},
		{"repository hidden", 404, 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t)
			w.wakeable()
			w.recipe("web", 3000, "npm run dev", "", nil, true)
			w.gh.Branch("feature/retry", "main", "retry")
			id := w.startBranch("feature/retry")
			handler := w.gh.Handler()
			srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "/git/ref/heads/") {
					rw.WriteHeader(tc.refStatus)
					fmt.Fprint(rw, `{"message":"ref unavailable"}`)
				} else if r.URL.Path == "/repos/acme/target" {
					rw.WriteHeader(tc.repoStatus)
					fmt.Fprint(rw, `{"message":"repository unavailable"}`)
				} else {
					handler.ServeHTTP(rw, r)
				}
			}))
			defer srv.Close()
			var originalURL string
			if err := w.owner.QueryRow(context.Background(), `SELECT api_base_url FROM forge_credentials WHERE organization_id = $1`, w.org).Scan(&originalURL); err != nil {
				t.Fatal(err)
			}
			mustExec(t, w.owner, `UPDATE forge_credentials SET api_base_url = $1 WHERE organization_id = $2`, srv.URL, w.org)
			w.lux.RequestServer(w.serverID(id, "web"), "/")
			w.until("retry scheduled", func() bool {
				return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND next_attempt_at IS NOT NULL`, id) == 1
			})
			if len(w.luxRuns()) != 0 || w.count(`SELECT count(*) FROM runs WHERE id = $1 AND ended_at IS NULL AND next_attempt_at IS NOT NULL`, id) != 1 {
				t.Fatalf("unavailable forge did not preserve a retryable preview: %s", w.describeRuns())
			}
			mustExec(t, w.owner, `UPDATE forge_credentials SET api_base_url = $1 WHERE organization_id = $2`, originalURL, w.org)
			mustExec(t, w.owner, `UPDATE runs SET next_attempt_at = NULL WHERE id = $1`, id)
			w.until("retry running", func() bool { return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running'`, id) == 1 })
		})
	}
}

func TestDeletionReceiptsCannotEndRecreatedPreviews(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	w.gh.Branch("feature/recreated", "main", "old")
	old := w.startBranch("feature/recreated")
	cutoff := time.Now()
	if err := w.gh.DeleteBranch("feature/recreated"); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := w.previews.BranchEvent(ctx, w.org, "acme/target", "feature/recreated", true, cutoff); err != nil {
		t.Fatal(err)
	}
	w.until("old preview ended", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'completed'`, old) == 1
	})
	w.gh.Branch("feature/recreated", "main", "new")
	fresh := w.startBranch("feature/recreated")
	// A previously received deletion cannot touch a newer preview, even if
	// the forge is down. A later-received duplicate must verify absence.
	w.gh.RefStatus = map[string]int{"feature/recreated": 502}
	if err := w.previews.BranchEvent(ctx, w.org, "acme/target", "feature/recreated", true, cutoff); err != nil {
		t.Fatal(err)
	}
	if err := w.previews.BranchEvent(ctx, w.org, "acme/target", "feature/recreated", true, time.Now()); err == nil {
		t.Fatal("uncertain deletion must retry")
	}
	w.gh.RefStatus = nil
	for _, event := range []string{"push", "delete", "push"} {
		payload := map[string]any{"ref": "feature/recreated", "ref_type": "branch", "deleted": true, "repository": map[string]any{"full_name": "acme/target"}}
		if event == "push" {
			payload["ref"] = "refs/heads/feature/recreated"
		}
		w.webhook(event, payload)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused'`, fresh); n != 1 {
		t.Fatalf("duplicate/out-of-order deletion ended recreated preview: %s", w.describeRuns())
	}
	// The cutoff, independently of forge visibility, fences a new preview
	// whose branch is later removed again.
	if err := w.gh.DeleteBranch("feature/recreated"); err != nil {
		t.Fatal(err)
	}
	if err := w.previews.BranchEvent(ctx, w.org, "acme/target", "feature/recreated", true, cutoff); err != nil {
		t.Fatal(err)
	}
	// Dispatch must use the persisted receipt, not processing time: the
	// branch is now absent, but this old delivery predates the fresh run.
	payload, _ := json.Marshal(map[string]any{"ref": "feature/recreated", "ref_type": "branch", "repository": map[string]any{"full_name": "acme/target"}})
	mustExec(t, w.owner, `INSERT INTO webhook_deliveries (id, organization_id, event, payload, received_at)
		VALUES ($1, $2, 'delete', $3, $4)`, "wh_old_"+w.org, w.org, payload, cutoff)
	if _, err := w.prs.ProcessDeliveries(ctx); err != nil {
		t.Fatal(err)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused'`, fresh); n != 1 {
		t.Fatal("old receipt ended new incarnation")
	}
}

func TestDailyBranchChecksDrainHealthyPages(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	// Same healthy branch across independent projects: no need for 51 git
	// branches or wake sweeps to exercise the global scheduling contract.
	for i := range 51 {
		project := fmt.Sprintf("prj_page_%d", i)
		mustExec(t, w.owner, `INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ($1, $2, $1, $1, $3)`, project, w.org, fmt.Sprintf("P%d", i))
		mustExec(t, w.owner, `INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch) VALUES ($1, $2, $3, 'target', 'https://github.com/acme/target.git', 'main')`, "repo_"+project, w.org, project)
		mustExec(t, w.owner, `INSERT INTO runs (id, organization_id, project_id, preview_branch, attempt, kind, status, wakeable) VALUES ($1, $2, $3, 'main', 1, 'preview', 'paused', true)`, "run_"+project, w.org, project)
	}
	for pass, want := range []int{50, 1, 0} {
		if n, err := w.previews.CheckBranches(context.Background()); err != nil || n != want {
			t.Fatalf("pass %d: processed %d err=%v, want %d", pass, n, err, want)
		}
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE organization_id = $1 AND preview_branch = 'main' AND preview_branch_checked_at > now() - interval '1 hour' AND status = 'paused'`, w.org); n != 51 {
		t.Fatalf("checked %d healthy previews, want 51", n)
	}
}

func TestDailyBranchCheckCannotEndANewIncarnation(t *testing.T) {
	w := newWorld(t)
	w.wakeable()
	w.recipe("web", 3000, "npm run dev", "", nil, true)
	w.gh.Branch("feature/incarnation", "main", "old")
	old := w.startBranch("feature/incarnation")
	ctx := context.Background()
	gh, err := w.previews.Forges.For(ctx, w.org)
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/git/ref/") {
			close(entered)
			<-release
			rw.WriteHeader(404)
			fmt.Fprint(rw, `{"message":"Not Found"}`)
		} else {
			fmt.Fprint(rw, `{"id":1}`)
		}
	}))
	defer srv.Close()
	mustExec(t, w.owner, `UPDATE forge_credentials SET api_base_url = $1 WHERE organization_id = $2`, srv.URL, w.org)
	mustExec(t, w.owner, `UPDATE runs SET preview_branch_checked_at = NULL WHERE id = $1`, old)
	done := make(chan error, 1)
	go func() { _, err := w.previews.CheckBranches(ctx); done <- err }()
	<-entered
	if _, err := w.previews.StopBranchPreview(ctx, w.org, old, ""); err != nil {
		close(release)
		t.Fatal(err)
	}
	fresh, err := w.previews.StartBranchPreview(ctx, gh, w.org, w.project, "feature/incarnation", "")
	close(release)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if fresh.ID == old || w.count(`SELECT count(*) FROM runs WHERE id = $1 AND ended_at IS NULL`, fresh.ID) != 1 {
		t.Fatal("stale daily absence ended the replacement preview")
	}
}

func TestBranchEventsFilterProjectsAndPreserveCloneURLVariants(t *testing.T) {
	w := newWorld(t)
	for i, url := range []string{"https://github.com/Acme/Target.git", "git@github.com:acme/target.git", "ssh://git@github.com/acme/target.git", "git://127.0.0.1/acme/other.git"} {
		project := fmt.Sprintf("prj_filter_%d", i)
		mustExec(t, w.owner, `INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ($1, $2, $1, $1, $3)`, project, w.org, fmt.Sprintf("F%d", i))
		mustExec(t, w.owner, `INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch) VALUES ($1, $2, $3, 'target', $4, 'main')`, "repo_"+project, w.org, project, url)
		mustExec(t, w.owner, `INSERT INTO runs (id, organization_id, project_id, preview_branch, attempt, kind, status, wakeable) VALUES ($1, $2, $3, 'feature/filter', 1, 'preview', 'paused', true)`, "run_"+project, w.org, project)
	}
	if err := w.previews.BranchEvent(context.Background(), w.org, "acme/target", "feature/filter", false, time.Now()); err != nil {
		t.Fatal(err)
	}
	for i := range 4 {
		want := 1
		if i == 3 {
			want = 0
		}
		if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND sync_wanted_at IS NOT NULL`, fmt.Sprintf("run_prj_filter_%d", i)); n != want {
			t.Errorf("project %d sync=%d want %d", i, n, want)
		}
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

	if err := w.previews.BranchEvent(ctx, w.org, "acme/target", "feature/a", false, time.Now()); err != nil {
		t.Fatal(err)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND sync_wanted_at IS NOT NULL`, a); n != 1 {
		t.Errorf("a push to feature/a set no sync on its preview")
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND sync_wanted_at IS NOT NULL`, b); n != 0 {
		t.Errorf("a push to feature/a asked feature/b's preview to sync")
	}
	// Another repository's branch of the same name: not this project's.
	if err := w.previews.BranchEvent(ctx, w.org, "acme/elsewhere", "feature/b", true, time.Now()); err != nil {
		t.Fatal(err)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused'`, b); n != 1 {
		t.Errorf("another repository's deletion ended feature/b's preview")
	}
	web := w.serverID(b, "web")
	if err := w.gh.DeleteBranch("feature/b"); err != nil {
		t.Fatal(err)
	}
	if err := w.previews.BranchEvent(ctx, w.org, "acme/target", "feature/b", true, time.Now()); err != nil {
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

	for _, branch := range []string{"feature/p", "feature/q"} {
		if err := w.gh.DeleteBranch(branch); err != nil {
			t.Fatal(err)
		}
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
	if n, err := w.previews.CheckBranches(ctx); err != nil || n != 1 {
		t.Fatalf("a forge error: processed %d, %v", n, err)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused'
		AND preview_branch_checked_at < now() - interval '22 hours'`, runID); n != 1 {
		t.Fatalf("a forge error ended the preview or put off its next check a day:\n%s", w.describeRuns())
	}
	w.gh.RefStatus = nil
	mustExec(t, w.owner, `UPDATE runs SET preview_branch_checked_at = now() - interval '25 hours' WHERE id = $1`, runID)
	if n, err := w.previews.CheckBranches(ctx); err != nil || n != 1 {
		t.Fatalf("a missing branch: processed %d, %v", n, err)
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
