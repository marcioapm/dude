package delivery

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

// The notes ABS-30's owner gave with Retry, which its fixer never saw.
const absNote = "I just pushed a fix to main, that uses versitygw instead of minio.\nMain no longer uses minio, you need to sync to main."

// ownedHeldTask is newHeldTask with its owner, Ola Nordmann (per_ola).
func ownedHeldTask(t *testing.T) heldTask {
	h := newHeldTask(t)
	h.exec(`INSERT INTO people (id, organization_id, name) VALUES ('per_ola', $1, 'Ola Nordmann')`, h.org)
	h.exec(`INSERT INTO task_people (task_id, person_id, organization_id, position) VALUES ('wi_h', 'per_ola', $1, 0)`, h.org)
	return h
}

func (h heldTask) directives(runID string) []string {
	h.t.Helper()
	rows, err := h.owner.Query(h.ctx, `SELECT text FROM directives WHERE run_id = $1 ORDER BY created_at, id`, runID)
	if err != nil {
		h.t.Fatal(err)
	}
	texts, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		h.t.Fatal(err)
	}
	return texts
}

func (h heldTask) decideAs(person string, in EscalationDecision) {
	h.t.Helper()
	in.ActorType, in.ActorID, in.Person = "human", person, person
	if err := h.app.InOrg(h.ctx, h.org, func(tx pgx.Tx) error {
		return DecideEscalationTx(h.ctx, tx, h.org, "wi_h", in)
	}); err != nil {
		h.t.Fatal(err)
	}
}

// A checks escalation retried with a note sends the held Run the note,
// verbatim and as its owner's words, ahead of the gate's send-back.
func TestAChecksRetryHandsTheOwnersNoteToTheHeldRun(t *testing.T) {
	h := ownedHeldTask(t)
	h.escalate("run_held")
	h.decideAs("per_ola", EscalationDecision{Action: "retry", Note: absNote})
	got := h.directives("run_held")
	if len(got) != 1 {
		t.Fatalf("directives %q, want one", got)
	}
	want := "The task's owner, Ola Nordmann, said:\n\n" + absNote
	if !strings.HasPrefix(got[0], want) {
		t.Fatalf("the directive does not start with the owner's words:\n%s", got[0])
	}
	if !strings.HasSuffix(got[0], "held") {
		t.Fatalf("the directive lost the gate's send-back:\n%s", got[0])
	}
}

// A retry without a note sends the gate's send-back alone, as before.
func TestAChecksRetryWithoutANoteSendsTheSendBackAlone(t *testing.T) {
	h := ownedHeldTask(t)
	h.escalate("run_held")
	h.decideAs("per_ola", EscalationDecision{Action: "retry"})
	if got := h.directives("run_held"); len(got) != 1 || got[0] != "held" {
		t.Fatalf("directives %q, want [held]", got)
	}
}

// Resuming a failed Run with a note sends it the note as the owner's
// words, verbatim.
func TestAResumeHandsTheOwnersNoteToTheResumedRun(t *testing.T) {
	h := ownedHeldTask(t)
	h.exec(`UPDATE runs SET status = 'failed', keep = true, lux_run_id = 'lux_held', lux_stop_reason = 'kept',
		kept_until = now() + interval '1 hour', ended_at = now() WHERE id = 'run_held'`)
	h.exec(`UPDATE tasks SET status = 'awaiting_input' WHERE id = 'wi_h'`)
	h.exec(`UPDATE workflow_runs SET step = 'decide', state = state || '{"escalation":{"reason":"implement_failed","at":"awaitImplement","step":"implement","detail":{"runId":"run_held"}}}'
		WHERE id = 'wf_h'`)
	h.decideAs("per_ola", EscalationDecision{Action: "resume", Note: absNote})
	got := h.directives("run_held")
	if len(got) != 1 || got[0] != "The task's owner, Ola Nordmann, said:\n\n"+absNote {
		t.Fatalf("directives %q", got)
	}
}

