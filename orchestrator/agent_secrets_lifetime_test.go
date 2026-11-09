package orchestrator_test

// How long a Run's copy of the values it was sent lives (run_project_secrets,
// phases.copyNeeded): for as long as anything can still produce or read the
// Run's output, not only until that output is first collected. A kept Run
// is resumed, or its lux Run taken over by the next round, only after its
// collection; a text file it published is read from lux each time it is
// served; a dead writer's commits are recovered from lux's checkpoints at
// any time. Each of these, after the value was rotated, still redacts or
// refuses the value the Run held; and a Run nothing can come back to loses
// its copy once its keep and its retention end.

import (
	"context"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

// hookConn is a pool on the test's database for a fakelux Decide hook: lux
// calls Decide on goroutines of its own while the test polls on w.owner, a
// single connection, which two goroutines must not use at once ("conn
// busy"). Closed when the test ends.
func (w *world) hookConn() *pgxpool.Pool {
	w.t.Helper()
	pool, err := pgxpool.New(context.Background(), w.owner.Config().ConnString())
	if err != nil {
		w.t.Fatal(err)
	}
	w.t.Cleanup(pool.Close)
	return pool
}

// githubHolds says whether any branch on the fake GitHub's repository has
// a commit that adds or removes value. (lux's checkpoints, which the fake
// keeps in the same repository under refs/checkpoints, are not GitHub's.)
func (w *world) githubHolds(value string) bool {
	w.t.Helper()
	out, err := exec.Command("git", "-C", w.gh.Repo, "log", "--branches", "--oneline", "-S"+value).Output()
	if err != nil {
		w.t.Fatal(err)
	}
	return strings.TrimSpace(string(out)) != ""
}

// collected waits until the ended Run's last exit is collected, which
// deletes its copy unless copyNeeded keeps it; then has it ended over
// copyBackstop ago (two hours) and runs the backstop (ForgetSecretCopies),
// which deletes any copy copyNeeded does not keep: what is left is what
// the Run keeps for as long as copyNeeded holds, not what a backstop not
// yet due left behind.
func (w *world) collected(runID string) {
	w.t.Helper()
	w.until("the Run's output collected", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status IN ('completed', 'failed', 'aborted')
			AND artifacts_due_at IS NULL`, runID) == 1
	})
	w.pump()
	mustExec(w.t, w.owner, `UPDATE runs SET ended_at = now() - interval '2 hours' WHERE id = $1`, runID)
	w.forgetCopies()
}

// forgetCopies runs the backstop once.
func (w *world) forgetCopies() {
	w.t.Helper()
	if _, err := w.syncer.ForgetSecretCopies(context.Background()); err != nil {
		w.t.Fatal(err)
	}
}

// copyExpired has the ended Run's retention past (ended 31 days ago) and
// runs the backstop, which deletes its copy.
func (w *world) copyExpired(runID string) {
	w.t.Helper()
	mustExec(w.t, w.owner, `UPDATE runs SET ended_at = now() - interval '31 days' WHERE id = $1`, runID)
	w.forgetCopies()
	if got := w.runCopy(runID); len(got) != 0 {
		w.t.Fatalf("the Run's copy holds %d values past its retention", len(got))
	}
}

// (a) A kept implementer committed and printed A, and was aborted; its
// output is collected and A is rotated to B; a person resumes it
// (ResumeKeptTx). The agent, which still remembers A, prints it, and dude
// stores the marker; its push, whose history holds A, is refused.
func TestAKeptRunResumedAfterItsCollectionStillRedactsAndRefusesAnOldValue(t *testing.T) {
	w := newWorld(t)
	w.grant("IMPL_TOKEN", implToken, "implementer")
	w.remembersImplementer(implToken, finishesTurn, writeCall("config/app.toml", "password = "+implToken), commitCall("Add config"))
	task := w.task()
	w.deliver(task)
	runID, _ := w.basedImplementer(task)
	if status, body := w.call("/internal/runs/"+runID+"/abort", map[string]any{"reason": "wrong approach"}); status != 200 {
		t.Fatalf("abort: %d %v", status, body)
	}
	w.until("the Run kept", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND lux_stop_reason = 'kept'`, runID) == 1
	})
	w.collected(runID)
	if got := w.runCopyValues(runID); !slices.Equal(got, []string{"IMPL_TOKEN=" + implToken}) {
		t.Errorf("a kept Run's copy after its collection holds %d values, want the one it was sent", len(got))
	}
	w.grant("IMPL_TOKEN", implTokenB, "implementer")
	if status, body := w.call("/internal/tasks/"+task+"/recover", map[string]any{"action": "resume", "note": "check the last commit"}); status != 200 {
		t.Fatalf("resume: %d %v", status, body)
	}
	w.until("the remembered value printed", func() bool {
		return w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND payload::text LIKE '%--grep=remembered %'`, runID) > 0
	})
	w.until("the implementer failed or pushed", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'failed'`, runID) == 1 || w.runBranchesOnGitHub() != ""
	})
	w.assertNotStored(task, runID, implToken)
	if w.githubHolds(implToken) {
		t.Error("the old value reached GitHub")
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.push.refused'
		AND payload->'secrets' = '["IMPL_TOKEN"]' AND payload->'files' = '["config/app.toml"]'`, runID); n != 1 {
		t.Errorf("%d run.push.refused naming IMPL_TOKEN and the committed file", n)
	}
	if got := w.runCopyValues(runID); !slices.Equal(got, []string{"IMPL_TOKEN=" + implToken, "IMPL_TOKEN=" + implTokenB}) {
		t.Errorf("copy after the resume holds %d values, want the submit's and the resume's", len(got))
	}
}

// (b) Three rounds on one lux Run: the implementer, kept for its next
// round, is collected; A is rotated to B while the first reviewer works,
// and the first fix round takes the implementer's lux Run over
// (handOverTx) and is resumed with B; B is rotated to C while the second
// reviewer works, and the second fix round takes it over in turn and is
// resumed with C. The second fixer prints A and commits it: its copy holds
// A and B, carried over twice, so the print is stored redacted and the
// push is refused. A predecessor handed over and collected keeps no copy.
func TestARoundHandedOverAfterARotationStillRedactsAndRefusesTheOldValue(t *testing.T) {
	w, task := stuckReview(t, 3)
	w.grant("IMPL_TOKEN", implToken, "implementer", "fixer")
	values := []string{implTokenB, implTokenC}
	hookConn := w.hookConn()
	var mu sync.Mutex
	reviews := 0
	scripted := w.lux.Decide
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		b := scripted(spec)
		labels, _ := spec["labels"].(map[string]any)
		switch labels["dude.phase"] {
		case "review":
			// Between the rounds: the writer has ended, and the next fix
			// round has not yet been resumed.
			mu.Lock()
			if reviews < len(values) {
				if _, err := hookConn.Exec(context.Background(), `UPDATE project_secrets SET value = $2, updated_at = now()
					WHERE project_id = $1 AND name = 'IMPL_TOKEN'`, w.project, values[reviews]); err != nil {
					t.Error(err)
				}
			}
			reviews++
			mu.Unlock()
		case "fix":
			var fixes int
			if err := hookConn.QueryRow(context.Background(), `SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'fix'`, task).
				Scan(&fixes); err != nil {
				t.Error(err)
			}
			if fixes >= 2 {
				b.CallTools = append(slices.Clone(b.CallTools), gitCall("log", "-1", "--grep=remembered "+implToken),
					writeCall("config/app.toml", "password = "+implToken), commitCall("Add config"))
			}
		}
		return b
	}
	var fix1, fix2 string
	w.until("the second fix round failed or pushed the value", func() bool {
		fix2 = w.str(`SELECT COALESCE((SELECT id FROM runs WHERE task_id = $1 AND phase = 'fix' ORDER BY created_at OFFSET 1 LIMIT 1), '')`, task)
		return fix2 != "" && (w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status IN ('failed', 'completed')`, fix2) == 1)
	})
	fix1 = w.str(`SELECT id FROM runs WHERE task_id = $1 AND phase = 'fix' ORDER BY created_at LIMIT 1`, task)
	impl := w.str(`SELECT id FROM runs WHERE task_id = $1 AND phase = 'implement'`, task)
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND continues_run_id = $2 AND continues_how = 'resume'`, fix1, impl); n != 1 {
		t.Fatalf("the first fix round did not take the implementer's lux Run over")
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND continues_run_id = $2 AND continues_how = 'resume'`, fix2, fix1); n != 1 {
		t.Fatalf("the second fix round did not take the first fixer's lux Run over")
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND payload::text LIKE '%--grep=remembered [REDACTED:IMPL_TOKEN]%'`, fix2); n == 0 {
		t.Error("no stored event of the second fix round shows the marker where the agent printed the old value")
	}
	w.assertTaskHoldsNo(task, implToken)
	if w.githubHolds(implToken) {
		t.Error("the old value reached GitHub")
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.push.refused'
		AND payload->'secrets' = '["IMPL_TOKEN"]'`, fix2); n != 1 {
		t.Errorf("%d run.push.refused on the second fix round naming IMPL_TOKEN", n)
	}
	got := w.runCopyValues(fix2)
	for _, v := range []string{implToken, implTokenB, implTokenC} {
		if !slices.Contains(got, "IMPL_TOKEN="+v) {
			t.Errorf("the second fix round's copy (%d values) lacks a value its lux Run was sent", len(got))
		}
	}
	// Each predecessor, its lux Run handed over and its output collected,
	// keeps no copy for the handover. The first fixer published nothing:
	// none at all. The implementer published its notes (text/markdown),
	// served redacted with its copy: the values it was sent itself, until
	// its retention ends.
	w.collected(fix1)
	if got := w.runCopy(fix1); len(got) != 0 {
		t.Errorf("the first fixer, handed over and collected, keeps %d values", len(got))
	}
	w.collected(impl)
	if n := w.count(`SELECT count(*) FROM artifacts WHERE run_id = $1 AND content_type LIKE 'text/%'`, impl); n == 0 {
		t.Fatal("the implementer published no text file")
	}
	if got := w.runCopyValues(impl); !slices.Equal(got, []string{"IMPL_TOKEN=" + implToken}) {
		t.Errorf("the implementer, handed over and collected, keeps %d values, want the one it was sent for its notes", len(got))
	}
	w.copyExpired(impl)
}

