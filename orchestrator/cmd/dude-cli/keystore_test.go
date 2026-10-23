package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// memStore is an OS keystore, or (absent) a machine without one.
type memStore struct {
	keys   map[string]string
	absent bool
}

func (m *memStore) name() string { return "test keystore" }
func (m *memStore) get(h string) (string, error) {
	if m.absent {
		return "", errNoKeystore
	}
	if k, ok := m.keys[h]; ok {
		return k, nil
	}
	return "", errNoKey
}
func (m *memStore) set(h, k string) error {
	if m.absent {
		return errNoKeystore
	}
	m.keys[h] = k
	return nil
}
func (m *memStore) del(h string) error {
	if _, err := m.get(h); err != nil {
		return err
	}
	delete(m.keys, h)
	return nil
}

// isolate swaps process globals: tests using it must not run in parallel.
func isolate(t *testing.T, absent bool) *memStore {
	t.Helper()
	store := &memStore{keys: map[string]string{}, absent: absent}
	prevStore, prevFile := osKeystore, credentialsFile
	path := filepath.Join(t.TempDir(), "dude", "credentials.toml")
	osKeystore, credentialsFile = store, func() string { return path }
	t.Setenv("DUDE_API_KEY", "")
	t.Setenv("DUDE_URL", "")
	t.Setenv("DUDE_ORG", "")
	t.Cleanup(func() { osKeystore, credentialsFile = prevStore, prevFile })
	return store
}

func TestEnvKeyNeedsNoLogin(t *testing.T) {
	isolate(t, false)
	t.Setenv("DUDE_API_KEY", "from-env")
	t.Setenv("DUDE_URL", "https://dude.example/")
	base, key, err := resolve("")
	if err != nil || base != "https://dude.example" || key != "from-env" {
		t.Fatalf("%q %q %v", base, key, err)
	}
}

func TestEnvURLCannotRedirectStoredKey(t *testing.T) {
	for _, file := range []bool{false, true} {
		t.Run(fmt.Sprint(file), func(t *testing.T) {
			isolate(t, file)
			if _, err := login("acme", "https://stored.example", "stored-key", false); err != nil {
				t.Fatal(err)
			}
			t.Setenv("DUDE_URL", "https://other.example")
			base, key, err := resolve("")
			if err == nil || base != "" || key != "" {
				t.Fatalf("redirected stored key: %q %q %v", base, key, err)
			}
			for _, part := range []string{"https://other.example", "https://stored.example", "DUDE_API_KEY", "unset DUDE_URL"} {
				if !strings.Contains(err.Error(), part) {
					t.Fatalf("missing %q: %v", part, err)
				}
			}
			for _, same := range []string{"https://stored.example/", "https://STORED.example", "https://stored.example:443", "stored.example"} {
				t.Setenv("DUDE_URL", same)
				if _, key, err := resolve(""); err != nil || key != "stored-key" {
					t.Fatalf("same URL %q: %q %v", same, key, err)
				}
			}
			t.Setenv("DUDE_URL", "http://stored.example")
			if _, _, err := resolve(""); err == nil {
				t.Fatal("http is another server than https")
			}
			t.Setenv("DUDE_API_KEY", "env-key")
			t.Setenv("DUDE_URL", "https://ci.example/")
			if base, key, err := resolve(""); err != nil || base != "https://ci.example" || key != "env-key" {
				t.Fatalf("CI override: %q %q %v", base, key, err)
			}
		})
	}
}

func TestLegacyHostKeyStillWorks(t *testing.T) {
	store := isolate(t, false)
	store.keys["dude.absmartly.dev"] = "old"
	if base, key, err := resolve(""); err != nil || base != defaultURL || key != "old" {
		t.Fatalf("%q %q %v", base, key, err)
	}
}

func TestNotLoggedIn(t *testing.T) {
	isolate(t, false)
	if _, _, err := resolve(""); err == nil || !strings.Contains(err.Error(), "dude login URL") {
		t.Fatalf("err = %v", err)
	}
}

