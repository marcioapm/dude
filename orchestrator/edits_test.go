package orchestrator_test

// The conductor's edits (design: the conductor, step 6), through the
// orchestrator's real code: its writable checkout kept current by the
// fake lux's fast-forward syncs, its publish tool, the syncer taking the
// pushed commits to the task branch through the fake GitHub, and the pull
// request gate refusing its unreviewed commit.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/agenttools"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// editing is a conducted task after its implementer: the task branch has
// a head, the delivery waits on the conductor after implement, and the
// conductor runs on a checkout it may change.
type editing struct {
	*world
	task, cond, branch string
}

func newEditing(t *testing.T) *editing {
	t.Helper()
	w := conducting(t)
	e := &editing{world: w, task: w.task()}
	e.cond = w.talk(e.task)
	e.branch = delivery.BranchFor(e.task, 1)
	w.must(e.task, "start_phase", `{"phase":"implement"}`)
	w.until("after implement", func() bool { return w.decisionAt(e.task) == delivery.PointImplemented })
	return e
}

// work is the conductor's checkout of the task's repository, in the fake lux.
func (e *editing) work() string {
	e.t.Helper()
	dir, err := e.lux.Checkout(e.luxRunOf(e.cond), "target")
	if err != nil {
		e.t.Fatal(err)
	}
	return dir
}

