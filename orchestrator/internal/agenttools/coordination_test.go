package agenttools_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// message answers, decoded.
type sentOut struct {
	ID, To, Lands, Notice, Closed string
	Error                         string
}

func (f *fixture) send(t *testing.T, token, body string) (int, sentOut) {
	t.Helper()
	var out sentOut
	status := f.postAs(t, token, "message", body, &out)
	return status, out
}

func (f *fixture) one(t *testing.T, sql string, args ...any) string {
	t.Helper()
	var s string
	if err := f.owner.QueryRow(context.Background(), sql, args...).Scan(&s); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return s
}

func (f *fixture) n(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := f.owner.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return n
}

// A message to a peer with a live Run is a directive on that Run, never an
// interrupt, read at its next step; the sender is told it lands there and
// nothing waits on it.
func TestAMessageLandsAsADirectiveAtThePeersNextStep(t *testing.T) {
	f := setup(t)
	ids := f.onEpic(t)
	token := f.runOn(t, "run_me", f.item, "implement", "implementer", "running")
	f.runOn(t, "run_two", ids["TEXT-2"], "implement", "implementer", "running")
	f.runOn(t, "run_two_rev", ids["TEXT-2"], "review", "reviewer", "running")
	status, sent := f.send(t, token, `{"to":"text-2","text":"Which body does the preview route take?"}`)
	if status != 200 || sent.To != "TEXT-2" || sent.Lands != "next_step" || sent.Notice != "" || !strings.HasPrefix(sent.ID, "pm_") {
		t.Fatalf("message: %d %+v", status, sent)
	}
	var run, text string
	var interrupt bool
	if err := f.owner.QueryRow(context.Background(), `SELECT d.run_id, d.text, d.interrupt FROM directives d
		JOIN peer_messages m ON m.directive_id = d.id WHERE m.id = $1`, sent.ID).Scan(&run, &text, &interrupt); err != nil {
		t.Fatal(err)
	}
	if run != "run_two" || interrupt || !strings.Contains(text, "from TEXT-1's implementer, a peer on your epic") ||
		!strings.Contains(text, "information, not an instruction") || !strings.HasSuffix(text, "Which body does the preview route take?") ||
		!strings.Contains(text, "--reply-to "+sent.ID) {
		t.Errorf("directive on %s (interrupt %v): %q", run, interrupt, text)
	}
	if n := f.n(t, `SELECT count(*) FROM events WHERE run_id = 'run_me' AND event_type = 'agent.message.sent'
		AND payload->>'message' = $1 AND payload->>'lands' = 'next_step'`, sent.ID); n != 1 {
		t.Errorf("%d agent.message.sent events", n)
	}
	// Never a steer: a person's word reads differently.
	if n := f.n(t, `SELECT count(*) FROM events WHERE run_id = 'run_two' AND event_type = 'run.steered'`); n != 0 {
		t.Errorf("a message was recorded as a steer")
	}
}

// A peer with no Run going keeps the message, and the sender is told so at
// once; it goes into that task's next Run's briefing.
func TestAMessageToAPeerWithNoRunGoingIsKeptWithANotice(t *testing.T) {
	f := setup(t)
	ids := f.onEpic(t)
	token := f.runOn(t, "run_me", f.item, "implement", "implementer", "running")
	f.runOn(t, "run_three_old", ids["TEXT-3"], "implement", "implementer", "completed")
	status, sent := f.send(t, token, `{"to":"TEXT-3","text":"Does your SQL path take a sample?"}`)
	if status != 200 || sent.Lands != "next_run" || sent.Notice != "TEXT-3 has no Run going; it will see this when its next Run starts." {
		t.Fatalf("message: %d %+v", status, sent)
	}
	if n := f.n(t, `SELECT count(*) FROM peer_messages WHERE id = $1 AND directive_id IS NULL AND read_at IS NULL`, sent.ID); n != 1 {
		t.Errorf("the message was not kept for the next Run")
	}
}

