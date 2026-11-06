package orchestrator_test

// The conductor's decision points after the real-model Slack simulation
// (PR4c): a person's answer that opens the pull request carries forward to
// the decision before it; decide ask_person asks with real choices; and a
// quiet decision's deadline does not run while the conductor waits on a
// person. Through the real workflow, syncer, API and the fake lux/GitHub.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// openChoices is the sim's question at a decision point, as choices: the
// first opens the pull request (action open, or draft).
func openChoices(action string) string {
	return `{"action":"ask_person","note":"Criterion 1 cannot be measured here: the agents have no container runtime. How do we go on?",
		"choices":[{"label":"Open the PR so CI measures","action":"` + action + `","recommended":true},
		{"label":"Run Docker locally"},{"label":"Accept partly met"}]}`
}

// answerOpen answers the conductor's open question with its first choice.
func (w *world) answerOpen(task string) string {
	w.t.Helper()
	q := w.conductorQuestion(task)
	if q == "" {
		w.t.Fatal("the conductor has no open question")
	}
	if status, out := w.call("/internal/questions/"+q+"/answer", map[string]any{"text": "Open the PR so CI measures"}); status != 200 {
		w.t.Fatalf("answer: %d %v", status, out)
	}
	return q
}

// askedAndAnsweredOpen is a conducted task at after implement whose
// conductor asked the sim's question with choices, answered with the
// choice that opens (action), and is refused open_pull_request there: the
// answer is kept for the decision before the pull request.
func (w *world) askedAndAnsweredOpen(task, action string) string {
	w.t.Helper()
	w.talk(task)
	w.implemented(task)
	out := w.must(task, "decide", openChoices(action))
	if choices, _ := out["choices"].([]any); len(choices) != 3 {
		w.t.Fatalf("decide ask_person with choices: %v", out)
	}
	q := w.answerOpen(task)
	w.until("the answer recorded as the opening's authority", func() bool {
		return w.str(`SELECT COALESCE(state->'openAuthority'->>'questionId', '') FROM workflow_runs WHERE task_id = $1`, task) == q
	})
	w.refused(task, "decide", `{"action":"open_pull_request"}`, "is kept")
	return q
}

// gateQuestions is how many times the gate's question was asked.
func (w *world) gateQuestions(task string) int {
	return w.count(`SELECT count(*) FROM questions WHERE task_id = $1 AND pr_gate_heads IS NOT NULL`, task)
}

