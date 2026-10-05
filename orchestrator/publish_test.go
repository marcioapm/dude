package orchestrator_test

// A conductor's publish as a durable operation (design: the conductor,
// step 6): reserved before the task branch moves, fencing writers and
// the conductor's replacement while it moves, each repository's move kept
// as it happens and reconciled with the forge on a retry, and carried on
// by its own worker, with back-off, apart from the phase sweep.

import (
	"context"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
)

// gate blocks the fake GitHub's requests matching match until opened,
// telling held when the first arrives.
type gate struct {
	held, open chan struct{}
	once, shut sync.Once
}

func newGate() *gate { return &gate{held: make(chan struct{}), open: make(chan struct{})} }

func (g *gate) wait() {
	g.once.Do(func() { close(g.held) })
	<-g.open
}

func (g *gate) release() { g.shut.Do(func() { close(g.open) }) }

func isCompare(r *http.Request) bool { return strings.Contains(r.URL.Path, "/compare/") }

func isMove(r *http.Request) bool {
	return r.Method == http.MethodPatch && strings.Contains(r.URL.Path, "/git/refs/heads/")
}

// settleInBackground runs the publish worker on its own until the test
// ends, as main does: the test's pump no longer carries publishes.
func (e *editing) settleInBackground() {
	e.noPublishes = true
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	e.t.Cleanup(func() { cancel(); <-done })
	go func() {
		defer close(done)
		for ctx.Err() == nil {
			_, _ = e.syncer.SettlePublishes(ctx)
			select {
			case <-ctx.Done():
			case <-time.After(20 * time.Millisecond):
			}
		}
	}()
}

// pausedAtCompare is a conductor's commit published, its comparison held
// at the fake GitHub: what happens next happens between the push and the
// reservation.
func pausedAtCompare(t *testing.T) (*editing, *gate, string) {
	e := newEditing(t)
	e.wokenWith(e.task, "after implement")
	e.current()
	e.commit(map[string]string{"README.md": "# target\n\nfixed\n"})
	g := newGate()
	e.gh.Set(func(s *fakegithub.Server) {
		s.Intercept = func(r *http.Request) int {
			if isCompare(r) {
				g.wait()
			}
			return 0
		}
	})
	e.settleInBackground()
	// After the worker's own cleanup in order, so it runs first: a held
	// request lets the worker stop.
	t.Cleanup(g.release)
	id := e.publish()
	select {
	case <-g.held:
	case <-time.After(20 * time.Second):
		t.Fatal("the comparison was never asked for")
	}
	return e, g, id
}

// refusedUnmoved: the publish settles refused saying want, its wake says
// nothing moved, and the task branch is where it was.
func (e *editing) refusedUnmoved(id, before, want string) {
	e.t.Helper()
	status, why := e.published(id)
	if status != delivery.PublishRefused || !strings.Contains(why, want) {
		e.t.Errorf("publish %s: %q, want refused saying %q", status, why, want)
	}
	if got := e.gh.SHA(e.branch); got != before {
		e.t.Errorf("the task branch moved to %s", got)
	}
	if n := e.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1 AND key = 'publish_refused:' || $2
		AND strpos(line, 'nothing moved') > 0`, e.task, id); n != 1 {
		e.t.Errorf("%d refusal wakes", n)
	}
}

// Whatever changes between the push and the move is judged before the
// task branch moves: a replaced conductor, an aborted task or a writer
// started is refused, and nothing moves.
func TestAPublishIsJudgedAgainBeforeItMoves(t *testing.T) {
	t.Run("the conductor replaced", func(t *testing.T) {
		e, g, id := pausedAtCompare(t)
		before := e.gh.SHA(e.branch)
		mustExec(t, e.owner, `UPDATE runs SET lux_state = 'stopped' WHERE id = $1`, e.cond)
		if status, out := e.chat(e.task, "are you there?"); status != 201 {
			t.Fatalf("chat: %d %v", status, out)
		}
		g.release()
		e.refusedUnmoved(id, before, "your conductor ended before it was published")
	})
	t.Run("the task aborted", func(t *testing.T) {
		e, g, id := pausedAtCompare(t)
		before := e.gh.SHA(e.branch)
		mustExec(t, e.owner, `UPDATE tasks SET status = 'aborted' WHERE id = $1`, e.task)
		g.release()
		e.refusedUnmoved(id, before, "this task is aborted")
	})
	t.Run("a fixer started", func(t *testing.T) {
		e, g, id := pausedAtCompare(t)
		before := e.gh.SHA(e.branch)
		mustExec(t, e.owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role)
			VALUES ('run_fixer_'||$3, $1, $2, $3, 1, 'running', 'fix', 'implementer')`, e.org, e.project, e.task)
		g.release()
		e.refusedUnmoved(id, before, "an implementer is working on this task")
	})
	t.Run("the checkout read-only after the push", func(t *testing.T) {
		e, g, id := pausedAtCompare(t)
		before := e.gh.SHA(e.branch)
		// A queued message's safe sync refused: the read-only fallback.
		e.lux.NoSyncModes = true
		if status, _ := e.chat(e.task, "one more thing"); status != 200 {
			t.Fatal(status)
		}
		e.until("read-only", func() bool {
			return e.count(`SELECT count(*) FROM runs WHERE id = $1 AND checkout_read_only IS NOT NULL`, e.cond) == 1
		})
		g.release()
		e.refusedUnmoved(id, before, "read-only")
	})
}

