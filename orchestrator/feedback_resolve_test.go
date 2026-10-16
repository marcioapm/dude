package orchestrator_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/agenttools"
	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
)

// agentResolve is a fixer resolving a person's item through the agent
// tools server, as its publish moves the task branch: the commit is
// pick(the branch before, the Run's new head). compare, when set, sees
// each GitHub compare request (its path) and may fail it with a status.
type agentResolve struct {
	mu       sync.Mutex
	fixer    string
	commit   string
	answer   string
	compares []string
}

func (w *world) resolveAsItPublishes(task, item string, pick func(base, head string) string, compare func(path string) int) *agentResolve {
	w.t.Helper()
	tools := httptest.NewServer((&agenttools.Server{DB: w.app, Log: quiet}).Handler())
	w.t.Cleanup(tools.Close)
	ar := &agentResolve{}
	ctx := context.Background()
	w.gh.Set(func(s *fakegithub.Server) {
		s.Intercept = func(r *http.Request) int {
			if r.Method == "GET" && strings.Contains(r.URL.Path, "/compare/") {
				ar.mu.Lock()
				ar.compares = append(ar.compares, r.URL.Path)
				ar.mu.Unlock()
				if compare != nil {
					return compare(r.URL.Path)
				}
				return 0
			}
			if r.Method != "PATCH" || !strings.Contains(r.URL.Path, "/git/refs/heads/") {
				return 0
			}
			ar.mu.Lock()
			defer ar.mu.Unlock()
			if ar.fixer != "" {
				return 0
			}
			var fixer string
			_ = w.app.InOrg(ctx, w.org, func(tx pgx.Tx) error {
				return tx.QueryRow(ctx, `SELECT id FROM runs WHERE task_id = $1 AND phase = 'fix' AND status = 'running'
					AND $2 = ANY(feedback_item_ids)`, task, item).Scan(&fixer)
			})
			if fixer == "" {
				return 0
			}
			body, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(body))
			var in struct{ SHA string }
			_ = json.Unmarshal(body, &in)
			branch := r.URL.Path[strings.Index(r.URL.Path, "/git/refs/heads/")+len("/git/refs/heads/"):]
			token, hash := agenttools.NewToken()
			mustExec(w.t, w.owner, `UPDATE runs SET mcp_token_hash = $2 WHERE id = $1`, fixer, hash)
			ar.fixer, ar.commit = fixer, pick(w.gh.SHA(branch), in.SHA)
			req, _ := http.NewRequest("POST", tools.URL+"/tools/feedback_resolve",
				strings.NewReader(fmt.Sprintf(`{"id":%q,"commit":%q}`, item, ar.commit)))
			req.Header.Set("Authorization", "Bearer "+token)
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				ar.answer = err.Error()
				return 0
			}
			out, _ := io.ReadAll(res.Body)
			res.Body.Close()
			ar.answer = fmt.Sprintf("%d %s", res.StatusCode, out)
			return 0
		}
	})
	return ar
}

func (ar *agentResolve) state() (fixer, commit, answer string) {
	ar.mu.Lock()
	defer ar.mu.Unlock()
	return ar.fixer, ar.commit, ar.answer
}

// A fixer's resolution through the agent tools holds only for a commit
// of its own in what it published: its new head is verified; the commit
// it started from, or one GitHub does not have, reopens the item.
func TestFeedbackAnAgentsResolutionHoldsOnlyForItsOwnPublishedCommit(t *testing.T) {
	for _, c := range []struct {
		name     string
		pick     func(base, head string) string
		verified bool
	}{
		{"its own new commit", func(_, head string) string { return head }, true},
		{"the commit it started from", func(base, _ string) string { return base }, false},
		{"a commit GitHub does not have", func(_, _ string) string { return "deadbeef1234" }, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			ana := w.person("Ana")
			wi := w.reviewing()
			ar := w.resolveAsItPublishes(wi, "fbi_res", c.pick, nil)
			if code, body := w.feedback(ana, wi, "fbi_res", "Log the retry count at info, not debug."); code != 201 {
				t.Fatalf("submit: %d %v", code, body)
			}
			w.until("the fixer resolved the item and completed", func() bool {
				fixer, _, _ := ar.state()
				return fixer != "" && w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'completed'`, fixer) == 1
			})
			fixer, commit, answer := ar.state()
			if !strings.HasPrefix(answer, "200 ") {
				t.Fatalf("feedback_resolve with %s: %s", commit, answer)
			}
			var status string
			var verified bool
			_ = w.owner.QueryRow(context.Background(), `SELECT status, resolved_verified_at IS NOT NULL FROM feedback_items WHERE id = 'fbi_res'`).
				Scan(&status, &verified)
			reopened := w.count(`SELECT count(*) FROM feedback_replies WHERE item_id = 'fbi_res' AND kind = 'reopen' AND run_id = $1`, fixer)
			if c.verified {
				if status != "resolved" || !verified || reopened != 0 {
					t.Errorf("its own commit %s: %s verified=%v, %d reopenings", commit, status, verified, reopened)
				}
				return
			}
			if status != "open" || reopened != 1 {
				t.Errorf("commit %s: %s, %d reopenings", commit, status, reopened)
			}
		})
	}
}

// GitHub failing the compare that checks a resolution does not wedge the
// finishing Run: it backs off (next_attempt_at), asks nothing more until
// then, and once GitHub answers the resolution is verified.
func TestFeedbackACompareFailureWhileSettlingAResolutionBacksOff(t *testing.T) {
	w := newWorld(t)
	ana := w.person("Ana")
	wi := w.reviewing()
	var ar *agentResolve
	failed := 0
	abbrev := func(_, head string) string { return head[:12] }
	ar = w.resolveAsItPublishes(wi, "fbi_res", abbrev, func(path string) int {
		_, commit, _ := ar.state()
		if commit != "" && strings.HasSuffix(path, "..."+commit) && failed == 0 {
			failed++
			return http.StatusServiceUnavailable
		}
		return 0
	})
	if code, body := w.feedback(ana, wi, "fbi_res", "Log the retry count at info, not debug."); code != 201 {
		t.Fatalf("submit: %d %v", code, body)
	}
	comparesOfCommit := func() int {
		_, commit, _ := ar.state()
		ar.mu.Lock()
		defer ar.mu.Unlock()
		n := 0
		for _, p := range ar.compares {
			if commit != "" && strings.HasSuffix(p, "..."+commit) {
				n++
			}
		}
		return n
	}
	w.until("GitHub failed the resolution's compare", func() bool { return comparesOfCommit() == 1 })
	fixer, _, answer := ar.state()
	if !strings.HasPrefix(answer, "200 ") {
		t.Fatalf("feedback_resolve: %s", answer)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'running' AND next_attempt_at > now()`, fixer); n != 1 {
		t.Fatal("the finishing Run did not back off after the compare failed")
	}
	for range 3 {
		w.pump()
	}
	if n := comparesOfCommit(); n != 1 {
		t.Errorf("the compare was asked %d times within the back-off", n)
	}
	mustExec(t, w.owner, `UPDATE runs SET next_attempt_at = now() WHERE id = $1`, fixer)
	w.until("the resolution verified", func() bool {
		return w.count(`SELECT count(*) FROM feedback_items WHERE id = 'fbi_res' AND status = 'resolved'
			AND resolved_verified_at IS NOT NULL`) == 1
	})
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'completed'`, fixer); n != 1 {
		t.Error("the fixer did not complete")
	}
}
