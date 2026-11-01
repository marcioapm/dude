package orchestrator_test

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakeagent"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

// Each fix publishes a different tree, so every judgement follows a real
// writer round rather than a synthetic completed Run.
func convergenceScript(w *world, review func(int) string) {
	w.t.Helper()
	var round int
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		labels, _ := spec["labels"].(map[string]any)
		switch labels["dude.phase"] {
		case "review":
			round++
			return fakelux.Behaviour{Reply: review(round)}
		case "implement", "fix":
			return fakelux.Behaviour{Commit: map[string]string{"FACTORY.md": fmt.Sprint(labels["dude.run"]) + "\n"}, Message: "work", Checks: fakeagent.NoChecks}
		}
		return fakelux.Behaviour{Reply: "Nothing to simplify."}
	}
}

// stillScript is a first review raising F1, with its acceptance criterion
// or without, and every later review judging it still.
func stillScript(w *world, criterion bool) {
	finding := fakeagent.Finding
	if !criterion {
		finding = strings.Replace(finding, "acceptance: "+fakeagent.FindingAcceptance+"\n", "", 1)
	}
	convergenceScript(w, func(round int) string {
		if round == 1 {
			return "```yaml\n" + finding + "```\n"
		}
		return "```yaml\nverdicts:\n  F1: still\n```\n"
	})
}

func startConvergence(w *world, p delivery.Policy) string {
	w.t.Helper()
	// Keep the project's resolved policy and the workflow's frozen policy
	// aligned, including the continuation policy used by phase creation.
	raw, err := json.Marshal(p)
	if err != nil {
		w.t.Fatal(err)
	}
	mustExec(w.t, w.owner, `UPDATE projects SET delivery_policy = $2 WHERE id = $1`, w.project, raw)
	w.onModel("reviewer", "llm-review")
	w.onModel("fixer", "llm-fix")
	wi := w.task()
	w.names(wi, w.repoID)
	w.start(wi, p)
	return wi
}

func convergenceStop(w *world, wi, reason string, fixes int) delivery.LoopExit {
	w.t.Helper()
	w.until("convergence escalation or a pull request", func() bool {
		return w.taskStatus(wi) == "awaiting_input" || len(w.gh.Pulls()) > 0
	})
	_, e := w.escalationOf(wi)
	if e.Reason != reason || w.taskStatus(wi) != "awaiting_input" {
		w.t.Fatalf("escalation = %+v, task = %s; want %s awaiting input", e, w.taskStatus(wi), reason)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'fix'`, wi); n != fixes {
		w.t.Errorf("%d fix rows, want exactly %d", n, fixes)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase IN ('simplify', 'test')`, wi); n != 0 || len(w.gh.Pulls()) != 0 {
		w.t.Errorf("escalated delivery progressed: %d simplify/test rows, %d pull requests", n, len(w.gh.Pulls()))
	}
	raw, err := json.Marshal(e.Detail)
	if err != nil {
		w.t.Fatal(err)
	}
	var exit delivery.LoopExit
	if err := json.Unmarshal(raw, &exit); err != nil {
		w.t.Fatal(err)
	}
	if exit.Reason != reason {
		w.t.Errorf("loop exit = %+v, want %s", exit, reason)
	}
	return exit
}

func TestConvergenceCriterionStillRounds(t *testing.T) {
	for _, resume := range []bool{false, true} {
		t.Run(fmt.Sprintf("resume=%v", resume), func(t *testing.T) {
			w := newWorld(t)
			stillScript(w, true)
			p := scriptedPolicy()
			p.ResumeRounds = resume
			p.MaxAttemptsPerFinding = 4
			wi := startConvergence(w, p)
			exit := convergenceStop(w, wi, "not_converging", 2)
			var id string
			var attempts int
			if err := w.owner.QueryRow(context.Background(), `SELECT id, fix_attempts FROM review_findings WHERE task_id = $1`, wi).Scan(&id, &attempts); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(exit.FindingIDs, []string{id}) || attempts != 2 {
				t.Errorf("exit = %+v, attempts = %d; want only %s after two attempts", exit, attempts, id)
			}
			reviews := w.reviewRows(wi)
			if len(reviews) != 3 {
				t.Fatalf("review rows = %+v, want initial review and two re-reviews", reviews)
			}
			for i, r := range reviews {
				wantHow := ""
				if resume && i > 0 {
					wantHow = delivery.ContinuesResume
				}
				if r.How != wantHow {
					t.Errorf("review %d continues_how = %q, want %q", i, r.How, wantHow)
				}
				if resume && i > 0 && r.Continues != reviews[i-1].ID {
					t.Errorf("review %d did not continue the previous review: %+v", i, reviews)
				}
			}
			if resume {
				luxReviews := w.luxRunsOf("review")
				if len(luxReviews) != 1 || luxReviews[0].Resumed != 2 {
					t.Errorf("reviewers on lux = %+v, want one resumed twice", luxReviews)
				}
			}
		})
	}
}

