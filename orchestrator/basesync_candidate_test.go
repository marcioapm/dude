package orchestrator_test

// An open pull request's integration is checked before the pull request
// shows it: the merge an integration fixer wrote, and every fix of its
// review and test, go to the candidate branch, and the pull request's
// branch moves once, to a head that was reviewed (and tested, when the
// policy tests).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakeagent"
	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

// checkedAt says what dude had not finished at sha in the task's repo
// when it was asked: "" when a review Run, and with test a test Run,
// completed on that exact commit.
func (w *world) checkedAt(wi, repo, sha string, test bool) string {
	var reviewed, tested bool
	if err := w.app.InOrg(context.Background(), w.org, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `SELECT
			EXISTS (SELECT 1 FROM runs WHERE task_id = $1 AND phase = 'review' AND status = 'completed' AND base_refs->>$3 = $2),
			EXISTS (SELECT 1 FROM runs WHERE task_id = $1 AND phase = 'test' AND status = 'completed' AND base_refs->>$3 = $2)`,
			wi, sha, repo).Scan(&reviewed, &tested)
	}); err != nil {
		return err.Error()
	}
	var missing []string
	if !reviewed {
		missing = append(missing, "review")
	}
	if test && !tested {
		missing = append(missing, "test")
	}
	return strings.Join(missing, " and ")
}

type prShowsOption int

// allowUpdateBranch lets prShowsOnlyChecked pass a base merge GitHub made
// on the branch by an update-branch request that moved it.
const allowUpdateBranch prShowsOption = 1

// prShowsOnlyChecked watches every forge write that moves branch in repo
// from now on (a ref update, a ref create, POST /merges into it) and
// records, as each is asked, what was not yet done at its SHA (checkedAt);
// the first unfinished answer for a SHA stands, so a later request for it
// cannot hide an earlier move. check fails the test unless every head the
// branch moved to since was a SHA reviewed (and with test, tested) when
// asked, or GitHub's merge of such a SHA onto the branch (its second
// parent), and the branch is still at the last of them: a git push, which
// the fake's BranchLog does not record, fails it too. A move by
// update-branch (a merge of the base the request saw onto the head it saw)
// fails it unless allowUpdateBranch is given. It chains to any Intercept
// already set, and a later one set by the test replaces it.
func (w *world) prShowsOnlyChecked(t testing.TB, task, repo, branch string, test bool, opts ...prShowsOption) (check func()) {
	t.Helper()
	gh := map[string]*fakegithub.Server{"target": w.gh, "web": w.web}[repo]
	if gh == nil {
		t.Fatalf("no fake GitHub for %s", repo)
	}
	prefix := "/repos/" + gh.Slug
	before, start := len(gh.BranchLog(branch)), gh.SHA(branch)
	var mu sync.Mutex
	asked := map[string]string{} // SHA → what was missing at it
	var updates [][2]string      // update-branch requests: the head and base each saw
	var previous func(*http.Request) int
	gh.Set(func(s *fakegithub.Server) {
		previous = s.Intercept
		s.Intercept = func(r *http.Request) int {
			var sha string
			switch {
			case r.Method == http.MethodPatch && r.URL.Path == prefix+"/git/refs/heads/"+branch:
				var in struct{ SHA string }
				readBody(r, &in)
				sha = in.SHA
			case r.Method == http.MethodPost && r.URL.Path == prefix+"/git/refs":
				var in struct{ Ref, SHA string }
				if readBody(r, &in); in.Ref == "refs/heads/"+branch {
					sha = in.SHA
				}
			case r.Method == http.MethodPost && r.URL.Path == prefix+"/merges":
				var in struct{ Base, Head string }
				if readBody(r, &in); in.Base == branch {
					sha = in.Head
				}
			case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, prefix+"/pulls/") && strings.HasSuffix(r.URL.Path, "/update-branch"):
				var n int
				if _, err := fmt.Sscanf(strings.TrimPrefix(r.URL.Path, prefix+"/pulls/"), "%d/update-branch", &n); err == nil {
					if p := gh.Pull(n); p != nil && p.Head == branch {
						seen := [2]string{gh.SHA(branch), gh.SHA(p.Base)}
						mu.Lock()
						updates = append(updates, seen)
						mu.Unlock()
					}
				}
			}
			if sha != "" {
				missing := w.checkedAt(task, repo, sha, test)
				mu.Lock()
				if prev, ok := asked[sha]; !ok || prev == "" {
					asked[sha] = missing
				}
				mu.Unlock()
			}
			if previous != nil {
				return previous(r)
			}
			return 0
		}
	})
	return func() {
		t.Helper()
		mu.Lock()
		defer mu.Unlock()
		last := start
		for _, head := range gh.BranchLog(branch)[before:] {
			last = head
			at := head
			missing, ok := asked[head]
			update := -1
			if parents := gh.Parents(head); !ok && len(parents) == 2 {
				at = parents[1]
				// An update-branch that moved the branch made exactly this
				// merge; one GitHub refused made none, and excuses nothing.
				if missing, ok = asked[at]; !ok {
					update = slices.Index(updates, [2]string{parents[0], parents[1]})
				}
			}
			switch {
			case ok && missing != "":
				t.Fatalf("%s's %s moved to %s before its %s completed at %s", repo, branch, head, missing, at)
			case ok:
			case update >= 0 && slices.Contains(opts, allowUpdateBranch):
				updates = slices.Delete(updates, update, update+1)
			case update >= 0:
				t.Fatalf("%s's %s moved to %s by update-branch, a base merge nobody reviewed", repo, branch, head)
			default:
				t.Fatalf("%s's %s moved to %s by no write dude asked for", repo, branch, head)
			}
		}
		if cur := gh.SHA(branch); cur != last {
			t.Fatalf("%s's %s is at %s, which no API write moved it to (a git push)", repo, branch, cur)
		}
	}
}

