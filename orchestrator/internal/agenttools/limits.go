package agenttools

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// limits (capabilities.LimitsRead) is the bounds the delivery holds the
// caller's task to, where each is set, and what is spent: read through the
// functions the checks themselves use (delivery.TaskLimitsTx, callsMade,
// githubReadsThisHour, machineMoves), so the answer is what the next check
// will find. A bound people raised for the task (request_limit) shows its
// setting's value, each raise and who granted it, and the sum.

type limitsIn struct{}

// limitOut is one bound: its value, where it is set, and what is spent.
type limitOut struct {
	Source string `json:"source"`
	// The layer of the source the value is set in, where it has layers.
	SetIn string `json:"setIn,omitempty"`
	grantsOut
	Limit     int `json:"limit"`
	Used      int `json:"used"`
	Remaining int `json:"remaining"`
}

// grantsOut is what people granted on top of a setting for this task: the
// setting's own value, each raise, and the sum said in words. Empty for a
// bound nobody raised.
type grantsOut struct {
	Base    *int         `json:"base,omitempty"`
	Granted []grantedOut `json:"granted,omitempty"`
	Line    string       `json:"line,omitempty"`
}

// grantedOut is one person's raise of a limit, for this task's delivery.
type grantedOut struct {
	Extra int    `json:"extra"`
	By    string `json:"by"`
	At    string `json:"at"`
}

// grantsFor are the grants of limit (a rerun's, at that head) over base,
// from source.
func grantsFor(grants []delivery.LimitGrant, limit, repo string, number int, head, source string, base int) grantsOut {
	var out grantsOut
	total := base
	for _, g := range grants {
		if g.Limit == limit && (limit != delivery.LimitRerun || g.Repo == repo && g.Number == number && g.Head == head) {
			out.Granted = append(out.Granted, grantedOut{Extra: g.Extra, By: g.ByName, At: g.At.UTC().Format(time.RFC3339)})
			total += g.Extra
		}
	}
	if len(out.Granted) == 0 {
		return out
	}
	out.Base = &base
	out.Line = limitsText("granted-line", struct {
		Source      string
		Base, Total int
		Grants      []grantedOut
	}{source, base, total, out.Granted})
	return out
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
	// What a delivery starting now would get: the task's froze its own.
	PolicyNow *int `json:"policyNow,omitempty"`
	// The organisation's fix rounds per pull request (fixRoundsPerPr); 0 is no bound.
	PerPullRequest struct {
		Source string `json:"source"`
		grantsOut
		Limit int          `json:"limit"`
		PRs   []prFixesOut `json:"pullRequests"`
	} `json:"perPullRequest"`
	Note string `json:"note"`
}

type settingOut struct {
	Source string `json:"source"`
	Value  any    `json:"value"`
}