func (e *editing) git(args ...string) string {
	e.t.Helper()
	out, err := exec.Command("git", append([]string{"-C", e.work(), "-c", "user.name=c", "-c", "user.email=c@x"}, args...)...).CombinedOutput()
	if err != nil {
		e.t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// commit writes files in the conductor's checkout and commits them.
func (e *editing) commit(files map[string]string) string {
	e.t.Helper()
	for path, content := range files {
		full := filepath.Join(e.work(), path)
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			e.t.Fatal(err)
		}
	}
	e.git("add", "-A")
	e.git("commit", "-q", "-m", "conductor edit")
	return e.git("rev-parse", "HEAD")
}

// current puts the conductor's checkout on the task branch at its head, as
// the conductor does when told (git switch -C <branch> lux/<branch>).
func (e *editing) current() {
	e.t.Helper()
	e.git("fetch", "-q", "origin")
	e.git("switch", "-q", "-C", e.branch, e.gh.SHA(e.branch))
}

// published waits for the publish to settle, and returns its status and error.
func (e *editing) published(id string) (string, string) {
	e.t.Helper()
	var status, why string
	e.until("the publish "+id+" settled", func() bool {
		_ = e.owner.QueryRow(context.Background(), `SELECT status, COALESCE(error, '') FROM conductor_publishes WHERE id = $1`, id).
			Scan(&status, &why)
		return status == delivery.PublishPublished || status == delivery.PublishRefused
	})
	return status, why
}

func (e *editing) publish() string {
	e.t.Helper()
	out := e.must(e.task, "publish", `{"message":"a small fix"}`)
	id, _ := out["publishId"].(string)
	if id == "" || out["status"] != "publishing" {
		e.t.Fatalf("publish: %v", out)
	}
	return id
}

// The conductor's spec: its repository writable, with a push branch of
// its own, as a phase's per-Run branch.
func TestTheConductorsCheckoutIsWritableWithABranchOfItsOwn(t *testing.T) {
	w := conducting(t)
	task := w.task()
	conductor := w.talk(task)
	var spec lux.Spec
	_ = json.Unmarshal([]byte(w.conductorSpecOf(task)), &spec)
	if spec.Git == nil || len(spec.Git.Repositories) != 1 || spec.Git.Repositories[0].Push != nil {
		t.Fatalf("repositories %+v, want target writable", spec.Git)
	}
	if want := fmt.Sprintf("dude/%s/run-%s", task, conductor); spec.Git.Push == nil || spec.Git.Push.Branch != want {
		t.Errorf("push %+v, want %s", spec.Git.Push, want)
	}
}

// A conductor started once the task branch exists checks it out by name,
// where lux's fast-forward can move it (lux never switches branches).
func TestALaterConductorIsCheckedOutOnTheTaskBranch(t *testing.T) {
	e := newEditing(t)
	mustExec(t, e.owner, `UPDATE runs SET lux_state = 'stopped' WHERE id = $1`, e.cond)
	if status, out := e.chat(e.task, "are you there?"); status != 201 {
		t.Fatalf("chat: %d %v", status, out)
	}
	var spec lux.Spec
	_ = json.Unmarshal([]byte(e.conductorSpecOf(e.task)), &spec)
	if spec.Git == nil || spec.Git.Repositories[0].Ref != e.branch {
		t.Fatalf("the new conductor's checkout: %+v, want %s", spec.Git, e.branch)
	}
}

// A read-only repository stays read-only for the conductor too.
func TestAReadOnlyRepositoryStaysReadOnlyForTheConductor(t *testing.T) {
	w := conducting(t)
	task := w.task()
	if err := w.app.InOrg(context.Background(), w.org, func(tx pgx.Tx) error {
		return delivery.NameOnlyRepository(context.Background(), tx, task)
	}); err != nil {
		t.Fatal(err)
	}
	mustExec(t, w.owner, `UPDATE task_repositories SET access = 'read' WHERE task_id = $1`, task)
	w.talk(task)
	var spec lux.Spec
	_ = json.Unmarshal([]byte(w.conductorSpecOf(task)), &spec)
	if spec.Git == nil || spec.Git.Repositories[0].Push == nil || *spec.Git.Repositories[0].Push {
		t.Errorf("a read repository: %+v", spec.Git)
	}
}

// A wake of the running conductor syncs its writable repository to the
// task branch, fast-forward, before the note. Started before the branch
// existed, its checkout is on another branch, which lux never switches:
// the note says how to take it. Once on the branch, the next wake moves
// it to the branch's head.
func TestAWakeBringsTheConductorsCheckoutCurrentFirst(t *testing.T) {
	e := newEditing(t)
	note := e.heard("after implement")
	lr := e.luxRunOf(e.cond)
	var syncs [][]lux.SyncRef
	for _, r := range e.lux.Runs() {
		if r.ID == lr {
			syncs = r.Syncs
		}
	}
	if len(syncs) == 0 || len(syncs[0]) != 1 || syncs[0][0] != (lux.SyncRef{Repo: "target", Ref: e.branch, Mode: lux.SyncFastForward}) {
		t.Fatalf("syncs %v, want target to %s fast-forward", syncs, e.branch)
	}
	// The sync went to lux before the note did.
	calls := callsOf(e, lr)
	if s, i := slices.Index(calls, "sync"), slices.Index(calls, "input"); s < 0 || i < s {
		t.Errorf("lux was asked %v: the note before the sync", calls)
	}
	if want := "target: your checkout is 1 behind and not on the task branch; `git switch -C " + e.branch; !strings.Contains(note, want) {
		t.Errorf("the note %q lacks %q", note, want)
	}
	// On the branch, one commit behind it: the next wake fast-forwards it.
	e.current()
	e.git("reset", "-q", "--hard", "HEAD~1")
	e.must(e.task, "decide", `{"action":"next"}`)
	e.until("after the review round", func() bool { return e.decisionAt(e.task) == delivery.PointReviewed })
	if note := e.heard("after a review round"); strings.Contains(note, "your checkout") {
		t.Errorf("a fast-forward told: %q", note)
	}
	if got, want := e.git("rev-parse", "HEAD"), e.gh.SHA(e.branch); got != want {
		t.Errorf("checkout at %s, want the task branch's %s", got, want)
	}
}

func callsOf(e *editing, lr string) []string {
	for _, r := range e.lux.Runs() {
		if r.ID == lr {
			return r.Calls
		}
	}
	return nil
}

// heard waits for lux to have given the conductor an input saying want,
// and returns it: what its agent read, the lines dude added to a note
// included.
func (e *editing) heard(want string) string {
	e.t.Helper()
	var got string
	e.until("the conductor told "+want, func() bool {
		for _, r := range e.lux.Runs() {
			if r.ID != e.luxRunOf(e.cond) {
				continue
			}
			for _, in := range r.Inputs {
				if strings.Contains(in, want) {
					got = in
					return true
				}
			}
		}
		return false
	})
	return got
}

// A checkout with work of its own is kept, and the note says how far
// behind it is and how to take the branch in.
func TestAKeptCheckoutIsToldInTheWakeNote(t *testing.T) {
	e := newEditing(t)
	e.wokenWith(e.task, "after implement")
	// Local work on the task branch, then the branch moves on under it (a fix).
	e.current()
	e.commit(map[string]string{"NOTE.md": "mine\n"})
	if err := os.WriteFile(filepath.Join(e.work(), "WIP.md"), []byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.git("add", "WIP.md")
	e.must(e.task, "decide", `{"action":"next"}`)
	e.until("after the review round", func() bool { return e.decisionAt(e.task) == delivery.PointReviewed })
	e.must(e.task, "start_phase", `{"phase":"fix"}`)
	e.until("after the fix", func() bool { return e.decisionAt(e.task) == delivery.PointFixed })
	note := e.heard("after a fix")
	want := "target: your checkout is 1 behind and has local changes and 1 commits of its own; `git merge lux/" + e.branch + "`"
	if !strings.Contains(note, want) {
		t.Errorf("the note %q lacks %q", note, want)
	}
}

// A checkout with commits on top of the task branch is ahead, and told so.
func TestAnAheadCheckoutIsToldInTheWakeNote(t *testing.T) {
	e := newEditing(t)
	e.wokenWith(e.task, "after implement")
	e.current()
	e.commit(map[string]string{"NOTE.md": "mine\n"})
	e.must(e.task, "decide", `{"action":"next"}`)
	e.until("after the review round", func() bool { return e.decisionAt(e.task) == delivery.PointReviewed })
	note := e.heard("after a review round")
	if !strings.Contains(note, "target: your checkout is 1 ahead of the task branch") {
		t.Errorf("the note %q", note)
	}
}

// A resume of a parked conductor syncs too, in the resume itself.
func TestAResumeBringsTheConductorsCheckoutCurrent(t *testing.T) {
	e := newEditing(t)
	e.wokenWith(e.task, "after implement")
	e.syncer.ConductorWarm = 1
	e.until("the conductor parked", func() bool { _, status, _ := e.conductor(e.task); return status == "paused" })
	e.syncer.ConductorWarm = 1 << 40
	if status, _ := e.chat(e.task, "are you there?"); status != 200 {
		t.Fatalf("chat %d", status)
	}
	lr := e.luxRunOf(e.cond)
	e.until("resumed", func() bool {
		for _, r := range e.lux.Runs() {
			if r.ID == lr && len(r.ResumeSyncs) > 0 {
				return true
			}
		}
		return false
	})
	for _, r := range e.lux.Runs() {
		if r.ID == lr {
			got := r.ResumeSyncs[len(r.ResumeSyncs)-1]
			if len(got) != 1 || got[0] != (lux.SyncRef{Repo: "target", Ref: e.branch, Mode: lux.SyncFastForward}) {
				t.Errorf("the resume's sync %v", got)
			}
		}
	}
}

// A lux without sync modes: the conductor is read-only for the rest of
// its life, Chat says so, and publish is refused.
func TestALuxWithoutSyncModesLeavesTheConductorReadOnly(t *testing.T) {
	w := conducting(t)
	w.lux.NoSyncModes = true
	e := &editing{world: w, task: w.task()}
	e.cond = w.talk(e.task)
	w.must(e.task, "start_phase", `{"phase":"implement"}`)
	w.until("after implement", func() bool { return w.decisionAt(e.task) == delivery.PointImplemented })
	note := w.wokenWith(e.task, "after implement")
	_ = note
	w.until("read-only", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND checkout_read_only IS NOT NULL`, e.cond) == 1
	})
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'chat.notice'
		AND payload->>'about' = 'checkout_read_only'`, e.task); n != 1 {
		t.Errorf("%d notices in Chat", n)
	}
	w.until("told", func() bool {
		for _, n := range w.woken(e.task) {
			if strings.Contains(n, "cannot be kept current") {
				return true
			}
		}
		return w.count(`SELECT count(*) FROM directives WHERE run_id = $1 AND strpos(text, 'cannot be kept current') > 0`, e.cond) > 0
	})
	w.refused(e.task, "publish", `{}`, "read-only")
	for _, r := range w.lux.Runs() {
		for _, s := range r.Syncs {
			for _, ref := range s {
				if ref.Mode == "" || ref.Mode == lux.SyncMove {
					t.Errorf("fell back to move: %v", s)
				}
			}
		}
	}
}

// Publish: the conductor's commit goes to the task branch, by
// fast-forward; the pull request's head follows; git.commit_created is
// the conductor's; the delivery's heads move; it is woken saying so.
func TestThePublishedCommitMovesTheTaskBranchAndThePullRequest(t *testing.T) {
	e := newEditing(t)
	e.wokenWith(e.task, "after implement")
	before := e.gh.SHA(e.branch)
	// A pull request open on the branch, as after the gate.
	mustExec(t, e.owner, `INSERT INTO pull_requests (id, organization_id, project_id, task_id, repository_id, number, url,
		head_branch, base_branch, head_sha, title, state, checks) VALUES ('pr_edit', $1, $2, $3, $4, 7, 'https://x/7', $5, 'main', $6, 't', 'open', 'passing')`,
		e.org, e.project, e.task, e.repoID, e.branch, before)
	e.current()
	sha := e.commit(map[string]string{"README.md": "# target\n\nfixed\n"})
	id := e.publish()
	if status, why := e.published(id); status != delivery.PublishPublished {
		t.Fatalf("publish %s: %s", status, why)
	}
	if got := e.gh.SHA(e.branch); got != sha {
		t.Fatalf("task branch at %s, want %s", got, sha)
	}
	if n := e.count(`SELECT count(*) FROM pull_requests WHERE id = 'pr_edit' AND head_sha = $1 AND checks = 'unknown'`, sha); n != 1 {
		t.Error("the pull request's head did not follow")
	}
	var actor, by string
	var payload map[string]any
	if err := e.owner.QueryRow(context.Background(), `SELECT actor_id, payload->>'by', payload FROM events
		WHERE task_id = $1 AND event_type = 'git.commit_created' AND payload->>'headSha' = $2`, e.task, sha).
		Scan(&actor, &by, &payload); err != nil {
		t.Fatal(err)
	}
	if actor != e.cond || by != "conductor" || payload["baseSha"] != before {
		t.Errorf("git.commit_created by %s (%s): %v", actor, by, payload)
	}
	if n := e.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND state->'heads'->>'target' = $2`, e.task, sha); n != 1 {
		t.Error("the delivery's head did not move")
	}
	e.wokenWith(e.task, "is on the task branch")
}

// Each refusal says why, and moves nothing.
func TestAPublishIsRefusedSayingWhy(t *testing.T) {
	t.Run("an implementer at work", func(t *testing.T) {
		e := newEditing(t)
		mustExec(t, e.owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role)
			VALUES ('run_writer', $1, $2, $3, 1, 'running', 'fix', 'implementer')`, e.org, e.project, e.task)
		e.refused(e.task, "publish", `{}`, "an implementer is working on this task; wait for it, or steer it")
		if n := e.count(`SELECT count(*) FROM conductor_publishes WHERE task_id = $1`, e.task); n != 0 {
			t.Errorf("%d publishes recorded", n)
		}
	})
	t.Run("Deliver decides", func(t *testing.T) {
		e := newEditing(t)
		if status, out := e.call("/internal/tasks/"+e.task+"/decider", map[string]any{"decider": "policy"}); status != 200 {
			t.Fatalf("hand back: %d %v", status, out)
		}
		e.refused(e.task, "publish", `{}`, "Deliver decides this task: steer its agents, or ask the person to hand it to you.")
	})
	t.Run("superseded", func(t *testing.T) {
		// Replaced after its call was authenticated, before the tool ran.
		e := newEditing(t)
		paused, resume := make(chan struct{}), make(chan struct{})
		var once sync.Once
		tools := httptest.NewServer((&agenttools.Server{DB: e.app, Log: quiet, BeforeCall: func(tool string) {
			if tool == "publish" {
				once.Do(func() { close(paused); <-resume })
			}
		}}).Handler())
		t.Cleanup(tools.Close)
		spec := e.conductorSpecOf(e.task)
		done := make(chan string, 1)
		go func() {
			status, body := e.callTool(tools.URL, spec, "publish", `{}`)
			done <- fmt.Sprint(status, " ", body)
		}()
		<-paused
		mustExec(t, e.owner, `UPDATE runs SET lux_state = 'stopped' WHERE id = $1`, e.cond)
		if status, out := e.chat(e.task, "are you there?"); status != 201 {
			t.Fatalf("chat: %d %v", status, out)
		}
		close(resume)
		if r := <-done; !strings.HasPrefix(r, "422 ") || !strings.Contains(r, "no longer this task's conductor") {
			t.Errorf("the replaced conductor's publish: %s", r)
		}
		if n := e.count(`SELECT count(*) FROM conductor_publishes WHERE task_id = $1`, e.task); n != 0 {
			t.Errorf("%d publishes recorded", n)
		}
	})
	settled := func(t *testing.T, e *editing, want string) {
		t.Helper()
		before := e.gh.SHA(e.branch)
		id := e.publish()
		status, why := e.published(id)
		if status != delivery.PublishRefused || !strings.Contains(why, want) {
			t.Errorf("publish %s: %q, want refused saying %q", status, why, want)
		}
		if got := e.gh.SHA(e.branch); got != before {
			t.Errorf("the task branch moved to %s", got)
		}
		e.wokenWith(e.task, "was refused, nothing moved")
	}
	t.Run("behind", func(t *testing.T) {
		e := newEditing(t)
		e.wokenWith(e.task, "after implement")
		// Its checkout where it started, before the implementer: behind.
		e.git("reset", "-q", "--hard", "HEAD")
		e.git("checkout", "-q", "--detach", e.git("rev-list", "--max-parents=0", "HEAD"))
		e.commit(map[string]string{"X.md": "x\n"})
		settled(t, e, "your checkout is behind the task branch; `git merge lux/"+e.branch+"` first")
	})
	t.Run("over the line limit", func(t *testing.T) {
		e := newEditing(t)
		e.wokenWith(e.task, "after implement")
		e.current()
		e.commit(map[string]string{"BIG.md": strings.Repeat("line\n", 61)})
		settled(t, e, "this is 61 lines in 1 files, past the conductor's limit of 60 lines / 3 files: delegate this (start_phase implement)")
	})
	t.Run("over the file limit", func(t *testing.T) {
		e := newEditing(t)
		e.wokenWith(e.task, "after implement")
		e.current()
		e.commit(map[string]string{"A.md": "a\n", "B.md": "b\n", "C.md": "c\n", "D.md": "d\n"})
		settled(t, e, "this is 4 lines in 4 files, past the conductor's limit of 60 lines / 3 files: delegate this")
	})
	t.Run("the project's own limit", func(t *testing.T) {
		e := newEditing(t)
		mustExec(t, e.owner, `UPDATE projects SET delivery_policy = '{"conductorEditLines": 2}' WHERE id = $1`, e.project)
		e.wokenWith(e.task, "after implement")
		e.current()
		e.commit(map[string]string{"A.md": "a\nb\nc\n"})
		settled(t, e, "past the conductor's limit of 2 lines / 3 files")
	})
	t.Run("nothing to publish", func(t *testing.T) {
		e := newEditing(t)
		e.wokenWith(e.task, "after implement")
		e.current()
		settled(t, e, "nothing to publish")
	})
}

