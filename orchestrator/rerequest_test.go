package orchestrator_test

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
)

// reviewRequests is every review request the fake GitHub received.
func (w *world) reviewRequests() []fakegithub.ReviewRequest {
	var out []fakegithub.ReviewRequest
	w.gh.Set(func(s *fakegithub.Server) { out = slices.Clone(s.ReviewRequests) })
	return out
}

// rerequests is the task's pull_request.review_rerequested payloads, oldest first.
func (w *world) rerequests(wi string) []map[string]any {
	rows, err := w.owner.Query(context.Background(), `SELECT payload FROM events WHERE task_id = $1
		AND event_type = 'pull_request.review_rerequested' ORDER BY cursor`, wi)
	if err != nil {
		w.t.Fatal(err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var raw []byte
		var p map[string]any
		if err := rows.Scan(&raw); err != nil {
			w.t.Fatal(err)
		}
		_ = json.Unmarshal(raw, &p)
		out = append(out, p)
	}
	return out
}

// askingFor makes the organization ask logins for a review of every pull
// request it opens.
func (w *world) askingFor(logins ...string) {
	raw, _ := json.Marshal(map[string]any{"requestReviewFrom": "logins", "reviewLogins": logins})
	mustExec(w.t, w.owner, `UPDATE forge_credentials SET settings = $2::jsonb WHERE organization_id = $1`, w.org, raw)
}

// prFixPushed has a collaborator ask for a change and waits until dude's
// fix is the pull request's head and the task is back with people (in
// review, or ready to merge on an approval). Returns the fix's commit.
func (w *world) prFixPushed(wi string) string {
	w.t.Helper()
	branch := w.gh.Pull(1).Head
	before := w.gh.SHA(branch)
	w.gh.Comment(1, "alice", "Please rename the greeting.")
	w.until("the fix pushed", func() bool {
		w.sync()
		s := w.taskStatus(wi)
		return w.fixes(wi) == 1 && w.gh.SHA(branch) != before && (s == "review" || s == "ready_to_merge")
	})
	return w.gh.SHA(branch)
}

func strs(v any) []string {
	var out []string
	list, _ := v.([]any)
	for _, s := range list {
		out = append(out, s.(string))
	}
	return out
}

// The incident: a reviewer that reviews only when asked submitted, which
// takes them off requested_reviewers; dude then pushed a fix. Each reviewer
// of the older head is asked again — the organization's and any other
// person — once; one still asked is not asked twice; the author and a
// review App are never asked.
func TestAFixDudePushesIsPutBeforeItsReviewersAgain(t *testing.T) {
	w := newWorld(t)
	w.askingFor("jerv", "bo")
	wi := w.reviewing()
	if p := w.gh.Pull(1); !slices.Equal(p.Requested, []string{"jerv", "bo"}) {
		t.Fatalf("asked at opening: %v", p.Requested)
	}
	w.gh.Review(1, "jerv", "COMMENTED")
	w.gh.Review(1, "coderabbitai[bot]", "COMMENTED")
	w.gh.Review(1, fakegithub.Login, "COMMENTED") // the author, on their own pull request
	w.gh.Review(1, "ana", "APPROVED")
	// cy reviewed, and someone asked them again already: still owed, not asked twice.
	w.gh.Review(1, "cy", "COMMENTED")
	w.gh.RequestReview(1, "cy")
	if p := w.gh.Pull(1); !slices.Equal(p.Requested, []string{"bo", "cy"}) {
		t.Fatalf("a submitted review satisfies the request: requested %v, want bo and cy", p.Requested)
	}

	head := w.prFixPushed(wi)

	reqs := w.reviewRequests()
	if len(reqs) != 2 || !slices.Equal(reqs[1].Logins, []string{"jerv", "ana"}) {
		t.Fatalf("review requests %v, want the opening's then jerv and ana", reqs)
	}
	if p := w.gh.Pull(1); !slices.Equal(p.Requested, []string{"bo", "cy", "jerv", "ana"}) {
		t.Errorf("requested %v", p.Requested)
	}
	events := w.rerequests(wi)
	if len(events) != 1 || events[0]["head"] != head || !slices.Equal(strs(events[0]["reviewersRequested"]), []string{"jerv", "ana"}) ||
		len(strs(events[0]["reviewersRefused"])) != 0 || events[0]["error"] != nil {
		t.Fatalf("recorded %v", events)
	}

	// Asked once per head, however often the loops run after.
	for range 3 {
		w.sync()
		w.pump()
	}
	if n := len(w.reviewRequests()); n != 2 {
		t.Errorf("%d review requests after more syncs, want 2", n)
	}
	if n := len(w.rerequests(wi)); n != 1 {
		t.Errorf("%d re-request events, want 1", n)
	}
}

// A person pushing to the pull request is theirs to ask for a review of:
// dude asks nobody.
func TestAPersonsPushAsksNobodyAgain(t *testing.T) {
	w := newWorld(t)
	w.askingFor("jerv")
	wi := w.reviewing()
	w.gh.Review(1, "jerv", "COMMENTED")
	w.gh.CommitOnTop(w.gh.Pull(1).Head, "A person's own fix")
	for range 3 {
		w.sync()
		w.pump()
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'pull_request.pushed'`, wi); n != 1 {
		t.Fatalf("%d person pushes recorded", n)
	}
	if reqs := w.reviewRequests(); len(reqs) != 1 {
		t.Errorf("review requests %v, want the opening's only", reqs)
	}
	if e := w.rerequests(wi); len(e) != 0 {
		t.Errorf("recorded %v", e)
	}
}

// Someone pushes on top of dude's fix before dude asks: the head is
// theirs, and dude does not ask for a review of it.
func TestAPushOnTopOfDudesFixIsNotDudesToAskAbout(t *testing.T) {
	w := newWorld(t)
	wi := w.reviewing()
	branch := w.gh.Pull(1).Head
	before := w.gh.SHA(branch)
	w.gh.Review(1, "ana", "APPROVED")
	// The first read of the pull request after dude's fast-forward is the
	// re-request's: a person's commit lands just before it.
	var pushed atomic.Bool
	w.gh.Set(func(s *fakegithub.Server) {
		s.Intercept = func(r *http.Request) int {
			if r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/pulls/1") && !pushed.Load() &&
				w.fixes(wi) == 1 && w.gh.SHA(branch) != before {
				pushed.Store(true)
				w.gh.CommitOnTop(branch, "A person's commit on top")
			}
			return 0
		}
	})
	w.gh.Comment(1, "alice", "Please rename the greeting.")
	w.until("the fix pushed and a person's on top", func() bool {
		w.sync()
		return pushed.Load() && w.taskStatus(wi) != "running"
	})
	for range 3 {
		w.sync()
		w.pump()
	}
	if reqs := w.reviewRequests(); len(reqs) != 0 {
		t.Errorf("review requests %v", reqs)
	}
	if e := w.rerequests(wi); len(e) != 0 {
		t.Errorf("recorded %v", e)
	}
}

// GitHub refusing a review request never fails the push: what it refused
// is recorded with its reason. One login it will not ask costs only that one.
func TestAReviewRequestGitHubRefusesLeavesTheFixPushed(t *testing.T) {
	t.Run("one login", func(t *testing.T) {
		w := newWorld(t)
		w.askingFor("jerv")
		wi := w.reviewing()
		w.gh.Review(1, "jerv", "COMMENTED")
		w.gh.Review(1, "ana", "APPROVED")
		// jerv is no longer a collaborator: GitHub refuses any request naming them.
		w.gh.Set(func(s *fakegithub.Server) { s.Permissions["jerv"] = "none" })
		head := w.prFixPushed(wi)
		e := w.rerequests(wi)
		if len(e) != 1 || e[0]["head"] != head || !slices.Equal(strs(e[0]["reviewersRequested"]), []string{"ana"}) ||
			!slices.Equal(strs(e[0]["reviewersRefused"]), []string{"jerv"}) || !strings.Contains(e[0]["error"].(string), "collaborators") {
			t.Fatalf("recorded %v", e)
		}
		if p := w.gh.Pull(1); !slices.Equal(p.Requested, []string{"ana"}) {
			t.Errorf("requested %v", p.Requested)
		}
	})
	t.Run("all of it", func(t *testing.T) {
		w := newWorld(t)
		w.askingFor("jerv")
		wi := w.reviewing()
		w.gh.Review(1, "jerv", "COMMENTED")
		w.gh.Set(func(s *fakegithub.Server) { s.ReviewRequestStatus = http.StatusForbidden })
		head := w.prFixPushed(wi)
		e := w.rerequests(wi)
		if len(e) != 1 || e[0]["head"] != head || len(strs(e[0]["reviewersRequested"])) != 0 ||
			!slices.Equal(strs(e[0]["reviewersRefused"]), []string{"jerv"}) || !strings.Contains(e[0]["error"].(string), "403") {
			t.Fatalf("recorded %v", e)
		}
		if s := w.taskStatus(wi); s != "review" {
			t.Errorf("task %s, want review", s)
		}
	})
}