type rerunHeadOut struct {
	Repo   string `json:"repo"`
	Number int    `json:"number"`
	Head   string `json:"head"`
	// The head's reruns of failed checks are spent: Reruns of Allowed.
	Used    bool `json:"used"`
	Reruns  int  `json:"reruns"`
	Allowed int  `json:"allowed"`
	grantsOut
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
	out.Delivery = limitsText("delivery", l.Delivered)

	round := l.RoundFixLimit()
	out.FixRounds.PerReviewRound = limitOut{Source: policy, SetIn: l.FixRoundsSetIn, Limit: round, Used: l.PRIteration,
		Remaining: max(0, round-l.PRIteration),
		grantsOut: grantsFor(l.Grants, delivery.LimitRoundFixes, "", 0, "", policy, l.Policy.MaxPRFixIterations)}
	if l.PolicyNow != nil {
		out.FixRounds.PolicyNow = &l.PolicyNow.MaxPRFixIterations
	}
	perPR := l.PullRequestFixLimit()
	out.FixRounds.PerPullRequest.Source, out.FixRounds.PerPullRequest.Limit = settings, perPR
	if l.GitHub.FixRoundsPerPR > 0 {
		out.FixRounds.PerPullRequest.grantsOut = grantsFor(l.Grants, delivery.LimitPullRequestFixes, "", 0, "", settings,
			l.GitHub.FixRoundsPerPR)
	}
	out.FixRounds.PerPullRequest.PRs = []prFixesOut{}
	roundLeft := out.FixRounds.PerReviewRound.Remaining
	if l.RoundFixesSpent() {
		roundLeft = 0
	}
	for _, p := range l.PullRequests {
		used := l.PRFixes[p.Repo]
		pr := prFixesOut{Repo: p.Repo, Number: p.Number, Used: used, Binding: policy}
		if perPR > 0 {
			left := max(0, perPR-used)
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
	out.FixRounds.Note = limitsText("fix-rounds-note", c.grants.Tool("request_limit"))

	out.CIStuck = settingOut{Source: settings, Value: l.GitHub.CIStuckMinutes}
	out.WhenBehind = settingOut{Source: settings, Value: l.GitHub.WhenBehind}

	heads := []rerunHeadOut{}
	for _, p := range l.PullRequests {
		heads = append(heads, rerunHeadOut{Repo: p.Repo, Number: p.Number, Head: p.Head, Used: p.RerunClaimed,
			Reruns: p.Reruns, Allowed: p.RerunsAllowed,
			grantsOut: grantsFor(l.Grants, delivery.LimitRerun, p.Repo, p.Number, p.Head, limitsText("dude", nil), 1)})
	}
	_, reruns, err := callsMade(ctx, tx, c.RunID, "rerun_failed_checks")
	if err != nil {
		return out, err
	}
	out.Reruns = map[string]any{
		"perHead": heads, "perHeadNote": limitsText("rerun-head", c.grants.Tool("request_limit")),
		"thisRun": limitOut{Source: limitsText("dude", nil), Limit: rerunsPerRun, Used: reruns, Remaining: max(0, rerunsPerRun-reruns)},
	}

	reads, err := githubReadsThisHour(ctx, tx, c)
	if err != nil {
		return out, err
	}
	out.GitHubReads = limitOut{Source: limitsText("dude", nil), Limit: githubReadsPerHour, Used: reads,
		Remaining: max(0, githubReadsPerHour-reads)}

	out.Publish = map[string]any{"source": policy, "linesPerPublish": l.PublishLines, "filesPerPublish": l.PublishFiles}

	_, moves, _, err := machineMoves(ctx, tx, c.RunID)
	if err != nil {
		return out, err
	}
	out.MachineMoves = limitOut{Source: limitsText("dude", nil), Limit: machineMovesPerRun, Used: moves,
		Remaining: max(0, machineMovesPerRun-moves)}
	return out, nil
}

// limitsText renders results/limits.md's text name.
func limitsText(name string, data any) string { return limitsTexts[name].Render(data) }

// limitsTexts are limits.md's texts, resolved at init.
var limitsTexts = func() map[string]delivery.Text {
	out := map[string]delivery.Text{}
	for _, name := range []string{"policy", "settings", "dude", "both", "delivery", "fix-rounds-note", "rerun-head", "granted-line"} {
		out[name] = delivery.MustText("results/limits/" + name)
	}
	return out
}()

// ---- request_limit -----------------------------------------------------------

type requestLimitIn struct {
	Limit  string `json:"limit"`
	To     int    `json:"to,omitempty"`
	PR     string `json:"pr,omitempty"`
	Reason string `json:"reason"`
}

type requestLimitOut struct {
	QuestionID string `json:"questionId"`
	Next       string `json:"next"`
}

// requestLimit (capabilities.LimitsRequest) asks the task's people to
// raise one of its delivery's limits (delivery.RequestLimitTx): the Run's
// one open question, which ends its turn.
func requestLimit(ctx context.Context, tx pgx.Tx, c Caller, in requestLimitIn) (requestLimitOut, error) {
	if c.TaskID == "" {
		return requestLimitOut{}, refuseText(rLimitsAreATasks)
	}
	id, err := conducted(delivery.RequestLimitTx(ctx, tx, c.run(), delivery.LimitAsk{Limit: strings.TrimSpace(in.Limit),
		To: in.To, PR: in.PR, Reason: in.Reason}))
	if err != nil {
		return requestLimitOut{}, err
	}
	return requestLimitOut{QuestionID: id, Next: limitRequestAsked}, nil
}

var limitRequestAsked = delivery.MustText("results/limit-request/asked").Render(nil)
