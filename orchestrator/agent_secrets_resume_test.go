package orchestrator_test

// A resume gives the agent the project's current values, but its checkout,
// its commits and its conversation are the ones it had: a value it was given
// before the resume can still be pushed or printed after it. The Run's copy
// keeps every value it was ever sent until its output is collected.

import (
	"context"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

const implTokenD = "impl-test-not-a-real-token-DDD"

// remembersImplementer is an implementer that does calls in its first
// turn, then hangs; each input it reads after that it answers by running
// git with value on its command line, as an agent repeating a token it
// remembers from its conversation would.
func (w *world) remembersImplementer(value string, calls ...[2]string) {
	scripted := w.lux.Decide
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		b := scripted(spec)
		if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] == "implement" {
			b.Commit = nil
			b.CallTools = calls
			b.Hang = true
			b.InputTools = [][2]string{gitCall("log", "-1", "--grep=remembered "+value)}
		}
		return b
	}
}

// pausedImplementer delivers a task and pauses its implementer once dude
// has recorded where its checkout started.
func (w *world) pausedImplementer() (task, runID, luxID string) {
	w.t.Helper()
	task = w.task()
	w.deliver(task)
	w.until("the implementer's base recorded", func() bool {
		_ = w.owner.QueryRow(context.Background(), `SELECT id, lux_run_id FROM runs WHERE task_id = $1 AND phase = 'implement'
			AND base_shas <> '{}'::jsonb AND status = 'running' AND lux_run_id IS NOT NULL`, task).Scan(&runID, &luxID)
		return luxID != ""
	})
	w.pausePhase(runID)
	w.until("paused", func() bool { return w.personallyPaused(runID) })
	return task, runID, luxID
}

