// An open pull request's fix, checked before the pull request shows it:
// the fix and every fix of it go to the candidate branch (CandidateBranch),
// are reviewed over their own range, simplified there with the
// simplifier's diff reviewed alone, and tested on every criterion, and only
// then is the pull request's branch fast-forwarded to it
// (steps.promoteCandidate).

package delivery

import (
	"context"
	"encoding/json"
	"maps"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// EvPullRequestFixMoved: a fix checked on the candidate branch reached a
// pull request: its branch was fast-forwarded to the candidate's head.
// Payload: {repo, number, from, to, candidate}.
const EvPullRequestFixMoved = "pull_request.fix_moved"

// rangeFindings are the open findings of this attempt raised by a review
// of the range the delivery reviews now: what a fix of that loop is sent.
// A range grows when a fix changes a repository it lacked (awaitFix), so
// an earlier round's review matches by its anchors being a subset of r's.
func (s *Store) rangeFindings(ctx context.Context, org string, st *State, r *ReviewRange) ([]string, error) {
	raw, _ := json.Marshal(r)
	var out []string
	err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id FROM review_findings
			WHERE task_id = $1 AND status = 'open' AND `+thisAttempt+`
			  AND run_id IN (SELECT id FROM runs WHERE task_id = $1 AND review_range->>'of' = ($3::jsonb->>'of')
			    AND ($3::jsonb->'base') @> (review_range->'base'))
			ORDER BY created_at`, st.TaskID, st.Attempt, raw)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	})
	return out, err
}

// unreviewedHeads are the repositories whose candidate heads have neither
// a completed review covering that repository in this attempt nor a person's
// acceptance of that exact commit.
func (s *Store) unreviewedHeads(ctx context.Context, org string, st *State, c *Candidate) ([]string, error) {
	var out []string
	err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		for _, repo := range slices.Sorted(maps.Keys(st.Heads)) {
			if st.Heads[repo] == c.From[repo] || st.Heads[repo] == c.Accepted[repo] {
				continue
			}
			var reviewed bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM runs WHERE task_id = $1
				AND attempt = COALESCE(NULLIF($4, 0), (SELECT max(attempt) FROM runs WHERE task_id = $1)) AND phase = 'review'
				AND status = 'completed' AND base_refs->>$2 = $3
				AND (review_range IS NULL OR review_range->'base' ? $2))`, st.TaskID, repo, st.Heads[repo], st.Attempt).Scan(&reviewed); err != nil {
				return err
			}
			if !reviewed {
				out = append(out, repo)
			}
		}
		return nil
	})
	return out, err
}

// runBaseRefs is the commit per repository a Run was created to start from.
func (s *Store) runBaseRefs(ctx context.Context, org, runID string) (map[string]string, error) {
	var heads map[string]string
	err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT base_refs FROM runs WHERE id = $1`, runID).Scan(&heads)
	})
	return heads, err
}

// writableSlugs is each repository the task may change, by name, as the
// forge names it.
func (s *Store) writableSlugs(ctx context.Context, org, taskID string) (map[string]string, error) {
	out := map[string]string{}
	err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		repos, err := TaskRepositories(ctx, tx, taskID)
		for _, r := range repos {
			if r.Access == "write" {
				out[r.Name] = forge.SlugFromURL(r.URL)
			}
		}
		return err
	})
	return out, err
}

// dropCandidateBranches deletes the delivery's candidate branch in each
// repository it may change, before a new round's fix: one a person's push
// left off the pull request's line would refuse the fix's fast-forward.
// One already gone is no error.
func (s *Store) dropCandidateBranches(ctx context.Context, org string, st *State, gh *forge.GitHub) error {
	slugs, err := s.writableSlugs(ctx, org, st.TaskID)
	if err != nil {
		return err
	}
	for _, repo := range slices.Sorted(maps.Keys(slugs)) {
		if slugs[repo] == "" {
			continue
		}
		if err := gh.DeleteBranch(ctx, slugs[repo], st.candidateBranch()); err != nil {
			return err
		}
	}
	return nil
}

// EvTaskBranchCreating: promotion is about to create the task branch in a
// repository it was never published to, recorded before the create is
// sent, whether or not GitHub then creates it. Payload: {repo, branch}.
const EvTaskBranchCreating = "task_branch.creating"

// neverPublished says the task branch was never on the forge in repo:
// no pull request opened from it, no Run or conductor publish moved it,
// no adopted source names it there, and promotion never created it. Such
// a branch missing was not deleted by a person, so promotion may create it.
func (s *Store) neverPublished(ctx context.Context, org string, st *State, repo string) (bool, error) {
	var published bool
	err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pull_requests pr JOIN repositories r ON r.id = pr.repository_id
			  WHERE pr.task_id = $1 AND r.name = $2 AND pr.head_branch = $3)
			OR EXISTS (SELECT 1 FROM runs WHERE task_id = $1 AND branch = $3 AND heads ? $2)
			OR EXISTS (SELECT 1 FROM conductor_publishes WHERE task_id = $1 AND branch = $3
			  AND status IN ('moving', 'published', 'stalled') AND moves ? $2)
			OR EXISTS (SELECT 1 FROM task_sources ts JOIN repositories r ON r.id = ts.repository_id
			  WHERE ts.task_id = $1 AND r.name = $2 AND ts.head_branch = $3)
			OR EXISTS (SELECT 1 FROM events WHERE task_id = $1 AND event_type = $4
			  AND payload->>'repo' = $2 AND payload->>'branch' = $3)`,
			st.TaskID, repo, st.Branch, EvTaskBranchCreating).Scan(&published)
	})
	return !published, err
}

