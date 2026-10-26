package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// put sends the task's choice before its pull request as person.
func (w *prActionWorld) choose(person, body string) (int, string, string) {
	w.t.Helper()
	r := httptest.NewRequest("PUT", "/internal/tasks/task_pr/pull-request-choice", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer svc")
	r.Header.Set("X-Dude-Organization", w.org)
	r.Header.Set("X-Dude-Credential-Kind", "person")
	r.Header.Set("X-Dude-Actor", person)
	r.Header.Set("X-Dude-Person", person)
	rec := httptest.NewRecorder()
	w.h.ServeHTTP(rec, r)
	var out struct {
		Error struct{ Code, Message string }
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out.Error.Code, out.Error.Message
}

func (w *prActionWorld) storedChoice() string {
	w.t.Helper()
	var c *string
	if err := w.owner.QueryRow(context.Background(), `SELECT pull_request_choice FROM tasks WHERE id = 'task_pr'`).Scan(&c); err != nil {
		w.t.Fatal(err)
	}
	if c == nil {
		return "project"
	}
	return *c
}

// The task's choice is its owner's alone, an admin's no more than anyone
// else's; and once a pull request of its attempt is on record, nobody's.
func TestATasksChoiceBeforeThePullRequestIsItsOwnersUntilOneIsOpen(t *testing.T) {
	w := newPRActionWorld(t)
	w.exec(`DELETE FROM pull_requests WHERE id = 'pr_one'`)
	for _, who := range []string{"per_member", "per_admin"} {
		if code, errCode, msg := w.choose(who, `{"choice":"ask"}`); code != 403 || errCode != "not_owner" || msg != "Only Olga can change it." {
			t.Errorf("%s: %d %s %q", who, code, errCode, msg)
		}
	}
	if code, errCode, _ := w.choose("per_owner", `{"choice":"later"}`); code != 400 || errCode != "bad_request" {
		t.Errorf("an unknown choice: %d %s", code, errCode)
	}
	if code, _, msg := w.choose("per_owner", `{"choice":"ask"}`); code != 200 || w.storedChoice() != "ask" {
		t.Fatalf("the owner's ask: %d %q, stored %s", code, msg, w.storedChoice())
	}
	if code, _, _ := w.choose("per_owner", `{"choice":null}`); code != 200 || w.storedChoice() != "project" {
		t.Fatalf("back to the project's: %d, stored %s", code, w.storedChoice())
	}
	w.exec(`INSERT INTO pull_requests (id, organization_id, project_id, task_id, repository_id, number, url, head_branch, base_branch, head_sha, title)
		VALUES ('pr_two', $1, 'prj_pr', 'task_pr', 'repo_pr', 2, 'https://github.com/acme/repo/pull/2', 'topic', 'main', 'h', 'Topic')`, w.org)
	if code, errCode, msg := w.choose("per_owner", `{"choice":"draft"}`); code != 409 || errCode != "pull_request_open" ||
		msg != "The pull request is already open." || w.storedChoice() != "project" {
		t.Fatalf("with a pull request open: %d %s %q, stored %s", code, errCode, msg, w.storedChoice())
	}
}
