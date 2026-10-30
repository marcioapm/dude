package orchestrator_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

// pruneFixture is one organization's task with two repositories, and a
// way to record snapshots captured hours ago.
type pruneFixture struct {
	t     *testing.T
	app   *db.DB
	owner *pgx.Conn
	org   string
	n     int
}

func newPruneFixture(t *testing.T) *pruneFixture {
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	mustExec(t, owner, `INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_p', $1, 'P', 'p', 'P')`, org)
	mustExec(t, owner, `INSERT INTO tasks (id, organization_id, project_id, number, title, goal) VALUES ('wi_p', $1, 'prj_p', 1, 'T', 'G')`, org)
	mustExec(t, owner, `INSERT INTO repositories (id, organization_id, project_id, name, url) VALUES
		('repo_api', $1, 'prj_p', 'api', 'https://example.com/acme/api'), ('repo_web', $1, 'prj_p', 'web', 'https://example.com/acme/web')`, org)
	mustExec(t, owner, `INSERT INTO people (id, organization_id, name) VALUES ('per_p', $1, 'Sam')`, org)
	return &pruneFixture{t: t, app: app, owner: owner, org: org}
}

func shaOf(i int) string { return fmt.Sprintf("%040x", i) }

// snap records a snapshot of attempt/repo captured hoursAgo, after prev
// ("" for none), on merge base mb.
func (f *pruneFixture) snap(id string, attempt int, repo string, hoursAgo float64, prev, relation string, mb int) {
	f.t.Helper()
	f.n++
	var p any
	if prev != "" {
		p = prev
	}
	mustExec(f.t, f.owner, `INSERT INTO task_branch_snapshots (id, organization_id, task_id, attempt, repository_id, repository, branch_ref,
		target_ref, target_sha, merge_base_sha, head_sha, files, checksum, stats, relation, previous_snapshot_id, captured_at)
		VALUES ($1, $2, 'wi_p', $3, $4, $5, 'dude/wi_p', 'main', $6, $7, $8, '[]', 'c', '{}', $9, $10, now() - make_interval(secs => $11))`,
		id, f.org, attempt, repo, strings.TrimPrefix(repo, "repo_"), shaOf(1), shaOf(mb), shaOf(1000+f.n), relation, p, hoursAgo*3600)
}

// chain records n snapshots of attempt/repo, one an hour, the newest
// newestHoursAgo, ids prefix1..prefixN, all fast-forwards on one merge base.
func (f *pruneFixture) chain(prefix string, attempt int, repo string, n int, newestHoursAgo float64) {
	f.t.Helper()
	prev := ""
	for i := 1; i <= n; i++ {
		rel := "fast-forward"
		if i == 1 {
			rel = "initial"
		}
		id := fmt.Sprintf("%s%d", prefix, i)
		f.snap(id, attempt, repo, newestHoursAgo+float64(n-i), prev, rel, 1)
		prev = id
	}
}

func (f *pruneFixture) prune(limit int) int {
	f.t.Helper()
	var n int
	if err := f.app.InSystem(context.Background(), "test", func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `SELECT prune_branch_snapshots($1)`, limit).Scan(&n)
	}); err != nil {
		f.t.Fatal(err)
	}
	return n
}

func (f *pruneFixture) ids() []string {
	f.t.Helper()
	rows, err := f.owner.Query(context.Background(), `SELECT id FROM task_branch_snapshots ORDER BY id`)
	if err != nil {
		f.t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		f.t.Fatal(err)
	}
	return out
}

// link reads id's previous, relation and steps.
func (f *pruneFixture) link(id string) string {
	f.t.Helper()
	var s string
	if err := f.owner.QueryRow(context.Background(), `SELECT COALESCE(previous_snapshot_id, '-') || ' ' || relation || ' ' || steps
		FROM task_branch_snapshots WHERE id = $1`, id).Scan(&s); err != nil {
		f.t.Fatalf("%s: %v", id, err)
	}
	return s
}

