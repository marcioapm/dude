package chat_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/chat"
	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/fakeslack"
)

func revoked(eventID string, bots, users []string) map[string]any {
	tokens := map[string]any{}
	if bots != nil {
		tokens["bot"] = bots
	}
	if users != nil {
		tokens["oauth"] = users
	}
	return map[string]any{"type": "event_callback", "team_id": "T1", "event_id": eventID,
		"event": map[string]any{"type": "tokens_revoked", "tokens": tokens}}
}

// A person's sign-in revoked unlinks that person; only the bot's token
// revoked ends the installation.
func TestChatTokensRevokedUnlinksAPersonOrUninstalls(t *testing.T) {
	w := setup(t)
	w.deliver(revoked("EvR1", nil, []string{"U2"}))
	if n := w.count(`SELECT count(*) FROM chat_installations WHERE organization_id = $1`, w.org); n != 1 {
		t.Fatal("a person's token revoked uninstalled the workspace")
	}
	if n := w.count(`SELECT count(*) FROM chat_identities WHERE external_user_id = 'U2'`); n != 0 {
		t.Fatal("Eli is still linked")
	}
	if n := w.count(`SELECT count(*) FROM chat_identities WHERE external_user_id = 'U1'`); n != 1 {
		t.Fatal("Márcia was unlinked too")
	}
	if n := w.count(`SELECT count(*) FROM events WHERE organization_id = $1 AND event_type = 'chat_identity.unlinked'
		AND payload->>'personId' = $2 AND payload->>'reason' = 'tokens_revoked'`, w.org, w.people["Eli"][0]); n != 1 {
		t.Fatalf("%d unlink events", n)
	}
	w.deliver(revoked("EvR2", []string{"UBT1"}, nil))
	if n := w.count(`SELECT count(*) FROM chat_installations WHERE organization_id = $1`, w.org); n != 0 {
		t.Fatal("the bot's token revoked left the installation")
	}
}

