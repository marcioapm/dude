package orchestrator_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
)

// The second review's probes: one answer, one dispatch, even when two
// calls race (F1); a refused POST frees the answer, a POST whose answer
// was lost is "may or may not", and a reservation dude never finished is
// ended unknown and never sent again.

// approvedDeploy is an answered question approving deploy.yml on branch.
func (w *world) approvedDeploy(task, branch, id string) {
	w.t.Helper()
	approve := delivery.DispatchApproval("deploy.yml", branch)
	mustExec(w.t, w.owner, `INSERT INTO questions (id, organization_id, task_id, prompt, options, status, answer, answered_at, addressee)
		VALUES ($1, $2, $3, 'Deploy?', $4::jsonb, 'answered', $5, now(), 'person')`, id, w.org, task,
		`["`+approve+`", "`+delivery.DispatchDecline+`"]`, approve)
}

// Two run_workflow calls naming the same answered question at once: the
// first reserves its row before its POST, which GitHub holds; the second
// is refused while that POST is still held, so it never posts. Exactly one
// POST, one row.
func TestProbeOneAnswerTwoConcurrentDispatches(t *testing.T) {
	w, task, branch := dispatchWorld(t)
	w.approvedDeploy(task, branch, "q_once")
	posting, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	w.gh.Set(func(s *fakegithub.Server) {
		s.Intercept = func(r *http.Request) int {
			if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/dispatches") {
				once.Do(func() { close(posting) })
				<-release
			}
			return 0
		}
	})
	type result struct {
		status int
		out    map[string]any
	}
	results := make(chan result, 2)
	args := `{"workflow":"deploy.yml","questionId":"q_once","inputs":{"env":"production"}}`
	// The spec read once: w.owner is one connection, not for goroutines.
	spec := w.conductorSpecOf(task)
	for range 2 {
		go func() {
			status, body := w.callTool(w.syncer.Agent.ToolsURL, spec, "run_workflow", args)
			var out map[string]any
			_ = json.Unmarshal([]byte(body), &out)
			results <- result{status, out}
		}()
	}
	select {
	case <-posting:
	case <-time.After(60 * time.Second):
		close(release)
		t.Fatal("neither call posted")
	}
	// The other call answers while the first POST is still held.
	var refused result
	select {
	case refused = <-results:
	case <-time.After(60 * time.Second):
		close(release)
		t.Fatal("the second call was not refused before the first POST ended")
	}
	close(release)
	accepted := <-results
	if refused.status != 422 || !strings.Contains(refused.out["error"].(string), "q_once") {
		t.Fatalf("the second call: %d %v", refused.status, refused.out)
	}
	if accepted.status != 200 || accepted.out["dispatchId"] == nil {
		t.Fatalf("the first call: %d %v", accepted.status, accepted.out)
	}
	w.gh.Set(func(s *fakegithub.Server) {
		s.Intercept = nil
		if len(s.Dispatches) != 1 {
			t.Errorf("%d POSTs for one answer", len(s.Dispatches))
		}
	})
	if n := w.count(`SELECT count(*) FROM workflow_dispatches WHERE question_id = 'q_once'`); n != 1 {
		t.Fatalf("%d rows for one answer", n)
	}
	if got := w.dispatchRow(accepted.out["dispatchId"].(string)); !strings.HasPrefix(got, "bound ") {
		t.Fatalf("the dispatch: %s", got)
	}
}

// GitHub refusing the POST (422): no workflow ran. The row ends failed,
// with GitHub's error, the call is refused, and the answer is not spent: a
// retry with it dispatches. A call refused before anything was sent
// leaves no row.
func TestARefusedDispatchFreesTheAnswer(t *testing.T) {
	w, task, branch := dispatchWorld(t)
	w.approvedDeploy(task, branch, "q_retry")
	w.gh.Set(func(s *fakegithub.Server) { s.DispatchStatus = 422 })
	w.refused(task, "run_workflow", `{"workflow":"deploy.yml","questionId":"q_retry"}`, "422")
	if got := w.str(`SELECT state || ': ' || last_error FROM workflow_dispatches WHERE question_id = 'q_retry'`); !strings.HasPrefix(got, "failed: ") ||
		!strings.Contains(got, "422") {
		t.Fatalf("refused POST: %s", got)
	}
	w.refused(task, "run_workflow", `{"workflow":"lint.yml"}`, "has no workflow_dispatch trigger")
	if n := w.count(`SELECT count(*) FROM workflow_dispatches WHERE workflow = 'lint.yml'`); n != 0 {
		t.Fatalf("a call refused before any POST left %d rows", n)
	}
	w.gh.Set(func(s *fakegithub.Server) { s.DispatchStatus = 0 })
	id := w.dispatched(task, `{"workflow":"deploy.yml","questionId":"q_retry"}`)
	if got := w.dispatchRow(id); !strings.HasPrefix(got, "bound ") {
		t.Fatalf("the retry: %s", got)
	}
	w.refused(task, "run_workflow", `{"workflow":"deploy.yml","questionId":"q_retry"}`, "q_retry already authorized dispatch "+id)
	if len(w.workflowWakes(task)) != 0 {
		t.Fatal("a refused POST woke the conductor")
	}
}