func (f *pruneFixture) want(want ...string) {
	f.t.Helper()
	slices.Sort(want)
	if got := f.ids(); !slices.Equal(got, want) {
		f.t.Errorf("kept %v, want %v", got, want)
	}
}

// The latest snapshot of each (task, attempt, repository) stays, however
// old: the read and the capturer both take it as the branch's.
func TestPruneKeepsEachLineagesLatest(t *testing.T) {
	f := newPruneFixture(t)
	f.chain("a", 1, "repo_api", 3, 48)
	f.chain("w", 1, "repo_web", 1, 100)
	// Another task's only snapshot, same attempt and repository, older
	// than a3: wi_p's newer rows are not its successors.
	mustExec(t, f.owner, `INSERT INTO tasks (id, organization_id, project_id, number, title, goal) VALUES ('wi_q', $1, 'prj_p', 2, 'Q', 'G')`, f.org)
	mustExec(t, f.owner, `INSERT INTO task_branch_snapshots (id, organization_id, task_id, attempt, repository_id, repository, branch_ref,
		target_ref, target_sha, merge_base_sha, head_sha, files, checksum, stats, relation, captured_at)
		VALUES ('q1', $1, 'wi_q', 1, 'repo_api', 'api', 'dude/wi_q', 'main', $2, $2, $3, '[]', 'c', '{}', 'initial', now() - interval '72 hours')`,
		f.org, shaOf(1), shaOf(7001))
	if n := f.prune(100); n != 2 {
		t.Errorf("pruned %d, want 2", n)
	}
	f.want("a3", "q1", "w1")
}

// One a feedback item names stays, so its anchor resolves.
func TestPruneKeepsWhatFeedbackNames(t *testing.T) {
	f := newPruneFixture(t)
	f.chain("a", 1, "repo_api", 4, 48)
	mustExec(t, f.owner, `INSERT INTO feedback_items (id, organization_id, task_id, attempt, author_person_id, body, anchor_kind, branch_snapshot_id, anchor)
		VALUES ('fbi_p', $1, 'wi_p', 1, 'per_p', 'b', 'code', 'a2', '{}')`, f.org)
	f.prune(100)
	f.want("a2", "a4")
}

// One a capture request names stays: the branch went back to it.
func TestPruneKeepsWhatACaptureRequestNames(t *testing.T) {
	f := newPruneFixture(t)
	f.chain("a", 1, "repo_api", 4, 48)
	mustExec(t, f.owner, `INSERT INTO task_branch_capture_requests (organization_id, task_id, attempt, repository_id, status, snapshot_id)
		VALUES ($1, 'wi_p', 1, 'repo_api', 'done', 'a2')`, f.org)
	f.prune(100)
	f.want("a2", "a4")
}

// One that was the latest in the last 24 hours stays: a tab pinned to it
// reads it. The day counts from when it was superseded, not captured.
func TestPruneKeepsTheLastDay(t *testing.T) {
	f := newPruneFixture(t)
	f.snap("old", 1, "repo_api", 24.5, "", "initial", 1)
	f.snap("day", 1, "repo_api", 23.5, "old", "fast-forward", 1)
	f.snap("now", 1, "repo_api", 0, "day", "fast-forward", 1)
	if n := f.prune(100); n != 0 {
		t.Errorf("pruned %d, want 0: old was the latest 23.5 hours ago", n)
	}
	f.want("old", "day", "now")
}

// One superseded more than a day ago goes; its child, itself superseded
// within the day, stays.
func TestPruneTakesWhatWasSupersededOverADayAgo(t *testing.T) {
	f := newPruneFixture(t)
	f.snap("old", 1, "repo_api", 26, "", "initial", 1)
	f.snap("day", 1, "repo_api", 24.5, "old", "fast-forward", 1)
	f.snap("now", 1, "repo_api", 0, "day", "fast-forward", 1)
	if n := f.prune(100); n != 1 {
		t.Errorf("pruned %d, want 1", n)
	}
	f.want("day", "now")
	// No previous left: as a foreign key's SET NULL leaves it.
	if got := f.link("day"); got != "- fast-forward 2" {
		t.Errorf("day after its initial went: %q", got)
	}
}

