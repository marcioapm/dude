package delivery_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

func TestEndedPeersAcceptNeitherMessagesNorWaits(t *testing.T) {
	for _, status := range []string{"done", "failed", "aborted"} {
		t.Run(status, func(t *testing.T) {
			w := newEpicWorld(t)
			ctx := context.Background()
			me := w.run(t, "run_me", "ABS-1", "implement", "implementer", "running")
			w.exec(t, `UPDATE runs SET agent_busy_at = now() WHERE id = 'run_me'`)
			q := w.send(t, me, "ABS-2", "Which path?", "")
			w.in(t, func(tx pgx.Tx) error {
				_, err := delivery.SetTaskStatusTx(ctx, tx, w.org, w.project, w.tasks["ABS-2"], "", status, "test")
				return err
			})
			peer := w.peer(t, "ABS-1", "ABS-2")
			err := w.app.InOrg(ctx, w.org, func(tx pgx.Tx) error {
				_, err := delivery.SendMessageTx(ctx, tx, me, delivery.MessageInput{To: peer, Text: "Anyone there?"})
				return err
			})
			var refused delivery.RefusedMessage
			want := "ABS-2 is " + status + ": no Run will read this. Carry on with your assumption, or ask a person."
			if !errors.As(err, &refused) || refused.Msg != want {
				t.Fatalf("send to ended peer: %v", err)
			}
			for _, how := range []string{"wait", "turn_ended"} {
				w.in(t, func(tx pgx.Tx) error {
					got, err := delivery.OpenWaitTx(ctx, tx, me, q.ID, how)
					if err == nil && (got.Message != "" || got.Notice != delivery.PeerEndedLine("ABS-2", status)) {
						t.Errorf("wait on ended peer: %+v", got)
					}
					return err
				})
			}
			w.in(t, func(tx pgx.Tx) error {
				waits, err := delivery.WaitOnUnansweredTx(ctx, tx, me)
				if waits {
					t.Error("peer ended between send and idle, but idle waits")
				}
				return err
			})
			if n := w.count(t, `SELECT count(*) FROM peer_waits`); n != 0 {
				t.Errorf("%d waits opened", n)
			}
			if n := w.count(t, `SELECT count(*) FROM peer_messages`); n != 1 {
				t.Errorf("%d messages, want only the pre-end question", n)
			}
		})
	}
}