// The gate refuses a head that is the conductor's commit until a review
// ran on it: the conductor's ask and open, and Deliver's opening after a
// hand-back; then allows.
func TestThePullRequestGateWaitsForAReviewOfTheConductorsCommit(t *testing.T) {
	e := newEditing(t)
	e.wokenWith(e.task, "after implement")
	e.must(e.task, "start_phase", `{"phase":"simplify"}`)
	e.until("before the pull request", func() bool { return e.decisionAt(e.task) == delivery.PointBeforePR })
	e.wokenWith(e.task, "before the pull request")
	e.current()
	e.commit(map[string]string{"README.md": "# target\n\nnit\n"})
	if status, why := e.published(e.publish()); status != delivery.PublishPublished {
		t.Fatalf("publish %s: %s", status, why)
	}
	e.refused(e.task, "decide", `{"action":"ask_person"}`, "the last commit is the conductor's; run a review first")
	e.refused(e.task, "decide", `{"action":"open_pull_request"}`, "the last commit is the conductor's; run a review first")
	if n := e.count(`SELECT count(*) FROM questions WHERE task_id = $1 AND pr_gate_heads IS NOT NULL`, e.task); n != 0 {
		t.Errorf("%d gate questions asked", n)
	}
	e.must(e.task, "start_phase", `{"phase":"review","categories":["correctness"]}`)
	e.until("after the review", func() bool { return e.decisionAt(e.task) == delivery.PointReviewed })
	// The scripted reviewer raises its finding: dismissed, then on.
	var finding string
	_ = e.owner.QueryRow(context.Background(), `SELECT id FROM review_findings WHERE task_id = $1 AND status = 'open' LIMIT 1`, e.task).Scan(&finding)
	if finding != "" {
		e.must(e.task, "dismiss_finding", fmt.Sprintf(`{"id":%q,"reason":"fine as it is"}`, finding))
	}
	e.must(e.task, "decide", `{"action":"next"}`)
	e.until("before the pull request again", func() bool { return e.decisionAt(e.task) == delivery.PointBeforePR })
	e.must(e.task, "decide", `{"action":"ask_person"}`)
	if status, out := e.chat(e.task, "Open"); status != 200 || out["questionId"] == nil {
		t.Fatalf("answer: %d %v", status, out)
	}
	e.must(e.task, "decide", `{"action":"open_pull_request"}`)
	e.until("the pull request", func() bool { return len(e.gh.Pulls()) == 1 })
}

