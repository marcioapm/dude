package orchestrator_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

// The next round's reviewer of the asking category is shown the open
// question with its status, resumed (in its round message) or fresh (in
// its first message); the reviewer of another category is not, and no
// writer (the fixer, or the implementer resumed to fix) is given it. The
// re-reviewer's `about: F1` names the finding it was shown as F1; an F
// beyond what it was shown names none.
func TestTheNextReviewerOfTheCategoryIsShownTheQuestion(t *testing.T) {
	for _, resume := range []bool{true, false} {
		t.Run(fmt.Sprintf("resume=%v", resume), func(t *testing.T) {
			world, policy := newWorld, scriptedPolicy()
			if resume {
				world, policy = resuming, resumingPolicy()
			}
			policy.RequiredReviewers = []string{"correctness", "security"}
			w := world(t)
			// A real model's name, so lux is given the prompt itself; the
			// scripted agent plays it all the same.
			w.onModel("reviewer", "llm-review")
			w.onModel("fixer", "llm-fix")
			if resume {
				// The fixer's model for the implementer too, so the first fix
				// round resumes the implementer's lux Run instead of replaying.
				w.onModel("implementer", "llm-fix")
			}
			scripted := w.lux.Decide
			w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
				b := scripted(spec)
				labels, _ := spec["labels"].(map[string]any)
				if labels["dude.phase"] != "review" {
					return b
				}
				workload, _ := spec["workload"].(map[string]any)
				if prompt, _ := workload["prompt"].(string); strings.Contains(prompt, "**security**") {
					return fakelux.Behaviour{Reply: "Nothing to report."}
				}
				switch {
				case strings.Contains(b.Reply, "One problem"):
					b.Reply += "\n```yaml\n" + mixedQuestion + "```\n"
				case strings.Contains(b.Reply, "verdicts"):
					b.Reply += "---\n```yaml\nquestion: Is F1's file meant to be in the release notes?\nabout: F1\n" +
						"---\nquestion: Is F7 still relevant?\nabout: F7\n```\n"
				}
				return b
			}
			wi := w.task()
			w.names(wi, w.repoID)
			w.start(wi, policy)
			w.until("a pull request, or a stop", func() bool {
				return len(w.gh.Pulls()) == 1 || w.taskStatus(wi) == "awaiting_input"
			})
			if s := w.escalationReason(wi); s != "" {
				t.Fatalf("the delivery stopped for a person: %s", s)
			}

			// How each correctness review round went on, and what it was told.
			rows, err := w.owner.Query(context.Background(), `SELECT id, COALESCE(continues_how, '') FROM runs
				WHERE task_id = $1 AND phase = 'review' AND category = 'correctness' ORDER BY created_at, id`, wi)
			if err != nil {
				t.Fatal(err)
			}
			correctness, err := pgx.CollectRows(rows, pgx.RowToStructByPos[struct{ ID, How string }])
			if err != nil {
				t.Fatal(err)
			}
			if len(correctness) < 2 || (correctness[1].How == delivery.ContinuesResume) != resume {
				t.Fatalf("correctness review Runs %+v; want at least two, the second resumed: %v", correctness, resume)
			}
			var messages = map[string][]string{}
			for _, lr := range w.luxRunsOf("review") {
				category := "correctness"
				if strings.Contains(lr.Prompt(), "**security**") {
					category = "security"
				}
				messages[category] = append(messages[category], lr.Prompt())
				messages[category] = append(messages[category], w.lux.Inputs(lr.ID)...)
			}
			// The section RoundMessage adds for the open question, whatever
			// its words: the difference with and without it.
			open := []delivery.EarlierQuestion{{Question: "Do invoices close in UTC or in the organisation's zone?", Status: "open"}}
			section := inserted(delivery.RoundMessage(delivery.PhaseReview, nil, nil, "", nil, nil, nil, ""),
				delivery.RoundMessage(delivery.PhaseReview, nil, nil, "", nil, open, nil, ""))
			if section == "" || !strings.Contains(section, open[0].Question) {
				t.Fatalf("RoundMessage renders no section for an open question: %q", section)
			}
			if len(messages["correctness"]) < 2 {
				t.Fatalf("%d correctness review messages, want the first and the re-review's", len(messages["correctness"]))
			}
			if first := messages["correctness"][0]; strings.Contains(first, open[0].Question) {
				t.Errorf("the first correctness review was shown a question before any was asked:\n%s", first)
			}
			second := messages["correctness"][1]
			if !strings.Contains(second, open[0].Question) || !strings.Contains(second, section) {
				t.Errorf("the re-review was not shown the open question:\n%s\nwant within it:\n%s", second, section)
			}
			if resume {
				// The resumed round's message: its lux Run's input, not a prompt.
				if lr := w.luxRunsOf("review"); len(w.lux.Inputs(lr[0].ID)) == 0 {
					t.Error("the resumed re-review got no round message")
				}
			}
			for _, m := range messages["security"] {
				if strings.Contains(m, open[0].Question) {
					t.Errorf("the security reviewer was shown the correctness reviewer's question:\n%s", m)
				}
			}
			if resume {
				// The first fix round goes on in the implementer's lux Run: what
				// it was sent is the fix row's queued round message, delivered
				// to that lux Run as an input.
				var fixID, how, luxRun string
				if err := w.owner.QueryRow(context.Background(), `SELECT id, COALESCE(continues_how, ''), COALESCE(lux_run_id, '')
					FROM runs WHERE task_id = $1 AND phase = 'fix' ORDER BY created_at, id LIMIT 1`, wi).Scan(&fixID, &how, &luxRun); err != nil {
					t.Fatal(err)
				}
				if how != delivery.ContinuesResume {
					t.Fatalf("the first fix round continues by %q, want %q", how, delivery.ContinuesResume)
				}
				var round string
				if err := w.owner.QueryRow(context.Background(), `SELECT text FROM directives WHERE run_id = $1 AND scope = 'run'
					ORDER BY created_at LIMIT 1`, fixID).Scan(&round); err != nil {
					t.Fatalf("the resumed fix round's message: %v", err)
				}
				if !slices.Contains(w.lux.Inputs(luxRun), round) {
					t.Fatalf("the resumed fix round's message was not sent to lux Run %s:\n%s", luxRun, round)
				}
				if strings.Contains(round, open[0].Question) {
					t.Errorf("the resumed fixer's round message carries the reviewer's question:\n%s", round)
				}
			}
			if !resume && len(w.luxRunsOf("fix")) == 0 {
				t.Fatal("no fresh fixer was submitted to lux")
			}
			for _, phase := range []string{"fix", "implement"} {
				for _, lr := range w.luxRunsOf(phase) {
					for _, m := range append([]string{lr.Prompt()}, w.lux.Inputs(lr.ID)...) {
						if strings.Contains(m, open[0].Question) {
							t.Errorf("a %s Run was given the reviewer's question:\n%s", phase, m)
						}
					}
				}
			}

			var shownF1 string
			if err := w.owner.QueryRow(context.Background(), `SELECT finding_ids[1] FROM runs WHERE id = $1`, correctness[1].ID).
				Scan(&shownF1); err != nil {
				t.Fatal(err)
			}
			about := map[string]string{}
			for _, q := range w.reviewQuestions(wi) {
				if q.RunID == correctness[1].ID {
					about[q.Question] = q.FindingID
				}
			}
			want := map[string]string{"Is F1's file meant to be in the release notes?": shownF1, "Is F7 still relevant?": ""}
			if shownF1 == "" || fmt.Sprint(about) != fmt.Sprint(want) {
				t.Errorf("the re-review's questions are about %v, want %v", about, want)
			}
			if n, q := w.events(wi, delivery.EvReviewQuestionRaised), len(w.reviewQuestions(wi)); n != q {
				t.Errorf("%d %s events for %d questions", n, delivery.EvReviewQuestionRaised, q)
			}
		})
	}
}

// inserted is what with has that without lacks, when with is without with
// one piece put in.
func inserted(without, with string) string {
	p := 0
	for p < len(without) && without[p] == with[p] {
		p++
	}
	s := 0
	for s < len(without)-p && without[len(without)-1-s] == with[len(with)-1-s] {
		s++
	}
	return strings.TrimSpace(with[p : len(with)-s])
}