// One "open" answer, then only the simplifier's commits: the decision
// before the pull request opens it citing the answer, with no second
// question; Draft opens a draft.
func TestAPersonsOpenAnswerCarriesToThePullRequest(t *testing.T) {
	for _, c := range []struct {
		action string
		draft  bool
	}{{"open", false}, {"draft", true}} {
		t.Run(c.action, func(t *testing.T) {
			w := conducting(t)
			task := w.task()
			q := w.askedAndAnsweredOpen(task, c.action)
			w.carriedOut(task, "start_phase", `{"phase":"simplify"}`)
			w.agentsDone(task, "the simplifier's narrow review", func() bool { return w.decisionAt(task) == delivery.PointReviewed })
			w.finishSimplifierReview(task)
			w.until("before the pull request", func() bool { return w.decisionAt(task) == delivery.PointBeforePR })
			if n := w.count(`SELECT jsonb_array_length(COALESCE(state->'openAuthority'->'moves', '[]')) FROM workflow_runs
				WHERE task_id = $1`, task); n != 1 {
				t.Errorf("%d simplifier commits recorded on the authority, want 1", n)
			}
			if n := w.wokenWith(task, "already answered"); !strings.Contains(n, q) {
				t.Errorf("the wake before the pull request does not cite the answer: %q", n)
			}
			out := w.must(task, "decide", `{"action":"open_pull_request"}`)
			by, _ := out["authorizedBy"].(map[string]any)
			if by["questionId"] != q || by["choice"] != c.action {
				t.Errorf("open_pull_request's result: %v", out)
			}
			w.until("the pull request", func() bool { return len(w.gh.Pulls()) == 1 })
			if w.gh.Pulls()[0].Draft != c.draft {
				t.Errorf("draft = %v, want %v", w.gh.Pulls()[0].Draft, c.draft)
			}
			if n := w.gateQuestions(task); n != 0 {
				t.Errorf("the person was asked the gate's question %d times", n)
			}
			if n := w.count(`SELECT count(*) FROM questions WHERE task_id = $1`, task); n != 1 {
				t.Errorf("%d questions to the person, want the one", n)
			}
			if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = $2
				AND payload->>'action' = 'open_pull_request' AND payload->'authorizedBy'->>'questionId' = $3`,
				task, delivery.EvDecisionTaken, q); n != 1 {
				t.Errorf("%d decisions in the ledger citing the answer, want 1", n)
			}
		})
	}
}

// What voids the answer: a fixer's commit since, a new blocking finding, a
// next attempt. Each asks again, and the gate's question has its choices.
func TestAnOpenAnswerNoLongerHoldingAsksAgain(t *testing.T) {
	t.Run("a fixer commit", func(t *testing.T) {
		w := conducting(t)
		task := w.task()
		w.askedAndAnsweredOpen(task, "open")
		w.must(task, "decide", `{"action":"next"}`)
		w.until("after the review round", func() bool { return w.decisionAt(task) == delivery.PointReviewed })
		w.must(task, "start_phase", `{"phase":"fix"}`)
		w.until("after the fix", func() bool { return w.decisionAt(task) == delivery.PointFixed })
		w.must(task, "start_phase", `{"phase":"simplify"}`)
		w.finishSimplifierReview(task)
		w.until("before the pull request", func() bool { return w.decisionAt(task) == delivery.PointBeforePR })
		w.refused(task, "decide", `{"action":"open_pull_request"}`, "not the simplifier's")
		w.asksTheGateAgain(task)
	})
	t.Run("a new blocking finding", func(t *testing.T) {
		w := conducting(t)
		task := w.task()
		w.reachGateAnswered(task)
		mustExec(t, w.owner, `INSERT INTO review_findings (id, organization_id, task_id, run_id, category, severity, title)
			SELECT 'fnd_new', organization_id, id, (SELECT id FROM runs WHERE task_id = $1 AND phase = 'review' LIMIT 1),
			'correctness', 'high', 'A new one' FROM tasks WHERE id = $1`, task)
		w.refused(task, "decide", `{"action":"open_pull_request"}`, "fnd_new")
		mustExec(t, w.owner, `UPDATE review_findings SET status = 'accepted' WHERE id = 'fnd_new'`)
		w.asksTheGateAgain(task)
	})
	t.Run("a next attempt", func(t *testing.T) {
		w := conducting(t)
		task := w.task()
		w.reachGateAnswered(task)
		mustExec(t, w.owner, `UPDATE workflow_runs SET state = jsonb_set(state, '{openAuthority,attempt}', '7') WHERE task_id = $1`, task)
		w.refused(task, "decide", `{"action":"open_pull_request"}`, "another attempt")
		w.asksTheGateAgain(task)
	})
}

// reachGateAnswered is askedAndAnsweredOpen, then the simplifier, to the
// decision before the pull request.
func (w *world) reachGateAnswered(task string) {
	w.t.Helper()
	w.askedAndAnsweredOpen(task, "open")
	w.carriedOut(task, "start_phase", `{"phase":"simplify"}`)
	w.agentsDone(task, "the simplifier's narrow review", func() bool { return w.decisionAt(task) == delivery.PointReviewed })
	w.finishSimplifierReview(task)
	w.until("before the pull request", func() bool { return w.decisionAt(task) == delivery.PointBeforePR })
}

// asksTheGateAgain is the conductor asking the gate's question, which has
// the standard choices, and the person's Open opening the pull request.
func (w *world) asksTheGateAgain(task string) {
	w.t.Helper()
	w.must(task, "decide", `{"action":"ask_person"}`)
	if n := w.gateQuestions(task); n != 1 {
		w.t.Fatalf("%d gate questions, want 1", n)
	}
	w.chat(task, "Open")
	w.must(task, "decide", `{"action":"open_pull_request"}`)
	w.until("the pull request", func() bool { return len(w.gh.Pulls()) == 1 })
}

// questionShape is a question's options, its first item's choices, which
// are recommended, and the actions it keeps.
func (w *world) questionShape(id string) (options []string, choices []delivery.Choice, actions []string) {
	w.t.Helper()
	var rawOpts, rawItems, rawActions []byte
	if err := w.owner.QueryRow(context.Background(), `SELECT options, items, COALESCE(actions, '[]') FROM questions WHERE id = $1`, id).
		Scan(&rawOpts, &rawItems, &rawActions); err != nil {
		w.t.Fatal(err)
	}
	var items []delivery.QuestionItem
	_ = json.Unmarshal(rawOpts, &options)
	_ = json.Unmarshal(rawItems, &items)
	_ = json.Unmarshal(rawActions, &actions)
	if len(items) > 0 {
		choices = items[0].Choices
	}
	return options, choices, actions
}

// The sim's second question, exactly: decide ask_person with only a note
// that lists the answers as prose. Before the pull request it is the gate's
// question with the standard choices, each with its action, Open
// recommended; at another decision it is refused, and a question with
// choices is asked with them.
func TestDecideAskPersonAsksWithChoices(t *testing.T) {
	const sim = `{"action":"ask_person","note":"You chose to open the PR so CI measures, and the tester could not measure criterion 1. Open / Draft / Show me the diff / Another round? I recommend Open."}`
	t.Run("before the pull request", func(t *testing.T) {
		w := conducting(t)
		task := w.task()
		w.reachGate(task)
		out := w.must(task, "decide", sim)
		id, _ := out["questionId"].(string)
		options, choices, actions := w.questionShape(id)
		if len(options) < 2 || len(choices) != len(options) || len(actions) != len(options) {
			t.Fatalf("options %v, choices %v, actions %v", options, choices, actions)
		}
		rec := 0
		for _, c := range choices {
			if c.Recommended {
				rec++
			}
		}
		if rec != 1 || !choices[0].Recommended || actions[0] != "open" || actions[1] != "draft" {
			t.Errorf("choices %+v, actions %v: want Open recommended, open and draft carried", choices, actions)
		}
		if p := w.str(`SELECT prompt FROM questions WHERE id = $1`, id); !strings.Contains(p, "tester could not measure") {
			t.Errorf("the note is not the question's context: %q", p)
		}
	})
	t.Run("at another decision", func(t *testing.T) {
		w := conducting(t)
		task := w.task()
		w.talk(task)
		w.implemented(task)
		w.refused(task, "decide", `{"action":"ask_person","note":"Open the PR so CI measures / run Docker locally / accept partly met?"}`, "as choices")
		w.refused(task, "decide", `{"action":"ask_person","note":"How?","choices":[{"label":"Ship it","action":"merge"},{"label":"Wait"}]}`, "one of open or draft")
		out := w.must(task, "decide", openChoices("open"))
		options, choices, actions := w.questionShape(out["questionId"].(string))
		if len(options) != 3 || !choices[0].Recommended || strings.Join(actions, ",") != "open,," {
			t.Errorf("options %v, choices %+v, actions %v", options, choices, actions)
		}
		// A choice that carries no action authorizes nothing.
		if status, out := w.call("/internal/questions/"+w.conductorQuestion(task)+"/answer", map[string]any{"text": "Run Docker locally"}); status != 200 {
			t.Fatalf("answer: %d %v", status, out)
		}
		if a := w.str(`SELECT COALESCE(state->>'openAuthority', '') FROM workflow_runs WHERE task_id = $1`, task); a != "" {
			t.Errorf("a choice with no action recorded an authority: %s", a)
		}
	})
	t.Run("a question in the person's own words", func(t *testing.T) {
		w := conducting(t)
		task := w.task()
		w.talk(task)
		w.implemented(task)
		out := w.must(task, "decide", `{"action":"ask_person","note":"What should the greeting say in Portuguese?"}`)
		if options, _, _ := w.questionShape(out["questionId"].(string)); len(options) != 0 {
			t.Errorf("a free question has options %v", options)
		}
	})
}

// A quiet decision's deadline is suspended while its conductor waits on a
// person: no default however long, no Run started, looked at again every
// askingRecheck; once the person answers, or dude withdraws the question,
// it restarts with its full window; once the question's Run ends, or its
// container is gone for good, the deadline applies as it stood.
func TestTheDecisionDeadlineWaitsForThePersonTheConductorAsked(t *testing.T) {
	setup := func(t *testing.T) (*world, string) {
		w := conducting(t)
		w.waitingFor(time.Hour)
		task := w.task()
		w.quietAfterImplement(task)
		w.until("the conductor woken", func() bool { id, _, _ := w.conductor(task); return id != "" })
		w.conductorSpecOf(task)
		w.must(task, "ask_person", `{"question":"Criterion 1 cannot be measured here. Open the PR so CI measures?","choices":["Yes","No"]}`)
		for range 3 {
			w.lapse(task)
			w.pump()
		}
		if n := w.defaulted(task, ""); n != 0 || w.phaseRuns(task, "review") != 0 || w.decisionAt(task) != delivery.PointImplemented {
			t.Fatalf("with the person's answer awaited: %d defaulted, %d reviews, at %q", n, w.phaseRuns(task, "review"), w.decisionAt(task))
		}
		// Held, it looks again by itself soon (askingRecheck), not at an
		// hour's deadline.
		if late := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1
			AND (wake_at IS NULL OR wake_at > now() + interval '35 seconds')`, task); late != 0 {
			t.Fatalf("a held decision does not look again within the recheck: %s",
				w.str(`SELECT COALESCE(wake_at::text, 'none') FROM workflow_runs WHERE task_id = $1`, task))
		}
		return w, task
	}
	decideBy := func(w *world, task string) time.Duration {
		var left float64
		_ = w.owner.QueryRow(context.Background(), `SELECT EXTRACT(EPOCH FROM ((state->'decision'->>'decideBy')::timestamptz - now()))
			FROM workflow_runs WHERE task_id = $1`, task).Scan(&left)
		return time.Duration(left * float64(time.Second))
	}
	t.Run("answered", func(t *testing.T) {
		w, task := setup(t)
		w.answerAs(w.person("Ana"), task, "Yes")
		w.until("the deadline restarted", func() bool {
			mustExec(t, w.owner, `UPDATE workflow_runs SET wake_at = now() WHERE task_id = $1`, task)
			w.pump()
			return decideBy(w, task) > 50*time.Minute
		})
		if n := w.defaulted(task, ""); n != 0 || w.phaseRuns(task, "review") != 0 {
			t.Errorf("after the answer: %d defaulted, %d reviews", n, w.phaseRuns(task, "review"))
		}
	})
	t.Run("withdrawn", func(t *testing.T) {
		// As dude withdraws a question (WithdrawQuestionsTo, leaveGateTx):
		// cancelled, a question.closed event, no answer. Its full window
		// again, from the withdrawal.
		w, task := setup(t)
		q := w.conductorQuestion(task)
		mustExec(t, w.owner, `UPDATE questions SET status = 'cancelled' WHERE id = $1`, q)
		mustExec(t, w.owner, `INSERT INTO events (id, organization_id, project_id, task_id, event_type, actor_type, actor_id, source, payload)
			SELECT 'evt_withdrawn_'||q.id, q.organization_id, t.project_id, q.task_id, $2, 'system', 'workflow', 'orchestrator',
			jsonb_build_object('questionId', q.id, 'by', 'test') FROM questions q JOIN tasks t ON t.id = q.task_id WHERE q.id = $1`, q, delivery.EvQuestionClosed)
		w.until("the deadline restarted", func() bool {
			mustExec(t, w.owner, `UPDATE workflow_runs SET wake_at = now() WHERE task_id = $1`, task)
			w.pump()
			return decideBy(w, task) > 50*time.Minute
		})
		if n := w.defaulted(task, ""); n != 0 || w.phaseRuns(task, "review") != 0 {
			t.Errorf("after the withdrawal: %d defaulted, %d reviews", n, w.phaseRuns(task, "review"))
		}
	})
	t.Run("its Run ended", func(t *testing.T) {
		// A Run's end through its status cancels its open questions
		// (runs_cancel_open_questions): no answer, no question.closed, so
		// no settle time; the deadline applies as it stood.
		w, task := setup(t)
		mustExec(t, w.owner, `UPDATE runs SET status = 'completed', ended_at = now() WHERE task_id = $1 AND role = 'conductor'`, task)
		if s := w.str(`SELECT status::text FROM questions WHERE task_id = $1`, task); s != "cancelled" {
			t.Fatalf("the ended Run's question is %s, want cancelled", s)
		}
		w.lapse(task)
		w.until("the defaults, the wait resumed", func() bool {
			w.pump()
			return w.defaulted(task, delivery.DefaultedDeadline) == 1
		})
	})
	t.Run("its container ended for good", func(t *testing.T) {
		// lux ended the conductor's container for good while dude has not
		// ended its Run yet (Ending): its question is still open, but
		// nobody is there to act on the answer, so it holds nothing.
		w, task := setup(t)
		mustExec(t, w.owner, `UPDATE runs SET lux_state = 'terminated' WHERE task_id = $1 AND role = 'conductor'`, task)
		if s := w.str(`SELECT status::text FROM questions WHERE task_id = $1`, task); s != "open" {
			t.Fatalf("the question is %s, want open", s)
		}
		w.lapse(task)
		mustExec(t, w.owner, `UPDATE workflow_runs SET wake_at = now() WHERE task_id = $1`, task)
		// The workflow's step alone: the syncer's sweep would end the Run
		// (cancelling the question) and hide what the step reads.
		if _, err := w.runtime.Tick(context.Background(), 10); err != nil {
			t.Fatal(err)
		}
		if n := w.defaulted(task, delivery.DefaultedDeadline); n != 1 {
			t.Errorf("%d defaulted with an ending conductor's question open (%s), want 1", n,
				w.str(`SELECT status::text FROM questions WHERE task_id = $1`, task))
		}
	})
}

// The person's newest word wins over an earlier open answer: once the
// gate's question is asked since, its answer — or its wait — is what
// counts, so Another round or Show me the diff opens nothing, nor does the
// question still being open. A later answer to another question of the
// conductor's that does not open it voids the earlier one too.
func TestANewerAnswerOverridesAnEarlierOpenAnswer(t *testing.T) {
	for _, c := range []struct{ name, answer, want string }{
		{"the gate question still open", "", "has not answered"},
		{"answered Another round", "Another round", "not Open or Draft"},
		{"answered Show me the diff", "Show me the diff", "not Open or Draft"},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := conducting(t)
			task := w.task()
			w.reachGateAnswered(task)
			w.must(task, "decide", `{"action":"ask_person"}`)
			if c.answer != "" {
				w.chat(task, c.answer)
				w.until("the gate question answered", func() bool {
					return w.count(`SELECT count(*) FROM questions WHERE task_id = $1 AND pr_gate_heads IS NOT NULL AND status = 'answered'`, task) == 1
				})
			}
			w.refused(task, "decide", `{"action":"open_pull_request"}`, c.want)
			w.pump()
			if n := len(w.gh.Pulls()); n != 0 {
				t.Errorf("%d pull requests opened against the person's newest word", n)
			}
			if v := w.str(`SELECT COALESCE(state->'openAuthority'->'voided'->>'why', '') FROM workflow_runs WHERE task_id = $1`, task); v == "" {
				t.Error("the earlier answer is not voided by the gate question asked since")
			}
		})
	}
	t.Run("a later answer that does not open", func(t *testing.T) {
		w := conducting(t)
		task := w.task()
		w.askedAndAnsweredOpen(task, "open")
		w.must(task, "ask_person", `{"question":"Should the greeting also say goodbye?","choices":["Yes","No"]}`)
		w.refused(task, "decide", `{"action":"open_pull_request"}`, "is kept")
		w.answerAs(w.person("Ana"), task, "No")
		w.until("the earlier answer voided", func() bool {
			return w.str(`SELECT COALESCE(state->'openAuthority'->'voided'->>'why', '') FROM workflow_runs WHERE task_id = $1`, task) != ""
		})
		// Void, it is not said to be kept.
		w.refused(task, "decide", `{"action":"open_pull_request"}`, "is not a decision at")
		w.carriedOut(task, "start_phase", `{"phase":"simplify"}`)
		w.agentsDone(task, "the simplifier's narrow review", func() bool { return w.decisionAt(task) == delivery.PointReviewed })
		w.finishSimplifierReview(task)
		w.until("before the pull request", func() bool { return w.decisionAt(task) == delivery.PointBeforePR })
		w.refused(task, "decide", `{"action":"open_pull_request"}`, "answered the conductor otherwise since")
		if len(w.gh.Pulls()) != 0 {
			t.Error("a pull request opened")
		}
	})
}

