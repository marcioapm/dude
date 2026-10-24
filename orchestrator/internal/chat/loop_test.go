package chat_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/marciomartins/dude/orchestrator/internal/chat"
	"github.com/marciomartins/dude/orchestrator/internal/fakeslack"
)

// receive stores a mention by name without routing it.
func (w *world) receive(name, channel, thread, text string) (ts, event string) {
	w.t.Helper()
	user := w.people[name][1]
	ts = w.fake.Post(channel, thread, user, "<@UBT1> "+text)
	w.seq++
	event = fmt.Sprintf("Ev%d", w.seq)
	w.store(fakeslack.Mention("T1", event, channel, thread, ts, user, text))
	return ts, event
}

func (w *world) store(payload map[string]any) {
	w.t.Helper()
	raw := mustJSON(payload)
	req, _ := fakeslack.SignedRequest("http://x/", signingSecret, "application/json", raw, time.Now())
	if _, err := w.svc.Receive(w.ctx, "slack", false, req.Header, raw); err != nil {
		w.t.Fatalf("receive: %v", err)
	}
}

// once runs one sweep and waits for what it started.
func (w *world) once() int {
	w.t.Helper()
	n, err := w.svc.Sweep(w.ctx)
	if err != nil {
		w.t.Fatal(err)
	}
	w.svc.Wait()
	return n
}

// due makes every pending delivery due now.
func (w *world) due() {
	w.exec(`UPDATE chat_events SET next_attempt_at = NULL WHERE organization_id = $1`, w.org)
}

func mustJSON(v any) []byte {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return raw
}

func urlQueryEscape(s string) string { return url.QueryEscape(s) }

// buttonValue is the value of a question's i-th button.
func buttonValue(t *testing.T, q fakeslack.Message, i int) string {
	t.Helper()
	var blocks []map[string]any
	if err := json.Unmarshal(q.Blocks, &blocks); err != nil || len(blocks) < 2 {
		t.Fatalf("blocks %s", q.Blocks)
	}
	return blocks[1]["elements"].([]any)[i].(map[string]any)["value"].(string)
}

type eventRow struct {
	Attempts            int
	Processed, Decision bool
	LastError           string
	NextIn              time.Duration
}

func (w *world) event(id string) eventRow {
	w.t.Helper()
	var e eventRow
	var next *time.Time
	var errText *string
	if err := w.owner.QueryRow(w.ctx, `SELECT attempts, processed_at IS NOT NULL, decision IS NOT NULL, last_error, next_attempt_at
		FROM chat_events WHERE event_id = $1`, id).Scan(&e.Attempts, &e.Processed, &e.Decision, &errText, &next); err != nil {
		w.t.Fatal(err)
	}
	if errText != nil {
		e.LastError = *errText
	}
	if next != nil {
		e.NextIn = time.Until(*next)
	}
	return e
}

// A failure after the routing call resumes from the kept decision: the
// model is asked once, and what was made is made once.
func TestChatARetryActsOnTheKeptDecisionAndNeverAsksAgain(t *testing.T) {
	w := setup(t)
	w.model.queue(answer(map[string]any{"route": "task", "confidence": 0.9, "project": "BIL", "title": "Stream it", "goal": "g"}))
	fails := 1
	w.svc.SetBeforeMake(func(context.Context) error {
		if fails > 0 {
			fails--
			return &pgconn.PgError{Code: "40001", Message: "could not serialize access"}
		}
		return nil
	})
	_, ev := w.receive("Eli", "C1", "", "file: stream it")
	w.once()
	if e := w.event(ev); e.Processed || e.Attempts != 1 || !e.Decision || !strings.Contains(e.LastError, "serialize") {
		t.Fatalf("after a serialisation failure: %+v", e)
	}
	if n := w.count(`SELECT count(*) FROM tasks WHERE organization_id = $1`, w.org); n != 0 {
		t.Fatal("a task was filed by the failed attempt")
	}
	w.due()
	w.once()
	if w.model.calls() != 1 {
		t.Fatalf("the model was asked %d times", w.model.calls())
	}
	if got := w.requests(); len(got) != 1 || got[0].Route != "task" {
		t.Fatalf("requests %+v", got)
	}
	if e := w.event(ev); !e.Processed {
		t.Fatalf("not processed: %+v", e)
	}
}

