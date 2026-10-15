package agenttools_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// feedbackFixture is the fixture task with Ana's submitted item and her
// private draft, and a repository the task changes.
func feedbackFixture(t *testing.T) *fixture {
	t.Helper()
	f := setup(t)
	mustExec(t, f.owner, `INSERT INTO people (id, organization_id, name) VALUES ('per_ana_'||$1, $1, 'Ana')`, f.org)
	mustExec(t, f.owner, `INSERT INTO repositories (id, organization_id, project_id, name, url) VALUES ('repo_'||$1, $1, $2, 'api', 'https://github.com/acme/api')`, f.org, f.project)
	mustExec(t, f.owner, `INSERT INTO task_repositories (task_id, repository_id, organization_id, access) VALUES ($1, 'repo_'||$2, $2, 'write')`, f.item, f.org)
	mustExec(t, f.owner, `INSERT INTO feedback_batches (id, organization_id, task_id, attempt, author_person_id, submission_key)
		VALUES ('fbt_1', $1, $2, 1, 'per_ana_'||$1, 'k')`, f.org, f.item)
	mustExec(t, f.owner, `INSERT INTO feedback_items (id, organization_id, task_id, attempt, batch_id, author_person_id, body, anchor_kind, anchor, status, submitted_at) VALUES
		('fbi_open', $1, $2, 1, 'fbt_1', 'per_ana_'||$1, 'Name the flag --dry-run, as git does.', 'general', '{"kind":"general"}', 'open', now()),
		('fbi_two', $1, $2, 1, 'fbt_1', 'per_ana_'||$1, 'And show it in --help.', 'general', '{"kind":"general"}', 'open', now())`, f.org, f.item)
	mustExec(t, f.owner, `INSERT INTO feedback_items (id, organization_id, task_id, attempt, author_person_id, body, anchor_kind, anchor)
		VALUES ('fbi_draft', $1, $2, 1, 'per_ana_'||$1, 'secret draft words', 'general', '{"kind":"general"}')`, f.org, f.item)
	return f
}

// An implementer lists the open items in their words, never a draft,
// replies to one and resolves the other with HEAD from its checkout; a
// reviewer has none of it, and the conductor reads and replies but does
// not resolve.
func TestFeedbackFromTheShellByRole(t *testing.T) {
	f := feedbackFixture(t)
	token := f.run(t, "run_fix", "implementer", "running")
	bin := cli(t)
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.email=a@b", "-c", "user.name=a", "commit", "-q", "--allow-empty", "-m", "fix"}} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	head, _ := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	env := append(os.Environ(), "LUX_SERVICE_DUDE="+luxService(t, f.url, token), "DUDE_TOOLS_TOKEN=", "DUDE_TOOLS_URL=")
	dude := func(args ...string) (string, error) {
		cmd := exec.Command(bin, args...)
		cmd.Env, cmd.Dir = env, repo
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		err := cmd.Run()
		return out.String(), err
	}
	out, err := dude("feedback")
	if err != nil || !strings.Contains(out, "Name the flag --dry-run, as git does.") || !strings.Contains(out, `"fbi_two"`) ||
		strings.Contains(out, "fbi_draft") || strings.Contains(out, "secret draft") {
		t.Fatalf("dude feedback: %v\n%s", err, out)
	}
	if out, err := dude("feedback", "fbi_draft"); err != nil || !strings.Contains(out, `"unknown"`) || strings.Contains(out, "secret draft") {
		t.Errorf("a draft by id: %v\n%s", err, out)
	}
	if out, err := dude("feedback", "reply", "fbi_two", "The help already lists every flag"); err != nil || !strings.Contains(out, `"replied"`) {
		t.Errorf("reply: %v\n%s", err, out)
	}
	if out, err := dude("feedback", "resolve", "fbi_open", "--commit", "HEAD"); err != nil || !strings.Contains(out, strings.TrimSpace(string(head))) {
		t.Errorf("resolve HEAD: %v\n%s", err, out)
	}
	if out, err := dude("feedback", "resolve", "fbi_draft", "--commit", "HEAD"); err == nil {
		t.Errorf("resolving a draft: %s", out)
	}
	if out, err := dude("feedback", "resolve", "fbi_two", "--commit", "nosuchref"); err == nil || !strings.Contains(out, "git does not know") {
		t.Errorf("an unknown commit: %v %s", err, out)
	}
	var status, commit string
	_ = f.owner.QueryRow(context.Background(), `SELECT status, resolved_commit FROM feedback_items WHERE id = 'fbi_open'`).Scan(&status, &commit)
	if status != "resolved" || commit != strings.TrimSpace(string(head)) {
		t.Errorf("fbi_open: %s %s", status, commit)
	}
	// A refused call never records the draft's words.
	var leaked int
	_ = f.owner.QueryRow(context.Background(), `SELECT count(*) FROM events WHERE payload::text LIKE '%secret draft%'`).Scan(&leaked)
	if leaked != 0 {
		t.Error("the ledger has a draft's words")
	}

	reviewer := f.run(t, "run_rev", "reviewer", "running")
	var res map[string]any
	for _, tool := range []string{"feedback", "feedback_reply", "feedback_resolve"} {
		if status := f.postAs(t, reviewer, tool, `{}`, &res); status != 404 {
			t.Errorf("a reviewer's %s: %d", tool, status)
		}
	}
	conductor := f.conductor(t)
	if status := f.postAs(t, conductor, "feedback", `{"all":true}`, &res); status != 200 {
		t.Errorf("the conductor's feedback: %d %v", status, res)
	}
	if status := f.postAs(t, conductor, "feedback_resolve", `{"id":"fbi_two","commit":"abcdef1"}`, &res); status != 404 {
		t.Errorf("the conductor's resolve: %d", status)
	}
}

