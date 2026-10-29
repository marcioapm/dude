package delivery

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

func (w *childWorld) startCohort(t *testing.T, key string, runs ...StartChildInput) (string, []string) {
	t.Helper()
	var cohort string
	var members []string
	if err := w.s.DB.InOrg(w.ctx, w.org, func(tx pgx.Tx) error {
		var err error
		cohort, members, err = StartCohortTx(w.ctx, tx, w.in.Parent, key, runs)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return cohort, members
}

func TestCohortDeliveryRollbackAndReplay(t *testing.T) {
	w := newChildWorld(t)
	_, members := w.startCohort(t, "crash", w.in)
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
	w.assertQueuedEvents(0)
	w.exec(`UPDATE child_result_outbox SET claimed_until=now()-interval '1 minute'`)
	for i := 0; i < 2; i++ {
		if _, err := w.s.EnqueueChildResults(w.ctx); err != nil {
			t.Fatal(err)
		}
	}
	if w.count("directives") != 1 {
		t.Fatal("recovery must queue exactly one digest")
	}
	w.assertQueuedEvents(1)
}

func (w *childWorld) assertQueuedEvents(want int) {
	w.t.Helper()
	var got int
	if err := w.owner.QueryRow(w.ctx, `SELECT count(*) FROM events WHERE event_type='run.cohort.queued'`).Scan(&got); err != nil || got != want {
		w.t.Fatalf("queued events %d want %d: %v", got, want, err)
	}
}

func TestCohortRejectsZeroRowConsume(t *testing.T) {
	w := newChildWorld(t)
	_, members := w.startCohort(t, "receipt", w.in)
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
	_, members := w.startCohort(t, "ended", w.in, w.in)
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
	w.assertQueuedEvents(0)
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

func TestCohortReplacedParentDoesNotQueueVisibleResult(t *testing.T) {
	w := newChildWorld(t)
	_, members := w.startCohort(t, "replaced", w.in)
	w.exec(`INSERT INTO runs(id,organization_id,project_id,task_id,attempt,role,status) VALUES('replacement',$1,'prj_life','wi_life',3,'conductor','completed')`, w.org)
	w.exec(`UPDATE runs SET replaced_by='replacement' WHERE id='run_parent'`)
	if _, err := w.s.SettleChild(w.ctx, w.org, members[0], ChildOutcome{Status: "completed"}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.EnqueueChildResults(w.ctx); err != nil {
		t.Fatal(err)
	}
	w.assertQueuedEvents(0)
	if w.count("directives") != 0 {
		t.Fatal("replaced parent received digest")
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
	cohort, members := w.startCohort(t, "batch", w.in, w.in, w.in)
	w.exec(`INSERT INTO events(id,organization_id,event_type,run_id,task_id,actor_type,actor_id,source,payload)
		VALUES ('evt_private_report',$1,'agent.message',$2,'wi_life','agent',$2,'test','{"text":"PRIVATE raw report credential=do-not-project"}')`, w.org, members[0])
	if err := w.s.DB.InOrg(w.ctx, w.org, func(tx pgx.Tx) error {
		_, err := AskTx(w.ctx, tx, w.in.Parent.Run, "May I proceed?", []string{"Yes", "No"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	w.exec(`UPDATE runs SET status='paused', dude_pause='person', waiting_since=now() WHERE id='run_parent'`)
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
		var events int
		if err := w.owner.QueryRow(w.ctx, `SELECT count(*) FROM events WHERE event_type='run.cohort.queued'`).Scan(&events); err != nil || events != want {
			t.Fatalf("after %d: queued events %d want %d: %v", i+1, events, want, err)
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
	var raw []byte
	if err := w.owner.QueryRow(w.ctx, `SELECT payload FROM events WHERE event_type='run.cohort.queued' AND task_id='wi_life' AND run_id='run_parent' AND project_id='prj_life' AND organization_id=$1`, w.org).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || len(fields) != 6 {
		t.Fatalf("unexpected public metadata fields: %s: %v", raw, err)
	}
	var payload struct {
		CohortID, ParentRunID, DirectiveID string
		Completed, Total                   int
		Members                            []struct{ RunID, Status string }
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.CohortID != cohort || payload.ParentRunID != "run_parent" || payload.DirectiveID == "" || payload.Completed != 1 || payload.Total != 3 || len(payload.Members) != 3 {
		t.Fatalf("queued metadata: %s", raw)
	}
	for _, m := range payload.Members {
		found := false
		for i, id := range members {
			if m.RunID == id && m.Status == []string{"completed", "failed", "aborted"}[i] {
				found = true
			}
		}
		if !found {
			t.Fatalf("unexpected public member: %+v", m)
		}
	}
	var parked, unanswered bool
	if err := w.owner.QueryRow(w.ctx, `SELECT status='paused' AND dude_pause='person', EXISTS(SELECT 1 FROM questions WHERE run_id=r.id AND status='open') FROM runs r WHERE id='run_parent'`).Scan(&parked, &unanswered); err != nil || !parked || !unanswered || w.count("runs") != 4 {
		t.Fatalf("parent was moved: parked=%v unanswered=%v err=%v", parked, unanswered, err)
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
	_, members := w.startCohort(t, "restart", w.in, w.in)
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
		t.Fatalf("restart after close: %v", err)
	}
}
