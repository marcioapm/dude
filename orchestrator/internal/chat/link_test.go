package chat_test

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/chat"
	"github.com/marciomartins/dude/orchestrator/internal/fakeslack"
)

// linkWorld is setup with three more projects (ABS with two repositories,
// SDK, OPS) beside BIL (one repository), and the RunStarted wake counted.
type linkWorld struct {
	*world
	abs, sdk, ops string
	woken         int
}

func linkSetup(t *testing.T) *linkWorld {
	w := &linkWorld{world: setup(t)}
	w.abs, w.sdk, w.ops = "prj_abs_"+w.org, "prj_sdk_"+w.org, "prj_ops_"+w.org
	w.exec(`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ($1, $4, 'Acme Backend', 'abs', 'ABS'),
		($2, $4, 'Client SDK', 'sdk', 'SDK'), ($3, $4, 'Operations', 'ops', 'OPS')`, w.abs, w.sdk, w.ops, w.org)
	w.exec(`INSERT INTO repositories (id, organization_id, project_id, name, url) VALUES ('repo_abs_api_'||$1, $1, $2, 'api', 'https://github.com/acme/api'),
		('repo_abs_web_'||$1, $1, $2, 'web', 'https://github.com/acme/web'), ('repo_bil_'||$1, $1, $3, 'billing', 'https://github.com/acme/billing')`,
		w.org, w.abs, w.project)
	w.svc.RunStarted = func() { w.woken++ }
	return w
}

// earlier records a request of C1 that filed a task in project.
func (w *linkWorld) earlier(project string) {
	w.seq++
	id := fmt.Sprintf("%s_%d", w.org, w.seq)
	w.exec(`INSERT INTO tasks (id, organization_id, project_id, number, title) VALUES ('wi_'||$1, $2, $3, $4, 'Earlier')`, id, w.org, project, 100+w.seq)
	w.exec(`INSERT INTO chat_requests (id, organization_id, provider, event_id, external_workspace_id, conversation_id, external_user_id, route,
		result_kind, result_id) VALUES ('chr_'||$1, $2, 'slack', 'ev_'||$1, 'T1', 'C1', 'U2', 'task', 'task', 'wi_'||$1)`, id, w.org)
}

func (w *linkWorld) usual(projects ...string) {
	for _, p := range projects {
		w.exec(`INSERT INTO chat_channel_projects (organization_id, provider, external_workspace_id, conversation_id, project_id)
			VALUES ($1, 'slack', 'T1', 'C1', $2)`, w.org, p)
	}
}

// brainstorm routes a top-level mention by name to a session with no
// project, the model naming maybe, and returns the reply and its session.
func (w *linkWorld) brainstorm(name string, maybe ...string) (fakeslack.Message, string) {
	w.t.Helper()
	w.model.queue(answer(map[string]any{"route": "brainstorm", "confidence": 0.5, "reasoning": "can't tell the project",
		"title": "Nightly failures", "maybe_projects": maybe}))
	w.mention(name, "C1", "", "can you investigate the failures above?")
	got := w.requests()
	return lastBot(w.t, w.fake), got[len(got)-1].ResultID
}

type button struct{ Label, Value, URL string }

// row is the buttons of a message's "Link a project" row.
func row(t *testing.T, blocks json.RawMessage) []button {
	t.Helper()
	var bs []struct {
		Type     string `json:"type"`
		BlockID  string `json:"block_id"`
		Elements []struct {
			Text  any    `json:"text"`
			Value string `json:"value"`
			URL   string `json:"url"`
		} `json:"elements"`
	}
	if err := json.Unmarshal(blocks, &bs); err != nil {
		t.Fatal(err)
	}
	var out []button
	for _, b := range bs {
		if b.BlockID != "row" {
			continue
		}
		for _, e := range b.Elements {
			label, _ := e.Text.(map[string]any)["text"].(string)
			out = append(out, button{label, e.Value, e.URL})
		}
	}
	return out
}

func labels(bs []button) string {
	var out []string
	for _, b := range bs {
		out = append(out, b.Label)
	}
	return strings.Join(out, ", ")
}