// A branch quiet for two days, then pushed: the snapshot a Changes tab has
// pinned stays, though captured long ago, until its successor is a day old.
func TestPruneKeepsOneJustSuperseded(t *testing.T) {
	f := newPruneFixture(t)
	f.snap("tbs_old", 1, "repo_api", 48, "", "initial", 1)
	f.snap("tbs_new", 1, "repo_api", 1.0/60, "tbs_old", "fast-forward", 1)
	if n := f.prune(100); n != 0 {
		t.Errorf("pruned %d, want 0", n)
	}
	f.want("tbs_new", "tbs_old")
}

// An older attempt keeps its latest per repository and what its feedback
// names; nothing else of it.
func TestPruneKeepsOlderAttemptsLatestAndFeedback(t *testing.T) {
	f := newPruneFixture(t)
	f.chain("o", 1, "repo_api", 4, 72)
	f.chain("x", 1, "repo_web", 2, 72)
	f.chain("c", 2, "repo_api", 2, 30)
	mustExec(t, f.owner, `INSERT INTO feedback_items (id, organization_id, task_id, attempt, author_person_id, body, anchor_kind, branch_snapshot_id, anchor)
		VALUES ('fbi_o', $1, 'wi_p', 1, 'per_p', 'b', 'code', 'o1', '{}')`, f.org)
	f.prune(100)
	f.want("o1", "o4", "x2", "c2")
	// o4 now follows o1 over the three captures since.
	if got := f.link("o4"); got != "o1 fast-forward 3" {
		t.Errorf("o4: %q", got)
	}
}

// A snapshot in the middle of a lineage goes; its child follows the one
// before it, over the captures between, and a rewrite anywhere in the span
// or a target merged in still reads as such.
func TestPruneRewiresTheLineageOverWhatWent(t *testing.T) {
	f := newPruneFixture(t)
	// api: k1 (kept by feedback) ← m2 ← m3 (rewritten) ← k4 ← n5 (latest)
	f.snap("k1", 1, "repo_api", 50, "", "initial", 1)
	f.snap("m2", 1, "repo_api", 49, "k1", "fast-forward", 1)
	f.snap("m3", 1, "repo_api", 48, "m2", "rewritten", 1)
	f.snap("k4", 1, "repo_api", 47, "m3", "fast-forward", 1)
	f.snap("n5", 1, "repo_api", 46, "k4", "fast-forward", 1)
	// web: j1 (kept) ← g2 (merge base 2: merged-target) ← j3 (latest, fast-forward on 2)
	f.snap("j1", 1, "repo_web", 50, "", "initial", 1)
	f.snap("g2", 1, "repo_web", 49, "j1", "merged-target", 2)
	f.snap("j3", 1, "repo_web", 48, "g2", "fast-forward", 2)
	mustExec(t, f.owner, `INSERT INTO feedback_items (id, organization_id, task_id, attempt, author_person_id, body, anchor_kind, branch_snapshot_id, anchor)
		VALUES ('fbi_k1', $1, 'wi_p', 1, 'per_p', 'b', 'code', 'k1', '{}'), ('fbi_k4', $1, 'wi_p', 1, 'per_p', 'b', 'code', 'k4', '{}'),
		       ('fbi_j1', $1, 'wi_p', 1, 'per_p', 'b', 'code', 'j1', '{}')`, f.org)
	if n := f.prune(100); n != 3 {
		t.Errorf("pruned %d, want 3", n)
	}
	f.want("k1", "k4", "n5", "j1", "j3")
	for id, want := range map[string]string{"k4": "k1 rewritten 3", "n5": "k4 fast-forward 1", "j3": "j1 merged-target 2", "k1": "- initial 1"} {
		if got := f.link(id); got != want {
			t.Errorf("%s: %q, want %q", id, got, want)
		}
	}
}

