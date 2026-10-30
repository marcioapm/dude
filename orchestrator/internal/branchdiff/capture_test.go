package branchdiff_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/branchdiff"
	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/prs"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

type oneForge struct{ gh *forge.GitHub }

func (f oneForge) For(context.Context, string) (*forge.GitHub, error) { return f.gh, nil }

// world is task wi_b (attempt 1) of Acme's api repository on a fake GitHub
// backed by a real git repository: main, and the task branch with one
// commit of the writer's.
type world struct {
	t      *testing.T
	app    *db.DB
	owner  *pgx.Conn
	org    string
	fake   *fakegithub.Server
	origin string
	cap    *branchdiff.Capturer
	branch string
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Sam", "GIT_AUTHOR_EMAIL=sam@example.com",
		"GIT_COMMITTER_NAME=Sam", "GIT_COMMITTER_EMAIL=sam@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func newWorld(t *testing.T) *world {
	t.Helper()
	app, owner := dbtest.Open(t)
	w := &world{t: t, app: app, owner: owner, org: dbtest.Org(t, owner), branch: "dude/wi_b/attempt-1"}
	dir := t.TempDir()
	w.origin = filepath.Join(dir, "origin.git")
	git(t, dir, "init", "-q", "--bare", "-b", "main", w.origin)
	work := filepath.Join(dir, "work")
	git(t, dir, "init", "-q", "-b", "main", work)
	if err := os.WriteFile(filepath.Join(work, "app.ts"), []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, work, "add", ".")
	git(t, work, "commit", "-qm", "seed")
	git(t, work, "push", "-q", w.origin, "main", "main:"+w.branch)
	w.fake = fakegithub.New(w.origin, "acme/api")
	// GitHub's patches, as git shows them (the fake's own are placeholders).
	w.fake.Set(func(s *fakegithub.Server) {
		s.CompareAnswer = func(base, head string, answer map[string]any) {
			for _, f := range answer["files"].([]any) {
				m := f.(map[string]any)
				if _, ok := m["patch"]; !ok {
					continue
				}
				out, err := exec.Command("git", "-C", w.origin, "diff", base+"..."+head, "--", m["filename"].(string)).Output()
				if err != nil {
					continue
				}
				if i := strings.Index(string(out), "\n@@"); i >= 0 {
					m["patch"] = strings.TrimSuffix(string(out[i+1:]), "\n")
				}
			}
		}
	})
	srv := httptest.NewServer(w.fake.Handler())
	t.Cleanup(srv.Close)
	gh := forge.NewGitHub(forge.Credential{Auth: "pat", Secret: "ghp_x", APIBaseURL: srv.URL})
	w.cap = &branchdiff.Capturer{DB: app, Forges: oneForge{gh}}
	w.exec(`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_b', $1, 'Acme', 'acme', 'ACME')`)
	w.exec(`INSERT INTO tasks (id, organization_id, project_id, number, title, goal) VALUES ('wi_b', $1, 'prj_b', 1, 'T', 'G')`)
	w.exec(`INSERT INTO repositories (id, organization_id, project_id, name, url) VALUES ('repo_api', $1, 'prj_b', 'api', 'https://github.com/acme/api')`)
	w.exec(`INSERT INTO task_repositories (task_id, repository_id, organization_id, access) VALUES ('wi_b', 'repo_api', $1, 'write')`)
	return w
}

func (w *world) exec(sql string, args ...any) {
	w.t.Helper()
	if _, err := w.owner.Exec(context.Background(), sql, append([]any{w.org}, args...)...); err != nil {
		w.t.Fatalf("%s: %v", sql, err)
	}
}

// capture asks for a capture, as a trigger does, and runs the worker.
func (w *world) capture() {
	w.t.Helper()
	w.exec(`SELECT want_branch_capture($1, 'wi_b', 1, 'repo_api', '')`)
	if _, err := w.cap.Sweep(context.Background()); err != nil {
		w.t.Fatal(err)
	}
}