func TestReplyAndIdleTakeTheRunBeforeTheMessage(t *testing.T) {
	for _, replyFirst := range []bool{false, true} {
		t.Run(fmt.Sprint("replyFirst=", replyFirst), func(t *testing.T) {
			w := newEpicWorld(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			one := w.run(t, "run_one", "ABS-1", "implement", "implementer", "running")
			w.exec(t, `UPDATE runs SET agent_busy_at = now() WHERE id = 'run_one'`)
			two := w.run(t, "run_two", "ABS-2", "implement", "implementer", "running")
			q := w.send(t, one, "ABS-2", "Which path?", "")
			peer := w.peer(t, "ABS-2", "ABS-1")
			reply := func(tx pgx.Tx) error {
				_, err := delivery.SendMessageTx(ctx, tx, two, delivery.MessageInput{To: peer, Text: "Here.", ReplyTo: q.ID})
				return err
			}
			lockRun := func(tx pgx.Tx) error {
				_, err := tx.Exec(ctx, `SELECT 1 FROM runs WHERE id = $1 FOR UPDATE`, one.RunID)
				return err
			}
			idle := func(tx pgx.Tx) error {
				_, err := delivery.WaitOnUnansweredTx(ctx, tx, one)
				return err
			}
			tx := w.begin(t, ctx)
			first := lockRun
			if replyFirst {
				first = reply
			}
			if err := first(tx); err != nil {
				t.Fatal(err)
			}
			done := w.blockedIn(t, ctx, tx, "second finished before commit", func(other pgx.Tx) error {
				if replyFirst {
					if err := lockRun(other); err != nil {
						return err
					}
					return idle(other)
				}
				return reply(other)
			})
			if !replyFirst {
				if err := idle(tx); err != nil {
					t.Fatal(err)
				}
			}
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if n := w.count(t, `SELECT count(*) FROM peer_waits WHERE closed_at IS NULL`); n != 0 {
				t.Fatalf("%d stranded waits", n)
			}
		})
	}
}

func TestOpeningAWaitTakesTheRunBeforeTheMessage(t *testing.T) {
	w := newEpicWorld(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	one := w.run(t, "run_one", "ABS-1", "implement", "implementer", "running")
	q := w.send(t, one, "ABS-2", "Which path?", "")
	tx := w.begin(t, ctx)
	if _, err := tx.Exec(ctx, `SELECT 1 FROM runs WHERE id = $1 FOR UPDATE`, one.RunID); err != nil {
		t.Fatal(err)
	}
	done := w.blockedIn(t, ctx, tx, "wait finished", func(other pgx.Tx) error {
		_, err := delivery.OpenWaitTx(ctx, other, one, q.ID, "wait")
		return err
	})
	if _, err := tx.Exec(ctx, `SELECT 1 FROM peer_messages WHERE id = $1 FOR NO KEY UPDATE NOWAIT`, q.ID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestRepliesAndWaitsSerializeOnTheAskedMessage(t *testing.T) {
	for _, replyFirst := range []bool{false, true} {
		t.Run(fmt.Sprint("replyFirst=", replyFirst), func(t *testing.T) {
			w := newEpicWorld(t)
			ctx := context.Background()
			one := w.run(t, "run_one", "ABS-1", "implement", "implementer", "running")
			two := w.run(t, "run_two", "ABS-2", "implement", "implementer", "running")
			q := w.send(t, one, "ABS-2", "Which path?", "")
			peer := w.peer(t, "ABS-2", "ABS-1")
			reply := func(tx pgx.Tx) error {
				_, err := delivery.SendMessageTx(ctx, tx, two, delivery.MessageInput{To: peer, Text: "Here.", ReplyTo: q.ID})
				return err
			}
			wait := func(tx pgx.Tx) error {
				_, err := delivery.OpenWaitTx(ctx, tx, one, q.ID, "wait")
				return err
			}
			first, second := wait, reply
			if replyFirst {
				first, second = reply, wait
			}
			tx := w.begin(t, ctx)
			if err := first(tx); err != nil {
				t.Fatal(err)
			}
			w.timesOut(t, ctx, "second transaction did not block on the message", second)
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			w.in(t, second)
			if n := w.count(t, `SELECT count(*) FROM peer_waits WHERE closed_at IS NULL`); n != 0 {
				t.Fatalf("%d stranded waits", n)
			}
			if n := w.count(t, `SELECT count(*) FROM peer_messages WHERE reply_to = $1`, q.ID); n != 1 {
				t.Errorf("%d replies", n)
			}
		})
	}
}

func TestPeerEndingSerializesWithOpeningAWait(t *testing.T) {
	w := newEpicWorld(t)
	ctx := context.Background()
	one := w.run(t, "run_one", "ABS-1", "implement", "implementer", "running")
	q := w.send(t, one, "ABS-2", "Which path?", "")
	tx := w.begin(t, ctx)
	if _, err := delivery.OpenWaitTx(ctx, tx, one, q.ID, "wait"); err != nil {
		t.Fatal(err)
	}
	end := func(other pgx.Tx) error {
		_, err := delivery.SetTaskStatusTx(ctx, other, w.org, w.project, w.tasks["ABS-2"], "", "done", "test")
		return err
	}
	w.timesOut(t, ctx, "ending did not wait", end)
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	w.in(t, end)
	if n := w.count(t, `SELECT count(*) FROM peer_waits WHERE closed_at IS NULL`); n != 0 {
		t.Fatalf("%d stranded waits", n)
	}
}

// A waiter's end holds its Run, then its trigger closes its waits; a peer
// ending must take the waiter's Run before the same waits, or each holds
// what the other needs (40P01).
func TestAPeerEndingWaitsForTheWaitingRunsEnd(t *testing.T) {
	w := newEpicWorld(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	one := w.run(t, "run_one", "ABS-1", "implement", "implementer", "running")
	q := w.send(t, one, "ABS-2", "Which path?", "")
	w.in(t, func(tx pgx.Tx) error { _, err := delivery.OpenWaitTx(ctx, tx, one, q.ID, "wait"); return err })
	tx := w.begin(t, ctx)
	if _, err := tx.Exec(ctx, `SELECT 1 FROM runs WHERE id = $1 FOR UPDATE`, one.RunID); err != nil {
		t.Fatal(err)
	}
	done := w.blockedIn(t, ctx, tx, "ending finished before the Run's end", func(other pgx.Tx) error {
		_, err := delivery.SetTaskStatusTx(ctx, other, w.org, w.project, w.tasks["ABS-2"], "", "done", "test")
		return err
	})
	if _, err := tx.Exec(ctx, `UPDATE runs SET status = 'failed' WHERE id = $1`, one.RunID); err != nil {
		t.Fatalf("run end: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("run end commit: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("peer ending: %v", err)
	}
	if got := w.count(t, `SELECT count(*) FROM peer_waits WHERE run_id = 'run_one' AND closed_by = 'run_ended'`); got != 1 {
		t.Errorf("%d waits closed by run_ended, want the one", got)
	}
	if n := w.count(t, `SELECT count(*) FROM peer_waits WHERE closed_at IS NULL`); n != 0 {
		t.Errorf("%d stranded waits", n)
	}
}

func TestUnreadMailSurvivesAnEndedRunOrFailedSubmit(t *testing.T) {
	for _, scenario := range []string{"unread", "submit failed", "started then failed"} {
		briefed := scenario != "unread"
		t.Run(scenario, func(t *testing.T) {
			w := newEpicWorld(t)
			ctx := context.Background()
			one := w.run(t, "run_one", "ABS-1", "implement", "implementer", "running")
			if !briefed {
				w.run(t, "run_old", "ABS-2", "implement", "implementer", "running")
			}
			q := w.send(t, one, "ABS-2", "Do not lose this.", "")
			brief := func(run string) string {
				var text string
				w.in(t, func(tx pgx.Tx) error {
					var err error
					text, err = delivery.EpicBriefing(ctx, tx, w.org, w.tasks["ABS-2"], run, "implementer")
					return err
				})
				return text
			}
			if briefed {
				w.run(t, "run_old", "ABS-2", "implement", "implementer", "pending")
				if !strings.Contains(brief("run_old"), q.ID) {
					t.Fatal("not briefed")
				}
			}
			if scenario == "started then failed" {
				w.exec(t, `UPDATE runs SET agent_busy_at = now() WHERE id = 'run_old'`)
			}
			w.exec(t, `UPDATE runs SET status = 'failed' WHERE id = 'run_old'`)
			w.run(t, "run_new", "ABS-2", "implement", "implementer", "pending")
			if scenario == "started then failed" {
				if strings.Contains(brief("run_new"), q.ID) {
					t.Error("mail the agent already had was repeated")
				}
				if n := w.count(t, `SELECT count(*) FROM events WHERE run_id = 'run_new' AND event_type = 'agent.message.received'`); n != 0 {
					t.Errorf("%d repeated receipts", n)
				}
				return
			}
			for range 2 {
				if !strings.Contains(brief("run_new"), q.ID) {
					t.Error("new Run lost unread mail")
				}
			}
			if n := w.count(t, `SELECT count(*) FROM events WHERE run_id = 'run_new' AND event_type = 'agent.message.received'
				AND payload->>'message' = $1 AND payload->>'via' = 'briefing'`, q.ID); n != 1 {
				t.Errorf("%d received events on new Run", n)
			}
			w.exec(t, `UPDATE runs SET agent_busy_at = now(), status = 'completed' WHERE id = 'run_new'`)
			w.run(t, "run_later", "ABS-2", "fix", "implementer", "pending")
			if strings.Contains(brief("run_later"), q.ID) {
				t.Error("successfully briefed mail repeated")
			}
		})
	}
}

func TestTheNoteLimitSerializesOnTheEpic(t *testing.T) {
	w := newEpicWorld(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	one := w.run(t, "run_one", "ABS-1", "implement", "implementer", "running")
	pin := func(tx pgx.Tx) error {
		_, err := delivery.PinNoteTx(ctx, tx, w.org, delivery.PinInput{EpicID: w.epic, Text: "fact", Run: one})
		return err
	}
	for range 9 {
		w.in(t, pin)
	}
	tx := w.begin(t, ctx)
	if err := pin(tx); err != nil {
		t.Fatal(err)
	}
	w.timesOut(t, ctx, "pin did not wait for epic lock", pin)
	// With no timeout, tx2 waits on tx1; once tx1 commits it must count ten
	// and refuse. Without the epic lock it waits on UNIQUE (epic_id, n)
	// instead and fails with 23505.
	done := w.blockedIn(t, ctx, tx, "second pin finished before tx1 committed", pin)
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	err := <-done
	var refused delivery.NoteRefusal
	if !errors.As(err, &refused) || refused.Reason != "limit" {
		t.Errorf("after commit: %v", err)
	}
}

func TestAConductorHearsOnlyRepliesItWaitsOn(t *testing.T) {
	w := newEpicWorld(t)
	ctx := context.Background()
	cond := w.run(t, "run_cond", "ABS-2", "", "conductor", "paused")
	w.exec(t, `UPDATE runs SET dude_pause = 'conductor', turn_done_at = now() WHERE id = 'run_cond'`)
	one := w.run(t, "run_one", "ABS-1", "implement", "implementer", "running")
	q := w.send(t, cond, "ABS-1", "First?", "")
	a := w.send(t, one, "ABS-2", "First answer.", q.ID)
	if a.Lands != delivery.LandsNextRun || w.count(t, `SELECT count(*) FROM directives WHERE run_id = 'run_cond'`) != 0 {
		t.Fatal("non-waiting conductor got a reply directive")
	}
	q = w.send(t, cond, "ABS-1", "Second?", "")
	w.in(t, func(tx pgx.Tx) error { _, err := delivery.OpenWaitTx(ctx, tx, cond, q.ID, "wait"); return err })
	a = w.send(t, one, "ABS-2", "Second answer.", q.ID)
	if a.Lands != delivery.LandsNextStep || w.count(t, `SELECT count(*) FROM directives WHERE run_id = 'run_cond'`) != 1 {
		t.Fatal("waiting conductor did not get exactly one directive")
	}
	if n := w.count(t, `SELECT count(*) FROM peer_waits WHERE run_id = 'run_cond' AND closed_by = 'reply'`); n != 1 {
		t.Errorf("%d waits closed", n)
	}
}

func (w *epicWorld) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := w.owner.Exec(context.Background(), sql, args...); err != nil {
		t.Fatal(err)
	}
}

// begin opens an owner transaction, rolled back at the test's end unless
// committed.
func (w *epicWorld) begin(t *testing.T, ctx context.Context) pgx.Tx {
	t.Helper()
	tx, err := w.owner.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	return tx
}

// timesOut runs fn in another transaction, which must hit its 200ms lock
// timeout (55P03) on what tx holds.
func (w *epicWorld) timesOut(t *testing.T, ctx context.Context, why string, fn func(pgx.Tx) error) {
	t.Helper()
	err := w.app.InOrg(ctx, w.org, func(other pgx.Tx) error {
		if _, err := other.Exec(ctx, `SET LOCAL lock_timeout = '200ms'`); err != nil {
			return err
		}
		return fn(other)
	})
	var blocked *pgconn.PgError
	if !errors.As(err, &blocked) || blocked.Code != "55P03" {
		t.Fatalf("%s: %v", why, err)
	}
}

// blockedIn runs fn in another transaction and returns once its backend
// waits on a row lock; why fails the test if fn finishes first. The
// channel has fn's result.
func (w *epicWorld) blockedIn(t *testing.T, ctx context.Context, tx pgx.Tx, why string, fn func(pgx.Tx) error) <-chan error {
	t.Helper()
	pid := make(chan int, 1)
	done := make(chan error, 1)
	go func() {
		done <- w.app.InOrg(ctx, w.org, func(other pgx.Tx) error {
			var id int
			if err := other.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&id); err != nil {
				return err
			}
			pid <- id
			return fn(other)
		})
	}()
	var otherPID int
	select {
	case otherPID = <-pid:
	case err := <-done:
		t.Fatalf("%s: %v", why, err)
	}
	for {
		var blocked bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_locks WHERE pid = $1 AND NOT granted
			AND locktype IN ('tuple','transactionid'))`, otherPID).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			return done
		}
		select {
		case err := <-done:
			t.Fatalf("%s: %v", why, err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		default:
		}
	}
}

func (w *epicWorld) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := w.owner.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// run makes an agent Run of a task, of phase ("" for a conductor).
func (w *epicWorld) run(t *testing.T, id, key, phase, role, status string) delivery.RunRef {
	t.Helper()
	w.exec(t, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, kind)
		VALUES ($1, $2, $3, $4, 1, $5::run_status, NULLIF($6, '')::run_phase, $7::agent_role, 'agent')`,
		id, w.org, w.project, w.tasks[key], status, phase, role)
	return delivery.RunRef{Org: w.org, ProjectID: w.project, TaskID: w.tasks[key], RunID: id}
}

func (w *epicWorld) peer(t *testing.T, of, key string) delivery.Peer {
	t.Helper()
	var p delivery.Peer
	w.in(t, func(tx pgx.Tx) error {
		var ok bool
		var err error
		p, ok, err = delivery.PeerByKey(context.Background(), tx, w.tasks[of], key)
		if !ok && err == nil {
			err = fmt.Errorf("%s is not %s's peer", key, of)
		}
		return err
	})
	return p
}

func (w *epicWorld) send(t *testing.T, from delivery.RunRef, to, text, replyTo string) delivery.Sent {
	t.Helper()
	var out delivery.Sent
	of := ""
	for k, id := range w.tasks {
		if id == from.TaskID {
			of = k
		}
	}
	peer := w.peer(t, of, to)
	w.in(t, func(tx pgx.Tx) error {
		var err error
		out, err = delivery.SendMessageTx(context.Background(), tx, from, delivery.MessageInput{To: peer, Text: text, ReplyTo: replyTo})
		return err
	})
	return out
}

// A message's text is bounded as a steer's: at SteerTextMax it is sent;
// a byte past, refused, saying so.
func TestAMessageIsBoundedAsASteer(t *testing.T) {
	w := newEpicWorld(t)
	me := w.run(t, "run_me", "ABS-1", "implement", "implementer", "running")
	peer := w.peer(t, "ABS-1", "ABS-2")
	ctx := context.Background()
	w.in(t, func(tx pgx.Tx) error {
		_, err := delivery.SendMessageTx(ctx, tx, me, delivery.MessageInput{To: peer, Text: strings.Repeat("x", delivery.SteerTextMax)})
		return err
	})
	err := w.app.InOrg(ctx, w.org, func(tx pgx.Tx) error {
		_, err := delivery.SendMessageTx(ctx, tx, me, delivery.MessageInput{To: peer, Text: strings.Repeat("x", delivery.SteerTextMax+1)})
		return err
	})
	var refused delivery.RefusedMessage
	if !errors.As(err, &refused) || refused.Reason != "too_long" || !strings.Contains(refused.Msg, "say less, or pin a note") {
		t.Errorf("past the bound: %v", err)
	}
}

// A message to a task with no Run going is in its next implementer's
// prompt, once, marked briefed to it and received there; a message
// waiting for a live Run is not.
func TestAHeldMessageIsInTheTasksNextBriefing(t *testing.T) {
	w := newEpicWorld(t)
	me := w.run(t, "run_me", "ABS-1", "implement", "implementer", "running")
	held := w.send(t, me, "ABS-2", "Which path does your estimate histogram use?", "")
	if held.Lands != delivery.LandsNextRun {
		t.Fatalf("lands %s", held.Lands)
	}
	if n := w.count(t, `SELECT count(*) FROM events WHERE event_type = 'agent.message.sent' AND payload->>'message' = $1
		AND payload->>'notice' = 'ABS-2 has no Run going; it will see this when its next Run starts.'`, held.ID); n != 1 {
		t.Errorf("%d held notices", n)
	}
	ctx := context.Background()
	brief := func(runID, key, role string) string {
		var s string
		w.in(t, func(tx pgx.Tx) error {
			var err error
			s, err = delivery.EpicBriefing(ctx, tx, w.org, w.tasks[key], runID, role)
			return err
		})
		return s
	}
	// A reviewer of the task reads its peers, not its mail.
	w.run(t, "run_two_rev", "ABS-2", "review", "reviewer", "running")
	if got := brief("run_two_rev", "ABS-2", "reviewer"); strings.Contains(got, held.ID) {
		t.Errorf("a reviewer was briefed with the task's messages")
	}
	w.run(t, "run_two", "ABS-2", "implement", "implementer", "pending")
	got := brief("run_two", "ABS-2", "implementer")
	for _, want := range []string{"## On this epic: Preview", "- ABS-1 · Task 1", "- ABS-3 · Task 3",
		"### Messages from your peers, sent while no Run of this task was going", "Message " + held.ID + " from ABS-1's implementer",
		"Which path does your estimate histogram use?", "`dude message KEY \"…\" --reply-to MSG`"} {
		if !strings.Contains(got, want) {
			t.Errorf("the briefing lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "ABS-4") || strings.Contains(got, "ABS-5") {
		t.Errorf("a non-peer in the briefing:\n%s", got)
	}
	if n := w.count(t, `SELECT count(*) FROM peer_messages WHERE id = $1 AND briefed_run_id = 'run_two'`, held.ID); n != 1 {
		t.Errorf("not marked briefed")
	}
	if n := w.count(t, `SELECT count(*) FROM events WHERE run_id = 'run_two' AND event_type = 'agent.message.received'
		AND payload->>'via' = 'briefing'`); n != 1 {
		t.Errorf("%d received events", n)
	}
	// The same prompt built again (a retried submit) carries it, recorded once.
	if again := brief("run_two", "ABS-2", "implementer"); !strings.Contains(again, held.ID) {
		t.Errorf("a retried submit lost the message")
	}
	if n := w.count(t, `SELECT count(*) FROM events WHERE event_type = 'agent.message.received'`); n != 1 {
		t.Errorf("%d received events after a retry", n)
	}
	// Another Run later is not told it again.
	w.run(t, "run_two_fix", "ABS-2", "fix", "implementer", "pending")
	if got := brief("run_two_fix", "ABS-2", "fixer"); strings.Contains(got, held.ID) {
		t.Errorf("a message briefed twice")
	}
}

// Every new Run on the epic, reviewers, the simplifier and QA included, has
// the current notes in its briefing, numbered with who pinned them;
// replaced and retired ones are gone. Reviewers are told to check the code
// against them, and not that they may message.
func TestNewRunsReviewersIncludedGetTheCurrentNotes(t *testing.T) {
	w := newEpicWorld(t)
	me := w.run(t, "run_me", "ABS-1", "implement", "implementer", "running")
	ctx := context.Background()
	w.exec(t, `INSERT INTO people (id, organization_id, name) VALUES ('per_m', $1, 'Márcio Martins')`, w.org)
	pin := func(in delivery.PinInput) delivery.Pinned {
		var p delivery.Pinned
		w.in(t, func(tx pgx.Tx) error {
			var err error
			p, err = delivery.PinNoteTx(ctx, tx, w.org, in)
			return err
		})
		return p
	}
	pin(delivery.PinInput{EpicID: w.epic, Text: "ABS-1 serves the engine endpoint.", Person: "per_m", Session: ""})
	pin(delivery.PinInput{EpicID: w.epic, Text: "Read .ai/INDEX.md first.", Run: me})
	pin(delivery.PinInput{EpicID: w.epic, Text: "Columns: _mean, _var", Run: me})
	pin(delivery.PinInput{EpicID: w.epic, Text: "Columns: _mean, _variance", Run: me, Replaces: 3})
	w.in(t, func(tx pgx.Tx) error {
		_, err := delivery.RetireNoteTx(ctx, tx, w.org, w.epic, 2, "per_m", delivery.Writer{ActorType: "human", ActorID: "per_m"})
		return err
	})
	for _, role := range []string{"reviewer", "simplifier", "qa_browser", "implementer"} {
		id := "run_new_" + role
		w.run(t, id, "ABS-3", map[string]string{"reviewer": "review", "simplifier": "simplify", "qa_browser": "test",
			"implementer": "implement"}[role], role, "pending")
		var got string
		w.in(t, func(tx pgx.Tx) error {
			var err error
			got, err = delivery.EpicBriefing(ctx, tx, w.org, w.tasks["ABS-3"], id, role)
			return err
		})
		for _, want := range []string{"### Epic notes (2 of 10)", "1. ABS-1 serves the engine endpoint.\n   — Márcio Martins",
			"4. Columns: _mean, _variance\n   — ABS-1's implementer"} {
			if !strings.Contains(got, want) {
				t.Errorf("the %s's briefing lacks %q:\n%s", role, want, got)
			}
		}
		if strings.Contains(got, "_var\n") || strings.Contains(got, ".ai/INDEX.md") {
			t.Errorf("the %s's briefing has a note no longer in force:\n%s", role, got)
		}
		reads := role != "implementer"
		if reads != strings.Contains(got, "Check the code against the epic notes below") || reads == strings.Contains(got, "`dude message KEY`") {
			t.Errorf("the %s is told the wrong peer tools:\n%s", role, got)
		}
		if n := w.count(t, `SELECT count(*) FROM epic_note_reads WHERE run_id = $1 AND change = 'briefing' AND read_at IS NOT NULL`, id); n != 2 {
			t.Errorf("the %s's briefing recorded %d notes told", role, n)
		}
	}
	// A task on no epic has no section.
	var none string
	w.in(t, func(tx pgx.Tx) error {
		var err error
		none, err = delivery.EpicBriefing(ctx, tx, w.org, w.tasks["ABS-4"], "", "implementer")
		return err
	})
	if none != "" {
		t.Errorf("a task on no epic: %q", none)
	}
}

// People pin and retire through delivery as the API calls it; a retired
// note is told to live agents; it cannot be retired twice or replaced.
func TestAPersonRetiresANoteAndAgentsAreTold(t *testing.T) {
	w := newEpicWorld(t)
	w.run(t, "run_two", "ABS-2", "implement", "implementer", "running")
	w.exec(t, `INSERT INTO people (id, organization_id, name) VALUES ('per_m', $1, 'Márcio Martins')`, w.org)
	ctx := context.Background()
	w.in(t, func(tx pgx.Tx) error {
		_, err := delivery.PinNoteTx(ctx, tx, w.org, delivery.PinInput{EpicID: w.epic, Text: "Read the docs.", Person: "per_m"})
		return err
	})
	w.in(t, func(tx pgx.Tx) error {
		_, err := delivery.RetireNoteTx(ctx, tx, w.org, w.epic, 1, "per_m", delivery.Writer{ActorType: "human", ActorID: "per_m"})
		return err
	})
	var text string
	if err := w.owner.QueryRow(ctx, `SELECT text FROM directives WHERE run_id = 'run_two' ORDER BY created_at DESC LIMIT 1`).Scan(&text); err != nil {
		t.Fatal(err)
	}
	if text != "Epic note retired: 1 · Márcio Martins · no longer in force: Read the docs." {
		t.Errorf("told %q", text)
	}
	if n := w.count(t, `SELECT count(*) FROM events WHERE event_type = 'epic.note.retired' AND payload->>'by' = 'Márcio Martins'`); n != 1 {
		t.Errorf("%d retired events", n)
	}
	for _, try := range []func(tx pgx.Tx) error{
		func(tx pgx.Tx) error {
			_, err := delivery.RetireNoteTx(ctx, tx, w.org, w.epic, 1, "per_m", delivery.Writer{ActorType: "human"})
			return err
		},
		func(tx pgx.Tx) error {
			_, err := delivery.PinNoteTx(ctx, tx, w.org, delivery.PinInput{EpicID: w.epic, Text: "x", Person: "per_m", Replaces: 1})
			return err
		},
	} {
		var refused delivery.NoteRefusal
		if err := w.app.InOrg(ctx, w.org, try); !errors.As(err, &refused) || refused.Reason != "retired" {
			t.Errorf("acting on a retired note: %v", err)
		}
	}
}

// A turn that ends with the Run's question to a peer unanswered waits on
// it; a reply to its own question (its assumption) closes it, and the turn
// may end; a later question waits again.
func TestATurnEndingOnAnUnansweredQuestionWaitsUntilASelfReply(t *testing.T) {
	w := newEpicWorld(t)
	me := w.run(t, "run_me", "ABS-1", "implement", "implementer", "running")
	w.exec(t, `UPDATE runs SET agent_busy_at = now() WHERE id = 'run_me'`)
	ctx := context.Background()
	ends := func() bool {
		var waits bool
		w.in(t, func(tx pgx.Tx) error {
			var err error
			waits, err = delivery.WaitOnUnansweredTx(ctx, tx, me)
			return err
		})
		return waits
	}
	if ends() {
		t.Fatal("a Run that asked nothing waits")
	}
	q := w.send(t, me, "ABS-2", "Which path?", "")
	if !ends() {
		t.Fatal("the turn ended with its question unanswered and does not wait")
	}
	if n := w.count(t, `SELECT count(*) FROM peer_waits WHERE run_id = 'run_me' AND how = 'turn_ended' AND closed_at IS NULL
		AND message_id = $1`, q.ID); n != 1 {
		t.Errorf("no turn_ended wait")
	}
	self := w.send(t, me, "ABS-2", "Not waiting on this: going ahead assuming /v1/query/estimate.", q.ID)
	if self.Closed != q.ID {
		t.Errorf("the self-reply closed %q", self.Closed)
	}
	if n := w.count(t, `SELECT count(*) FROM peer_waits WHERE run_id = 'run_me' AND closed_by = 'self'`); n != 1 {
		t.Errorf("the wait was not closed by the self-reply")
	}
	if ends() {
		t.Error("a question closed by a self-reply still holds the turn")
	}
	// The self-reply goes to the peer's thread too.
	if n := w.count(t, `SELECT count(*) FROM peer_messages WHERE reply_to = $1 AND to_task_id = $2`, q.ID, w.tasks["ABS-2"]); n != 1 {
		t.Errorf("the self-reply is not in the thread")
	}
	w.send(t, me, "ABS-3", "And your sample?", "")
	if !ends() {
		t.Error("a new question does not hold the turn")
	}
}

// Peer traffic wakes no conductor: a message to its task, a note change,
// a reply to the conductor — no conductor_wakes row, no directive on it.
// Its next briefing has "Since you last looked", bounded, and the one
// after does not repeat it.
func TestTheConductorIsNeverWokenAndItsDigestIsBoundedAndNotRepeated(t *testing.T) {
	w := newEpicWorld(t)
	ctx := context.Background()
	cond := w.run(t, "run_cond", "ABS-2", "", "conductor", "paused")
	w.exec(t, `UPDATE runs SET dude_pause = 'conductor' WHERE id = 'run_cond'`)
	one := w.run(t, "run_one", "ABS-1", "implement", "implementer", "running")
	three := w.run(t, "run_three", "ABS-3", "implement", "implementer", "running")
	for i := range 12 {
		w.send(t, one, "ABS-2", fmt.Sprintf("question %d %s", i, strings.Repeat("long ", 100)), "")
		// Sent long ago, as far as the hour's limit goes; after the
		// conductor started, as far as its digest goes.
		w.exec(t, `UPDATE peer_messages SET created_at = now() - interval '2 hours' WHERE text LIKE $1`, fmt.Sprintf("question %d %%", i))
	}
	w.exec(t, `UPDATE peer_messages SET created_at = created_at + make_interval(mins => n::int)
		FROM (SELECT id, row_number() OVER (ORDER BY id) AS n FROM peer_messages) o WHERE o.id = peer_messages.id`)
	w.exec(t, `UPDATE runs SET created_at = now() - interval '3 hours' WHERE id = 'run_cond'`)
	w.send(t, three, "ABS-1", "not about ABS-2", "")
	w.in(t, func(tx pgx.Tx) error {
		_, err := delivery.PinNoteTx(ctx, tx, w.org, delivery.PinInput{EpicID: w.epic, Text: "Body is {metric} " + strings.Repeat("long ", 300), Run: one})
		return err
	})
	w.exec(t, `UPDATE tasks SET status = 'review' WHERE id = $1`, w.tasks["ABS-1"])
	w.in(t, func(tx pgx.Tx) error {
		return delivery.RecordStatusTx(ctx, tx, w.org, w.project, w.tasks["ABS-1"], "review", "test")
	})
	if n := w.count(t, `SELECT count(*) FROM conductor_wakes`); n != 0 {
		t.Errorf("%d conductor wakes", n)
	}
	if n := w.count(t, `SELECT count(*) FROM directives WHERE run_id = 'run_cond'`); n != 0 {
		t.Errorf("a directive reached the parked conductor")
	}
	_ = cond
	var digest string
	w.in(t, func(tx pgx.Tx) error {
		var err error
		digest, err = delivery.ConductorDigestTx(ctx, tx, w.org, w.tasks["ABS-2"], "", "")
		return err
	})
	for _, want := range []string{"## Since you last looked, on this epic", "Messages to or from ABS-2:", "question 11", "question 2 ",
		"- … and 2 more: `dude task list --peers`", "Epic notes added, replaced or retired:", "note 1, ABS-1's implementer: Body is {metric}",
		"Peers that changed:", "- ABS-1 is review since"} {
		if !strings.Contains(digest, want) {
			t.Errorf("the digest lacks %q:\n%s", want, digest)
		}
	}
	if strings.Contains(digest, "question 1 ") || strings.Contains(digest, "not about ABS-2") || strings.Count(digest, "\n- ") != 10+1+1+1 {
		t.Errorf("the digest is not bounded to the newest 10 of ABS-2's own:\n%s", digest)
	}
	if strings.Contains(digest, strings.Repeat("long ", 60)) {
		t.Errorf("a message or note is not clipped:\n%s", digest)
	}
	// Told: the next says nothing of it.
	w.in(t, func(tx pgx.Tx) error {
		var err error
		digest, err = delivery.ConductorDigestTx(ctx, tx, w.org, w.tasks["ABS-2"], "", "")
		return err
	})
	if digest != "" {
		t.Errorf("the second digest repeats:\n%s", digest)
	}
	// Something new is told, and only it.
	w.send(t, one, "ABS-2", "one more thing", "")
	w.in(t, func(tx pgx.Tx) error {
		var err error
		digest, err = delivery.ConductorDigestTx(ctx, tx, w.org, w.tasks["ABS-2"], "", "")
		return err
	})
	if !strings.Contains(digest, "one more thing") || strings.Contains(digest, "question") || strings.Contains(digest, "Body is") {
		t.Errorf("the third digest:\n%s", digest)
	}
	// The briefing of a conductor carries it.
	w.send(t, one, "ABS-2", "for the briefing", "")
	var brief string
	w.in(t, func(tx pgx.Tx) error {
		var err error
		brief, err = delivery.Briefing(ctx, tx, w.tasks["ABS-2"], "run_cond2", "Márcio", "what's up?")
		return err
	})
	if !strings.Contains(brief, "## Since you last looked, on this epic") || !strings.Contains(brief, "for the briefing") ||
		!strings.Contains(brief, "## On this epic: Preview") {
		t.Errorf("the briefing lacks the digest:\n%s", brief)
	}
}

// A conductor woken for its own reason hears, with the wake note, what its
// epic's peers did since it last looked; peer traffic alone woke nothing.
func TestAWakeNoteCarriesTheDigest(t *testing.T) {
	w := newEpicWorld(t)
	ctx := context.Background()
	w.run(t, "run_cond", "ABS-2", "", "conductor", "running")
	w.exec(t, `UPDATE runs SET turn_done_at = now(), created_at = now() - interval '1 hour' WHERE id = 'run_cond'`)
	one := w.run(t, "run_one", "ABS-1", "implement", "implementer", "running")
	w.send(t, one, "ABS-2", "Which body?", "")
	var woke string
	w.in(t, func(tx pgx.Tx) error {
		if n := w.count(t, `SELECT count(*) FROM conductor_wakes`); n != 0 {
			t.Errorf("%d wakes from a message", n)
		}
		if _, err := delivery.RecordWakeTx(ctx, tx, w.org, w.tasks["ABS-2"], "decision", "d:1", "A decision waits."); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE conductor_wakes SET created_at = now() - interval '1 hour'`); err != nil {
			return err
		}
		var err error
		woke, err = delivery.WakeConductorTx(ctx, tx, w.org, w.tasks["ABS-2"], 1)
		return err
	})
	if woke != "run_cond" {
		t.Fatalf("woke %q", woke)
	}
	var text string
	if err := w.owner.QueryRow(ctx, `SELECT text FROM directives WHERE run_id = 'run_cond'`).Scan(&text); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(text, "dude woke you; one thing:\n- A decision waits.") ||
		!strings.Contains(text, "## Since you last looked, on this epic") || !strings.Contains(text, "Which body?") {
		t.Errorf("the wake note:\n%s", text)
	}
}

// A message to a task whose only live Run is its conductor is held: it is
// never queued on the conductor, which it would wake.
func TestAMessageNeverReachesAPeersConductor(t *testing.T) {
	w := newEpicWorld(t)
	one := w.run(t, "run_one", "ABS-1", "implement", "implementer", "running")
	w.run(t, "run_cond", "ABS-2", "", "conductor", "running")
	sent := w.send(t, one, "ABS-2", "hello", "")
	if sent.Lands != delivery.LandsNextRun || w.count(t, `SELECT count(*) FROM directives`) != 0 {
		t.Errorf("a message reached a conductor: %+v", sent)
	}
}

// A task that ends with a peer waiting on its reply releases the peer: its
// wait closes by peer_ended, with the event, and it is told, as a
// directive on its Run, that no reply is coming. A wait on another task
// stays open, and a status that is not an end releases nobody.
func TestATaskEndingWithoutReplyingReleasesItsWaiters(t *testing.T) {
	w := newEpicWorld(t)
	ctx := context.Background()
	one := w.run(t, "run_one", "ABS-1", "implement", "implementer", "running")
	three := w.run(t, "run_three", "ABS-3", "implement", "implementer", "running")
	toTwo := w.send(t, one, "ABS-2", "Which body?", "")
	toOne := w.send(t, three, "ABS-1", "Which path?", "")
	w.in(t, func(tx pgx.Tx) error {
		if _, err := delivery.OpenWaitTx(ctx, tx, one, toTwo.ID, "wait"); err != nil {
			return err
		}
		_, err := delivery.OpenWaitTx(ctx, tx, three, toOne.ID, "wait")
		return err
	})
	setStatus := func(status string) {
		w.in(t, func(tx pgx.Tx) error {
			_, err := delivery.SetTaskStatusTx(ctx, tx, w.org, w.project, w.tasks["ABS-2"], "", status, "test")
			return err
		})
	}
	setStatus("review")
	if n := w.count(t, `SELECT count(*) FROM peer_waits WHERE closed_at IS NULL`); n != 2 {
		t.Fatalf("a status that is no end closed waits: %d open", n)
	}
	setStatus("aborted")
	if n := w.count(t, `SELECT count(*) FROM peer_waits WHERE run_id = 'run_one' AND closed_by = 'peer_ended' AND closed_at IS NOT NULL`); n != 1 {
		t.Errorf("the wait on the ended task is not closed by peer_ended")
	}
	if n := w.count(t, `SELECT count(*) FROM peer_waits WHERE run_id = 'run_three' AND closed_at IS NULL`); n != 1 {
		t.Errorf("a wait on another task was closed")
	}
	if n := w.count(t, `SELECT count(*) FROM events WHERE run_id = 'run_one' AND event_type = 'agent.peer.wait_ended'
		AND payload->>'by' = 'peer_ended' AND payload->>'status' = 'aborted' AND payload->>'on' = 'ABS-2'
		AND payload->>'line' = 'ABS-2 ended without replying (status: aborted). Carry on with your assumption, or ask a person.'`); n != 1 {
		t.Errorf("%d peer_ended events", n)
	}
	var text string
	if err := w.owner.QueryRow(ctx, `SELECT text FROM directives WHERE run_id = 'run_one' AND text LIKE 'ABS-2 %'`).Scan(&text); err != nil {
		t.Fatal(err)
	}
	if text != "ABS-2 ended without replying (status: aborted). Carry on with your assumption, or ask a person." {
		t.Errorf("the sender is told %q", text)
	}
	if n := w.count(t, `SELECT count(*) FROM directives WHERE run_id = 'run_three' AND text LIKE '%ended without replying%'`); n != 0 {
		t.Errorf("a Run waiting on another task was told")
	}
}