// An answer found not to hold is void for good: the conductor dismissing
// the finding that voided it does not bring it back; only the person
// answering again does.
func TestAVoidedOpenAnswerStaysVoid(t *testing.T) {
	w := conducting(t)
	task := w.task()
	w.reachGateAnswered(task)
	mustExec(t, w.owner, `INSERT INTO review_findings (id, organization_id, task_id, run_id, category, severity, title)
		SELECT 'fnd_new', organization_id, id, (SELECT id FROM runs WHERE task_id = $1 AND phase = 'review' LIMIT 1),
		'correctness', 'high', 'A new one' FROM tasks WHERE id = $1`, task)
	w.refused(task, "decide", `{"action":"open_pull_request"}`, "fnd_new")
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = $2`, task, delivery.EvOpenAuthorityVoided); n != 1 {
		t.Errorf("%d voided events, want 1", n)
	}
	w.must(task, "dismiss_finding", `{"id":"fnd_new","reason":"not in scope"}`)
	w.refused(task, "decide", `{"action":"open_pull_request"}`, "fnd_new blocks now")
	if n := len(w.gh.Pulls()); n != 0 {
		t.Fatalf("%d pull requests opened on a voided answer", n)
	}
	w.asksTheGateAgain(task)
}

// testedWith records a completed test Run of the task's attempt, now, with
// a verdict on criterion 2 of status and decision ("" for none).
func (w *world) testedWith(task, status, decision string) {
	w.t.Helper()
	mustExec(w.t, w.owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, phase, status, ended_at)
		SELECT 'run_retest_'||id, organization_id, project_id, id,
			COALESCE((SELECT (state->>'attempt')::int FROM workflow_runs WHERE task_id = $1 ORDER BY created_at DESC LIMIT 1), 1),
			'test', 'completed', now() FROM tasks WHERE id = $1`, task)
	mustExec(w.t, w.owner, `INSERT INTO acceptance_verdicts (organization_id, task_id, run_id, criterion, text, status, decision, decided_at)
		SELECT organization_id, id, 'run_retest_'||id, 2, 'Says hello', $2, NULLIF($3, ''), CASE WHEN $3 <> '' THEN now() END
		FROM tasks WHERE id = $1`, task, status, decision)
}

