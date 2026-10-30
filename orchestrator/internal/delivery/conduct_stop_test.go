package delivery

import (
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5"
)

// A cohort member the conductor stops counts in the cohort's one digest as
// aborted, whatever its Run reported: with the other member completed, one
// digest, one aborted and one completed.
func TestStoppingACohortMemberCompletesTheDigestAsAborted(t *testing.T) {
	w := newChildWorld(t)
	cohort, members := w.startCohort(t, "pair", w.in, w.in)
	var status string
	if err := w.s.DB.InOrg(w.ctx, w.org, func(tx pgx.Tx) error {
		var err error
		status, err = ConductStopRunTx(w.ctx, tx, w.in.Parent.Run, members[0], "Its twin has the answer already.")
		return err
	}); err != nil || status != StopStopping {
		t.Fatalf("stop_run: %q %v", status, err)
	}
	// The syncer settles the stopped member when its Run ends; whatever it
	// read of the Run, a stopped child is aborted.
	if _, err := w.s.SettleChild(w.ctx, w.org, members[0], ChildOutcome{Status: "completed"}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.EnqueueChildResults(w.ctx); err != nil {
		t.Fatal(err)
	}
	if got := w.count("directives"); got != 0 {
		t.Fatalf("a digest before every member settled: %d directives", got)
	}
	if _, err := w.s.SettleChild(w.ctx, w.org, members[1], ChildOutcome{Status: "completed"}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.EnqueueChildResults(w.ctx); err != nil {
		t.Fatal(err)
	}
	if got := w.count("directives"); got != 1 {
		t.Fatalf("%d digests, want 1", got)
	}
	var raw []byte
	if err := w.owner.QueryRow(w.ctx, `SELECT payload FROM events WHERE event_type = 'run.cohort.queued'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var payload struct {
		CohortID         string
		Completed, Total int
		Members          []struct{ RunID, Status string }
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	statuses := map[string]string{}
	for _, m := range payload.Members {
		statuses[m.RunID] = m.Status
	}
	if payload.CohortID != cohort || payload.Completed != 1 || payload.Total != 2 ||
		statuses[members[0]] != "aborted" || statuses[members[1]] != "completed" {
		t.Fatalf("the digest's counts: %s", raw)
	}
}
