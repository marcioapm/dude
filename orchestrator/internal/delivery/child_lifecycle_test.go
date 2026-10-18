package delivery

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

type childWorld struct {
	t     *testing.T
	ctx   context.Context
	s     *Store
	owner *pgx.Conn
	org   string
	in    StartChildInput
}

func newChildWorld(t *testing.T) *childWorld {
	t.Helper()
	ctx := context.Background()
	if err := dbtest.Reachable(ctx); err != nil {
		t.Fatal(err)
	}
	app, owner := dbtest.Open(t)
	w := &childWorld{t: t, ctx: ctx, s: &Store{DB: app}, owner: owner, org: dbtest.Org(t, owner)}
	w.exec(`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_life', $1, 'P', 'p', 'P')`, w.org)
	w.exec(`INSERT INTO tasks (id, organization_id, project_id, number, title, goal, status)
		VALUES ('wi_life', $1, 'prj_life', 1, 'Child', 'Investigate', 'running')`, w.org)
	w.exec(`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, role)
		VALUES ('run_parent', $1, 'prj_life', 'wi_life', 3, 'running', 'conductor')`, w.org)
	w.exec(`INSERT INTO repositories (id, organization_id, project_id, name, url)
		VALUES ('repo_life', $1, 'prj_life', 'repo', 'https://github.com/test/repo')`, w.org)
	w.exec(`INSERT INTO pull_requests (id, organization_id, project_id, task_id, repository_id, run_id, number, url,
		head_branch, base_branch, head_sha, title, checks, review)
		VALUES ('pr_life', $1, 'prj_life', 'wi_life', 'repo_life', 'run_parent', 7, 'https://github.com/test/repo/pull/7',
		'work', 'main', 'unchanged', 'PR', 'passing', 'approved')`, w.org)
	w.exec(`INSERT INTO workflow_runs (id, organization_id, task_id, workflow_type, idempotency_key, status, step, state)
		VALUES ('wf_life', $1, 'wi_life', 'task.delivery', 'life', 'waiting', 'awaitPullRequest',
		'{"taskId":"wi_life","projectId":"prj_life","attempt":3,"heads":{"repo":"stored"},"headRunId":"run_parent","pendingRunIds":["run_parent"],"reviewRunIds":["run_parent"],"pullRequestIds":["pr_life"],"prGate":{"answer":"approved"}}')`, w.org)
	w.in = StartChildInput{Parent: ChildParent{Kind: ChildParentTaskConductor, Run: RunRef{Org: w.org, ProjectID: "prj_life", TaskID: "wi_life", RunID: "run_parent"}}, Profile: "investigator"}
	return w
}

func (w *childWorld) exec(sql string, args ...any) {
	w.t.Helper()
	if _, err := w.owner.Exec(w.ctx, sql, args...); err != nil {
		w.t.Fatal(err)
	}
}

func (w *childWorld) child(key string) string {
	w.t.Helper()
	in := w.in
	in.Key = key
	id, err := w.s.StartChild(w.ctx, in)
	if err != nil {
		w.t.Fatal(err)
	}
	return id
}

func (w *childWorld) snapshot() string {
	w.t.Helper()
	var raw string
	if err := w.owner.QueryRow(w.ctx, `SELECT jsonb_build_object('task', to_jsonb(t), 'workflow', to_jsonb(w),
		'prs', (SELECT jsonb_agg(to_jsonb(p) ORDER BY p.id) FROM pull_requests p WHERE p.task_id = t.id),
		'signals', (SELECT jsonb_agg(to_jsonb(s) ORDER BY s.id) FROM workflow_signals s WHERE s.workflow_run_id = w.id))::text
		FROM tasks t JOIN workflow_runs w ON w.task_id = t.id WHERE t.id = 'wi_life'`).Scan(&raw); err != nil {
		w.t.Fatal(err)
	}
	return raw
}

func (w *childWorld) count(table string) int {
	w.t.Helper()
	// Only fixed test-owned table names are passed by callers.
	var n int
	if err := w.owner.QueryRow(w.ctx, `SELECT count(*) FROM `+table).Scan(&n); err != nil {
		w.t.Fatal(err)
	}
	return n
}

