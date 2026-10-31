package orchestrator_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

// A writer's own account of its work is stored on its Run: the implementer
// that says it is partial, with its line, is stored partial; the fixer that
// says nothing is stored done.
func TestAWritersOutcomeIsStoredOnItsRun(t *testing.T) {
	w := newWorld(t)
	reviews := 0
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		labels, _ := spec["labels"].(map[string]any)
		switch labels["dude.phase"] {
		case "implement":
			return fakelux.Behaviour{Commit: map[string]string{"FACTORY.md": "implemented\n"}, Message: "work",
				Checks: "```yaml\noutcome: partial\noutcome_note: the second greeting is left\nchecks: []\n```"}
		case "fix":
			return fakelux.Behaviour{Commit: map[string]string{"FACTORY.md": fmt.Sprint(labels["dude.run"]) + "\n"}, Message: "fix"}
		case "review":
			if reviews++; reviews == 1 {
				return fakelux.Behaviour{Reply: reviewFinding("FACTORY.md is wrong")}
			}
			return fakelux.Behaviour{Reply: "```yaml\nverdicts:\n  F1: fixed\n```\n"}
		}
		return fakelux.Behaviour{Reply: "Nothing to simplify."}
	}
	wi := w.task()
	w.deliver(wi)
	w.until("the fix to end", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'fix' AND status = 'completed'`, wi) == 1
	})
	got := map[string]string{}
	rows, err := w.owner.Query(context.Background(), `SELECT phase::text, COALESCE(writer_outcome, '-') || ' ' || COALESCE(writer_outcome_note, '-')
		FROM runs WHERE task_id = $1 AND phase IN ('implement', 'fix', 'review')`, wi)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var phase, outcome string
		if err := rows.Scan(&phase, &outcome); err != nil {
			t.Fatal(err)
		}
		got[phase] = outcome
	}
	want := map[string]string{"implement": "partial the second greeting is left", "fix": "done -", "review": "- -"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("writer outcomes %v, want %v", got, want)
	}
}