// readBody decodes r's JSON body into v and leaves it for the handler.
func readBody(r *http.Request, v any) {
	body, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(body))
	_ = json.Unmarshal(body, v)
}

// fatalRecorder stands in for the test given to prShowsOnlyChecked: it
// records the first Fatalf and ends the goroutine, as t.Fatalf does.
type fatalRecorder struct {
	testing.TB
	msg string
}

func (f *fatalRecorder) Helper() {}
func (f *fatalRecorder) Fatalf(format string, a ...any) {
	if f.msg == "" {
		f.msg = fmt.Sprintf(format, a...)
	}
	runtime.Goexit()
}

// prShowsOnlyChecked against the fake GitHub alone: each case moves a
// branch of its own through the API (or around it) and reads what check
// says. No agent and no workflow run.
func TestPRShowsOnlyCheckedFailsWhatWasNotChecked(t *testing.T) {
	w := newWorld(t)
	wi := w.task()
	n := 0
	reviewed := func(sha string) {
		n++
		mustExec(t, w.owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, base_refs)
			VALUES ($1, $2, $3, $4, 1, 'completed', 'review', 'reviewer', jsonb_build_object('target', $5::text))`,
			fmt.Sprintf("run_%s_%d", wi, n), w.org, w.project, wi, sha)
	}
	api := func(method, path string, body any) int {
		b, _ := json.Marshal(body)
		req, _ := http.NewRequest(method, w.ghURL+"/repos/acme/target"+path, bytes.NewReader(b))
		req.Header.Set("Authorization", "token ghp_test")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	// ahead is a commit on top of branch, on a ref of its own.
	ahead := func(branch, name string) string { return w.gh.Branch(branch+"-ahead", branch, name+": ahead") }
	// updateBranch moves main and asks GitHub to merge it into the pull request.
	updateBranch := func(branch string, number int) {
		w.gh.AdvanceBase("main", branch+": base")
		api("PUT", fmt.Sprintf("/pulls/%d/update-branch", number), map[string]any{"expected_head_sha": w.gh.SHA(branch)})
	}
	for _, tc := range []struct {
		name string
		// pr opens a pull request from the branch (update-branch needs one).
		pr   bool
		opts []prShowsOption
		move func(branch string, number int)
		want string // a substring of check's failure; "" passes
	}{
		// Arm "before its … completed", by the SHA itself.
		{name: "an unreviewed fast-forward", move: func(branch string, _ int) {
			api("PATCH", "/git/refs/heads/"+branch, map[string]any{"sha": ahead(branch, "unreviewed")})
		}, want: "before its review completed"},
		// The same arm: asked is read at the request, not at check.
		{name: "a review recorded only after the move", move: func(branch string, _ int) {
			sha := ahead(branch, "late")
			api("PATCH", "/git/refs/heads/"+branch, map[string]any{"sha": sha})
			reviewed(sha)
		}, want: "before its review completed"},
		// The same arm: a later request for the SHA, once reviewed, does
		// not hide the earlier unreviewed move.
		{name: "an unreviewed move asked again once reviewed", move: func(branch string, _ int) {
			sha := ahead(branch, "again")
			api("PATCH", "/git/refs/heads/"+branch, map[string]any{"sha": sha})
			reviewed(sha)
			api("PATCH", "/git/refs/heads/"+branch, map[string]any{"sha": sha})
		}, want: "before its review completed"},
		// Arm "ok", by the SHA itself.
		{name: "a reviewed fast-forward", move: func(branch string, _ int) {
			sha := ahead(branch, "reviewed")
			reviewed(sha)
			api("PATCH", "/git/refs/heads/"+branch, map[string]any{"sha": sha})
		}},
		// The final check: the branch is not where the API last moved it.
		{name: "a git push", move: func(branch string, _ int) {
			w.gh.CommitOnTop(branch, "pushed")
		}, want: "which no API write moved it to"},
		// Arm "by no write dude asked for": a move the API logged but the
		// helper never saw (a later Intercept replaced it).
		{name: "a write the helper did not see", move: func(branch string, _ int) {
			sha := ahead(branch, "unseen")
			reviewed(sha)
			w.gh.Set(func(s *fakegithub.Server) { s.Intercept = nil })
			api("PATCH", "/git/refs/heads/"+branch, map[string]any{"sha": sha})
		}, want: "by no write dude asked for"},
		// Arm "ok" by the merge's second parent.
		{name: "POST /merges of a reviewed head", move: func(branch string, _ int) {
			sha := w.gh.Branch(branch+"-side", "main", branch+": reviewed side")
			reviewed(sha)
			api("POST", "/merges", map[string]any{"base": branch, "head": sha})
		}},
		// Arm "before its … completed" by the merge's second parent.
		{name: "POST /merges of an unreviewed head", move: func(branch string, _ int) {
			sha := w.gh.Branch(branch+"-side", "main", branch+": unreviewed side")
			api("POST", "/merges", map[string]any{"base": branch, "head": sha})
		}, want: "before its review completed"},
		// Arm "by update-branch".
		{name: "update-branch", pr: true, move: updateBranch, want: "by update-branch"},
		// Arm "update-branch allowed".
		{name: "update-branch allowed", pr: true, opts: []prShowsOption{allowUpdateBranch}, move: updateBranch},
		// An update-branch GitHub refused moved nothing: it excuses no
		// other move, even with allowUpdateBranch.
		{name: "a refused update-branch", pr: true, opts: []prShowsOption{allowUpdateBranch}, move: func(branch string, number int) {
			w.gh.AdvanceBase("main", branch+": base")
			if status := api("PUT", fmt.Sprintf("/pulls/%d/update-branch", number), map[string]any{"expected_head_sha": "0000000"}); status != 422 {
				t.Errorf("a stale update-branch answered %d, want 422", status)
			}
			sha := ahead(branch, "after the refusal")
			w.gh.Set(func(s *fakegithub.Server) { s.Intercept = nil })
			api("PATCH", "/git/refs/heads/"+branch, map[string]any{"sha": sha})
		}, want: "by no write dude asked for"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			branch := fmt.Sprintf("dude/%s/case-%d", wi, time.Now().UnixNano())
			number := 0
			if tc.pr {
				number = w.gh.OpenAsPerson("alice", branch, "main", tc.name)
			} else {
				w.gh.Branch(branch, "main", tc.name)
			}
			w.gh.Set(func(s *fakegithub.Server) { s.Intercept = nil })
			rec := &fatalRecorder{TB: t}
			check := w.prShowsOnlyChecked(rec, wi, "target", branch, false, tc.opts...)
			tc.move(branch, number)
			done := make(chan struct{})
			go func() { defer close(done); check() }()
			<-done
			switch {
			case tc.want == "" && rec.msg != "":
				t.Fatalf("check failed: %s", rec.msg)
			case tc.want != "" && !strings.Contains(rec.msg, tc.want):
				t.Fatalf("check said %q, want it to fail with %q", rec.msg, tc.want)
			}
		})
	}
}

func TestAnIntegrationReachesThePullRequestOnlyOnceReviewedAndTested(t *testing.T) {
	for _, test := range []bool{true, false} {
		t.Run(fmt.Sprintf("test=%v", test), func(t *testing.T) {
			w := newWorld(t)
			w.toolsWithSync()
			wi := w.task()
			w.names(wi, w.repoID)
			p := scriptedPolicy()
			p.Test = test
			w.start(wi, p)
			w.until("the pull request waiting on people", func() bool {
				return len(w.gh.Pulls()) == 1 && w.taskStatus(wi) == "review"
			})
			branch := w.gh.Pull(1).Head
			// A standing approval: on a repository that keeps approvals
			// across pushes, whatever head the branch shows can be merged.
			w.gh.Review(1, "ana", "APPROVED")
			approved := w.gh.SHA(branch)
			published := len(w.gh.BranchLog(branch))
			check := w.prShowsOnlyChecked(t, wi, "target", branch, test)
			w.conflictOnFactory()

			// Every head the pull request shows meanwhile, read at each pass.
			shown := []string{approved}
			w.until("the integration promoted and the pull request waiting again", func() bool {
				w.sync()
				if r := w.escalationReason(wi); r != "" {
					t.Fatalf("escalated: %s", r)
				}
				if h := w.gh.SHA(branch); h != shown[len(shown)-1] {
					shown = append(shown, h)
				}
				return w.count(integrationCompleted, wi) == 1 &&
					w.count(`SELECT count(*) FROM workflow_runs WHERE task_id=$1 AND step='awaitPullRequest' AND status='waiting'`, wi) == 1
			})

			moves := w.gh.BranchLog(branch)[published:]
			if len(moves) != 1 {
				t.Fatalf("the pull request's branch moved %d times during the integration (%v), want one promotion", len(moves), moves)
			}
			head := moves[0]
			check()
			// Fast-forwarded to the checked head itself, not a merge onto it.
			if missing := w.checkedAt(wi, "target", head, test); missing != "" {
				t.Fatalf("the branch moved to %s, which has no %s of its own", head, missing)
			}
			for _, h := range shown {
				if h != approved && h != head {
					t.Fatalf("the pull request showed %s, neither the approved head nor the promoted one", h)
				}
			}
			if out := gitOut(t, w.gh.Repo, "rev-list", "--count", head+"..main"); out != "0" {
				t.Fatalf("the promoted head lacks the base: %s behind", out)
			}
			if out := gitOut(t, w.gh.Repo, "rev-list", "--count", head+".."+approved); out != "0" {
				t.Fatalf("the promoted head %s does not descend from the approved one", head)
			}
			if n := w.count(`SELECT count(*) FROM pull_requests WHERE task_id=$1 AND mergeable_state='clean' AND head_sha=$2`, wi, head); n != 1 {
				t.Fatalf("the pull request is not recorded clean at the promoted head")
			}
			if n := w.count(integrationRuns+` AND branch LIKE '%-candidate'`, wi); n != 1 {
				t.Fatalf("integration Runs on the candidate %d, want 1", n)
			}
		})
	}
}

// A person's push during integration refuses promotion and keeps the
// conflict comment for the fix after they choose to wait.
func TestAnIntegrationPersonPushKeepsConflictFeedback(t *testing.T) {
	w := newWorld(t)
	w.toolsWithSync()
	var once sync.Once
	var person, branch string
	armed := w.gated(func(step, next string) {
		if step == "awaitPRFix" && next == "review" {
			once.Do(func() { person = w.commitFileIn(w.gh.Repo, branch, "PERSON.md", "mine\n", "A person's push") })
		}
	})
	wi := w.reviewing()
	branch = w.gh.Pull(1).Head
	w.conflictWithMovedBase()
	w.gh.Comment(1, "alice", "Please keep this comment.")
	armed.Store(true)
	w.until("refused", func() bool { w.sync(); return w.escalationReason(wi) != "" })
	if r := w.escalationReason(wi); r != delivery.ReasonPullRequestMoved {
		t.Fatalf("escalated %q", r)
	}
	if got := w.gh.SHA(branch); got != person || person == "" {
		t.Fatalf("PR at %s, want the person's %s", got, person)
	}
	if n := w.count(integrationCompleted, wi); n != 0 {
		t.Fatalf("integration-completed %d on a refused promotion", n)
	}
	if code, body := w.decide(w.person("owner"), wi, "wait", ""); code != 200 {
		t.Fatal(code, body)
	}
	w.until("a fix given the comment", func() bool {
		w.sync()
		return w.count(`SELECT count(*) FROM runs WHERE task_id=$1 AND phase='fix' AND pr_feedback::text LIKE '%keep this comment%'`, wi) == 1
	})
	if n := w.count(`SELECT count(*) FROM events WHERE task_id=$1 AND event_type='pull_request.action' AND payload->>'action'='integration-refused' AND payload->>'cause'=$2`, wi, delivery.ReasonPullRequestMoved); n != 1 {
		t.Fatalf("integration-refused lines %d", n)
	}
}

// A failed integration test is fixed and tested on the candidate before the
// pull request branch moves to the promoted head.
func TestAnIntegrationTestFixStaysOnCandidate(t *testing.T) {
	w := newWorld(t)
	w.toolsWithSync()
	wi := w.task()
	w.names(wi, w.repoID)
	p := scriptedPolicy()
	p.Test = true
	w.start(wi, p)
	w.until("PR waiting", func() bool { return len(w.gh.Pulls()) == 1 && w.taskStatus(wi) == "review" })
	branch := w.gh.Pull(1).Head
	before := len(w.gh.BranchLog(branch))
	w.onModel("qa_browser", fakeagent.TestFailOnceModel)
	w.conflictOnFactory()
	w.until("integration completed", func() bool {
		w.sync()
		if r := w.escalationReason(wi); r != "" {
			t.Fatalf("escalated %s\n%s", r, w.describeRuns())
		}
		return w.count(integrationCompleted, wi) == 1
	})
	moves := w.gh.BranchLog(branch)[before:]
	if len(moves) != 1 {
		t.Fatalf("PR branch moved %d times: %v", len(moves), moves)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id=$1 AND phase='test' AND status='completed' AND base_refs->>'target' = $2`, wi, moves[0]); n != 1 {
		t.Fatalf("tests completed at the promoted head: %d", n)
	}
	if tests := w.count(`SELECT count(*) FROM runs WHERE task_id=$1 AND phase='test' AND creation_key LIKE '%:integration:%'`, wi); tests < 2 {
		t.Fatalf("integration tests %d, want at least 2", tests)
	}
	if fixes := w.count(`SELECT count(*) FROM runs WHERE task_id=$1 AND phase='fix' AND branch LIKE '%-candidate'`, wi); fixes < 2 {
		t.Fatalf("candidate fixes %d, want at least 2", fixes)
	}
}