// A POST whose answer failed (a 502: GitHub may have started the run) is
// unknown, "may or may not", told in the call's refusal (no wake besides),
// and spends the answer: dude never sends it again.
func TestADispatchWhoseAnswerFailedIsMaybe(t *testing.T) {
	w, task, branch := dispatchWorld(t)
	w.approvedDeploy(task, branch, "q_maybe")
	w.gh.Set(func(s *fakegithub.Server) { s.DispatchStatus = http.StatusBadGateway })
	w.refused(task, "run_workflow", `{"workflow":"deploy.yml","questionId":"q_maybe"}`, "dude may or may not have started it")
	if got := w.str(`SELECT state || ' ' || woken_outcome || ': ' || reason FROM workflow_dispatches WHERE question_id = 'q_maybe'`); !strings.HasPrefix(got,
		"unknown unknown: dude may or may not have dispatched this") {
		t.Fatalf("lost answer: %s", got)
	}
	w.gh.Set(func(s *fakegithub.Server) { s.DispatchStatus = 0 })
	w.refused(task, "run_workflow", `{"workflow":"deploy.yml","questionId":"q_maybe"}`, "q_maybe already authorized dispatch")
	w.supervise()
	if n := len(w.workflowWakes(task)); n != 0 {
		t.Fatalf("%d wakes beside the call's refusal", n)
	}
}

// dude stopped between the reserve and the POST (or never heard GitHub):
// the row is left dispatching. Within DispatchReservedFor the supervisor
// leaves it (the call may still be posting); past it, the row ends unknown,
// "may or may not", never posted again, and the conductor is woken once.
func TestAReservationLeftDispatchingEndsUnknown(t *testing.T) {
	w, task, branch := dispatchWorld(t)
	w.approvedDeploy(task, branch, "q_crash")
	w.gh.Set(func(s *fakegithub.Server) { s.DispatchRunID = 2300 })
	id := w.dispatched(task, `{"workflow":"ci.yml"}`)
	mustExec(t, w.owner, `INSERT INTO workflow_dispatches (id, organization_id, task_id, repository_id, workflow, ref, question_id, state)
		SELECT 'wfd_crash', organization_id, task_id, repository_id, 'deploy.yml', ref, 'q_crash', 'dispatching' FROM workflow_dispatches WHERE id = $1`, id)
	w.supervise()
	if got := w.dispatchRow("wfd_crash"); got != "dispatching none" {
		t.Fatalf("within the bound: %s", got)
	}
	mustExec(t, w.owner, `UPDATE workflow_dispatches SET dispatched_at = now() - interval '6 minutes', last_read_at = NULL WHERE id = 'wfd_crash'`)
	for range 3 {
		w.supervise()
		mustExec(t, w.owner, `UPDATE workflow_dispatches SET last_read_at = NULL WHERE id = 'wfd_crash'`)
	}
	if got := w.str(`SELECT state || ': ' || reason FROM workflow_dispatches WHERE id = 'wfd_crash'`); !strings.HasPrefix(got,
		"unknown: dude may or may not have dispatched this") {
		t.Fatalf("past the bound: %s", got)
	}
	wakes := w.wakesNaming(task, "wfd_crash")
	if len(wakes) != 1 || !strings.Contains(wakes[0], "may or may not") {
		t.Fatalf("wakes %q", wakes)
	}
	w.gh.Set(func(s *fakegithub.Server) {
		if len(s.Dispatches) != 1 {
			t.Errorf("re-posted: %d POSTs", len(s.Dispatches))
		}
	})
	// The answer is spent: dude cannot tell that it was not used.
	w.refused(task, "run_workflow", `{"workflow":"deploy.yml","questionId":"q_crash"}`, "q_crash already authorized dispatch wfd_crash")
}