// message refuses a broadcast, a task that is not a peer (recorded), an
// empty text and one past the steer's bound.
func TestAMessageGoesToOnePeerOnly(t *testing.T) {
	f := setup(t)
	f.onEpic(t)
	token := f.runOn(t, "run_me", f.item, "implement", "implementer", "running")
	for body, want := range map[string]string{
		`{"to":"all","text":"hi"}`:                       "pin a note",
		`{"to":"TEXT-2,TEXT-3","text":"hi"}`:             "pin a note",
		`{"to":"TEXT-5","text":"hi"}`:                    "not one of your peers",
		`{"to":"TEXT-4","text":"hi"}`:                    "not one of your peers",
		`{"to":"TEXT-1","text":"hi"}`:                    "not one of your peers",
		`{"to":"TEXT-2","text":"  "}`:                    "text is required",
		`{"to":"TEXT-2","text":"x","replyTo":"pm_nope"}`: "no message pm_nope",
	} {
		if status, out := f.post(t, token, "message", body); status != 422 || !strings.Contains(out["error"].(string), want) {
			t.Errorf("%s: %d %v, want %q", body, status, out, want)
		}
	}
	if n := f.n(t, `SELECT count(*) FROM events WHERE run_id = 'run_me' AND event_type = 'agent.message.refused'
		AND payload->>'reason' = 'not_a_peer'`); n != 3 {
		t.Errorf("%d not_a_peer refusals recorded, want 3", n)
	}
	if n := f.n(t, `SELECT count(*) FROM peer_messages`); n != 0 {
		t.Errorf("%d messages sent", n)
	}
}

// A Run may send five messages in any rolling hour: the sixth is refused,
// saying when the next is allowed and where else to look, recorded; once
// the first is an hour old, the next is allowed.
func TestTheSixthMessageInAnHourIsRefusedAndTheFirstAfterIsAllowed(t *testing.T) {
	f := setup(t)
	f.onEpic(t)
	token := f.runOn(t, "run_me", f.item, "implement", "implementer", "running")
	for i := range 5 {
		if status, sent := f.send(t, token, `{"to":"TEXT-2","text":"question"}`); status != 200 {
			t.Fatalf("message %d: %d %+v", i+1, status, sent)
		}
	}
	// The first was sent 59 minutes ago: the next is allowed at its hour.
	mustExec(t, f.owner, `UPDATE peer_messages SET created_at = now() - interval '59 minutes'
		WHERE id = (SELECT id FROM peer_messages ORDER BY id LIMIT 1)`)
	status, out := f.post(t, token, "message", `{"to":"TEXT-2","text":"one more"}`)
	msg, _ := out["error"].(string)
	if status != 422 || !strings.Contains(msg, "Refused: this Run has sent 5 messages in the last hour, the most a Run may send.") ||
		!strings.Contains(msg, "The next is allowed at "+f.one(t, `SELECT to_char((min(created_at) + interval '1 hour') AT TIME ZONE 'UTC', 'HH24:MI') FROM peer_messages`)) ||
		!strings.Contains(msg, "`dude task list --peers`, `dude diff --task TEXT-2` and the epic notes may already answer it") {
		t.Fatalf("the sixth: %d %q", status, msg)
	}
	if n := f.n(t, `SELECT count(*) FROM events WHERE run_id = 'run_me' AND event_type = 'agent.message.refused'
		AND payload->>'reason' = 'rate' AND payload->>'retry_at' IS NOT NULL`); n != 1 {
		t.Errorf("%d rate refusals recorded", n)
	}
	mustExec(t, f.owner, `UPDATE peer_messages SET created_at = now() - interval '61 minutes'
		WHERE id = (SELECT id FROM peer_messages ORDER BY id LIMIT 1)`)
	if status, sent := f.send(t, token, `{"to":"TEXT-2","text":"one more"}`); status != 200 {
		t.Errorf("the first after the hour: %d %+v", status, sent)
	}
	// Another Run of the same task has its own hour.
	other := f.runOn(t, "run_me_fix", f.item, "fix", "implementer", "running")
	if status, sent := f.send(t, other, `{"to":"TEXT-2","text":"from the fixer"}`); status != 200 {
		t.Errorf("another Run's first: %d %+v", status, sent)
	}
}

