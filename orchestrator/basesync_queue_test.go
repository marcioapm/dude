package orchestrator_test

// A person's queued "Resolve conflicts" against the writer lifecycle and
// the pull request syncer: every window between a request and a capable
// writer delivers it once (in the writer's submitted prompt or as a
// directive), and only the forge's ancestry reading of the head a sync
// records completes it.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

// heldConflict is a task whose pull request conflicts with a moved base,
// its delivery held on a signal nothing sends: pumps run its Runs, never
// its workflow, so the only writers are the ones a test makes.
func (w *world) heldConflict() (wi, prID, owner string) {
	w.t.Helper()
	w.toolsWithSync()
	wi = w.reviewing()
	w.tell()
	w.conflictWithMovedBase()
	w.sync()
	// A real model: lux is given the writer's own prompt, not a script.
	mustExec(w.t, w.owner, `UPDATE projects SET agent_models = '{}'::jsonb WHERE id = $1`, w.project)
	w.onModel("implementer", "claude-impl")
	mustExec(w.t, w.owner, `UPDATE workflow_runs SET step='awaitFix', status='waiting', awaiting_signals='["queue_test_hold"]',
		wake_at=NULL, state=state-'escalation' WHERE task_id=$1`, wi)
	return wi, w.prOf(wi), w.person("owner")
}

// queueResolve is the owner's Resolve conflicts, accepted; the base SHA
// it queued.
func (w *world) queueResolve(owner, prID string) string {
	w.t.Helper()
	code, body := w.callAs(owner, "/internal/pull-requests/"+prID+"/resolve-conflicts", map[string]any{})
	if code != 200 || body["status"] != "queued" {
		w.t.Fatalf("resolve: %d %v", code, body)
	}
	sha, _ := body["baseSha"].(string)
	if sha == "" {
		w.t.Fatalf("resolve named no base: %v", body)
	}
	return sha
}

// freshFixer is a new fix Run of the task, pending: the next writer.
func (w *world) freshFixer(wi, key string) string {
	w.t.Helper()
	id, err := (&delivery.Store{DB: w.app}).CreatePhaseRun(context.Background(), w.org,
		delivery.PhaseRun{TaskID: wi, Phase: delivery.PhaseFix, Key: key})
	if err != nil {
		w.t.Fatal(err)
	}
	return id
}

// submittedPrompt is the prompt lux was given for runID's lux Run.
func (w *world) submittedPrompt(luxRunID string) string {
	w.t.Helper()
	var spec struct {
		Workload struct {
			Prompt string `json:"prompt"`
		} `json:"workload"`
	}
	if err := json.Unmarshal(w.lux.SpecOf(luxRunID), &spec); err != nil {
		w.t.Fatal(err)
	}
	return spec.Workload.Prompt
}

