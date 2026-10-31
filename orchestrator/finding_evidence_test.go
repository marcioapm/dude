package orchestrator_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

// What a reviewer says of each finding's evidence is stored with it: a
// medium finding with its evidence is stored as said; a medium one without
// is stored unverified, still medium; a low and a note without evidence
// are not unverified; an improvised evidence kind is stored as none.
func TestAFindingsEvidenceIsStoredAndOneWithoutItAtMediumIsUnverified(t *testing.T) {
	w := newWorld(t)
	reviews := 0
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		labels, _ := spec["labels"].(map[string]any)
		switch labels["dude.phase"] {
		case "review":
			if reviews++; reviews == 1 {
				return fakelux.Behaviour{Reply: "```yaml\n" +
					"severity: medium\ncategory: correctness\nfile: FACTORY.md\nline: 1\ntitle: Evidenced\ndescription: D.\n" +
					"trivial: no\nintroduced: yes\nscenario: Reading FACTORY.md after a delivery shows nothing.\n" +
					"evidence_kind: command\nevidence: |\n  cat FACTORY.md\n  (empty)\n" +
					"---\nseverity: medium\ncategory: correctness\nfile: FACTORY.md\nline: 1\ntitle: Unevidenced\ndescription: D.\n" +
					"---\nseverity: low\ncategory: correctness\ntitle: Low\ndescription: D.\ntrivial: yes\n" +
					"---\nseverity: note\ncategory: correctness\ntitle: Note\ndescription: D.\n" +
					"---\nseverity: high\ncategory: correctness\nfile: FACTORY.md\nline: 1\ntitle: Improvised\ndescription: D.\n" +
					"scenario: S.\nevidence_kind: hunch\nevidence: E.\n```\n"}
			}
			return fakelux.Behaviour{Reply: "```yaml\nverdicts:\n  F1: fixed\n```\n"}
		case "implement", "fix":
			return fakelux.Behaviour{Commit: map[string]string{"FACTORY.md": fmt.Sprint(labels["dude.run"]) + "\n"}, Message: "work"}
		}
		return fakelux.Behaviour{Reply: "Nothing to simplify."}
	}
	wi := w.task()
	w.deliver(wi)
	w.until("the first review's findings", func() bool {
		return w.count(`SELECT count(*) FROM review_findings WHERE task_id = $1`, wi) == 5
	})
	type stored struct {
		severity, trivial, introduced, kind, evidence, scenario string
		unverified                                              bool
	}
	got := map[string]stored{}
	rows, err := w.owner.Query(context.Background(), `SELECT title, severity::text, COALESCE(trivial::text, '-'), COALESCE(introduced::text, '-'),
		COALESCE(evidence_kind, '-'), COALESCE(evidence, '-'), COALESCE(scenario, '-'), unverified FROM review_findings WHERE task_id = $1`, wi)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var title string
		var s stored
		if err := rows.Scan(&title, &s.severity, &s.trivial, &s.introduced, &s.kind, &s.evidence, &s.scenario, &s.unverified); err != nil {
			t.Fatal(err)
		}
		got[title] = s
	}
	want := map[string]stored{
		"Evidenced":   {"medium", "false", "true", "command", "cat FACTORY.md (empty)", "Reading FACTORY.md after a delivery shows nothing.", false},
		"Unevidenced": {"medium", "-", "-", "-", "-", "-", true},
		"Low":         {"low", "true", "-", "-", "-", "-", false},
		"Note":        {"note", "-", "-", "-", "-", "-", false},
		"Improvised":  {"high", "-", "-", "-", "E.", "S.", true},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("stored\n%v\nwant\n%v", got, want)
	}
	var unverified int
	_ = w.owner.QueryRow(context.Background(), `SELECT (payload->>'unverified')::int FROM events WHERE task_id = $1
		AND event_type = 'review.completed' ORDER BY cursor LIMIT 1`, wi).Scan(&unverified)
	if unverified != 2 {
		t.Errorf("review.completed counts %d unverified, want 2", unverified)
	}
}
