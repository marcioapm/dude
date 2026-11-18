package delivery

import (
	"context"
	"maps"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

var answersTmpl = mustTemplate("wakes/fixer-answers")

// answersLine is the conductor's wake for what dude did with a fixer's
// answers.
func answersLine(f FixerAnswers) string {
	var commits []string
	for _, repo := range slices.Sorted(maps.Keys(f.Commits)) {
		commits = append(commits, repo+"@"+short(f.Commits[repo]))
	}
	return render(answersTmpl, struct {
		FixerAnswers
		CommitList []string
	}{f, commits})
}

// Line is the conductor's wake for f (answersLine).
func (f FixerAnswers) Line() string { return answersLine(f) }

// wakeOnAnswers wakes the task's live conductor, when it has one, once per
// fixer's answers settled, with what dude posted, resolved, failed or
// withheld (answersLine): it may add to or correct them. A delivery under
// Deliver's policy has none, and no wake is recorded: the posting is all.
func (w *steps) wakeOnAnswers(ctx context.Context, sc workflow.StepContext, st *State, done []FixerAnswers) error {
	if len(done) == 0 {
		return nil
	}
	return w.s.DB.InOrg(ctx, sc.OrganizationID, func(tx pgx.Tx) error {
		for _, f := range done {
			if !f.settled() {
				continue
			}
			if err := wakeAnswersTx(ctx, tx, sc.OrganizationID, st.TaskID, f); err != nil {
				return err
			}
		}
		return nil
	})
}

// wakeAnswersTx wakes the task's live conductor, if any, with what was
// done with fixer f's answers, keyed by the answers it settled.
func wakeAnswersTx(ctx context.Context, tx pgx.Tx, org, taskID string, f FixerAnswers) error {
	return wakeLiveConductorTx(ctx, tx, org, taskID, WakeRound, "answers:"+f.RunID+":"+strings.Join(f.ids, ","), answersLine(f))
}