func TestConvergenceStillRequiresCriterionAndConsecutiveVerdicts(t *testing.T) {
	for _, tc := range []struct {
		name, reason                 string
		criterion, disabled, silence bool
		fixes, attempts              int
	}{
		{name: "absent criterion reaches attempt bound", reason: "stuck", fixes: 4, attempts: 4},
		{name: "disabled reaches attempt bound", criterion: true, disabled: true, reason: "stuck", fixes: 4, attempts: 4},
		{name: "silence breaks streak", criterion: true, silence: true, reason: "not_converging", fixes: 4, attempts: 6},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t)
			convergenceScript(w, func(round int) string {
				if round == 1 {
					finding := fakeagent.Finding
					if !tc.criterion {
						finding = strings.Replace(finding, "acceptance: "+fakeagent.FindingAcceptance+"\n", "", 1)
					}
					return "```yaml\n" + finding + "```\n"
				}
				if tc.silence && round == 3 {
					return "No new findings."
				}
				return "```yaml\nverdicts:\n  F1: still\n```\n"
			})
			p := scriptedPolicy()
			p.MaxAttemptsPerFinding = tc.attempts
			p.MaxReviewIterations = 8
			if tc.disabled {
				p.MaxStillRounds, p.MaxRegressionRounds = 0, 0
			}
			wi := startConvergence(w, p)
			convergenceStop(w, wi, tc.reason, tc.fixes)
			if tc.silence {
				var verdicts map[string]string
				if err := w.owner.QueryRow(context.Background(), `SELECT verdicts FROM runs WHERE task_id = $1 AND phase = 'review' ORDER BY created_at, id OFFSET 2 LIMIT 1`, wi).Scan(&verdicts); err != nil {
					t.Fatal(err)
				}
				if len(verdicts) != 0 {
					t.Errorf("silent round stored verdicts %v", verdicts)
				}
			}
		})
	}
}

func TestConvergenceDecisionsReachNextRound(t *testing.T) {
	for _, resume := range []bool{false, true} {
		for _, action := range []string{"retry", "amend", "accept"} {
			t.Run(fmt.Sprintf("resume=%v/%s", resume, action), func(t *testing.T) {
				w := newWorld(t)
				stillScript(w, true)
				p := scriptedPolicy()
				p.ResumeRounds, p.MaxAttemptsPerFinding = resume, 4
				wi := startConvergence(w, p)
				convergenceStop(w, wi, "not_converging", 2)
				owner, _, member := w.people(wi)
				for _, note := range []string{"", " \n\t"} {
					if code, out := w.decide(owner, wi, action, note); code != 400 {
						t.Fatalf("blank %s: %d %v, want 400", action, code, out)
					}
				}
				const note = "Record the rollback outcome before returning success."
				if code, out := w.decide(member, wi, action, note); code != 403 {
					t.Fatalf("non-owner %s: %d %v, want 403", action, code, out)
				}
				if code, out := w.decide(owner, wi, "unknown", note); code != 400 {
					t.Fatalf("unknown action: %d %v, want 400", code, out)
				}
				if code, out := w.decide(owner, wi, action, note); code != 200 {
					t.Fatalf("%s: %d %v", action, code, out)
				}
				if action == "accept" {
					w.until("accepted risk reaches pull request", func() bool { return len(w.gh.Pulls()) == 1 })
					if n := w.count(`SELECT count(*) FROM review_findings WHERE task_id = $1 AND status = 'accepted'`, wi); n != 1 {
						t.Errorf("%d accepted findings, want one", n)
					}
					if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'fix'`, wi); n != 2 {
						t.Errorf("accept sent another fixer: %d rows", n)
					}
					return
				}
				w.until("two new judgements after direction", func() bool {
					return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'fix'`, wi) >= 3 && w.taskStatus(wi) == "awaiting_input"
				})
				convergenceStop(w, wi, "not_converging", 4)
				var acceptance string
				if err := w.owner.QueryRow(context.Background(), `SELECT acceptance FROM review_findings WHERE task_id = $1`, wi).Scan(&acceptance); err != nil {
					t.Fatal(err)
				}
				want := fakeagent.FindingAcceptance
				if action == "amend" {
					want = note
				}
				if acceptance != want {
					t.Errorf("criterion = %q, want %q", acceptance, want)
				}
				var messages []string
				for _, phase := range []string{"implement", "fix"} {
					for _, run := range w.luxRunsOf(phase) {
						messages = append(messages, run.Prompt())
						messages = append(messages, w.lux.Inputs(run.ID)...)
					}
				}
				if !slices.ContainsFunc(messages, func(message string) bool { return strings.Contains(message, note) }) {
					t.Errorf("next writer never received owner direction: %q", messages)
				}
				var criteria []string
				if err := w.owner.QueryRow(context.Background(), `SELECT acceptance_criteria FROM tasks WHERE id = $1`, wi).Scan(&criteria); err != nil {
					t.Fatal(err)
				}
				if !slices.Equal(criteria, []string{"it greets"}) {
					t.Errorf("task criteria changed: %q", criteria)
				}
			})
		}
	}
}

