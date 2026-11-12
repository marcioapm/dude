package orchestrator_test

// A merge on an epic wakes its peers' conductors (delivery.EpicMerges):
// who is woken, once, with what overlap, and from stored state when the
// event was missed. Every line is compared whole.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

// epicMergeWorld is a task under review on an epic, its pull request #1
// open on acme/target, and the worker that tells its peers.
type epicMergeWorld struct {
	*world
	epic, merging, prID string
	worker              *delivery.EpicMerges
}

func newEpicMergeWorld(t *testing.T) *epicMergeWorld {
	w := &epicMergeWorld{world: newWorld(t)}
	w.merging = w.reviewing()
	w.epic = "epc_merge_" + w.org
	mustExec(t, w.owner, `INSERT INTO epics (id, organization_id, project_id, title) VALUES ($1, $2, $3, 'Billing')`, w.epic, w.org, w.project)
	mustExec(t, w.owner, `UPDATE tasks SET epic_id = $1 WHERE id = $2`, w.epic, w.merging)
	w.prID = w.prOf(w.merging)
	w.worker = &delivery.EpicMerges{DB: w.app, Forges: forge.Resolver{DB: w.app}}
	return w
}

// peerSpec is a peer task: on epic (none: ""), in status, writing
// acme/target unless access says otherwise ("" for no access), with a
// delivery in deliveryStatus ("" for none) whose state is state, a live
// conductor if conductor, and a branch snapshot listing files (nil: none).
type peerSpec struct {
	epic, status, access, deliveryStatus, state string
	conductor                                   bool
	files                                       []string
}

// live is a running peer on the epic with a waiting delivery and a live
// conductor, its branch changing files.
func (w *epicMergeWorld) live(files ...string) peerSpec {
	return peerSpec{epic: w.epic, status: "running", access: "write", deliveryStatus: "waiting", conductor: true, files: files}
}

func (w *epicMergeWorld) peer(p peerSpec) string {
	t := w.t
	id := w.task()
	if p.epic != "" {
		mustExec(t, w.owner, `UPDATE tasks SET epic_id = $1 WHERE id = $2`, p.epic, id)
	}
	mustExec(t, w.owner, `UPDATE tasks SET status = $2::task_status WHERE id = $1`, id, p.status)
	if p.access != "" {
		mustExec(t, w.owner, `INSERT INTO task_repositories(task_id, repository_id, access, organization_id) VALUES($1,$2,$3::repository_access,$4)`,
			id, w.repoID, p.access, w.org)
	}
	if p.deliveryStatus != "" {
		state := p.state
		if state == "" {
			state = "{}"
		}
		mustExec(t, w.owner, `INSERT INTO workflow_runs(id, organization_id, workflow_type, idempotency_key, task_id, status, step, state)
			VALUES($1,$2,$3,$1,$4,$5,'awaitPullRequest',$6::jsonb)`, "wf_"+id, w.org, deliveryType, id, p.deliveryStatus, state)
	}
	if p.conductor {
		mustExec(t, w.owner, `INSERT INTO runs(id, organization_id, project_id, task_id, attempt, status, role, kind)
			VALUES($1,$2,$3,$4,1,'paused','conductor','agent')`, "run_c_"+id, w.org, w.project, id)
	}
	if p.files != nil {
		w.snapshot(id, strings.Repeat("b", 40), p.files, "complete")
	}
	return id
}

const deliveryType = delivery.WorkflowType

// snapshot records task's branch snapshot of acme/target at head, at the
// task's attempt, captured now.
func (w *epicMergeWorld) snapshot(task, head string, files []string, status string) {
	w.snapshotAt(task, w.repoID, 0, head, 0, status, entries(files...))
}

// entries are snapshot file entries, modified, for paths.
func entries(paths ...string) []string {
	out := make([]string, 0, len(paths))
	for _, f := range paths {
		out = append(out, fmt.Sprintf(`{"path": %q, "status": "M", "additions": 1, "deletions": 0}`, f))
	}
	return out
}

