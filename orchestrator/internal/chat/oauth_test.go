package chat_test

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/chat"
	"github.com/marciomartins/dude/orchestrator/internal/fakeslack"
)

const (
	installRedirect = "https://dude.example.com/v1/chat/slack/install/callback"
	connectRedirect = "https://dude.example.com/v1/chat/slack/connect/callback"
)

func (w *world) beginInstall(person string) url.Values {
	w.t.Helper()
	start, err := w.svc.BeginInstall(w.ctx, w.org, chat.Begin{Provider: "slack", Person: person, BrowserHash: "b", Redirect: installRedirect, Admin: true})
	if err != nil {
		w.t.Fatal(err)
	}
	return follow(w.t, start)
}

func (w *world) beginConnect(person, prompt string) url.Values {
	w.t.Helper()
	start, err := w.svc.BeginConnect(w.ctx, w.org, chat.Begin{Provider: "slack", Person: person, BrowserHash: "b", Redirect: connectRedirect, Prompt: prompt})
	if err != nil {
		w.t.Fatal(err)
	}
	return follow(w.t, start)
}

func callback(back url.Values, browser string) chat.Callback {
	return chat.Callback{Provider: "slack", State: back.Get("state"), Code: back.Get("code"), BrowserHash: browser}
}

// A callback with no cookie (no browser hash) is never finished: the
// cookie is what binds the state to the browser that began it.
func TestChatACallbackWithoutTheBrowsersCookieIsRefused(t *testing.T) {
	w := setup(t)
	w.exec(`DELETE FROM chat_installations WHERE organization_id = $1`, w.org)
	w.fake.AuthorizeAs("U1", "U3")
	if l, _ := w.svc.CompleteInstall(w.ctx, callback(w.beginInstall(w.people["Márcia"][0]), "")); l.Outcome != "invalid_or_expired" {
		t.Fatalf("install with no cookie: %+v", l)
	}
	if n := w.count(`SELECT count(*) FROM chat_installations WHERE organization_id = $1`, w.org); n != 0 {
		t.Fatal("installed with no cookie")
	}
	if l, _ := w.svc.CompleteInstall(w.ctx, callback(w.beginInstall(w.people["Márcia"][0]), "b")); l.Outcome != "installed" {
		t.Fatalf("install: %+v", l)
	}
	if l, _ := w.svc.CompleteConnect(w.ctx, callback(w.beginConnect(w.people["Gus"][0], ""), "")); l.Outcome != "invalid_or_expired" {
		t.Fatalf("connect with no cookie: %+v", l)
	}
	if n := w.count(`SELECT count(*) FROM chat_identities WHERE external_user_id = 'U3'`); n != 0 {
		t.Fatal("linked with no cookie")
	}
}

// Every state lasts exactly 15 minutes: an install's, a sign-in's and a
// Connect prompt's, each refused once past it.
func TestChatStatesExpireAfterFifteenMinutes(t *testing.T) {
	w := setup(t)
	w.exec(`DELETE FROM chat_installations WHERE organization_id = $1`, w.org)
	w.fake.AuthorizeAs("U1", "U3")
	later := func() time.Time { return time.Now().Add(15*time.Minute + time.Second) }
	back := w.beginInstall(w.people["Márcia"][0])
	if n := w.count(`SELECT count(*) FROM chat_connect_states WHERE kind = 'install'
		AND expires_at BETWEEN now() + interval '14 minutes 58 seconds' AND now() + interval '15 minutes'`); n != 1 {
		t.Fatal("an install state does not last 15 minutes")
	}
	w.svc.Now = later
	if l, _ := w.svc.CompleteInstall(w.ctx, callback(back, "b")); l.Outcome != "invalid_or_expired" {
		t.Fatalf("an expired install: %+v", l)
	}
	w.svc.Now = nil
	if l, _ := w.svc.CompleteInstall(w.ctx, callback(w.beginInstall(w.people["Márcia"][0]), "b")); l.Outcome != "installed" {
		t.Fatalf("install: %+v", l)
	}
	back = w.beginConnect(w.people["Gus"][0], "")
	w.svc.Now = later
	if l, _ := w.svc.CompleteConnect(w.ctx, callback(back, "b")); l.Outcome != "invalid_or_expired" {
		t.Fatalf("an expired sign-in: %+v", l)
	}
	w.svc.Now = nil
	// A Connect prompt: begun from past its 15 minutes, refused with a 410; read as expired.
	w.mention("Gus", "C1", "", "help")
	prompt := promptState(t, w)
	if n := w.count(`SELECT count(*) FROM chat_connect_states WHERE kind = 'prompt'
		AND expires_at BETWEEN now() + interval '14 minutes 58 seconds' AND now() + interval '15 minutes'`); n != 1 {
		t.Fatal("a prompt does not last 15 minutes")
	}
	w.exec(`UPDATE chat_connect_states SET expires_at = now() - interval '1 second' WHERE kind = 'prompt'`)
	if st, err := w.svc.ConnectState(w.ctx, w.org, "slack", w.people["Gus"][0], prompt); err != nil || st["status"] != "expired" {
		t.Fatalf("connect state %+v, %v", st, err)
	}
	_, err := w.svc.BeginConnect(w.ctx, w.org, chat.Begin{Provider: "slack", Person: w.people["Gus"][0], BrowserHash: "b",
		Redirect: connectRedirect, Prompt: prompt})
	if ref, ok := err.(*chat.Refusal); !ok || ref.Status != 410 {
		t.Fatalf("an expired prompt: %v", err)
	}
	if n := w.count(`SELECT count(*) FROM chat_identities WHERE external_user_id = 'U3'`); n != 0 {
		t.Fatal("linked from an expired state")
	}
}

