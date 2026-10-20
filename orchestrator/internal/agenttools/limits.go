package agenttools

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// limits (capabilities.LimitsRead) is the bounds the delivery holds the
// caller's task to, where each is set, and what is spent: read through the
// functions the checks themselves use (delivery.TaskLimitsTx, callsMade,
// githubReadsThisHour, machineMoves), so the answer is what the next check
// will find.

type limitsIn struct{}

// limitOut is one bound: its value, where it is set, and what is spent.
type limitOut struct {
	Source string `json:"source"`
	// The layer of the source the value is set in, where it has layers.
	SetIn     string `json:"setIn,omitempty"`
	Limit     int    `json:"limit"`
	Used      int    `json:"used"`
	Remaining int    `json:"remaining"`
}

type prFixesOut struct {
	Repo      string `json:"repo"`
	Number    int    `json:"number"`
	Used      int    `json:"used"`
	Remaining *int   `json:"remaining"`
	// The bound that stops this pull request's next fix first.
	Binding string `json:"binding"`
}

type fixRoundsOut struct {
	// The delivery policy's fixes per review round (MaxPRFixIterations).
	PerReviewRound limitOut `json:"perReviewRound"`
	// What a delivery starting now would get, and the layer of the policy
	// it is set in: the task's delivery froze another.
	PolicyNow *policyNowOut `json:"policyNow,omitempty"`
	// The organisation's fix rounds per pull request (fixRoundsPerPr); 0 is no bound.
	PerPullRequest struct {
		Source string       `json:"source"`
		Limit  int          `json:"limit"`
		PRs    []prFixesOut `json:"pullRequests"`
	} `json:"perPullRequest"`
	Note string `json:"note"`
}

type policyNowOut struct {
	Limit int    `json:"limit"`
	SetIn string `json:"setIn"`
}

type settingOut struct {
	Source string `json:"source"`
	Value  any    `json:"value"`
}

type rerunHeadOut struct {
	Repo   string `json:"repo"`
	Number int    `json:"number"`
	Head   string `json:"head"`
	// The head's one rerun of failed checks is spent.
	Used bool `json:"used"`
}

type limitsOut struct {
	Delivery     string       `json:"delivery"`
	FixRounds    fixRoundsOut `json:"fixRounds"`
	CIStuck      settingOut   `json:"ciStuckMinutes"`
	WhenBehind   settingOut   `json:"whenBehind"`
	Reruns       any          `json:"failedCheckReruns"`
	GitHubReads  limitOut     `json:"githubReadsThisHour"`
	Publish      any          `json:"publish"`
	MachineMoves limitOut     `json:"machineMovesThisRun"`
}

func readLimits(ctx context.Context, tx pgx.Tx, c Caller, _ limitsIn) (limitsOut, error) {
	var out limitsOut
	if c.TaskID == "" {
		return out, refuseText(rLimitsAreATasks)
	}
	l, err := delivery.TaskLimitsTx(ctx, tx, c.TaskID)
	if err != nil {
		return out, err
	}
	policy, settings := limitsText("policy", nil), limitsText("settings", nil)
	out.Delivery = limitsText("delivery", l.Delivery)

	// setIn is the layer today's policy takes the value from: beside
	// policyNow when the delivery froze another, else beside the limit.
	// Remaining is 0 where the checks' own getters say spent.
	round := l.Policy.MaxPRFixIterations
	out.FixRounds.PerReviewRound = limitOut{Source: policy, Limit: round, Used: l.PRIteration, Remaining: round - l.PRIteration}
	if l.RoundFixesSpent() {
		out.FixRounds.PerReviewRound.Remaining = 0
	}
	if l.PolicyNow != nil {
		out.FixRounds.PolicyNow = &policyNowOut{Limit: l.PolicyNow.MaxPRFixIterations, SetIn: l.FixRoundsSetIn}
	} else {
		out.FixRounds.PerReviewRound.SetIn = l.FixRoundsSetIn
	}
	perPR := l.GitHub.FixRoundsPerPR
	out.FixRounds.PerPullRequest.Source, out.FixRounds.PerPullRequest.Limit = settings, perPR
	out.FixRounds.PerPullRequest.PRs = []prFixesOut{}
	roundLeft := out.FixRounds.PerReviewRound.Remaining
	for _, p := range l.PullRequests {
		used := l.PRFixes[p.Repo]
		pr := prFixesOut{Repo: p.Repo, Number: p.Number, Used: used, Binding: policy}
		if perPR > 0 {
			left := perPR - used
			if l.PullRequestFixesSpent(p.Repo) {
				left = 0
			}
			pr.Remaining = &left
			switch {
			case left < roundLeft:
				pr.Binding = settings
			case left == roundLeft:
				pr.Binding = limitsText("both", nil)
			}
		}
		out.FixRounds.PerPullRequest.PRs = append(out.FixRounds.PerPullRequest.PRs, pr)
	}
	out.FixRounds.Note = limitsText("fix-rounds-note", nil)

	out.CIStuck = settingOut{Source: settings, Value: l.GitHub.CIStuckMinutes}
	out.WhenBehind = settingOut{Source: settings, Value: l.GitHub.WhenBehind}

	heads := []rerunHeadOut{}
	for _, p := range l.PullRequests {
		heads = append(heads, rerunHeadOut{Repo: p.Repo, Number: p.Number, Head: p.Head, Used: p.RerunClaimed})
	}
	_, reruns, err := callsMade(ctx, tx, c.RunID, "rerun_failed_checks")
	if err != nil {
		return out, err
	}
	out.Reruns = map[string]any{
		"perHead": heads, "perHeadNote": limitsText("rerun-head", nil),
		"thisRun": dudeLimit(rerunsPerRun, reruns),
	}

	reads, err := githubReadsThisHour(ctx, tx, c)
	if err != nil {
		return out, err
	}
	out.GitHubReads = dudeLimit(githubReadsPerHour, reads)

	out.Publish = map[string]any{"source": policy, "linesPerPublish": l.PublishLines, "filesPerPublish": l.PublishFiles}

	_, moves, _, err := machineMoves(ctx, tx, c.RunID)
	if err != nil {
		return out, err
	}
	out.MachineMoves = dudeLimit(machineMovesPerRun, moves)
	return out, nil
}

// dudeLimit is one of dude's own bounds, limit, with used spent of it.
func dudeLimit(limit, used int) limitOut {
	return limitOut{Source: limitsText("dude", nil), Limit: limit, Used: used, Remaining: max(0, limit-used)}
}

// limitsText renders results/limits.md's text name.
func limitsText(name string, data any) string { return limitsTexts[name].Render(data) }

// limitsTexts are limits.md's texts, resolved at init.
var limitsTexts = func() map[string]delivery.Text {
	out := map[string]delivery.Text{}
	for _, name := range []string{"policy", "settings", "dude", "both", "delivery", "fix-rounds-note", "rerun-head"} {
		out[name] = delivery.MustText("results/limits/" + name)
	}
	return out
}()