// (c) A reviewer given A publishes a text file holding A; A is rotated to
// B; the reviewer ends, nothing will resume it, and its output is
// collected. The file, read from lux when it is served, is still served
// redacted.
func TestATextFileServedAfterItsRunIsCollectedAndRotatedIsRedacted(t *testing.T) {
	w := newWorld(t)
	w.grant("SNOWFLAKE_PASSWORD", snowflakePassword, "reviewer")
	scripted := w.lux.Decide
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		b := scripted(spec)
		if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] == "review" {
			b.Hang = true
		}
		return b
	}
	task := w.task()
	w.deliver(task)
	var review, luxID string
	w.until("the reviewer at work", func() bool {
		_ = w.owner.QueryRow(context.Background(), `SELECT id, COALESCE(lux_run_id, '') FROM runs WHERE task_id = $1 AND phase = 'review'
			AND status = 'running' AND lux_state = 'running'`, task).Scan(&review, &luxID)
		return luxID != ""
	})
	artifact := w.publishedBy(review, luxID, "notes.txt", "connected with "+snowflakePassword+"\n")
	w.grant("SNOWFLAKE_PASSWORD", "sf-test-ROTATED-password-999", "reviewer")
	w.lux.EndTurn(luxID)
	w.collected(review)
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND (keep AND kept_until > now())`, review); n != 0 {
		t.Fatal("the reviewer is kept: this is not a Run nothing will resume")
	}
	got := w.serveArtifact(artifact)
	if got.status != 200 || string(got.body) != "connected with [REDACTED:SNOWFLAKE_PASSWORD]\n" {
		t.Errorf("served %d %q", got.status, got.body)
	}
	if strings.Contains(string(got.body), snowflakePassword) {
		t.Error("the file was served with the value the Run was given")
	}
}

// (d) An implementer given A committed it and died at its time limit (not
// kept); its output is collected and A is rotated to B. recover_run still
// scans its commits for A and refuses the recovery.
func TestARecoveryAfterARotationRefusesTheOldValue(t *testing.T) {
	w := conducting(t)
	w.grant("IMPL_TOKEN", implToken, "implementer")
	task, run, _ := w.deadWriter("config/app.toml", "password = "+implToken+"\n")
	w.collected(run)
	w.grant("IMPL_TOKEN", implTokenB, "implementer")
	id, _ := w.must(task, "recover_run", `{"run":"`+run+`"}`)["recoveryId"].(string)
	w.until("the recovery ended", func() bool {
		s := w.recoveryStatus(id)
		return s == "refused" || s == "done" || s == "failed"
	})
	if s := w.recoveryStatus(id); s != "refused" {
		t.Errorf("the recovery %s, want refused", s)
	}
	if got := w.branchAt("dude/" + task + "/recovered-" + run); got != "" {
		t.Errorf("the recovered branch reached GitHub at %s", got)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.push.refused'
		AND payload->'secrets' = '["IMPL_TOKEN"]' AND payload->'files' = '["config/app.toml"]' AND payload->>'recoveryId' = $2`, run, id); n != 1 {
		t.Errorf("%d run.push.refused naming the secret and file", n)
	}
	w.assertTaskHoldsNo(task, implToken)
}

