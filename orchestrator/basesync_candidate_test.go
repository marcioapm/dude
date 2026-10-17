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
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
)

// checkedAt says what dude had not finished at sha in the task's target
// repository when it was asked: "" when a review Run, and with test a
// test Run, completed on that exact commit.
func (w *world) checkedAt(wi, sha string, test bool) string {
	var reviewed, tested bool
	if err := w.app.InOrg(context.Background(), w.org, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `SELECT
			EXISTS (SELECT 1 FROM runs WHERE task_id = $1 AND phase = 'review' AND status = 'completed' AND base_refs->>'target' = $2),
			EXISTS (SELECT 1 FROM runs WHERE task_id = $1 AND phase = 'test' AND status = 'completed' AND base_refs->>'target' = $2)`,
			wi, sha).Scan(&reviewed, &tested)
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

			// What dude had not done to each head it sent the branch to, as
			// the move was asked.
			var mu sync.Mutex
			unchecked := map[string]string{}
			w.gh.Set(func(s *fakegithub.Server) {
				s.Intercept = func(r *http.Request) int {
					if r.Method != http.MethodPatch || !strings.HasSuffix(r.URL.Path, "/git/refs/heads/"+branch) {
						return 0
					}
					body, _ := io.ReadAll(r.Body)
					r.Body = io.NopCloser(bytes.NewReader(body))
					var in struct {
						SHA string `json:"sha"`
					}
					_ = json.Unmarshal(body, &in)
					missing := w.checkedAt(wi, in.SHA, test)
					mu.Lock()
					unchecked[in.SHA] = missing
					mu.Unlock()
					return 0
				}
			})
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
			mu.Lock()
			missing, asked := unchecked[head]
			mu.Unlock()
			if !asked {
				t.Fatalf("the branch moved to %s other than by a fast-forward", head)
			}
			if missing != "" {
				t.Fatalf("the pull request's branch moved to %s before its %s completed", head, missing)
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