// While a publish moves the task branch, a writer does not start, the
// conductor is not replaced or stopped, and the decider does not change:
// each waits, or is refused saying to try again.
func TestAMovingPublishHoldsOffWritersAndTheConductorsEnd(t *testing.T) {
	e := newEditing(t)
	e.wokenWith(e.task, "after implement")
	e.current()
	e.commit(map[string]string{"README.md": "# target\n\nfixed\n"})
	g := newGate()
	e.gh.Set(func(s *fakegithub.Server) {
		s.Intercept = func(r *http.Request) int {
			if isMove(r) {
				g.wait()
			}
			return 0
		}
	})
	e.settleInBackground()
	t.Cleanup(g.release)
	id := e.publish()
	select {
	case <-g.held:
	case <-time.After(20 * time.Second):
		t.Fatal("the move was never asked for")
	}
	if n := e.count(`SELECT count(*) FROM conductor_publishes WHERE id = $1 AND status = 'moving'`, id); n != 1 {
		t.Fatal("not reserved before the move")
	}
	e.refused(e.task, "start_phase", `{"phase":"simplify"}`, "your publish is moving the task branch now")
	if status, out := e.call("/internal/tasks/"+e.task+"/decider", map[string]any{"decider": "policy"}); status != 409 {
		t.Errorf("the hand-back while moving: %d %v", status, out)
	}
	if status, out := e.call("/internal/runs/"+e.cond+"/abort", map[string]any{}); status != 409 {
		t.Errorf("stopping the conductor while moving: %d %v", status, out)
	}
	// Its container stopped meanwhile: Chat would end it and start another,
	// and waits instead.
	mustExec(t, e.owner, `UPDATE runs SET lux_state = 'stopped' WHERE id = $1`, e.cond)
	if status, out := e.chat(e.task, "replace while moving"); status != 409 {
		t.Errorf("Chat replacing the conductor while moving: %d %v", status, out)
	}
	if n := e.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND role = 'conductor'`, e.task); n != 1 {
		t.Errorf("%d conductors, want no replacement", n)
	}
	if n := e.count(`SELECT count(*) FROM runs WHERE id = $1 AND status <> 'completed'`, e.cond); n != 1 {
		t.Error("the moving publish's conductor was completed")
	}
	mustExec(t, e.owner, `UPDATE runs SET lux_state = 'running' WHERE id = $1`, e.cond)
	// The workflow's own writer step waits too.
	if _, err := e.store().CreatePhaseRun(context.Background(), e.org, delivery.PhaseRun{TaskID: e.task, Phase: delivery.PhaseFix}); err == nil {
		t.Error("a fix Run was created while the publish moved")
	}
	if n := e.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'fix'`, e.task); n != 0 {
		t.Errorf("%d fix Runs", n)
	}
	g.release()
	if status, why := e.published(id); status != delivery.PublishPublished {
		t.Fatalf("publish %s: %s", status, why)
	}
	e.must(e.task, "start_phase", `{"phase":"review","categories":["correctness"]}`)
}

func (e *editing) store() *delivery.Store { return &delivery.Store{DB: e.app} }