// A reset makes two children of one snapshot: both follow its previous.
func TestPruneRewiresEveryChild(t *testing.T) {
	f := newPruneFixture(t)
	f.snap("r1", 1, "repo_api", 50, "", "initial", 1)
	f.snap("r2", 1, "repo_api", 49, "r1", "fast-forward", 1)
	f.snap("r3", 1, "repo_api", 48, "r2", "fast-forward", 1)
	f.snap("r4", 1, "repo_api", 47, "r2", "fast-forward", 1) // the branch went back to r2, then on
	mustExec(t, f.owner, `INSERT INTO feedback_items (id, organization_id, task_id, attempt, author_person_id, body, anchor_kind, branch_snapshot_id, anchor)
		VALUES ('fbi_r1', $1, 'wi_p', 1, 'per_p', 'b', 'code', 'r1', '{}'), ('fbi_r3', $1, 'wi_p', 1, 'per_p', 'b', 'code', 'r3', '{}')`, f.org)
	f.prune(100)
	f.want("r1", "r3", "r4")
	for _, id := range []string{"r3", "r4"} {
		if got := f.link(id); got != "r1 fast-forward 2" {
			t.Errorf("%s: %q", id, got)
		}
	}
}

// p_limit bounds a call; calls again take the rest, oldest first.
func TestPruneDeletesInBatchesOldestFirst(t *testing.T) {
	f := newPruneFixture(t)
	f.chain("b", 1, "repo_api", 9, 30)
	if n := f.prune(3); n != 3 {
		t.Fatalf("first batch %d, want 3", n)
	}
	f.want("b4", "b5", "b6", "b7", "b8", "b9")
	if got := f.link("b4"); got != "- fast-forward 4" {
		t.Errorf("b4 after its ancestors went: %q", got)
	}
	if n := f.prune(3); n != 3 {
		t.Errorf("second batch %d", n)
	}
	if n := f.prune(3); n != 2 {
		t.Errorf("third batch %d, want 2", n)
	}
	if n := f.prune(3); n != 0 {
		t.Errorf("fourth batch %d, want 0", n)
	}
	f.want("b9")
	if n := f.prune(0); n != 0 {
		t.Errorf("a limit of 0 pruned %d", n)
	}
}

// Two callers at once, each in its own transaction, never wait on each
// other and never count a row twice: together they prune each prunable
// row once.
func TestPruneTwoCallersAtOnce(t *testing.T) {
	f := newPruneFixture(t)
	for i := 0; i < 12; i++ {
		f.chain(fmt.Sprintf("t%02d_", i), 1+i, "repo_api", 6, 30)
		f.chain(fmt.Sprintf("u%02d_", i), 1+i, "repo_web", 6, 30)
	}
	ctx := context.Background()
	// The first holds its transaction open until the second is done.
	firstIn, secondDone := make(chan struct{}), make(chan struct{})
	var first, second int
	var wg sync.WaitGroup
	var errs [2]error
	wg.Add(2)
	go func() {
		defer wg.Done()
		errs[0] = f.app.InSystem(ctx, "test-a", func(tx pgx.Tx) error {
			err := tx.QueryRow(ctx, `SELECT prune_branch_snapshots(40)`).Scan(&first)
			close(firstIn)
			<-secondDone
			return err
		})
	}()
	go func() {
		defer wg.Done()
		<-firstIn
		defer close(secondDone)
		errs[1] = f.app.InSystem(ctx, "test-b", func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `SET LOCAL lock_timeout = '5s'`)
			if err == nil {
				err = tx.QueryRow(ctx, `SELECT prune_branch_snapshots(1000)`).Scan(&second)
			}
			return err
		})
	}()
	wg.Wait()
	if errs[0] != nil || errs[1] != nil {
		t.Fatalf("callers: %v / %v", errs[0], errs[1])
	}
	if first != 40 || first+second > 120 {
		t.Errorf("first %d, second %d: want 40, and at most 120 together", first, second)
	}
	rest := f.prune(1000)
	if first+second+rest != 120 {
		t.Errorf("pruned %d+%d+%d, want 120", first, second, rest)
	}
	if n := len(f.ids()); n != 24 {
		t.Errorf("kept %d, want 24 latest", n)
	}
}