// A retry acting on a kept decision reuses what it was decided from: the
// conversation, its members and the thread are not read again.
func TestChatARetryOnAKeptDecisionReadsNothingAgain(t *testing.T) {
	w := setup(t)
	root := w.fake.Post("G1", "", "U9", "the export times out")
	w.model.queue(answer(map[string]any{"route": "brainstorm", "confidence": 0.9, "title": "Exports"}))
	fails := 1
	w.svc.SetBeforeMake(func(context.Context) error {
		if fails > 0 {
			fails--
			return &pgconn.PgError{Code: "40001", Message: "could not serialize access"}
		}
		return nil
	})
	w.receive("Márcia", "G1", root, "look into it")
	w.once()
	methods := []string{"conversations.info", "conversations.members", "conversations.replies"}
	before := map[string]int{}
	for _, m := range methods {
		before[m] = w.fake.Calls(m)
	}
	w.due()
	w.once()
	for _, m := range methods {
		if n := w.fake.Calls(m) - before[m]; n != 0 {
			t.Fatalf("the retry called %s %d times", m, n)
		}
	}
	got := w.requests()
	if len(got) != 1 || got[0].Route != "brainstorm" || strings.Join(got[0].Added, ",") != w.people["Eli"][0] {
		t.Fatalf("requests %+v", got)
	}
	if o := w.one(`SELECT origin->'thread'->0->>'text' FROM sessions WHERE id = $1`, got[0].ResultID); o != "the export times out" {
		t.Fatalf("thread %q", o)
	}
}

// A reply that fails transiently is posted on the retry, alone: nothing
// is made twice and the model is not asked again.
func TestChatARetryPostsOnlyWhatIsLeft(t *testing.T) {
	w := setup(t)
	w.model.queue(answer(map[string]any{"route": "brainstorm", "confidence": 0.9, "title": "Hello"}))
	w.fake.FailNext("chat.postMessage", "internal_error")
	ts, ev := w.receive("Márcia", "C1", "", "hello")
	w.once()
	if e := w.event(ev); e.Processed || e.Attempts != 1 {
		t.Fatalf("after the failed reply: %+v", e)
	}
	if n := len(w.fake.BotMessages()); n != 0 {
		t.Fatalf("%d replies", n)
	}
	w.due()
	w.once()
	if n := len(w.fake.BotMessages()); n != 1 || w.model.calls() != 1 ||
		w.count(`SELECT count(*) FROM sessions WHERE organization_id = $1`, w.org) != 1 {
		t.Fatalf("%d replies, %d model calls", n, w.model.calls())
	}
	if r := w.fake.Reactions("C1", ts); strings.Join(r, ",") != "white_check_mark" {
		t.Fatalf("reactions %v", r)
	}
}

// diedBeforeTheMark puts a delivered mention back where a worker that
// died after posting the reply, and before the mark, left it: the reply
// effect done, the mark not, nothing replied, and the lease run out.
func (w *world) diedBeforeTheMark(ev string) {
	w.t.Helper()
	w.exec(`UPDATE chat_requests SET replied_at = NULL, effects = jsonb_set(effects, '{1}', $3::jsonb)
		WHERE organization_id = $1 AND event_id = $2`, w.org, ev, `{"kind":"react","reaction":`+fmt.Sprint(int(chat.ReactionDone))+`}`)
	w.exec(`UPDATE chat_events SET processed_at = NULL, claimed_until = NULL WHERE organization_id = $1 AND event_id = $2`, w.org, ev)
}

// A retry after the reply was posted, by a worker that died before the
// ✅, posts only the ✅: the reply is not posted twice.
func TestChatARetryAfterAPostedReplyPostsOnlyTheMark(t *testing.T) {
	w := setup(t)
	w.model.queue(answer(map[string]any{"route": "brainstorm", "confidence": 0.9, "title": "Hello"}))
	ts, ev := w.receive("Márcia", "C1", "", "hello")
	w.once()
	if e := w.event(ev); !e.Processed || len(w.fake.BotMessages()) != 1 {
		t.Fatalf("after the first attempt: %+v, %d replies", e, len(w.fake.BotMessages()))
	}
	w.diedBeforeTheMark(ev)
	marks := w.fake.Calls("reactions.add")
	w.once()
	if n := len(w.fake.BotMessages()); n != 1 || w.model.calls() != 1 || !w.event(ev).Processed || w.fake.Calls("reactions.add") != marks+1 {
		t.Fatalf("%d replies, %d model calls, %d marks", n, w.model.calls(), w.fake.Calls("reactions.add")-marks)
	}
	if r := w.fake.Reactions("C1", ts); strings.Join(r, ",") != "white_check_mark" {
		t.Fatalf("reactions %v", r)
	}
}

