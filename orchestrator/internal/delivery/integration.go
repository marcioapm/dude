package delivery

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

// syncBeforePR runs while the workflow holds its step lease. A moving
// conductor publish is refused before any network move; its reservation
// cannot start while stepHolds is true.
func (w *steps) syncBeforePR(ctx context.Context, sc workflow.StepContext, st *State) (workflow.Result, bool, error) {
	gh, err := w.forges.For(ctx, sc.OrganizationID)
	if err != nil || gh == nil {
		return workflow.Result{}, false, err
	}
	if gh.Settings.WhenBehind != "update" {
		return workflow.Result{}, false, nil
	}
	var repos []Repository
	err = w.s.DB.InOrg(ctx, sc.OrganizationID, func(tx pgx.Tx) error {
		if err := RefuseWhileMovingTx(ctx, tx, st.TaskID); err != nil {
			return err
		}
		var writer bool
		if err := tx.QueryRow(ctx, `SELECT `+liveWriter, st.TaskID).Scan(&writer); err != nil {
			return err
		}
		if writer {
			return workflow.Wait{After: time.Second, Why: "a phase is still writing the task branch"}
		}
		var e error
		repos, e = TaskRepositories(ctx, tx, st.TaskID)
		return e
	})
	if err != nil {
		return workflow.Result{}, false, err
	}
	for _, repo := range repos {
		if repo.Access != "write" || st.Heads[repo.Name] == "" {
			continue
		}
		var open bool
		err = w.s.DB.InOrg(ctx, sc.OrganizationID, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pull_requests WHERE task_id=$1 AND repository_id=$2 AND head_branch=$3 AND state IN ('open','draft'))`, st.TaskID, repo.ID, st.Branch).Scan(&open)
		})
		if err != nil {
			return workflow.Result{}, false, err
		}
		if open {
			continue
		}
		slug := forge.SlugFromURL(repo.URL)
		base := st.BaseFor(repo)
		head, e := gh.BranchSHA(ctx, slug, st.Branch)
		if e != nil {
			return workflow.Result{}, false, e
		}
		bh, e := gh.BranchSHA(ctx, slug, base)
		if e != nil {
			return workflow.Result{}, false, e
		}
		cmp, e := gh.Compare(ctx, slug, bh, head)
		if e != nil {
			return workflow.Result{}, false, e
		}
		if head != st.Heads[repo.Name] {
			st.Heads[repo.Name] = head
			st.Integration = &Integration{Repo: repo.Name, Base: base, Head: head, BaseHead: bh, BeforePR: true, Published: true, RunID: "server-" + head}
			return workflow.Result{Next: "test", State: st}, true, nil
		}
		if cmp.BehindBy == 0 {
			continue
		}
		i := &Integration{Repo: repo.Name, Base: base, Head: head, BaseHead: bh, BeforePR: true}
		if run := st.IntegrationPairs[i.pair()]; run != "" {
			i.RunID = run
			st.Integration = i
			r, e := w.integrationFailure(ctx, sc, st, "this head/base pair was already attempted")
			return r, true, e
		}
		merged, e := gh.MergeBase(ctx, slug, st.Branch, bh)
		if e != nil {
			if fe, ok := e.(*forge.Error); ok && fe.Status == 409 {
				st.Integration = i
				st.Heads[repo.Name] = head
				return workflow.Result{Next: "prFix", State: st}, true, nil
			}
			return workflow.Result{}, false, e
		}
		if merged == "" {
			continue
		}
		st.Heads[repo.Name] = merged
		// A changed head is tested again even when Git merged it without conflicts.
		st.Integration = i
		st.Integration.Published = true
		st.Integration.RunID = "server-" + merged
		err = w.s.DB.InOrg(ctx, sc.OrganizationID, func(tx pgx.Tx) error {
			_, e := ledger.Append(ctx, tx, ledger.Event{Type: EvPullRequestAction, OrganizationID: sc.OrganizationID, ProjectID: st.ProjectID, TaskID: st.TaskID, ActorType: ledger.ActorSystem, ActorID: "workflow", Source: ledger.SourceOrchestrator, CorrelationID: st.TaskID, Payload: map[string]any{"action": "merge-base", "repo": repo.Name, "base": base, "head": merged}})
			return e
		})
		if err != nil {
			return workflow.Result{}, false, err
		}
		return workflow.Result{Next: "test", State: st}, true, nil
	}
	return workflow.Result{}, false, nil
}

// Integration keeps the attempted pair and continuation through review and test.
type Integration struct {
	Repo      string `json:"repo"`
	Number    int    `json:"number"`
	Base      string `json:"base"`
	Head      string `json:"head"`
	BaseHead  string `json:"baseHead"`
	RunID     string `json:"runId,omitempty"`
	Published bool   `json:"published,omitempty"`
	MergeSHA  string `json:"mergeSha,omitempty"`
	BeforePR  bool   `json:"beforePr,omitempty"`
}

func (i *Integration) pair() string { return i.Repo + ":" + i.Head + ":" + i.BaseHead }
func (i *Integration) brief() string {
	return fmt.Sprintf("Base integration: this branch conflicts with `%s` in %s. Call sync_base, then `git merge lux/%s`. Resolve every conflict keeping both sides' intent, run the affected tests, and commit with git's default merge message. Never rebase or force. The recorded base head is %s.", i.Base, i.Repo, i.Base, i.BaseHead)
}

func (w *steps) integrationFailure(ctx context.Context, sc workflow.StepContext, st *State, problem string) (workflow.Result, error) {
	i := st.Integration
	return w.escalate(ctx, sc, st, "pull_request_conflict", map[string]any{"repo": i.Repo, "number": i.Number, "base": i.Base, "runId": i.RunID, "error": problem, "integration": true})
}

func (w *steps) prepareIntegration(ctx context.Context, sc workflow.StepContext, st *State, stop *forge.Signal) (bool, error) {
	gh, err := w.forges.For(ctx, sc.OrganizationID)
	if err != nil || gh == nil {
		return false, err
	}
	if gh.Settings.WhenBehind != "update" {
		return false, nil
	}
	states, err := w.s.PullRequestStates(ctx, sc.OrganizationID, st.PullRequestIDs)
	if err != nil {
		return false, err
	}
	for _, p := range states {
		if p.Repo != stop.Repo || p.Number != stop.Number || p.State == forge.StateMerged || p.State == forge.StateClosed {
			continue
		}
		var repo Repository
		err = w.s.DB.InOrg(ctx, sc.OrganizationID, func(tx pgx.Tx) error {
			repos, e := TaskRepositories(ctx, tx, st.TaskID)
			if e != nil {
				return e
			}
			for _, r := range repos {
				if r.Name == p.Repo {
					repo = r
					break
				}
			}
			return nil
		})
		if err != nil {
			return false, err
		}
		fresh, e := gh.PullRequest(ctx, p.Slug, p.Number)
		if e != nil {
			return false, e
		}
		if fresh.State == forge.StateClosed || fresh.State == forge.StateMerged || fresh.Mergeable != forge.MergeConflicting {
			return false, nil
		}
		head, e := gh.Head(ctx, p.Slug, p.Number)
		if e != nil {
			return false, e
		}
		base := st.BaseFor(repo)
		bh, e := gh.BranchSHA(ctx, p.Slug, base)
		if e != nil {
			return false, e
		}
		if head == "" || bh == "" {
			return false, fmt.Errorf("integration requires current task and base heads")
		}
		i := &Integration{Repo: p.Repo, Number: p.Number, Base: base, Head: head, BaseHead: bh}
		if run := st.IntegrationPairs[i.pair()]; run != "" {
			i.RunID = run
			st.Integration = i
			return false, nil
		}
		st.Integration = i
		if st.Heads == nil {
			st.Heads = map[string]string{}
		}
		st.Heads[p.Repo] = head
		return true, nil
	}
	return false, nil
}
