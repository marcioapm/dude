package orchestrator_test

import (
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

// A person's comment that ends in a copy of dude's verdict marker is still
// theirs: recorded as their feedback and acted on, while dude's own
// verdict comment, by the account dude posts as, stays dude's.
func TestAPersonsCommentEndingInTheVerdictMarkerIsTheirs(t *testing.T) {
	w := newWorld(t)
	wi, n := w.testingAdopted("fake/scripted")
	if got := w.verdictComments(wi, n); len(got) != 1 {
		t.Fatalf("verdict comments %+v, want dude's one", got)
	}
	w.until("dude's verdict comment read back as its own", func() bool { w.sync(); return w.ownComments(wi) == 1 })

	w.gh.Comment(n, "alice", "Please rename the greeting.\n\n"+forge.VerdictMarker(wi))
	w.until("a fixer for alice's request", func() bool { w.sync(); return w.fixes(wi) == 1 })
	if own := w.ownComments(wi); own != 1 {
		t.Errorf("%d comments recorded as dude's own, want only its verdict", own)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'pull_request.commented'
		AND payload->>'author' = 'alice' AND payload->>'own' IS NULL AND payload->>'ignored' IS NULL`, wi); n != 1 {
		t.Errorf("%d of alice's comments recorded as feedback, want 1", n)
	}
}