// What a test since the answer says: a criterion failing now that the
// person was not asked about voids the answer, named; a waived verdict and
// a finding below the fix bar do not.
func TestAnOpenAnswerWeighsTheTestsAndFindingsSince(t *testing.T) {
	t.Run("a new failing criterion", func(t *testing.T) {
		w := conducting(t)
		task := w.task()
		w.reachGateAnswered(task)
		w.testedWith(task, "fail", "")
		w.refused(task, "decide", `{"action":"open_pull_request"}`, "criterion 2 fails now")
		if len(w.gh.Pulls()) != 0 {
			t.Error("a pull request opened")
		}
	})
	for _, c := range []struct{ name, status, decision, severity string }{
		{"a waived verdict", "fail", "waived", ""},
		{"a low finding", "pass", "", "low"},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := conducting(t)
			task := w.task()
			w.reachGateAnswered(task)
			w.testedWith(task, c.status, c.decision)
			if c.severity != "" {
				mustExec(t, w.owner, `INSERT INTO review_findings (id, organization_id, task_id, run_id, category, severity, title)
					SELECT 'fnd_low', organization_id, id, (SELECT id FROM runs WHERE task_id = $1 AND phase = 'review' LIMIT 1),
					'correctness', $2, 'A nit' FROM tasks WHERE id = $1`, task, c.severity)
			}
			out := w.must(task, "decide", `{"action":"open_pull_request"}`)
			if out["authorizedBy"] == nil {
				t.Errorf("open_pull_request: %v", out)
			}
			w.until("the pull request", func() bool { return len(w.gh.Pulls()) == 1 })
		})
	}
}

