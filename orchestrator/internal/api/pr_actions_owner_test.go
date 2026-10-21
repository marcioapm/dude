package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

type oneForge struct{ gh *forge.GitHub }

func (f oneForge) For(context.Context, string) (*forge.GitHub, error) { return f.gh, nil }

// prActionWorld is an organisation with one open pull request on the fake
// GitHub, its task owned by per_owner; per_admin is an admin, per_member
// neither.
type prActionWorld struct {
	t     *testing.T
	owner *pgx.Conn
	org   string
	gh    *fakegithub.Server
	h     http.Handler
}

func newPRActionWorld(t *testing.T) *prActionWorld {
	t.Helper()
	app, owner := dbtest.Open(t)
	org := dbtest.Org(t, owner)
	w := &prActionWorld{t: t, owner: owner, org: org}
	w.exec(`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_pr', $1, 'P', 'p', 'P')`, org)
	w.exec(`INSERT INTO people (id, organization_id, name, role) VALUES ('per_owner', $1, 'Olga', 'member'),
		('per_admin', $1, 'Ada', 'admin'), ('per_member', $1, 'Mo', 'member')`, org)
	w.exec(`INSERT INTO tasks (id, organization_id, project_id, number, title) VALUES ('task_pr', $1, 'prj_pr', 1, 'Task')`, org)
	w.exec(`DELETE FROM task_people WHERE task_id = 'task_pr'`)
	w.exec(`INSERT INTO task_people (task_id, person_id, organization_id, position) VALUES ('task_pr', 'per_owner', $1, 0)`, org)
	w.exec(`INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch)
		VALUES ('repo_pr', $1, 'prj_pr', 'repo', 'https://github.com/acme/repo.git', 'main')`, org)

	repo := t.TempDir()
	git := func(args ...string) string {
		out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "--bare", "--initial-branch=main")
	git("update-ref", "refs/heads/main", git("-c", "user.name=A", "-c", "user.email=a@example.com", "commit-tree", git("mktree"), "-m", "Initial"))
	w.gh = fakegithub.New(repo, "acme/repo")
	// Each action would go through on GitHub: approved and green now, with a
	// failed check run recorded to re-run.
	number := w.gh.OpenAsPerson("alice", "topic", "main", "Topic")
	w.gh.Review(number, "bo", "APPROVED")
	w.exec(`INSERT INTO pull_requests (id, organization_id, project_id, task_id, repository_id, number, url,
			head_branch, base_branch, head_sha, title, checks_json)
		VALUES ('pr_one', $1, 'prj_pr', 'task_pr', 'repo_pr', $2, 'https://github.com/acme/repo/pull/1', 'topic', 'main', $3, 'Topic',
			'[{"name":"ci","status":"completed","conclusion":"failure","runId":5}]')`,
		org, number, w.gh.SHA("topic"))
	srv := httptest.NewServer(w.gh.Handler())
	t.Cleanup(srv.Close)
	s := &Server{DB: app, Token: "svc", Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Forges: oneForge{forge.NewGitHub(forge.Credential{APIBaseURL: srv.URL})}}
	w.h = s.Handler()
	return w
}

func (w *prActionWorld) exec(q string, args ...any) {
	w.t.Helper()
	if _, err := w.owner.Exec(context.Background(), q, args...); err != nil {
		w.t.Fatal(err)
	}
}