func TestConvergenceRegressionUsesProjectBlockingSeverities(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("disabled=%v", disabled), func(t *testing.T) {
			w := newWorld(t)
			convergenceScript(w, func(round int) string {
				if round == 1 {
					return "```yaml\nseverity: medium\ncategory: correctness\ntitle: Original defect\ndescription: Observable failure.\n```\n"
				}
				if disabled && round == 4 {
					return "```yaml\nverdicts:\n  F1: fixed\n```\n"
				}
				return fmt.Sprintf("```yaml\nverdicts:\n  F1: fixed\n---\nseverity: medium\ncategory: correctness\ntitle: Regression %d\ndescription: A new observable failure.\ncaused_by_fix: yes\n```\n", round-1)
			})
			p := scriptedPolicy()
			p.BlockingSeverities = []string{"blocking", "high", "medium"}
			p.MaxAttemptsPerFinding = 4
			if disabled {
				p.MaxRegressionRounds = 0
			}
			wi := startConvergence(w, p)
			if disabled {
				w.until("a pull request or escalation", func() bool { return len(w.gh.Pulls()) > 0 || w.taskStatus(wi) == "awaiting_input" })
				if len(w.gh.Pulls()) != 1 || w.escalationReason(wi) != "" {
					t.Fatalf("disabled regression detector stopped: %s", w.escalationReason(wi))
				}
				if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'fix'`, wi); n != 3 {
					t.Errorf("%d fixes, want three", n)
				}
				return
			}
			exit := convergenceStop(w, wi, "not_converging", 2)
			var id string
			if err := w.owner.QueryRow(context.Background(), `SELECT id FROM review_findings WHERE task_id = $1 AND title = 'Regression 2' AND status = 'open' AND caused_by_fix`, wi).Scan(&id); err != nil {
				t.Fatal(err)
			}
			if exit.RegressionRounds != 2 || !slices.Equal(exit.FindingIDs, []string{id}) {
				t.Errorf("exit = %+v, want two regression rounds naming only %s", exit, id)
			}
			if n := w.count(`SELECT count(*) FROM review_findings WHERE task_id = $1 AND status = 'resolved'`, wi); n != 2 {
				t.Errorf("%d resolved predecessors, want two", n)
			}
		})
	}
}

// nextFixInput is what the task's latest fix row's agent was last given: a
// resumed fixer's round message, or a fresh fixer's prompt.
func (w *world) nextFixInput(wi string) (how, input string) {
	w.t.Helper()
	var luxID string
	if err := w.owner.QueryRow(context.Background(), `SELECT COALESCE(continues_how, ''),
			COALESCE((SELECT payload->>'luxRunId' FROM events e WHERE e.run_id = r.id AND e.event_type = 'run.lease.acquired'), r.lux_run_id, '')
		FROM runs r WHERE task_id = $1 AND phase = 'fix' ORDER BY created_at DESC, id DESC LIMIT 1`, wi).Scan(&how, &luxID); err != nil {
		w.t.Fatal(err)
	}
	for _, r := range w.lux.Runs() {
		if r == nil || r.ID != luxID {
			continue
		}
		if how == delivery.ContinuesResume {
			inputs := w.lux.Inputs(r.ID)
			if len(inputs) == 0 {
				w.t.Fatalf("resumed fixer %s was given no round message", luxID)
			}
			return how, inputs[len(inputs)-1]
		}
		return how, r.Prompt()
	}
	w.t.Fatalf("no lux Run %q for the latest fix", luxID)
	return "", ""
}

// directionReachesNextFix waits for the fix after a decision to be judged,
// then checks the direction is in that fixer's own input.
func (w *world) directionReachesNextFix(wi string, fixes int, resume bool, note string) {
	w.t.Helper()
	w.until("the directed fix judged", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'fix'`, wi) > fixes &&
			w.count(`SELECT count(*) FROM runs r WHERE r.task_id = $1 AND r.phase = 'review' AND r.status = 'completed'
				AND r.parent_run_id = (SELECT id FROM runs WHERE task_id = $1 AND phase = 'fix' ORDER BY created_at DESC, id DESC LIMIT 1)`, wi) > 0
	})
	how, input := w.nextFixInput(wi)
	// A decision releases kept Runs (ReleaseKeptTx), so under resumed rounds
	// the directed fixer replays its predecessor with a fresh prompt.
	if want := map[bool]string{true: delivery.ContinuesReplay, false: ""}[resume]; how != want {
		w.t.Fatalf("next fix continues_how = %q, want %q", how, want)
	}
	if !strings.Contains(input, note) {
		w.t.Errorf("next fixer's input lacks the direction %q:\n%s", note, input)
	}
}