func TestProfilesPerOrganisation(t *testing.T) {
	store := isolate(t, false)
	if _, err := login("acme", "https://dude.acme.dev", "ka", false); err != nil {
		t.Fatal(err)
	}
	if _, err := login("northwind", "https://dude.acme.dev", "kn", false); err != nil {
		t.Fatal(err)
	}
	if store.keys["dude.acme.dev/acme"] != "ka" || store.keys["dude.acme.dev/northwind"] != "kn" {
		t.Fatalf("store %v", store.keys)
	}
	raw, _ := os.ReadFile(credentialsFile())
	if strings.Contains(string(raw), "ka") || strings.Contains(string(raw), "kn") {
		t.Fatalf("a key is in the file:\n%s", raw)
	}
	if _, key, _ := resolve(""); key != "kn" {
		t.Fatalf("current: %q, want the last logged in to", key)
	}
	if _, key, _ := resolve("acme"); key != "ka" {
		t.Fatalf("--org acme: %q", key)
	}
	t.Setenv("DUDE_ORG", "acme")
	if _, key, _ := resolve(""); key != "ka" {
		t.Fatalf("DUDE_ORG=acme: %q", key)
	}
	if _, _, err := resolve("nope"); err == nil || !strings.Contains(err.Error(), "dude orgs") {
		t.Fatalf("unknown org: %v", err)
	}
	t.Setenv("DUDE_ORG", "")
	if err := logout("northwind"); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.keys["dude.acme.dev/northwind"]; ok {
		t.Fatal("logout left the key")
	}
	creds, _ := loadCredentials()
	if creds.Current != "acme" {
		t.Fatalf("current after logout: %q", creds.Current)
	}
	logout("acme")
	if _, err := os.Stat(credentialsFile()); !os.IsNotExist(err) {
		t.Fatalf("empty credentials file left behind: %v", err)
	}
}

func TestReloginKeepsNewKeyAndRemovesOldItem(t *testing.T) {
	for _, tc := range []struct {
		name, oldURL, newURL, oldID, newID string
		file                               bool
	}{
		{"port to https", "http://host:8080", "https://host", "host:8080/acme", "host/acme", false},
		{"scheme only", "http://host", "https://host", "host/acme", "host/acme", false},
		{"another host", "https://old", "https://new", "old/acme", "new/acme", false},
		{"host case", "https://Host", "https://host", "host/acme", "host/acme", false},
		{"to file", "https://host", "https://host", "host/acme", "host/acme", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := isolate(t, false)
			if _, err := login("acme", tc.oldURL, "old-key", false); err != nil {
				t.Fatal(err)
			}
			if store.keys[tc.oldID] != "old-key" {
				t.Fatalf("old item: %v", store.keys)
			}
			if _, err := login("acme", tc.newURL, "new-key", tc.file); err != nil {
				t.Fatal(err)
			}
			if base, key, err := resolve(""); err != nil || base != tc.newURL || key != "new-key" {
				t.Fatalf("after re-login: %q %q %v", base, key, err)
			}
			if tc.file {
				if len(store.keys) != 0 {
					t.Fatalf("file login left keys: %v", store.keys)
				}
			} else if len(store.keys) != 1 || store.keys[tc.newID] != "new-key" {
				t.Fatalf("new item: %v", store.keys)
			}
		})
	}
}

func TestKeyInFileWithoutStore(t *testing.T) {
	isolate(t, true)
	where, err := login("acme", "https://dude.acme.dev", "s3cret", false)
	if err != nil || where != credentialsFile() {
		t.Fatalf("kept in %q, %v", where, err)
	}
	fi, err := os.Stat(credentialsFile())
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("file %v, %v", fi, err)
	}
	if base, key, _ := resolve(""); key != "s3cret" || base != "https://dude.acme.dev" {
		t.Fatalf("%q %q", base, key)
	}
}

func TestLoginNamesTheOrganisation(t *testing.T) {
	store := isolate(t, false)
	srv, calls := fakeDude(t)
	var out bytes.Buffer
	c := &cli{login: login, logout: logout, http: srv.Client(), stdin: strings.NewReader("new-key\n"), out: &out,
		creds: func(string) (string, string, error) { t.Fatal("login used stored credentials"); return "", "", nil }}
	if err := c.run([]string{"login", srv.URL}); err != nil {
		t.Fatal(err)
	}
	if store.keys[keyID("acme", srv.URL)] != "new-key" {
		t.Fatalf("stored %v", store.keys)
	}
	if c := last(*calls); c.path != "/v1/me" || c.auth != "Bearer new-key" {
		t.Fatalf("checked with %+v", c)
	}
	if strings.Contains(out.String(), "new-key") {
		t.Fatal("login printed the key")
	}
	if base, key, _ := resolve(""); base != srv.URL || key != "new-key" {
		t.Fatalf("after login: %q %q", base, key)
	}
}

