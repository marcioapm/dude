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
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
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
// parent), and the branch is at one of them: a git push, which
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
		// The head first, then the log: a move landing between the two
		// reads is in the log, not taken for a git push.
		cur := gh.SHA(branch)
		moves := gh.BranchLog(branch)[before:]
		last := start
		for _, head := range moves {
			last = head
			at := head
			missing, ok := asked[head]
			update := -1
			if parents := gh.Parents(head); !ok && len(parents) == 2 {
				at = parents[1]
				if missing, ok = asked[at]; !ok {
					// An update-branch that moved the branch made exactly this
					// merge; one GitHub refused made none, and excuses nothing.
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
		if cur != last && !slices.Contains(moves, cur) {
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
