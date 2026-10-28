package delivery

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

func TestCohortDeliveryRollbackAndReplay(t *testing.T) {
	w := newChildWorld(t)
	var members []string
	if err := w.s.DB.InOrg(w.ctx, w.org, func(tx pgx.Tx) error {
		var err error
		_, members, err = StartCohortTx(w.ctx, tx, w.in.Parent, "crash", []StartChildInput{w.in})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.SettleChild(w.ctx, w.org, members[0], ChildOutcome{Status: "completed"}); err != nil {
		t.Fatal(err)
	}
	claim := claimChildForTest(t, w, members[0], "crashed-worker")
	crash := errors.New("crash before commit")
	if err := w.s.DB.InOrg(w.ctx, w.org, func(tx pgx.Tx) error {
		if delivered, err := deliverChildResultTx(w.ctx, tx, claim); err != nil || !delivered {
			t.Fatalf("deliver: %v %v", delivered, err)
		}
		return crash
	}); !errors.Is(err, crash) {
		t.Fatal(err)
	}
	if w.count("directives") != 0 {
		t.Fatal("rollback retained digest")
	}
	w.exec(`UPDATE child_result_outbox SET claimed_until=now()-interval '1 minute'`)
	for i := 0; i < 2; i++ {
		if _, err := w.s.EnqueueChildResults(w.ctx); err != nil {
			t.Fatal(err)
		}
	}
	if w.count("directives") != 1 {
		t.Fatal("recovery must queue exactly one digest")
	}
}

func TestCohortRejectsZeroRowConsume(t *testing.T) {
	w := newChildWorld(t)
	var members []string
	if err := w.s.DB.InOrg(w.ctx, w.org, func(tx pgx.Tx) error {
		var err error
		_, members, err = StartCohortTx(w.ctx, tx, w.in.Parent, "receipt", []StartChildInput{w.in})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.SettleChild(w.ctx, w.org, members[0], ChildOutcome{Status: "completed"}); err != nil {
		t.Fatal(err)
	}
	claim := claimChildForTest(t, w, members[0], "worker")
	w.exec(`CREATE FUNCTION ignore_cohort_consume() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.consumed_at IS NOT NULL THEN RETURN NULL; END IF; RETURN NEW; END $$`)
	w.exec(`CREATE TRIGGER ignore_cohort_consume BEFORE UPDATE OF consumed_at ON child_result_outbox FOR EACH ROW EXECUTE FUNCTION ignore_cohort_consume()`)
	err := w.s.DB.InOrg(w.ctx, w.org, func(tx pgx.Tx) error {
		_, err := deliverChildResultTx(w.ctx, tx, claim)
		return err
	})
	if err == nil || w.count("directives") != 0 {
		t.Fatalf("zero-row receipt committed digest: %v", err)
	}
}

func TestCohortEndedParentDoesNotNotifyReplacement(t *testing.T) {
	w := newChildWorld(t)
	var members []string
	if err := w.s.DB.InOrg(w.ctx, w.org, func(tx pgx.Tx) error {
		var err error
		_, members, err = StartCohortTx(w.ctx, tx, w.in.Parent, "ended", []StartChildInput{w.in, w.in})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	w.exec(`UPDATE runs SET status='completed' WHERE id='run_parent'`)
	w.exec(`INSERT INTO runs(id,organization_id,project_id,task_id,attempt,role,status) VALUES('replacement',$1,'prj_life','wi_life',3,'conductor','running')`, w.org)
	for _, id := range members {
		if _, err := w.s.SettleChild(w.ctx, w.org, id, ChildOutcome{Status: "aborted"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := w.s.EnqueueChildResults(w.ctx); err != nil {
		t.Fatal(err)
	}
	if w.count("directives") != 0 {
		t.Fatal("ended parent or replacement received digest")
	}
	var stale int
	if err := w.owner.QueryRow(w.ctx, `SELECT count(*) FROM child_results WHERE stale`).Scan(&stale); err != nil || stale != 2 {
		t.Fatalf("stale results %d: %v", stale, err)
	}
}

func TestCohortWriterConflictRollsBackEarlierMember(t *testing.T) {
	w := newChildWorld(t)
	before := w.count("runs")
	writer := w.in
	writer.Profile = "implementer"
	writer.Capabilities.PublishCode = true
	err := w.s.DB.InOrg(w.ctx, w.org, func(tx pgx.Tx) error {
		_, _, err := StartCohortTx(w.ctx, tx, w.in.Parent, "writer", []StartChildInput{w.in, writer, writer})
		return err
	})
	if !IsRefusal(err) || w.count("runs") != before || w.count("child_cohort_members") != 0 {
		t.Fatalf("writer refusal left partial admission: %v", err)
	}
}

func TestCohortReportBoundAndQuote(t *testing.T) {
	report := boundedCohortReport(strings.Repeat("`", 30) + strings.Repeat("界", 20000))
	if len(report) > 6000 || !utf8.ValidString(report) || !strings.Contains(report, "[report clipped;") {
		t.Fatalf("invalid bound/clipping: %d bytes", len(report))
	}
	// A fenced report must parse as one quote, even with adversarial backticks.
	if got := strings.Count("```text\n"+report+"\n```", "```"); got != 2 {
		t.Fatalf("report escaped quote: %d fences", got)
	}
}

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
