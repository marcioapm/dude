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
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

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

// collected waits until the ended Run's last exit is collected.
func (w *world) collected(runID string) {
	w.t.Helper()
	w.until("the Run's output collected", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status IN ('completed', 'failed', 'aborted')
			AND artifacts_due_at IS NULL`, runID) == 1
	})
	// One more pass of every loop, the backstop included: nothing deletes
	// the copy later than this in the window a person resumes in.
	w.pump()
	if _, err := w.syncer.ForgetSecretCopies(context.Background()); err != nil {
		w.t.Fatal(err)
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

// (b) The implementer, kept for its next round, is collected; A is rotated
// to B while the reviewer works; the fix round takes the implementer's lux
// Run over (handOverTx) and is resumed with B. The round-2 agent prints A
// and commits it: the new row's copy holds A, carried over, so the print is
// stored redacted and the push is refused.
func TestARoundHandedOverAfterARotationStillRedactsAndRefusesTheOldValue(t *testing.T) {
	w, task := stuckReview(t, 2)
	w.grant("IMPL_TOKEN", implToken, "implementer", "fixer")
	scripted := w.lux.Decide
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		b := scripted(spec)
		labels, _ := spec["labels"].(map[string]any)
		switch labels["dude.phase"] {
		case "review":
			// Between the rounds: the implementer has ended, and the fix
			// round has not yet been resumed.
			mustExec(t, w.owner, `UPDATE project_secrets SET value = $2, updated_at = now() WHERE project_id = $1 AND name = 'IMPL_TOKEN'`,
				w.project, implTokenB)
		case "fix":
			b.CallTools = append(slices.Clone(b.CallTools), gitCall("log", "-1", "--grep=remembered "+implToken),
				writeCall("config/app.toml", "password = "+implToken), commitCall("Add config"))
		}
		return b
	}
	var fix string
	w.until("the fix round failed or pushed the value", func() bool {
		fix = w.str(`SELECT COALESCE((SELECT id FROM runs WHERE task_id = $1 AND phase = 'fix' ORDER BY created_at LIMIT 1), '')`, task)
		return fix != "" && (w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status IN ('failed', 'completed')`, fix) == 1)
	})
	impl := w.str(`SELECT id FROM runs WHERE task_id = $1 AND phase = 'implement'`, task)
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND continues_run_id = $2 AND continues_how = 'resume'`, fix, impl); n != 1 {
		t.Fatalf("the fix round did not take the implementer's lux Run over")
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND payload::text LIKE '%--grep=remembered [REDACTED:IMPL_TOKEN]%'`, fix); n == 0 {
		t.Error("no stored event of the fix round shows the marker where the agent printed the old value")
	}
	w.assertTaskHoldsNo(task, implToken)
	if w.githubHolds(implToken) {
		t.Error("the old value reached GitHub")
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.push.refused'
		AND payload->'secrets' = '["IMPL_TOKEN"]'`, fix); n != 1 {
		t.Errorf("%d run.push.refused on the fix round naming IMPL_TOKEN", n)
	}
	if got := w.runCopyValues(fix); !slices.Equal(got, []string{"IMPL_TOKEN=" + implToken, "IMPL_TOKEN=" + implTokenB}) {
		t.Errorf("the fix round's copy holds %d values, want its predecessor's and its resume's", len(got))
	}
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
// finds them: one nothing can come back to loses its copy; one kept, or
// one whose output may still be read (a text file, a dead writer's
// commits), keeps it until its keep or its retention ends, and then loses
// it.
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
	}
	art(gone["a reviewer that published bytes"], "shot.png", "image/png")
	kept := map[string]string{
		"a kept Run":                    mk("run_kept", "review", "aborted", "keep = true, kept_until = now() + interval '1 day', lux_stop_reason = 'kept'"),
		"a kept Run not yet stopped":    mk("run_keeping", "test", "failed", "keep = true"),
		"a Run kept for its next round": mk("run_next", "review", "completed", "keep = true, kept_until = now() + interval '1 day', lux_stop_reason = 'complete'"),
		"a failed writer":               mk("run_dead", "implement", "failed", ""),
		"an aborted fixer":              mk("run_fixer", "fix", "aborted", ""),
		"a reviewer that wrote text":    mk("run_text", "review", "completed", ""),
		"a reviewer that wrote json":    mk("run_json", "review", "completed", ""),
	}
	art(kept["a reviewer that wrote text"], "notes.md", "text/markdown; charset=utf-8")
	art(kept["a reviewer that wrote json"], "out.json", "application/json")

	forget := func() {
		t.Helper()
		if _, err := w.syncer.ForgetSecretCopies(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
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

	// Its keep over, its retention past: every one of them goes.
	mustExec(t, w.owner, `UPDATE runs SET kept_until = now() - interval '1 second' WHERE id = ANY($1) AND kept_until IS NOT NULL`,
		[]string{kept["a kept Run"], kept["a Run kept for its next round"]})
	mustExec(t, w.owner, `UPDATE runs SET ended_at = now() - interval '31 days' WHERE id = ANY($1)`,
		[]string{kept["a kept Run not yet stopped"], kept["a failed writer"], kept["an aborted fixer"], kept["a reviewer that wrote text"], kept["a reviewer that wrote json"]})
	forget()
	for what, id := range kept {
		if got := w.runCopy(id); len(got) != 0 {
			t.Errorf("%s keeps its copy after its keep and retention ended", what)
		}
	}
	if got := w.runCopy(live); len(got) != 1 {
		t.Error("a live Run lost its copy")
	}
}
