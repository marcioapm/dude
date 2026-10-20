package delivery

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

// The bounds the delivery holds a task to, as its checks read them: the
// conductor's limits tool reads them through the same functions, so the
// two cannot disagree.

// The limits a person may raise for one task (request_limit): the keys of
// LimitGrant.Limit and of a limit request.
const (
	LimitRoundFixes       = "perReviewRound"
	LimitPullRequestFixes = "perPullRequest"
	LimitRerun            = "rerun"
)

// LimitKeys are the limits request_limit may ask to raise.
var LimitKeys = []string{LimitRoundFixes, LimitPullRequestFixes, LimitRerun}

// LimitGrant is a person's answer raising one of the task's limits for its
// delivery (State.LimitGrants): Extra on top of the setting, by whom.
type LimitGrant struct {
	Limit string `json:"limit"`
	Extra int    `json:"extra"`
	// A rerun's: the pull request head it is for.
	Repo   string    `json:"repo,omitempty"`
	Number int       `json:"number,omitempty"`
	Head   string    `json:"head,omitempty"`
	By     string    `json:"by"`
	ByName string    `json:"byName"`
	Asked  string    `json:"questionId"`
	At     time.Time `json:"at"`
}

// granted is what grants add to limit (and, for a rerun, at head).
func granted(grants []LimitGrant, limit, repo string, number int, head string) int {
	n := 0
	for _, g := range grants {
		if g.Limit == limit && (limit != LimitRerun || g.Repo == repo && g.Number == number && g.Head == head) {
			n += g.Extra
		}
	}
	return n
}

// RoundFixLimit is the fix rounds a review round may have: the delivery
// policy's MaxPRFixIterations, plus what people granted this delivery.
func (st *State) RoundFixLimit() int {
	return st.Policy.MaxPRFixIterations + granted(st.LimitGrants, LimitRoundFixes, "", 0, "")
}

// PullRequestFixLimit is the fix rounds each pull request may have: the
// organisation's total (fixRoundsPerPr; 0 is no bound, and stays so), plus
// what people granted this delivery.
func (st *State) PullRequestFixLimit(total int) int {
	if total <= 0 {
		return 0
	}
	return total + granted(st.LimitGrants, LimitPullRequestFixes, "", 0, "")
}

// RoundFixesSpent says this review round's pull request fixes are at
// RoundFixLimit: no further fix starts in it.
func (st *State) RoundFixesSpent() bool {
	return st.PRIteration >= st.RoundFixLimit()
}

// PullRequestFixesSpent says the pull request in repo has had its fix
// rounds: PullRequestFixLimit of the organisation's total.
func (st *State) PullRequestFixesSpent(repo string, total int) bool {
	limit := st.PullRequestFixLimit(total)
	return limit > 0 && st.PRFixes[repo] >= limit
}

