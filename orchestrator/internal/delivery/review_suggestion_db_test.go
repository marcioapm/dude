package delivery

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// The finding a reply in dude's thread is about (threadFindingTx, what
// the conductor's wake shows) and the verdict's findings
// (incompleteFindingsTx), for each kind of accepted finding. Only one the
// conductor kept as a suggestion carries its note and is shown as
// "Suggestion, not blocking". One withdrawn as wrong or a duplicate, or
// accepted by a person, even in a suggestion's words, is shown to the
// conductor as it was filed, holding the verdict as filed, and is left out
// of the verdict.
func TestOnlyAKeptSuggestionReachesTheWakeAndTheVerdictAsOne(t *testing.T) {
	w := newGoldenWorld(t)
	w.exec(t, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, category, kind, base_refs)
		VALUES ('run_lens', $1, 'prj_g', 'wi_g', 1, 'completed', 'review', 'reviewer', 'correctness', 'agent', '{"api":"aaaa1111"}')`)
	notes := map[string]string{
		"fnd_kept":      DismissedByConductor + "suggestion: gate 2: only DEV_MODE=1 reaches it, cmd/dev.go:12.",
		"fnd_wrong":     DismissedByConductor + "wrong: the caller checks it, api/x.go:4.",
		"fnd_duplicate": DismissedByConductor + "duplicate: fnd_kept",
		"fnd_person":    "accepted by a person: suggestion: gate 2: only DEV_MODE=1 reaches it.",
	}
	for id, note := range notes {
		w.exec(t, `INSERT INTO review_findings (id, organization_id, task_id, run_id, category, severity, title, description,
			status, resolution_note, introduced, file, line)
			VALUES ($2, $1, 'wi_g', 'run_lens', 'correctness', 'medium', 'Local mode leaks', 'D.', 'accepted', $3, true, 'a.go', 3)`, id, note)
	}
	for id := range notes {
		var f *verdictFinding
		w.in(t, func(ctx context.Context, tx pgx.Tx) (_ string, err error) {
			f, err = threadFindingTx(ctx, tx, id)
			return "", err
		})
		if f == nil {
			t.Fatalf("%s: no finding", id)
		}
		text := f.inlineText()
		_, _, kept := f.suggestion()
		if id == "fnd_kept" {
			if !kept || !strings.HasPrefix(text, "Suggestion, not blocking: **Local mode leaks** (medium)") {
				t.Errorf("the kept suggestion in the wake: %+v %q", f, text)
			}
			continue
		}
		if kept || f.Kept != "" || !f.blocks() || strings.Contains(text, "Suggestion, not blocking") || strings.Contains(text, "Kept as a suggestion") ||
			!strings.HasPrefix(text, "**Local mode leaks** (medium)") {
			t.Errorf("%s in the wake, not as filed: %+v %q", id, f, text)
		}
	}
	var findings []verdictFinding
	w.in(t, func(ctx context.Context, tx pgx.Tx) (_ string, err error) {
		findings, err = incompleteFindingsTx(ctx, tx, &State{TaskID: "wi_g", Attempt: 1}, "api", "aaaa1111")
		return "", err
	})
	if len(findings) != 1 || findings[0].ID != "fnd_kept" {
		t.Fatalf("the verdict's findings: %+v", findings)
	}
	body, _, _ := verdictReviewOf(verdictDecision{Headline: "Approved"}, "aaaa1111", "main", nil, findings, "")
	if strings.Count(body, "kept as a suggestion") != 1 {
		t.Fatalf("body:\n%s", body)
	}
}
