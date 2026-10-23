package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestLifecycleCommandsReachTheirRoutes(t *testing.T) {
	cases := []struct {
		args   []string
		method string
		path   string
		body   string
	}{
		{[]string{"feedback", "wi_12", "--attempt", "2"}, "GET", "/v1/tasks/wi_12/feedback?attempt=2", ``},
		{[]string{"feedback-draft", "wi_12", "-"}, "POST", "/v1/tasks/wi_12/feedback/drafts",
			`{"anchor":{"kind":"general"},"body":"from stdin"}`},
		{[]string{"feedback-draft", "ABS-12", "rename this", "--path", "web/a.ts", "--lines", "10-12"}, "POST", "/v1/tasks/wi_12/feedback/drafts",
			`{"anchor":{"endLine":12,"kind":"code","path":"web/a.ts","runId":"run_new","side":"new","startLine":10},"body":"rename this"}`},
		{[]string{"feedback-draft", "wi_12", "x", "--path", "a.ts", "--lines", "4", "--side", "old", "--run", "run_9"}, "POST", "/v1/tasks/wi_12/feedback/drafts",
			`{"anchor":{"endLine":4,"kind":"code","path":"a.ts","runId":"run_9","side":"old","startLine":4},"body":"x"}`},
		{[]string{"feedback-delete", "fb_1"}, "DELETE", "/v1/feedback/fb_1", ``},
		{[]string{"feedback-reply", "fb_1", "done in abc123"}, "POST", "/v1/feedback/fb_1/reply", `{"body":"done in abc123"}`},
		{[]string{"feedback-resolve", "fb_1", "--note", "fixed"}, "POST", "/v1/feedback/fb_1/resolve", `{"note":"fixed"}`},
		{[]string{"feedback-reopen", "fb_1", "still broken"}, "POST", "/v1/feedback/fb_1/reopen", `{"body":"still broken"}`},
		{[]string{"feedback-reopen", "fb_1"}, "POST", "/v1/feedback/fb_1/reopen", `{}`},
		{[]string{"review-questions", "ABS-12"}, "GET", "/v1/tasks/wi_12/review-questions", ``},
		{[]string{"review-answer", "rq_1", "yes, keep v1"}, "POST", "/v1/review-questions/rq_1/answer", `{"answer":"yes, keep v1"}`},
		{[]string{"review-withdraw", "rq_1", "--reason", "moot"}, "POST", "/v1/review-questions/rq_1/withdraw", `{"reason":"moot"}`},
		{[]string{"verification", "wi_12"}, "GET", "/v1/tasks/wi_12/verification", ``},
		{[]string{"close", "wi_12", "duplicate", "--duplicate-of", "ABS-3", "--close-pr"}, "POST", "/v1/tasks/wi_12/close",
			`{"closePullRequest":true,"duplicateOf":"ABS-3","resolution":"duplicate"}`},
		{[]string{"reopen", "wi_12"}, "POST", "/v1/tasks/wi_12/reopen", ``},
		{[]string{"archive", "wi_12", "--resolution", "wont_do"}, "POST", "/v1/tasks/wi_12/archive", `{"resolution":"wont_do"}`},
		{[]string{"unarchive", "wi_12"}, "POST", "/v1/tasks/wi_12/unarchive", ``},
		{[]string{"epic-archive", "ep_1", "--ended-only"}, "POST", "/v1/epics/ep_1/archive", `{"withEndedTasks":true}`},
		{[]string{"epic-unarchive", "ep_1"}, "POST", "/v1/epics/ep_1/unarchive", ``},
		{[]string{"stop", "wi_12"}, "POST", "/v1/tasks/wi_12/stop", `{}`},
		{[]string{"stop", "wi_12", "--restart", "--decider", "conductor", "--note", "wrong approach"}, "POST", "/v1/tasks/wi_12/stop",
			`{"andRestart":true,"decider":"conductor","note":"wrong approach"}`},
		{[]string{"resolve-conflicts", "pr_1"}, "POST", "/v1/pull-requests/pr_1/resolve-conflicts", ``},
		{[]string{"decider", "wi_12", "conductor", "--open-pr", "--draft"}, "POST", "/v1/tasks/wi_12/decider",
			`{"decider":"conductor","draft":true,"openPullRequest":true}`},
		{[]string{"guide"}, "GET", "/v1/help", ``},
		{[]string{"guide", "tasks"}, "GET", "/v1/help/tasks", ``},
		{[]string{"judgements", "--asker", "reviewer", "--limit", "5"}, "GET", "/v1/judgements?asker=reviewer&limit=5", ``},
		{[]string{"judgement", "mc_1"}, "GET", "/v1/judgements/mc_1", ``},
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

func TestFeedbackSubmitSendsYourDrafts(t *testing.T) {
	srv, calls := fakeDude(t)
	if _, err := runCLI(t, srv, "", "feedback-submit", "ABS-12"); err != nil {
		t.Fatal(err)
	}
	c := last(*calls)
	if c.path != "/v1/tasks/wi_12/feedback/submit" {
		t.Fatalf("submit: %+v", c)
	}
	ids, _ := json.Marshal(c.body["draftIds"])
	if string(ids) != `["fb_mine","fb_mine2"]` {
		t.Fatalf("draftIds %s: want only the drafts", ids)
	}
	key, _ := c.body["submissionKey"].(string)
	if !strings.HasPrefix(key, "cli-") {
		t.Fatalf("submissionKey %q", key)
	}
	if _, err := runCLI(t, srv, "", "feedback-submit", "wi_12", "fb_a,fb_b"); err != nil {
		t.Fatal(err)
	}
	if ids, _ := json.Marshal(last(*calls).body["draftIds"]); string(ids) != `["fb_a","fb_b"]` {
		t.Fatalf("named drafts: %s", ids)
	}
}

func TestFeedbackSendDraftsThenSubmits(t *testing.T) {
	srv, calls := fakeDude(t)
	if _, err := runCLI(t, srv, "", "feedback-send", "wi_12", "the button is off by 2px"); err != nil {
		t.Fatal(err)
	}
	n := len(*calls)
	draft, submit := (*calls)[n-2], (*calls)[n-1]
	if draft.path != "/v1/tasks/wi_12/feedback/drafts" || draft.body["body"] != "the button is off by 2px" {
		t.Fatalf("draft: %+v", draft)
	}
	if ids, _ := json.Marshal(submit.body["draftIds"]); submit.path != "/v1/tasks/wi_12/feedback/submit" || string(ids) != `["fb_new"]` {
		t.Fatalf("submit: %+v", submit)
	}
}

func TestCreateFromExistingWork(t *testing.T) {
	srv, calls := fakeDude(t)
	if _, err := runCLI(t, srv, "", "create", "ABS", "--title", "Finish it", "--goal", "take over the half-done branch",
		"--repo", "web", "--from-branch", "ana/wip", "--base", "main", "--mode", "continue"); err != nil {
		t.Fatal(err)
	}
	var create call
	for _, c := range *calls {
		if c.method == "POST" && c.path == "/v1/tasks" {
			create = c
		}
	}
	src, _ := json.Marshal(create.body["source"])
	if want := `{"base":"main","branch":"ana/wip","kind":"branch","mode":"continue","repositoryId":"rep_web"}`; string(src) != want {
		t.Fatalf("source %s, want %s", src, want)
	}
}

func TestLifecycleArgumentsAreCheckedFirst(t *testing.T) {
	srv, calls := fakeDude(t)
	for _, args := range [][]string{
		{"close", "wi_12", "done"},
		{"close", "wi_12", "wont_do", "--duplicate-of", "ABS-3"},
		{"archive", "wi_12", "--resolution", "meh"},
		{"stop", "wi_12", "--decider", "policy"},
		{"stop", "wi_12", "--restart", "--decider", "me"},
		{"feedback-draft", "wi_12", "x", "--path", "a.ts"},
		{"feedback-draft", "wi_12", "x", "--path", "a.ts", "--lines", "9-3"},
		{"feedback-draft", "wi_12", "x", "--lines", "3"},
		{"create", "ABS", "--title", "t", "--goal", "sixteen chars or more", "--from-pr", "12"},
		{"create", "ABS", "--title", "t", "--goal", "sixteen chars or more", "--repo", "web", "--from-pr", "1", "--from-branch", "b"},
		{"create", "ABS", "--title", "t", "--goal", "sixteen chars or more", "--repo", "web", "--from-pr", "1", "--base", "main"},
		{"create", "ABS", "--title", "t", "--goal", "sixteen chars or more", "--mode", "review"},
	} {
		before := len(*calls)
		if _, err := runCLI(t, srv, "", args...); err == nil {
			t.Errorf("%v succeeded", args)
		}
		for _, c := range (*calls)[before:] {
			if c.method != "GET" {
				t.Errorf("%v sent %s %s", args, c.method, c.path)
			}
		}
	}
}