// A stuck retry takes the owner's direction like a convergence retry: a
// missing or blank note is a 400 that records nothing, the retry stays the
// owner's (an admin's say is still the test waiver alone), and the note is
// the next fixer's actual input, with rounds resumed or not.
func TestStuckRetryNeedsDirectionThatReachesTheNextFixer(t *testing.T) {
	for _, resume := range []bool{false, true} {
		t.Run(fmt.Sprintf("resume=%v", resume), func(t *testing.T) {
			w := newWorld(t)
			stillScript(w, false)
			p := scriptedPolicy()
			p.ResumeRounds, p.MaxAttemptsPerFinding = resume, 1
			wi := startConvergence(w, p)
			convergenceStop(w, wi, "stuck", 1)
			owner, admin, member := w.people(wi)
			for _, note := range []string{"", " \n\t"} {
				code, out := w.decide(owner, wi, "retry", note)
				if e, _ := out["error"].(map[string]any); code != 400 || e["code"] != "bad_request" {
					t.Fatalf("stuck retry with note %q: %d %v, want 400 bad_request", note, code, out)
				}
			}
			if d := w.decided(wi); d != nil {
				t.Fatalf("a refused retry was recorded: %v", d)
			}
			const note = "Write FACTORY.md from the review's own wording."
			for _, as := range []string{admin, member} {
				if code, out := w.decide(as, wi, "retry", note); code != 403 {
					t.Fatalf("a non-owner's stuck retry: %d %v, want 403", code, out)
				}
			}
			if code, out := w.decide(owner, wi, "retry", note); code != 200 {
				t.Fatalf("stuck retry: %d %v", code, out)
			}
			w.directionReachesNextFix(wi, 1, resume, note)
		})
	}
}

// Stop, then Try again, on a loop stopped not converging or stuck takes the
// owner's direction as the banner's retry does: no note is a 400 that picks
// nothing up; the note reaches the next fixer; and a not_converging loop
// needs two new still verdicts before it escalates again.
func TestTryAgainAfterStoppingAStalledLoopNeedsDirection(t *testing.T) {
	for _, reason := range []string{delivery.ReasonNotConverging, "stuck"} {
		t.Run(reason, func(t *testing.T) {
			w := newWorld(t)
			stillScript(w, reason != "stuck")
			p := scriptedPolicy()
			fixes := 2
			p.MaxAttemptsPerFinding = 4
			if reason == "stuck" {
				p.MaxAttemptsPerFinding, fixes = 1, 1
			}
			wi := startConvergence(w, p)
			convergenceStop(w, wi, reason, fixes)
			owner, _, _ := w.people(wi)
			if code, out := w.decide(owner, wi, "stop", ""); code != 200 {
				t.Fatalf("stop: %d %v", code, out)
			}
			w.until("stopped", func() bool { return w.taskStatus(wi) == "aborted" })
			for _, note := range []string{"", " \n\t"} {
				code, out := w.callAs(owner, "/internal/tasks/"+wi+"/recover", map[string]any{"action": "retry", "note": note})
				if e, _ := out["error"].(map[string]any); code != 400 || e["code"] != "bad_request" {
					t.Fatalf("try again with note %q: %d %v, want 400 bad_request", note, code, out)
				}
			}
			if s := w.taskStatus(wi); s != "aborted" {
				t.Fatalf("a refused try again picked the task up: %s", s)
			}
			if needs := w.retryNeedsNote(wi); needs != true {
				t.Fatalf("recovery options retryNeedsNote = %v, want true", needs)
			}
			const note = "Name the finding on FACTORY.md's first line."
			if code, out := w.callAs(owner, "/internal/tasks/"+wi+"/recover", map[string]any{"action": "retry", "note": note}); code != 200 {
				t.Fatalf("try again: %d %v", code, out)
			}
			// Recorded as the answer to the escalation it stopped at, as the banner records it.
			if got := w.str(`SELECT COALESCE(string_agg(prompt, ' | '), '') FROM questions WHERE task_id = $1 AND answer = $2`, wi, note); got != delivery.EscalationDecisionQuestion(reason) {
				t.Fatalf("the direction is recorded under %q", got)
			}
			w.directionReachesNextFix(wi, fixes, false, note)
			if reason == "stuck" {
				return
			}
			w.until("escalated again", func() bool { return w.taskStatus(wi) == "awaiting_input" })
			convergenceStop(w, wi, delivery.ReasonNotConverging, 4)
		})
	}
}