// A transient failure is tried again after a backoff that doubles (or the
// provider's Retry-After), MaxAttempts times, then given up: the person is
// told privately and Requests says failed.
func TestChatTransientFailuresBackOffThenGiveUp(t *testing.T) {
	w := setup(t)
	w.fake.FailTimes("conversations.info", "ratelimited", 100)
	_, ev := w.receive("Márcia", "C1", "", "hello")
	w.once()
	e := w.event(ev)
	if e.Processed || e.Attempts != 1 || e.NextIn < 3*time.Second || e.NextIn > 6*time.Second || !strings.Contains(e.LastError, "429") {
		t.Fatalf("first failure: %+v", e)
	}
	if n := w.count(`SELECT failed_today FROM chat_installations WHERE organization_id = $1`, w.org); n != 1 {
		t.Fatalf("failed_today %d", n)
	}
	// Not due yet: nothing is claimed.
	if n := w.once(); n != 0 {
		t.Fatalf("claimed %d before its backoff", n)
	}
	w.due()
	w.once()
	if e := w.event(ev); e.Attempts != 2 || e.NextIn < 8*time.Second || e.NextIn > 11*time.Second {
		t.Fatalf("second failure: %+v", e)
	}
	for range chat.MaxAttempts {
		w.due()
		w.once()
	}
	e = w.event(ev)
	if !e.Processed || e.Attempts != chat.MaxAttempts {
		t.Fatalf("not given up: %+v", e)
	}
	if got := w.requests(); len(got) != 1 || got[0].Route != "failed" {
		t.Fatalf("requests %+v", got)
	}
	if eph := w.fake.Ephemerals(); len(eph) != 1 || !strings.Contains(eph[0].Text, "did not answer") {
		t.Fatalf("ephemerals %+v", eph)
	}
	if w.model.calls() != 0 {
		t.Fatal("the model was asked")
	}
	// Given up: never claimed again.
	w.due()
	if n := w.once(); n != 0 {
		t.Fatalf("a given-up delivery was claimed %d", n)
	}
}

// Given up after the routing call was kept: the failed request still
// links that call, so Requests counts the model as asked.
func TestChatGivingUpAfterTheRoutingCallKeepsItsModelCall(t *testing.T) {
	w := setup(t)
	w.model.queue(answer(map[string]any{"route": "task", "confidence": 0.9, "project": "BIL", "title": "Stream it", "goal": "g"}))
	w.svc.SetBeforeMake(func(context.Context) error {
		return &pgconn.PgError{Code: "40001", Message: "could not serialize access"}
	})
	_, ev := w.receive("Eli", "C1", "", "file: stream it")
	w.once()
	for range chat.MaxAttempts {
		w.due()
		w.once()
	}
	if e := w.event(ev); !e.Processed || e.Attempts != chat.MaxAttempts {
		t.Fatalf("not given up: %+v", e)
	}
	if w.model.calls() != 1 {
		t.Fatalf("the model was asked %d times", w.model.calls())
	}
	got := w.requests()
	if len(got) != 1 || got[0].Route != "failed" || got[0].ModelCall == "" {
		t.Fatalf("requests %+v", got)
	}
}

// Retry-After longer than the backoff is waited for.
func TestChatWaitsForTheProvidersRetryAfter(t *testing.T) {
	_, after := chat.IsTransient(chat.Transient(errors.New("x"), 40*time.Second))
	if after != 40*time.Second {
		t.Fatalf("after %v", after)
	}
	if ok, _ := chat.IsTransient(&pgconn.PgError{Code: "23503"}); ok {
		t.Fatal("a foreign key violation is transient")
	}
	if ok, _ := chat.IsTransient(&pgconn.PgError{Code: "40P01"}); !ok {
		t.Fatal("a deadlock is not transient")
	}
	if ok, _ := chat.IsTransient(context.DeadlineExceeded); !ok {
		t.Fatal("a deadline is not transient")
	}
}

// A decision the database refuses is not retried: it becomes a
// brainstorm, recorded with what was asked.
func TestChatADeterministicFailureFallsBackWithoutRetrying(t *testing.T) {
	w := setup(t)
	w.model.queue(answer(map[string]any{"route": "task", "confidence": 0.9, "project": "BIL", "epic": "Exports", "title": "x", "goal": "g"}))
	w.svc.SetBeforeMake(func(ctx context.Context) error {
		_, err := w.owner.Exec(ctx, `DELETE FROM epics WHERE id = $1`, "epc_exp_"+w.org)
		return err
	})
	_, ev := w.receive("Eli", "C1", "", "file it")
	w.once()
	if e := w.event(ev); !e.Processed || e.Attempts != 0 {
		t.Fatalf("delivery %+v", e)
	}
	got := w.requests()
	if len(got) != 1 || got[0].Route != "brainstorm" || got[0].AskedRoute != "task" || got[0].ResultKind != "session" || w.model.calls() != 1 {
		t.Fatalf("requests %+v, %d model calls", got, w.model.calls())
	}
	if n := w.count(`SELECT count(*) FROM tasks WHERE organization_id = $1`, w.org); n != 0 {
		t.Fatal("a task was filed")
	}
}