// GitHubSettingsTx are the organisation's GitHub settings as dude applies
// them (forge.ReadSettings), and whether it has a GitHub connection at all;
// without one, the defaults.
func GitHubSettingsTx(ctx context.Context, tx pgx.Tx) (forge.Settings, bool, error) {
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT settings FROM forge_credentials WHERE forge = 'github' LIMIT 1`).Scan(&raw)
	if db.IsNotFound(err) {
		return forge.DefaultSettings(), false, nil
	}
	if err != nil {
		return forge.Settings{}, false, err
	}
	return forge.ReadSettings(raw), true, nil
}

// RerunsAtHeadTx counts the times the failed checks of repo's pull request
// number at head were run again for the task, by any of its Runs: a claim
// GitHub refused outright (released) does not count.
func RerunsAtHeadTx(ctx context.Context, tx pgx.Tx, taskID, repo string, number int, head string) (int, error) {
	var n int
	err := tx.QueryRow(ctx, `SELECT count(*) FROM events e WHERE e.task_id = $1 AND e.event_type = $2
		AND e.payload->>'action' = $3 AND e.payload->>'repo' = $4 AND (e.payload->>'number')::int = $5 AND e.payload->>'head' = $6
		AND e.payload->>'released' IS NULL
		AND NOT EXISTS (SELECT 1 FROM events r WHERE r.task_id = e.task_id AND r.event_type = e.event_type
			AND r.payload->>'released' = e.id)`,
		taskID, EvPullRequestAction, ActionRerunChecks, repo, number, head).Scan(&n)
	return n, err
}

// RerunsAllowed is how often a head's failed checks may be run again: once,
// plus what people granted for that head on the task's delivery.
func RerunsAllowed(grants []LimitGrant, repo string, number int, head string) int {
	return 1 + granted(grants, LimitRerun, repo, number, head)
}

// RerunClaimedTx says the failed checks of repo's pull request number at
// head were already run again as often as they may be (RerunsAllowed).
func RerunClaimedTx(ctx context.Context, tx pgx.Tx, taskID, repo string, number int, head string) (bool, error) {
	n, err := RerunsAtHeadTx(ctx, tx, taskID, repo, number, head)
	if err != nil {
		return false, err
	}
	d, err := ReadDelivery(ctx, tx, taskID)
	if err != nil {
		return false, err
	}
	var grants []LimitGrant
	if d != nil {
		grants = d.State.LimitGrants
	}
	return n >= RerunsAllowed(grants, repo, number, head), nil
}

// TaskLimits are a task's delivery bounds and what of them is spent.
type TaskLimits struct {
	// The task has a delivery: Policy is the one it froze when it started,
	// PRIteration and PRFixes what it has spent. Without one, Policy is
	// what a delivery would start with now, and nothing is spent.
	Delivered bool
	Policy    Policy
	// What a delivery starting now would get, when it differs from Policy.
	PolicyNow *Policy
	// The layer the policy's MaxPRFixIterations is set in now (SetIn…).
	FixRoundsSetIn string
	// Fix rounds spent in this review round, and on each pull request.
	PRIteration int
	PRFixes     map[string]int
	GitHub      forge.Settings
	Connected   bool
	// What people granted the delivery beyond the settings.
	Grants []LimitGrant
	// The task's open pull requests.
	PullRequests []LimitsPR
	// One conductor publish's bounds, from the policy as it is now.
	PublishLines, PublishFiles int
}

// LimitsPR is one of the task's open pull requests, as its rerun claim reads it.
type LimitsPR struct {
	Repo   string
	Number int
	Head   string
	// Its failed checks were run again at Head this often, of Allowed.
	Reruns, RerunsAllowed int
	// They may not be run again at Head.
	RerunClaimed bool
}

func (l TaskLimits) state() *State {
	return &State{PRIteration: l.PRIteration, PRFixes: l.PRFixes, Policy: l.Policy, LimitGrants: l.Grants}
}

// RoundFixLimit is State.RoundFixLimit for the limits read.
func (l TaskLimits) RoundFixLimit() int { return l.state().RoundFixLimit() }

// PullRequestFixLimit is State.PullRequestFixLimit for the limits read.
func (l TaskLimits) PullRequestFixLimit() int {
	return l.state().PullRequestFixLimit(l.GitHub.FixRoundsPerPR)
}

// RoundFixesSpent is State.RoundFixesSpent for the limits read.
func (l TaskLimits) RoundFixesSpent() bool { return l.state().RoundFixesSpent() }

// PullRequestFixesSpent is State.PullRequestFixesSpent for the limits read.
func (l TaskLimits) PullRequestFixesSpent(repo string) bool {
	return l.state().PullRequestFixesSpent(repo, l.GitHub.FixRoundsPerPR)
}

// TaskLimitsTx reads the task's limits in tx, without locking anything.
func TaskLimitsTx(ctx context.Context, tx pgx.Tx, taskID string) (TaskLimits, error) {
	var l TaskLimits
	d, err := ReadDelivery(ctx, tx, taskID)
	if err != nil {
		return l, err
	}
	_, now, err := PolicyFor(ctx, tx, taskID)
	if err != nil {
		return l, err
	}
	if d != nil {
		l.Delivered, l.Policy, l.PRIteration, l.PRFixes = true, d.State.Policy, d.State.PRIteration, d.State.PRFixes
		l.Grants = d.State.LimitGrants
		if now.MaxPRFixIterations != l.Policy.MaxPRFixIterations {
			l.PolicyNow = &now
		}
	} else {
		l.Policy = now
	}
	if l.PRFixes == nil {
		l.PRFixes = map[string]int{}
	}
	if l.GitHub, l.Connected, err = GitHubSettingsTx(ctx, tx); err != nil {
		return l, err
	}
	if l.PublishLines, l.PublishFiles, err = PublishLimitsTx(ctx, tx, taskID); err != nil {
		return l, err
	}
	if l.FixRoundsSetIn, err = policySetIn(ctx, tx, taskID, "maxPrFixIterations"); err != nil {
		return l, err
	}
	rows, err := tx.Query(ctx, `SELECT r.name, pr.number, COALESCE(pr.head_sha, '')
		FROM pull_requests pr JOIN repositories r ON r.id = pr.repository_id
		WHERE pr.task_id = $1 AND pr.state IN ('open', 'draft') ORDER BY pr.created_at, pr.id`, taskID)
	if err != nil {
		return l, err
	}
	prs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (LimitsPR, error) {
		var p LimitsPR
		return p, r.Scan(&p.Repo, &p.Number, &p.Head)
	})
	if err != nil {
		return l, err
	}
	for i, p := range prs {
		if prs[i].Reruns, err = RerunsAtHeadTx(ctx, tx, taskID, p.Repo, p.Number, p.Head); err != nil {
			return l, err
		}
		prs[i].RerunsAllowed = RerunsAllowed(l.Grants, p.Repo, p.Number, p.Head)
		prs[i].RerunClaimed = prs[i].Reruns >= prs[i].RerunsAllowed
	}
	l.PullRequests = prs
	return l, nil
}

// Where a policy key's value comes from: the layer of ResolvePolicy that
// names it last.
const (
	SetInProject      = "project"
	SetInOrganization = "organization"
	SetInDefault      = "default"
)

// policySetIn is the layer the task's policy takes key from now.
func policySetIn(ctx context.Context, tx pgx.Tx, taskID, key string) (string, error) {
	var org, project bool
	err := tx.QueryRow(ctx, `SELECT COALESCE(o.delivery_policy ? $2, false), COALESCE(p.delivery_policy ? $2, false)
		FROM tasks t JOIN projects p ON p.id = t.project_id JOIN organizations o ON o.id = p.organization_id
		WHERE t.id = $1`, taskID, key).Scan(&org, &project)
	switch {
	case err != nil:
		return "", err
	case project:
		return SetInProject, nil
	case org:
		return SetInOrganization, nil
	}
	return SetInDefault, nil
}
