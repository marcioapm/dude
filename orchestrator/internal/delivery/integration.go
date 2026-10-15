package delivery

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
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

// ResolveConflictsTx is a person's "Resolve conflicts" on a pull request:
// the workflow waiting on that pull request is signalled to integrate its
// base, whatever whenBehind says; one stopped at that pull request's
// conflict escalation is decided so (retry for a failed integration, else
// wait with the request queued). Anything else is a conflict: the handler
// starts no phase itself. authorize checks who may decide an escalation.
func ResolveConflictsTx(ctx context.Context, tx pgx.Tx, org, taskID, prID, repo string, number int, head string,
	in EscalationDecision) error {
	_, d, err := LockEscalationTx(ctx, tx, taskID)
	if err != nil {
		return err
	}
	if d == nil || !d.Live() || !slices.Contains(d.State.PullRequestIDs, prID) {
		return escalationErr("conflict", "dude is not waiting on this pull request: it is busy with the task or has stopped")
	}
	signal := forge.Signal{Kind: "conflict", Repo: repo, Number: number, Requested: true}
	key := fmt.Sprintf("resolve-conflicts:%s:%d:%s", repo, number, head)
	if e := d.State.Escalation; e != nil {
		if e.Reason != "pull_request_conflict" || e.Decided != nil || !e.about(repo, number) {
			return escalationErr("conflict", "dude is waiting on a decision about something else")
		}
		in.Action = "wait"
		if slices.Contains(e.Actions(), "retry") {
			in.Action = "retry"
		}
		if err := DecideEscalationTx(ctx, tx, org, taskID, in); err != nil || in.Action == "retry" {
			return err
		}
		return workflow.SignalTx(ctx, tx, org, d.WorkflowID, SignalPRFeedback, signal, key)
	}
	if d.Step != "awaitPullRequest" || d.State.Integration != nil {
		return escalationErr("conflict", "dude is already working on the task; try again once it waits on the pull request")
	}
	return workflow.SignalTx(ctx, tx, org, d.WorkflowID, SignalPRFeedback, signal, key)
}

// about says the escalation names this pull request.
func (e *Escalation) about(repo string, number int) bool {
	var d struct {
		Repo   string `json:"repo"`
		Number int    `json:"number"`
	}
	e.decode(&d)
	return d.Repo == repo && d.Number == number
}

func (w *steps) integrationFailure(ctx context.Context, sc workflow.StepContext, st *State, problem string) (workflow.Result, error) {
	i := st.Integration
	detail := map[string]any{"repo": i.Repo, "number": i.Number, "base": i.Base, "error": problem, "integration": true}
	if i.agentRun() {
		detail["runId"] = i.RunID
	}
	if err := w.integrationEvent(ctx, sc, st, "integration-refused", map[string]any{"error": problem}); err != nil {
		return workflow.Result{}, err
	}
	return w.escalate(ctx, sc, st, "pull_request_conflict", detail)
}

// agentRun says the integration was an agent's Run; a merge GitHub made
// carries a "server-<sha>" key that names no Run.
func (i *Integration) agentRun() bool { return i.RunID != "" && !strings.HasPrefix(i.RunID, "server-") }

// integrationEvent records an integration's start, completion or refusal on
// the task's activity, once per action and attempt however often the step
// is replayed.
func (w *steps) integrationEvent(ctx context.Context, sc workflow.StepContext, st *State, action string, extra map[string]any) error {
	i := st.Integration
	payload := map[string]any{"action": action, "repo": i.Repo, "base": i.Base, "attempt": i.pair() + ":" + i.RunID}
	if i.Number > 0 {
		payload["number"] = i.Number
	}
	if i.agentRun() {
		payload["runId"] = i.RunID
	}
	maps.Copy(payload, extra)
	return w.s.DB.InOrg(ctx, sc.OrganizationID, func(tx pgx.Tx) error {
		// The workflow's step lease admits one runner of this step at a
		// time, which is what makes this read-then-append write once.
		var seen bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM events WHERE task_id = $1 AND event_type = $2
				AND payload->>'action' = $3 AND payload->>'attempt' = $4)`,
			st.TaskID, EvPullRequestAction, action, payload["attempt"]).Scan(&seen); err != nil || seen {
			return err
		}
		_, err := ledger.Append(ctx, tx, ledger.Event{Type: EvPullRequestAction, OrganizationID: sc.OrganizationID,
			ProjectID: st.ProjectID, TaskID: st.TaskID, ActorType: ledger.ActorSystem, ActorID: "workflow",
			Source: ledger.SourceOrchestrator, CorrelationID: st.TaskID, Payload: payload})
		return err
	})
}

func (w *steps) prepareIntegration(ctx context.Context, sc workflow.StepContext, st *State, stop *forge.Signal) (bool, error) {
	gh, err := w.forges.For(ctx, sc.OrganizationID)
	if err != nil || gh == nil {
		return false, err
	}
	if gh.Settings.WhenBehind != "update" && !stop.Requested {
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