// clickAs posts user's click (a Slack user id) on value in message msg.
func (w *world) clickAs(user, channel, msg, thread, value string) {
	w.t.Helper()
	raw, _ := json.Marshal(fakeslack.Click("T1", channel, msg, thread, user, value))
	body := []byte(url.Values{"payload": {string(raw)}}.Encode())
	req, _ := fakeslack.SignedRequest("http://x/", signingSecret, "application/x-www-form-urlencoded", body, time.Now())
	if _, err := w.svc.Receive(w.ctx, "slack", true, req.Header, body); err != nil {
		w.t.Fatal(err)
	}
	w.sweep()
}

func (w *linkWorld) linked(session string) string {
	return w.one(`SELECT string_agg(p.key_prefix, ',' ORDER BY p.key_prefix) FROM session_projects sp JOIN projects p ON p.id = sp.project_id
		WHERE sp.session_id = $1`, session)
}

func (w *linkWorld) repos(session string) string {
	return w.one(`SELECT string_agg(r.name, ',' ORDER BY r.name) FROM session_repositories sr JOIN repositories r ON r.id = sr.repository_id
		WHERE sr.session_id = $1`, session)
}

func TestChatALinkOffersTheChannelsProjectsThenTheLearnedThenTheModelsOnceEach(t *testing.T) {
	w := linkSetup(t)
	w.usual(w.project)
	// Learned: SDK twice, BIL once (also usual), OPS once.
	w.earlier(w.sdk)
	w.earlier(w.sdk)
	w.earlier(w.project)
	w.earlier(w.ops)
	reply, session := w.brainstorm("Márcia", "BIL", "ABS")
	got := row(t, reply.Blocks)
	if l := labels(got); l != "BIL · Billing, SDK · Client SDK, OPS · Operations, Choose in dude" {
		t.Fatalf("row %q", l)
	}
	if got[3].URL != "https://dude.example.com/#/o/acme/sessions/"+session || got[3].Value != "" {
		t.Fatalf("Choose in dude %+v", got[3])
	}
	if !strings.Contains(string(reply.Blocks), "No project is linked") {
		t.Fatalf("blocks %s", reply.Blocks)
	}
	// The model's own guesses fill what the hints leave, in its order.
	w2 := linkSetup(t)
	reply, _ = w2.brainstorm("Márcia", "SDK", "ABS", "BIL", "OPS")
	if l := labels(row(t, reply.Blocks)); l != "SDK · Client SDK, ABS · Acme Backend, BIL · Billing, Choose in dude" {
		t.Fatalf("row %q", l)
	}
}

func TestChatALinkNeverOffersAHiddenProjectAndWithoutCandidatesOnlyTheSessionPage(t *testing.T) {
	w := linkSetup(t)
	w.usual(w.abs)
	w.earlier(w.abs)
	w.exec(`INSERT INTO chat_hidden_projects (organization_id, provider, project_id) VALUES ($1, 'slack', $2)`, w.org, w.abs)
	// An unknown key and a hidden one from the model are dropped as well.
	reply, _ := w.brainstorm("Márcia", "ABS", "NOPE")
	if l := labels(row(t, reply.Blocks)); l != "Choose in dude" {
		t.Fatalf("row %q", l)
	}
	if strings.Contains(string(reply.Blocks), "ABS") {
		t.Fatalf("the hidden project is offered: %s", reply.Blocks)
	}
}

func TestChatASessionWithAProjectOffersNoLink(t *testing.T) {
	w := linkSetup(t)
	w.usual(w.abs)
	w.model.queue(answer(map[string]any{"route": "brainstorm", "confidence": 0.9, "reasoning": "billing", "title": "Exports",
		"project": "BIL", "maybe_projects": []string{"ABS"}}))
	w.mention("Márcia", "C1", "", "the exports are slow")
	if bs := row(t, lastBot(t, w.fake).Blocks); len(bs) != 0 {
		t.Fatalf("row %+v", bs)
	}
	if n := w.count(`SELECT count(*) FROM chat_requests WHERE organization_id = $1 AND link_offer IS NOT NULL`, w.org); n != 0 {
		t.Fatalf("%d offers recorded", n)
	}
}