// authority is the task's recorded open authority's question, "" for none.
func (w *world) authority(task string) string {
	return w.str(`SELECT COALESCE(state->'openAuthority'->>'questionId', '') FROM workflow_runs WHERE task_id = $1
		ORDER BY created_at DESC LIMIT 1`, task)
}

// An answer gives authority only as the conductor's own question's, asked
// in this delivery, before its pull request, while it is live: not the
// gate's own Open (GateAnswer reads that), not a question asked before the
// delivery began, not one answered once the pull request is open or the
// delivery is over.
func TestOnlyAConductorsQuestionInTheLiveDeliveryGivesAuthority(t *testing.T) {
	t.Run("the gate's own Open", func(t *testing.T) {
		w := conducting(t)
		task := w.task()
		w.reachGate(task)
		w.must(task, "decide", `{"action":"ask_person"}`)
		w.chat(task, "Open")
		w.until("the gate answered", func() bool {
			return w.count(`SELECT count(*) FROM questions WHERE task_id = $1 AND pr_gate_heads IS NOT NULL AND status = 'answered'`, task) == 1
		})
		w.pump()
		if a := w.authority(task); a != "" {
			t.Errorf("the gate's Open recorded as authority: %s", a)
		}
	})
	for _, c := range []struct{ name, before string }{
		{"asked before the delivery", `UPDATE questions SET asked_at = (SELECT min(created_at) - interval '1 hour' FROM workflow_runs WHERE task_id = $1)
			WHERE task_id = $1 AND status = 'open'`},
		{"answered with the pull request open", `UPDATE workflow_runs SET state = jsonb_set(state, '{pullRequestIds}', '["pr_x"]') WHERE task_id = $1`},
		{"answered with the delivery over", `UPDATE workflow_runs SET status = 'completed' WHERE task_id = $1`},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := conducting(t)
			task := w.task()
			w.talk(task)
			w.implemented(task)
			w.must(task, "decide", openChoices("open"))
			mustExec(t, w.owner, c.before, task)
			w.answerOpen(task)
			if a := w.authority(task); a != "" {
				t.Errorf("recorded as authority: %s", a)
			}
		})
	}
}

