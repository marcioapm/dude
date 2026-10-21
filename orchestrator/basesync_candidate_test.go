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
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

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

// allowUpdateBranch lets prShowsOnlyChecked pass a base merge GitHub made
// on the branch by update-branch.
type prShowsOption int

const allowUpdateBranch prShowsOption = 1

// prShowsOnlyChecked watches every forge write that moves branch in repo
// from now on (a ref update, a ref create, POST /merges into it) and
// records, as each is asked, what was not yet done at its SHA (checkedAt).
// check fails the test unless every head the branch moved to since was a
// SHA reviewed (and with test, tested) when asked, or GitHub's merge of
// such a SHA onto the branch (its second parent). A move by update-branch
// fails it too unless allowUpdateBranch is given. It chains to any
// Intercept already set, and a later one set by the test replaces it.
func (w *world) prShowsOnlyChecked(t *testing.T, task, repo, branch string, test bool, opts ...prShowsOption) (check func()) {
	t.Helper()
	gh := map[string]*fakegithub.Server{"target": w.gh, "web": w.web}[repo]
	if gh == nil {
		t.Fatalf("no fake GitHub for %s", repo)
	}
	prefix := "/repos/" + gh.Slug
	before := len(gh.BranchLog(branch))
	var mu sync.Mutex
	asked := map[string]string{} // SHA → what was missing at it
	updates := 0
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
						mu.Lock()
						updates++
						mu.Unlock()
					}
				}
			}
			if sha != "" {
				missing := w.checkedAt(task, repo, sha, test)
				mu.Lock()
				asked[sha] = missing
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
		unasked := updates // moves only update-branch requests can account for
		for _, head := range gh.BranchLog(branch)[before:] {
			at := head
			missing, ok := asked[head]
			if parents := gh.Parents(head); !ok && len(parents) == 2 {
				at = parents[1]
				missing, ok = asked[at]
			}
			switch {
			case ok && missing != "":
				t.Fatalf("%s's %s moved to %s before its %s completed at %s", repo, branch, head, missing, at)
			case ok:
			case unasked > 0 && slices.Contains(opts, allowUpdateBranch):
				unasked--
			case unasked > 0:
				t.Fatalf("%s's %s moved to %s by update-branch, a base merge nobody reviewed", repo, branch, head)
			default:
				t.Fatalf("%s's %s moved to %s by no write dude asked for", repo, branch, head)
			}
		}
	}
}

// readBody decodes r's JSON body into v and leaves it for the handler.
func readBody(r *http.Request, v any) {
	body, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(body))
	_ = json.Unmarshal(body, v)
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