// promptState is the state in the last Connect prompt dude sent.
func promptState(t *testing.T, w *world) string {
	t.Helper()
	eph := w.fake.Ephemerals()
	link, _ := url.Parse(regexpFind(string(eph[len(eph)-1].Blocks)))
	return link.Query().Get("state")
}

// The install and connect guards, each refusing with nothing written.
func TestChatInstallAndConnectGuards(t *testing.T) {
	t.Run("an admin demoted since the install began installs nothing", func(t *testing.T) {
		w := setup(t)
		w.exec(`DELETE FROM chat_installations WHERE organization_id = $1`, w.org)
		w.fake.AuthorizeAs("U1", "")
		back := w.beginInstall(w.people["Márcia"][0])
		w.exec(`UPDATE people SET role = 'member' WHERE id = $1`, w.people["Márcia"][0])
		if l, _ := w.svc.CompleteInstall(w.ctx, callback(back, "b")); l.Outcome != "authorization_denied" {
			t.Fatalf("%+v", l)
		}
		if n := w.count(`SELECT count(*) FROM chat_installations WHERE organization_id = $1`, w.org); n != 0 {
			t.Fatal("installed")
		}
	})
	t.Run("a sign-in from another workspace links nothing", func(t *testing.T) {
		w := setup(t)
		w.fake.AddTeam("T2", "Elsewhere")
		w.fake.AddUser(fakeslack.User{ID: "UZ", Team: "T2", Name: "zara", RealName: "Zara"})
		w.fake.AuthorizeAs("", "UZ")
		if l, _ := w.svc.CompleteConnect(w.ctx, callback(w.beginConnect(w.people["Gus"][0], ""), "b")); l.Outcome != "workspace_not_installed" {
			t.Fatalf("%+v", l)
		}
		if n := w.count(`SELECT count(*) FROM chat_identities WHERE person_id = $1`, w.people["Gus"][0]); n != 0 {
			t.Fatal("linked")
		}
	})
	t.Run("an install state at the connect callback, and a sign-in state at the install one", func(t *testing.T) {
		w := setup(t)
		w.exec(`DELETE FROM chat_installations WHERE organization_id = $1`, w.org)
		w.fake.AuthorizeAs("U1", "U3")
		installBack := w.beginInstall(w.people["Márcia"][0])
		if l, _ := w.svc.CompleteConnect(w.ctx, callback(installBack, "b")); l.Outcome != "invalid_or_expired" {
			t.Fatalf("an install state connected: %+v", l)
		}
		w.exec(`INSERT INTO chat_installations (id, organization_id, provider, external_workspace_id, workspace_name, bot_user_id, bot_token)
			VALUES ($1, $2, 'slack', 'T1', 'Acme Slack', 'UBT1', $3)`, "chi2_"+w.org, w.org, w.fake.Token("T1"))
		connectBack := w.beginConnect(w.people["Gus"][0], "")
		w.exec(`DELETE FROM chat_installations WHERE organization_id = $1`, w.org)
		if l, _ := w.svc.CompleteInstall(w.ctx, callback(connectBack, "b")); l.Outcome != "invalid_or_expired" {
			t.Fatalf("a sign-in state installed: %+v", l)
		}
		if n := w.count(`SELECT count(*) FROM chat_installations WHERE organization_id = $1`, w.org) +
			w.count(`SELECT count(*) FROM chat_identities WHERE external_user_id = 'U3'`); n != 0 {
			t.Fatal("something was written")
		}
	})
	t.Run("a kept mention is routed only for the Slack account it was kept for", func(t *testing.T) {
		w := setup(t)
		w.mention("Gus", "C1", "", "file it")
		prompt := promptState(t, w)
		// Zed's Slack account signs in from Gus's prompt, for Gus's dude account.
		w.fake.AuthorizeAs("", "U9")
		l, _ := w.svc.CompleteConnect(w.ctx, callback(w.beginConnect(w.people["Gus"][0], prompt), "b"))
		if l.Outcome != "connected" {
			t.Fatalf("%+v", l)
		}
		if n := w.count(`SELECT count(*) FROM chat_events WHERE event_id LIKE 'connect:%'`); n != 0 {
			t.Fatal("Gus's kept mention was queued for Zed's sign-in")
		}
	})
}

