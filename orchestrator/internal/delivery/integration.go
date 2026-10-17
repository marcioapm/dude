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
		// Only a repository about to get a pull request (OpenPullRequests'
		// rule): one open already, or one merged or closed at this head,
		// covers it, and merging the base in would only open an empty one.
		var covered bool
		err = w.s.DB.InOrg(ctx, sc.OrganizationID, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pull_requests WHERE task_id=$1 AND repository_id=$2
				AND head_branch=$3 AND (state IN ('open','draft') OR head_sha=$4))`,
				st.TaskID, repo.ID, st.Branch, st.Heads[repo.Name]).Scan(&covered)
		})
		if err != nil {
			return workflow.Result{}, false, err
		}
		if covered {
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
		// A head GitHub merged and adopted is tested again, clean or not.
		serverMerged := func(from, sha string) *Integration {
			return &Integration{Repo: repo.Name, Base: base, Head: from, BaseHead: bh, BeforePR: true, Published: true, RunID: "server-" + sha}
		}
		if head != st.Heads[repo.Name] {
			st.Heads[repo.Name] = head
			st.Integration = serverMerged(head, head)
			return workflow.Result{Next: "test", State: st}, true, nil
		}
		if cmp.BehindBy == 0 {
			continue
		}
		i := &Integration{Repo: repo.Name, Base: base, Head: head, BaseHead: bh, BeforePR: true}
		if run := st.IntegrationPairs[i.pair()]; run != "" {
			i.RunID = run
			st.Integration = i
			r, e := w.integrationFailure(ctx, sc, st, causePairAttempted, reasonPairAttempted)
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
		st.Integration = serverMerged(head, merged)
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
	Repo string `json:"repo"`
	// The pull request; 0 before one is open (BeforePR).
	Number   int    `json:"number"`
	Base     string `json:"base"`
	Head     string `json:"head"`
	BaseHead string `json:"baseHead"`
	// The integration Run, or "server-<sha>" for a merge GitHub made
	// (agentRun tells them apart).
	RunID string `json:"runId,omitempty"`
	// The merge is published on the candidate for an open pull request;
	// what follows validates it.
	Published bool   `json:"published,omitempty"`
	MergeSHA  string `json:"mergeSha,omitempty"`
	// Before the pull request opened (syncBeforePR): a person's wait goes
	// on to open it, not back to waiting on it.
	BeforePR bool `json:"beforePr,omitempty"`
	// Reads of the pull request after the integration published that
	// found GitHub had not worked its mergeability out yet.
	Polls int `json:"polls,omitempty"`
}

// unknownMergeabilityPolls bounds those reads, 1 s apart and doubling up
// to a minute (about four minutes in all); past it the pull request
// syncer's reads take over.
const unknownMergeabilityPolls = 8

// Why an integration escalated: the escalation's and its activity line's
// cause, which the web words; the error is for logs and the Run's own.
const (
	causeStillConflicting = "still_conflicting"
	causePairAttempted    = "pair_attempted"
	causeRunFailed        = "run_failed"
	causeNoMerge          = "no_merge"

	reasonStillConflicting = "still conflicting after integration"
	reasonPairAttempted    = "this head/base pair was already attempted"
	reasonNoMerge          = "the integration Run finished without publishing a merge"
)

func (i *Integration) pair() string { return i.Repo + ":" + i.Head + ":" + i.BaseHead }

// attemptedIn says an integration of repo was attempted at some pair.
func (st *State) attemptedIn(repo string) bool {
	for p := range st.IntegrationPairs {
		if strings.HasPrefix(p, repo+":") {
			return true
		}
	}
	return false
}

// attemptCause is why an attempted pair escalates: one handed over to the
// pull request syncer with GitHub undecided is dude's merge, still
// conflicting; any other was attempted and not resolved.
func (st *State) attemptCause(i *Integration) (string, string) {
	if _, ok := st.HandedOver[i.pair()]; ok {
		return causeStillConflicting, reasonStillConflicting
	}
	return causePairAttempted, reasonPairAttempted
}

// brief is the integration fixer's feedback item. Its "Base integration:"
// lead is what marks the fix Run as an integration (phases.buildSpec).
func (i *Integration) brief() string {
	return render(integrationFixTmpl, struct{ Base, Repo, BaseHead string }{i.Base, i.Repo, i.BaseHead})
}

// IntegrationMergeWhen is when an integration fixer may merge lux/<base>,
// and when it gives up, in the words the brief and sync_base's answer share.
func IntegrationMergeWhen(base, recorded string) string {
	return render(integrationMergeWhenTmpl, struct{ Base, BaseHead string }{base, recorded})
}

// ResolveConflictsTx is a person's "Resolve conflicts" on a pull request:
// the workflow waiting on that pull request is signalled to integrate its
// base, whatever whenBehind says; one stopped at that pull request's
// conflict escalation is decided so (retry for a failed integration, else
// wait with the request queued). Anything else is a conflict: the handler
// starts no phase itself. in.Authorize checks who may decide an escalation.
func ResolveConflictsTx(ctx context.Context, tx pgx.Tx, org, taskID, prID, repo string, number int, head string,
	in EscalationDecision) error {
	_, d, err := LockEscalationTx(ctx, tx, taskID)
	if err != nil {
		return err
	}
	if d == nil || !d.Live() || !slices.Contains(d.State.PullRequestIDs, prID) {
		return escalationErr("conflict", "%s", mDudeNotWaitingOnThisPull)
	}
	signal := forge.Signal{Kind: "conflict", Repo: repo, Number: number, Requested: true}
	key := fmt.Sprintf("resolve-conflicts:%s:%d:%s", repo, number, head)
	if e := d.State.Escalation; e != nil {
		if e.Reason != "pull_request_conflict" || e.Decided != nil || !e.about(repo, number) {
			return escalationErr("conflict", "%s", mDudeWaitingOnDecisionAboutSomething)
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
		return escalationErr("conflict", "%s", mDudeAlreadyWorkingOnTaskTry)
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

// integrationFailure escalates the integration as pull_request_conflict
// (retry or wait). cause is the web's to word: causeStillConflicting,
// causePairAttempted, causeNoMerge, or causeRunFailed with the Run's error.
func (w *steps) integrationFailure(ctx context.Context, sc workflow.StepContext, st *State, cause, problem string) (workflow.Result, error) {
	i := st.Integration
	detail := map[string]any{"repo": i.Repo, "number": i.Number, "base": i.Base, "cause": cause, "error": problem, "integration": true}
	if i.agentRun() {
		detail["runId"] = i.RunID
	}
	if err := w.integrationEvent(ctx, sc, st, "integration-refused", map[string]any{"cause": cause, "error": problem}); err != nil {
		return workflow.Result{}, err
	}
	if c := st.Candidate; c != nil {
		// Never promoted: what its Runs pushed stays on the candidate, and
		// the delivery stands on what the pull requests show.
		st.Heads, st.Candidate = maps.Clone(c.From), nil
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
	return w.s.DB.InOrg(ctx, sc.OrganizationID, func(tx pgx.Tx) error {
		// The workflow's step lease admits one runner of this step at a
		// time, which is what makes this read-then-append write once.
		return integrationEventTx(ctx, tx, sc.OrganizationID, st, st.Integration, action, extra)
	})
}

// HandOverPending says, without locking the delivery, whether it holds an
// integration handed over at repo's head that is not closed: what
// HandOverMergedTx would write. false because the line (or a refusal) is on
// record is final; false for want of a hand-over is not, so every sync asks
// again. true is rechecked under the lock.
func HandOverPending(ctx context.Context, tx pgx.Tx, taskID, repo, head string) (bool, error) {
	d, err := ReadDelivery(ctx, tx, taskID)
	if err != nil || d == nil {
		return false, err
	}
	pair, i, ok := handedOverAt(d.State.HandedOver, repo, head)
	if !ok {
		return false, nil
	}
	closed, err := integrationClosed(ctx, tx, taskID, pair, &i)
	return !closed, err
}

// handedOverAt is the integration handed over whose merge is repo's head,
// and its pair.
func handedOverAt(handed map[string]Integration, repo, head string) (string, Integration, bool) {
	for pair, i := range handed {
		if pair == repo+":"+head+":"+i.BaseHead {
			return pair, i, true
		}
	}
	return "", Integration{}, false
}

// integrationClosed says whether i, handed over under pair, has its
// completed line or was refused: an attempt escalated as still conflicting
// is the person's to decide, and is never completed. The refusal's attempt
// names the integrated head (pair's), not the head i started from.
func integrationClosed(ctx context.Context, tx pgx.Tx, taskID, pair string, i *Integration) (bool, error) {
	if seen, err := integrationEventSeen(ctx, tx, taskID, "integration-completed", i.attempt()); err != nil || seen {
		return seen, err
	}
	return integrationEventSeen(ctx, tx, taskID, "integration-refused", pair+":"+i.RunID)
}

// HandOverMergedTx is a reading that repo's pull request merges, at head,
// after an integration left to the pull request syncer (State.HandedOver):
// the integration's closing line, once. Any such reading closes it, the
// first or a later one: a step that records the hand-over after one sync
// read the delivery may also read the pull request before that sync's
// reading commits, and the next reading of it as merging is then the one
// that closes it.
func HandOverMergedTx(ctx context.Context, tx pgx.Tx, org, taskID, repo, head string) error {
	// Locked: serialised with closeHandedOver, so the line's dedupe holds.
	d, err := LoadDelivery(ctx, tx, taskID)
	if err != nil || d == nil {
		return err
	}
	return handOverMergedTx(ctx, tx, org, &d.State, repo, head)
}

func handOverMergedTx(ctx context.Context, tx pgx.Tx, org string, st *State, repo, head string) error {
	pair, i, ok := handedOverAt(st.HandedOver, repo, head)
	if !ok {
		return nil
	}
	if closed, err := integrationClosed(ctx, tx, st.TaskID, pair, &i); err != nil || closed {
		return err
	}
	return integrationEventTx(ctx, tx, org, st, &i, "integration-completed", map[string]any{"head": head})
}

// closeHandedOver writes the closing line of each handed-over integration
// whose pull request is on record as merging at the handed-over head: the
// syncer may have read it in the window before the hand-over was recorded,
// and found nothing to close then.
func (w *steps) closeHandedOver(ctx context.Context, sc workflow.StepContext, st *State) error {
	if len(st.HandedOver) == 0 {
		return nil
	}
	states, err := w.s.PullRequestStates(ctx, sc.OrganizationID, st.PullRequestIDs)
	if err != nil {
		return err
	}
	return w.s.DB.InOrg(ctx, sc.OrganizationID, func(tx pgx.Tx) error {
		// Serialises this with the syncer's hand-over (HandOverMergedTx).
		if _, err := LoadDelivery(ctx, tx, st.TaskID); err != nil {
			return err
		}
		for _, p := range states {
			if p.State != forge.StateOpen && p.State != forge.StateDraft ||
				p.Mergeable != forge.MergeClean && p.Mergeable != forge.MergeBehind {
				continue
			}
			if err := handOverMergedTx(ctx, tx, sc.OrganizationID, st, p.Repo, p.HeadSHA); err != nil {
				return err
			}
		}
		if w.s.HandOverClosing != nil {
			w.s.HandOverClosing(ctx, tx)
		}
		return nil
	})
}

// integrationEventTx is integrationEvent for integration i. The type and
// correlation narrow its dedupe to the task's pull request actions
// (events_correlation_idx).
func integrationEventTx(ctx context.Context, tx pgx.Tx, org string, st *State, i *Integration, action string, extra map[string]any) error {
	payload := map[string]any{"action": action, "repo": i.Repo, "base": i.Base, "attempt": i.attempt()}
	if i.Number > 0 {
		payload["number"] = i.Number
	}
	if i.agentRun() {
		payload["runId"] = i.RunID
	}
	maps.Copy(payload, extra)
	if seen, err := integrationEventSeen(ctx, tx, st.TaskID, action, i.attempt()); err != nil || seen {
		return err
	}
	_, err := ledger.Append(ctx, tx, ledger.Event{Type: EvPullRequestAction, OrganizationID: org,
		ProjectID: st.ProjectID, TaskID: st.TaskID, ActorType: ledger.ActorSystem, ActorID: "workflow",
		Source: ledger.SourceOrchestrator, CorrelationID: st.TaskID, Payload: payload})
	return err
}

// attempt names one integration attempt on its activity lines.
func (i *Integration) attempt() string { return i.pair() + ":" + i.RunID }

func integrationEventSeen(ctx context.Context, tx pgx.Tx, taskID, action, attempt string) (bool, error) {
	var seen bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM events WHERE task_id = $1
			AND event_type = 'pull_request.action' AND correlation_id = $1
			AND payload->>'action' = $2 AND payload->>'attempt' = $3)`,
		taskID, action, attempt).Scan(&seen)
	return seen, err
}

func (w *steps) prepareIntegration(ctx context.Context, sc workflow.StepContext, st *State, stop *forge.Signal) (bool, error) {
	gh, err := w.forges.For(ctx, sc.OrganizationID)
	if err != nil || gh == nil {
		return false, err
	}
	// Integrating is update's, or a person's request. A pair dude already
	// attempted is looked up whatever the setting, so a hand-over the
	// syncer finds still conflicting escalates as dude's attempt.
	integrate := gh.Settings.WhenBehind == "update" || stop.Requested
	if !integrate && !st.attemptedIn(stop.Repo) {
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
		// One GET /pulls/{n}: its state, head and mergeability.
		fresh, mergeable, e := gh.Mergeability(ctx, p.Slug, p.Number)
		if e != nil {
			return false, e
		}
		if fresh.State == forge.StateClosed || fresh.State == forge.StateMerged || mergeable != forge.MergeConflicting {
			return false, nil
		}
		head := fresh.HeadSHA
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
		if !integrate {
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
