package chat_test

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/chat"
	"github.com/marciomartins/dude/orchestrator/internal/fakeslack"
)

// A provider refusal before anything is decided is not retried, and not
// dropped: Requests says failed and why, the asker is told privately, and
// the 👀 becomes ❌.
func TestChatADeterministicFailureBeforeMakeIsAnsweredAndRecorded(t *testing.T) {
	for _, c := range []struct {
		name, conv, method, code string
	}{
		{"conversation info refused", "C1", "conversations.info", "missing_scope"},
		{"members refused", "G1", "conversations.members", "channel_not_found"},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := setup(t)
			w.fake.FailNext(c.method, c.code)
			ts, ev := w.receive("Eli", c.conv, "", "file it")
			w.once()
			if e := w.event(ev); !e.Processed || e.Attempts != 1 || !strings.Contains(e.LastError, c.code) {
				t.Fatalf("delivery %+v", e)
			}
			got := w.requests()
			if len(got) != 1 || got[0].Route != "failed" || got[0].Person != w.people["Eli"][0] || !strings.Contains(got[0].Reasoning, c.code) {
				t.Fatalf("requests %+v", got)
			}
			if eph := w.fake.Ephemerals(); len(eph) != 1 || eph[0].User != "U2" || !strings.Contains(eph[0].Text, "couldn't act on this") {
				t.Fatalf("ephemerals %+v", eph)
			}
			if r := w.fake.Reactions(c.conv, ts); strings.Join(r, ",") != "x" {
				t.Fatalf("reactions %v", r)
			}
			if w.model.calls() != 0 || len(w.fake.BotMessages()) != 0 {
				t.Fatal("a refused delivery asked the model or replied in public")
			}
		})
	}
}

// A request made whose reply never goes out (the provider down past the
// last attempt) is recorded as failed, keeping what it made, and the
// asker gets the link privately with the mention marked ❌.
func TestChatGivingUpAfterTheRequestWasMadeTellsThePerson(t *testing.T) {
	w := setup(t)
	w.model.queue(answer(map[string]any{"route": "task", "confidence": 0.9, "project": "BIL", "title": "Stream it", "goal": "g"}))
	w.fake.FailTimes("chat.postMessage", "internal_error", 100)
	ts, ev := w.receive("Eli", "C1", "", "file: stream it")
	for range 7 {
		w.due()
		w.once()
	}
	if e := w.event(ev); !e.Processed {
		t.Fatalf("not given up: %+v", e)
	}
	got := w.requests()
	if len(got) != 1 || got[0].Route != "failed" || got[0].AskedRoute != "task" || got[0].ResultKind != "task" ||
		!strings.Contains(got[0].Reasoning, "its reply was not posted") {
		t.Fatalf("requests %+v", got)
	}
	eph := w.fake.Ephemerals()
	if len(eph) != 1 || eph[0].User != "U2" || !strings.Contains(eph[0].Text, "Filed BIL-1 Stream it") ||
		!strings.Contains(string(eph[0].Blocks), "#/o/acme/task/"+got[0].ResultID) {
		t.Fatalf("ephemerals %+v", eph)
	}
	if r := w.fake.Reactions("C1", ts); strings.Join(r, ",") != "x" {
		t.Fatalf("reactions %v", r)
	}
	if n := w.count(`SELECT count(*) FROM tasks WHERE organization_id = $1`, w.org); n != 1 {
		t.Fatalf("%d tasks", n)
	}
}