// A group DM shares the session with every connected member, all of
// them, and a private channel with two connected members adds both.
func TestChatAGroupSharesWithEveryConnectedMember(t *testing.T) {
	w := setup(t)
	gus := w.people["Gus"][0]
	gusAccount := w.one(`SELECT account_id FROM people WHERE id = $1`, gus)
	w.exec(`INSERT INTO chat_identities (organization_id, provider, external_workspace_id, external_user_id, account_id, person_id)
		VALUES ($1, 'slack', 'T1', 'U3', $2, $3)`, w.org, gusAccount, gus)
	w.fake.SetMembers("M1", []string{"U1", "U2", "U3", "U9", "UBT1"})
	w.model.queue(answer(map[string]any{"route": "brainstorm", "confidence": 0.9, "title": "Pair"}))
	w.mention("Márcia", "M1", "", "pair on this")
	got := w.last()
	if got.Kind != "group" || got.ResultKind != "session" {
		t.Fatalf("request %+v", got)
	}
	if a := w.one(`SELECT access FROM sessions WHERE id = $1`, got.ResultID); a != "private" {
		t.Fatalf("access %s", a)
	}
	for _, name := range []string{"Eli", "Gus"} {
		if r := w.one(`SELECT session_role($1, $2)`, got.ResultID, w.people[name][0]); r != "chat" {
			t.Fatalf("%s's role %q", name, r)
		}
	}
	if len(got.Added) != 2 || strings.Join(got.Skipped, ",") != "U9" {
		t.Fatalf("added %v skipped %v", got.Added, got.Skipped)
	}
}

// A thread bound to an epic gets the epic's link, with no model.
func TestChatAnEpicBoundThreadGetsItsLink(t *testing.T) {
	w := setup(t)
	w.model.queue(answer(map[string]any{"route": "epic", "confidence": 0.9, "project": "ABS", "new_epic": map[string]any{"title": "Fast exports",
		"tasks": []map[string]any{{"ref": "a", "title": "Measure"}}}}))
	root := w.mention("Márcia", "C1", "", "open an epic")
	epic := w.last().ResultID
	calls := w.model.calls()
	w.mention("Eli", "C1", root, "where are we?")
	got := w.last()
	if got.Route != "task_link" || got.ResultKind != "epic" || got.ResultID != epic || w.model.calls() != calls {
		t.Fatalf("request %+v, %d model calls", got, w.model.calls())
	}
	if text := lastBot(t, w.fake).Text; text != "This thread is the epic ABS › Fast exports" {
		t.Fatalf("reply %q", text)
	}
}

// A person in a bound thread who cannot chat in its session (they left
// the organisation's session's conversation and were never added, here a
// thread a resume bound) is refused privately and nothing is sent in.
func TestChatABoundThreadRefusesWhoCannotChatInItsSession(t *testing.T) {
	w := setup(t)
	w.model.queue(answer(map[string]any{"route": "brainstorm", "confidence": 0.9, "title": "Ideas"}))
	w.mention("Márcia", "G1", "", "ideas")
	session := w.last().ResultID
	w.model.queue(resume(session))
	root := w.mention("Márcia", "M1", "", "continue")
	// Eli was in G1 and chats in the session; take him out, then he mentions in M1's bound thread.
	w.exec(`DELETE FROM session_people WHERE session_id = $1 AND person_id = $2`, session, w.people["Eli"][0])
	directives := w.count(`SELECT count(*) FROM directives WHERE organization_id = $1`, w.org)
	w.mention("Eli", "M1", root, "let me see")
	if got := w.last(); got.Route != "not_member" {
		t.Fatalf("request %+v", got)
	}
	if n := w.count(`SELECT count(*) FROM directives WHERE organization_id = $1`, w.org); n != directives {
		t.Fatal("a message went into the session")
	}
	if eph := w.fake.Ephemerals(); len(eph) == 0 || !strings.Contains(eph[len(eph)-1].Text, "private, and you are not in it") {
		t.Fatalf("ephemerals %+v", eph)
	}
}

// An unsure brainstorm is no other route dude went from.
func TestChatAnUnsureBrainstormIsJustABrainstorm(t *testing.T) {
	w := setup(t)
	w.model.queue(answer(map[string]any{"route": "brainstorm", "confidence": 0.6, "title": "x"}))
	w.mention("Márcia", "C1", "", "hm")
	if got := w.last(); got.Route != "brainstorm" || got.AskedRoute != "" {
		t.Fatalf("request %+v", got)
	}
}