// The conductor passes items to a fixer by id: dude writes their words
// into the steer, never a draft, never to a reviewer.
func TestTheConductorPassesFeedbackByIDInThePersonsWords(t *testing.T) {
	f := feedbackFixture(t)
	conductor := f.conductor(t)
	f.run(t, "run_fix", "implementer", "running")
	f.run(t, "run_rev", "reviewer", "running")
	var res map[string]any
	if status := f.postAs(t, conductor, "steer", `{"run":"run_fix","text":"Start with these.","feedbackIds":["fbi_open"]}`, &res); status != 200 {
		t.Fatalf("steer with feedback: %d %v", status, res)
	}
	var text string
	var ids []string
	_ = f.owner.QueryRow(context.Background(), `SELECT d.text, r.feedback_item_ids FROM directives d JOIN runs r ON r.id = d.run_id
		WHERE d.run_id = 'run_fix'`).Scan(&text, &ids)
	if !strings.HasPrefix(text, "Start with these.") || !strings.Contains(text, "### fbi_open — Ana") ||
		!strings.Contains(text, "> Name the flag --dry-run, as git does.") || strings.Contains(text, "fbi_two") {
		t.Errorf("the steer:\n%s", text)
	}
	if len(ids) != 1 || ids[0] != "fbi_open" {
		t.Errorf("the fixer's items: %v", ids)
	}
	for body, why := range map[string]string{
		`{"run":"run_rev","feedbackIds":["fbi_open"]}`:  "a reviewer",
		`{"run":"run_fix","feedbackIds":["fbi_draft"]}`: "a draft",
		`{"run":"run_fix","feedbackIds":["find_x"]}`:    "a finding's id",
	} {
		if status := f.postAs(t, conductor, "steer", body, &res); status == 200 {
			t.Errorf("steering %s's feedback was accepted", why)
		}
	}
	var n int
	_ = f.owner.QueryRow(context.Background(), `SELECT count(*) FROM directives WHERE text LIKE '%secret draft%' OR run_id = 'run_rev'`).Scan(&n)
	if n != 0 {
		t.Error("a refused steer was queued")
	}
}