// A reply the provider refuses outright is not retried: the asker gets the
// link privately, the mention ❌, and Requests says failed, with the task kept.
func TestChatARefusedReplyIsSentPrivatelyAndRecordedAsFailed(t *testing.T) {
	w := setup(t)
	w.model.queue(answer(map[string]any{"route": "task", "confidence": 0.9, "project": "BIL", "title": "Stream it", "goal": "g"}))
	w.fake.FailNext("chat.postMessage", "msg_too_long")
	ts, ev := w.receive("Eli", "C1", "", "file: stream it")
	w.once()
	if e := w.event(ev); !e.Processed || e.Attempts != 0 {
		t.Fatalf("delivery %+v", e)
	}
	got := w.requests()
	if len(got) != 1 || got[0].Route != "failed" || got[0].AskedRoute != "task" || got[0].ResultKind != "task" ||
		!strings.Contains(got[0].Reasoning, "msg_too_long") {
		t.Fatalf("requests %+v", got)
	}
	if eph := w.fake.Ephemerals(); len(eph) != 1 || !strings.Contains(eph[0].Text, "Filed BIL-1 Stream it") {
		t.Fatalf("ephemerals %+v", eph)
	}
	if r := w.fake.Reactions("C1", ts); strings.Join(r, ",") != "x" {
		t.Fatalf("reactions %v", r)
	}
}

// A mark is cosmetic: when every reaction call fails, the reply posted
// stands, the request stays what it made, and nobody is sent a note.
func TestChatAFailedMarkLeavesAPostedReplySuccessful(t *testing.T) {
	w := setup(t)
	w.model.queue(answer(map[string]any{"route": "task", "confidence": 0.9, "project": "BIL", "title": "Stream it", "goal": "g"}))
	w.fake.FailTimes("reactions.add", "internal_error", 100)
	_, ev := w.receive("Eli", "C1", "", "file: stream it")
	for range chat.MaxAttempts + 2 {
		w.due()
		w.once()
	}
	if e := w.event(ev); !e.Processed || e.Attempts != 0 {
		t.Fatalf("delivery %+v", e)
	}
	got := w.requests()
	if len(got) != 1 || got[0].Route != "task" || got[0].AskedRoute != "" || strings.Contains(got[0].Reasoning, "not posted") {
		t.Fatalf("requests %+v", got)
	}
	if n := w.count(`SELECT count(*) FROM chat_requests WHERE organization_id = $1 AND replied_at IS NOT NULL`, w.org); n != 1 {
		t.Fatalf("%d requests replied", n)
	}
	if bot := w.fake.BotMessages(); len(bot) != 1 || !strings.HasPrefix(bot[0].Text, "Filed BIL-1") {
		t.Fatalf("replies %+v", bot)
	}
	if eph := w.fake.Ephemerals(); len(eph) != 0 {
		t.Fatalf("ephemerals %+v", eph)
	}
}

// A refusal recorded on one attempt is final: an attempt after the worker
// died (the reply refused and marked so, the mark and the request's
// outcome not written yet) reads it, marks ❌, records failed, and neither
// posts nor tells the asker again.
func TestChatARetryRemembersAnEarlierRefusal(t *testing.T) {
	w := setup(t)
	w.model.queue(answer(map[string]any{"route": "task", "confidence": 0.9, "project": "BIL", "title": "Stream it", "goal": "g"}))
	w.fake.FailNext("chat.postMessage", "msg_too_long")
	ts, ev := w.receive("Eli", "C1", "", "file: stream it")
	w.once()
	w.exec(`UPDATE chat_requests SET route = 'task', asked_route = NULL, reasoning = '', replied_at = NULL,
			effects = jsonb_set(effects, '{1}', $3::jsonb)
		WHERE organization_id = $1 AND event_id = $2`, w.org, ev, `{"kind":"react","reaction":`+fmt.Sprint(int(chat.ReactionDone))+`}`)
	w.exec(`UPDATE chat_events SET processed_at = NULL, claimed_until = NULL WHERE organization_id = $1 AND event_id = $2`, w.org, ev)
	posts := w.fake.Calls("chat.postMessage")
	w.once()
	got := w.requests()
	if len(got) != 1 || got[0].Route != "failed" || got[0].AskedRoute != "task" || !strings.Contains(got[0].Reasoning, "msg_too_long") {
		t.Fatalf("requests %+v", got)
	}
	if r := w.fake.Reactions("C1", ts); strings.Join(r, ",") != "x" {
		t.Fatalf("reactions %v", r)
	}
	if eph := w.fake.Ephemerals(); len(eph) != 1 || w.fake.Calls("chat.postMessage") != posts {
		t.Fatalf("ephemerals %+v, %d posts", eph, w.fake.Calls("chat.postMessage")-posts)
	}
}

