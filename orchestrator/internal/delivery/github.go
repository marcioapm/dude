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
func prName(g PRGate) string { return fmt.Sprintf("%s#%d", g.Repo, g.Number) }

// readinessLine says which pull requests are ready to merge, or which
// one no longer is and which of its gate's items fail.
func readinessLine(gates []PRGate, ready bool) string {
	var open []string
	for _, g := range gates {
		name := prName(g)
		if !ready && !g.Ready {
			return fmt.Sprintf("%s is no longer ready to merge: %s.", name, failingLine(g))
		}
		open = append(open, name)
	}
	if !ready {
		return "The pull requests are no longer ready to merge."
	}
	verb := "is"
	if len(open) > 1 {
		verb = "are"
	}
	return fmt.Sprintf("%s %s ready to merge: reviewed through its head, checks green, nothing open. Merging is yours.", strings.Join(open, ", "), verb)
}

// failingLine names a gate's failing items, each with what it found.
func failingLine(g PRGate) string {
	var parts []string
	for _, it := range g.Failing() {
		parts = append(parts, it.Key+" ("+it.Detail+")")
	}
	return strings.Join(parts, "; ")
}

// moveReadiness moves the task into ready to merge, or out of it, and
// under the conductor says so in Chat, in the same transaction: a notice
// once per move, waking nobody, naming the gate's failing items when it
// moves out.
func (s *Store) moveReadiness(ctx context.Context, org string, st *State, gates []PRGate, ready bool, open int) error {
	return s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		var moved bool
		var err error
		if ready {
			if moved, err = SetTaskStatusTx(ctx, tx, org, st.ProjectID, st.TaskID, "review", "ready_to_merge",
				"the ready gate passes: merging is the only act left"); err == nil && moved {
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
		payload := map[string]any{"text": readinessLine(gates, ready), "about": about}
		var failing []map[string]any
		for _, g := range gates {
			for _, it := range g.Failing() {
				failing = append(failing, map[string]any{"pr": prName(g), "key": it.Key, "detail": it.Detail})
			}
		}
		if len(failing) > 0 {
			payload["failing"] = failing
		}
		_, err = ledger.Append(ctx, tx, ledger.Event{Type: EvChatNotice, OrganizationID: org, ProjectID: st.ProjectID,
			TaskID: st.TaskID, ActorType: ledger.ActorSystem, ActorID: "dude", Source: ledger.SourceOrchestrator,
			CorrelationID: st.TaskID, Payload: payload})
		return err
	})
}

// closeOutLine tells the conductor repo#number ended (state merged or
// closed) and what to do about it.
func closeOutLine(repo string, number int, state string) string {
	return render(prEndedTmpl, struct {
		Repo   string
		Number int
		Merged bool
	}{repo, number, state == forge.StateMerged})
}

// CloseOutTx records the reason to wake a conducted task's conductor for
// one pull request that ended (merged or closed; any other state records
// nothing): keyed by the pull request and how it ended, so every sync that
// sees it records it once. The pull request syncer records it as it reads
// the end, the workflow as it weighs it: one reason per (task, key) either
// way.
func CloseOutTx(ctx context.Context, tx pgx.Tx, org, taskID, repo string, number int, state string) error {
	var kind string
	switch state {
	case forge.StateMerged:
		kind = "pr_merged"
	case forge.StateClosed:
		kind = "pr_closed"
	default:
		return nil
	}
	line := closeOutLine(repo, number, state)
	_, err := RecordWakeTx(ctx, tx, org, taskID, kind, fmt.Sprintf("%s:%s:%d", kind, repo, number), line)
	return err
}