func TestChatTheAskersClickLinksTheProjectWithItsRepositories(t *testing.T) {
	w := linkSetup(t)
	w.usual(w.abs, w.project)
	reply, session := w.brainstorm("Eli")
	bs := row(t, reply.Blocks)
	woken := w.woken
	w.click("Eli", "C1", reply.TS, reply.ThreadTS, bs[0].Value)
	if l := w.linked(session); l != "ABS" {
		t.Fatalf("linked %q", l)
	}
	if r := w.repos(session); r != "api,web" {
		t.Fatalf("repositories %q", r)
	}
	// The message says who linked what, and offers what is left.
	u := w.fake.Updates()
	if len(u) != 1 || u[0].TS != reply.TS || !strings.Contains(string(u[0].Blocks), "Eli linked ABS · Acme Backend") {
		t.Fatalf("updates %+v", u)
	}
	if l := labels(row(t, u[0].Blocks)); l != "BIL · Billing, Choose in dude" {
		t.Fatalf("row after %q", l)
	}
	// The session has its line, as a member's link records it.
	var payload map[string]any
	raw := w.one(`SELECT payload::text FROM events WHERE session_id = $1 AND event_type = 'session.linked'`, session)
	_ = json.Unmarshal([]byte(raw), &payload)
	if payload["linked"] != "ABS" || payload["viaName"] != "Slack" ||
		w.one(`SELECT actor_id FROM events WHERE session_id = $1 AND event_type = 'session.linked'`, session) != w.people["Eli"][0] {
		t.Fatalf("session.linked %s", raw)
	}
	if w.woken == woken {
		t.Fatal("the session's agent was not woken for its new repositories")
	}
	// Requests records it on the request.
	if l := w.one(`SELECT string_agg(l.project_key || ' by ' || pe.name, ',') FROM chat_request_links l JOIN people pe ON pe.id = l.linked_by
		JOIN chat_requests r ON r.id = l.request_id WHERE r.result_id = $1`, session); l != "ABS by Eli" {
		t.Fatalf("recorded %q", l)
	}
	// A second candidate is linked beside the first.
	w.click("Eli", "C1", reply.TS, reply.ThreadTS, bs[1].Value)
	if l := w.linked(session); l != "ABS,BIL" {
		t.Fatalf("linked %q", l)
	}
	if r := w.repos(session); r != "api,billing,web" {
		t.Fatalf("repositories %q", r)
	}
	u = w.fake.Updates()
	if len(u) != 2 || !strings.Contains(string(u[1].Blocks), "Eli linked BIL · Billing") || !strings.Contains(string(u[1].Blocks), "Eli linked ABS") {
		t.Fatalf("updates %+v", u)
	}
	if l := labels(row(t, u[1].Blocks)); l != "Choose in dude" {
		t.Fatalf("row after both %q", l)
	}
	// The mention's marks are the session's, as before: 👀 until its agent answers.
	if r := w.fake.Reactions("C1", reply.ThreadTS); strings.Join(r, ",") != "eyes" {
		t.Fatalf("reactions %v", r)
	}
}

func TestChatAnAdminMayLinkForTheAskerAndNobodyElse(t *testing.T) {
	w := linkSetup(t)
	w.usual(w.abs)
	reply, session := w.brainstorm("Eli")
	value := row(t, reply.Blocks)[0].Value
	// Ana's account left Acme; Zed (U9) never connected; Gus (U3) is not connected.
	for _, user := range []string{w.people["Ana"][1], "U9", "U3"} {
		w.clickAs(user, "C1", reply.TS, reply.ThreadTS, value)
	}
	if l := w.linked(session); l != "" || len(w.fake.Updates()) != 0 {
		t.Fatalf("linked %q by someone else", l)
	}
	eph := w.fake.Ephemerals()
	if len(eph) != 3 || eph[2].User != "U3" {
		t.Fatalf("ephemerals %+v", eph)
	}
	for _, e := range eph {
		if e.Text != "Only Eli or an admin can link a project here." {
			t.Fatalf("ephemeral %+v", e)
		}
	}
	w.click("Márcia", "C1", reply.TS, reply.ThreadTS, value)
	if l := w.linked(session); l != "ABS" {
		t.Fatalf("the admin's click linked %q", l)
	}
	if u := w.fake.Updates(); len(u) != 1 || !strings.Contains(string(u[0].Blocks), "Márcia linked ABS") {
		t.Fatalf("updates %+v", u)
	}
}