// Two repositories, and the second's task branch refuses its fast-forward:
// what moved is recorded — the pull request's head, the commit event, the
// delivery's heads, the publish's heads for the gate — and the wake says
// it is published in part.
func TestAPartlyMovedPublishRecordsWhatMoved(t *testing.T) {
	w := conducting(t)
	e := &editing{world: w, task: w.task()}
	mustExec(t, w.owner, `INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch)
		VALUES ($1, $2, $3, 'web', 'git://127.0.0.1/acme/web.git', 'main')`, "repo_web_"+w.org, w.org, w.project)
	mustExec(t, w.owner, `INSERT INTO task_repositories (organization_id, task_id, repository_id, access)
		VALUES ($1, $2, $3, 'write'), ($1, $2, $4, 'write')`, w.org, e.task, w.repoID, "repo_web_"+w.org)
	e.cond = w.talk(e.task)
	e.branch = delivery.BranchFor(e.task, 1)
	w.must(e.task, "start_phase", `{"phase":"implement"}`)
	w.until("after implement", func() bool { return w.decisionAt(e.task) == delivery.PointImplemented })
	e.wokenWith(e.task, "after implement")
	before := e.gh.SHA(e.branch)
	mustExec(t, e.owner, `INSERT INTO pull_requests (id, organization_id, project_id, task_id, repository_id, number, url,
		head_branch, base_branch, head_sha, title, state, checks) VALUES ('pr_part', $1, $2, $3, $4, 7, 'https://x/7', $5, 'main', $6, 't', 'open', 'passing')`,
		e.org, e.project, e.task, e.repoID, e.branch, before)
	e.current()
	sha := e.commit(map[string]string{"README.md": "# target\n\nfixed\n"})
	web, err := e.lux.Checkout(e.luxRunOf(e.cond), "web")
	if err != nil {
		t.Fatal(err)
	}
	// On web's task branch too, which its implementer moved.
	if out, err := exec.Command("git", "-C", web, "fetch", "-q", "origin").CombinedOutput(); err != nil {
		t.Fatalf("fetch: %v %s", err, out)
	}
	if out, err := exec.Command("git", "-C", web, "switch", "-q", "-C", e.branch, e.web.SHA(e.branch)).CombinedOutput(); err != nil {
		t.Fatalf("switch: %v %s", err, out)
	}
	addFile(t, web, "WEB.md", "web\n")
	webBefore := e.web.SHA(e.branch)
	e.web.Set(func(s *fakegithub.Server) {
		s.Intercept = func(r *http.Request) int {
			if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/git/refs") || isMove(r) {
				return 422
			}
			return 0
		}
	})
	id := e.publish()
	status, why := e.published(id)
	if status != delivery.PublishPublished || !strings.Contains(why, "web was refused") {
		var heads, push string
		_ = e.owner.QueryRow(context.Background(), `SELECT w.state->>'heads', p.push_result::text FROM workflow_runs w, conductor_publishes p
			WHERE w.task_id = $1 AND p.id = $2`, e.task, id).Scan(&heads, &push)
		t.Fatalf("publish %s: %q, want published with web refused; heads %s push %s", status, why, heads, push)
	}
	if got := e.gh.SHA(e.branch); got != sha {
		t.Errorf("target's task branch at %s, want %s", got, sha)
	}
	if got := e.web.SHA(e.branch); got != webBefore {
		t.Errorf("web's task branch at %s, want %s", got, webBefore)
	}
	if n := e.count(`SELECT count(*) FROM pull_requests WHERE id = 'pr_part' AND head_sha = $1`, sha); n != 1 {
		t.Error("the pull request's head did not follow")
	}
	if n := e.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'git.commit_created' AND payload->>'headSha' = $2
		AND payload->>'by' = 'conductor'`, e.task, sha); n != 1 {
		t.Errorf("%d commit events", n)
	}
	if n := e.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND state->'heads'->>'target' = $2
		AND state->'heads'->>'web' = $3`, e.task, sha, webBefore); n != 1 {
		t.Error("the delivery's heads are not what moved")
	}
	if n := e.count(`SELECT count(*) FROM conductor_publishes WHERE id = $1 AND heads->'target'->>'sha' = $2 AND NOT heads ? 'web'`, id, sha); n != 1 {
		t.Error("the publish's heads are not what moved")
	}
	note := e.wokenWith(e.task, "on the task branch in part")
	if !strings.Contains(note, "target@"+sha[:7]) || !strings.Contains(note, "web was refused") {
		t.Errorf("the wake %q", note)
	}
}

func addFile(t *testing.T, dir, name, content string) {
	t.Helper()
	cmd := exec.Command("sh", "-c", `printf '%s' "$2" > "$1" && git add "$1" && git -c user.name=c -c user.email=c@x commit -q -m edit`,
		"sh", name, content)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("commit %s: %v %s", name, err, out)
	}
}