// A reply goes back to the asking Run, in the same thread; a reply to a
// message of another pair, or to the wrong task, is refused.
func TestRepliesFormAThreadBetweenTwoTasks(t *testing.T) {
	f := setup(t)
	ids := f.onEpic(t)
	me := f.runOn(t, "run_me", f.item, "implement", "implementer", "running")
	two := f.runOn(t, "run_two", ids["TEXT-2"], "implement", "implementer", "running")
	three := f.runOn(t, "run_three", ids["TEXT-3"], "implement", "implementer", "running")
	_, q := f.send(t, me, `{"to":"TEXT-2","text":"Which columns?"}`)
	status, a := f.send(t, two, `{"to":"TEXT-1","text":"mean and variance","replyTo":"`+q.ID+`"}`)
	if status != 200 || a.Lands != "next_step" {
		t.Fatalf("reply: %d %+v", status, a)
	}
	if got := f.one(t, `SELECT d.run_id || ' ' || m.reply_to FROM peer_messages m JOIN directives d ON d.id = m.directive_id WHERE m.id = $1`, a.ID); got != "run_me "+q.ID {
		t.Errorf("the reply went to %s", got)
	}
	// And back again in the same thread.
	if status, _ := f.send(t, me, `{"to":"TEXT-2","text":"thanks: and the bins?","replyTo":"`+a.ID+`"}`); status != 200 {
		t.Errorf("a reply to the reply: %d", status)
	}
	if status, out := f.post(t, three, "message", `{"to":"TEXT-2","text":"me too","replyTo":"`+q.ID+`"}`); status != 422 ||
		!strings.Contains(out["error"].(string), "not a message between your task and TEXT-2") {
		t.Errorf("a third task in a thread: %d %v", status, out)
	}
	if status, out := f.post(t, two, "message", `{"to":"TEXT-3","text":"x","replyTo":"`+q.ID+`"}`); status != 422 {
		t.Errorf("a reply to the wrong task: %d %v", status, out)
	}
}