func TestChatAnotherMembersClickChangesNothing(t *testing.T) {
	w := linkSetup(t)
	w.usual(w.abs)
	reply, session := w.brainstorm("Márcia")
	w.click("Eli", "C1", reply.TS, reply.ThreadTS, row(t, reply.Blocks)[0].Value)
	if l := w.linked(session); l != "" || len(w.fake.Updates()) != 0 ||
		w.count(`SELECT count(*) FROM chat_request_links WHERE organization_id = $1`, w.org) != 0 {
		t.Fatalf("Eli linked %q", l)
	}
	if eph := w.fake.Ephemerals(); len(eph) != 1 || eph[0].User != "U2" || eph[0].Text != "Only Márcia or an admin can link a project here." {
		t.Fatalf("ephemerals %+v", eph)
	}
}

func TestChatAnAskerWhoLeftCannotLink(t *testing.T) {
	w := linkSetup(t)
	w.usual(w.abs)
	reply, session := w.brainstorm("Eli")
	w.exec(`UPDATE people SET removed_at = now() WHERE id = $1`, w.people["Eli"][0])
	w.click("Eli", "C1", reply.TS, reply.ThreadTS, row(t, reply.Blocks)[0].Value)
	if l := w.linked(session); l != "" || len(w.fake.Updates()) != 0 {
		t.Fatalf("linked %q by someone no longer in Acme", l)
	}
	if eph := w.fake.Ephemerals(); len(eph) != 1 || eph[0].User != "U2" || eph[0].Text != "Only Eli or an admin can link a project here." {
		t.Fatalf("ephemerals %+v", eph)
	}
	if n := w.count(`SELECT count(*) FROM chat_events WHERE organization_id = $1 AND kind = 'interaction' AND attempts > 1`, w.org); n != 0 {
		t.Fatalf("%d clicks were tried again", n)
	}
}

func TestChatAnAskerWhoHandedTheSessionOverCannotLink(t *testing.T) {
	w := linkSetup(t)
	w.usual(w.abs)
	reply, session := w.brainstorm("Eli")
	// Eli hands it to Gus, keeping chat; then leaves it.
	gus := w.people["Gus"][0]
	// One owner at a time, checked at commit.
	w.exec(`BEGIN`)
	w.exec(`UPDATE session_people SET role = 'chat' WHERE session_id = $1 AND person_id = $2`, session, w.people["Eli"][0])
	w.exec(`INSERT INTO session_people (session_id, person_id, organization_id, role, accepted_at) VALUES ($1, $2, $3, 'owner', now())`,
		session, gus, w.org)
	w.exec(`COMMIT`)
	value := row(t, reply.Blocks)[0].Value
	w.click("Eli", "C1", reply.TS, reply.ThreadTS, value)
	w.exec(`DELETE FROM session_people WHERE session_id = $1 AND person_id = $2`, session, w.people["Eli"][0])
	w.click("Eli", "C1", reply.TS, reply.ThreadTS, value)
	if l := w.linked(session); l != "" || len(w.fake.Updates()) != 0 ||
		w.count(`SELECT count(*) FROM chat_request_links WHERE organization_id = $1`, w.org) != 0 {
		t.Fatalf("linked %q by someone who no longer owns the session", l)
	}
	eph := w.fake.Ephemerals()
	if len(eph) != 2 {
		t.Fatalf("ephemerals %+v", eph)
	}
	for _, e := range eph {
		if e.User != "U2" || e.Text != "Only Eli or an admin can link a project here." {
			t.Fatalf("ephemeral %+v", e)
		}
	}
	// An admin still links for the asker.
	w.click("Márcia", "C1", reply.TS, reply.ThreadTS, value)
	if l := w.linked(session); l != "ABS" {
		t.Fatalf("the admin's click linked %q", l)
	}
}