// One thread's deliveries are routed in order, one at a time; different
// threads' at once. A claimed delivery is no other sweep's.
func TestChatRoutesOneThreadInOrderAndThreadsAtOnce(t *testing.T) {
	w := setup(t)
	w.receive("Márcia", "C1", "", "first")
	root := w.fake.Post("C1", "", "U1", "a thread")
	w.seq++
	w.store(fakeslack.Mention("T1", "EvA", "C1", root, w.fake.Post("C1", root, "U1", "<@UBT1> a"), "U1", "a"))
	w.store(fakeslack.Mention("T1", "EvB", "C1", root, w.fake.Post("C1", root, "U1", "<@UBT1> b"), "U1", "b"))
	// Hold the sweep's work: claimed, not yet routed.
	release := make(chan struct{})
	w.svc.SetBeforeMake(func(context.Context) error { <-release; return nil })
	for range 3 {
		w.model.queue(answer(map[string]any{"route": "brainstorm", "confidence": 0.9, "title": "x"}))
	}
	n, err := w.svc.Sweep(w.ctx)
	if err != nil || n != 2 {
		t.Fatalf("claimed %d (%v): the first mention and EvA, not EvB behind EvA", n, err)
	}
	if n, err := w.svc.Sweep(w.ctx); err != nil || n != 0 {
		t.Fatalf("a second sweep claimed %d (%v)", n, err)
	}
	close(release)
	w.svc.Wait()
	if e := w.event("EvB"); e.Processed {
		t.Fatal("EvB was routed with EvA")
	}
	if n := w.once(); n != 1 {
		t.Fatalf("then claimed %d", n)
	}
	if got := w.requests(); len(got) != 3 {
		t.Fatalf("requests %+v", got)
	}
	// EvB found EvA's session bound to the thread: it went in, no model.
	if got := w.requests(); got[2].Route != "resume" || w.model.calls() != 2 {
		t.Fatalf("requests %+v, %d calls", got, w.model.calls())
	}
}

// A clicked choice is spent only by the routing it starts: a click whose
// routing failed transiently is routed on the retry, and the question
// updated once.
func TestChatAClickSurvivesARetry(t *testing.T) {
	w := setup(t)
	w.model.queue(answer(map[string]any{"route": "task", "confidence": 0.6, "project": "BIL", "title": "Fix the export", "goal": "Fix it."}))
	root := w.mention("Eli", "C1", "", "the export is broken")
	q := lastBot(t, w.fake)
	value := buttonValue(t, q, 0)
	w.fake.FailNext("conversations.info", "ratelimited")
	raw := mustJSON(fakeslack.Click("T1", "C1", q.TS, root, "U2", value))
	body := []byte("payload=" + urlQueryEscape(string(raw)))
	req, _ := fakeslack.SignedRequest("http://x/", signingSecret, "application/x-www-form-urlencoded", body, time.Now())
	if _, err := w.svc.Receive(w.ctx, "slack", true, req.Header, body); err != nil {
		t.Fatal(err)
	}
	w.once()
	if len(w.fake.Updates()) != 0 || len(w.requests()) != 1 {
		t.Fatal("the failed click was spent")
	}
	w.due()
	w.once()
	if got := w.requests(); len(got) != 2 || got[1].Route != "task" {
		t.Fatalf("requests %+v", got)
	}
	if u := w.fake.Updates(); len(u) != 1 || u[0].Text != "Eli chose File it in BIL" {
		t.Fatalf("updates %+v", u)
	}
}