// branchCreatingTx records, committed before the create is sent, that
// promotion creates the task branch in repo: a retry or replay that then
// finds it missing reads a person's deletion, never a branch to create.
func (s *Store) branchCreatingTx(ctx context.Context, org string, st *State, repo string) error {
	return s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		_, err := ledger.Append(ctx, tx, ledger.Event{Type: EvTaskBranchCreating, OrganizationID: org,
			ProjectID: st.ProjectID, TaskID: st.TaskID, RunID: st.HeadRunID, ActorType: ledger.ActorSystem, ActorID: "workflow",
			Source: ledger.SourceOrchestrator, CorrelationID: st.TaskID,
			Payload: map[string]any{"repo": repo, "branch": st.Branch}})
		return err
	})
}

// pullRequestNumber is the number of the delivery's pull request in repo,
// 0 for none.
func (s *Store) pullRequestNumber(ctx context.Context, org string, st *State, repo string) (int, error) {
	states, err := s.PullRequestStates(ctx, org, st.PullRequestIDs)
	for _, p := range states {
		if p.Repo == repo {
			return p.Number, err
		}
	}
	return 0, err
}

// CandidateMovedTx records the pull requests' new heads once their branch
// was moved to the candidate's (moved, by repository): their checks are
// yet to run on it, as after any push; and the move, once per pull
// request and head.
func (s *Store) CandidateMovedTx(ctx context.Context, org string, st *State, moved map[string]string) error {
	if len(moved) == 0 {
		return nil
	}
	return s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		for _, repo := range slices.Sorted(maps.Keys(moved)) {
			var id string
			var number int
			var from *string
			err := tx.QueryRow(ctx, `SELECT pr.id, pr.number, pr.head_sha FROM pull_requests pr JOIN repositories r ON r.id = pr.repository_id
				WHERE pr.id = ANY($1) AND r.name = $2 AND pr.state IN ('open', 'draft') ORDER BY pr.created_at DESC LIMIT 1`,
				st.PullRequestIDs, repo).Scan(&id, &number, &from)
			if err != nil && err != pgx.ErrNoRows {
				return err
			}
			if id == "" || from != nil && *from == moved[repo] {
				continue
			}
			if _, err := tx.Exec(ctx, `UPDATE pull_requests SET head_sha = $2, head_seen_at = now(),
				checks = CASE WHEN had_ci THEN 'pending' ELSE 'unknown' END::check_state, updated_at = now()
				WHERE id = $1`, id, moved[repo]); err != nil {
				return err
			}
			if _, err := ledger.Append(ctx, tx, ledger.Event{Type: EvPullRequestFixMoved, OrganizationID: org,
				ProjectID: st.ProjectID, TaskID: st.TaskID, RunID: st.HeadRunID, ActorType: ledger.ActorSystem, ActorID: "workflow",
				Source: ledger.SourceOrchestrator, CorrelationID: st.TaskID,
				Payload: map[string]any{"repo": repo, "number": number, "from": deref(from), "to": moved[repo],
					"candidate": st.Candidate.Branch}}); err != nil {
				return err
			}
		}
		return nil
	})
}