// A person may accept an integration review after all reviewers failed; the
// accepted candidate is promoted without starting another review round.
func TestAnAcceptedFailedIntegrationReviewIsPromoted(t *testing.T) {
	w := newWorld(t)
	w.toolsWithSync()
	wi := w.reviewing()
	branch := w.gh.Pull(1).Head
	before := w.gh.SHA(branch)
	scripted := w.lux.Decide
	var crashing atomic.Bool
	crashing.Store(true)
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		labels, _ := spec["labels"].(map[string]any)
		if labels["dude.phase"] == "review" && crashing.Load() {
			return fakelux.Behaviour{Crash: true}
		}
		return scripted(spec)
	}
	w.conflictWithMovedBase()
	w.until("review_failed", func() bool { w.sync(); return w.escalationReason(wi) != "" })
	if r := w.escalationReason(wi); r != "review_failed" {
		t.Fatalf("escalated %q", r)
	}
	crashing.Store(false)
	reviews := w.count(`SELECT count(*) FROM runs WHERE task_id=$1 AND phase='review'`, wi)
	if code, body := w.decide(w.person("owner"), wi, "accept", "Read the merge myself."); code != 200 {
		t.Fatal(code, body)
	}
	w.gh.Set(func(s *fakegithub.Server) { s.Conflicting[1] = false })
	w.until("promoted", func() bool { w.sync(); return w.gh.SHA(branch) != before && w.count(integrationCompleted, wi) == 1 })
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id=$1 AND phase='review'`, wi); n != reviews {
		t.Fatalf("%d reviews after the person accepted", n-reviews)
	}
}