// After a convergence retry or amend, the next fixer's own input carries
// the owner's direction, with rounds resumed or not.
func TestConvergenceDirectionIsTheNextFixersInput(t *testing.T) {
	for _, resume := range []bool{false, true} {
		for _, action := range []string{"retry", "amend"} {
			t.Run(fmt.Sprintf("resume=%v/%s", resume, action), func(t *testing.T) {
				w := newWorld(t)
				stillScript(w, true)
				p := scriptedPolicy()
				p.ResumeRounds, p.MaxAttemptsPerFinding = resume, 4
				wi := startConvergence(w, p)
				convergenceStop(w, wi, "not_converging", 2)
				owner, _, _ := w.people(wi)
				const note = "FACTORY.md names the finding it fixes on its first line."
				if code, out := w.decide(owner, wi, action, note); code != 200 {
					t.Fatalf("%s: %d %v", action, code, out)
				}
				w.directionReachesNextFix(wi, 2, resume, note)
			})
		}
	}
}

// Accepting a not_converging escalation accepts the named finding alone:
// another reviewer blocker still open, without a criterion and so never
// named, is sent to the next fixer and fixed before the pull request opens.
func TestConvergenceAcceptLeavesAnUnrelatedBlockerToBeFixed(t *testing.T) {
	const other = "---\nseverity: high\ncategory: correctness\nfile: FACTORY.md\nline: 1\n" +
		"title: FACTORY.md has no trailing summary\ndescription: The summary line is missing.\n"
	w := newWorld(t)
	convergenceScript(w, func(round int) string {
		switch {
		case round == 1:
			return "```yaml\n" + fakeagent.Finding + other + "```\n"
		case round <= 3:
			return "```yaml\nverdicts:\n  F1: still\n  F2: still\n```\n"
		case round == 4:
			// Only the unrelated blocker is judged again: the accepted one is settled.
			return "```yaml\nverdicts:\n  F1: fixed\n```\n"
		}
		return "No new findings."
	})
	p := scriptedPolicy()
	p.MaxAttemptsPerFinding = 4
	wi := startConvergence(w, p)
	exit := convergenceStop(w, wi, "not_converging", 2)
	var named, unrelated string
	if err := w.owner.QueryRow(context.Background(), `SELECT
			(SELECT id FROM review_findings WHERE task_id = $1 AND acceptance IS NOT NULL),
			(SELECT id FROM review_findings WHERE task_id = $1 AND acceptance IS NULL)`, wi).Scan(&named, &unrelated); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(exit.FindingIDs, []string{named}) {
		t.Fatalf("exit names %v, want only %s", exit.FindingIDs, named)
	}
	owner, _, _ := w.people(wi)
	if code, out := w.decide(owner, wi, "accept", "The record file is a convention, not a requirement."); code != 200 {
		t.Fatalf("accept: %d %v", code, out)
	}
	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	status := func(id string) string {
		var s string
		if err := w.owner.QueryRow(context.Background(), `SELECT status::text FROM review_findings WHERE id = $1`, id).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	if s := status(named); s != "accepted" {
		t.Errorf("named finding %s, want accepted", s)
	}
	if s := status(unrelated); s != "resolved" {
		t.Errorf("unrelated blocker %s, want resolved by its fix", s)
	}
	var sent []string
	if err := w.owner.QueryRow(context.Background(), `SELECT finding_ids FROM runs WHERE task_id = $1 AND phase = 'fix'
		ORDER BY created_at DESC, id DESC LIMIT 1`, wi).Scan(&sent); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(sent, []string{unrelated}) {
		t.Errorf("fix after accept was sent %v, want only %s", sent, unrelated)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'fix'`, wi); n != 3 {
		t.Errorf("%d fix rows, want the two before the escalation and one for the unrelated blocker", n)
	}
}

// notConvergingConducted is a delivery whose reviewer judged its criterion
// finding still after two fixes: Deliver's loop escalates not_converging (a
// conducted loop has no such bound), then a person's Chat message hands it
// to the conductor while it waits, the conductor told.
func notConvergingConducted(t *testing.T) (*world, string) {
	return notConvergingConductedWith(t, "")
}

// notConvergingConductedWith is notConvergingConducted whose first review
// also reports extra (YAML documents after the criterion finding). The
// conductor's wake names the escalated finding.
func notConvergingConductedWith(t *testing.T, extra string) (*world, string) {
	w := conducting(t)
	scripted := w.lux.Decide
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		labels, _ := spec["labels"].(map[string]any)
		switch labels["dude.phase"] {
		case "review":
			if fmt.Sprint(labels["dude.run"]) == w.firstReview() {
				return fakelux.Behaviour{Reply: "```yaml\n" + fakeagent.Finding + extra + "```\n"}
			}
			return fakelux.Behaviour{Reply: "```yaml\nverdicts:\n  F1: still\n```\n"}
		case "implement", "fix":
			return fakelux.Behaviour{Commit: map[string]string{"FACTORY.md": fmt.Sprint(labels["dude.run"]) + "\n"}, Message: "work"}
		}
		return scripted(spec)
	}
	task := w.task()
	w.deliver(task)
	setPolicy(w, task, `{"maxAttemptsPerFinding":4,"maxReviewIterations":9,"maxStillRounds":2}`)
	w.until("not converging", func() bool { return w.escalationReason(task) == delivery.ReasonNotConverging })
	_, e := w.escalationOf(task)
	raw, _ := json.Marshal(e.Detail)
	var exit delivery.LoopExit
	if err := json.Unmarshal(raw, &exit); err != nil || len(exit.FindingIDs) != 1 {
		t.Fatalf("escalation detail %s: %v", raw, err)
	}
	handOver(w, task)
	w.wokenWith(task, "Escalated to a person: not converging")
	// The wake as recorded (a batched note shortens each line).
	var line string
	if err := w.owner.QueryRow(context.Background(), `SELECT line FROM conductor_wakes WHERE task_id = $1 AND kind = 'escalation'
		ORDER BY created_at DESC LIMIT 1`, task).Scan(&line); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(line, "Findings: "+exit.FindingIDs[0]+".") {
		t.Fatalf("the wake does not name the escalated finding %s: %s", exit.FindingIDs[0], line)
	}
	return w, task
}

// The owner's free answer hands a not_converging retry to the conductor:
// decide_escalation succeeds on it, the owner's words are the task's
// decision, and the next fix starts.
func TestTheConductorRetriesNotConvergingOnTheOwnersFreeAnswer(t *testing.T) {
	w, task := notConvergingConducted(t)
	ana := w.person("Ana")
	w.assignOwner(task, ana)
	w.must(task, "ask_person", `{"question":"The criterion stays unmet after two fixes. How should the next fix differ?","choices":["Stop"],"actions":["stop"]}`)
	q := w.conductorQuestion(task)
	const words = "Have the fixer name the finding inside FACTORY.md itself."
	if status, out := w.answerAs(ana, task, words); status != 200 {
		t.Fatalf("the answer: %d %v", status, out)
	}
	if d := w.decided(task); d != nil {
		t.Fatalf("a free answer decided the escalation: %v", d)
	}
	fixes := w.phaseRuns(task, "fix")
	// Told what to expect after a retry, not what an amend changed.
	if out := w.must(task, "decide_escalation", `{"action":"retry"}`); out["next"] != delivery.EscalationDecidedNext(false) {
		t.Fatalf("decide_escalation retry answered next %q", out["next"])
	}
	w.until("the next fix round", func() bool { return w.phaseRuns(task, "fix") == fixes+1 })
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'task.decided'
		AND payload->>'action' = 'retry' AND payload->>'reason' = $2 AND payload->>'questionId' = $3
		AND strpos(payload->>'note', $4) > 0`, task, delivery.ReasonNotConverging, q, words); n != 1 {
		t.Errorf("%d task.decided retry events carrying the owner's words", n)
	}
	if n := w.count(`SELECT count(*) FROM questions WHERE task_id = $1 AND run_id IS NULL AND status = 'answered'
		AND strpos(answer, $2) > 0`, task, words); n != 1 {
		t.Errorf("the owner's direction is not one of the task's decisions")
	}
}

// A conducted amend replaces the named finding's criterion with exactly the
// owner's own words, trimmed: not the conductor's proposal or note, not the
// question. Another finding's criterion and the task's criteria stay.
func TestTheConductorAmendsNotConvergingWithTheOwnersWords(t *testing.T) {
	const other = "---\nseverity: low\ncategory: correctness\nfile: FACTORY.md\nline: 1\n" +
		"title: FACTORY.md has no summary\ndescription: The summary line is missing.\nacceptance: FACTORY.md ends with a summary line.\n"
	w, task := notConvergingConductedWith(t, other)
	ana := w.person("Ana")
	w.assignOwner(task, ana)
	_, e := w.escalationOf(task)
	named := e.Detail.(map[string]any)["findingIds"].([]any)[0].(string)
	w.must(task, "ask_person", `{"question":"What should the criterion say instead?","choices":["Stop"],"actions":["stop"]}`)
	const words = "FACTORY.md names the finding it fixes on its first line."
	if status, out := w.answerAs(ana, task, "  \n"+words+"\t\n"); status != 200 {
		t.Fatalf("the answer: %d %v", status, out)
	}
	if out := w.must(task, "decide_escalation", `{"action":"amend","note":"The conductor's own summary of the change."}`); out["next"] != delivery.EscalationDecidedNext(true) {
		t.Fatalf("decide_escalation amend answered next %q", out["next"])
	}
	w.until("the amend carried out", func() bool {
		return w.count(`SELECT count(*) FROM review_findings WHERE task_id = $1 AND acceptance <> $2 AND id = $3`,
			task, fakeagent.FindingAcceptance, named) == 1
	})
	rows, err := w.owner.Query(context.Background(), `SELECT id, COALESCE(acceptance, '') FROM review_findings WHERE task_id = $1`, task)
	if err != nil {
		t.Fatal(err)
	}
	criteria := map[string]string{}
	for rows.Next() {
		var id, acceptance string
		if err := rows.Scan(&id, &acceptance); err != nil {
			t.Fatal(err)
		}
		criteria[id] = acceptance
	}
	rows.Close()
	if len(criteria) != 2 || criteria[named] != words {
		t.Fatalf("criteria after amend = %q, want %s = %q", criteria, named, words)
	}
	for id, c := range criteria {
		if id != named && c != "FACTORY.md ends with a summary line." {
			t.Errorf("unnamed finding %s criterion changed to %q", id, c)
		}
	}
	var taskCriteria []string
	if err := w.owner.QueryRow(context.Background(), `SELECT acceptance_criteria FROM tasks WHERE id = $1`, task).Scan(&taskCriteria); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(taskCriteria, []string{"it greets"}) {
		t.Errorf("task criteria changed: %q", taskCriteria)
	}
}

// A task nobody owns is anyone's to direct: a member's free answer hands a
// not_converging or stuck retry to the conductor, which needs no admin for
// it (an admin is needed only to ship past a failed test or waive a check).
func TestTheConductorRetriesAnOwnerlessStalledLoopOnAMembersDirection(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(*testing.T) (*world, string)
	}{
		{"not converging", notConvergingConducted},
		{"stuck", stuck},
	} {
		t.Run(c.name, func(t *testing.T) {
			w, task := c.setup(t)
			mustExec(t, w.owner, `DELETE FROM task_people WHERE task_id = $1`, task)
			bo := w.person("Bo")
			mustExec(t, w.owner, `UPDATE people SET role = 'member' WHERE organization_id = $1`, w.org)
			w.must(task, "ask_person", `{"question":"How should the next fix differ?","choices":["Stop"],"actions":["stop"]}`)
			const words = "Have the fixer quote the finding inside FACTORY.md."
			if status, out := w.answerAs(bo, task, words); status != 200 {
				t.Fatalf("the answer: %d %v", status, out)
			}
			fixes := w.phaseRuns(task, "fix")
			w.must(task, "decide_escalation", `{"action":"retry"}`)
			w.until("the next fix round", func() bool { return w.phaseRuns(task, "fix") == fixes+1 })
		})
	}
}