// A thread whose task went between reading the binding and linking it is
// routed as unbound: the model is asked and a session made, not a failure.
// The binding cascades with its task, so the race is built by deleting the
// task with foreign-key triggers off, leaving the binding behind.
func TestChatABoundTaskGoneBeforeTheLinkRoutesAfresh(t *testing.T) {
	w := setup(t)
	w.model.queue(answer(map[string]any{"route": "task", "confidence": 0.9, "project": "BIL", "title": "Stream it", "goal": "g"}))
	root := w.mention("Eli", "C1", "", "file: stream it")
	task := w.requests()[0].ResultID
	w.exec(`SET session_replication_role = replica`)
	w.exec(`DELETE FROM tasks WHERE id = $1`, task)
	w.exec(`RESET session_replication_role`)
	if n := w.count(`SELECT count(*) FROM chat_threads WHERE organization_id = $1 AND task_id = $2`, w.org, task); n != 1 {
		t.Fatalf("%d bindings left", n)
	}
	w.model.queue(answer(map[string]any{"route": "brainstorm", "confidence": 0.9, "title": "Status"}))
	ts := w.mention("Eli", "C1", root, "what's the status?")
	got := w.requests()
	if len(got) != 2 || got[1].Route != "brainstorm" || got[1].ResultKind != "session" || w.model.calls() != 2 {
		t.Fatalf("requests %+v, %d model calls", got, w.model.calls())
	}
	if r := w.fake.Reactions("C1", ts); strings.Join(r, ",") != "eyes" || len(w.fake.Ephemerals()) != 0 {
		t.Fatalf("reactions %v, ephemerals %+v", r, w.fake.Ephemerals())
	}
}

// Giving up on a delivery whose request another attempt already recorded
// and replied to tells nobody anything: no note, no ❌.
func TestChatGivingUpAfterAnotherAttemptRepliedSaysNothing(t *testing.T) {
	w := setup(t)
	w.model.queue(answer(map[string]any{"route": "task", "confidence": 0.9, "project": "BIL", "title": "Stream it", "goal": "g"}))
	ts, ev := w.receive("Eli", "C1", "", "file: stream it")
	w.svc.SetBeforeMake(func(ctx context.Context) error {
		if _, err := w.owner.Exec(ctx, `INSERT INTO chat_requests (id, organization_id, provider, event_id, external_workspace_id,
				conversation_id, external_user_id, route, replied_at) VALUES ('chr_other', $1, 'slack', $2, 'T1', 'C1', 'U2', 'task', now())`,
			w.org, ev); err != nil {
			return err
		}
		return errors.New("refused")
	})
	w.once()
	if e := w.event(ev); !e.Processed {
		t.Fatalf("delivery %+v", e)
	}
	if got := w.requests(); len(got) != 1 || got[0].Route != "task" {
		t.Fatalf("requests %+v", got)
	}
	if eph := w.fake.Ephemerals(); len(eph) != 0 {
		t.Fatalf("ephemerals %+v", eph)
	}
	if r := w.fake.Reactions("C1", ts); strings.Join(r, ",") != "eyes" {
		t.Fatalf("reactions %v", r)
	}
}