type snap struct {
	ID, Relation, Previous, Head, MergeBase, Target, Status string
	Files                                                   []struct {
		Path      string `json:"path"`
		Truncated bool   `json:"truncated"`
		Binary    bool   `json:"binary"`
		Hunks     []any  `json:"hunks"`
	}
	BinaryFiles int
}

func (w *world) snapshots() []snap {
	w.t.Helper()
	rows, err := w.owner.Query(context.Background(), `SELECT id, relation, COALESCE(previous_snapshot_id, ''), head_sha, merge_base_sha,
		target_sha, status, files, binary_files FROM task_branch_snapshots WHERE task_id = 'wi_b' ORDER BY captured_at, id`)
	if err != nil {
		w.t.Fatal(err)
	}
	var out []snap
	for rows.Next() {
		var s snap
		var files []byte
		if err := rows.Scan(&s.ID, &s.Relation, &s.Previous, &s.Head, &s.MergeBase, &s.Target, &s.Status, &files, &s.BinaryFiles); err != nil {
			w.t.Fatal(err)
		}
		if err := json.Unmarshal(files, &s.Files); err != nil {
			w.t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func (w *world) request() (status, reason string) {
	w.t.Helper()
	if err := w.owner.QueryRow(context.Background(), `SELECT status, COALESCE(reason, '') FROM task_branch_capture_requests
		WHERE task_id = 'wi_b'`).Scan(&status, &reason); err != nil {
		w.t.Fatal(err)
	}
	return status, reason
}

// The lineage through the four relations, read from real git history: the
// writer's first commit (initial), another (fast-forward), main merged in
// (merged-target: the base moves to main's head), a rebase onto main
// (rewritten). A move of main alone makes none; a head read again is the
// snapshot it has.
func TestALineageOfSnapshotsFollowsTheBranch(t *testing.T) {
	w := newWorld(t)
	w.fake.CommitFiles(w.branch, "first", map[string]string{"app.ts": "one\n2\nthree\n"})
	w.capture()
	got := w.snapshots()
	if len(got) != 1 || got[0].Relation != branchdiff.Initial || got[0].Previous != "" || got[0].Head != w.fake.SHA(w.branch) ||
		got[0].MergeBase != w.fake.SHA("main") || got[0].Target != w.fake.SHA("main") {
		t.Fatalf("first capture: %+v", got)
	}
	if len(got[0].Files) != 1 || got[0].Files[0].Path != "app.ts" || len(got[0].Files[0].Hunks) != 1 {
		t.Fatalf("its files: %+v", got[0].Files)
	}
	if s, _ := w.request(); s != "done" {
		t.Errorf("the request after a capture: %s", s)
	}

	// Idempotent: read again with nothing moved, and main alone moving, make none.
	compares := func() int {
		n := 0
		for _, r := range w.fake.Requests() {
			if strings.Contains(r, "/compare/") {
				n++
			}
		}
		return n
	}
	before := compares()
	w.capture()
	base0 := w.fake.SHA("main")
	w.fake.CommitFiles("main", "elsewhere", map[string]string{"other.ts": "x\n"})
	w.capture()
	if n := len(w.snapshots()); n != 1 {
		t.Fatalf("%d snapshots after the target alone moved, want 1", n)
	}
	// The branch head read is enough: nothing compared for a head it has.
	if n := compares() - before; n != 0 {
		t.Errorf("%d compares for an unchanged head", n)
	}

	w.fake.CommitFiles(w.branch, "second", map[string]string{"app.ts": "one\n2\n3\n"})
	w.capture()
	got = w.snapshots()
	if len(got) != 2 || got[1].Relation != branchdiff.FastForward || got[1].Previous != got[0].ID || got[1].MergeBase != base0 {
		t.Fatalf("after new commits: %+v", got)
	}
	// The target is recorded as it is at this capture.
	if got[1].Target != w.fake.SHA("main") {
		t.Errorf("target %s, want main's head %s", got[1].Target, w.fake.SHA("main"))
	}

	w.fake.MergeInto(w.branch, "main", nil)
	w.capture()
	got = w.snapshots()
	if len(got) != 3 || got[2].Relation != branchdiff.MergedTarget || got[2].Previous != got[1].ID || got[2].MergeBase != w.fake.SHA("main") {
		t.Fatalf("after main merged in: %+v", got)
	}
	// Only the branch's own change: main's other.ts is in the base now.
	if len(got[2].Files) != 1 || got[2].Files[0].Path != "app.ts" {
		t.Errorf("the merged branch's files: %+v", got[2].Files)
	}

	// Rebased: a new commit on main's head carrying the change; the old head is no ancestor.
	w.fake.CommitFiles("main", "more", map[string]string{"third.ts": "y\n"})
	git(t, w.origin, "update-ref", "refs/heads/"+w.branch, w.fake.SHA("main"))
	w.fake.CommitFiles(w.branch, "rebased", map[string]string{"app.ts": "one\n2\n3\n"})
	w.capture()
	got = w.snapshots()
	if len(got) != 4 || got[3].Relation != branchdiff.Rewritten || got[3].Previous != got[2].ID {
		t.Fatalf("after a rebase: %+v", got)
	}
	// One event per snapshot, carrying its id and relation; none for a read that made none.
	rows, err := w.owner.Query(context.Background(), `SELECT payload->>'snapshotId', payload->>'relation', payload->>'previousHeadSha'
		FROM events WHERE task_id = 'wi_b' AND event_type = $1 ORDER BY cursor`, branchdiff.EvSnapshotCaptured)
	if err != nil {
		t.Fatal(err)
	}
	var said []string
	for rows.Next() {
		var id, rel, prev string
		if err := rows.Scan(&id, &rel, &prev); err != nil {
			t.Fatal(err)
		}
		said = append(said, id+" "+rel+" "+short(prev))
	}
	want := []string{got[0].ID + " initial ", got[1].ID + " fast-forward " + short(got[0].Head),
		got[2].ID + " merged-target " + short(got[1].Head), got[3].ID + " rewritten " + short(got[2].Head)}
	if strings.Join(said, "\n") != strings.Join(want, "\n") {
		t.Errorf("events %q, want %q", said, want)
	}
	rows.Close()
	// The previous head has vanished from GitHub. The lineage request is
	// cheap, and a 404 still means rewritten (not a fabricated descent).
	oldHead := got[3].Head
	lineageReads := 0
	w.fake.Set(func(s *fakegithub.Server) {
		s.Intercept = func(r *http.Request) int {
			if strings.Contains(r.URL.Path, "/compare/"+oldHead+"...") {
				lineageReads++
				if r.URL.Query().Get("per_page") != "1" {
					t.Error("lineage compare did not request its minimum page")
				}
				return 404
			}
			return 0
		}
	})
	w.fake.CommitFiles(w.branch, "after collection", map[string]string{"app.ts": "collected\n"})
	w.capture()
	got = w.snapshots()
	if lineageReads != 1 || len(got) != 5 || got[4].Relation != branchdiff.Rewritten {
		t.Fatalf("vanished previous head: reads %d snapshots %+v", lineageReads, got)
	}
	// A 5xx is not evidence of a rewrite. Leave the snapshot uncaptured
	// and retry rather than telling the UI that a rebase happened.
	oldHead = got[4].Head
	w.fake.Set(func(s *fakegithub.Server) {
		s.Intercept = func(r *http.Request) int {
			if strings.Contains(r.URL.Path, "/compare/"+oldHead+"...") {
				return 502
			}
			return 0
		}
	})
	w.fake.CommitFiles(w.branch, "after outage", map[string]string{"app.ts": "outage\n"})
	w.capture()
	if len(w.snapshots()) != 5 || w.requestOf("wi_b", 1).failures != 1 {
		t.Fatal("a lineage outage was recorded as a snapshot")
	}
}

func TestTheTargetRefComesFromDeliveryThenPullRequestAndRetargetingCaptures(t *testing.T) {
	for _, source := range []string{"delivery", "pull request"} {
		t.Run(source, func(t *testing.T) {
			w := newWorld(t)
			first := w.fake.CommitFiles(w.branch, "first", map[string]string{"app.ts": "1\n"})
			git(t, w.origin, "update-ref", "refs/heads/release", first)
			w.fake.CommitFiles("release", "release-only", map[string]string{"release.ts": "release\n"})
			w.fake.CommitFiles(w.branch, "second", map[string]string{"app.ts": "2\n"})
			w.capture() // default main
			if source == "delivery" {
				w.exec(`INSERT INTO workflow_runs (id, organization_id, workflow_type, idempotency_key, task_id, status, step, state)
					VALUES ('wf_b', $1, 'task.delivery', 'd', 'wi_b', 'running', 's', '{"bases":{"api":"release"}}')`)
			} else {
				w.exec(`INSERT INTO pull_requests (id, organization_id, project_id, task_id, repository_id, number, url, head_branch, base_branch, head_sha, title)
					VALUES ('pr_b', $1, 'prj_b', 'wi_b', 'repo_api', 1, 'https://github.com/acme/api/pull/1', $2, 'release', $3, 'T')`, w.branch, w.fake.SHA(w.branch))
			}
			w.capture() // same head, different target ref and merge base
			got := w.snapshots()
			var ref string
			if len(got) != 2 || got[1].MergeBase != first || got[1].Relation != branchdiff.MergedTarget {
				t.Fatalf("retargeted unchanged head: %+v", got)
			}
			if err := w.owner.QueryRow(context.Background(), `SELECT target_ref FROM task_branch_snapshots WHERE id = $1`, got[1].ID).Scan(&ref); err != nil || ref != "release" {
				t.Fatalf("target ref %q (%v)", ref, err)
			}
			// Another ref with the same history still names a different target:
			// the uniqueness/conflict path must not silently return release's row.
			git(t, w.origin, "update-ref", "refs/heads/release-next", w.fake.SHA("release"))
			if source == "delivery" {
				w.exec(`UPDATE workflow_runs SET state = '{"bases":{"api":"release-next"}}' WHERE organization_id = $1`)
			} else {
				w.exec(`UPDATE pull_requests SET base_branch = 'release-next' WHERE organization_id = $1`)
			}
			w.capture()
			if n := len(w.snapshots()); n != 3 {
				t.Fatalf("same merge base, different target ref: %d snapshots", n)
			}
		})
	}
}

func TestTheFileCapIsStoredAsPartialWithItsReason(t *testing.T) {
	w := newWorld(t)
	w.fake.CommitFiles(w.branch, "first", map[string]string{"app.ts": "1\n"})
	w.fake.Set(func(s *fakegithub.Server) {
		s.CompareAnswer = func(base, head string, answer map[string]any) {
			files := answer["files"].([]any)
			for len(files) < 300 {
				files = append(files, files[0])
			}
			answer["files"] = files
		}
	})
	w.capture()
	var status, reason string
	var truncated bool
	if err := w.owner.QueryRow(context.Background(), `SELECT status, reason, truncated FROM task_branch_snapshots`).
		Scan(&status, &reason, &truncated); err != nil || status != "partial" || !strings.Contains(reason, "300") || !truncated {
		t.Fatalf("file cap: %s %q truncated=%v (%v)", status, reason, truncated, err)
	}
}

func TestResettingTheBranchReusesItsFirstSnapshotWithoutAnEvent(t *testing.T) {
	w := newWorld(t)
	first := w.fake.CommitFiles(w.branch, "first", map[string]string{"app.ts": "first\n"})
	w.capture()
	w.fake.CommitFiles(w.branch, "second", map[string]string{"app.ts": "second\n"})
	w.capture()
	got := w.snapshots()
	git(t, w.origin, "update-ref", "refs/heads/"+w.branch, first)
	w.capture()
	if len(w.snapshots()) != 2 {
		t.Fatal("reset made a new snapshot")
	}
	var id, status string
	var events int
	if err := w.owner.QueryRow(context.Background(), `SELECT snapshot_id, status FROM task_branch_capture_requests WHERE task_id = 'wi_b'`).Scan(&id, &status); err != nil || id != got[0].ID || status != "done" {
		t.Fatalf("reset request: %s %s (%v)", id, status, err)
	}
	if err := w.owner.QueryRow(context.Background(), `SELECT count(*) FROM events WHERE task_id = 'wi_b' AND event_type = $1`, branchdiff.EvSnapshotCaptured).Scan(&events); err != nil || events != 2 {
		t.Fatalf("reset emitted an event: %d (%v)", events, err)
	}
}

func TestAnInsertConflictReusesTheSnapshotWithoutAnotherEvent(t *testing.T) {
	w := newWorld(t)
	w.fake.CommitFiles(w.branch, "first", map[string]string{"app.ts": "first\n"})
	w.capture()
	w.fake.CommitFiles(w.branch, "second", map[string]string{"app.ts": "second\n"})
	w.capture()
	got := w.snapshots()
	// Point away from this head so the read reaches INSERT, not errSame.
	w.exec(`UPDATE task_branch_capture_requests SET snapshot_id = $2 WHERE organization_id = $1`, got[0].ID)
	w.capture()
	var id, status string
	var events int
	if err := w.owner.QueryRow(context.Background(), `SELECT snapshot_id, status FROM task_branch_capture_requests WHERE task_id = 'wi_b'`).Scan(&id, &status); err != nil || id != got[1].ID || status != "done" {
		t.Fatalf("conflict request: %s %s (%v)", id, status, err)
	}
	if err := w.owner.QueryRow(context.Background(), `SELECT count(*) FROM events WHERE task_id = 'wi_b' AND event_type = $1`, branchdiff.EvSnapshotCaptured).Scan(&events); err != nil || events != 2 || len(w.snapshots()) != 2 {
		t.Fatalf("conflict emitted an event or snapshot: %d (%v)", events, err)
	}
}

func short(s string) string { return s[:min(len(s), 7)] }

// A binary file and a file GitHub gave no lines for are flagged, file by
// file, and counted on the snapshot.
func TestBinaryAndTooLargeFilesAreFlagged(t *testing.T) {
	w := newWorld(t)
	w.fake.CommitFiles(w.branch, "assets", map[string]string{"logo.png": "\x89PNG\x00\x01\x02", "big.lock": "a\nb\n", "app.ts": "1\ntwo\nthree\n"})
	w.fake.Set(func(s *fakegithub.Server) {
		prev := s.CompareAnswer
		s.CompareAnswer = func(base, head string, answer map[string]any) {
			prev(base, head, answer)
			for _, f := range answer["files"].([]any) {
				if m := f.(map[string]any); m["filename"] == "big.lock" {
					delete(m, "patch") // GitHub: too large to show
				}
			}
		}
	})
	w.capture()
	got := w.snapshots()
	if len(got) != 1 {
		t.Fatalf("%+v", got)
	}
	by := map[string]int{}
	for i, f := range got[0].Files {
		by[f.Path] = i
	}
	if f := got[0].Files[by["logo.png"]]; !f.Binary || f.Truncated {
		t.Errorf("the binary file: %+v", f)
	}
	if f := got[0].Files[by["big.lock"]]; !f.Truncated || f.Binary || len(f.Hunks) != 0 {
		t.Errorf("the file shown without lines: %+v", f)
	}
	if f := got[0].Files[by["app.ts"]]; f.Truncated || f.Binary || len(f.Hunks) != 1 {
		t.Errorf("the plain file: %+v", f)
	}
	var truncated bool
	if err := w.owner.QueryRow(context.Background(), `SELECT truncated FROM task_branch_snapshots`).Scan(&truncated); err != nil || !truncated ||
		got[0].BinaryFiles != 1 {
		t.Errorf("the snapshot's flags: truncated %v binary %d (%v)", truncated, got[0].BinaryFiles, err)
	}
}

// No branch yet: absent, no snapshot. A repository GitHub refuses:
// unavailable, saying why.
func TestAbsentAndUnavailableAreSaid(t *testing.T) {
	w := newWorld(t)
	git(t, w.origin, "update-ref", "-d", "refs/heads/"+w.branch)
	w.capture()
	if s, _ := w.request(); s != "absent" || len(w.snapshots()) != 0 {
		t.Errorf("no branch: %s, %d snapshots", s, len(w.snapshots()))
	}
	git(t, w.origin, "update-ref", "refs/heads/"+w.branch, "refs/heads/main")
	w.fake.CommitFiles(w.branch, "first", map[string]string{"app.ts": "1\n"})
	w.fake.Set(func(s *fakegithub.Server) {
		s.Intercept = func(r *http.Request) int {
			if strings.Contains(r.URL.Path, "/compare/") {
				return 403
			}
			return 0
		}
	})
	w.capture()
	if s, reason := w.request(); s != "unavailable" || !strings.Contains(reason, "403") || len(w.snapshots()) != 0 {
		t.Errorf("GitHub refusing: %s %q, %d snapshots", s, reason, len(w.snapshots()))
	}
	// Asked again (the branch moved), and GitHub answers: captured.
	w.fake.Set(func(s *fakegithub.Server) { s.Intercept = nil })
	w.capture()
	if s, _ := w.request(); s != "done" || len(w.snapshots()) != 1 {
		t.Errorf("read again: %s, %d snapshots", s, len(w.snapshots()))
	}
}

// Someone else pushes to the pull request's branch: the pull request sync
// (a webhook's or the reconciler's, prs.Syncer.Sync) records the new head,
// which asks for a capture; the worker captures it, its relation read from
// the history. No new poller: the sync that already reads the head.
func TestAPushBySomeoneElseSeenByThePullRequestSyncIsCaptured(t *testing.T) {
	w := newWorld(t)
	n := w.fake.OpenAsPerson("dude-bot", w.branch, "main", "Fix VAT")
	w.exec(`INSERT INTO pull_requests (id, organization_id, project_id, task_id, repository_id, number, url, head_branch, base_branch, head_sha, title)
		VALUES ('pr_b', $1, 'prj_b', 'wi_b', 'repo_api', $2, 'https://github.com/acme/api/pull/1', $3, 'main', $4, 'Fix VAT')`,
		n, w.branch, w.fake.SHA(w.branch))
	if _, err := w.cap.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := w.snapshots(); len(got) != 1 || got[0].Relation != branchdiff.Initial {
		t.Fatalf("the pull request opened: %+v", got)
	}
	w.fake.CommitFiles(w.branch, "Priya's fix", map[string]string{"app.ts": "one\ntwo\nthree\nfour\n"})
	syncer := &prs.Syncer{DB: w.app, Forges: oneForge{w.cap.Forges.(oneForge).gh},
		Signal: func(context.Context, string, string, string, any, string) error { return nil }}
	if err := syncer.Sync(context.Background(), w.org, "pr_b"); err != nil {
		t.Fatal(err)
	}
	if s, _ := w.request(); s != "pending" {
		t.Fatalf("after the sync read a new head: %s, want a capture asked for", s)
	}
	if _, err := w.cap.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := w.snapshots()
	if len(got) != 2 || got[1].Relation != branchdiff.FastForward || got[1].Head != w.fake.SHA(w.branch) {
		t.Fatalf("after someone else's push: %+v", got)
	}
	var prHead string
	if err := w.owner.QueryRow(context.Background(), `SELECT pr_head_sha FROM task_branch_snapshots WHERE id = $1`, got[1].ID).Scan(&prHead); err != nil ||
		prHead != got[1].Head {
		t.Errorf("the published head at capture: %q (%v)", prHead, err)
	}
}