// A person who is not the task's owner is named as who they are.
func TestAnotherPersonsNoteIsNotPassedOffAsTheOwners(t *testing.T) {
	h := ownedHeldTask(t)
	h.exec(`INSERT INTO people (id, organization_id, name) VALUES ('per_kim', $1, 'Kim Lee')`, h.org)
	h.escalate("run_held")
	h.decideAs("per_kim", EscalationDecision{Action: "retry", Note: "Use the staging bucket."})
	got := h.directives("run_held")
	if len(got) != 1 || !strings.HasPrefix(got[0], "Kim Lee, not the task's owner, said:\n\nUse the staging bucket.") {
		t.Fatalf("directives %q", got)
	}
}

// A conducted task's escalation decided on the banner with a note tells
// its conductor the note in the wake.
func TestTheConductorHearsTheNoteOfABannerDecision(t *testing.T) {
	h := ownedHeldTask(t)
	h.exec(`UPDATE workflow_runs SET state = state || '{"decider":"conductor"}' WHERE id = 'wf_h'`)
	h.escalate("run_held")
	h.decideAs("per_ola", EscalationDecision{Action: "retry", Note: absNote})
	var line string
	if err := h.owner.QueryRow(h.ctx, `SELECT COALESCE(max(line), '') FROM conductor_wakes WHERE task_id = 'wi_h' AND key LIKE '%:decided'`).
		Scan(&line); err != nil {
		t.Fatal(err)
	}
	if want := absNote; !strings.Contains(line, want) || !strings.Contains(line, "Ola Nordmann") {
		t.Fatalf("the conductor's wake lacks the owner's note %q:\n%q", want, line)
	}
}

// A stuck loop retried with the owner's direction: the decide step hands
// the marked note to the next fix, whose Run records it for its prompt,
// and it is spent once the step waits on that Run.
func TestARetrysNoteGoesToTheNextFixRun(t *testing.T) {
	h := ownedHeldTask(t)
	h.exec(`UPDATE tasks SET status = 'awaiting_input' WHERE id = 'wi_h'`)
	h.exec(`UPDATE workflow_runs SET step = 'decide', state = state || '{"escalation":{"reason":"stuck","step":"fix","detail":{"reason":"stuck","findingIds":["f"]}}}'
		WHERE id = 'wf_h'`)
	h.decideAs("per_ola", EscalationDecision{Action: "retry", Note: absNote})
	var raw []byte
	if err := h.owner.QueryRow(h.ctx, `SELECT state FROM workflow_runs WHERE id = 'wf_h'`).Scan(&raw); err != nil {
		h.t.Fatal(err)
	}
	w := &steps{s: &Store{DB: h.app}}
	res, err := w.decide(h.ctx, workflow.StepContext{WorkflowRunID: "wf_h", OrganizationID: h.org, Step: "decide", State: raw})
	if err != nil || res.Next != "fix" {
		t.Fatalf("decided: %q %v", res.Next, err)
	}
	st := res.State.(*State)
	want := "The task's owner, Ola Nordmann, said:\n\n" + absNote
	if st.PersonSaid != want {
		t.Fatalf("the state hands on %q", st.PersonSaid)
	}
	id, err := w.phase(h.ctx, workflow.StepContext{WorkflowRunID: "wf_h", OrganizationID: h.org}, st, PhaseFix, "k_fix", nil)
	if err != nil {
		t.Fatal(err)
	}
	var got string
	if err := h.owner.QueryRow(h.ctx, `SELECT COALESCE(person_note, '') FROM runs WHERE id = $1`, id).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("the fix Run's person_note %q", got)
	}
	if park("awaitFix", st, []string{id}); st.PersonSaid != "" {
		t.Fatalf("the words outlive the Runs they were for: %q", st.PersonSaid)
	}
}

// A system decision's note (dude's own) is no one's words: nothing is
// handed on.
func TestDudesOwnDecisionHandsOnNoWords(t *testing.T) {
	h := ownedHeldTask(t)
	h.escalate("run_held")
	if err := h.app.InOrg(h.ctx, h.org, func(tx pgx.Tx) error {
		return DecideEscalationTx(h.ctx, tx, h.org, "wi_h", EscalationDecision{Action: "retry", Note: "dude says", ActorType: "system", ActorID: "forge"})
	}); err != nil {
		t.Fatal(err)
	}
	if got := h.directives("run_held"); len(got) != 1 || got[0] != "held" {
		t.Fatalf("directives %q", got)
	}
}