// Under Deliver, a head that is the conductor's untested commit goes to a
// review instead of the pull request, and Chat says so.
func TestDeliverReviewsTheConductorsCommitBeforeThePullRequest(t *testing.T) {
	e := newEditing(t)
	e.wokenWith(e.task, "after implement")
	e.must(e.task, "start_phase", `{"phase":"simplify"}`)
	e.until("before the pull request", func() bool { return e.decisionAt(e.task) == delivery.PointBeforePR })
	e.current()
	e.commit(map[string]string{"README.md": "# target\n\nnit\n"})
	if status, why := e.published(e.publish()); status != delivery.PublishPublished {
		t.Fatalf("publish %s: %s", status, why)
	}
	head := e.gh.SHA(e.branch)
	if status, out := e.call("/internal/tasks/"+e.task+"/decider", map[string]any{"decider": "policy", "openPullRequest": true}); status != 200 {
		t.Fatalf("hand back: %d %v", status, out)
	}
	e.until("a review of the conductor's commit", func() bool {
		return e.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review' AND base_refs->>'target' = $2`, e.task, head) > 0
	})
	if len(e.gh.Pulls()) != 0 {
		t.Fatal("a pull request opened at the unreviewed head")
	}
	if n := e.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'chat.notice' AND payload->>'about' = 'untested_head'`, e.task); n != 1 {
		t.Errorf("%d notices", n)
	}
}
