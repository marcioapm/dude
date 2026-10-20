package orchestrator_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

// resuming is the world with rounds going on from the last: its project's
// policy, and the delivery's, keep reviewers and testers for their next
// round.
func resuming(t *testing.T) *world {
	w := newWorld(t)
	mustExec(t, w.owner, `UPDATE projects SET delivery_policy = delivery_policy || '{"resumeRounds":true}' WHERE id = $1`, w.project)
	return w
}

func resumingPolicy() delivery.Policy {
	p := scriptedPolicy()
	p.ResumeRounds = true
	return p
}

// luxRunsOf are the fake lux's Runs of a dude phase, in submit order.
func (w *world) luxRunsOf(phase string) []*fakelux.Run {
	var out []*fakelux.Run
	for _, r := range w.lux.Runs() {
		var spec struct {
			Labels map[string]string `json:"labels"`
		}
		_ = json.Unmarshal(r.Spec, &spec)
		if spec.Labels["dude.phase"] == phase {
			out = append(out, r)
		}
	}
	return out
}

// reviewRow is one review Run as the test reads it.
type reviewRow struct {
	ID, Continues, How, LuxRun string
	Base                       map[string]string
}

func (w *world) reviewRows(task string) []reviewRow {
	w.t.Helper()
	rows, err := w.owner.Query(context.Background(), `SELECT id, COALESCE(continues_run_id, ''), COALESCE(continues_how, ''),
			COALESCE((SELECT payload->>'luxRunId' FROM events e WHERE e.run_id = r.id AND e.event_type = 'run.lease.acquired'), r.lux_run_id, ''),
			base_refs
		FROM runs r WHERE task_id = $1 AND phase = 'review' ORDER BY created_at, id`, task)
	if err != nil {
		w.t.Fatal(err)
	}
	defer rows.Close()
	var out []reviewRow
	for rows.Next() {
		var r reviewRow
		if err := rows.Scan(&r.ID, &r.Continues, &r.How, &r.LuxRun, &r.Base); err != nil {
			w.t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

// A re-review takes the first round's reviewer back up: a new Run that
// continues it, on the same lux Run, moved to the fix's head and told what
// changed and which of its findings to judge. Its verdict resolves the
// finding it raised, which keeps its id and its one fix attempt; the agent's
// cost is counted once across the two rows.
func TestASecondReviewRoundResumesTheFirstReviewer(t *testing.T) {
	w := resuming(t)
	wi := w.task()
	w.names(wi, w.repoID)
	w.start(wi, resumingPolicy())
	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })

	reviews := w.reviewRows(wi)
	if len(reviews) < 2 {
		t.Fatalf("%d review Runs, want the first round and its re-review", len(reviews))
	}
	first, second := reviews[0], reviews[1]
	if second.Continues != first.ID || second.How != delivery.ContinuesResume {
		t.Fatalf("the re-review %s continues %q (%s), want %s resumed", second.ID, second.Continues, second.How, first.ID)
	}
	if first.LuxRun == "" || second.LuxRun != "" && second.LuxRun != first.LuxRun {
		t.Fatalf("lux Runs: first %q, re-review %q", first.LuxRun, second.LuxRun)
	}
	// Every round went on in the one lux Run: no second reviewer was
	// submitted.
	luxReviews := w.luxRunsOf("review")
	if len(luxReviews) != 1 || luxReviews[0].ID != first.LuxRun {
		t.Fatalf("%d reviewers submitted to lux, want the first round's only", len(luxReviews))
	}
	lr := luxReviews[0]
	if len(lr.ResumeSyncs) == 0 || len(lr.ResumeSyncs[0]) != 1 || lr.ResumeSyncs[0][0].Ref != second.Base["target"] {
		t.Fatalf("resume syncs %v, want the checkout moved to the fix's head %s", lr.ResumeSyncs, second.Base["target"])
	}
	inputs := w.lux.Inputs(lr.ID)
	var round string
	for _, in := range inputs {
		if strings.Contains(in, "## A new round") {
			round = in
			break
		}
	}
	if round == "" {
		t.Fatalf("the resumed reviewer was never told of its new round; inputs: %q", inputs)
	}
	if want := "git log " + first.Base["target"] + "..HEAD"; !strings.Contains(round, want) {
		t.Errorf("the round message does not name the commits since (%s):\n%s", want, round)
	}
	if !strings.Contains(round, "**F1**") || !strings.Contains(round, "verdicts:") {
		t.Errorf("the round message does not list the finding to judge:\n%s", round)
	}
	if !strings.Contains(round, "outside the lines it touched") {
		t.Errorf("the round message does not warn the change may break what it did not touch:\n%s", round)
	}

	// The finding: raised once, by the first round, judged fixed by the
	// second, its one fix attempt kept.
	if n := w.count(`SELECT count(*) FROM review_findings WHERE task_id = $1`, wi); n != 1 {
		t.Fatalf("%d findings, want the one raised", n)
	}
	if n := w.count(`SELECT count(*) FROM review_findings WHERE task_id = $1 AND run_id = $2 AND status = 'resolved' AND fix_attempts = 1`,
		wi, first.ID); n != 1 {
		t.Fatalf("the first round's finding is not resolved on its own id with one attempt")
	}
	// The agent's running cost is its lux Run's: split across the rows,
	// never counted twice.
	if n := w.count(`SELECT count(*) FROM (SELECT sum(agent_cost_usd) AS s FROM runs WHERE task_id = $1 AND phase = 'review') x
		WHERE s = 0.01`, wi); n != 1 {
		t.Fatal("the reviewers' agent cost is not the one lux Run's 0.01")
	}
}
