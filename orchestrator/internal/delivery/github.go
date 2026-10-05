package delivery

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// What a conducted task's pull requests do that the conductor hears of:
// readiness is dude's notice in Chat and wakes nobody; a merge or a close
// wakes it once, to close out.

// EvChatNotice is dude's own line in a task's Chat, in its voice, about
// something that wakes nobody. Payload: {text, about}.
const EvChatNotice = "chat.notice"

// prName is a pull request as a line names it: repo#number.
func prName(p PullRequestState) string { return fmt.Sprintf("%s#%d", p.Repo, p.Number) }

// readinessLine says which pull requests are ready to merge, or which one
// no longer is and why.
func readinessLine(states []PullRequestState, ready bool) string {
	var open []string
	for _, s := range states {
		if s.State == forge.StateMerged || s.State == forge.StateClosed {
			continue
		}
		if !ready && !forge.Ready(s.Status) {
			return fmt.Sprintf("%s is no longer ready to merge: %s.", prName(s), strings.Join(forge.Blockers(s.Status), ", "))
		}
		open = append(open, prName(s))
	}
	if !ready {
		return "The pull requests are no longer ready to merge."
	}
	verb := "is"
	if len(open) > 1 {
		verb = "are"
	}
	return fmt.Sprintf("%s %s ready to merge: approved, checks green. Merging is yours.", strings.Join(open, ", "), verb)
}

// moveReadiness moves the task into ready to merge, or out of it, and
// under the conductor says so in Chat, in the same transaction: a notice
// once per move, waking nobody.
func (s *Store) moveReadiness(ctx context.Context, org string, st *State, states []PullRequestState, ready bool, open int) error {
	return s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		var moved bool
		var err error
		if ready {
			if moved, err = SetTaskStatusTx(ctx, tx, org, st.ProjectID, st.TaskID, "review", "ready_to_merge",
				"approved, checks passing"); err == nil && moved {
				err = emitTx(ctx, tx, org, st, EvReadyToMerge, map[string]any{"pullRequests": open})
			}
		} else {
			moved, err = SetTaskStatusTx(ctx, tx, org, st.ProjectID, st.TaskID, "ready_to_merge", "review", "no longer ready to merge")
		}
		if err != nil || !moved || !st.conducted() {
			return err
		}
		about := "ready_to_merge"
		if !ready {
			about = "no_longer_ready"
		}
		_, err = ledger.Append(ctx, tx, ledger.Event{Type: EvChatNotice, OrganizationID: org, ProjectID: st.ProjectID,
			TaskID: st.TaskID, ActorType: ledger.ActorSystem, ActorID: "dude", Source: ledger.SourceOrchestrator,
			CorrelationID: st.TaskID, Payload: map[string]any{"text": readinessLine(states, ready), "about": about}})
		return err
	})
}

// CloseOutTx records the reason to wake a conducted task's conductor for
// one pull request that ended (merged or closed; any other state records
// nothing): keyed by the pull request and how it ended, so every sync that
// sees it records it once. The pull request syncer records it as it reads
// the end, the workflow as it weighs it: one reason per (task, key) either
// way.
func CloseOutTx(ctx context.Context, tx pgx.Tx, org, taskID, repo string, number int, state string) error {
	name := fmt.Sprintf("%s#%d", repo, number)
	var kind, line string
	switch state {
	case forge.StateMerged:
		kind, line = "pr_merged", fmt.Sprintf("Pull request %s was merged.", name)
	case forge.StateClosed:
		kind, line = "pr_closed", fmt.Sprintf("Pull request %s was closed without merging.", name)
	default:
		return nil
	}
	line += " Close out: say so in Chat if it helps the people on the task. Start nothing."
	_, err := RecordWakeTx(ctx, tx, org, taskID, kind, fmt.Sprintf("%s:%s:%d", kind, repo, number), line)
	return err
}