func TestChildSettlement(t *testing.T) {
	for _, status := range []string{"completed", "failed", "aborted"} {
		t.Run(status, func(t *testing.T) {
			w := newChildWorld(t)
			id := w.child("outcome")
			w.exec(`UPDATE runs SET heads = '{"repo":"0123456789012345678901234567890123456789"}', agent_cost_usd = 1.25 WHERE id = $1`, id)
			w.exec(`INSERT INTO artifacts (id, organization_id, run_id, kind, name, storage_key, sha256)
				VALUES ('art_life', $1, $2, 'published', 'report.md', 'private-storage-key', 'x')`, w.org, id)
			w.exec(`INSERT INTO review_findings (id, organization_id, task_id, run_id, category, severity, title, description)
				VALUES ('fnd_life', $1, 'wi_life', $2, 'correctness', 'high', 'private title', 'private prose')`, w.org, id)
			w.exec(`INSERT INTO acceptance_verdicts (organization_id, task_id, run_id, criterion, text, status, observed)
				VALUES ($1, 'wi_life', $2, 1, 'private criterion', 'pass', 'private observation')`, w.org, id)
			before := w.snapshot()
			if w.count("child_results") != 0 || w.count("child_result_outbox") != 0 {
				t.Fatal("nonempty fixture")
			}
			type result struct {
				won bool
				err error
			}
			results := make(chan result, 2)
			gate := make(chan struct{})
			for i := 0; i < 2; i++ {
				go func() {
					<-gate
					won, err := (&Store{DB: w.s.DB}).SettleChild(w.ctx, w.org, id, ChildOutcome{Status: status})
					results <- result{won, err}
				}()
			}
			close(gate)
			wins := 0
			for i := 0; i < 2; i++ {
				r := <-results
				if r.err != nil {
					t.Fatal(r.err)
				}
				if r.won {
					wins++
				}
			}
			if wins != 1 || w.count("child_results") != 1 || w.count("child_result_outbox") != 1 {
				t.Fatalf("wins %d results %d outbox %d", wins, w.count("child_results"), w.count("child_result_outbox"))
			}
			var gotStatus, parent, profile string
			var stale bool
			var findings, verdicts int
			var cost float64
			var commits map[string]string
			var artifacts []string
			if err := w.owner.QueryRow(w.ctx, `SELECT status, parent_run_id, profile, stale, findings, verdicts, cost_usd, commits, artifact_ids FROM child_results WHERE child_run_id = $1`, id).
				Scan(&gotStatus, &parent, &profile, &stale, &findings, &verdicts, &cost, &commits, &artifacts); err != nil {
				t.Fatal(err)
			}
			if gotStatus != status || parent != "run_parent" || profile != "investigator" || stale || findings != 1 || verdicts != 1 || cost != 1.25 || len(commits) != 1 || !reflect.DeepEqual(artifacts, []string{"art_life"}) {
				t.Fatalf("bad result: %s %s %s %v %d %d %v %v %v", gotStatus, parent, profile, stale, findings, verdicts, cost, commits, artifacts)
			}
			var runStatus string
			if err := w.owner.QueryRow(w.ctx, `SELECT status::text FROM runs WHERE id = $1`, id).Scan(&runStatus); err != nil || runStatus != status {
				t.Fatalf("run status %s: %v", runStatus, err)
			}
			if after := w.snapshot(); after != before {
				t.Fatal("settlement mutated task/workflow/real PR")
			}
			// New Store instances stand for workers restarting with no process-local state.
			for i := 0; i < 2; i++ {
				go func() {
					won, err := (&Store{DB: w.s.DB}).ConsumeChildResult(w.ctx, w.org, "run_parent", id, 1)
					results <- result{won, err}
				}()
			}
			wins = 0
			for i := 0; i < 2; i++ {
				r := <-results
				if r.err != nil {
					t.Fatal(r.err)
				}
				if r.won {
					wins++
				}
			}
			if wins != 1 {
				t.Fatalf("notification consumptions %d, want 1", wins)
			}
			for i := 0; i < 2; i++ {
				consumed, err := (&Store{DB: w.s.DB}).ConsumeChildResult(w.ctx, w.org, "run_parent", id, 1)
				if err != nil || consumed {
					t.Fatalf("receipt %d: %v %v", i, consumed, err)
				}
			}
			won, err := (&Store{DB: w.s.DB}).SettleChild(w.ctx, w.org, id, ChildOutcome{Status: status})
			if err != nil || won || w.count("child_result_outbox") != 1 {
				t.Fatalf("restart settlement: %v %v", won, err)
			}
			if after := w.snapshot(); after != before {
				t.Fatal("consumption mutated workflow")
			}
		})
	}
}

func TestChildWriterAdmissionReservesOnlyOneWriter(t *testing.T) {
	w := newChildWorld(t)
	in := w.in
	in.Profile = "implementer"
	in.Capabilities.PublishCode = true
	in.Key = "writer-one"
	if _, err := w.s.StartChild(w.ctx, in); err != nil {
		t.Fatal(err)
	}
	in.Key = "writer-two"
	if _, err := w.s.StartChild(w.ctx, in); !IsRefusal(err) {
		t.Fatalf("second writer: %v", err)
	}
	in.Key = "read-only"
	in.Profile = "investigator"
	in.Capabilities.PublishCode = false
	if _, err := w.s.StartChild(w.ctx, in); err != nil {
		t.Fatalf("read-only child beside writer: %v", err)
	}
}