// F4: a wait's digest is clipped to its bound. Each workflow item is
// bounded (200 runes), so eight long outcomes fit the digest, with or
// without the longest note; every outcome reaches the conductor exactly
// once, in the digest or by a later wake. (An item cut from a digest
// anyway stays owed: delivery's TestAnItemCutFromTheDigestIsStillOwed.)
func TestProbeClippedDigestMarksAllDelivered(t *testing.T) {
	for _, note := range []string{"", strings.Repeat("n", delivery.WaitNoteMax)} {
		t.Run(map[bool]string{true: "no note", false: "long note"}[note == ""], func(t *testing.T) {
			w, task, _ := dispatchWorld(t)
			w.gh.Set(func(s *fakegithub.Server) { s.DispatchRunID = 2400 })
			first := w.dispatched(task, `{"workflow":"ci.yml"}`)
			ids := []string{first}
			for i := 1; i < delivery.WaitMaxConditions; i++ {
				id := fmt.Sprintf("wfd_clip%d", i)
				mustExec(t, w.owner, `INSERT INTO workflow_dispatches (id, organization_id, task_id, repository_id, workflow, ref, state, run_id)
					SELECT $2, organization_id, task_id, repository_id, workflow, ref, 'bound', 2500 + $3::int FROM workflow_dispatches WHERE id = $1`,
					first, id, i)
				ids = append(ids, id)
			}
			var conds []string
			for _, id := range ids {
				conds = append(conds, `{"workflowEnded":"`+id+`"}`)
			}
			args, _ := json.Marshal(map[string]any{"conditions": json.RawMessage("[" + strings.Join(conds, ",") + "]"), "deadline": "30m", "note": note})
			out := w.must(task, "wait_for", string(args))
			waitID, _ := out["waitId"].(string)
			// All end unknown, each with a long reason, while the wait is open.
			mustExec(t, w.owner, `UPDATE workflow_dispatches SET state = 'unknown', outcome = 'unknown', reason = $2 WHERE task_id = $1`,
				task, strings.Repeat("r", 900))
			if err := w.prs.DB.InOrg(context.Background(), w.org, func(tx pgx.Tx) error {
				_, err := delivery.EvaluateWaitTx(context.Background(), tx, w.org, task, waitID)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			digest := w.str(`SELECT line FROM conductor_wakes WHERE task_id = $1 AND key = $2`, task, "wait:"+waitID)
			var inDigest []string
			for _, id := range ids {
				if strings.Contains(digest, "("+id+")") {
					inDigest = append(inDigest, id)
				}
			}
			// Bounded items come before the note, so the clip takes the
			// note first: all eight fit either way.
			if len(inDigest) != len(ids) {
				t.Fatalf("eight bounded items did not fit: %d in %q", len(inDigest), digest)
			}
			for range 3 {
				mustExec(t, w.owner, `UPDATE workflow_dispatches SET last_read_at = NULL WHERE task_id = $1`, task)
				w.supervise()
			}
			wakes := w.workflowWakes(task)
			for _, id := range ids {
				told := 0
				if slices.Contains(inDigest, id) {
					told++
				}
				for _, l := range wakes {
					if strings.Contains(l, "("+id+")") {
						told++
					}
				}
				if told != 1 {
					t.Errorf("%s told %d times (digest %q, wakes %q)", id, told, digest, wakes)
				}
			}
		})
	}
}

// F6: free CI is decided against the base the task's change goes into: its
// pull request's base when it has one, else its delivery's base for the
// repository, else the repository's default branch. A workflow whose only
// trigger for the change is a pull_request filtered to one branch is free
// against that base, and needs the person against any other.
func TestFreeCIIsDecidedAgainstTheChangesBase(t *testing.T) {
	prOnly := func(base string) string {
		return "name: PR\non:\n  pull_request:\n    branches: [" + base + "]\n  workflow_dispatch:\njobs: {}\n"
	}
	for _, c := range []struct{ name, pr, delivery, free, notFree string }{
		{"the pull request's base", "main", "develop", "main", "develop"},
		{"the delivery's base", "", "develop", "develop", "main"},
		{"the default branch", "", "", "main", "develop"},
	} {
		t.Run(c.name, func(t *testing.T) {
			w, task, branch := dispatchWorld(t)
			repo := w.str(`SELECT r.name FROM repositories r JOIN pull_requests p ON p.repository_id = r.id WHERE p.task_id = $1`, task)
			if def := w.str(`SELECT default_branch FROM repositories WHERE name = $1 AND organization_id = $2`, repo, w.org); def != "main" {
				t.Fatalf("default branch %q", def)
			}
			if c.pr == "" {
				mustExec(t, w.owner, `DELETE FROM pull_requests WHERE task_id = $1`, task)
			} else {
				mustExec(t, w.owner, `UPDATE pull_requests SET base_branch = $2 WHERE task_id = $1`, task, c.pr)
			}
			bases := "{}"
			if c.delivery != "" {
				bases = `{"` + repo + `": "` + c.delivery + `"}`
			}
			mustExec(t, w.owner, `UPDATE workflow_runs SET state = jsonb_set(state, '{bases}', $2::jsonb) WHERE task_id = $1`, task, bases)
			w.gh.CommitFiles(branch, "pr workflows", map[string]string{".github/workflows/free.yml": prOnly(c.free),
				".github/workflows/other.yml": prOnly(c.notFree)})
			w.refused(task, "run_workflow", `{"workflow":"other.yml"}`, "a pull request into "+c.free+"), or dude cannot tell")
			id := w.dispatched(task, `{"workflow":"free.yml"}`)
			if got := w.str(`SELECT COALESCE(question_id, 'free') FROM workflow_dispatches WHERE id = $1`, id); got != "free" {
				t.Fatalf("free.yml: %s", got)
			}
		})
	}
}
