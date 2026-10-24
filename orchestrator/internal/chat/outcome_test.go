package chat_test

import (
	"context"
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
	w.model.queue(answer(map[string]any{"route": "task", "confidence": 0.9, "project": "ABS", "title": "Stream it", "goal": "g"}))
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
	if len(eph) != 1 || eph[0].User != "U2" || !strings.Contains(eph[0].Text, "Filed ABS-1 Stream it") ||
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
	w.model.queue(answer(map[string]any{"route": "task", "confidence": 0.9, "project": "ABS", "title": "Stream it", "goal": "g"}))
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
	if eph := w.fake.Ephemerals(); len(eph) != 1 || !strings.Contains(eph[0].Text, "Filed ABS-1 Stream it") {
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
	w.model.queue(answer(map[string]any{"route": "task", "confidence": 0.9, "project": "ABS", "title": "Stream it", "goal": "g"}))
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
	if bot := w.fake.BotMessages(); len(bot) != 1 || !strings.HasPrefix(bot[0].Text, "Filed ABS-1") {
		t.Fatalf("replies %+v", bot)
	}
	if eph := w.fake.Ephemerals(); len(eph) != 0 {
		t.Fatalf("ephemerals %+v", eph)
	}
}

// A click whose routing committed but whose question update failed
// transiently posts, on the retry, the update and the reply once each.
func TestChatAClickWhoseUpdateFailedPostsTheRestOnTheRetry(t *testing.T) {
	w := setup(t)
	w.model.queue(answer(map[string]any{"route": "task", "confidence": 0.6, "project": "ABS", "title": "Fix the export", "goal": "Fix it."}))
	root := w.mention("Eli", "C1", "", "the export is broken")
	q := lastBot(t, w.fake)
	w.fake.FailNext("chat.update", "internal_error")
	w.click("Eli", "C1", q.TS, root, buttonValue(t, q, 0))
	if got := w.requests(); len(got) != 2 || got[1].Route != "task" || len(w.fake.Updates()) != 0 {
		t.Fatalf("after the failed update: %+v", got)
	}
	w.due()
	w.sweep()
	if u := w.fake.Updates(); len(u) != 1 || u[0].Text != "Eli chose File it in ABS" {
		t.Fatalf("updates %+v", u)
	}
	var filed int
	for _, m := range w.fake.BotMessages() {
		if strings.HasPrefix(m.Text, "Filed ABS-1") {
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