// A step of the delivery taking its lease just as the branch moved: the
// recording waits for it, and the move is recorded, not refused — though a
// writer started meanwhile would refuse a publish not yet moved. The same
// when the forge moved the branch and its answer was lost: the retry reads
// the branch, finds the move made, and records it.
func TestAMovedPublishIsRecordedOnRetry(t *testing.T) {
	moved := func(t *testing.T, e *editing, id, sha string) {
		t.Helper()
		// A writer meanwhile, as a step starting from the old heads would.
		mustExec(t, e.owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role)
			VALUES ('run_late_'||$3, $1, $2, $3, 1, 'running', 'fix', 'implementer')`, e.org, e.project, e.task)
		mustExec(t, e.owner, `UPDATE workflow_runs SET locked_by = NULL, locked_until = NULL WHERE task_id = $1`, e.task)
		mustExec(t, e.owner, `UPDATE conductor_publishes SET next_attempt_at = NULL WHERE id = $1`, id)
		if status, why := e.published(id); status != delivery.PublishPublished {
			t.Fatalf("publish %s: %s", status, why)
		}
		if n := e.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND state->'heads'->>'target' = $2`, e.task, sha); n != 1 {
			t.Error("the delivery's head did not move")
		}
		e.wokenWith(e.task, "is on the task branch")
	}
	t.Run("a lease taken after the move", func(t *testing.T) {
		e := newEditing(t)
		e.wokenWith(e.task, "after implement")
		e.current()
		sha := e.commit(map[string]string{"README.md": "# target\n\nfixed\n"})
		var leased atomic.Bool
		e.gh.Set(func(s *fakegithub.Server) {
			s.Intercept = func(r *http.Request) int {
				if isMove(r) && !leased.Swap(true) {
					// The test's own connection is not this goroutine's to use.
					if err := e.app.InOrg(context.Background(), e.org, func(tx pgx.Tx) error {
						_, err := tx.Exec(context.Background(), `UPDATE workflow_runs SET locked_by = 'a-step',
							locked_until = now() + interval '1 hour' WHERE task_id = $1`, e.task)
						return err
					}); err != nil {
						t.Error(err)
					}
				}
				return 0
			}
		})
		id := e.publish()
		e.until("moved, not recorded", func() bool {
			return e.gh.SHA(e.branch) == sha && e.count(`SELECT count(*) FROM conductor_publishes WHERE id = $1 AND status = 'moving'
				AND moves->'target'->>'status' = 'moved'`, id) == 1
		})
		moved(t, e, id, sha)
	})
	t.Run("the forge's answer lost", func(t *testing.T) {
		e := newEditing(t)
		e.wokenWith(e.task, "after implement")
		e.current()
		sha := e.commit(map[string]string{"README.md": "# target\n\nfixed\n"})
		var lost atomic.Bool
		e.gh.Set(func(s *fakegithub.Server) {
			s.Intercept = func(r *http.Request) int {
				if isMove(r) && !lost.Swap(true) {
					// GitHub moves the branch; its answer never arrives.
					if out, err := exec.Command("git", "-C", e.gh.Repo, "update-ref", "refs/heads/"+e.branch, sha).CombinedOutput(); err != nil {
						t.Errorf("update-ref: %v %s", err, out)
					}
					return http.StatusBadGateway
				}
				return 0
			}
		})
		id := e.publish()
		e.until("moved, its answer lost", func() bool {
			return lost.Load() && e.count(`SELECT count(*) FROM conductor_publishes WHERE id = $1 AND status = 'moving'
				AND next_attempt_at > now()`, id) == 1
		})
		moved(t, e, id, sha)
	})
}

// A push lux accepted and never reported: refused once its conductor is
// stopped, with its wake; and given up on after its bound.
func TestAnAskedPublishIsNotStranded(t *testing.T) {
	asked := func(t *testing.T) (*editing, string) {
		e := newEditing(t)
		e.wokenWith(e.task, "after implement")
		e.current()
		e.commit(map[string]string{"README.md": "# target\n\nfixed\n"})
		e.lux.HoldPushes = true
		id := e.publish()
		e.until("asked", func() bool {
			return e.count(`SELECT count(*) FROM conductor_publishes WHERE id = $1 AND status = 'asked'`, id) == 1
		})
		return e, id
	}
	t.Run("its conductor stopped", func(t *testing.T) {
		e, id := asked(t)
		before := e.gh.SHA(e.branch)
		if status, out := e.call("/internal/runs/"+e.cond+"/abort", map[string]any{}); status != 200 {
			t.Fatalf("abort: %d %v", status, out)
		}
		e.refusedUnmoved(id, before, "stopped before it was published")
	})
	t.Run("lux never reports it", func(t *testing.T) {
		e, id := asked(t)
		before := e.gh.SHA(e.branch)
		mustExec(t, e.owner, `UPDATE conductor_publishes SET asked_at = now() - interval '11 minutes', next_attempt_at = NULL
			WHERE id = $1`, id)
		e.refusedUnmoved(id, before, "lux never reported the push")
	})
}

