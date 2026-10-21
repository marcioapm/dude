package orchestrator_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/fakeagent"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

// A re-review's verdicts are kept on its Run, a still as much as a fixed:
// the finding is judged still after the first fix and fixed after the
// second, and each review Run reads back what it said of it. The finding
// keeps the criterion and caused_by_fix it was raised with.
func TestEachReviewRunKeepsItsVerdicts(t *testing.T) {
	w := newWorld(t)
	var reviews int
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		labels, _ := spec["labels"].(map[string]any)
		switch labels["dude.phase"] {
		case "review":
			switch reviews++; reviews {
			case 1:
				return fakelux.Behaviour{Reply: "```yaml\n" + fakeagent.Finding + "caused_by_fix: yes\n```\n"}
			case 2:
				return fakelux.Behaviour{Reply: "```yaml\nverdicts:\n  F1: still\n```\n"}
			case 3:
				return fakelux.Behaviour{Reply: "```yaml\nverdicts:\n  F1: fixed\n```\n"}
			}
			return fakelux.Behaviour{Reply: "Nothing new."}
		case "implement", "fix":
			return fakelux.Behaviour{Commit: map[string]string{"FACTORY.md": fmt.Sprint(labels["dude.run"]) + "\n"}, Message: "work"}
		}
		return fakelux.Behaviour{Reply: "Nothing to simplify."}
	}
	wi := w.task()
	w.deliver(wi)
	w.until("a pull request, or a stop", func() bool {
		return len(w.gh.Pulls()) == 1 || w.taskStatus(wi) == "awaiting_input"
	})
	if s := w.escalationReason(wi); s != "" {
		t.Fatalf("the delivery stopped for a person: %s", s)
	}
	var finding, acceptance string
	var causedByFix bool
	if err := w.owner.QueryRow(context.Background(), `SELECT id, COALESCE(acceptance, ''), caused_by_fix FROM review_findings WHERE task_id = $1`, wi).
		Scan(&finding, &acceptance, &causedByFix); err != nil {
		t.Fatal(err)
	}
	if acceptance != fakeagent.FindingAcceptance || !causedByFix {
		t.Errorf("the finding was stored with acceptance %q, caused_by_fix %v; want %q, true", acceptance, causedByFix, fakeagent.FindingAcceptance)
	}
	rows, err := w.owner.Query(context.Background(), `SELECT verdicts FROM runs WHERE task_id = $1 AND phase = 'review'
		ORDER BY created_at, id`, wi)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []map[string]string
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var v map[string]string
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Fatal(err)
		}
		got = append(got, v)
	}
	want := []map[string]string{{}, {finding: "still"}, {finding: "fixed"}}
	if len(got) < len(want) {
		t.Fatalf("review Runs' verdicts = %v, want at least %v", got, want)
	}
	for i, v := range want {
		if fmt.Sprint(got[i]) != fmt.Sprint(v) {
			t.Errorf("review %d's verdicts = %v, want %v", i+1, got[i], v)
		}
	}
}