// snapshotAt records task's branch snapshot of repo at head with the file
// entries given (JSON), at attempt (0: the task's), captured ago before
// now.
func (w *epicMergeWorld) snapshotAt(task, repo string, attempt int, head string, ago time.Duration, status string, files []string) {
	var reason any
	if status == "partial" {
		reason = "GitHub listed 300 files, its cap"
	}
	mustExec(w.t, w.owner, `INSERT INTO task_branch_snapshots (id, organization_id, task_id, attempt, repository_id, repository,
			branch_ref, target_ref, target_sha, merge_base_sha, head_sha, files, checksum, stats, status, reason, relation, captured_at)
		VALUES ($1, $2, $3, CASE WHEN $10 > 0 THEN $10 ELSE GREATEST(task_attempt($3), 1) END, $4, 'target', 'dude/x', 'main', $5, $5, $6,
			$7::jsonb, 'c', '{}', $8, $9, 'initial', now() - make_interval(secs => $11))`,
		fmt.Sprintf("tbs_%s_%s_%d_%d", task, head[:8], attempt, time.Now().UnixNano()), w.org, task, repo, strings.Repeat("c", 40), head,
		"["+strings.Join(files, ",")+"]", status, reason, attempt, ago.Seconds())
}

// merge merges pull request #1 on GitHub and records it as the sync does.
func (w *epicMergeWorld) merge() {
	w.gh.Merge(1)
	w.until("merge recorded", func() bool {
		if err := w.prs.Sync(context.Background(), w.org, w.prID); err != nil {
			w.t.Fatal(err)
		}
		return w.count(`SELECT count(*) FROM pull_requests WHERE id=$1 AND state='merged'`, w.prID) == 1
	})
}

func (w *epicMergeWorld) sweep() int {
	w.t.Helper()
	n, err := w.worker.Sweep(context.Background())
	if err != nil {
		w.t.Fatal(err)
	}
	return n
}

// wakes are task's epic-merge reasons' lines for pull request #1.
func (w *epicMergeWorld) wakes(task string) []string {
	return w.wakesOf(task, w.prID)
}