// Neither the app nor the sweeper may delete a snapshot or change one
// directly: only the pruner deletes (the app reaches it through
// dude_sweeper, as purge_automation_run_events, 008).
func TestPruneIsTheSweepersAlone(t *testing.T) {
	f := newPruneFixture(t)
	f.chain("a", 1, "repo_api", 2, 48)
	ctx := context.Background()
	for _, q := range []string{`DELETE FROM task_branch_snapshots`, `UPDATE task_branch_snapshots SET steps = 2`} {
		err := f.app.InOrg(ctx, f.org, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, q)
			return err
		})
		if err == nil || !strings.Contains(err.Error(), "permission denied") {
			t.Errorf("the app: %s: %v", q, err)
		}
	}
	err := f.app.InSystem(ctx, "test", func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM task_branch_snapshots`)
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("the sweeper deleting directly: %v", err)
	}
	var public bool
	if err := f.owner.QueryRow(ctx, `SELECT has_function_privilege('public', 'prune_branch_snapshots(integer)', 'EXECUTE')`).Scan(&public); err != nil || public {
		t.Errorf("PUBLIC may execute the pruner: %v (%v)", public, err)
	}
	var definer bool
	var config []string
	if err := f.owner.QueryRow(ctx, `SELECT prosecdef, proconfig FROM pg_proc WHERE proname = 'prune_branch_snapshots'`).Scan(&definer, &config); err != nil ||
		!definer || !slices.Contains(config, "search_path=public, pg_temp") {
		t.Errorf("prune_branch_snapshots: definer %v, config %v (%v)", definer, config, err)
	}
	f.want("a1", "a2")
}

// A snapshot someone is writing a feedback item on (the item's foreign key
// holds it, not yet committed) is skipped, never waited on, and kept once
// the item commits.
func TestPruneSkipsASnapshotAWriterHolds(t *testing.T) {
	f := newPruneFixture(t)
	f.chain("h", 1, "repo_api", 3, 48)
	ctx := context.Background()
	writer, err := f.app.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Rollback(ctx)
	if _, err := writer.Exec(ctx, `SELECT set_config('app.organization_id', $1, true)`, f.org); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Exec(ctx, `INSERT INTO feedback_items (id, organization_id, task_id, attempt, author_person_id, body, anchor_kind, branch_snapshot_id, anchor)
		VALUES ('fbi_h', $1, 'wi_p', 1, 'per_p', 'b', 'code', 'h1', '{}')`, f.org); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := f.app.InSystem(ctx, "test", func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout = '2s'`); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT prune_branch_snapshots(100)`).Scan(&n)
	}); err != nil {
		t.Fatalf("the pruner waited on the writer: %v", err)
	}
	if err := writer.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	f.want("h1", "h3")
	if got := f.link("h3"); got != "h1 fast-forward 2" {
		t.Errorf("h3: %q", got)
	}
}

// Another pruner holding rows (FOR UPDATE, as its own candidates): one
// whose child it holds, or whose children's new previous it holds, is
// skipped, never waited on; the rest of the lineage still goes.
func TestPruneSkipsALineageAnotherCallerHolds(t *testing.T) {
	f := newPruneFixture(t)
	f.chain("h", 1, "repo_api", 4, 48)
	f.chain("k", 1, "repo_web", 3, 48)
	ctx := context.Background()
	other, err := f.owner.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Rollback(ctx)
	if _, err := other.Exec(ctx, `SELECT id FROM task_branch_snapshots WHERE id IN ('h1', 'k2') FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := f.app.InSystem(ctx, "test", func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout = '2s'`); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT prune_branch_snapshots(100)`).Scan(&n)
	}); err != nil {
		t.Fatalf("the pruner waited on another: %v", err)
	}
	if err := other.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	// h2 would rewire h3 onto h1 (held): skipped; h3 goes, h4 onto h2.
	// k1's child k2 is held: k1 stays; k2 itself is held.
	if n != 1 {
		t.Errorf("pruned %d, want 1", n)
	}
	f.want("h1", "h2", "h4", "k1", "k2", "k3")
	if got := f.link("h4"); got != "h2 fast-forward 2" {
		t.Errorf("h4: %q", got)
	}
	if n := f.prune(100); n != 4 {
		t.Errorf("the next pass pruned %d, want 4", n)
	}
	f.want("h4", "k3")
}