// The wake before the pull request cites the answer only while it holds:
// with a fixer's commit since, it does not say the person already answered.
func TestTheWakeBeforeThePRCitesOnlyAnAnswerThatHolds(t *testing.T) {
	w := conducting(t)
	task := w.task()
	w.askedAndAnsweredOpen(task, "open")
	w.must(task, "decide", `{"action":"next"}`)
	w.until("after the review round", func() bool { return w.decisionAt(task) == delivery.PointReviewed })
	w.must(task, "start_phase", `{"phase":"fix"}`)
	w.until("after the fix", func() bool { return w.decisionAt(task) == delivery.PointFixed })
	w.must(task, "start_phase", `{"phase":"simplify"}`)
	w.finishSimplifierReview(task)
	w.until("before the pull request", func() bool { return w.decisionAt(task) == delivery.PointBeforePR })
	w.until("woken before the pull request", func() bool {
		return w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = $2 AND payload->>'point' = $3`,
			task, delivery.EvDecisionAwaited, delivery.PointBeforePR) > 0
	})
	for _, n := range w.woken(task) {
		if strings.Contains(n, "already answered") {
			t.Errorf("the wake cites an answer that no longer holds: %q", n)
		}
	}
	if v := w.str(`SELECT COALESCE(state->'openAuthority'->'voided'->>'why', '') FROM workflow_runs WHERE task_id = $1`, task); !strings.Contains(v, "not the simplifier's") {
		t.Errorf("voided %q", v)
	}
}

// choices are for decide ask_person away from the gate: refused before the
// pull request, whose are fixed, and on any other action.
func TestDecideChoicesAreRefusedWhereTheyDoNotApply(t *testing.T) {
	w := conducting(t)
	task := w.task()
	w.reachGate(task)
	w.refused(task, "decide", `{"action":"ask_person","choices":[{"label":"Ship"},{"label":"Wait"}]}`, "choices are fixed")
	w.refused(task, "decide", `{"action":"open_pull_request","choices":[{"label":"Ship"},{"label":"Wait"}]}`, "for decide ask_person")
	w.refused(task, "decide", `{"action":"open_pull_request","recommend":"Open"}`, "for decide ask_person")
	if n := w.gateQuestions(task); n != 0 {
		t.Errorf("%d gate questions asked", n)
	}
}

// Nothing opens while the person is being asked: a question of the
// conductor's to the person still open holds an earlier answer back.
func TestAnOpenAnswerWaitsWhileThePersonIsAsked(t *testing.T) {
	w := conducting(t)
	task := w.task()
	w.reachGateAnswered(task)
	w.must(task, "ask_person", `{"question":"Should the greeting also say goodbye?","choices":["Yes","No"]}`)
	w.refused(task, "decide", `{"action":"open_pull_request"}`, "waiting for an answer")
	if len(w.gh.Pulls()) != 0 {
		t.Error("a pull request opened while the person was asked")
	}
}
