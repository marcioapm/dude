package delivery

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

// Integration keeps the attempted pair and continuation through review and test.
type Integration struct {
	Repo      string `json:"repo"`
	Number    int    `json:"number"`
	Base      string `json:"base"`
	Head      string `json:"head"`
	BaseHead  string `json:"baseHead"`
	RunID     string `json:"runId,omitempty"`
	Published bool   `json:"published,omitempty"`
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