// heardQueued counts how often lux was handed runID's agent the queued
// instruction for sha after its prompt: as a directive's input (one per
// directive id: lux drops a repeat) or as a resume's input.
func (w *world) heardQueued(runID, luxRunID, sha string) int {
	w.t.Helper()
	rows, err := w.owner.Query(context.Background(), `SELECT id FROM directives WHERE run_id=$1`, runID)
	if err != nil {
		w.t.Fatal(err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		w.t.Fatal(err)
	}
	n := 0
	for _, id := range ids {
		for _, body := range w.lux.InputBodies(luxRunID, id) {
			if strings.Contains(body, "Queued conflict resolution") && strings.Contains(body, sha) {
				n++
				break
			}
		}
	}
	for _, in := range w.lux.ResumeInputs(luxRunID) {
		if strings.Contains(in, "Queued conflict resolution") && strings.Contains(in, sha) {
			n++
		}
	}
	return n
}

func (w *world) baseSyncDirectives(runID, sha string) int {
	return w.count(`SELECT count(*) FROM directives WHERE run_id=$1 AND text LIKE '%Queued conflict resolution%' AND text LIKE '%' || $2 || '%'`, runID, sha)
}

// A request accepted after a writer was submitted, while lux has not yet
// started it (scheduled), reaches that writer as a directive once it runs.
func TestAQueuedRequestReachesAScheduledWriter(t *testing.T) {
	w := newWorld(t)
	wi, prID, owner := w.heldConflict()
	w.lux.Decide = func(map[string]any) fakelux.Behaviour { return fakelux.Behaviour{Hang: true} }
	release := make(chan struct{})
	var releaseOnce sync.Once
	free := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(free)
	w.lux.BeforeStart(func(string) {
		select {
		case <-release:
		case <-time.After(30 * time.Second):
		}
	})
	fixer := w.freshFixer(wi, "queue-scheduled")
	w.until("the writer submitted and held before it runs", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id=$1 AND status='scheduled'`, fixer) == 1
	})
	lr := w.luxRunOf(fixer)
	if strings.Contains(w.submittedPrompt(lr), "Queued conflict resolution") {
		t.Fatal("nothing was queued when the prompt was built")
	}
	sha := w.queueResolve(owner, prID)
	if n := w.baseSyncDirectives(fixer, sha); n != 1 {
		t.Fatalf("directives for the scheduled writer %d, want 1", n)
	}
	w.queueResolve(owner, prID)
	if n := w.baseSyncDirectives(fixer, sha); n != 1 {
		t.Fatalf("a repeated request queued %d directives, want 1", n)
	}
	free()
	w.until("the running writer hears the request", func() bool { return w.heardQueued(fixer, lr, sha) == 1 })
	if n := w.count(integrationRuns, wi); n != 0 {
		t.Fatalf("the request started %d integration Runs", n)
	}
}

// A request committed while a fresh writer's spec claims what its prompt
// carries is either in that prompt or delivered as a directive, never only
// marked as the writer's.
func TestARequestDuringAWritersPromptClaimIsDelivered(t *testing.T) {
	w := newWorld(t)
	wi, prID, owner := w.heldConflict()
	w.lux.Decide = func(map[string]any) fakelux.Behaviour { return fakelux.Behaviour{Hang: true} }
	first := w.queueResolve(owner, prID)
	fixer := w.freshFixer(wi, "queue-claim")
	var second string
	done := make(chan struct{})
	var once sync.Once
	w.syncer.BaseSyncClaim = func(ctx context.Context, tx pgx.Tx, runID string) {
		once.Do(func() {
			var pid int
			if err := tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
				t.Error(err)
				return
			}
			w.gh.AdvanceBase("main", "Base moves while the writer's prompt is built")
			go func() {
				defer close(done)
				code, body := w.callAs(owner, "/internal/pull-requests/"+prID+"/resolve-conflicts", map[string]any{})
				if code != 200 {
					t.Errorf("second resolve: %d %v", code, body)
				}
				second, _ = body["baseSha"].(string)
			}()
			// Barrier: the request has committed, or waits on this claim.
			deadline := time.Now().Add(20 * time.Second)
			for time.Now().Before(deadline) {
				select {
				case <-done:
					return
				default:
				}
				var n int
				if err := w.owner.QueryRow(ctx, blockedBy, pid).Scan(&n); err == nil && n > 0 {
					return
				}
				time.Sleep(2 * time.Millisecond)
			}
			t.Error("the concurrent request neither committed nor waited on the claim")
		})
	}
	w.until("the writer submitted", func() bool { return w.luxRunOf(fixer) != "" })
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("the concurrent request never finished")
	}
	if second == "" || second == first {
		t.Fatalf("second base %q, first %q", second, first)
	}
	lr := w.luxRunOf(fixer)
	prompt := w.submittedPrompt(lr)
	if !strings.Contains(prompt, "Queued conflict resolution") || !strings.Contains(prompt, first) {
		t.Fatalf("the writer's prompt lacks the first request:\n%s", prompt)
	}
	inPrompt := strings.Contains(prompt, second)
	directives := w.baseSyncDirectives(fixer, second)
	if inPrompt == (directives == 1) || directives > 1 {
		t.Fatalf("second request: in prompt %v, directives %d; want exactly one of them", inPrompt, directives)
	}
	if !inPrompt {
		w.until("the writer hears the second request", func() bool { return w.heardQueued(fixer, lr, second) == 1 })
	}
	if n := w.baseSyncDirectives(fixer, first); n != 0 {
		t.Fatalf("the prompted request was also sent as %d directives", n)
	}
}

// A writer resumed (a continuation, a person's resume) is sent no prompt:
// a request no live writer was told of reaches it as a directive, once,
// and is not marked as its without that.
func TestAResumedWriterHearsAQueuedRequest(t *testing.T) {
	w := newWorld(t)
	wi, prID, owner := w.heldConflict()
	w.lux.Decide = func(map[string]any) fakelux.Behaviour { return fakelux.Behaviour{Hang: true} }
	fixer := w.freshFixer(wi, "queue-resume")
	w.until("the writer runs", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id=$1 AND status='running' AND lux_state='running'`, fixer) == 1
	})
	if status, body := w.call("/internal/runs/"+fixer+"/pause", map[string]any{}); status != 200 {
		t.Fatalf("pause: %d %v", status, body)
	}
	w.until("the writer paused", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id=$1 AND status='paused'`, fixer) == 1
	})
	// Accepted while no writer was live (say, during review, before this
	// continuation existed): pending, told to no one.
	sha := w.gh.SHA("main")
	mustExec(t, w.owner, `INSERT INTO base_sync_requests(organization_id,task_id,pull_request_id,base,base_sha)
		VALUES($1,$2,$3,'main',$4)`, w.org, wi, prID, sha)
	if status, body := w.call("/internal/runs/"+fixer+"/resume", map[string]any{}); status != 200 {
		t.Fatalf("resume: %d %v", status, body)
	}
	lr := w.luxRunOf(fixer)
	w.until("the resumed writer hears the request", func() bool { return w.heardQueued(fixer, lr, sha) == 1 })
	if n := w.count(`SELECT count(*) FROM base_sync_requests WHERE base_sha=$1 AND writer_run_id=$2 AND completed_at IS NULL`, sha, fixer); n != 1 {
		t.Fatalf("request not marked as the resumed writer's: %d", n)
	}
	w.queueResolve(owner, prID)
	for range 3 {
		w.pump()
	}
	if n := w.baseSyncDirectives(fixer, sha); n != 1 {
		t.Fatalf("directives %d after a repeated request, want 1", n)
	}
}

// A request on a pull request of an earlier attempt, which the live
// delivery no longer tracks, is not given to the new attempt's writer.
func TestAnEarlierAttemptsRequestIsNotBriefedToANewWriter(t *testing.T) {
	w := newWorld(t)
	wi, prID, owner := w.heldConflict()
	w.lux.Decide = func(map[string]any) fakelux.Behaviour { return fakelux.Behaviour{Hang: true} }
	current := w.queueResolve(owner, prID)
	mustExec(t, w.owner, `INSERT INTO pull_requests (id, organization_id, project_id, task_id, repository_id, number, url,
		head_branch, base_branch, head_sha, title, state, checks) SELECT 'pr_earlier', organization_id, project_id, task_id,
		repository_id, 99, 'https://x/99', 'dude/old/attempt-1', 'main', head_sha, 'old', 'closed', 'passing'
		FROM pull_requests WHERE id = $1`, prID)
	mustExec(t, w.owner, `INSERT INTO base_sync_requests(organization_id,task_id,pull_request_id,base,base_sha)
		VALUES($1,$2,'pr_earlier','main','0ld0ba5e')`, w.org, wi)
	fixer := w.freshFixer(wi, "queue-attempt")
	w.until("the writer submitted", func() bool { return w.luxRunOf(fixer) != "" })
	prompt := w.submittedPrompt(w.luxRunOf(fixer))
	if !strings.Contains(prompt, current) {
		t.Fatalf("the current pull request's request is missing:\n%s", prompt)
	}
	if strings.Contains(prompt, "0ld0ba5e") {
		t.Fatalf("an earlier attempt's request reached the new writer:\n%s", prompt)
	}
	if n := w.count(`SELECT count(*) FROM base_sync_requests WHERE pull_request_id='pr_earlier' AND writer_run_id IS NULL`); n != 1 {
		t.Fatalf("the earlier attempt's request was claimed: %d", n)
	}
}

// A sync that read a public head containing the base, then lost the race
// to record it to a sync of a newer head without the base, completes
// nothing: completion commits only with the head a sync records.
func TestAnObsoleteSyncDoesNotCompleteARequest(t *testing.T) {
	w := newWorld(t)
	wi, prID, _ := w.heldConflict()
	ctx := context.Background()
	pull := w.gh.Pull(1)
	without := w.gh.SHA(pull.Head)
	base := w.gh.SHA("main")
	mustExec(t, w.owner, `INSERT INTO base_sync_requests(organization_id,task_id,pull_request_id,base,base_sha)
		VALUES($1,$2,$3,'main',$4)`, w.org, wi, prID, base)
	gitOut(t, w.gh.Repo, "branch", "-f", pull.Head, "main") // H1 contains the base
	compareHeld, compareGo := make(chan struct{}), make(chan struct{})
	var holds atomic.Int32
	w.gh.Set(func(s *fakegithub.Server) {
		s.Intercept = func(r *http.Request) int {
			if strings.Contains(r.URL.Path, "/compare/"+base+"...") && holds.Add(1) == 1 {
				close(compareHeld)
				<-compareGo
			}
			return 0
		}
	})
	done := make(chan error, 1)
	go func() { done <- w.prs.Sync(ctx, w.org, prID) }()
	select {
	case <-compareHeld:
	case <-time.After(20 * time.Second):
		t.Fatal("the first sync never compared")
	}
	// A force-push of a head without the base, recorded by another sync.
	gitOut(t, w.gh.Repo, "branch", "-f", pull.Head, without)
	if err := w.prs.Sync(ctx, w.org, prID); err != nil {
		t.Fatal(err)
	}
	close(compareGo)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the first sync hung")
	}
	if got := w.str(`SELECT head_sha FROM pull_requests WHERE id=$1`, prID); got != without {
		t.Fatalf("recorded head %s, want %s", got, without)
	}
	if n := w.count(`SELECT count(*) FROM base_sync_requests WHERE pull_request_id=$1 AND completed_at IS NULL`, prID); n != 1 {
		t.Fatalf("a request completed against a head the pull request no longer has: pending %d", n)
	}
}

// One compare per (base, public head): syncs of an unchanged head do not
// ask again, a failed compare is retried, a new head is compared afresh.
func TestAncestryIsComparedOncePerHead(t *testing.T) {
	w := newWorld(t)
	wi, prID, _ := w.heldConflict()
	ctx := context.Background()
	base := w.gh.SHA("main")
	mustExec(t, w.owner, `INSERT INTO base_sync_requests(organization_id,task_id,pull_request_id,base,base_sha)
		VALUES($1,$2,$3,'main',$4)`, w.org, wi, prID, base)
	var compares, failures atomic.Int32
	failures.Store(1)
	w.gh.Set(func(s *fakegithub.Server) {
		s.Intercept = func(r *http.Request) int {
			if strings.Contains(r.URL.Path, "/compare/"+base+"...") {
				compares.Add(1)
				if failures.Add(-1) >= 0 {
					return http.StatusBadGateway
				}
			}
			return 0
		}
	})
	sync := func() {
		t.Helper()
		if err := w.prs.Sync(ctx, w.org, prID); err != nil {
			t.Fatal(err)
		}
	}
	sync() // the forge fails: kept, and asked again next time
	sync()
	for range 5 {
		sync()
	}
	if n := compares.Load(); n != 2 {
		t.Fatalf("compares %d over seven syncs of one head, want 2 (one failed, one read)", n)
	}
	if n := w.count(`SELECT count(*) FROM base_sync_requests WHERE pull_request_id=$1 AND completed_at IS NULL`, prID); n != 1 {
		t.Fatalf("pending %d", n)
	}
	gitOut(t, w.gh.Repo, "branch", "-f", w.gh.Pull(1).Head, "main")
	sync()
	if n := compares.Load(); n != 3 {
		t.Fatalf("compares %d after the head moved, want 3", n)
	}
	if n := w.count(`SELECT count(*) FROM base_sync_requests WHERE pull_request_id=$1 AND completed_at IS NOT NULL`, prID); n != 1 {
		t.Fatalf("the integrated base was not completed: %d", n)
	}
}

// A sync recording its ancestry readings while a fresh writer claims the
// same requests for its prompt: both lock them in the queue's order, so
// neither is chosen as a deadlock victim and the writer starts.
func TestASyncAndAWritersClaimDoNotDeadlock(t *testing.T) {
	w := newWorld(t)
	wi, prID, _ := w.heldConflict()
	ctx := context.Background()
	w.lux.Decide = func(map[string]any) fakelux.Behaviour { return fakelux.Behaviour{Hang: true} }
	head := w.gh.SHA(w.gh.Pull(1).Head)
	// The earlier request is of the moved base, not in the head; the later
	// one of a base the head contains. A sync reads them integrated-first.
	moved, contained := w.gh.SHA("main"), gitOut(t, w.gh.Repo, "merge-base", head, "main")
	mustExec(t, w.owner, `INSERT INTO base_sync_requests(organization_id,task_id,pull_request_id,base,base_sha,created_at)
		VALUES($1,$2,$3,'main',$4,now()-interval '2 minutes'), ($1,$2,$3,'main',$5,now()-interval '1 minute')`,
		w.org, wi, prID, moved, contained)
	fixer := w.freshFixer(wi, "queue-deadlock")
	var once sync.Once
	var held atomic.Bool
	swept := make(chan error, 1)
	w.prs.BaseSyncLocked = func(ctx context.Context, tx pgx.Tx) {
		once.Do(func() {
			held.Store(true)
			var pid int
			if err := tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
				t.Error(err)
				return
			}
			go func() {
				_, err := w.syncer.Sweep(context.Background())
				swept <- err
			}()
			// Barrier: the writer's claim waits on this sync's lock.
			deadline := time.Now().Add(20 * time.Second)
			for time.Now().Before(deadline) {
				var n int
				if err := w.owner.QueryRow(ctx, blockedBy, pid).Scan(&n); err == nil && n > 0 {
					return
				}
				time.Sleep(2 * time.Millisecond)
			}
			t.Error("the writer's claim never waited on the sync")
		})
	}
	if err := w.prs.Sync(ctx, w.org, prID); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if !held.Load() {
		t.Fatal("the sync never held its requests' locks")
	}
	select {
	case err := <-swept:
		if err != nil {
			t.Fatalf("sweep: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the writer's sweep never finished")
	}
	w.until("the writer submitted or failed", func() bool {
		return w.luxRunOf(fixer) != "" || w.count(`SELECT count(*) FROM runs WHERE id=$1 AND status='failed'`, fixer) == 1
	})
	if got := w.str(`SELECT status::text || ' ' || COALESCE(error, '') FROM runs WHERE id=$1`, fixer); strings.HasPrefix(got, "failed") ||
		strings.Contains(got, "deadlock") || strings.Contains(got, "40P01") {
		t.Fatalf("the writer: %s", got)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id=$1 AND (retry_failures > 0 OR status='failed')`, fixer); n != 0 {
		t.Fatalf("the writer's start failed %d times", n)
	}
	if prompt := w.submittedPrompt(w.luxRunOf(fixer)); !strings.Contains(prompt, moved) || strings.Contains(prompt, contained) {
		t.Fatalf("the writer's prompt carries the wrong requests:\n%s", prompt)
	}
	if n := w.count(`SELECT count(*) FROM base_sync_requests WHERE base_sha=$1 AND completed_at IS NOT NULL AND compared_head=$2`, contained, head); n != 1 {
		t.Fatalf("the contained base was not completed: %d", n)
	}
}