func TestChatASecondClickOrARetryLinksAndPostsOnce(t *testing.T) {
	w := linkSetup(t)
	w.usual(w.abs)
	reply, session := w.brainstorm("Eli")
	value := row(t, reply.Blocks)[0].Value
	// The first update is rate limited: the click's delivery is tried again
	// and posts it then.
	w.fake.FailNext("chat.update", "ratelimited")
	w.click("Eli", "C1", reply.TS, reply.ThreadTS, value)
	if len(w.fake.Updates()) != 0 {
		t.Fatal("updated through a rate limit")
	}
	w.exec(`UPDATE chat_events SET next_attempt_at = now() WHERE organization_id = $1`, w.org)
	w.sweep()
	if u := w.fake.Updates(); len(u) != 1 {
		t.Fatalf("updates %+v", u)
	}
	// The same click handled again, and a second click: nothing twice.
	w.exec(`UPDATE chat_events SET processed_at = NULL, attempts = 0, next_attempt_at = NULL WHERE organization_id = $1 AND kind = 'interaction'`, w.org)
	w.sweep()
	w.click("Eli", "C1", reply.TS, reply.ThreadTS, value)
	if u := w.fake.Updates(); len(u) != 1 {
		t.Fatalf("updates %+v", u)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE session_id = $1 AND event_type = 'session.linked'`, session); n != 1 {
		t.Fatalf("%d session.linked", n)
	}
	if n := w.count(`SELECT count(*) FROM session_projects WHERE session_id = $1`, session); n != 1 {
		t.Fatalf("%d links", n)
	}
	if n := w.count(`SELECT count(*) FROM chat_request_links WHERE organization_id = $1`, w.org); n != 1 {
		t.Fatalf("%d recorded", n)
	}
}

func TestChatAForgedLinkValueLinksNothing(t *testing.T) {
	w := linkSetup(t)
	w.usual(w.abs)
	reply, session := w.brainstorm("Eli")
	value := row(t, reply.Blocks)[0].Value
	request := strings.Split(value, ":")[1]
	// A clarifying question's request offers nothing to link.
	w.model.queue(answer(map[string]any{"route": "task", "confidence": 0.5, "project": "BIL", "title": "x"}))
	w.mention("Eli", "C1", "", "hmm")
	clarify := w.one(`SELECT id FROM chat_requests WHERE organization_id = $1 AND route = 'clarify'`, w.org)
	for _, forged := range []string{"link:" + request + ":3", "link:" + request + ":-1", "link:" + request + ":x", "link:" + request,
		"link:chr_nope:0", "link:" + clarify + ":0", "link:" + w.sdk + ":0"} {
		w.click("Eli", "C1", reply.TS, reply.ThreadTS, forged)
	}
	if l := w.linked(session); l != "" || len(w.fake.Updates()) != 0 {
		t.Fatalf("a forged value linked %q", l)
	}
	if eph := w.fake.Ephemerals(); len(eph) != 7 || eph[0].Text != "That isn't a project dude offered to link here." {
		t.Fatalf("ephemerals %+v", eph)
	}
	// From another conversation the offer's value is not this one's.
	w.click("Eli", "G1", reply.TS, reply.ThreadTS, value)
	if l := w.linked(session); l != "" {
		t.Fatalf("linked %q from another conversation", l)
	}
}

func TestChatAProjectHiddenAfterTheOfferIsRefusedAtTheClick(t *testing.T) {
	w := linkSetup(t)
	w.usual(w.abs)
	reply, session := w.brainstorm("Eli")
	value := row(t, reply.Blocks)[0].Value
	w.exec(`INSERT INTO chat_hidden_projects (organization_id, provider, project_id) VALUES ($1, 'slack', $2)`, w.org, w.abs)
	w.click("Eli", "C1", reply.TS, reply.ThreadTS, value)
	if l := w.linked(session); l != "" || w.repos(session) != "" || len(w.fake.Updates()) != 0 {
		t.Fatalf("linked %q", l)
	}
	if eph := w.fake.Ephemerals(); len(eph) != 1 || eph[0].Text != "An admin hid ABS from Slack: it can't be linked to this session." {
		t.Fatalf("ephemerals %+v", eph)
	}
	if n := w.count(`SELECT count(*) FROM chat_request_links WHERE organization_id = $1`, w.org); n != 0 {
		t.Fatalf("%d recorded", n)
	}
}

// linkedOnce is what a click that linked ABS leaves however its update
// fared: the one link, no failed request and no "Sorry".
func (w *linkWorld) linkedOnce(session string) {
	w.t.Helper()
	if l := w.linked(session); l != "ABS" {
		w.t.Fatalf("linked %q", l)
	}
	if n := w.count(`SELECT count(*) FROM chat_request_links WHERE organization_id = $1`, w.org); n != 1 {
		w.t.Fatalf("%d recorded", n)
	}
	for _, r := range w.requests() {
		if r.Route == "failed" {
			w.t.Fatalf("requests %+v", w.requests())
		}
	}
	if n := w.count(`SELECT count(*) FROM chat_requests WHERE organization_id = $1`, w.org); n != 1 {
		w.t.Fatalf("%d requests: %+v", n, w.requests())
	}
	for _, e := range w.fake.Ephemerals() {
		if strings.Contains(e.Text, "Sorry") {
			w.t.Fatalf("ephemerals %+v", w.fake.Ephemerals())
		}
	}
	if n := w.count(`SELECT count(*) FROM chat_events WHERE organization_id = $1 AND kind = 'interaction' AND processed_at IS NULL`, w.org); n != 0 {
		w.t.Fatalf("%d clicks pending", n)
	}
}

func TestChatAReplyRefusedInTheThreadGoesPrivatelyWithoutItsRow(t *testing.T) {
	w := linkSetup(t)
	w.usual(w.abs)
	w.fake.FailNext("chat.postMessage", "msg_too_long")
	w.model.queue(answer(map[string]any{"route": "brainstorm", "confidence": 0.5, "reasoning": "can't tell the project", "title": "Nightly failures"}))
	w.mention("Eli", "C1", "", "can you investigate the failures above?")
	eph := w.fake.Ephemerals()
	if len(eph) != 1 || !strings.Contains(eph[0].Text, "did not take dude's reply") {
		t.Fatalf("ephemerals %+v", eph)
	}
	if bs := row(t, eph[0].Blocks); len(bs) != 0 {
		t.Fatalf("the private reply carries the row %+v", bs)
	}
	// Its own link still reaches the session page.
	session := w.requests()[0].ResultID
	if !strings.Contains(string(eph[0].Blocks), "https://dude.example.com/#/o/acme/sessions/"+session) {
		t.Fatalf("blocks %s", eph[0].Blocks)
	}
}

func TestChatAClickOnAnOfferWithNoMessageLinksOnceAndFailsNothing(t *testing.T) {
	w := linkSetup(t)
	w.usual(w.abs)
	reply, session := w.brainstorm("Eli")
	value := row(t, reply.Blocks)[0].Value
	// The reply refused in the thread (as a fallback ephemeral would have it):
	// replied, with no message id.
	w.exec(`UPDATE chat_requests SET question_message_id = NULL WHERE organization_id = $1`, w.org)
	w.click("Eli", "C1", reply.TS, reply.ThreadTS, value)
	w.linkedOnce(session)
	if n := w.count(`SELECT count(*) FROM chat_events WHERE organization_id = $1 AND kind = 'interaction' AND attempts > 0`, w.org); n != 0 {
		t.Fatalf("%d clicks were tried again", n)
	}
	if len(w.fake.Updates()) != 0 {
		t.Fatalf("updates %+v", w.fake.Updates())
	}
}

func TestChatAnUpdateFailingPastItsRetriesLeavesTheLinkAndFailsNothing(t *testing.T) {
	w := linkSetup(t)
	w.usual(w.abs)
	reply, session := w.brainstorm("Eli")
	w.fake.FailTimes("chat.update", "ratelimited", 100)
	w.click("Eli", "C1", reply.TS, reply.ThreadTS, row(t, reply.Blocks)[0].Value)
	for range chat.MaxAttempts {
		w.due()
		w.sweep()
	}
	w.linkedOnce(session)
	if n := w.count(`SELECT count(*) FROM chat_events WHERE organization_id = $1 AND kind = 'interaction' AND attempts = $2 - 1`,
		w.org, chat.MaxAttempts); n != 1 {
		t.Fatal("the update was not tried MaxAttempts times")
	}
}

func TestChatAClickBeforeTheReplysIdIsKeptUpdatesItOnceItIs(t *testing.T) {
	w := linkSetup(t)
	w.usual(w.abs)
	reply, session := w.brainstorm("Eli")
	// The reply posted, its id and outcome not written yet.
	w.exec(`UPDATE chat_requests SET question_message_id = NULL, replied_at = NULL WHERE organization_id = $1`, w.org)
	w.click("Eli", "C1", reply.TS, reply.ThreadTS, row(t, reply.Blocks)[0].Value)
	if l := w.linked(session); l != "ABS" || len(w.fake.Updates()) != 0 {
		t.Fatalf("linked %q, updates %+v", l, w.fake.Updates())
	}
	w.exec(`UPDATE chat_requests SET question_message_id = $2, replied_at = now() WHERE organization_id = $1`, w.org, reply.TS)
	w.due()
	w.sweep()
	if u := w.fake.Updates(); len(u) != 1 || u[0].TS != reply.TS || !strings.Contains(string(u[0].Blocks), "Eli linked ABS") {
		t.Fatalf("updates %+v", u)
	}
}
