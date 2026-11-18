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
			if err := wakeLiveConductorTx(ctx, tx, sc.OrganizationID, st.TaskID, WakeRound,
				"answers:"+f.RunID+":"+strings.Join(f.ids, ","), answersLine(f)); err != nil {
				return err
			}
		}
		return nil
	})
}
