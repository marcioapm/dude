package orchestrator_test

// A merge on an epic wakes its peers' conductors (delivery.EpicMerges):
// who is woken, once, with what overlap, and from stored state when the
// event was missed.

import (
	"context"
	"fmt"
	"strings"
	"testing"

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

// peer is a task on epic (none: "") in the given status, writing
// acme/target, with an open delivery unless !delivery, a live conductor
// unless !conductor, and a branch snapshot listing files (nil: none).
func (w *epicMergeWorld) peer(epic, status string, delivery, conductor bool, files []string) string {
	t := w.t
	id := w.task()
	if epic != "" {
		mustExec(t, w.owner, `UPDATE tasks SET epic_id = $1 WHERE id = $2`, epic, id)
	}
	mustExec(t, w.owner, `UPDATE tasks SET status = $2::task_status WHERE id = $1`, id, status)
	mustExec(t, w.owner, `INSERT INTO task_repositories(task_id, repository_id, access, organization_id) VALUES($1,$2,'write',$3)`, id, w.repoID, w.org)
	if delivery {
		mustExec(t, w.owner, `INSERT INTO workflow_runs(id, organization_id, workflow_type, idempotency_key, task_id, status, step, state)
			VALUES($1,$2,$3,$1,$4,'waiting','awaitPullRequest','{}')`, "wf_"+id, w.org, deliveryType, id)
	}
	if conductor {
		mustExec(t, w.owner, `INSERT INTO runs(id, organization_id, project_id, task_id, attempt, status, role, kind)
			VALUES($1,$2,$3,$4,1,'paused','conductor','agent')`, "run_c_"+id, w.org, w.project, id)
	}
	if files != nil {
		w.snapshot(id, strings.Repeat("b", 40), files, "complete")
	}
	return id
}

const deliveryType = delivery.WorkflowType

// snapshot records task's branch snapshot of acme/target at head.
func (w *epicMergeWorld) snapshot(task, head string, files []string, status string) {
	list := make([]string, 0, len(files))
	for _, f := range files {
		list = append(list, fmt.Sprintf(`{"path": %q, "status": "modified", "additions": 1, "deletions": 0}`, f))
	}
	var reason any
	if status == "partial" {
		reason = "GitHub listed 300 files, its cap"
	}
	mustExec(w.t, w.owner, `INSERT INTO task_branch_snapshots (id, organization_id, task_id, attempt, repository_id, repository,
			branch_ref, target_ref, target_sha, merge_base_sha, head_sha, files, checksum, stats, status, reason, relation)
		VALUES ($1, $2, $3, GREATEST(task_attempt($3), 1), $4, 'target', 'dude/x', 'main', $5, $5, $6, $7::jsonb, 'c', '{}', $8, $9, 'initial')`,
		"tbs_"+task+"_"+head[:8], w.org, task, w.repoID, strings.Repeat("c", 40), head, "["+strings.Join(list, ",")+"]", status, reason)
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

func (w *epicMergeWorld) sweep() {
	w.t.Helper()
	if _, err := w.worker.Sweep(context.Background()); err != nil {
		w.t.Fatal(err)
	}
}

// wakes are task's epic-merge reasons' lines.
func (w *epicMergeWorld) wakes(task string) []string {
	rows, err := w.owner.Query(context.Background(), `SELECT line FROM conductor_wakes WHERE task_id = $1 AND kind = 'epic_merge'
		AND key = $2 ORDER BY created_at`, task, delivery.EpicMergeWakeKey(w.prID))
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

// A peer on the epic with an open delivery and a live conductor is woken
// once, told the merged task, its pull request, the merge commit and the
// files its branch shares; the merged task, ended peers, a peer not
// started, one with no conductor, and tasks on another epic or none are
// not.
func TestEpicMergeWakesLivePeersOnce(t *testing.T) {
	w := newEpicMergeWorld(t)
	shared := w.mergedFiles()[0]
	w.mergeInto(t, shared)
}

func (w *epicMergeWorld) mergeInto(t *testing.T, shared string) {
	mustExec(t, w.owner, `INSERT INTO epics (id, organization_id, project_id, title) VALUES ($1, $2, $3, 'Other')`, w.epic+"_other", w.org, w.project)
	peer := w.peer(w.epic, "running", true, true, []string{"docs/unrelated.md", shared})
	done := w.peer(w.epic, "done", true, true, []string{shared})
	cancelled := w.peer(w.epic, "aborted", true, true, []string{shared})
	notStarted := w.peer(w.epic, "queued", false, false, []string{shared})
	noConductor := w.peer(w.epic, "running", true, false, []string{shared})
	otherEpic := w.peer(w.epic+"_other", "running", true, true, []string{shared})
	noEpic := w.peer("", "running", true, true, []string{shared})
	// The merged task's own conductor, live: told by its own close-out.
	mustExec(t, w.owner, `INSERT INTO runs(id, organization_id, project_id, task_id, attempt, status, role, kind)
		VALUES($1,$2,$3,$4,1,'paused','conductor','agent') ON CONFLICT DO NOTHING`, "run_c_"+w.merging, w.org, w.project, w.merging)
	w.merge()
	w.sweep()
	got := w.wakes(peer)
	if len(got) != 1 {
		t.Fatalf("peer woken %d times: %q", len(got), got)
	}
	var key string
	var number int
	var sha string
	if err := w.owner.QueryRow(context.Background(), `SELECT p.key_prefix || '-' || t.number, pr.number, pr.merge_commit_sha
		FROM pull_requests pr JOIN tasks t ON t.id = pr.task_id JOIN projects p ON p.id = t.project_id WHERE pr.id = $1`, w.prID).Scan(&key, &number, &sha); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{key + " on your epic merged target#" + fmt.Sprint(number), sha[:12], "`" + shared + "`", "bring `main` in"} {
		if !strings.Contains(got[0], want) {
			t.Errorf("line %q lacks %q", got[0], want)
		}
	}
	if strings.Contains(got[0], "unrelated") {
		t.Errorf("line %q names a file the merge did not change", got[0])
	}
	for name, id := range map[string]string{"merged": w.merging, "done": done, "cancelled": cancelled, "not started": notStarted,
		"no conductor": noConductor, "other epic": otherEpic, "no epic": noEpic} {
		if n := w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1 AND kind = 'epic_merge'`, id); n != 0 {
			t.Errorf("%s task woken %d times", name, n)
		}
	}
}

// Two passes, a worker restarted, and the pull request found untold again
// (a pass that crashed before marking it) each leave one wake per peer.
func TestEpicMergeWakesOncePerMerge(t *testing.T) {
	w := newEpicMergeWorld(t)
	peer := w.peer(w.epic, "running", true, true, []string{"x.go"})
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
	if got := w.wakes(peer); len(got) != 1 {
		t.Fatalf("peer woken %d times: %q", len(got), got)
	}
	if n := w.count(`SELECT count(*) FROM pull_requests WHERE id = $1 AND epic_peers_told_at IS NOT NULL`, w.prID); n != 1 {
		t.Fatal("the merged pull request is not marked told")
	}
}

// The overlap: no shared file says so; a peer whose branch was never read
// (no snapshot), or whose list is cut short with nothing shared, is
// "overlap unknown", never "no overlap"; a merge whose files the forge
// will not give, past the retry window, is unknown too.
func TestEpicMergeOverlap(t *testing.T) {
	t.Run("no shared file", func(t *testing.T) {
		w := newEpicMergeWorld(t)
		peer := w.peer(w.epic, "running", true, true, []string{"elsewhere/only.go"})
		w.merge()
		w.sweep()
		if got := w.wakes(peer); len(got) != 1 || !strings.Contains(got[0], "No overlapping files") {
			t.Fatalf("%q", got)
		}
	})
	t.Run("peer branch never read", func(t *testing.T) {
		w := newEpicMergeWorld(t)
		peer := w.peer(w.epic, "running", true, true, nil)
		w.merge()
		w.sweep()
		if got := w.wakes(peer); len(got) != 1 || !strings.Contains(got[0], "Overlap unknown") || strings.Contains(got[0], "No overlapping") {
			t.Fatalf("%q", got)
		}
	})
	t.Run("peer list cut short", func(t *testing.T) {
		w := newEpicMergeWorld(t)
		peer := w.peer(w.epic, "running", true, true, nil)
		w.snapshot(peer, strings.Repeat("d", 40), []string{"elsewhere/only.go"}, "partial")
		w.merge()
		w.sweep()
		if got := w.wakes(peer); len(got) != 1 || !strings.Contains(got[0], "Overlap unknown") {
			t.Fatalf("%q", got)
		}
	})
	t.Run("merged files unreadable", func(t *testing.T) {
		w := newEpicMergeWorld(t)
		shared := w.mergedFiles()[0]
		peer := w.peer(w.epic, "running", true, true, []string{shared})
		w.merge()
		w.worker.Forges = nil
		w.sweep()
		if got := w.wakes(peer); len(got) != 0 {
			t.Fatalf("woken before the retry window ended: %q", got)
		}
		mustExec(t, w.owner, `UPDATE pull_requests SET merged_at = now() - interval '1 hour' WHERE id = $1`, w.prID)
		w.sweep()
		if got := w.wakes(peer); len(got) != 1 || !strings.Contains(got[0], "Overlap unknown") {
			t.Fatalf("%q", got)
		}
	})
	t.Run("merged files stored", func(t *testing.T) {
		w := newEpicMergeWorld(t)
		peer := w.peer(w.epic, "running", true, true, []string{"stored/only.go"})
		var head string
		if err := w.owner.QueryRow(context.Background(), `SELECT head_sha FROM pull_requests WHERE id = $1`, w.prID).Scan(&head); err != nil {
			t.Fatal(err)
		}
		w.snapshot(w.merging, head, []string{"stored/only.go"}, "complete")
		w.merge()
		// No forge: the merged task's snapshot at the pull request's head is
		// what the merge changed.
		w.worker.Forges = nil
		w.sweep()
		if got := w.wakes(peer); len(got) != 1 || !strings.Contains(got[0], "`stored/only.go`") {
			t.Fatalf("%q", got)
		}
	})
}

// Level-triggered: a pass that missed the merge (the hook skipped, as a
// crash between the merge's record and the worker would) wakes nobody, and
// the next pass, reading the merged pull requests not yet told, does.
func TestEpicMergeMissedPassStillWakes(t *testing.T) {
	w := newEpicMergeWorld(t)
	peer := w.peer(w.epic, "running", true, true, []string{"x.go"})
	w.merge()
	w.worker.Skip = func(string) bool { return true }
	w.sweep()
	if got := w.wakes(peer); len(got) != 0 {
		t.Fatalf("woken by a skipped pass: %q", got)
	}
	w.worker.Skip = nil
	w.sweep()
	if got := w.wakes(peer); len(got) != 1 {
		t.Fatalf("peer woken %d times after the missed pass", len(got))
	}
}

// A peer that ends between the read and the record is not woken: the
// peers are read again under the pull request's lock. It ends while the
// merge's files are read from the forge, between the two.
func TestEpicMergePeerEndedIsNotWoken(t *testing.T) {
	w := newEpicMergeWorld(t)
	peer := w.peer(w.epic, "running", true, true, []string{"x.go"})
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

// endingForges runs end as a forge client is asked for.
type endingForges struct {
	delivery.Forges
	end func()
}

func (f endingForges) For(ctx context.Context, org string) (*forge.GitHub, error) {
	f.end()
	return f.Forges.For(ctx, org)
}