// Giving up on a click marks the mention that was asked, not the click,
// and forwards the link it made privately.
func TestChatGivingUpOnAClickMarksTheAskedMention(t *testing.T) {
	w := setup(t)
	w.model.queue(answer(map[string]any{"route": "task", "confidence": 0.6, "project": "BIL", "title": "Fix the export", "goal": "Fix it."}))
	root := w.mention("Eli", "C1", "", "the export is broken")
	q := lastBot(t, w.fake)
	w.fake.FailTimes("chat.update", "internal_error", 100)
	w.click("Eli", "C1", q.TS, root, buttonValue(t, q, 0))
	for range chat.MaxAttempts {
		w.due()
		w.once()
	}
	got := w.requests()
	if len(got) != 2 || got[1].Route != "failed" || got[1].ResultKind != "task" {
		t.Fatalf("requests %+v", got)
	}
	if r := w.fake.Reactions("C1", root); strings.Join(r, ",") != "x" {
		t.Fatalf("reactions on the asked mention %v", r)
	}
	if eph := w.fake.Ephemerals(); len(eph) != 1 || !strings.Contains(eph[0].Text, "Filed BIL-1") {
		t.Fatalf("ephemerals %+v", eph)
	}
}

// Giving up on someone linked with no display name records them by their
// provider name.
func TestChatGivingUpNamesAnAskerLinkedWithoutAName(t *testing.T) {
	w := setup(t)
	w.fake.FailNext("conversations.info", "missing_scope")
	w.exec(`INSERT INTO chat_identities (organization_id, provider, external_workspace_id, external_user_id, account_id, person_id, handle,
		display_name) VALUES ($1, 'slack', 'T1', 'U3', $2, $3, 'gus', '')`, w.org, "acc_U3_"+w.org, w.people["Gus"][0])
	w.receive("Gus", "C1", "", "file it")
	w.once()
	if n := w.one(`SELECT external_user_name FROM chat_requests WHERE organization_id = $1`, w.org); n != "Gus" {
		t.Fatalf("named %q", n)
	}
}

// A clarifying question the provider refuses outright ends the mention:
// the asker is told privately, the mention is ❌, Requests says failed
// with what was asked, and nothing is tried again. A transient failure is
// tried again and the question posted once. What was asked stays the
// model's route, which the clarify request already carried.
func TestChatARefusedQuestionEndsTheMention(t *testing.T) {
	low := answer(map[string]any{"route": "task", "confidence": 0.6, "project": "BIL", "title": "Fix the export", "goal": "Fix it."})
	for _, code := range []string{"not_in_channel", "channel_not_found"} {
		t.Run(code, func(t *testing.T) {
			w := setup(t)
			w.model.queue(low)
			w.fake.FailNext("chat.postMessage", code)
			ts, ev := w.receive("Eli", "C1", "", "the export is broken")
			w.once()
			if e := w.event(ev); !e.Processed || e.Attempts != 0 {
				t.Fatalf("delivery %+v", e)
			}
			got := w.requests()
			if len(got) != 1 || got[0].Route != "failed" || got[0].AskedRoute != "task" || !strings.Contains(got[0].Reasoning, code) {
				t.Fatalf("requests %+v", got)
			}
			if eph := w.fake.Ephemerals(); len(eph) != 1 || eph[0].User != "U2" || !strings.Contains(eph[0].Text, "couldn't ask its question") {
				t.Fatalf("ephemerals %+v", eph)
			}
			if r := w.fake.Reactions("C1", ts); strings.Join(r, ",") != "x" {
				t.Fatalf("reactions %v", r)
			}
			if n := len(w.fake.BotMessages()); n != 0 {
				t.Fatalf("%d messages posted", n)
			}
			w.due()
			if n := w.once(); n != 0 || w.model.calls() != 1 {
				t.Fatalf("claimed again %d, %d model calls", n, w.model.calls())
			}
		})
	}
	t.Run("transient", func(t *testing.T) {
		w := setup(t)
		w.model.queue(low)
		w.fake.FailNext("chat.postMessage", "internal_error")
		_, ev := w.receive("Eli", "C1", "", "the export is broken")
		w.once()
		if e := w.event(ev); e.Processed || e.Attempts != 1 {
			t.Fatalf("after the failed question: %+v", e)
		}
		w.due()
		w.once()
		if e := w.event(ev); !e.Processed {
			t.Fatalf("not processed: %+v", e)
		}
		if got := w.requests(); len(got) != 1 || got[0].Route != "clarify" {
			t.Fatalf("requests %+v", got)
		}
		if bot := w.fake.BotMessages(); len(bot) != 1 || !strings.Contains(bot[0].Text, "Should I file") || len(w.fake.Ephemerals()) != 0 {
			t.Fatalf("messages %+v, ephemerals %+v", bot, w.fake.Ephemerals())
		}
	})
}

