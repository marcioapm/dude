package delivery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"maps"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
)

var (
	answersTmpl    = mustTemplate("wakes/fixer-answers")
	reconciledTmpl = mustTemplate("wakes/fixer-answers/reconciled")
)

// Line is the conductor's wake for what dude did with a fixer's answers.
func (f FixerAnswers) Line() string {
	var commits []string
	for _, repo := range slices.Sorted(maps.Keys(f.Commits)) {
		commits = append(commits, repo+"@"+short(f.Commits[repo]))
	}
	return render(answersTmpl, struct {
		FixerAnswers
		CommitList []string
	}{f, commits})
}

// reconciledLine says what an answer in flight when its delivery ended
// ended as (outcome), and, for an uncertain one, why dude cannot tell.
func reconciledLine(runID, label, ended, outcome, detail string) string {
	return render(reconciledTmpl, struct{ RunID, Label, Ended, Outcome, Detail string }{runID, label, ended, outcome, detail})
}

// wakeAnswersTx wakes the task's live conductor, if any, with what was
// done with fixer f's answers, once per answers settled (answersKey): it
// may add to or correct them. Nothing settled: nothing.
func wakeAnswersTx(ctx context.Context, tx pgx.Tx, org, taskID string, f FixerAnswers) error {
	if !f.settled() {
		return nil
	}
	return wakeLiveConductorTx(ctx, tx, org, taskID, WakeRound, answersKey(f), f.Line())
}

// answersKey is f's wake key: "answers:<run>:" and 16 hex digits of the
// sha256 of the answers it settled, so the same answers wake nobody twice
// and the key's size is fixed however many there are.
func answersKey(f FixerAnswers) string {
	sum := sha256.Sum256([]byte(strings.Join(f.ids, ",")))
	return "answers:" + f.RunID + ":" + hex.EncodeToString(sum[:8])
}

// AnswersKeyForIDs is answersKey for run's answers ids; for tests.
func AnswersKeyForIDs(run string, ids []string) string {
	return answersKey(FixerAnswers{RunID: run, ids: ids})
}