func TestLoginRefusedKeyIsNotStored(t *testing.T) {
	store := isolate(t, false)
	srv, _ := fakeDude(t)
	c := &cli{login: login, logout: logout, http: srv.Client(), stdin: strings.NewReader("bad-key\n"), out: io.Discard}
	err := c.run([]string{"login", srv.URL})
	var he *httpError
	if !errors.As(err, &he) || he.status != 401 || !strings.Contains(err.Error(), "refused the key") {
		t.Fatalf("refusal: %v", err)
	}
	if len(store.keys) != 0 {
		t.Fatalf("stored %v", store.keys)
	}
	if _, err := os.Stat(credentialsFile()); !os.IsNotExist(err) {
		t.Fatal("credentials file written")
	}
}

func TestOrgFlagBeforeCommand(t *testing.T) {
	srv, _ := fakeDude(t)
	var asked string
	c := &cli{http: srv.Client(), stdin: strings.NewReader(""), out: io.Discard,
		creds: func(org string) (string, string, error) { asked = org; return srv.URL, "k", nil }}
	if err := c.run([]string{"--org", "northwind", "projects"}); err != nil {
		t.Fatal(err)
	}
	if asked != "northwind" {
		t.Fatalf("org %q", asked)
	}
}

func TestLogoutSelectsOrganisation(t *testing.T) {
	for _, mode := range []string{"flag", "equals", "env"} {
		t.Run(mode, func(t *testing.T) {
			store := isolate(t, false)
			for _, slug := range []string{"acme", "northwind"} {
				if _, err := login(slug, "https://host", slug+"-key", false); err != nil {
					t.Fatal(err)
				}
			}
			args := []string{"logout"}
			switch mode {
			case "flag":
				args = []string{"--org", "acme", "logout"}
			case "equals":
				args = []string{"--org=acme", "logout"}
			default:
				t.Setenv("DUDE_ORG", "acme")
			}
			c := &cli{logout: logout, out: io.Discard}
			if err := c.run(args); err != nil {
				t.Fatal(err)
			}
			creds, err := loadCredentials()
			if err != nil || len(creds.Orgs) != 1 || creds.Current != "northwind" || store.keys["host/northwind"] != "northwind-key" || len(store.keys) != 1 {
				t.Fatalf("logout: %+v %v %v", creds, store.keys, err)
			}
		})
	}
}

func TestUsePersistsAndIdentityOutputHidesKeys(t *testing.T) {
	for _, file := range []bool{false, true} {
		t.Run(fmt.Sprint(file), func(t *testing.T) {
			isolate(t, file)
			srv, _ := fakeDude(t)
			for _, slug := range []string{"acme", "northwind"} {
				if _, err := login(slug, srv.URL, slug+"-secret", false); err != nil {
					t.Fatal(err)
				}
			}
			var out bytes.Buffer
			c := &cli{creds: resolve, http: srv.Client(), out: &out}
			if err := c.run([]string{"use", "acme"}); err != nil {
				t.Fatal(err)
			}
			if _, key, err := resolve(""); err != nil || key != "acme-secret" {
				t.Fatalf("use not saved: %q %v", key, err)
			}
			out.Reset()
			if err := c.run([]string{"orgs"}); err != nil {
				t.Fatal(err)
			}
			var orgs []struct {
				Slug    string
				Current bool
			}
			if err := json.Unmarshal(out.Bytes(), &orgs); err != nil || len(orgs) != 2 || orgs[0].Slug != "acme" || !orgs[0].Current || orgs[1].Current {
				t.Fatalf("orgs: %s %v", out.String(), err)
			}
			if err := c.run([]string{"whoami"}); err != nil {
				t.Fatal(err)
			}
			for _, key := range []string{"acme-secret", "northwind-secret"} {
				if strings.Contains(out.String(), key) {
					t.Fatalf("identity output contains %q", key)
				}
			}
		})
	}
}

func TestVariadicArguments(t *testing.T) {
	srv, calls := fakeDude(t)
	if _, err := runCLI(t, srv, "", "assign", "wi_12", "usr_1", "usr_2"); err != nil {
		t.Fatal(err)
	}
	c := last(*calls)
	if c.method != "PUT" || c.path != "/v1/tasks/wi_12/people" || len(c.body["people"].([]any)) != 2 {
		t.Fatalf("assign: %+v", c)
	}
	if _, err := runCLI(t, srv, "", "assign", "wi_12"); err == nil {
		t.Fatal("assign with no person succeeded")
	}
}