// A click whose routing committed but whose question update failed
// transiently posts, on the retry, the update and the reply once each.
func TestChatAClickWhoseUpdateFailedPostsTheRestOnTheRetry(t *testing.T) {
	w := setup(t)
	w.model.queue(answer(map[string]any{"route": "task", "confidence": 0.6, "project": "BIL", "title": "Fix the export", "goal": "Fix it."}))
	root := w.mention("Eli", "C1", "", "the export is broken")
	q := lastBot(t, w.fake)
	w.fake.FailNext("chat.update", "internal_error")
	w.click("Eli", "C1", q.TS, root, buttonValue(t, q, 0))
	if got := w.requests(); len(got) != 2 || got[1].Route != "task" || len(w.fake.Updates()) != 0 {
		t.Fatalf("after the failed update: %+v", got)
	}
	w.due()
	w.sweep()
	if u := w.fake.Updates(); len(u) != 1 || u[0].Text != "Eli chose File it in BIL" {
		t.Fatalf("updates %+v", u)
	}
	var filed int
	for _, m := range w.fake.BotMessages() {
		if strings.HasPrefix(m.Text, "Filed BIL-1") {
			filed++
		}
	}
	if filed != 1 || w.count(`SELECT count(*) FROM tasks WHERE organization_id = $1`, w.org) != 1 {
		t.Fatalf("%d replies", filed)
	}
}

// A mention kept until its author connects is routed in order with the
// mentions that follow it in its thread.
func TestChatAKeptMentionIsOrderedWithItsThread(t *testing.T) {
	w := setup(t)
	gus := w.people["Gus"][0]
	root := w.mention("Gus", "C1", "", "file a task: tidy the export page")
	prompt := w.promptState()
	w.fake.AuthorizeAs("", "U3")
	l, err := w.svc.CompleteConnect(w.ctx, callback(w.beginConnect(gus, prompt), "b"))
	if err != nil || l.Outcome != "connected" {
		t.Fatalf("connect: %+v, %v", l, err)
	}
	w.store(fakeslack.Mention("T1", "EvNext", "C1", root, w.fake.Post("C1", root, "U3", "<@UBT1> and more"), "U3", "and more"))
	release := make(chan struct{})
	w.svc.SetBeforeMake(func(context.Context) error { <-release; return nil })
	w.model.queue(answer(map[string]any{"route": "brainstorm", "confidence": 0.9, "title": "Tidy"}))
	n, err := w.svc.Sweep(w.ctx)
	close(release)
	w.svc.Wait()
	if err != nil || n != 1 {
		t.Fatalf("claimed %d (%v): the kept mention, not the one after it in its thread", n, err)
	}
	w.sweep()
	if got := w.requests(); len(got) != 3 || got[1].Route != "brainstorm" || got[2].Route != "resume" {
		t.Fatalf("requests %+v", got)
	}
}

// promptState is the state of the last Connect prompt dude sent.
func (w *world) promptState() string {
	w.t.Helper()
	eph := w.fake.Ephemerals()
	if len(eph) == 0 {
		w.t.Fatal("no Connect prompt")
	}
	_, q, _ := strings.Cut(regexpFind(string(eph[len(eph)-1].Blocks)), "?")
	v, _ := url.ParseQuery(q)
	return v.Get("state")
}