// wait_for_reply opens one wait on the caller's own message, which holds
// its turn; with the reply already there it returns it at once and opens
// nothing.
func TestWaitForReplyWaitsOrReturnsTheReplyAtOnce(t *testing.T) {
	f := setup(t)
	ids := f.onEpic(t)
	me := f.runOn(t, "run_me", f.item, "implement", "implementer", "running")
	two := f.runOn(t, "run_two", ids["TEXT-2"], "implement", "implementer", "running")
	_, q := f.send(t, me, `{"to":"TEXT-2","text":"Which path?"}`)
	if status, out := f.post(t, two, "wait_for_reply", `{"message":"`+q.ID+`"}`); status != 422 ||
		!strings.Contains(out["error"].(string), "not a message you sent") {
		t.Errorf("waiting on another's message: %d %v", status, out)
	}
	var w struct {
		Waiting struct{ Message, On, How string }
		Reply   *struct{ Text string }
		Next    string
	}
	if status := f.postAs(t, me, "wait_for_reply", `{"message":"`+q.ID+`"}`, &w); status != 200 || w.Reply != nil ||
		w.Waiting.On != "TEXT-2" || w.Waiting.How != "wait" || !strings.HasPrefix(w.Next, "End your turn now.") {
		t.Fatalf("wait: %d %+v", status, w)
	}
	if n := f.n(t, `SELECT count(*) FROM peer_waits WHERE run_id = 'run_me' AND closed_at IS NULL AND how = 'wait'`); n != 1 {
		t.Errorf("%d open waits", n)
	}
	if n := f.n(t, `SELECT count(*) FROM events WHERE run_id = 'run_me' AND event_type = 'agent.peer.waiting'
		AND payload->>'how' = 'wait' AND payload->>'parks_at' IS NOT NULL`); n != 1 {
		t.Errorf("%d agent.peer.waiting events", n)
	}
	_, second := f.send(t, me, `{"to":"TEXT-2","text":"Another question?"}`)
	if status, out := f.post(t, me, "wait_for_reply", `{"message":"`+second.ID+`"}`); status != 422 || !strings.Contains(out["error"].(string), "one wait at a time") {
		t.Errorf("second wait: %d %v", status, out)
	}
	// The reply closes the wait, and is the waiting Run's next input.
	_, a := f.send(t, two, `{"to":"TEXT-1","text":"/v1/query/estimate","replyTo":"`+q.ID+`"}`)
	if got := f.one(t, `SELECT closed_by FROM peer_waits WHERE run_id = 'run_me'`); got != "reply" {
		t.Errorf("the wait closed by %s", got)
	}
	if n := f.n(t, `SELECT count(*) FROM peer_messages m JOIN directives d ON d.id = m.directive_id
		WHERE m.id = $1 AND d.run_id = 'run_me'`, a.ID); n != 1 {
		t.Errorf("the reply is not queued for the waiting Run")
	}
	if n := f.n(t, `SELECT count(*) FROM events WHERE run_id = 'run_me' AND event_type = 'agent.peer.wait_ended' AND payload->>'by' = 'reply'`); n != 1 {
		t.Errorf("%d wait_ended events", n)
	}
	// Asked again with the reply in: it is returned, and nothing waits.
	w.Reply = nil
	if status := f.postAs(t, me, "wait_for_reply", `{"message":"`+q.ID+`"}`, &w); status != 200 || w.Reply == nil ||
		w.Reply.Text != "/v1/query/estimate" {
		t.Fatalf("wait with the reply in: %d %+v", status, w)
	}
	_, self := f.send(t, me, `{"to":"TEXT-2","text":"Going ahead.","replyTo":"`+second.ID+`"}`)
	if self.ID == "" {
		t.Fatal("self reply refused")
	}
	if status, out := f.post(t, me, "wait_for_reply", `{"message":"`+second.ID+`"}`); status != 422 || !strings.Contains(out["error"].(string), "you closed") {
		t.Errorf("wait after self close: %d %v", status, out)
	}
	if n := f.n(t, `SELECT count(*) FROM peer_waits WHERE run_id = 'run_me' AND closed_at IS NULL`); n != 0 {
		t.Errorf("a wait opened on a message already answered")
	}
}

// A Run ending closes its open waits, recorded; the messages to its task
// stay for its next Run.
func TestARunEndingClosesItsWaits(t *testing.T) {
	for _, terminal := range []string{"completed", "failed", "aborted"} {
		t.Run(terminal, func(t *testing.T) {
			f := setup(t)
			ids := f.onEpic(t)
			me := f.runOn(t, "run_me", f.item, "implement", "implementer", "running")
			two := f.runOn(t, "run_two", ids["TEXT-2"], "implement", "implementer", "running")
			_, q := f.send(t, me, `{"to":"TEXT-2","text":"Which path?"}`)
			_, in := f.send(t, two, `{"to":"TEXT-1","text":"a question for TEXT-1"}`)
			if status, _ := f.post(t, me, "wait_for_reply", `{"message":"`+q.ID+`"}`); status != 200 {
				t.Fatal("wait")
			}
			mustExec(t, f.owner, `UPDATE runs SET status = $1::run_status WHERE id = 'run_me'`, terminal)
			if got := f.one(t, `SELECT closed_by FROM peer_waits WHERE run_id = 'run_me'`); got != "run_ended" {
				t.Errorf("the wait closed by %q", got)
			}
			if n := f.n(t, `SELECT count(*) FROM events WHERE run_id = 'run_me' AND event_type = 'agent.peer.wait_ended'
		AND payload->>'by' = 'run_ended'`); n != 1 {
				t.Errorf("%d wait_ended events", n)
			}
			if n := f.n(t, `SELECT count(*) FROM peer_messages WHERE id = $1 AND read_at IS NULL AND briefed_run_id IS NULL`, in.ID); n != 1 {
				t.Errorf("the message to the ended Run's task is gone")
			}
		})
	}
}