// A clarifying question is recorded before it is posted: a retry after
// the post failed posts the recorded question once, with the same buttons.
func TestChatAQuestionIsRecordedBeforeItIsPosted(t *testing.T) {
	w := setup(t)
	w.model.queue(answer(map[string]any{"route": "task", "confidence": 0.6, "project": "BIL", "title": "x", "goal": "g"}))
	w.fake.FailNext("chat.postMessage", "internal_error")
	_, ev := w.receive("Eli", "C1", "", "maybe a task")
	w.once()
	if got := w.requests(); len(got) != 1 || got[0].Route != "clarify" || len(w.fake.BotMessages()) != 0 {
		t.Fatalf("requests %+v", got)
	}
	w.due()
	w.once()
	msgs := w.fake.BotMessages()
	if len(msgs) != 1 || w.model.calls() != 1 || !w.event(ev).Processed {
		t.Fatalf("%d questions, %d calls", len(msgs), w.model.calls())
	}
	id := w.one(`SELECT id FROM chat_requests WHERE route = 'clarify'`)
	if v := buttonValue(t, msgs[0], 0); !strings.HasPrefix(v, id+":") {
		t.Fatalf("button %s for request %s", v, id)
	}
	if m := w.one(`SELECT question_message_id FROM chat_requests WHERE id = $1`, id); m != msgs[0].TS {
		t.Fatalf("question message %q", m)
	}
}

// A request recorded by another process between this one's check and its
// commit rolls back everything this one made.
func TestChatARequestRecordedMeanwhileRollsTheWorkBack(t *testing.T) {
	w := setup(t)
	w.model.queue(answer(map[string]any{"route": "task", "confidence": 0.9, "project": "BIL", "title": "T", "goal": "g"}))
	var ev string
	w.svc.SetBeforeMake(func(ctx context.Context) error {
		_, err := w.owner.Exec(ctx, `INSERT INTO chat_requests (id, organization_id, provider, event_id, external_workspace_id, conversation_id,
			external_user_id, route) VALUES ('chr_other', $1, 'slack', $2, 'T1', 'C1', 'U2', 'task')`, w.org, ev)
		return err
	})
	_, ev = w.receive("Eli", "C1", "", "file it")
	w.once()
	if n := w.count(`SELECT count(*) FROM tasks WHERE organization_id = $1`, w.org); n != 0 {
		t.Fatalf("%d tasks filed under a request another process recorded", n)
	}
	if n := w.count(`SELECT count(*) FROM chat_threads WHERE organization_id = $1`, w.org); n != 0 {
		t.Fatal("the thread was bound")
	}
}

// A delivery handled again (a crash between its work and marking it
// done) makes nothing twice, on every path: a DM's session, a task, and a
// message into a bound thread's session.
func TestChatARedeliveryMakesNothingTwice(t *testing.T) {
	w := setup(t)
	counts := func() string {
		return fmt.Sprintf("sessions %d tasks %d runs %d directives %d chat %d requests %d replies %d",
			w.count(`SELECT count(*) FROM sessions WHERE organization_id = $1`, w.org),
			w.count(`SELECT count(*) FROM tasks WHERE organization_id = $1`, w.org),
			w.count(`SELECT count(*) FROM runs WHERE organization_id = $1`, w.org),
			w.count(`SELECT count(*) FROM directives WHERE organization_id = $1`, w.org),
			w.count(`SELECT count(*) FROM events WHERE organization_id = $1 AND event_type = 'chat.message'`, w.org),
			w.count(`SELECT count(*) FROM chat_requests WHERE organization_id = $1`, w.org),
			len(w.fake.BotMessages()))
	}
	replay := func(name string) {
		t.Helper()
		before, calls := counts(), w.model.calls()
		w.exec(`UPDATE chat_events SET processed_at = NULL, decision = NULL WHERE organization_id = $1`, w.org)
		w.sweep()
		if after := counts(); after != before || w.model.calls() != calls {
			t.Fatalf("%s replayed: %s, was %s; %d model calls, were %d", name, after, before, w.model.calls(), calls)
		}
		w.exec(`DELETE FROM chat_events WHERE organization_id = $1`, w.org)
	}
	w.model.queue(answer(map[string]any{"route": "brainstorm", "confidence": 0.9, "title": "Ideas"}))
	w.dm("Márcia", "ideas")
	replay("a DM")
	w.model.queue(answer(map[string]any{"route": "task", "confidence": 0.9, "project": "BIL", "title": "T", "goal": "g"}))
	w.mention("Eli", "C1", "", "file it")
	replay("a task")
	w.model.queue(answer(map[string]any{"route": "brainstorm", "confidence": 0.9, "title": "Talk"}))
	root := w.mention("Márcia", "C1", "", "talk")
	w.exec(`DELETE FROM chat_events WHERE organization_id = $1`, w.org)
	w.mention("Eli", "C1", root, "more")
	if got := w.last(); got.Route != "resume" {
		t.Fatalf("not into the bound session: %+v", got)
	}
	replay("a bound thread's message")
}
