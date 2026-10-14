package phases

import (
	"testing"
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
	defer rows.Close()
	type told struct {
		epoch int
		text  string
	}
	var got []told
	for rows.Next() {
		var e told
		if err := rows.Scan(&e.epoch, &e.text); err != nil {
			t.Fatal(err)
		}
		got = append(got, e)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if want := []told{{2, "first"}, {3, "third"}}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("run.told %+v, want %+v", got, want)
	}
}