// A forge that hangs holds the publish worker, never the phase sweep:
// while a publish's comparison is held at the forge, sweeps bounded on
// their own deliver a message to another task's conductor, before the
// forge answers and without the held request being given up on.
func TestASlowForgeDoesNotHoldThePhaseSweep(t *testing.T) {
	e := newEditing(t)
	other := e.task2()
	oc, _, _ := e.conductor(other)
	e.wokenWith(e.task, "after implement")
	e.current()
	e.commit(map[string]string{"README.md": "# target\n\nfixed\n"})
	g := newGate()
	var compares atomic.Int64
	e.gh.Set(func(s *fakegithub.Server) {
		s.Intercept = func(r *http.Request) int {
			if isCompare(r) {
				compares.Add(1)
				// Held until the gate opens or its caller gives up: a
				// sweep holding it is let go by the sweep's own bound.
				g.once.Do(func() { close(g.held) })
				select {
				case <-g.open:
				case <-r.Context().Done():
				}
			}
			return 0
		}
	})
	e.settleInBackground()
	t.Cleanup(g.release)
	e.publish()
	select {
	case <-g.held:
	case <-time.After(20 * time.Second):
		t.Fatal("the comparison was never asked for")
	}
	if status, _ := e.chat(other, "hello over here"); status != 200 {
		t.Fatal(status)
	}
	heard := func() bool {
		for _, r := range e.lux.Runs() {
			if r.ID != e.luxRunOf(oc) {
				continue
			}
			for _, in := range r.Inputs {
				if strings.Contains(in, "hello over here") {
					return true
				}
			}
		}
		return false
	}
	// Shorter than the forge client's own request timeout, so only the
	// sweep's bound, never the forge's, can let a held sweep go.
	deadline := time.Now().Add(10 * time.Second)
	for !heard() {
		if time.Now().After(deadline) {
			t.Fatal("the other conductor did not hear its message while the publish was held at the forge")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, _ = e.syncer.Sweep(ctx)
		cancel()
		time.Sleep(20 * time.Millisecond)
	}
	select {
	case <-g.open:
		t.Fatal("the gate opened before the message was heard")
	default:
	}
	if n := compares.Load(); n != 1 {
		t.Errorf("%d comparisons while the first was held, want 1", n)
	}
	g.release()
}

func isBranchRead(r *http.Request) bool {
	return r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/git/ref/heads/")
}

// due makes a live publish due now, as its back-off had passed.
func (e *editing) due(id string) {
	e.t.Helper()
	mustExec(e.t, e.owner, `UPDATE conductor_publishes SET next_attempt_at = NULL WHERE id = $1`, id)
}

// settleOnce runs one pass of the publish worker.
func (e *editing) settleOnce() {
	e.t.Helper()
	if _, err := e.syncer.SettlePublishes(context.Background()); err != nil {
		e.t.Fatal(err)
	}
}

// A forge that refuses for good to read the task branch, before any move
// was sent: the repository is refused, the publish settles with nothing
// moved, and the fence is gone — the hand-back goes through.
func TestAForgeRefusingToReadTheBranchBeforeAMoveRefusesThePublish(t *testing.T) {
	e := newEditing(t)
	e.wokenWith(e.task, "after implement")
	e.current()
	e.commit(map[string]string{"README.md": "# target\n\nfixed\n"})
	before := e.gh.SHA(e.branch)
	var moves atomic.Int64
	e.gh.Set(func(s *fakegithub.Server) {
		s.Intercept = func(r *http.Request) int {
			if isMove(r) {
				moves.Add(1)
			}
			if isBranchRead(r) {
				return http.StatusForbidden
			}
			return 0
		}
	})
	id := e.publish()
	status, why := e.published(id)
	if status != delivery.PublishRefused || !strings.Contains(why, "403") {
		t.Fatalf("publish %s: %q, want refused for the forge's 403", status, why)
	}
	if n := moves.Load(); n != 0 {
		t.Errorf("%d moves sent", n)
	}
	e.refusedUnmoved(id, before, "403")
	if status, out := e.call("/internal/tasks/"+e.task+"/decider", map[string]any{"decider": "policy"}); status != 200 {
		t.Errorf("the hand-back after the refusal: %d %v", status, out)
	}
}

// A move sent and answered 502, then a forge that will not let the branch
// be read: reconciled with back-off while the move may still be confirmed,
// then, 30 minutes after it was sent, settled as stalled — nothing
// recorded as published, the fence released, Chat and the conductor told.
func TestAMoveTheForgeWillNotConfirmStallsAfterItsBound(t *testing.T) {
	e := newEditing(t)
	e.wokenWith(e.task, "after implement")
	e.current()
	sha := e.commit(map[string]string{"README.md": "# target\n\nfixed\n"})
	before := e.gh.SHA(e.branch)
	var answered, readable atomic.Bool
	readable.Store(true)
	e.gh.Set(func(s *fakegithub.Server) {
		s.Intercept = func(r *http.Request) int {
			if isMove(r) && !answered.Swap(true) {
				return http.StatusBadGateway
			}
			if isBranchRead(r) && !readable.Load() {
				return http.StatusForbidden
			}
			return 0
		}
	})
	id := e.publish()
	e.until("the move sent, its answer a 502", func() bool {
		return answered.Load() && e.count(`SELECT count(*) FROM conductor_publishes WHERE id = $1 AND status = 'moving'
			AND moves->'target' ? 'attemptedAt' AND failures > 0`, id) == 1
	})
	e.noPublishes = true
	readable.Store(false)
	// Within its bound: asked again, still moving.
	e.due(id)
	var passed time.Time
	if err := e.owner.QueryRow(context.Background(), `SELECT clock_timestamp()`).Scan(&passed); err != nil {
		t.Fatal(err)
	}
	e.settleOnce()
	// Scheduled past the pass, not merely still in the future now: a host
	// pause after the pass must not fail a correct schedule.
	if n := e.count(`SELECT count(*) FROM conductor_publishes WHERE id = $1 AND status = 'moving' AND next_attempt_at > $2`, id, passed); n != 1 {
		t.Fatal("an unconfirmed move within its bound is not reconciled again later")
	}
	mustExec(t, e.owner, `UPDATE conductor_publishes SET moves = jsonb_set(moves, '{target,attemptedAt}',
		to_jsonb(now() - interval '31 minutes')), next_attempt_at = NULL WHERE id = $1`, id)
	e.settleOnce()
	var status string
	if err := e.owner.QueryRow(context.Background(), `SELECT status FROM conductor_publishes WHERE id = $1`, id).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != delivery.PublishStalled {
		t.Fatalf("publish %s, want stalled", status)
	}
	if got := e.gh.SHA(e.branch); got != before {
		t.Errorf("the task branch moved to %s", got)
	}
	if n := e.count(`SELECT count(*) FROM conductor_publishes WHERE id = $1 AND heads IS NULL`, id); n != 1 {
		t.Error("a stalled publish recorded heads")
	}
	if n := e.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'git.commit_created' AND payload->>'publishId' = $2`,
		e.task, id); n != 0 {
		t.Errorf("%d commit events for a move never confirmed", n)
	}
	if n := e.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND state->'heads'->>'target' = $2`, e.task, sha); n != 0 {
		t.Error("the delivery's head moved to an unconfirmed commit")
	}
	want := "dude could not confirm whether target's task branch moved to " + sha + "; check the branch"
	if n := e.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'chat.notice' AND payload->>'text' = $2`,
		e.task, want); n != 1 {
		t.Errorf("%d Chat notices saying %q", n, want)
	}
	if n := e.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1 AND kind = 'publish_stalled' AND key = 'publish_stalled:' || $2`,
		e.task, id); n != 1 {
		t.Errorf("%d publish_stalled wakes", n)
	}
	if status, out := e.call("/internal/tasks/"+e.task+"/decider", map[string]any{"decider": "policy"}); status != 200 {
		t.Errorf("the hand-back after the stall: %d %v", status, out)
	}
}