func (w *epicMergeWorld) wakesOf(task, prID string) []string {
	rows, err := w.owner.Query(context.Background(), `SELECT line FROM conductor_wakes WHERE task_id = $1 AND kind = 'epic_merge'
		AND key = $2 ORDER BY created_at`, task, delivery.EpicMergeWakeKey(prID))
	if err != nil {
		w.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			w.t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

// head is the merged task's line's start: its key, the pull request, the
// merge commit's first 12 characters.
func (w *epicMergeWorld) head() string {
	var key, sha string
	var number int
	if err := w.owner.QueryRow(context.Background(), `SELECT p.key_prefix || '-' || t.number, pr.number, COALESCE(pr.merge_commit_sha, '')
		FROM pull_requests pr JOIN tasks t ON t.id = pr.task_id JOIN projects p ON p.id = t.project_id WHERE pr.id = $1`, w.prID).Scan(&key, &number, &sha); err != nil {
		w.t.Fatal(err)
	}
	if len(sha) < 12 {
		w.t.Fatalf("no merge commit recorded: %q", sha)
	}
	return fmt.Sprintf("%s on your epic merged target#%d (%s).", key, number, sha[:12])
}

func (w *epicMergeWorld) key() string {
	var key string
	if err := w.owner.QueryRow(context.Background(), `SELECT p.key_prefix || '-' || t.number FROM tasks t JOIN projects p ON p.id = t.project_id
		WHERE t.id = $1`, w.merging).Scan(&key); err != nil {
		w.t.Fatal(err)
	}
	return key
}

// The whole lines a peer may be told.
func (w *epicMergeWorld) shared(files ...string) string {
	return w.head() + " Your branch also changes `" + strings.Join(files, "`, `") +
		"`: bring `main` in (sync_base, or start_phase fix while a pull request is open) before you go on."
}

func (w *epicMergeWorld) none() string { return w.head() + " No overlapping files with your branch." }

func (w *epicMergeWorld) unknown() string {
	return w.head() + " Overlap unknown: check `dude diff --task " + w.key() + "` against your branch before you go on."
}

// told says task was told line, once.
func (w *epicMergeWorld) told(task, line string) {
	w.t.Helper()
	if got := w.wakes(task); len(got) != 1 || got[0] != line {
		w.t.Fatalf("told %q\nwant [%q]", got, line)
	}
}

// mergedFiles are the files pull request #1 changes, as GitHub lists them.
func (w *epicMergeWorld) mergedFiles() []string {
	gh, err := forge.Resolver{DB: w.app}.For(context.Background(), w.org)
	if err != nil {
		w.t.Fatal(err)
	}
	files, err := gh.PullFiles(context.Background(), "acme/target", 1, 100)
	if err != nil || len(files) == 0 {
		w.t.Fatalf("pull request files %v: %v", files, err)
	}
	return files
}

// prHead is pull request #1's head as recorded.
func (w *epicMergeWorld) prHead() string {
	var head string
	if err := w.owner.QueryRow(context.Background(), `SELECT head_sha FROM pull_requests WHERE id = $1`, w.prID).Scan(&head); err != nil {
		w.t.Fatal(err)
	}
	return head
}

// stubForge is a GitHub that answers a pull request's files as files says:
// the page's entries, or a status and a Retry-After. It counts requests.
type stubForge struct {
	srv   *httptest.Server
	calls atomic.Int32
	files atomic.Value // func(number, page int) (status int, retryAfter string, body []map[string]any)
}

func newStubForge(t *testing.T) *stubForge {
	f := &stubForge{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		parts := strings.Split(r.URL.Path, "/") // /repos/acme/target/pulls/N/files
		number, _ := strconv.Atoi(parts[len(parts)-2])
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		status, retry, body := f.files.Load().(func(int, int) (int, string, []map[string]any))(number, page)
		if retry != "" {
			rw.Header().Set("Retry-After", retry)
		}
		rw.Header().Set("Content-Type", "application/json")
		rw.WriteHeader(status)
		if status != 200 {
			_, _ = rw.Write([]byte(`{"message":"slow down"}`))
			return
		}
		_ = json.NewEncoder(rw).Encode(body)
	}))
	t.Cleanup(f.srv.Close)
	f.answer(200, "")
	return f
}

// answer makes every request answer status (a page of files on 200).
func (f *stubForge) answer(status int, retry string, files ...map[string]any) {
	f.files.Store(func(int, int) (int, string, []map[string]any) { return status, retry, files })
}

func (f *stubForge) For(context.Context, string) (*forge.GitHub, error) {
	return forge.NewGitHub(forge.Credential{Auth: "pat", Secret: "ghp_stub", APIBaseURL: f.srv.URL}), nil
}

func file(path string) map[string]any { return map[string]any{"filename": path, "status": "modified"} }