// remembered steers the Run and waits until the git call it answers with
// is stored.
func (w *world) remembered(runID string) {
	w.t.Helper()
	if status, body := w.call("/internal/runs/"+runID+"/steer", map[string]any{"text": "check the last commit"}); status != 201 {
		w.t.Fatalf("steer: %d %v", status, body)
	}
	w.until("the remembered value printed", func() bool {
		return w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND payload::text LIKE '%--grep=remembered %'`, runID) > 0
	})
}

// assertNotStored checks that no event of the task holds value, raw or
// encoded, and that the Run's printed command shows the marker.
func (w *world) assertNotStored(task, runID, value string) {
	w.t.Helper()
	for _, l := range leaks(value) {
		if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND payload::text LIKE '%' || $2 || '%'`, task, l); n != 0 {
			w.t.Errorf("%d of the task's events hold the value as %q", n, l)
		}
	}
	if runID == "" {
		return
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND payload::text LIKE '%--grep=remembered [REDACTED:IMPL_TOKEN]%'`, runID); n == 0 {
		w.t.Error("no stored event shows the marker where the agent printed the value")
	}
}

// A value committed and taken out again before a pause, then rotated: the
// resumed Run's history still holds the old value, and the push is refused.
func TestAValueCommittedBeforeAResumeStillRefusesThePush(t *testing.T) {
	w := newWorld(t)
	w.grant("IMPL_TOKEN", implToken, "implementer")
	w.remembersImplementer(implToken,
		writeCall("config/app.toml", "password = "+implToken), commitCall("Add config"),
		gitCall("rm", "-q", "config/app.toml"), writeCall("README.md", "reads IMPL_TOKEN from the environment"), commitCall("Drop config"))
	task, runID, _ := w.pausedImplementer()
	w.grant("IMPL_TOKEN", implTokenB, "implementer")
	w.resumePhase(runID)
	if status, body := w.call("/internal/runs/"+runID+"/steer", map[string]any{"text": "finish"}); status != 201 {
		t.Fatalf("steer: %d %v", status, body)
	}
	pushed := func() string {
		out, _ := exec.Command("git", "-C", w.gh.Repo, "for-each-ref", "--format=%(refname)", "refs/heads/dude/").Output()
		var runs []string
		for _, ref := range strings.Fields(string(out)) {
			if strings.Contains(ref, "/run-") {
				runs = append(runs, ref)
			}
		}
		return strings.Join(runs, " ")
	}
	w.until("the implementer failed or pushed", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'failed'`, runID) == 1 || pushed() != ""
	})
	if branch := pushed(); branch != "" {
		t.Fatalf("the work reached GitHub as %s", branch)
	}
	if lr := w.luxRunFor(runID); len(lr.PushCredentials) != 0 {
		t.Errorf("lux was asked to push %d times", len(lr.PushCredentials))
	}
	var reason string
	_ = w.owner.QueryRow(context.Background(), `SELECT error FROM runs WHERE id = $1`, runID).Scan(&reason)
	if !strings.Contains(reason, "IMPL_TOKEN in config/app.toml") {
		t.Errorf("the reason does not name the secret and the committed file: %q", reason)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.push.refused'
		AND payload->'secrets' = '["IMPL_TOKEN"]' AND payload->'files' = '["config/app.toml"]'`, runID); n != 1 {
		t.Errorf("%d run.push.refused naming IMPL_TOKEN and the committed file", n)
	}
	w.assertNotStored(task, "", implToken)
}

// Given A, paused before printing anything, rotated to B and resumed: the
// agent still remembers A and prints it, and dude stores the marker.
func TestAValueGivenBeforeAResumeIsStillRedacted(t *testing.T) {
	w := newWorld(t)
	w.grant("IMPL_TOKEN", implToken, "implementer")
	w.remembersImplementer(implToken)
	task, runID, luxID := w.pausedImplementer()
	w.grant("IMPL_TOKEN", implTokenB, "implementer")
	w.resumePhase(runID)
	if sent := secretsByName(w.lux.Runs()[0].ResumeSecrets[0]); w.lux.Runs()[0].ID != luxID || sent["IMPL_TOKEN"] != implTokenB {
		t.Fatal("the resume did not send the rotated value")
	}
	w.remembered(runID)
	w.assertNotStored(task, runID, implToken)
	if got := w.runCopyValues(runID); !slices.Equal(got, []string{"IMPL_TOKEN=" + implToken, "IMPL_TOKEN=" + implTokenB}) {
		t.Errorf("copy after the resume = %d values, want the submit's and the resume's", len(got))
	}
}

// A resume sending B is taken by lux but its answer is lost; rotated to C,
// the retry is answered with the resume lux already holds, which has B; then
// rotated to D. The agent prints B, and dude stores the marker.
func TestAValueOfAResumeWhoseAnswerWasLostIsRedacted(t *testing.T) {
	w := newWorld(t)
	w.grant("IMPL_TOKEN", implToken, "implementer")
	w.remembersImplementer(implTokenB)
	task, runID, luxID := w.pausedImplementer()
	// The resumed placement waits for a host until the retry is answered,
	// so lux is still resuming when it comes.
	hold := make(chan struct{})
	w.lux.BeforeAssign(func(id string) {
		if id == luxID {
			<-hold
		}
	})
	released := false
	release := func() {
		if !released {
			released = true
			close(hold)
		}
	}
	defer release()
	lost := &lostAnswer{Client: w.syncer.Lux}
	lost.left.Store(1)
	w.syncer.Lux = lost
	w.grant("IMPL_TOKEN", implTokenB, "implementer")
	if status, body := w.call("/internal/runs/"+runID+"/resume", map[string]any{}); status != 200 {
		t.Fatalf("resume: %d %v", status, body)
	}
	w.until("lux took the first resume", func() bool { return len(w.lux.ResumeInputs(luxID)) == 1 })
	w.grant("IMPL_TOKEN", implTokenC, "implementer")
	mustExec(t, w.owner, `UPDATE runs SET next_attempt_at = NULL WHERE id = $1`, runID)
	w.until("the retry answered", func() bool { return lost.left.Load() < 0 })
	w.grant("IMPL_TOKEN", implTokenD, "implementer")
	release()
	w.until("the Run to run again", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running'`, runID) == 1 && w.lux.State(luxID) == "running"
	})
	lr := w.luxRunFor(runID)
	if n := len(w.lux.ResumeInputs(luxID)); n != 1 || secretsByName(lr.ResumeSecrets[0])["IMPL_TOKEN"] != implTokenB {
		t.Fatalf("lux took %d resumes; want only the first, with B", n)
	}
	w.remembered(runID)
	w.assertNotStored(task, runID, implTokenB)
	if got := w.runCopyValues(runID); !slices.Equal(got, []string{"IMPL_TOKEN=" + implToken, "IMPL_TOKEN=" + implTokenB, "IMPL_TOKEN=" + implTokenC}) {
		t.Errorf("copy = %d values, want every value the Run was sent", len(got))
	}
}