func TestChildWriterAdmissionSeesWorkflowWriterAndPublishes(t *testing.T) {
	for _, livePublish := range []string{"requested", "asked", "pushed", "moving"} {
		t.Run(livePublish, func(t *testing.T) {
			w := newChildWorld(t)
			w.exec(`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, base_refs)
				VALUES ('workflow_writer', $1, 'prj_life', 'wi_life', 3, 'pending', 'implement', 'implementer', '{}')`, w.org)
			in := w.in
			in.Profile = "implementer"
			in.Capabilities.PublishCode = true
			in.Key = "blocked-by-workflow-writer"
			if _, err := w.s.StartChild(w.ctx, in); !IsRefusal(err) {
				t.Fatalf("workflow writer: %v", err)
			}
			w.exec(`UPDATE runs SET status = 'completed', ended_at = now() WHERE id = 'workflow_writer'`)
			w.exec(`INSERT INTO conductor_publishes (id, organization_id, task_id, run_id, request_id, message, workflow_run_id, attempt, branch, status)
				VALUES ('pub_life', $1, 'wi_life', 'run_parent', 'req_life', '', 'wf_life', 3, 'work', $2)`, w.org, livePublish)
			if _, err := w.s.StartChild(w.ctx, in); !IsRefusal(err) {
				t.Fatalf("live publish %s: %v", livePublish, err)
			}
		})
	}
}