func TestAChecksWaiversNoteGoesToTheNextRun(t *testing.T) {
	for _, scope := range []string{ScopeRun, ScopeAttempt} {
		t.Run(scope, func(t *testing.T) {
			h := ownedHeldTask(t)
			h.exec(`INSERT INTO events(id,organization_id,event_type,run_id,task_id,actor_type,actor_id,source,payload)
				VALUES ('ev_failure',$1,'run.checks.judged','run_held','wi_h','system','dude','test',
				'{"turn":0,"verified":false,"checks":[{"command":"make test","result":"fail","reason":"hardware unavailable"}]}')`, h.org)
			h.escalate("run_held")
			h.decideAs("per_ola", EscalationDecision{Action: ActionWaive, Scope: scope, Note: absNote})
			var raw []byte
			if err := h.owner.QueryRow(h.ctx, `SELECT state FROM workflow_runs WHERE id = 'wf_h'`).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			res, err := (&steps{s: &Store{DB: h.app}}).decide(h.ctx, workflow.StepContext{OrganizationID: h.org, State: raw})
			if err != nil {
				t.Fatal(err)
			}
			if got := res.State.(*State).PersonSaid; !strings.Contains(got, absNote) || !strings.Contains(got, "Ola Nordmann") {
				t.Fatalf("the next Run receives %q", got)
			}
		})
	}
}

func TestEscalationAnswerPreservesTheOwnersWords(t *testing.T) {
	note := strings.Repeat("Keep this exact line.\n", 100) + "Final direction."
	if got := escalationNote("What next?", note, "Retry it."); !strings.Contains(got, note) {
		t.Fatalf("the owner's answer was flattened or clipped: %q", got)
	}
}

func TestALongBannerNoteReachesTheConductorVerbatim(t *testing.T) {
	h := ownedHeldTask(t)
	h.exec(`UPDATE workflow_runs SET state = state || '{"decider":"conductor"}' WHERE id = 'wf_h'`)
	h.escalate("run_held")
	note := strings.Repeat("Read this exact paragraph.\n", 100) + "The final instruction."
	h.decideAs("per_ola", EscalationDecision{Action: "retry", Note: note})
	var line string
	if err := h.owner.QueryRow(h.ctx, `SELECT line FROM conductor_wakes WHERE task_id = 'wi_h' AND key LIKE '%:decided'`).Scan(&line); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(line, note) || !strings.Contains(WakeNote([]string{line}), note) {
		t.Fatalf("the conductor's wake dropped words: %q", line)
	}
}