// Reviewers, the simplifier and QA read their peers but never write to
// them: message, wait_for_reply and note are not theirs. A brainstorm
// has none of the peer tools, and its list_tasks takes no peers.
func TestReadOnlyRolesAndBrainstormLackThePeerTools(t *testing.T) {
	f := setup(t)
	f.onEpic(t)
	writers := map[string][2]string{"implementer": {"implement", "implementer"}, "fixer": {"fix", "implementer"},
		"investigator": {"investigate", "investigator"}}
	for name, pr := range writers {
		token := f.runOn(t, "run_w_"+name, f.item, pr[0], pr[1], "running")
		if names := f.toolNames(t, token); !hasAll(names, "list_tasks", "run_diff", "message", "wait_for_reply", "note") {
			t.Errorf("the %s lacks a peer tool: %v", name, names)
		}
	}
	cond := f.conductor(t)
	if names := f.toolNames(t, cond); !hasAll(names, "list_tasks", "run_diff", "message", "wait_for_reply", "note") {
		t.Errorf("the conductor lacks a peer tool: %v", names)
	}
	for _, pr := range [][2]string{{"review", "reviewer"}, {"simplify", "simplifier"}, {"test", "qa_browser"}} {
		token := f.runOn(t, "run_r_"+pr[1], f.item, pr[0], pr[1], "running")
		names := f.toolNames(t, token)
		if !hasAll(names, "list_tasks", "run_diff") || hasAny(names, "message", "wait_for_reply", "note") {
			t.Errorf("the %s's tools: %v", pr[1], names)
		}
		for _, tool := range []string{"message", "note", "wait_for_reply"} {
			if status, _ := f.post(t, token, tool, `{"to":"TEXT-2","text":"x","message":"pm_x"}`); status != 404 {
				t.Errorf("the %s called %s: %d", pr[1], tool, status)
			}
		}
		if got := f.peers(t, token); len(got) != 3 {
			t.Errorf("the %s's peers: %v", pr[1], got)
		}
	}
	brain := f.session(t, "ssn_peer", "", "")
	names := f.toolNames(t, brain)
	if hasAny(names, "message", "wait_for_reply", "note", "run_diff") {
		t.Errorf("a brainstorm has a peer tool: %v", names)
	}
	if status, _ := f.post(t, brain, "list_tasks", `{"peers":true}`); status != 422 {
		t.Errorf("a brainstorm's list_tasks took peers: %d", status)
	}
}