func TestChildWriterBlocksWorkflowAndConductorPublication(t *testing.T) {
	w := newChildWorld(t)
	in := w.in
	in.Profile = "implementer"
	in.Capabilities.PublishCode = true
	in.Key = "writer"
	if _, err := w.s.StartChild(w.ctx, in); err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.CreatePhaseRun(w.ctx, w.org, PhaseRun{TaskID: "wi_life", Phase: PhaseImplement, Attempt: 3, Key: "workflow-writer"}); !errors.As(err, new(workflow.Wait)) {
		t.Fatalf("workflow writer: %v", err)
	}
	w.exec(`UPDATE workflow_runs SET state = state || '{"decider":"conductor"}'::jsonb WHERE id = 'wf_life'`)
	if err := w.s.DB.InOrg(w.ctx, w.org, func(tx pgx.Tx) error {
		if _, err := ConductPublish(w.ctx, tx, w.in.Parent.Run, "publish"); !IsRefusal(err) {
			return fmt.Errorf("conduct publish: %v", err)
		}
		if _, err := ConductStartPhase(w.ctx, tx, w.in.Parent.Run, StartPhase{Phase: PhaseReview}); !IsRefusal(err) {
			return fmt.Errorf("start phase: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRestartWriterAdmissionAbortsOldInReplacementTransaction(t *testing.T) {
	w := newChildWorld(t)
	in := w.in
	in.Profile = "implementer"
	in.Capabilities.PublishCode = true
	in.Key = "writer"
	old, err := w.s.StartChild(w.ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	next, err := w.s.RestartChild(w.ctx, w.org, "wi_life", old, "replace")
	if err != nil || next == old {
		t.Fatalf("restart %s: %v", next, err)
	}
	var oldControl, oldReason, replaced string
	if err := w.owner.QueryRow(w.ctx, `SELECT control::text, child_stop_reason, replaced_by FROM runs WHERE id = $1`, old).Scan(&oldControl, &oldReason, &replaced); err != nil {
		t.Fatal(err)
	}
	if oldControl != "abort" || oldReason != "restarted" || replaced != next {
		t.Fatalf("old reservation fence %s/%s/%s", oldControl, oldReason, replaced)
	}
	if _, err := w.s.RestartChild(w.ctx, w.org, "wi_life", old, "other"); !IsRefusal(err) {
		t.Fatalf("branched replacement: %v", err)
	}
}

func TestWriterChildHeadIsUntestedUntilReviewAtThatHead(t *testing.T) {
	w := newChildWorld(t)
	writer := w.child("writer-head")
	w.exec(`UPDATE runs SET phase='implement', child_profile='implementer', child_capabilities='{"publishCode":true}',
		status='completed', heads='{"repo":{"sha":"0123456789012345678901234567890123456789","base":"base","changedPaths":["main.go"]}}'
		WHERE id=$1`, writer)
	var untested bool
	if err := w.s.DB.InOrg(w.ctx, w.org, func(tx pgx.Tx) error {
		var err error
		untested, err = UntestedConductorHeadTx(w.ctx, tx, "wi_life", map[string]string{"repo": "0123456789012345678901234567890123456789"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if !untested {
		t.Fatal("writer child head was treated as tested")
	}
	w.exec(`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, base_refs)
		VALUES ('review_head', $1, 'prj_life', 'wi_life', 3, 'completed', 'review', 'reviewer', '{"repo":"0123456789012345678901234567890123456789"}')`, w.org)
	if err := w.s.DB.InOrg(w.ctx, w.org, func(tx pgx.Tx) error {
		var err error
		untested, err = UntestedConductorHeadTx(w.ctx, tx, "wi_life", map[string]string{"repo": "0123456789012345678901234567890123456789"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if untested {
		t.Fatal("review at writer child head did not clear gate")
	}
}

func TestChildStaleResults(t *testing.T) {
	for _, change := range []string{
		`UPDATE tasks SET status = 'done' WHERE id = 'wi_life'`,
		`UPDATE runs SET status = 'completed' WHERE id = 'run_parent'`,
		`UPDATE runs SET replaced_by = 'run_parent' WHERE id = 'run_parent'`,
		`UPDATE runs SET control = 'abort' WHERE id = 'run_parent'`,
		`UPDATE workflow_runs SET state = jsonb_set(state, '{attempt}', '4') WHERE id = 'wf_life'`,
	} {
		t.Run(change, func(t *testing.T) {
			w := newChildWorld(t)
			id := w.child("late")
			w.exec(change)
			before := w.snapshot()
			won, err := w.s.SettleChild(w.ctx, w.org, id, ChildOutcome{Status: "completed"})
			if err != nil || !won {
				t.Fatalf("settle: %v %v", won, err)
			}
			var stale bool
			if err := w.owner.QueryRow(w.ctx, `SELECT stale FROM child_results WHERE child_run_id = $1`, id).Scan(&stale); err != nil || !stale {
				t.Fatalf("late result stale=%v: %v", stale, err)
			}
			if w.count("child_result_outbox") != 0 {
				t.Fatal("stale result queued a wake")
			}
			if after := w.snapshot(); after != before {
				t.Fatal("late result changed workflow/PR")
			}
		})
	}
	// A valid pending result becomes non-actionable if the parent ends before consumption.
	w := newChildWorld(t)
	id := w.child("pending")
	if _, err := w.s.SettleChild(w.ctx, w.org, id, ChildOutcome{Status: "completed"}); err != nil {
		t.Fatal(err)
	}
	w.exec(`UPDATE runs SET status='completed' WHERE id='run_parent'`)
	w.exec(`UPDATE tasks SET status = 'done' WHERE id = 'wi_life'`)
	if consumed, err := w.s.ConsumeChildResult(w.ctx, w.org, "run_parent", id, 1); err != nil || consumed {
		t.Fatalf("ended receipt: %v %v", consumed, err)
	}
	var stale, discarded bool
	if err := w.owner.QueryRow(w.ctx, `SELECT r.stale, o.stale_at IS NOT NULL FROM child_results r
		JOIN child_result_outbox o USING (parent_run_id, child_run_id, generation) WHERE r.child_run_id = $1`, id).Scan(&stale, &discarded); err != nil || !stale || !discarded {
		t.Fatalf("pending result not fenced: %v %v %v", stale, discarded, err)
	}
}

func TestChildAbortAndRestart(t *testing.T) {
	w := newChildWorld(t)
	id, sibling := w.child("first"), w.child("sibling")
	before := w.snapshot()
	for i := 0; i < 2; i++ {
		if err := w.s.AbortChild(w.ctx, w.org, "wi_life", id); err != nil {
			t.Fatal(err)
		}
	}
	var control, status string
	if err := w.owner.QueryRow(w.ctx, `SELECT control::text, status::text FROM runs WHERE id = $1`, id).Scan(&control, &status); err != nil || control != "abort" || status != "pending" {
		t.Fatalf("abort request %s %s %v", control, status, err)
	}
	if err := w.owner.QueryRow(w.ctx, `SELECT control::text FROM runs WHERE id = $1`, sibling).Scan(&control); err != nil || control != "none" {
		t.Fatalf("sibling aborted: %s %v", control, err)
	}
	if err := w.s.AbortChild(w.ctx, w.org, "wrong-task", sibling); !IsRefusal(err) {
		t.Fatalf("wrong task: %v", err)
	}
	if err := w.s.AbortChild(w.ctx, w.org, "wi_life", "run_parent"); !IsRefusal(err) {
		t.Fatalf("workflow owner: %v", err)
	}
	var fresh string
	for i := 0; i < 2; i++ {
		next, err := (&Store{DB: w.s.DB}).RestartChild(w.ctx, w.org, "wi_life", id, "restart-key")
		if err != nil || next == id || i > 0 && next != fresh {
			t.Fatalf("restart %s %v", next, err)
		}
		fresh = next
	}
	var parent, profile, replaces, replacedBy string
	var generation int
	var capabilities ChildCapabilities
	if err := w.owner.QueryRow(w.ctx, `SELECT parent_run_id, child_profile, child_replaces, child_generation, child_capabilities FROM runs WHERE id = $1`, fresh).
		Scan(&parent, &profile, &replaces, &generation, &capabilities); err != nil {
		t.Fatal(err)
	}
	if parent != "run_parent" || profile != w.in.Profile || replaces != id || generation != 2 || capabilities != w.in.Capabilities {
		t.Fatal("restart lost lineage/policy")
	}
	if err := w.owner.QueryRow(w.ctx, `SELECT replaced_by FROM runs WHERE id = $1`, id).Scan(&replacedBy); err != nil || replacedBy != fresh {
		t.Fatalf("reverse lineage %s %v", replacedBy, err)
	}
	if _, err := w.s.RestartChild(w.ctx, w.org, "wi_life", id, "different-key"); !IsRefusal(err) {
		t.Fatalf("branched restart: %v", err)
	}
	if after := w.snapshot(); after != before {
		t.Fatal("abort/restart changed workflow/PR")
	}
	if won, err := w.s.SettleChild(w.ctx, w.org, id, ChildOutcome{Status: "completed"}); err != nil || !won {
		t.Fatalf("replaced child settlement %v %v", won, err)
	}
	var oldStale bool
	if err := w.owner.QueryRow(w.ctx, `SELECT stale FROM child_results WHERE child_run_id = $1`, id).Scan(&oldStale); err != nil || !oldStale {
		t.Fatalf("replaced child not stale: %v %v", oldStale, err)
	}
	if won, err := w.s.SettleChild(w.ctx, w.org, fresh, ChildOutcome{Status: "failed"}); err != nil || !won {
		t.Fatalf("replacement settlement %v %v", won, err)
	}
	if err := w.owner.QueryRow(w.ctx, `SELECT generation FROM child_results WHERE child_run_id = $1`, fresh).Scan(&generation); err != nil || generation != 2 {
		t.Fatalf("replacement result generation %d %v", generation, err)
	}
	if consumed, err := w.s.ConsumeChildResult(w.ctx, w.org, "run_parent", fresh, 2); err != nil || !consumed {
		t.Fatalf("replacement receipt %v %v", consumed, err)
	}
	w.exec(`UPDATE tasks SET status = 'aborted' WHERE id = 'wi_life'`)
	before = w.snapshot()
	for i := 0; i < 2; i++ {
		if err := (&Store{DB: w.s.DB}).EndChildParent(w.ctx, w.org, "wi_life", "run_parent"); err != nil {
			t.Fatal(err)
		}
	}
	var reason string
	if err := w.owner.QueryRow(w.ctx, `SELECT child_stop_reason, control::text FROM runs WHERE id = $1`, sibling).Scan(&reason, &control); err != nil || reason != "parent-ended" || control != "abort" {
		t.Fatalf("parent end %s %s %v", reason, control, err)
	}
	if _, err := w.s.RestartChild(w.ctx, w.org, "wi_life", fresh, "ended"); !IsRefusal(err) {
		t.Fatalf("ended restart: %v", err)
	}
	in := w.in
	in.Key = "ended"
	if _, err := w.s.StartChild(w.ctx, in); !IsRefusal(err) {
		t.Fatalf("ended admission: %v", err)
	}
	if after := w.snapshot(); after != before {
		t.Fatal("parent end changed workflow/PR")
	}
	if won, err := w.s.SettleChild(w.ctx, w.org, sibling, ChildOutcome{Status: "aborted"}); err != nil || !won {
		t.Fatalf("parent-ended collect: %v %v", won, err)
	}
	if w.count("child_result_outbox") != 1 {
		t.Fatal("parent-ended child woke parent")
	}
}

func TestChildSettlementOwnership(t *testing.T) {
	w := newChildWorld(t)
	id := w.child("owned")
	for _, bad := range []struct{ org, id string }{{w.org, "run_parent"}, {dbtest.Org(t, w.owner), id}} {
		if _, err := w.s.SettleChild(w.ctx, bad.org, bad.id, ChildOutcome{Status: "completed"}); !IsRefusal(err) {
			t.Fatalf("ownership: %v", err)
		}
	}
	if _, err := w.s.SettleChild(w.ctx, w.org, id, ChildOutcome{Status: "running"}); !IsRefusal(err) {
		t.Fatalf("invalid outcome: %v", err)
	}
	if w.count("child_results") != 0 || w.count("child_result_outbox") != 0 {
		t.Fatal("refused writes")
	}
	// Outbox receipt cannot be redirected to a request-supplied parent.
	if _, err := w.s.SettleChild(w.ctx, w.org, id, ChildOutcome{Status: "failed"}); err != nil {
		t.Fatal(err)
	}
	if consumed, err := w.s.ConsumeChildResult(w.ctx, w.org, "forged", id, 1); err != nil || consumed {
		t.Fatalf("forged receipt %v %v", consumed, err)
	}
}

func TestChildResultBounds(t *testing.T) {
	w := newChildWorld(t)
	id := w.child("bounded")
	w.exec(`INSERT INTO artifacts (id, organization_id, run_id, kind, name, storage_key, sha256)
		SELECT 'art_bound_' || n, $1, $2, 'published', 'report', 'bound_' || n, 'x' FROM generate_series(1, 105) n`, w.org, id)
	w.exec(`UPDATE runs SET heads = '{"repo":"secret-not-a-commit","secret/key":"0123456789012345678901234567890123456789"}' WHERE id = $1`, id)
	if _, err := w.s.SettleChild(w.ctx, w.org, id, ChildOutcome{Status: "completed"}); err != nil {
		t.Fatal(err)
	}
	var refs map[string]string
	var artifacts []string
	if err := w.owner.QueryRow(w.ctx, `SELECT commits, artifact_ids FROM child_results WHERE child_run_id = $1`, id).Scan(&refs, &artifacts); err != nil {
		t.Fatal(err)
	}
	if len(refs) != 0 || len(artifacts) != 100 {
		t.Fatalf("unbounded/unsafe result %v %d", refs, len(artifacts))
	}
}

func TestChildAtomicEnqueue(t *testing.T) {
	w := newChildWorld(t)
	id := w.child("transport")
	w.exec(`UPDATE runs SET heads='{"repo":{"sha":"0123456789012345678901234567890123456789","branch":"work"}}' WHERE id=$1`, id)
	if _, err := w.s.SettleChild(w.ctx, w.org, id, ChildOutcome{Status: "completed"}); err != nil {
		t.Fatal(err)
	}
	before := w.snapshot()
	type answer struct {
		n   int
		err error
	}
	results := make(chan answer, 2)
	for i := 0; i < 2; i++ {
		go func() { n, err := (&Store{DB: w.s.DB}).EnqueueChildResults(w.ctx); results <- answer{n, err} }()
	}
	wins := 0
	for i := 0; i < 2; i++ {
		r := <-results
		if r.err != nil {
			t.Fatal(r.err)
		}
		wins += r.n
	}
	if wins != 1 {
		t.Fatalf("enqueues %d, want 1", wins)
	}
	var claimed int
	if err := w.s.DB.InSystem(w.ctx, "phase-sync", func(tx pgx.Tx) error {
		return tx.QueryRow(w.ctx, `SELECT count(*) FROM child_result_outbox WHERE claimed_by IS NOT NULL`).Scan(&claimed)
	}); err != nil {
		t.Fatal(err)
	}
	if claimed != 0 {
		t.Fatalf("workers left %d overlapping claims", claimed)
	}
	var directives, receipts int
	if err := w.owner.QueryRow(w.ctx, `SELECT count(*),count(*) FILTER (WHERE consumed_at IS NOT NULL AND directive_id IS NOT NULL) FROM child_result_outbox`).Scan(&directives, &receipts); err != nil {
		t.Fatal(err)
	}
	if directives != 1 || receipts != 1 || w.count("directives") != 1 {
		t.Fatalf("outbox %d receipts %d directives %d", directives, receipts, w.count("directives"))
	}
	if n, err := (&Store{DB: w.s.DB}).EnqueueChildResults(w.ctx); err != nil || n != 0 {
		t.Fatalf("restart enqueue %d %v", n, err)
	}
	var text string
	var commits map[string]string
	if err := w.owner.QueryRow(w.ctx, `SELECT d.text,r.commits FROM child_result_outbox o JOIN directives d ON d.id=o.directive_id JOIN child_results r USING(parent_run_id,child_run_id,generation)`).Scan(&text, &commits); err != nil {
		t.Fatal(err)
	}
	if commits["repo"] != "0123456789012345678901234567890123456789" {
		t.Fatalf("lost object-valued head %v", commits)
	}
	var summary []byte
	if err := w.owner.QueryRow(w.ctx, `SELECT jsonb_build_object('childId',child_run_id,'profile',profile,'status',status,'findings',findings,'verdicts',verdicts,'commits',commits,'artifactIds',artifact_ids,'modelCostUsd',cost_usd,'stale',stale) FROM child_results`).Scan(&summary); err != nil {
		t.Fatal(err)
	}
	want, err := childResultText(summary)
	if err != nil || text != want {
		t.Fatalf("directive mismatch %q %v", text, err)
	}
	if w.snapshot() != before {
		t.Fatal("notification mutated workflow/PR/task")
	}
}

func TestChildEnqueueClaimsDisjointBatches(t *testing.T) {
	w := newChildWorld(t)
	for i := 0; i < 2; i++ {
		id := w.child(fmt.Sprintf("claim-%d", i))
		if _, err := w.s.SettleChild(w.ctx, w.org, id, ChildOutcome{Status: "completed"}); err != nil {
			t.Fatal(err)
		}
	}
	claim := func() ([]string, error) {
		var ids []string
		err := w.s.DB.InSystem(w.ctx, "phase-sync", func(tx pgx.Tx) error {
			token := fmt.Sprintf("claim-%d", len(ids))
			rows, err := tx.Query(w.ctx, `UPDATE child_result_outbox SET claimed_by=$1,claimed_until=now()+interval '1 minute'
				WHERE (parent_run_id,child_run_id,generation) IN (
					SELECT parent_run_id,child_run_id,generation FROM child_result_outbox
					WHERE consumed_at IS NULL AND stale_at IS NULL AND (claimed_until IS NULL OR claimed_until < now())
					ORDER BY created_at LIMIT 1 FOR UPDATE SKIP LOCKED
				) RETURNING child_run_id`, token)
			if err != nil {
				return err
			}
			ids, err = pgx.CollectRows(rows, pgx.RowTo[string])
			return err
		})
		return ids, err
	}
	first, err := claim()
	if err != nil {
		t.Fatal(err)
	}
	second, err := claim()
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 || len(second) != 1 || first[0] == second[0] {
		t.Fatalf("overlapping claims: %v %v", first, second)
	}
}

func TestChildNotificationStaysWithParent(t *testing.T) {
	w := newChildWorld(t)
	id := w.child("immutable")
	if _, err := w.s.SettleChild(w.ctx, w.org, id, ChildOutcome{Status: "completed"}); err != nil {
		t.Fatal(err)
	}
	w.exec(`UPDATE runs SET status='paused',dude_pause=NULL WHERE id='run_parent'`)
	if n, err := w.s.EnqueueChildResults(w.ctx); err != nil || n != 1 {
		t.Fatalf("paused enqueue %d %v", n, err)
	}
	var control string
	if err := w.owner.QueryRow(w.ctx, `SELECT control::text FROM runs WHERE id='run_parent'`).Scan(&control); err != nil || control != "none" {
		t.Fatalf("person pause changed %s %v", control, err)
	}
	w.exec(`UPDATE runs SET status='completed' WHERE id='run_parent'`)
	w.exec(`INSERT INTO runs(id,organization_id,project_id,task_id,attempt,role,status) VALUES('run_new_parent',$1,'prj_life','wi_life',3,'conductor','running')`, w.org)
	if err := w.s.DB.InOrg(w.ctx, w.org, func(tx pgx.Tx) error {
		if err := LockChat(w.ctx, tx, "wi_life"); err != nil {
			return err
		}
		_, err := HandOver(w.ctx, tx, w.in.Parent.Run, true)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := w.owner.QueryRow(w.ctx, `SELECT count(*) FROM directives WHERE run_id='run_new_parent'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("retargeted directives %d %v", n, err)
	}
	if n, err := w.s.EnqueueChildResults(w.ctx); err != nil || n != 0 {
		t.Fatalf("requeued %d %v", n, err)
	}
}

func TestChildHandOverFencesFamily(t *testing.T) {
	w := newChildWorld(t)
	id := w.child("handover")
	w.exec(`UPDATE runs SET status='failed' WHERE id='run_parent'`)
	if err := w.s.DB.InOrg(w.ctx, w.org, func(tx pgx.Tx) error {
		if err := LockChat(w.ctx, tx, "wi_life"); err != nil {
			return err
		}
		_, err := HandOver(w.ctx, tx, w.in.Parent.Run, true)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var reason string
	if err := w.owner.QueryRow(w.ctx, `SELECT COALESCE(child_stop_reason,'') FROM runs WHERE id=$1`, id).Scan(&reason); err != nil || reason != "parent-ended" {
		t.Fatalf("handover left child live: %q %v", reason, err)
	}
}

func TestChildParentFenceIsFamilySelective(t *testing.T) {
	w := newChildWorld(t)
	w.exec(`UPDATE runs SET status='completed' WHERE id='run_parent'`)
	w.exec(`INSERT INTO projects(id,organization_id,name,slug,key_prefix) VALUES('prj_other',$1,'Q','q','Q')`, w.org)
	w.exec(`INSERT INTO tasks(id,organization_id,project_id,number,title,goal,status) VALUES
		('wi_two',$1,'prj_other',1,'Two','G','running')`, w.org)
	w.exec(`INSERT INTO runs(id,organization_id,project_id,task_id,attempt,role,status) VALUES
		('parent_two',$1,'prj_other','wi_two',1,'conductor','completed')`, w.org)
	w.exec(`INSERT INTO runs(id,organization_id,project_id,task_id,attempt,role,status) VALUES
		('parent_same_task',$1,'prj_life','wi_life',1,'conductor','completed')`, w.org)
	w.exec(`INSERT INTO runs(id,organization_id,project_id,task_id,attempt,status,phase,role,source_kind,parent_run_id,child_profile,child_capabilities) VALUES
		('child_one',$1,'prj_life','wi_life',1,'running','investigate','investigator','task_conductor','run_parent','investigator','{}'),
		('child_same_task',$1,'prj_life','wi_life',1,'running','investigate','investigator','task_conductor','parent_same_task','investigator','{}'),
		('child_two',$1,'prj_other','wi_two',1,'running','investigate','investigator','task_conductor','parent_two','investigator','{}'),
		('child_unrelated',$1,'prj_other','wi_two',1,'running','investigate','investigator','task_conductor','parent_two','investigator','{}')`, w.org)
	if err := w.s.EndChildParent(w.ctx, w.org, "wi_life", "run_parent"); err != nil {
		t.Fatal(err)
	}
	var one, same, two, unrelated string
	if err := w.owner.QueryRow(w.ctx, `SELECT child_stop_reason FROM runs WHERE id='child_one'`).Scan(&one); err != nil {
		t.Fatal(err)
	}
	if err := w.owner.QueryRow(w.ctx, `SELECT COALESCE(child_stop_reason,'') FROM runs WHERE id='child_same_task'`).Scan(&same); err != nil {
		t.Fatal(err)
	}
	if err := w.owner.QueryRow(w.ctx, `SELECT COALESCE(child_stop_reason,''),COALESCE((SELECT child_stop_reason FROM runs WHERE id='child_unrelated'),'') FROM runs WHERE id='child_two'`).Scan(&two, &unrelated); err != nil {
		t.Fatal(err)
	}
	if one != "parent-ended" || same != "" || two != "" || unrelated != "" {
		t.Fatalf("family fence %q %q %q %q", one, same, two, unrelated)
	}
}

func TestChildTransportRecovery(t *testing.T) {
	for _, state := range []string{"expired", "stale", "rollback"} {
		t.Run(state, func(t *testing.T) {
			w := newChildWorld(t)
			id := w.child("recover")
			if _, err := w.s.SettleChild(w.ctx, w.org, id, ChildOutcome{Status: "completed"}); err != nil {
				t.Fatal(err)
			}
			switch state {
			case "expired":
				w.exec(`UPDATE child_result_outbox SET claimed_by='dead-worker',claimed_until=now()-interval '1 minute'`)
			case "stale":
				w.exec(`UPDATE runs SET replaced_by=id WHERE id='run_parent'`)
			case "rollback":
				w.exec(`CREATE FUNCTION reject_child_receipt() RETURNS trigger LANGUAGE plpgsql AS 'BEGIN RAISE EXCEPTION ''receipt unavailable''; END'`)
				w.exec(`CREATE TRIGGER reject_receipt BEFORE UPDATE OF consumed_at ON child_result_outbox FOR EACH ROW EXECUTE FUNCTION reject_child_receipt()`)
			}
			n, err := (&Store{DB: w.s.DB}).EnqueueChildResults(w.ctx)
			if state == "rollback" {
				if err == nil || w.count("directives") != 0 {
					t.Fatalf("rollback %d %v directives %d", n, err, w.count("directives"))
				}
				var leased bool
				if err := w.owner.QueryRow(w.ctx, `SELECT consumed_at IS NULL AND claimed_by IS NOT NULL AND claimed_until > now() FROM child_result_outbox`).Scan(&leased); err != nil || !leased {
					t.Fatalf("partial receipt lease %v %v", leased, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := 1
			if state == "stale" {
				want = 0
			}
			if n != want || w.count("directives") != want {
				t.Fatalf("%s enqueues %d directives %d", state, n, w.count("directives"))
			}
		})
	}
}

func TestChildEndedParentRestart(t *testing.T) {
	w := newChildWorld(t)
	id := w.child("ended-parent")
	if err := w.s.EndChildParent(w.ctx, w.org, "wi_life", "run_parent"); !IsRefusal(err) {
		t.Fatalf("live parent end: %v", err)
	}
	w.exec(`UPDATE runs SET status = 'completed' WHERE id = 'run_parent'`)
	if _, err := w.s.RestartChild(w.ctx, w.org, "wi_life", id, "restart"); !IsRefusal(err) {
		t.Fatalf("ended parent restart: %v", err)
	}
	if err := w.s.EndChildParent(w.ctx, w.org, "wi_life", "run_parent"); err != nil {
		t.Fatal(err)
	}
}
