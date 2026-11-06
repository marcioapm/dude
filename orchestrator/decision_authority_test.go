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
// person: no default however long, no Run started; once the person answers
// it restarts with its full window; once the question's Run ends it
// resumes.
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
		w, task := setup(t)
		mustExec(t, w.owner, `UPDATE questions SET status = 'cancelled', answered_at = now() - interval '2 hours'
			WHERE task_id = $1 AND status = 'open'`, task)
		w.lapse(task)
		w.until("the defaults, the wait resumed", func() bool {
			w.pump()
			return w.defaulted(task, delivery.DefaultedDeadline) == 1
		})
	})
	t.Run("its Run ended", func(t *testing.T) {
		w, task := setup(t)
		mustExec(t, w.owner, `UPDATE runs SET status = 'completed', ended_at = now() - interval '2 hours'
			WHERE task_id = $1 AND role = 'conductor'`, task)
		w.lapse(task)
		w.until("the defaults, the wait resumed", func() bool {
			w.pump()
			return w.defaulted(task, delivery.DefaultedDeadline) == 1
		})
	})
}