// act posts action on the pull request as person; the status and the
// error's code and message, if any.
func (w *prActionWorld) act(person, action, body string) (int, string, string) {
	w.t.Helper()
	r := httptest.NewRequest("POST", "/internal/pull-requests/pr_one/"+action, strings.NewReader(body))
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

func (w *prActionWorld) actions() int {
	w.t.Helper()
	var n int
	if err := w.owner.QueryRow(context.Background(), `SELECT count(*) FROM events
		WHERE task_id = 'task_pr' AND event_type = 'pull_request.action'`).Scan(&n); err != nil {
		w.t.Fatal(err)
	}
	return n
}

// prActionBodies is a body each action would otherwise go on to GitHub with.
var prActionBodies = []struct{ action, body, verb string }{
	{"merge", `{}`, "merge"},
	{"update-branch", `{}`, "update the branch of"},
	{"rerun-failed", `{}`, "re-run the failed checks of"},
	{"reviewers", `{"logins":["cy"]}`, "ask for reviewers on"},
}

// A member who neither owns the task nor administers the organisation is
// refused each action GitHub would do as dude's token, and GitHub is not
// asked anything. A world each, so no action's outcome hides another's.
func TestPullRequestActionsRefuseAMemberWhoIsNotOwnerOrAdmin(t *testing.T) {
	for _, c := range prActionBodies {
		t.Run(c.action, func(t *testing.T) {
			w := newPRActionWorld(t)
			code, errCode, msg := w.act("per_member", c.action, c.body)
			want := "only the task's owner or an organisation admin can " + c.verb + " this"
			if code != http.StatusForbidden || errCode != "not_owner" || msg != want {
				t.Errorf("%d %s %q, want 403 not_owner %q", code, errCode, msg, want)
			}
			if reqs := w.gh.Requests(); len(reqs) != 0 {
				t.Errorf("GitHub was asked %v", reqs)
			}
			w.gh.Set(func(s *fakegithub.Server) {
				if len(s.Merges) != 0 || len(s.Updates) != 0 || len(s.JobsRerun) != 0 || len(s.Rerequested) != 0 || len(s.ReviewRequests) != 0 {
					t.Errorf("GitHub acted: merges %v updates %v reruns %v %v reviews %v",
						s.Merges, s.Updates, s.JobsRerun, s.Rerequested, s.ReviewRequests)
				}
			})
			if n := w.actions(); n != 0 {
				t.Errorf("%d actions recorded for a refused request", n)
			}
		})
	}
}

// An action outside the guarded set is not found, so it cannot skip the
// owner check on its way to GitHub or the event log.
func TestPullRequestActionsUnknownActionIsNotFound(t *testing.T) {
	w := newPRActionWorld(t)
	code, errCode, msg := w.act("per_member", "frobnicate", `{}`)
	want := `no pull request action "frobnicate"`
	if code != http.StatusNotFound || errCode != "not_found" || msg != want {
		t.Errorf("%d %s %q, want 404 not_found %q", code, errCode, msg, want)
	}
	if reqs := w.gh.Requests(); len(reqs) != 0 {
		t.Errorf("GitHub was asked %v", reqs)
	}
	if n := w.actions(); n != 0 {
		t.Errorf("%d actions recorded for an unknown action", n)
	}
}

// githubActed is how many times GitHub did what action asks.
func (w *prActionWorld) githubActed(action string) int {
	w.t.Helper()
	var n int
	w.gh.Set(func(s *fakegithub.Server) {
		switch action {
		case "merge":
			n = len(s.Merges)
		case "update-branch":
			n = len(s.Updates)
		case "rerun-failed":
			n = len(s.JobsRerun) + len(s.Rerequested)
		case "reviewers":
			n = len(s.ReviewRequests)
		default:
			w.t.Fatalf("no count for %s", action)
		}
	})
	return n
}

// An admin who does not own the task may take each action, and GitHub does it.
func TestPullRequestActionsAllowAnAdminEach(t *testing.T) {
	for _, c := range prActionBodies {
		t.Run(c.action, func(t *testing.T) {
			w := newPRActionWorld(t)
			if code, errCode, msg := w.act("per_admin", c.action, c.body); code != http.StatusOK {
				t.Fatalf("%d %s %s", code, errCode, msg)
			}
			if n := w.githubActed(c.action); n == 0 {
				t.Errorf("GitHub did not %s", c.verb)
			}
		})
	}
}

// Who may act is answered before whether the pull request still can be:
// a non-owner learns only that it is not theirs.
func TestPullRequestActionsOnAMergedPullRequestRefuseANonOwnerFirst(t *testing.T) {
	w := newPRActionWorld(t)
	w.exec(`UPDATE pull_requests SET state = 'merged' WHERE id = 'pr_one'`)
	if code, errCode, _ := w.act("per_member", "reviewers", `{"logins":["cy"]}`); code != http.StatusForbidden || errCode != "not_owner" {
		t.Errorf("a member: %d %s, want 403 not_owner", code, errCode)
	}
	if code, errCode, msg := w.act("per_owner", "reviewers", `{"logins":["cy"]}`); code != http.StatusConflict || msg != "the pull request is merged" {
		t.Errorf("the owner: %d %s %q, want 409 %q", code, errCode, msg, "the pull request is merged")
	}
	if reqs := w.gh.Requests(); len(reqs) != 0 {
		t.Errorf("GitHub was asked %v", reqs)
	}
}

// The task's owner and an admin who is not may each act, and each is recorded.
func TestPullRequestActionsAllowTheOwnerAndAnAdmin(t *testing.T) {
	w := newPRActionWorld(t)
	for i, person := range []string{"per_owner", "per_admin"} {
		if code, errCode, msg := w.act(person, "reviewers", `{"logins":["cy"]}`); code != http.StatusOK {
			t.Fatalf("%s: %d %s %s", person, code, errCode, msg)
		}
		if asked := w.githubActed("reviewers"); asked != i+1 {
			t.Fatalf("after %s, GitHub asked for reviews %d times", person, asked)
		}
	}
	if n := w.actions(); n != 2 {
		t.Errorf("%d actions recorded, want 2", n)
	}
}

// A task with no active owner is the admins' alone.
func TestPullRequestActionsOnAnUnownedTaskAreTheAdmins(t *testing.T) {
	w := newPRActionWorld(t)
	w.exec(`UPDATE people SET removed_at = now() WHERE id = 'per_owner'`)
	if code, errCode, _ := w.act("per_member", "reviewers", `{"logins":["cy"]}`); code != http.StatusForbidden || errCode != "not_owner" {
		t.Fatalf("a member on an unowned task: %d %s", code, errCode)
	}
	if reqs := w.gh.Requests(); len(reqs) != 0 {
		t.Fatalf("GitHub was asked %v", reqs)
	}
	if n := w.actions(); n != 0 {
		t.Fatalf("%d actions recorded for a refused request", n)
	}
	if code, errCode, msg := w.act("per_admin", "reviewers", `{"logins":["cy"]}`); code != http.StatusOK {
		t.Fatalf("an admin on an unowned task: %d %s %s", code, errCode, msg)
	}
	if asked := w.githubActed("reviewers"); asked != 1 {
		t.Errorf("GitHub asked for reviews %d times, want 1", asked)
	}
}