func TestNewCommandsReachTheirRoutes(t *testing.T) {
	cases := []struct {
		args   []string
		method string
		path   string
		body   string
	}{
		{[]string{"start", "wi_12"}, "POST", "/v1/tasks/wi_12/deliver", `{}`},
		{[]string{"start", "wi_12", "--override", "--note", "urgent"}, "POST", "/v1/tasks/wi_12/deliver", `{"note":"urgent","override":true}`},
		{[]string{"talk", "wi_12", "--override", "--note", "plan"}, "POST", "/v1/tasks/wi_12/talk", `{"note":"plan","override":true}`},
		{[]string{"create", "ABS", "--title", "New", "--goal", "A goal long enough to save.", "--talk"}, "POST", "/v1/tasks/wi_13/talk", `{}`},
		{[]string{"chat", "wi_12", "hello"}, "POST", "/v1/tasks/wi_12/chat", `{"text":"hello"}`},
		{[]string{"chat", "wi_12", "hello", "--aside"}, "POST", "/v1/tasks/wi_12/chat", `{"aside":true,"text":"hello"}`},
		{[]string{"pause", "run_9"}, "POST", "/v1/runs/run_9/pause", `{"mode":"graceful"}`},
		{[]string{"pause", "run_9", "--hard", "--reason", "wait"}, "POST", "/v1/runs/run_9/pause", `{"mode":"hard","reason":"wait"}`},
		{[]string{"resume", "run_9", "--reason", "ready"}, "POST", "/v1/runs/run_9/resume", `{"reason":"ready"}`},
		{[]string{"abort", "run_9", "--reason", "stop"}, "POST", "/v1/runs/run_9/abort", `{"reason":"stop"}`},
		{[]string{"restart", "run_9", "try again", "--tier", "tier_1"}, "POST", "/v1/runs/run_9/restart", `{"note":"try again","tier":"tier_1"}`},
		{[]string{"reviewers", "pr_1", "ana", "bob"}, "POST", "/v1/pull-requests/pr_1/reviewers", `{"logins":["ana","bob"]}`},
		{[]string{"invite", "ana@example.com", "--role", "admin"}, "POST", "/v1/organization/invitations", `{"invitations":[{"email":"ana@example.com"}],"role":"admin"}`},
		{[]string{"decide", "wi_12", "retry", "--note", "ready"}, "POST", "/v1/tasks/wi_12/decide", `{"action":"retry","note":"ready"}`},
		{[]string{"memory-add", "--title", "Queue", "--content", "Use it", "--kind", "procedure", "--project", "ABS"}, "POST", "/v1/memory/memories", `{"content":"Use it","kind":"procedure","projectId":"prj_1","title":"Queue"}`},
		{[]string{"memories", "--project", "ABS", "--text", "queue", "--archived"}, "GET", "/v1/memory/memories?archived=true&project=prj_1&q=queue", ``},
		{[]string{"epic-create", "ABS", "--title", "Launch", "--description", "Soon"}, "POST", "/v1/projects/prj_1/epics", `{"description":"Soon","title":"Launch"}`},
		{[]string{"events", "wi_12", "--after", "42", "--limit", "10"}, "GET", "/v1/events?after=42&limit=10&taskId=wi_12", ``},
		{[]string{"events", "wi_12", "--run", "run_9", "--after", "42", "--limit", "10"}, "GET", "/v1/events?after=42&limit=10&runId=run_9", ``},
		{[]string{"update-branch", "pr_1"}, "POST", "/v1/pull-requests/pr_1/update-branch", ``},
		{[]string{"rerun-checks", "pr_1"}, "POST", "/v1/pull-requests/pr_1/rerun-failed", ``},
		{[]string{"unrelate", "wi_12", "blocks", "ABS-3"}, "DELETE", "/v1/tasks/wi_12/relations/blocks/ABS-3", ``},
		{[]string{"brainstorm", "Think", "--title", "Queue", "--project", "ABS", "--tier", "tier_1"}, "POST", "/v1/brainstorms", `{"message":"Think","projects":[{"projectId":"prj_1"}],"tier":"tier_1","title":"Queue"}`},
		{[]string{"brainstorm-answer", "bs_1", "q_1", "because", "--choice", "1"}, "POST", "/v1/brainstorms/bs_1/questions/q_1/answer", `{"answers":[{"choices":[1],"text":"because"}]}`},
		{[]string{"brainstorm-invite", "bs_1", "per_1", "--role", "read"}, "POST", "/v1/brainstorms/bs_1/people", `{"people":["per_1"],"role":"read"}`},
		{[]string{"reset-machine", "wi_12", "implementer", "--note", "fresh"}, "DELETE", "/v1/tasks/wi_12/agent-machines/implementer", `{"note":"fresh"}`},
		{[]string{"preview", "wi_12"}, "POST", "/v1/tasks/wi_12/preview", ``},
		{[]string{"preview", "wi_12", "--stop"}, "DELETE", "/v1/tasks/wi_12/preview", ``},
		{[]string{"brainstorm-title", "bs_1", "Queue"}, "POST", "/v1/brainstorms/bs_1/title", `{"title":"Queue"}`},
		{[]string{"brainstorm-chat", "bs_1", "hello"}, "POST", "/v1/brainstorms/bs_1/chat", `{"text":"hello"}`},
		{[]string{"brainstorm-retry", "bs_1"}, "POST", "/v1/brainstorms/bs_1/retry", ``},
		{[]string{"brainstorm-archive", "bs_1"}, "POST", "/v1/brainstorms/bs_1/archive", ``},
		{[]string{"brainstorm-unarchive", "bs_1"}, "POST", "/v1/brainstorms/bs_1/unarchive", ``},
		{[]string{"brainstorms", "--archived"}, "GET", "/v1/brainstorms?archived=1", ``},
		{[]string{"brainstorm-show", "bs_1"}, "GET", "/v1/brainstorms/bs_1", ``},
		{[]string{"project-create", "--name", "Web", "--slug", "web", "--key", "WEB"}, "POST", "/v1/projects", `{"description":"","key":"WEB","name":"Web","repositories":[],"slug":"web"}`},
		{[]string{"repo-add", "ABS", "--name", "web", "--url", "https://example.com/web.git", "--branch", "dev"}, "POST", "/v1/projects/prj_1/repositories", `{"defaultBranch":"dev","name":"web","url":"https://example.com/web.git"}`},
		{[]string{"resolve", "fnd_1", "accepted", "--note", "fine"}, "POST", "/v1/findings/fnd_1/resolve", `{"note":"fine","status":"accepted"}`},
		{[]string{"recover", "wi_12"}, "GET", "/v1/tasks/wi_12/recover", ``},
		{[]string{"recover", "wi_12", "restart", "--override"}, "POST", "/v1/tasks/wi_12/recover", `{"action":"restart","override":true}`},
		{[]string{"repo-decide", "rr_1", "decline"}, "POST", "/v1/repository-requests/rr_1/decide", `{"approve":false}`},
		{[]string{"note", "ep_1", "-", "--replaces", "2"}, "POST", "/v1/epics/ep_1/notes", `{"replaces":2,"text":"from stdin"}`},
		{[]string{"epic-update", "ep_1", "--state", "none"}, "PATCH", "/v1/epics/ep_1", `{"state":null}`},
		{[]string{"merge", "pr_1", "--method", "squash"}, "POST", "/v1/pull-requests/pr_1/merge", `{"method":"squash"}`},
		{[]string{"findings", "--run", "run_9"}, "GET", "/v1/findings?runId=run_9", ``},
		{[]string{"memory-search", "retry backoff", "--words"}, "GET", "/v1/memory/search?mode=words&q=retry+backoff", ``},
		{[]string{"brainstorm-file", "bs_1", "prop_1", "0", "2"}, "POST", "/v1/brainstorms/bs_1/file", `{"items":[0,2],"proposalId":"prop_1"}`},
		{[]string{"steer", "ABS-12", "x"}, "POST", "/v1/runs/run_new/steer", `{"text":"x"}`},
	}
	for _, tc := range cases {
		srv, calls := fakeDude(t)
		if _, err := runCLI(t, srv, "from stdin", tc.args...); err != nil {
			t.Fatalf("%v: %v", tc.args, err)
		}
		c := last(*calls)
		body := ""
		if c.body != nil {
			b, _ := json.Marshal(c.body)
			body = string(b)
		}
		if c.method != tc.method || c.path != tc.path || body != tc.body {
			t.Errorf("%v: %s %s %s, want %s %s %s", tc.args, c.method, c.path, body, tc.method, tc.path, tc.body)
		}
	}
}

func TestEnumsAreCheckedBeforeAnyRequest(t *testing.T) {
	srv, calls := fakeDude(t)
	for _, args := range [][]string{
		{"resolve", "fnd_1", "dismissed"},
		{"recover", "wi_12", "reboot"},
		{"decider", "wi_12", "me"},
		{"repo-decide", "rr_1", "maybe"},
	} {
		if _, err := runCLI(t, srv, "", args...); err == nil {
			t.Errorf("%v succeeded", args)
		}
	}
	if len(*calls) != 0 {
		t.Fatalf("made requests: %+v", *calls)
	}
}
