package orchestrator_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

func TestCandidatePromotionRequiresRepositoryReviewCoverage(t *testing.T) {
	for _, tc := range []struct {
		name       string
		whole      bool
		prior      bool
		accepted   bool
		stale      bool
		wantReview bool
	}{
		{name: "target-only checkout is not web coverage", wantReview: true},
		{name: "prior attempt whole-branch review is not coverage", whole: true, prior: true, wantReview: true},
		{name: "current attempt whole-branch review covers web", whole: true},
		{name: "human accepted exact web head", accepted: true},
		{name: "human accepted older web head", accepted: true, stale: true, wantReview: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newPRFixWorldIn(t, true)
			ctx := context.Background()
			var st delivery.State
			var raw []byte
			if err := w.owner.QueryRow(ctx, `SELECT state FROM workflow_runs WHERE task_id = $1`, w.wi).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(raw, &st); err != nil {
				t.Fatal(err)
			}
			if st.Attempt == 0 {
				st.Attempt = 1
			}
			candidate := delivery.CandidateBranch(w.wi, st.Attempt)
			head := w.web.Branch(candidate, w.branch, "Web change awaiting review")
			if head == "" || head == w.webBefore {
				t.Fatal("failed to create a new web candidate head")
			}

			// A completed target-only review checked out web incidentally at
			// the candidate SHA. No other completed review saw that SHA.
			var run string
			if err := w.owner.QueryRow(ctx, `SELECT id FROM runs WHERE task_id = $1 AND phase = 'review'
				AND status = 'completed' ORDER BY created_at DESC LIMIT 1`, w.wi).Scan(&run); err != nil {
				t.Fatal(err)
			}
			rangeJSON := any(`{"of":"pull_request_fix","base":{"target":"` + w.before + `"}}`)
			if tc.whole {
				rangeJSON = nil
			}
			reviewAttempt := st.Attempt
			if tc.prior {
				st.Attempt++
			}
			mustExec(t, w.owner, `UPDATE runs SET base_refs = jsonb_set(base_refs, '{web}', to_jsonb($2::text)),
				review_range = $3::jsonb, attempt = $4 WHERE id = $1`, run, head, rangeJSON, reviewAttempt)
			st.Candidate = &delivery.Candidate{Round: 1, Branch: candidate,
				From: map[string]string{"target": w.before, "web": w.webBefore},
				Base: map[string]string{"web": w.webBefore}}
			if tc.accepted {
				accepted := head
				if tc.stale {
					accepted = w.webBefore
				}
				st.Candidate.Accepted = map[string]string{"web": accepted}
			}
			st.Heads["web"] = head
			st.Iteration++
			st.Policy.Test = false
			raw, err := json.Marshal(st)
			if err != nil {
				t.Fatal(err)
			}
			mustExec(t, w.owner, `UPDATE workflow_runs SET step = 'promoteCandidate', state = $2::jsonb,
				status = 'running', awaiting_signals = '[]', wake_at = NULL, wait_until = NULL WHERE task_id = $1`, w.wi, raw)
			beforeReviews := w.phaseRuns(w.wi, "review")
			if n, err := w.runtime.Tick(ctx, 1); err != nil || n != 1 {
				t.Fatalf("promotion tick: %d, %v", n, err)
			}
			if tc.wantReview {
				if got := w.stateStep(); got != "review" {
					t.Fatalf("promotion went to %q, want review; web PR at %s, candidate %s", got, w.web.SHA(w.branch), head)
				}
				if w.web.SHA(w.branch) != w.webBefore || w.gh.SHA(w.branch) != w.before {
					t.Fatal("a pull request moved before web's required review")
				}
				armed := w.gated(func(step, next string) {
					if step == "promoteCandidate" {
						return
					}
					if w.web.SHA(w.branch) != w.webBefore {
						t.Errorf("web moved before promotion, during %s -> %s", step, next)
					}
				})
				armed.Store(true)
				w.until("completed web-naming review", func() bool {
					return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND attempt = $2
						AND phase = 'review' AND status = 'completed' AND base_refs->>'web' = $3
						AND review_range->'base'->>'web' = $4`, w.wi, st.Attempt, head, w.webBefore) == 1
				})
				armed.Store(false)
			}
			w.until("reviewed candidate on web's pull request", func() bool {
				return w.web.SHA(w.branch) == head && w.stateStep() == "awaitPullRequest"
			})
			want := beforeReviews
			if tc.wantReview {
				want++
			}
			if n := w.phaseRuns(w.wi, "review"); n != want {
				t.Errorf("%d reviews, want %d", n, want)
			}
		})
	}
}

func (w *prFixWorld) stateStep() string {
	w.t.Helper()
	var step string
	if err := w.owner.QueryRow(context.Background(), `SELECT step FROM workflow_runs WHERE task_id = $1`, w.wi).Scan(&step); err != nil {
		w.t.Fatal(err)
	}
	return step
}