func (f *fixture) toolNames(t *testing.T, token string) []string {
	t.Helper()
	req, _ := http.NewRequest("GET", f.url+"/tools", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out struct{ Tools []struct{ Name string } }
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil || res.StatusCode != 200 {
		t.Fatalf("tools: %d %v", res.StatusCode, err)
	}
	names := make([]string, len(out.Tools))
	for i, tool := range out.Tools {
		names[i] = tool.Name
	}
	return names
}

func hasAll(names []string, want ...string) bool {
	for _, w := range want {
		if !hasAny(names, w) {
			return false
		}
	}
	return true
}

func hasAny(names []string, want ...string) bool {
	for _, n := range names {
		for _, w := range want {
			if n == w {
				return true
			}
		}
	}
	return false
}

// Notes are numbered per epic and never edited: a replace pins the next
// number and marks the old one replaced; replacing a replaced note is
// refused, naming its successor. Agents cannot retire a note.
func TestNotesAreNumberedAndReplacedNeverEdited(t *testing.T) {
	f := setup(t)
	f.onEpic(t)
	token := f.runOn(t, "run_me", f.item, "implement", "implementer", "running")
	var pinned struct {
		ID       string
		N        int
		Replaces int
		Told     int
		Current  []struct {
			N    int
			Text string
			By   string
		}
	}
	if status := f.postAs(t, token, "note", `{"text":"Preview API: POST /v1/metrics/preview/history"}`, &pinned); status != 200 ||
		pinned.N != 1 || len(pinned.Current) != 1 || pinned.Current[0].By != "TEXT-1's implementer" {
		t.Fatalf("pin: %d %+v", status, pinned)
	}
	f.postAs(t, token, "note", `{"text":"Docs: .ai/INDEX.md"}`, &pinned)
	if pinned.N != 2 {
		t.Fatalf("second note: %+v", pinned)
	}
	if status := f.postAs(t, token, "note", `{"text":"Preview API: columns _mean, _variance","replaces":1}`, &pinned); status != 200 ||
		pinned.N != 3 || pinned.Replaces != 1 || len(pinned.Current) != 2 || pinned.Current[0].N != 2 || pinned.Current[1].N != 3 {
		t.Fatalf("replace: %d %+v", status, pinned)
	}
	if got := f.one(t, `SELECT text || '|' || (replaced_at IS NOT NULL) FROM epic_notes WHERE n = 1`); got != "Preview API: POST /v1/metrics/preview/history|true" {
		t.Errorf("note 1 after its replacement: %s", got)
	}
	if status, out := f.post(t, token, "note", `{"text":"again","replaces":1}`); status != 422 ||
		!strings.Contains(out["error"].(string), "note 1 was replaced by note 3") {
		t.Errorf("replacing a replaced note: %d %v", status, out)
	}
	if status, out := f.post(t, token, "note", `{"text":"x","replaces":9}`); status != 422 || !strings.Contains(out["error"].(string), "no note 9") {
		t.Errorf("replacing no note: %d %v", status, out)
	}
	// There is no retire for an agent: not a tool, not an argument.
	if status, _ := f.post(t, token, "retire_note", `{"n":2}`); status != 404 {
		t.Errorf("an agent retired a note: %d", status)
	}
	if status, _ := f.post(t, token, "note", `{"n":2,"retire":true}`); status != 422 {
		t.Errorf("note took a retire: %d", status)
	}
	if n := f.n(t, `SELECT count(*) FROM events WHERE run_id = 'run_me' AND event_type = 'epic.note.pinned'`); n != 3 {
		t.Errorf("%d epic.note.pinned events", n)
	}
	// A task on no epic has nowhere to pin.
	alone := f.runOn(t, "run_alone", "wi_5"+f.org, "implement", "implementer", "running")
	mustExec(t, f.owner, `UPDATE tasks SET epic_id = NULL WHERE id = $1`, "wi_5"+f.org)
	if status, out := f.post(t, alone, "note", `{"text":"x"}`); status != 422 || !strings.Contains(out["error"].(string), "on no epic") {
		t.Errorf("a note off an epic: %d %v", status, out)
	}
}

// At 10 current notes the 11th is refused with the doc's words, recorded;
// a replace at 10 is allowed.
func TestTheEleventhNoteIsRefusedAndAReplaceAtTenIsAllowed(t *testing.T) {
	f := setup(t)
	f.onEpic(t)
	token := f.runOn(t, "run_me", f.item, "implement", "implementer", "running")
	for i := range 10 {
		if status, out := f.post(t, token, "note", `{"text":"fact `+string(rune('a'+i))+`"}`); status != 200 {
			t.Fatalf("note %d: %d %v", i+1, status, out)
		}
	}
	status, out := f.post(t, token, "note", `{"text":"the eleventh"}`)
	if status != 422 || out["error"] != "This epic has 10 notes. Replace one (`dude note --replaces N`) instead." {
		t.Fatalf("the 11th: %d %v", status, out)
	}
	if n := f.n(t, `SELECT count(*) FROM events WHERE run_id = 'run_me' AND event_type = 'epic.note.refused'
		AND payload->>'reason' = 'limit' AND (payload->>'current')::int = 10`); n != 1 {
		t.Errorf("%d epic.note.refused events", n)
	}
	if status, out := f.post(t, token, "note", `{"text":"fact i, corrected","replaces":9}`); status != 200 {
		t.Errorf("a replace at 10: %d %v", status, out)
	}
	if n := f.n(t, `SELECT count(*) FROM epic_notes WHERE replaced_at IS NULL AND retired_at IS NULL`); n != 10 {
		t.Errorf("%d current notes", n)
	}
}

// Two agents pinning at once at 9 notes make 10, never 11: the epic's row
// is locked for the count.
func TestConcurrentNotesAtNineMakeTen(t *testing.T) {
	f := setup(t)
	ids := f.onEpic(t)
	a := f.runOn(t, "run_a", f.item, "implement", "implementer", "running")
	b := f.runOn(t, "run_b", ids["TEXT-2"], "implement", "implementer", "running")
	for i := range 9 {
		if status, _ := f.post(t, a, "note", `{"text":"fact `+string(rune('a'+i))+`"}`); status != 200 {
			t.Fatal("seeding")
		}
	}
	var wg sync.WaitGroup
	statuses := make([]int, 6)
	for i := range statuses {
		wg.Add(1)
		go func() {
			defer wg.Done()
			token := a
			if i%2 == 1 {
				token = b
			}
			statuses[i], _ = f.post(t, token, "note", `{"text":"racing"}`)
		}()
	}
	wg.Wait()
	ok := 0
	for _, s := range statuses {
		if s == 200 {
			ok++
		}
	}
	if n := f.n(t, `SELECT count(*) FROM epic_notes WHERE replaced_at IS NULL AND retired_at IS NULL`); n != 10 || ok != 1 {
		t.Errorf("%d current notes after %d accepted (%v)", n, ok, statuses)
	}
}

// Every live phase Run on the epic but the pinner gets one directive per
// change, read at its next step; nobody off the epic, no conductor, and no
// ended Run.
func TestEveryLiveRunOnTheEpicIsToldOfEachNoteChange(t *testing.T) {
	f := setup(t)
	ids := f.onEpic(t)
	me := f.runOn(t, "run_me", f.item, "implement", "implementer", "running")
	f.runOn(t, "run_two", ids["TEXT-2"], "implement", "implementer", "running")
	f.runOn(t, "run_two_rev", ids["TEXT-2"], "review", "reviewer", "paused")
	f.runOn(t, "run_three_done", ids["TEXT-3"], "implement", "implementer", "completed")
	f.runOn(t, "run_five", ids["TEXT-5"], "implement", "implementer", "running")
	mustExec(t, f.owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, role, kind)
		VALUES ('run_two_cond', $1, $2, $3, 1, 'running', 'conductor', 'agent')`, f.org, f.project, ids["TEXT-2"])
	var pinned struct{ Told int }
	f.postAs(t, me, "note", `{"text":"Body is {metric, from, period}"}`, &pinned)
	f.postAs(t, me, "note", `{"text":"Body is {metric, from, period, sample}","replaces":1}`, &pinned)
	if pinned.Told != 2 {
		t.Errorf("told %d agents, want 2", pinned.Told)
	}
	rows, err := f.owner.Query(context.Background(), `SELECT d.run_id, d.text, d.interrupt FROM directives d ORDER BY d.created_at`)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][]string{}
	for rows.Next() {
		var run, text string
		var interrupt bool
		_ = rows.Scan(&run, &text, &interrupt)
		if interrupt {
			t.Errorf("a note interrupted %s", run)
		}
		got[run] = append(got[run], text)
	}
	if len(got) != 2 || len(got["run_two"]) != 2 || len(got["run_two_rev"]) != 2 {
		t.Fatalf("directives: %v", got)
	}
	if want := "Epic note added: 1 · TEXT-1's implementer · Body is {metric, from, period}"; got["run_two"][0] != want {
		t.Errorf("added: %q", got["run_two"][0])
	}
	if !strings.HasPrefix(got["run_two"][1], "Epic note replaced: 1 → 2 · TEXT-1's implementer · Body is {metric, from, period, sample}") ||
		!strings.Contains(got["run_two"][1], "(was: Body is {metric, from, period})") {
		t.Errorf("replaced: %q", got["run_two"][1])
	}
	if n := f.n(t, `SELECT count(*) FROM epic_note_reads WHERE directive_id IS NOT NULL`); n != 4 {
		t.Errorf("%d note reads recorded", n)
	}
}
