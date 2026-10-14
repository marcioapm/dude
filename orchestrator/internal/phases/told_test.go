package phases

import (
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"
)

// RecordTold writes one run.told per Run and epoch: a second call for an
// epoch already recorded adds nothing and keeps the first text, and another
// epoch gets its own.
func TestRecordToldWritesOncePerEpoch(t *testing.T) {
	w := newResumeWorld(t)
	tell := func(epoch int, text string) {
		t.Helper()
		RecordTold(w.ctx, w.s.DB, w.s.Log, w.run.ref(), &ResizePlan{moveID: "mmv_1", toldKind: "moved", toldText: text}, epoch)
	}
	tell(2, "first")
	tell(2, "second")
	tell(3, "third")
	rows, err := w.owner.Query(w.ctx, `SELECT (payload->>'epoch')::int, payload->>'text' FROM events
		WHERE run_id = $1 AND event_type = $2 ORDER BY cursor`, w.run.ID, EvRunTold)
	if err != nil {
		t.Fatal(err)
	}
	type told struct {
		Epoch int
		Text  string
	}
	got, err := pgx.CollectRows(rows, pgx.RowToStructByPos[told])
	if err != nil {
		t.Fatal(err)
	}
	if want := []told{{2, "first"}, {3, "third"}}; !slices.Equal(got, want) {
		t.Fatalf("run.told %+v, want %+v", got, want)
	}
}