// (e) The copy still goes. Runs ended over an hour ago, as the backstop
// finds them: one nothing can come back to loses its copy (one kept whose
// lux Run was handed to its next round too); one kept, or one whose output
// may still be read (a text file, a dead writer's commits), keeps it until
// its keep or its retention (30 days, an hour short of which it is still
// kept) ends, and then loses it (an hour past them).
func TestACopyIsKeptOnlyWhileItsRunCanComeBackAndThenDeleted(t *testing.T) {
	w := newWorld(t)
	w.grant("IMPL_TOKEN", implToken, "implementer")
	_, live, _ := w.liveImplementer()
	task := w.task()
	mk := func(id, phase, status, set string) string {
		id += "_" + w.org
		mustExec(t, w.owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, phase, status, ended_at, lux_run_id)
			VALUES ($1, $2, $3, $4, 1, $5::run_phase, $6::run_status, now() - interval '2 hours', 'lux_' || $1)`, id, w.org, w.project, task, phase, status)
		if set != "" {
			mustExec(t, w.owner, `UPDATE runs SET `+set+` WHERE id = $1`, id)
		}
		mustExec(t, w.owner, `INSERT INTO run_project_secrets (run_id, organization_id, name, value) VALUES ($1, $2, 'IMPL_TOKEN', $3)`,
			id, w.org, implToken)
		return id
	}
	art := func(runID, name, ctype string) {
		mustExec(t, w.owner, `INSERT INTO artifacts (id, organization_id, run_id, kind, name, content_type, storage_key, sha256)
			VALUES ('art_' || $1 || $2, $3, $1, 'published', $2, $4, 'key_' || $1 || $2, 'x')`, runID, name, w.org, ctype)
	}
	gone := map[string]string{
		"a completed reviewer":            mk("run_done", "review", "completed", ""),
		"a completed writer":              mk("run_wrote", "implement", "completed", ""),
		"a reviewer that published bytes": mk("run_png", "review", "completed", ""),
		"a Run let go":                    mk("run_released", "review", "aborted", "keep = false, kept_until = now(), lux_stop_reason = 'kept'"),
		"a kept Run cancelled in lux":     mk("run_cancelled", "review", "aborted", "keep = true, kept_until = now() + interval '1 day', lux_stop_reason = 'cancel'"),
		"a failed writer lux never had":   mk("run_nolux", "implement", "failed", "lux_run_id = NULL"),
		"kept, its lux Run handed over":   mk("run_handed", "implement", "completed", "keep = true, kept_until = now() + interval '1 day', lux_run_id = NULL"),
	}
	art(gone["a reviewer that published bytes"], "shot.png", "image/png")
	kept := map[string]string{
		"a kept Run":                      mk("run_kept", "review", "aborted", "keep = true, kept_until = now() + interval '1 day', lux_stop_reason = 'kept'"),
		"a kept Run not yet stopped":      mk("run_keeping", "test", "failed", "keep = true"),
		"a Run kept for its next round":   mk("run_next", "review", "completed", "keep = true, kept_until = now() + interval '1 day', lux_stop_reason = 'complete'"),
		"a failed writer":                 mk("run_dead", "implement", "failed", ""),
		"an aborted fixer":                mk("run_fixer", "fix", "aborted", ""),
		"a reviewer that wrote text":      mk("run_text", "review", "completed", ""),
		"a reviewer that wrote json":      mk("run_json", "review", "completed", ""),
		"a failed writer, an hour short":  mk("run_boundary", "implement", "failed", "ended_at = now() - interval '29 days 23 hours'"),
		"a reviewer, text, an hour short": mk("run_boundary_text", "review", "completed", "ended_at = now() - interval '29 days 23 hours'"),
	}
	// Only ended Runs lose a copy, whatever their ended_at says: one
	// running again keeps it (its age past every retention).
	stale := mk("run_again", "implement", "running", "ended_at = now() - interval '40 days'")
	art(kept["a reviewer that wrote text"], "notes.md", "text/markdown; charset=utf-8")
	art(kept["a reviewer that wrote json"], "out.json", "application/json")
	art(kept["a reviewer, text, an hour short"], "notes.txt", "text/plain")

	forget := w.forgetCopies
	forget()
	for what, id := range gone {
		if got := w.runCopy(id); len(got) != 0 {
			t.Errorf("%s keeps its copy", what)
		}
	}
	for what, id := range kept {
		if got := w.runCopy(id); len(got) != 1 {
			t.Errorf("%s lost its copy while it can still come back", what)
		}
	}
	if got := w.runCopy(live); len(got) != 1 {
		t.Error("a live Run lost its copy")
	}
	if got := w.runCopy(stale); len(got) != 1 {
		t.Error("a live Run with an ended_at of an earlier life lost its copy")
	}

	// Its keep over, its retention past: every one of them goes.
	mustExec(t, w.owner, `UPDATE runs SET kept_until = now() - interval '1 second' WHERE id = ANY($1) AND kept_until IS NOT NULL`,
		[]string{kept["a kept Run"], kept["a Run kept for its next round"]})
	mustExec(t, w.owner, `UPDATE runs SET ended_at = now() - interval '30 days 1 hour' WHERE id = ANY($1)`,
		[]string{kept["a kept Run not yet stopped"], kept["a failed writer"], kept["an aborted fixer"], kept["a reviewer that wrote text"], kept["a reviewer that wrote json"],
			kept["a failed writer, an hour short"], kept["a reviewer, text, an hour short"]})
	forget()
	for what, id := range kept {
		if got := w.runCopy(id); len(got) != 0 {
			t.Errorf("%s keeps its copy after its keep and retention ended", what)
		}
	}
	if got := w.runCopy(live); len(got) != 1 {
		t.Error("a live Run lost its copy")
	}
	if got := w.runCopy(stale); len(got) != 1 {
		t.Error("a live Run with an ended_at of an earlier life lost its copy")
	}
}

// (f) A dead writer given A committed it; its copy, past its retention, is
// gone, and A is rotated to B. recover_run, which could scan its commits
// only for B, is refused, and nothing is written or pushed.
func TestARecoveryOfARunWhoseCopyIsGoneIsRefused(t *testing.T) {
	w := conducting(t)
	w.grant("IMPL_TOKEN", implToken, "implementer")
	task, run, _ := w.deadWriter("config/app.toml", "password = "+implToken+"\n")
	w.collected(run)
	w.copyExpired(run)
	w.grant("IMPL_TOKEN", implTokenB, "implementer")
	w.recoverRefused(task, `{"run":"`+run+`"}`, "dude no longer holds every value it was given")
	for range 3 {
		w.pump()
	}
	if got := w.branchAt("dude/" + task + "/recovered-" + run); got != "" {
		t.Errorf("the recovered branch reached GitHub at %s", got)
	}
	if w.githubHolds(implToken) {
		t.Error("the old value reached GitHub")
	}

	// A Run given no secrets has nothing to scan for: still recovered.
	t.Run("given none", func(t *testing.T) {
		w := conducting(t)
		task, run, commit := w.deadWriter("WIP.md", "half of it\n")
		w.collected(run)
		mustExec(t, w.owner, `UPDATE runs SET ended_at = now() - interval '31 days' WHERE id = $1`, run)
		id, _ := w.must(task, "recover_run", `{"run":"`+run+`"}`)["recoveryId"].(string)
		w.until("the recovery done", func() bool { return w.recoveryStatus(id) == "done" })
		if got := w.branchAt("dude/" + task + "/recovered-" + run); got != commit {
			t.Errorf("the recovered branch is at %q, want %s", got, commit)
		}
	})
}

// (g) As (f), but the copy goes after recover_run accepted the recovery
// and before the worker scans it: the recovery is refused, saying why,
// nothing is pushed, and the conductor is told, with the person's warning.
func TestARecoveryWhoseCopyGoesBeforeItsScanIsRefusedUnpushed(t *testing.T) {
	w := conducting(t)
	w.grant("IMPL_TOKEN", implToken, "implementer")
	task, run, _ := w.deadWriter("config/app.toml", "password = "+implToken+"\n")
	w.collected(run)
	id, _ := w.must(task, "recover_run", `{"run":"`+run+`"}`)["recoveryId"].(string)
	w.copyExpired(run)
	w.grant("IMPL_TOKEN", implTokenB, "implementer")
	w.until("the recovery ended", func() bool {
		s := w.recoveryStatus(id)
		return s == "refused" || s == "done" || s == "failed"
	})
	if s := w.recoveryStatus(id); s != "refused" {
		t.Errorf("the recovery %s, want refused", s)
	}
	if e := w.str(`SELECT COALESCE(error, '') FROM run_recoveries WHERE id = $1`, id); e != delivery.RecoverySecretsExpired {
		t.Errorf("the recovery's error %q", e)
	}
	if got := w.branchAt("dude/" + task + "/recovered-" + run); got != "" {
		t.Errorf("the recovered branch reached GitHub at %s", got)
	}
	if pub := w.publisherOf(id); pub != nil && len(pub.PushCredentials) != 0 {
		t.Error("lux was asked to push the publisher")
	}
	if w.githubHolds(implToken) {
		t.Error("the old value reached GitHub")
	}
	w.until("the conductor told", func() bool { return w.recoveredWakes(task) == 1 })
	if line := w.str(`SELECT line FROM conductor_wakes WHERE task_id = $1 AND kind = 'recovered'`, task); !strings.Contains(line, delivery.RecoverySecretsExpired) ||
		!strings.Contains(line, "must be checked for secrets by hand before it is pushed anywhere") {
		t.Errorf("the wake says %q", line)
	}
}