// A peer on the epic with an open delivery and a live conductor is woken
// once, told the merged task, its pull request, the merge commit and the
// files its branch shares; the merged task, ended peers (done, cancelled,
// failed), a peer not started, one with no conductor, and tasks on another
// epic or none are not. A running delivery counts as open, and a conductor
// not live with a reason already pending will be started: both woken.
func TestEpicMergeWakesLivePeersOnce(t *testing.T) {
	w := newEpicMergeWorld(t)
	shared := w.mergedFiles()[0]
	mustExec(t, w.owner, `INSERT INTO epics (id, organization_id, project_id, title) VALUES ($1, $2, $3, 'Other')`, w.epic+"_other", w.org, w.project)
	peer := w.peer(w.live("docs/unrelated.md", shared))
	running := w.live(shared)
	running.deliveryStatus = "running"
	runningPeer := w.peer(running)
	pendingWake := w.live(shared)
	pendingWake.conductor = false
	pendingPeer := w.peer(pendingWake)
	mustExec(t, w.owner, `INSERT INTO conductor_wakes (id, organization_id, task_id, kind, key, line) VALUES ($1, $2, $3, 'decision', 'd:1', 'decide')`,
		"cwk_p_"+pendingPeer, w.org, pendingPeer)
	ended := map[string]string{}
	for _, status := range []string{"done", "aborted", "failed"} {
		p := w.live(shared)
		p.status = status
		ended[status] = w.peer(p)
	}
	notStarted := w.live(shared)
	notStarted.status, notStarted.deliveryStatus, notStarted.conductor = "queued", "", false
	noConductor := w.live(shared)
	noConductor.conductor = false
	otherEpic := w.live(shared)
	otherEpic.epic = w.epic + "_other"
	noEpic := w.live(shared)
	noEpic.epic = ""
	quiet := map[string]string{"not started": w.peer(notStarted), "no conductor": w.peer(noConductor),
		"other epic": w.peer(otherEpic), "no epic": w.peer(noEpic)}
	for k, v := range ended {
		quiet[k] = v
	}
	// The merged task's own conductor, live: told by its own close-out.
	mustExec(t, w.owner, `INSERT INTO runs(id, organization_id, project_id, task_id, attempt, status, role, kind)
		VALUES($1,$2,$3,$4,1,'paused','conductor','agent') ON CONFLICT DO NOTHING`, "run_c_"+w.merging, w.org, w.project, w.merging)
	quiet["merged"] = w.merging
	w.merge()
	if n := w.sweep(); n != 1 {
		t.Errorf("settled %d, want the one merge", n)
	}
	w.told(peer, w.shared(shared))
	w.told(runningPeer, w.shared(shared))
	w.told(pendingPeer, w.shared(shared))
	for name, id := range quiet {
		if n := w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1 AND kind = 'epic_merge'`, id); n != 0 {
			t.Errorf("%s task woken %d times", name, n)
		}
	}
}

// Two passes, a worker restarted, and the pull request found untold again
// (a pass that crashed before marking it) each leave one wake per peer.
func TestEpicMergeWakesOncePerMerge(t *testing.T) {
	w := newEpicMergeWorld(t)
	peer := w.peer(w.live("x.go"))
	w.merge()
	w.sweep()
	w.sweep()
	w.worker = &delivery.EpicMerges{DB: w.app, Forges: forge.Resolver{DB: w.app}}
	w.sweep()
	mustExec(t, w.owner, `UPDATE pull_requests SET epic_peers_told_at = NULL WHERE id = $1`, w.prID)
	w.sweep()
	for range 3 {
		w.sync()
	}
	w.sweep()
	w.told(peer, w.none())
	if n := w.count(`SELECT count(*) FROM pull_requests WHERE id = $1 AND epic_peers_told_at IS NOT NULL`, w.prID); n != 1 {
		t.Fatal("the merged pull request is not marked told")
	}
}

// The overlap, on each side, from what is stored and what the forge says.
// "Can't tell" is "overlap unknown", never "no overlapping files".
func TestEpicMergeOverlap(t *testing.T) {
	t.Run("no shared file", func(t *testing.T) {
		w := newEpicMergeWorld(t)
		peer := w.peer(w.live("elsewhere/only.go"))
		w.merge()
		w.sweep()
		w.told(peer, w.none())
	})
	t.Run("peer branch never read", func(t *testing.T) {
		w := newEpicMergeWorld(t)
		peer := w.peer(w.live())
		w.merge()
		w.sweep()
		w.told(peer, w.unknown())
	})
	t.Run("peer branch known absent", func(t *testing.T) {
		w := newEpicMergeWorld(t)
		peer := w.peer(w.live())
		mustExec(t, w.owner, `INSERT INTO task_branch_capture_requests (organization_id, task_id, attempt, repository_id, status)
			VALUES ($1, $2, 1, $3, 'absent')`, w.org, peer, w.repoID)
		w.merge()
		w.sweep()
		w.told(peer, w.none())
	})
	for _, status := range []string{"unavailable", "pending"} {
		t.Run("peer branch capture "+status, func(t *testing.T) {
			w := newEpicMergeWorld(t)
			peer := w.peer(w.live())
			mustExec(t, w.owner, `INSERT INTO task_branch_capture_requests (organization_id, task_id, attempt, repository_id, status)
				VALUES ($1, $2, 1, $3, $4)`, w.org, peer, w.repoID, status)
			w.merge()
			w.sweep()
			w.told(peer, w.unknown())
		})
	}
	t.Run("peer list cut short", func(t *testing.T) {
		w := newEpicMergeWorld(t)
		peer := w.peer(w.live())
		w.snapshot(peer, strings.Repeat("d", 40), []string{"elsewhere/only.go"}, "partial")
		w.merge()
		w.sweep()
		w.told(peer, w.unknown())
	})
	t.Run("merged list cut at the cap", func(t *testing.T) {
		w := newEpicMergeWorld(t)
		peer := w.peer(w.live("elsewhere/only.go"))
		f := newStubForge(t)
		f.files.Store(func(_, page int) (int, string, []map[string]any) {
			var out []map[string]any
			for i := range 100 {
				out = append(out, file(fmt.Sprintf("gen/%02d/%03d.go", page, i)))
			}
			return 200, "", out
		})
		w.worker.Forges = f
		w.merge()
		w.sweep()
		w.told(peer, w.unknown())
	})
	t.Run("restarted peer's earlier attempt", func(t *testing.T) {
		w := newEpicMergeWorld(t)
		shared := w.mergedFiles()[0]
		p := w.live()
		p.state = `{"attempt": 2}`
		peer := w.peer(p)
		w.snapshotAt(peer, w.repoID, 1, strings.Repeat("b", 40), 0, "complete", entries(shared))
		w.merge()
		w.sweep()
		w.told(peer, w.unknown())
	})
	t.Run("peer's branch in another repository", func(t *testing.T) {
		w := newEpicMergeWorld(t)
		shared := w.mergedFiles()[0]
		peer := w.peer(w.live())
		web := "repo_web_" + w.org
		mustExec(t, w.owner, `INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch)
			VALUES ($1, $2, $3, 'web', 'git://127.0.0.1/acme/web.git', 'main')`, web, w.org, w.project)
		mustExec(t, w.owner, `INSERT INTO task_repositories (organization_id, task_id, repository_id, access) VALUES ($1, $2, $3, 'write')`,
			w.org, peer, web)
		w.snapshotAt(peer, web, 0, strings.Repeat("b", 40), 0, "complete", entries(shared))
		w.merge()
		w.sweep()
		w.told(peer, w.unknown())
	})
	t.Run("peer's newest snapshot", func(t *testing.T) {
		w := newEpicMergeWorld(t)
		shared := w.mergedFiles()[0]
		peer := w.peer(w.live())
		w.snapshotAt(peer, w.repoID, 0, strings.Repeat("e", 40), time.Hour, "complete", entries(shared))
		w.snapshotAt(peer, w.repoID, 0, strings.Repeat("f", 40), 0, "complete", entries("elsewhere/only.go"))
		w.merge()
		w.sweep()
		w.told(peer, w.none())
	})
	t.Run("read-only peer", func(t *testing.T) {
		w := newEpicMergeWorld(t)
		shared := w.mergedFiles()[0]
		p := w.live(shared)
		p.access = "read"
		peer := w.peer(p)
		w.merge()
		w.sweep()
		w.told(peer, w.none())
	})
	t.Run("merged files unreadable past the window", func(t *testing.T) {
		w := newEpicMergeWorld(t)
		shared := w.mergedFiles()[0]
		peer := w.peer(w.live(shared))
		w.merge()
		w.worker.Forges = nil
		w.sweep()
		if got := w.wakes(peer); len(got) != 0 {
			t.Fatalf("woken before the retry window ended: %q", got)
		}
		mustExec(t, w.owner, `UPDATE pull_requests SET merged_at = now() - interval '1 hour' WHERE id = $1`, w.prID)
		w.sweep()
		w.told(peer, w.unknown())
	})
	t.Run("merged files stored, in any case and punctuation", func(t *testing.T) {
		w := newEpicMergeWorld(t)
		peer := w.peer(w.live("api/a.go", "README.md", ".github/x", "Makefile", "zz/peer-only.go"))
		w.snapshot(w.merging, w.prHead(), []string{"Makefile", "README.md", ".github/x", "api/a.go", "b.go", "docs/x.md"}, "complete")
		// The merged task's snapshot at an older head: not what merged.
		w.snapshot(w.merging, strings.Repeat("9", 40), []string{"zz/peer-only.go"}, "complete")
		w.merge()
		// No forge: the merged task's snapshot at the pull request's head is
		// what the merge changed.
		w.worker.Forges = nil
		w.sweep()
		w.told(peer, w.shared(".github/x", "Makefile", "README.md", "api/a.go"))
	})
	t.Run("forge's files unsorted", func(t *testing.T) {
		w := newEpicMergeWorld(t)
		peer := w.peer(w.live("a.go"))
		f := newStubForge(t)
		f.answer(200, "", file("z.go"), file("m.go"), file("a.go"))
		w.worker.Forges = f
		w.merge()
		w.sweep()
		w.told(peer, w.shared("a.go"))
	})
	t.Run("the merge renamed what the peer edits", func(t *testing.T) {
		w := newEpicMergeWorld(t)
		peer := w.peer(w.live("billing/old.go"))
		f := newStubForge(t)
		f.answer(200, "", map[string]any{"filename": "billing/new.go", "status": "renamed", "previous_filename": "billing/old.go"})
		w.worker.Forges = f
		w.merge()
		w.sweep()
		w.told(peer, w.shared("billing/old.go"))
	})
	t.Run("the peer renamed what the merge edits", func(t *testing.T) {
		w := newEpicMergeWorld(t)
		peer := w.peer(w.live())
		w.snapshotAt(peer, w.repoID, 0, strings.Repeat("b", 40), 0, "complete",
			[]string{`{"path": "billing/new.go", "previousPath": "billing/old.go", "status": "R", "additions": 0, "deletions": 0}`})
		f := newStubForge(t)
		f.answer(200, "", file("billing/old.go"))
		w.worker.Forges = f
		w.merge()
		w.sweep()
		w.told(peer, w.shared("billing/old.go"))
	})
	t.Run("a rename stored without its previous name", func(t *testing.T) {
		w := newEpicMergeWorld(t)
		peer := w.peer(w.live("billing/old.go"))
		w.snapshotAt(w.merging, w.repoID, 0, w.prHead(), 0, "complete",
			[]string{`{"path": "billing/new.go", "status": "R", "additions": 0, "deletions": 0}`})
		w.merge()
		w.worker.Forges = nil
		w.sweep()
		w.told(peer, w.unknown())
	})
	t.Run("a peer on another base", func(t *testing.T) {
		w := newEpicMergeWorld(t)
		shared := w.mergedFiles()[0]
		p := w.live(shared)
		p.state = `{"bases": {"target": "release/1.2"}}`
		peer := w.peer(p)
		w.merge()
		w.sweep()
		w.told(peer, w.head()+" Your branch also changes `"+shared+"`. It merged into `main`, not your base: check whether your work needs it before you go on.")
	})
}

// The forge failing: the pull request waits for its retry time (the
// forge's Retry-After, else a backoff from 10s doubling to 2m) and asks
// nothing meanwhile; once it answers, the peers are told. The 10 minutes
// from the merge are the database's: at 9m50s it still waits, at 10m10s
// the peers are told the overlap is unknown, retry time or not.
func TestEpicMergeForgeBackOff(t *testing.T) {
	w := newEpicMergeWorld(t)
	peer := w.peer(w.live("a.go"))
	f := newStubForge(t)
	f.answer(http.StatusTooManyRequests, "30")
	w.worker.Forges = f
	w.merge()
	retryIn := func() float64 {
		var s float64
		if err := w.owner.QueryRow(context.Background(), `SELECT EXTRACT(EPOCH FROM epic_peers_retry_at - now()) FROM pull_requests WHERE id = $1`,
			w.prID).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	if n := w.sweep(); n != 0 {
		t.Fatalf("settled %d with the forge refusing", n)
	}
	if s := retryIn(); s < 25 || s > 30 {
		t.Errorf("retry in %.0fs, want the forge's 30", s)
	}
	calls := f.calls.Load()
	w.sweep()
	if f.calls.Load() != calls || len(w.wakes(peer)) != 0 {
		t.Fatalf("asked again before the retry time: %d calls, %q", f.calls.Load()-calls, w.wakes(peer))
	}
	// No Retry-After: the backoff, as long as since the merge, 10s to 2m.
	f.answer(http.StatusBadGateway, "")
	for _, c := range []struct {
		since    string
		min, max float64
	}{{"0 seconds", 5, 10.5}, {"40 seconds", 35, 40.5}, {"5 minutes", 115, 120.5}} {
		mustExec(t, w.owner, `UPDATE pull_requests SET merged_at = now() - $2::interval, epic_peers_retry_at = NULL WHERE id = $1`, w.prID, c.since)
		w.sweep()
		if s := retryIn(); s < c.min || s > c.max {
			t.Errorf("merged %s ago: retry in %.0fs, want %.0f-%.0f", c.since, s, c.min, c.max)
		}
	}
	// At 9m50s, the retry time not yet come: waiting still.
	mustExec(t, w.owner, `UPDATE pull_requests SET merged_at = now() - interval '9 minutes 50 seconds' WHERE id = $1`, w.prID)
	w.sweep()
	if got := w.wakes(peer); len(got) != 0 {
		t.Fatalf("told before 10 minutes: %q", got)
	}
	// At 10m10s: unknown, though the retry time has not come.
	mustExec(t, w.owner, `UPDATE pull_requests SET merged_at = now() - interval '10 minutes 10 seconds' WHERE id = $1`, w.prID)
	w.sweep()
	w.told(peer, w.unknown())
}

// The forge answering after it failed: the next pass past the retry time
// tells the peers the overlap.
func TestEpicMergeForgeRecovers(t *testing.T) {
	w := newEpicMergeWorld(t)
	peer := w.peer(w.live("a.go"))
	f := newStubForge(t)
	f.answer(http.StatusBadGateway, "")
	w.worker.Forges = f
	w.merge()
	w.sweep()
	f.answer(200, "", file("a.go"))
	mustExec(t, w.owner, `UPDATE pull_requests SET epic_peers_retry_at = now() - interval '1 second' WHERE id = $1`, w.prID)
	w.sweep()
	w.told(peer, w.shared("a.go"))
}

// One merge that cannot be told (its wake refused, as a CHECK that lost
// the kind would) is logged and left untold with a retry time; the next
// merge in the same pass is told all the same.
func TestEpicMergeOneFailureDoesNotHoldTheRest(t *testing.T) {
	w := newEpicMergeWorld(t)
	peer := w.peer(w.live("a.go"))
	other := w.task()
	mustExec(t, w.owner, `UPDATE tasks SET epic_id = $1 WHERE id = $2`, w.epic, other)
	mustExec(t, w.owner, `INSERT INTO pull_requests (id, organization_id, project_id, task_id, repository_id, number, url, head_branch, base_branch,
			head_sha, title, state, merged_at)
		VALUES ($1, $2, $3, $4, $5, 2, 'https://github.test/acme/target/pull/2', 'dude/other', 'main', $6, 't', 'merged', now())`,
		"pr_other_"+w.org, w.org, w.project, other, w.repoID, strings.Repeat("a", 40))
	f := newStubForge(t)
	f.answer(200, "", file("a.go"))
	w.worker.Forges = f
	w.merge()
	// Pull request #1's merge sorts first; its wake is refused.
	mustExec(t, w.owner, `UPDATE pull_requests SET merged_at = now() - interval '1 minute' WHERE id = $1`, w.prID)
	mustExec(t, w.owner, `ALTER TABLE conductor_wakes ADD CONSTRAINT refuse_one CHECK (key <> '`+delivery.EpicMergeWakeKey(w.prID)+`') NOT VALID`)
	n, err := w.worker.Sweep(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("settled %d (%v), want the other merge", n, err)
	}
	if got := w.wakesOf(peer, "pr_other_"+w.org); len(got) != 1 {
		t.Fatalf("the later merge's peer told %q", got)
	}
	if c := w.count(`SELECT count(*) FROM pull_requests WHERE id = $1 AND epic_peers_told_at IS NULL AND epic_peers_retry_at > now()`, w.prID); c != 1 {
		t.Fatal("the refused merge is not left untold, waiting for its retry")
	}
}

// A pull request another transaction holds is skipped, not counted as
// settled, and told on the next pass once released.
func TestEpicMergeLockedIsNotSettled(t *testing.T) {
	w := newEpicMergeWorld(t)
	peer := w.peer(w.live("a.go"))
	w.merge()
	ctx := context.Background()
	tx, err := w.owner.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SELECT 1 FROM pull_requests WHERE id = $1 FOR UPDATE`, w.prID); err != nil {
		t.Fatal(err)
	}
	short, cancel := context.WithTimeout(ctx, 10*time.Second)
	n, err := w.worker.Sweep(short)
	cancel()
	if err != nil || n != 0 {
		t.Fatalf("a held pull request: settled %d (%v), want 0 without waiting", n, err)
	}
	if got := w.wakes(peer); len(got) != 0 {
		t.Fatalf("told while held: %q", got)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if n := w.sweep(); n != 1 {
		t.Fatalf("settled %d once released", n)
	}
	w.told(peer, w.none())
}