// The forge moved the branch and its answer was lost, and then someone
// pushed on top before the retry: the move is recorded as published at
// the conductor's commit, the descendant is not taken as the conductor's,
// and the branch is never moved back.
func TestALostMoveFollowedByAnExternalPushIsRecorded(t *testing.T) {
	e := newEditing(t)
	e.wokenWith(e.task, "after implement")
	e.current()
	sha := e.commit(map[string]string{"README.md": "# target\n\nfixed\n"})
	var lost atomic.Bool
	var moves atomic.Int64
	e.gh.Set(func(s *fakegithub.Server) {
		s.Intercept = func(r *http.Request) int {
			if !isMove(r) {
				return 0
			}
			moves.Add(1)
			if !lost.Swap(true) {
				if out, err := exec.Command("git", "-C", e.gh.Repo, "update-ref", "refs/heads/"+e.branch, sha).CombinedOutput(); err != nil {
					t.Errorf("update-ref: %v %s", err, out)
				}
				return http.StatusBadGateway
			}
			return 0
		}
	})
	id := e.publish()
	e.until("moved, its answer lost", func() bool {
		return lost.Load() && e.count(`SELECT count(*) FROM conductor_publishes WHERE id = $1 AND status = 'moving'
			AND next_attempt_at > now()`, id) == 1
	})
	descendant := e.gh.CommitOnTop(e.branch, "a person's commit on top")
	if descendant == "" {
		t.Fatal("no descendant pushed")
	}
	e.due(id)
	if status, why := e.published(id); status != delivery.PublishPublished {
		t.Fatalf("accepted move lost after a descendant was pushed: %s %q", status, why)
	}
	if n := moves.Load(); n != 1 {
		t.Errorf("%d moves sent, want the first only", n)
	}
	if got := e.gh.SHA(e.branch); got != descendant {
		t.Errorf("the task branch at %s, want the descendant %s kept", got, descendant)
	}
	if n := e.count(`SELECT count(*) FROM conductor_publishes WHERE id = $1 AND heads->'target'->>'sha' = $2`, id, sha); n != 1 {
		t.Error("the publish's head is not the conductor's commit")
	}
	if n := e.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND state->'heads'->>'target' = $2`, e.task, sha); n != 1 {
		t.Error("the delivery's head is not the conductor's commit")
	}
	if n := e.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'git.commit_created' AND payload->>'by' = 'conductor'
		AND payload->>'headSha' = $2`, e.task, descendant); n != 0 {
		t.Error("the descendant was recorded as the conductor's")
	}
}