// Re-parking on an existing fixer must not spend a waiver's words. A
// delayed finish from a sibling and a sibling PR closing both wake waits.
func TestOwnerWaiverSurvivesPendingFixerWakes(t *testing.T) {
	for _, scope := range []string{ScopeRun, ScopeAttempt} {
		for _, at := range []string{"awaitFix", "awaitPRFix"} {
			t.Run(scope+"/"+at, func(t *testing.T) {
				h := ownedHeldTask(t)
				h.exec(`UPDATE runs SET phase='fix' WHERE id='run_held'`)
				h.exec(`UPDATE workflow_runs SET step=$1, state=state || '{"pendingRunIds":["run_held"]}' WHERE id='wf_h'`, at)
				if at == "awaitPRFix" {
					h.ownerPRs()
				}
				h.exec(`INSERT INTO events(id,organization_id,event_type,run_id,task_id,actor_type,actor_id,source,payload)
					VALUES ('ev_failure',$1,'run.checks.judged','run_held','wi_h','system','dude','test',
					'{"turn":0,"verified":false,"checks":[{"command":"make test","result":"fail","reason":"hardware unavailable"}]}')`, h.org)
				h.escalate("run_held")
				h.decideAs("per_ola", EscalationDecision{Action: ActionWaive, Scope: scope, Note: absNote})
				w := &steps{s: &Store{DB: h.app}, forges: forgeOf{}}
				var raw []byte
				if err := h.owner.QueryRow(h.ctx, `SELECT state FROM workflow_runs WHERE id='wf_h'`).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				sc := workflow.StepContext{WorkflowRunID: "wf_h", OrganizationID: h.org, State: raw}
				res, err := w.decide(h.ctx, sc)
				if err != nil {
					t.Fatal(err)
				}
				sc.State, _ = json.Marshal(res.State)
				sc.Step = at
				sc.Signals = []workflow.Signal{{Name: SignalPhaseFinished, Payload: json.RawMessage(`{"runId":"run_other"}`)}}
				wait := w.awaitFix
				if at == "awaitPRFix" {
					h.exec(`UPDATE pull_requests SET state='closed' WHERE id='pr_sibling'`)
					sc.Signals = []workflow.Signal{{Name: SignalPullRequestEnded}}
					wait = w.endable(w.awaitPRFix)
				}
				res, err = wait(h.ctx, sc)
				if err != nil {
					t.Fatal(err)
				}
				sc.State, _ = json.Marshal(res.State)
				if at == "awaitPRFix" {
					pending := res.State.(*State)
					pending.Candidate = &Candidate{From: map[string]string{"api": "aaa"}, Branch: "candidate"}
					sc.State, _ = json.Marshal(pending)
				}
				h.exec(`UPDATE runs SET status='completed', ended_at=now(), heads='{"api":{"sha":"bbb","changedPaths":["main.go"],"base":"aaa"}}' WHERE id='run_held'`)
				sc.Signals = []workflow.Signal{{Name: SignalPhaseFinished, Payload: json.RawMessage(`{"runId":"run_held"}`)}}
				res, err = wait(h.ctx, sc)
				if err != nil {
					t.Fatal(err)
				}
				st := res.State.(*State)
				// Start real phase work after the finish; force two reviewers so
				// consuming at the first creation cannot strand the second.
				st.RetryCategories = []string{"correctness", "security"}
				sc.State, _ = json.Marshal(st)
				res, err = w.review(h.ctx, sc)
				if err != nil {
					t.Fatal(err)
				}
				st = res.State.(*State)
				if len(st.PendingRunIDs) != 2 {
					t.Fatalf("reviewers: %v", st.PendingRunIDs)
				}
				for _, id := range st.PendingRunIDs {
					h.assertOwnerRunInput(id)
				}
				if st.PersonSaid != "" {
					t.Fatal("note remains after full fan-out")
				}
			})
		}
	}
}

func (h heldTask) ownerPRs() {
	h.exec(`INSERT INTO repositories(id,organization_id,project_id,name,url) VALUES('repo_owner',$1,'prj_h','api','https://github.com/acme/api')`, h.org)
	h.exec(`INSERT INTO pull_requests(id,organization_id,project_id,task_id,repository_id,number,url,head_branch,base_branch,head_sha,title,state)
		VALUES ('pr_open',$1,'prj_h','wi_h','repo_owner',1,'https://github.com/acme/api/pull/1','work','main','aaa','P','open'),
		('pr_sibling',$1,'prj_h','wi_h','repo_owner',2,'https://github.com/acme/api/pull/2','other','main','aaa','S','open')`, h.org)
	h.exec(`UPDATE workflow_runs SET state=state || '{"pullRequestIds":["pr_open","pr_sibling"]}' WHERE id='wf_h'`)
}

func (h heldTask) assertOwnerRunInput(id string) {
	h.t.Helper()
	var note, phase string
	if err := h.owner.QueryRow(h.ctx, `SELECT COALESCE(person_note,''), phase::text FROM runs WHERE id=$1`, id).Scan(&note, &phase); err != nil {
		h.t.Fatal(err)
	}
	want := "The task's owner, Ola Nordmann, said:\n\n" + absNote
	input := Prompt(phase, PromptInput{PersonNote: note, Title: "H", Goal: "H"})
	if !strings.HasPrefix(input, want+"\n\n") || strings.Count(input, absNote) != 1 {
		h.t.Fatalf("Run %s input lost or duplicated note: %q", id, input)
	}
}

