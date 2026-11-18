package orchestrator_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// #485 round 3: the reviews' findings on round 2, as tests.

// Reviewer (rev485t-t, finding 2; rev485t-q F1): a fixer that published to
// another repository only, none of it on the pull request its answers are
// on, has them withheld there: "its fix did not change target".
func TestReviewerAnswersWhereTheFixerPublishedNothing(t *testing.T) {
	a := newAnswering(t, newWorld(t))
	ref := a.fixer("run_fixer")
	a.answerAll(t, ref, "Fixed:")
	if err := a.w.app.InOrg(context.Background(), a.w.org, func(tx pgx.Tx) error {
		_, err := ledger.Append(context.Background(), tx, ref.Event(delivery.EvGitCommitCreated, ledger.ActorAgent,
			map[string]any{"repo": "elsewhere", "headSha": "deadbeef"}))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	a.end(ref, "completed")
	done, err := a.post()
	t.Logf("posted %+v %v", done, err)
	if n := len(a.posted("Fixed: " + a.firstID)); n != 0 {
		t.Errorf("posted %d times on target#1, where the fixer published nothing", n)
	}
	a.countPosted(t, "Fixed:", 0)
	if f, s := a.resolved(); f || s {
		t.Errorf("resolved %v %v where the fixer published nothing", f, s)
	}
	if err != nil || len(done) != 1 || done[0].Withheld != "its fix did not change target" {
		t.Errorf("want withheld with the reason, got %+v %v", done, err)
	}
	if n := a.w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = $2 AND payload->>'withheld' = 'true'`,
		ref.RunID, delivery.EvAnswerFailed); n != 5 {
		t.Errorf("%d answers withheld, want 5", n)
	}
	if strings.Contains(done[0].Line(), "is on the pull request's head") {
		t.Errorf("the wake claims the fix is on the head: %s", done[0].Line())
	}
}