// A choice is a pick, never the person's direction: while a not_converging
// or stuck escalation waits, ask_person may not offer an action that needs
// a note as a choice, and nothing is asked.
func TestANakedChoiceThatNeedsDirectionIsRefused(t *testing.T) {
	cases := []struct {
		name    string
		setup   func(*testing.T) (*world, string)
		actions []string
	}{
		{"not converging", notConvergingConducted, []string{"retry", "accept", "amend"}},
		{"stuck", stuck, []string{"retry"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w, task := c.setup(t)
			for _, action := range c.actions {
				w.refused(task, "ask_person", fmt.Sprintf(`{"question":"Q","choices":["Do it","Stop"],"actions":[%q,"stop"]}`, action),
					fmt.Sprintf("%q is not a choice", action))
				if q := w.conductorQuestion(task); q != "" {
					t.Fatalf("question %s recorded with %s as a choice", q, action)
				}
			}
			if d := w.decided(task); d != nil {
				t.Fatalf("decided with no answer: %v", d)
			}
		})
	}
}

// A pull request's fix checked on its candidate stops not_converging like
// the review loop before the pull request, never moving the pull request's
// branch. Accepting the named risk sends the candidate through the normal
// gates (simplifier, its diff's review), and the question its first
// reviewer asked still holds the promotion until it is answered.
func TestAPullRequestFixCandidateAcceptedPastNotConvergingKeepsItsGates(t *testing.T) {
	w := newWorld(t)
	wi := w.reviewing()
	ana := w.person("Ana")
	w.assignOwner(wi, ana)
	setPolicy(w, wi, `{"maxAttemptsPerFinding":4,"maxStillRounds":2}`)
	before := w.head(wi)
	scripted := w.lux.Decide
	var reviews int
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		b := scripted(spec)
		if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] == "review" {
			switch reviews++; {
			case reviews == 1:
				b.Reply = "One problem:\n\n```yaml\n" + fakeagent.Finding + "```\n\n" + questionDoc
			case reviews <= 3:
				b.Reply = "```yaml\nverdicts:\n  F1: still\n```\n"
			default:
				b.Reply = "No new findings."
			}
		}
		return b
	}
	w.gh.Comment(1, "alice", "Please rename the greeting.")
	w.until("the candidate not converging", func() bool {
		w.sync()
		return w.escalationReason(wi) == delivery.ReasonNotConverging
	})
	var candidate bool
	if err := w.owner.QueryRow(context.Background(), `SELECT COALESCE(jsonb_typeof(state->'candidate'->'base') = 'object', false)
		FROM workflow_runs WHERE task_id = $1`, wi).Scan(&candidate); err != nil {
		t.Fatal(err)
	}
	if !candidate {
		t.Fatal("escalated outside a checked candidate")
	}
	w.notPromoted(wi, before)
	simplifiers := w.phaseRuns(wi, "simplify")
	if code, out := w.decide(ana, wi, "accept", "The record file is the pull request's own convention."); code != 200 {
		t.Fatalf("accept: %d %v", code, out)
	}
	w.until("the candidate held at its promotion, or promoted", func() bool {
		w.sync()
		return w.head(wi) != before || w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1
			AND step = 'promoteCandidate' AND status = 'waiting'`, wi) == 1
	})
	for range 5 {
		w.pump()
	}
	w.notPromoted(wi, before)
	if n := w.phaseRuns(wi, "simplify"); n != simplifiers+1 {
		t.Errorf("%d simplifiers after the accept, want the candidate's own", n-simplifiers)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review' AND review_range->>'of' = $2
		AND status = 'completed' AND created_at > (SELECT max(created_at) FROM runs WHERE task_id = $1 AND phase = 'simplify')`,
		wi, delivery.RangeSimplify); n != 1 {
		t.Errorf("%d reviews of the candidate simplifier's diff, want one", n)
	}
	if n := w.count(`SELECT count(*) FROM review_findings WHERE task_id = $1 AND status = 'accepted'`, wi); n != 1 {
		t.Errorf("%d accepted findings, want the named one", n)
	}
	if status, out := w.callAs(ana, "/internal/review-questions/"+w.reviewQuestionOf(wi)+"/answer", map[string]any{"answer": "Yes, once."}); status != 200 {
		t.Fatalf("answer: %d %v", status, out)
	}
	w.until("the promotion", func() bool { w.sync(); return w.head(wi) != before })
	w.until("the pull request in review", func() bool { w.sync(); return w.taskStatus(wi) == "review" })
}