// Disconnecting removes the workspace's links, Connect states and thread
// bindings (what was made keeps its origin), so another organisation can
// install it and link the same people.
func TestChatDisconnectFreesTheWorkspaceForAnotherOrganisation(t *testing.T) {
	w := setup(t)
	w.model.queue(answer(map[string]any{"route": "brainstorm", "confidence": 0.9, "title": "Talk"}))
	root := w.mention("Márcia", "C1", "", "talk")
	session := w.last().ResultID
	w.mention("Zed", "C1", "", "help") // a pending Connect state
	if err := w.svc.Disconnect(w.ctx, w.org, "slack", w.people["Márcia"][0], true); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"chat_identities", "chat_threads", "chat_connect_states", "chat_installations"} {
		if n := w.count(`SELECT count(*) FROM ` + table); n != 0 {
			t.Fatalf("%d %s left", n, table)
		}
	}
	if o := w.one(`SELECT origin->>'threadId' FROM sessions WHERE id = $1`, session); o != root {
		t.Fatalf("the session lost its origin: %q", o)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE organization_id = $1 AND event_type = 'chat_provider.removed'
		AND actor_id = $2 AND payload->>'reason' = 'disconnected'`, w.org, w.people["Márcia"][0]); n != 1 {
		t.Fatalf("%d removal events", n)
	}

	// Another organisation installs T1 and links Eli's Slack account.
	other := dbtest.Org(t, w.owner)
	admin := "per_o_" + other
	w.exec(`INSERT INTO accounts (id, email, name) VALUES ($1, 'o@example.com', 'Other admin')`, "acc_o_"+other)
	w.exec(`INSERT INTO people (id, organization_id, name, role, account_id) VALUES ($1, $2, 'Other admin', 'admin', $3)`, admin, other, "acc_o_"+other)
	redirect := "https://dude.example.com/v1/chat/slack/install/callback"
	w.fake.AuthorizeAs("U1", "U2")
	start, err := w.svc.BeginInstall(w.ctx, other, chat.Begin{Provider: "slack", Person: admin, BrowserHash: "o", Redirect: redirect, Admin: true})
	if err != nil {
		t.Fatal(err)
	}
	back := follow(t, start)
	if l, err := w.svc.CompleteInstall(w.ctx, chat.Callback{Provider: "slack", State: back.Get("state"), Code: back.Get("code"), BrowserHash: "o"}); err != nil ||
		l.Outcome != "installed" {
		t.Fatalf("install elsewhere: %+v, %v", l, err)
	}
	start, err = w.svc.BeginConnect(w.ctx, other, chat.Begin{Provider: "slack", Person: admin, BrowserHash: "o",
		Redirect: "https://dude.example.com/v1/chat/slack/connect/callback"})
	if err != nil {
		t.Fatal(err)
	}
	back = follow(t, start)
	if l, err := w.svc.CompleteConnect(w.ctx, chat.Callback{Provider: "slack", State: back.Get("state"), Code: back.Get("code"), BrowserHash: "o"}); err != nil ||
		l.Outcome != "connected" {
		t.Fatalf("link elsewhere: %+v, %v", l, err)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE organization_id = $1 AND event_type IN ('chat_provider.installed', 'chat_identity.linked')`, other); n != 2 {
		t.Fatalf("%d install and link events", n)
	}
	if err := w.svc.Unlink(w.ctx, other, "slack", admin); err != nil {
		t.Fatal(err)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE organization_id = $1 AND event_type = 'chat_identity.unlinked' AND actor_id = $2`, other, admin); n != 1 {
		t.Fatalf("%d unlink events", n)
	}
}

// The app's removal also takes the workspace's links and bindings.
func TestChatTheAppsRemovalTakesItsLinksAndBindings(t *testing.T) {
	w := setup(t)
	w.model.queue(answer(map[string]any{"route": "brainstorm", "confidence": 0.9, "title": "Talk"}))
	w.mention("Márcia", "C1", "", "talk")
	w.deliver(map[string]any{"type": "event_callback", "team_id": "T1", "event_id": "EvGone2", "event": map[string]any{"type": "app_uninstalled"}})
	for _, table := range []string{"chat_identities", "chat_threads", "chat_installations"} {
		if n := w.count(`SELECT count(*) FROM ` + table); n != 0 {
			t.Fatalf("%d %s left", n, table)
		}
	}
	if n := w.count(`SELECT count(*) FROM events WHERE organization_id = $1 AND event_type = 'chat_provider.removed'
		AND actor_type = 'system' AND payload->>'reason' = 'uninstalled'`, w.org); n != 1 {
		t.Fatalf("%d removal events", n)
	}
}

// The Connect link a non-member is sent names no organisation.
func TestChatTheConnectPromptNamesNoOrganisation(t *testing.T) {
	w := setup(t)
	w.mention("Zed", "C1", "", "who are you")
	eph := w.fake.Ephemerals()
	if len(eph) != 1 {
		t.Fatalf("ephemerals %+v", eph)
	}
	all := eph[0].Text + string(eph[0].Blocks)
	if strings.Contains(all, "acme") || strings.Contains(all, "Acme") || !strings.Contains(all, "https://dude.example.com/#/connect/slack?state=") {
		t.Fatalf("prompt %s", all)
	}
}

// users.info is asked once per person per 10 minutes, and not for
// connected members, whose names dude has.
func TestChatReadsNamesOnceAndOnlyForStrangers(t *testing.T) {
	w := setup(t)
	var members []string
	for i := range 30 {
		id := fmt.Sprintf("UX%02d", i)
		w.fake.AddUser(fakeslack.User{ID: id, Team: "T1", Name: id, RealName: "Guest " + id})
		members = append(members, id)
	}
	w.fake.SetMembers("G1", append([]string{"U1", "U2", "UBT1"}, members...))
	w.model.queue(answer(map[string]any{"route": "brainstorm", "confidence": 0.9, "title": "a"}),
		answer(map[string]any{"route": "brainstorm", "confidence": 0.9, "title": "b"}))
	w.mention("Márcia", "G1", "", "one")
	first := w.fake.Calls("users.info")
	if first != 30 {
		t.Fatalf("users.info %d times for 30 strangers and 2 connected members", first)
	}
	if o := w.one(`SELECT origin->'routing'->'skippedNames'->>0 FROM sessions WHERE id = $1`, w.last().ResultID); o != "Guest UX00" {
		t.Fatalf("skipped name %q", o)
	}
	w.mention("Márcia", "G1", "", "two")
	if n := w.fake.Calls("users.info"); n != first {
		t.Fatalf("users.info %d more times within the TTL", n-first)
	}
}

// Conversation members are read only where a route needs them (an unbound
// private or group mention, which may resume or make a session there, and
// a mention in a session-bound thread of its own conversation), and
// display names only for a session made there: a task- or epic-bound
// thread, a DM and a public channel read neither.
func TestChatReadsMembersAndNamesOnlyForTheRoutesThatUseThem(t *testing.T) {
	w := setup(t)
	members := []string{"U1", "U2", "UBT1"}
	for i := range 200 {
		id := fmt.Sprintf("UG%03d", i)
		w.fake.AddUser(fakeslack.User{ID: id, Team: "T1", Name: id, RealName: "Guest " + id})
		members = append(members, id)
	}
	w.fake.SetMembers("G1", members)
	calls := func() [2]int { return [2]int{w.fake.Calls("conversations.members"), w.fake.Calls("users.info")} }
	step := func(name string, want [2]int, fn func()) {
		t.Helper()
		before := calls()
		fn()
		after := calls()
		if got := [2]int{after[0] - before[0], after[1] - before[1]}; got != want {
			t.Fatalf("%s: conversations.members, users.info = %v, want %v", name, got, want)
		}
	}
	task := func(title string) {
		w.model.queue(answer(map[string]any{"route": "task", "confidence": 0.9, "project": "BIL", "title": title, "goal": "g"}))
	}
	brainstorm := func(title string) {
		w.model.queue(answer(map[string]any{"route": "brainstorm", "confidence": 0.9, "title": title}))
	}
	var taskRoot, sessionRoot string
	step("a task in a private channel", [2]int{1, 0}, func() { task("One"); taskRoot = w.mention("Eli", "G1", "", "file one") })
	step("the task-bound thread", [2]int{0, 0}, func() { w.mention("Eli", "G1", taskRoot, "status?") })
	if got := w.last(); got.Route != "task_link" {
		t.Fatalf("not linked: %+v", got)
	}
	step("a DM", [2]int{0, 0}, func() { brainstorm("Mine"); w.dm("Márcia", "ideas") })
	step("a public channel", [2]int{0, 0}, func() { task("Two"); w.mention("Eli", "C1", "", "file two") })
	// A session made in G1 names its skipped members: the first 50, the same 50 each time.
	step("a session in a private channel", [2]int{1, 50}, func() { brainstorm("Talk"); sessionRoot = w.mention("Eli", "G1", "", "talk") })
	step("another session there", [2]int{1, 0}, func() { brainstorm("Talk more"); w.mention("Eli", "G1", "", "talk more") })
	step("the session-bound thread", [2]int{1, 0}, func() { w.mention("Eli", "G1", sessionRoot, "more") })
	if got := w.last(); got.Route != "resume" {
		t.Fatalf("not into the session: %+v", got)
	}
}

// A stored delivery wakes the chat loop (Kick); only a commit that started
// or resumed a session's run wakes the loops that run it (RunStarted).
func TestChatWakesOnlyTheLoopsThatHaveWork(t *testing.T) {
	w := setup(t)
	var kicks, runs int
	w.svc.Kick = func() { kicks++ }
	w.svc.RunStarted = func() { runs++ }
	w.model.queue(answer(map[string]any{"route": "task", "confidence": 0.9, "project": "BIL", "title": "T", "goal": "g"}))
	w.mention("Eli", "C1", "", "file it")
	if kicks != 1 || runs != 0 {
		t.Fatalf("a task: %d kicks, %d run wakes", kicks, runs)
	}
	w.model.queue(answer(map[string]any{"route": "brainstorm", "confidence": 0.9, "title": "S"}))
	root := w.mention("Eli", "C1", "", "talk")
	if kicks != 2 || runs != 1 {
		t.Fatalf("a session: %d kicks, %d run wakes", kicks, runs)
	}
	// A message into the thread's session wakes them too.
	w.mention("Eli", "C1", root, "and more")
	if got := w.last(); got.Route != "resume" || runs != 2 {
		t.Fatalf("into the bound session: %+v, %d run wakes", got, runs)
	}
}

// The routing context is bounded by size, and the model is told what was
// left out.
func TestChatRoutingContextSaysWhatItLeftOut(t *testing.T) {
	var projects []chat.Project
	for i := range 100 {
		var epics []chat.EpicRef
		for j := range 20 {
			epics = append(epics, chat.EpicRef{ID: fmt.Sprintf("e%d", j), Title: strings.Repeat("x", 40)})
		}
		projects = append(projects, chat.Project{ID: fmt.Sprint(i), Key: fmt.Sprintf("P%03d", i), Name: "Project", Epics: epics, EpicsTotal: 25})
	}
	in := chat.RoutingInput("Eli", "#support", "Slack", "hi", "", "", chat.World{Projects: projects, ProjectsTotal: 140, Kind: chat.KindPublic})
	if len(in) > 30000 {
		t.Fatalf("input %d characters", len(in))
	}
	if !strings.Contains(in, "(and 5 more not shown)") {
		t.Fatal("epics left out are not said")
	}
	shown := strings.Count(in, "\n- P")
	if want := fmt.Sprintf("- %d more not shown", 140-shown); !strings.Contains(in, want) || shown == 100 {
		t.Fatalf("%d projects shown; %q not in the input", shown, want)
	}
}

// Old Requests and processed deliveries go in the retention loop.
func TestChatRetentionDeletesOldRequestsAndDeliveries(t *testing.T) {
	w := setup(t)
	w.model.queue(answer(map[string]any{"route": "brainstorm", "confidence": 0.9, "title": "x"}), answer(map[string]any{"route": "brainstorm", "confidence": 0.9, "title": "y"}))
	w.mention("Márcia", "C1", "", "old")
	w.mention("Márcia", "C1", "", "new")
	w.exec(`UPDATE chat_requests SET created_at = now() - interval '31 days' WHERE text = 'old'`)
	w.exec(`UPDATE chat_events SET received_at = now() - interval '8 days' WHERE payload->'mention'->>'text' = 'old'`)
	w.exec(`INSERT INTO chat_events (provider, event_id, organization_id, kind, payload, received_at) VALUES ('slack', 'EvPending', $1, 'event', '{}', now() - interval '8 days')`, w.org)
	n, err := w.svc.Retain(w.ctx)
	if err != nil || n != 2 {
		t.Fatalf("retained %d, %v", n, err)
	}
	if got := w.count(`SELECT count(*) FROM chat_requests`); got != 1 {
		t.Fatalf("%d requests left", got)
	}
	if got := w.count(`SELECT count(*) FROM chat_events WHERE event_id = 'EvPending'`); got != 1 {
		t.Fatal("a pending delivery was deleted")
	}
}

// The installation's last event is written at most once a minute.
func TestChatLastEventIsWrittenAtMostOnceAMinute(t *testing.T) {
	w := setup(t)
	w.exec(`UPDATE chat_installations SET last_event_at = now() - interval '30 seconds'`)
	before := w.one(`SELECT last_event_at::text FROM chat_installations`)
	w.store(fakeslack.Mention("T1", "EvL1", "C1", "", w.fake.Post("C1", "", "U1", "x"), "U1", "x"))
	if after := w.one(`SELECT last_event_at::text FROM chat_installations`); after != before {
		t.Fatal("rewritten within the minute")
	}
	w.exec(`UPDATE chat_installations SET last_event_at = now() - interval '2 minutes'`)
	before = w.one(`SELECT last_event_at::text FROM chat_installations`)
	w.store(fakeslack.Mention("T1", "EvL2", "C1", "", w.fake.Post("C1", "", "U1", "y"), "U1", "y"))
	if after := w.one(`SELECT last_event_at::text FROM chat_installations`); after == before {
		t.Fatal("not written after a minute")
	}
}

// Only the provider's own headers reach its verification: a delivery
// signed in another header is refused.
func TestChatVerifiesOnlyTheProvidersHeaders(t *testing.T) {
	w := setup(t)
	raw := []byte(`{"type":"url_verification","challenge":"c"}`)
	req, _ := fakeslack.SignedRequest("http://x/", signingSecret, "application/json", raw, time.Now())
	h := req.Header.Clone()
	h.Set("X-Other-Signature", h.Get("X-Slack-Signature"))
	h.Del("X-Slack-Signature")
	if _, err := w.svc.Receive(w.ctx, "slack", false, h, raw); err == nil {
		t.Fatal("a delivery with no Slack signature was accepted")
	}
	if _, err := w.svc.Receive(w.ctx, "slack", false, req.Header, raw); err != nil {
		t.Fatal(err)
	}
}