// A worker whose claim expired while its move hung, and a second worker
// that took the publish over and recorded it: when the first one's move
// returns, it records nothing — one commit event, and the delivery's heads
// as the next writer left them.
func TestAnExpiredPublishClaimRecordsNothing(t *testing.T) {
	e := newEditing(t)
	e.wokenWith(e.task, "after implement")
	e.current()
	sha := e.commit(map[string]string{"README.md": "# target\n\nfixed\n"})
	g := newGate()
	var first atomic.Bool
	e.gh.Set(func(s *fakegithub.Server) {
		s.Intercept = func(r *http.Request) int {
			if isMove(r) && !first.Swap(true) {
				g.wait()
			}
			return 0
		}
	})
	t.Cleanup(g.release)
	e.noPublishes = true
	id := e.publish()
	// Worker A: passes until one of them hangs at the move.
	aDone := make(chan struct{})
	go func() {
		defer close(aDone)
		for {
			select {
			case <-g.held:
				return
			default:
			}
			_, _ = e.syncer.SettlePublishes(context.Background())
			time.Sleep(20 * time.Millisecond)
		}
	}()
	t.Cleanup(func() { g.release(); <-aDone })
	e.until("worker A's move hangs", func() bool {
		select {
		case <-g.held:
			return true
		default:
			return false
		}
	})
	// A's claim lapses; worker B takes the publish over and records it.
	mustExec(t, e.owner, `UPDATE conductor_publishes SET claimed_until = now() - interval '1 second' WHERE id = $1`, id)
	e.until("worker B recorded it", func() bool {
		e.settleOnce()
		return e.count(`SELECT count(*) FROM conductor_publishes WHERE id = $1 AND status = 'published'`, id) == 1
	})
	// The next writer moves the delivery's head on.
	mustExec(t, e.owner, `UPDATE workflow_runs SET state = jsonb_set(state, '{heads,target}', '"a-later-head"') WHERE task_id = $1`, e.task)
	g.release()
	<-aDone
	if n := e.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'git.commit_created' AND payload->>'publishId' = $2`,
		e.task, id); n != 1 {
		t.Errorf("%d commit events for one publish, want 1", n)
	}
	if n := e.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND state->'heads'->>'target' = 'a-later-head'`, e.task); n != 1 {
		t.Errorf("the expired worker wrote its head %s over the later one", sha)
	}
}

