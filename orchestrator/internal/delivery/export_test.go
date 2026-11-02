package delivery

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// ReplayEventTypes are the types, in cursor order, of the events Replay
// reads from the ledger for the talker.
func ReplayEventTypes(ctx context.Context, tx pgx.Tx, of Talker) ([]string, error) {
	events, err := replayEvents(ctx, tx, of)
	types := make([]string, len(events))
	for i, e := range events {
		types[i] = e.Type
	}
	return types, err
}

// BeforeReplayQuery has fn called in the replay's transaction just before
// its ledger query starts; nil removes it.
func BeforeReplayQuery(fn func(context.Context, pgx.Tx)) { beforeReplayQuery = fn }

// BeforeTimeSpentQuery has fn called in a next round's transaction just
// before its predecessor's time used is read; nil removes it.
func BeforeTimeSpentQuery(fn func(context.Context, pgx.Tx)) { beforeTimeSpentQuery = fn }

// CreatePhaseRunTx is CreatePhaseRun in the caller's transaction, with the
// store's time limit.
func CreatePhaseRunTx(ctx context.Context, tx pgx.Tx, org string, in PhaseRun, limit time.Duration) (string, error) {
	in.timeLimit = limit
	return createPhaseRunTx(ctx, tx, org, in)
}

// PullRequestText is the title and body a pull request opens with.
func (s *Store) PullRequestText(ctx context.Context, org string, st *State) (string, string, error) {
	return s.pullRequestText(ctx, org, st)
}

// LoopFindings is what the review loop weighs for st (loopFindings).
func (s *Store) LoopFindings(ctx context.Context, org string, st *State) ([]FindingState, Convergence, error) {
	return (&steps{s: s}).loopFindings(ctx, org, st)
}