// Level-triggered: a pass that missed the merge (the hook skipped, as a
// crash between the merge's record and the worker would) wakes nobody, and
// the next pass, reading the merged pull requests not yet told, does.
func TestEpicMergeMissedPassStillWakes(t *testing.T) {
	w := newEpicMergeWorld(t)
	peer := w.peer(w.live("x.go"))
	w.merge()
	w.worker.Skip = func(string) bool { return true }
	w.sweep()
	if got := w.wakes(peer); len(got) != 0 {
		t.Fatalf("woken by a skipped pass: %q", got)
	}
	w.worker.Skip = nil
	w.sweep()
	w.told(peer, w.none())
}

// A peer that ends between the read and the record is not woken: the
// peers are read again under the pull request's lock. It ends while the
// merge's files are read from the forge, between the two.
func TestEpicMergePeerEndedIsNotWoken(t *testing.T) {
	w := newEpicMergeWorld(t)
	peer := w.peer(w.live("x.go"))
	w.merge()
	w.worker.Forges = endingForges{Forges: forge.Resolver{DB: w.app}, end: func() {
		mustExec(t, w.owner, `UPDATE tasks SET status = 'done' WHERE id = $1`, peer)
	}}
	w.sweep()
	if got := w.wakes(peer); len(got) != 0 {
		t.Fatalf("%q", got)
	}
	if n := w.count(`SELECT count(*) FROM pull_requests WHERE id = $1 AND epic_peers_told_at IS NOT NULL`, w.prID); n != 1 {
		t.Fatal("not marked told")
	}
}

// A peer whose branch moves while the merge's files are read is told what
// its branch is under the lock, not what it was before.
func TestEpicMergePeerReadAgain(t *testing.T) {
	w := newEpicMergeWorld(t)
	shared := w.mergedFiles()[0]
	peer := w.peer(w.live("elsewhere/only.go"))
	w.merge()
	w.worker.Forges = endingForges{Forges: forge.Resolver{DB: w.app}, end: func() {
		w.snapshotAt(peer, w.repoID, 0, strings.Repeat("7", 40), -time.Minute, "complete", entries(shared))
	}}
	w.sweep()
	w.told(peer, w.shared(shared))
}

// endingForges runs end as a forge client is asked for.
type endingForges struct {
	delivery.Forges
	end func()
}

func (f endingForges) For(ctx context.Context, org string) (*forge.GitHub, error) {
	f.end()
	return f.Forges.For(ctx, org)
}