// A push lux reported past more than a pass's pages of the Run's events,
// its follower not running: each pass reads on from where the last one
// stopped, and the push is found — not given up on because one pass's
// pages ran out, though the ask is past its bound.
func TestAnAskedPushPastManyPagesOfEventsIsFound(t *testing.T) {
	e := newEditing(t)
	e.wokenWith(e.task, "after implement")
	e.current()
	e.commit(map[string]string{"README.md": "# target\n\nfixed\n"})
	e.lux.HoldPushes = true
	id := e.publish()
	e.until("asked", func() bool {
		return e.count(`SELECT count(*) FROM conductor_publishes WHERE id = $1 AND status = 'asked'`, id) == 1
	})
	// From here only the publish worker runs: no follower reports the push.
	e.noPublishes = true
	e.syncer.Stop()
	time.Sleep(200 * time.Millisecond)
	e.lux.AddEvents(e.luxRunOf(e.cond), "run.note", 21*1000)
	e.lux.ReleasePushes()
	mustExec(t, e.owner, `UPDATE conductor_publishes SET asked_at = now() - interval '11 minutes' WHERE id = $1`, id)
	var status, why string
	for range 6 {
		e.due(id)
		e.settleOnce()
		if err := e.owner.QueryRow(context.Background(), `SELECT status, COALESCE(error, '') FROM conductor_publishes WHERE id = $1`, id).
			Scan(&status, &why); err != nil {
			t.Fatal(err)
		}
		if status != delivery.PublishAsked {
			break
		}
	}
	if status == delivery.PublishRefused || status == delivery.PublishAsked {
		t.Fatalf("publish %s %q, want the push found", status, why)
	}
}

// task2 is a second conducted task in the world, its conductor running.
func (e *editing) task2() string {
	e.t.Helper()
	task := e.world.task()
	e.world.talk(task)
	return task
}

// A forge failing for a while is asked again with back-off, not on every
// pass: a failing comparison schedules the next one past the pass, passes
// before then ask nothing, and the next failure waits longer.
func TestATransientForgeFailureBacksOff(t *testing.T) {
	e := newEditing(t)
	e.wokenWith(e.task, "after implement")
	e.current()
	e.commit(map[string]string{"README.md": "# target\n\nfixed\n"})
	var compares atomic.Int64
	e.gh.Set(func(s *fakegithub.Server) {
		s.Intercept = func(r *http.Request) int {
			if isCompare(r) {
				compares.Add(1)
				return http.StatusServiceUnavailable
			}
			return 0
		}
	})
	e.noPublishes = true
	id := e.publish()
	// The worker asks lux to push; the conductor's follower records it.
	e.until("pushed", func() bool {
		if e.count(`SELECT count(*) FROM conductor_publishes WHERE id = $1 AND status = 'pushed'`, id) == 1 {
			return true
		}
		e.settleOnce()
		return false
	})
	ctx := context.Background()
	dbNow := func() time.Time {
		t.Helper()
		var now time.Time
		if err := e.owner.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
			t.Fatal(err)
		}
		return now
	}
	// backedOff runs one failing pass and checks the back-off it scheduled.
	// The worker writes now() + wait during the pass, so with the database
	// clock read just before and just after it, next-after <= wait <=
	// next-before however long the host paused: the wait the policy asks
	// for must lie in that window.
	backedOff := func(failures int, want time.Duration) {
		t.Helper()
		before := dbNow()
		e.settleOnce()
		after := dbNow()
		var next time.Time
		var got int
		if err := e.owner.QueryRow(ctx, `SELECT next_attempt_at, failures FROM conductor_publishes WHERE id = $1`, id).
			Scan(&next, &got); err != nil {
			t.Fatal(err)
		}
		const slack = 10 * time.Millisecond
		if got != failures || next.Sub(after) > want+slack || next.Sub(before) < want-slack {
			t.Fatalf("after failure %d (counted %d): the next attempt is %v to %v past the pass, want a back-off of %v",
				failures, got, next.Sub(after), next.Sub(before), want)
		}
	}
	// Counted from here: a pass racing the follower's report may have
	// compared once already.
	base := compares.Load()
	e.due(id)
	backedOff(1, 2*time.Second)
	if n := compares.Load() - base; n != 1 {
		t.Fatalf("%d comparisons in one pass", n)
	}
	// Not due: passes leave it alone. The deadline is pushed far out so a
	// host pause cannot make it due under the test; when it is set is what
	// backedOff checks.
	mustExec(t, e.owner, `UPDATE conductor_publishes SET next_attempt_at = now() + interval '1 day' WHERE id = $1`, id)
	for range 5 {
		e.settleOnce()
	}
	if n := compares.Load() - base; n != 1 {
		t.Errorf("%d comparisons while the back-off was not up, want 1", n)
	}
	e.due(id)
	backedOff(2, 4*time.Second)
	if n := compares.Load() - base; n != 2 {
		t.Errorf("%d comparisons once due, want 2", n)
	}
}