// While a batch is open, the children it rewired (usually the lineage's
// latest) still take a capture's next child and a feedback item: the
// pruner never makes those writers wait.
func TestPruneLetsWritersOnTheRewiredLatestGoAhead(t *testing.T) {
	f := newPruneFixture(t)
	f.chain("h", 1, "repo_api", 3, 48)
	ctx := context.Background()
	var n int
	err := f.app.InSystem(ctx, "test", func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT prune_branch_snapshots(100)`).Scan(&n); err != nil {
			return err
		}
		// The batch is still open: another session writes on h3.
		w, err := f.owner.Begin(ctx)
		if err != nil {
			return err
		}
		defer w.Rollback(ctx)
		if _, err := w.Exec(ctx, `SET LOCAL lock_timeout = '2s'`); err != nil {
			return err
		}
		start := time.Now()
		if _, err := w.Exec(ctx, `INSERT INTO task_branch_snapshots (id, organization_id, task_id, attempt, repository_id, repository, branch_ref,
			target_ref, target_sha, merge_base_sha, head_sha, files, checksum, stats, relation, previous_snapshot_id)
			VALUES ('h4', $1, 'wi_p', 1, 'repo_api', 'api', 'dude/wi_p', 'main', $2, $2, $3, '[]', 'c', '{}', 'fast-forward', 'h3')`,
			f.org, shaOf(1), shaOf(9004)); err != nil {
			return fmt.Errorf("a capture of h3's child waited on the pruner: %w", err)
		}
		if _, err := w.Exec(ctx, `INSERT INTO feedback_items (id, organization_id, task_id, attempt, author_person_id, body, anchor_kind, branch_snapshot_id, anchor)
			VALUES ('fbi_w', $1, 'wi_p', 1, 'per_p', 'b', 'code', 'h3', '{}')`, f.org); err != nil {
			return fmt.Errorf("a feedback item on h3 waited on the pruner: %w", err)
		}
		if took := time.Since(start); took > time.Second {
			t.Errorf("the writes took %v with the batch open", took)
		}
		return w.Commit(ctx)
	})
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("pruned %d, want 2", n)
	}
	f.want("h3", "h4")
	if got := f.link("h3"); got != "- fast-forward 3" {
		t.Errorf("h3: %q", got)
	}
	if got := f.link("h4"); got != "h3 fast-forward 1" {
		t.Errorf("h4: %q", got)
	}
}

// A child whose own link already spans several captures adds them to its
// pruned parent's: 3 over 2 reads as 5.
func TestPruneAddsTheChildsSpanToTheParents(t *testing.T) {
	f := newPruneFixture(t)
	f.snap("k1", 1, "repo_api", 50, "", "initial", 1)
	f.snap("m2", 1, "repo_api", 40, "k1", "fast-forward", 1)
	f.snap("c3", 1, "repo_api", 30, "m2", "fast-forward", 1)
	mustExec(t, f.owner, `UPDATE task_branch_snapshots SET steps = CASE id WHEN 'm2' THEN 2 ELSE 3 END WHERE id IN ('m2', 'c3')`)
	mustExec(t, f.owner, `INSERT INTO feedback_items (id, organization_id, task_id, attempt, author_person_id, body, anchor_kind, branch_snapshot_id, anchor)
		VALUES ('fbi_k1', $1, 'wi_p', 1, 'per_p', 'b', 'code', 'k1', '{}')`, f.org)
	if n := f.prune(100); n != 1 {
		t.Errorf("pruned %d, want 1", n)
	}
	f.want("k1", "c3")
	if got := f.link("c3"); got != "k1 fast-forward 5" {
		t.Errorf("c3: %q, want k1 fast-forward 5", got)
	}
}
