package delivery

import (
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestCohortStaggeredResults(t *testing.T) {
	w := newChildWorld(t)
	var cohort string
	var members []string
	err := w.s.DB.InOrg(w.ctx, w.org, func(tx pgx.Tx) error {
		runs := []StartChildInput{w.in, w.in, w.in}
		var err error
		cohort, members, err = StartCohortTx(w.ctx, tx, w.in.Parent, "batch", runs)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	for i, status := range []string{"completed", "failed", "aborted"} {
		if _, err := w.s.SettleChild(w.ctx, w.org, members[i], ChildOutcome{Status: status}); err != nil {
			t.Fatal(err)
		}
		if _, err := w.s.EnqueueChildResults(w.ctx); err != nil {
			t.Fatal(err)
		}
		want := 0
		if i == 2 {
			want = 1
		}
		if got := w.count("directives"); got != want {
			t.Fatalf("after %d: directives %d want %d", i+1, got, want)
		}
	}
	if _, err := w.s.EnqueueChildResults(w.ctx); err != nil {
		t.Fatal(err)
	}
	if got := w.count("directives"); got != 1 {
		t.Fatalf("replay: %d directives", got)
	}
	var text string
	if err := w.owner.QueryRow(w.ctx, `SELECT text FROM directives`).Scan(&text); err != nil {
		t.Fatal(err)
	}
	for _, id := range append(members, cohort) {
		if !strings.Contains(text, id) {
			t.Errorf("digest missing %s", id)
		}
	}
	if strings.Index(text, members[1]) > strings.Index(text, members[0]) {
		t.Error("failed member not worst first")
	}
	if w.count("child_results") != 3 {
		t.Fatal("lost independent results")
	}
}

func TestCohortAtomicRefusal(t *testing.T) {
	w := newChildWorld(t)
	before := w.count("runs")
	err := w.s.DB.InOrg(w.ctx, w.org, func(tx pgx.Tx) error {
		invalid := w.in
		invalid.Profile = "no-such-profile"
		_, _, err := StartCohortTx(w.ctx, tx, w.in.Parent, "invalid", []StartChildInput{w.in, invalid})
		return err
	})
	if !IsRefusal(err) {
		t.Fatalf("invalid member: %v", err)
	}
	if got := w.count("runs"); got != before {
		t.Fatalf("partial runs: %d -> %d", before, got)
	}
	if w.count("child_cohorts") != 0 {
		t.Fatal("partial cohort")
	}
}

func TestCohortRestartTracksReplacement(t *testing.T) {
	w := newChildWorld(t)
	var members []string
	err := w.s.DB.InOrg(w.ctx, w.org, func(tx pgx.Tx) error {
		var err error
		_, members, err = StartCohortTx(w.ctx, tx, w.in.Parent, "restart", []StartChildInput{w.in, w.in})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := w.s.RestartChild(w.ctx, w.org, "wi_life", members[0], "again")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range members {
		if _, err := w.s.SettleChild(w.ctx, w.org, id, ChildOutcome{Status: "completed"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := w.s.EnqueueChildResults(w.ctx); err != nil {
		t.Fatal(err)
	}
	if w.count("directives") != 0 {
		t.Fatal("old generation settled cohort")
	}
	if _, err := w.s.SettleChild(w.ctx, w.org, replacement, ChildOutcome{Status: "failed"}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.EnqueueChildResults(w.ctx); err != nil {
		t.Fatal(err)
	}
	if w.count("directives") != 1 {
		t.Fatal("replacement did not close cohort")
	}
	if _, err := w.s.RestartChild(w.ctx, w.org, "wi_life", replacement, "late"); !IsRefusal(err) {
		t.Fatal(fmt.Sprintf("restart after close: %v", err))
	}
}