func TestOwnerWaitDispatchHandsNoteToFixer(t *testing.T) {
	for _, route := range []string{"conflict", "integration-feedback", "open-feedback", "waiting", "stop", "done"} {
		t.Run(route, func(t *testing.T) {
			h := ownedHeldTask(t)
			h.ownerPRs()
			h.exec(`UPDATE runs SET status='completed', ended_at=now() WHERE task_id='wi_h'`)
			reason, action := "pull_request_conflict", "wait"
			if route == "stop" || route == "done" {
				reason, action = "pull_request_closed", route
			}
			st := &State{TaskID: "wi_h", ProjectID: "prj_h", Attempt: 1, Policy: DefaultPolicy(), PullRequestIDs: []string{"pr_open"}}
			if route == "conflict" || route == "integration-feedback" {
				st.PRFeedback = []forge.ActionableFeedback{{Repo: "api", Source: "review", Body: "Please fix this"}}
			}
			if route == "integration-feedback" || route == "open-feedback" {
				st.Integration = &Integration{Repo: "api"}
			}
			if route == "open-feedback" {
				h.exec(`INSERT INTO feedback_batches(id,organization_id,task_id,attempt,author_person_id,submission_key) VALUES('batch_owner',$1,'wi_h',1,'per_ola','owner')`, h.org)
				h.exec(`INSERT INTO feedback_items(id,organization_id,task_id,attempt,batch_id,author_person_id,body,anchor_kind,anchor,status,submitted_at) VALUES('fb_owner',$1,'wi_h',1,'batch_owner','per_ola','Fix this','general','{"kind":"general"}','open',now())`, h.org)
			}
			st.Escalation = &Escalation{Reason: reason, Decided: &HumanDecision{Action: action, Said: "The task's owner, Ola Nordmann, said:\n\n" + absNote}}
			raw, _ := json.Marshal(st)
			w := &steps{s: &Store{DB: h.app}, forges: forgeOf{}}
			sc := workflow.StepContext{WorkflowRunID: "wf_h", OrganizationID: h.org, Step: "decide", State: raw}
			res, err := w.decide(h.ctx, sc)
			if err != nil {
				t.Fatal(err)
			}
			if route == "waiting" || route == "stop" || route == "done" {
				if res.Next == "prFix" || len(res.State.(*State).PendingRunIDs) > 0 {
					t.Fatal("note alone started work")
				}
				return
			}
			if res.Next != "prFix" {
				t.Fatalf("dispatch %q", res.Next)
			}
			sc.State, _ = json.Marshal(res.State)
			res, err = w.prFix(h.ctx, sc)
			if err != nil {
				t.Fatal(err)
			}
			st = res.State.(*State)
			for _, id := range st.PendingRunIDs {
				h.assertOwnerRunInput(id)
			}
			if len(st.PendingRunIDs) != 1 || st.PersonSaid != "" {
				t.Fatalf("handoff %+v", st)
			}
		})
	}
}

// A fresh Run's prompt leads with the person's words (after a replay);
// a resumed round's message does too.
func TestAPersonsNoteLeadsThePromptAndTheRoundMessage(t *testing.T) {
	said := "The task's owner, Ola Nordmann, said:\n\nSync to main."
	for _, phase := range []string{PhaseImplement, PhaseFix, PhaseReview, PhaseTest, PhaseSimplify} {
		p := Prompt(phase, PromptInput{Title: "T", Goal: "G", PersonNote: said})
		if !strings.HasPrefix(p, said+"\n\n") {
			t.Errorf("%s's prompt does not lead with the words:\n%s", phase, p)
		}
	}
	if p := Prompt(PhaseFix, PromptInput{Title: "T", Goal: "G"}); strings.Contains(p, "said:") {
		t.Errorf("a prompt with no words has some:\n%s", p)
	}
}
