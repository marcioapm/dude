package delivery

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

// The bounds the delivery holds a task to, as its checks read them: the
// conductor's limits tool reads them through the same functions, so the
// two cannot disagree.

// RoundFixesSpent says this review round's pull request fixes are at the
// delivery policy's MaxPRFixIterations: no further fix starts in it.
func (st *State) RoundFixesSpent() bool {
	return st.PRIteration >= st.Policy.MaxPRFixIterations
}

// PullRequestFixesSpent says the pull request in repo has had total fix
// rounds (the organisation's fixRoundsPerPr; 0 is no bound).
func (st *State) PullRequestFixesSpent(repo string, total int) bool {
	return total > 0 && st.PRFixes[repo] >= total
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

// rerunClaims are the task's ($1) rerun claims (event $2, action $3), as e:
// a claim GitHub refused outright (released) does not count.
const rerunClaims = `SELECT e.payload->>'repo' AS repo, (e.payload->>'number')::int AS number, e.payload->>'head' AS head
	FROM events e WHERE e.task_id = $1 AND e.event_type = $2 AND e.payload->>'action' = $3 AND e.payload->>'released' IS NULL
	AND NOT EXISTS (SELECT 1 FROM events r WHERE r.task_id = e.task_id AND r.event_type = e.event_type AND r.payload->>'released' = e.id)`

// RerunClaimedTx says the failed checks of repo's pull request number at
// head were already run again for the task, by any of its Runs.
func RerunClaimedTx(ctx context.Context, tx pgx.Tx, taskID, repo string, number int, head string) (bool, error) {
	var claimed bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM (`+rerunClaims+`) c WHERE c.repo = $4 AND c.number = $5 AND c.head = $6)`,
		taskID, EvPullRequestAction, ActionRerunChecks, repo, number, head).Scan(&claimed)
	return claimed, err
}

// What a task's delivery is, for its limits.
const (
	DeliveryNone  = ""
	DeliveryLive  = "live"
	DeliveryEnded = "ended"
)

// TaskLimits are a task's delivery bounds and what of them is spent.
type TaskLimits struct {
	// DeliveryLive: Policy is the one the delivery froze when it started,
	// PRIteration and PRFixes what it has spent. With none, or one that
	// ended, Policy is what a delivery would start with now, and nothing
	// is spent.
	Delivery string
	Policy   Policy
	// What a delivery starting now would get, when it differs from Policy.
	PolicyNow *Policy
	// The layer the policy's MaxPRFixIterations is set in now (SetIn…).
	FixRoundsSetIn string
	// Fix rounds spent in this review round, and on each pull request.
	PRIteration int
	PRFixes     map[string]int
	GitHub      forge.Settings
	Connected   bool
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
	// Its failed checks were already run again at Head.
	RerunClaimed bool
}

// RoundFixesSpent is State.RoundFixesSpent for the limits read.
func (l TaskLimits) RoundFixesSpent() bool {
	st := State{PRIteration: l.PRIteration, Policy: l.Policy}
	return st.RoundFixesSpent()
}

// PullRequestFixesSpent is State.PullRequestFixesSpent for the limits read.
func (l TaskLimits) PullRequestFixesSpent(repo string) bool {
	st := State{PRFixes: l.PRFixes}
	return st.PullRequestFixesSpent(repo, l.GitHub.FixRoundsPerPR)
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
	switch {
	case d != nil && d.Live():
		l.Delivery, l.Policy, l.PRIteration, l.PRFixes = DeliveryLive, d.State.Policy, d.State.PRIteration, d.State.PRFixes
		if now.MaxPRFixIterations != l.Policy.MaxPRFixIterations {
			l.PolicyNow = &now
		}
	case d != nil:
		l.Delivery, l.Policy = DeliveryEnded, now
	default:
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
	// Each open pull request with its rerun claim at its head, in one query.
	rows, err := tx.Query(ctx, `WITH claims AS (SELECT DISTINCT repo, number, head FROM (`+rerunClaims+`) c)
		SELECT r.name, pr.number, COALESCE(pr.head_sha, ''), c.repo IS NOT NULL
		FROM pull_requests pr JOIN repositories r ON r.id = pr.repository_id
		LEFT JOIN claims c ON c.repo = r.name AND c.number = pr.number AND c.head = COALESCE(pr.head_sha, '')
		WHERE pr.task_id = $1 AND pr.state IN ('open', 'draft') ORDER BY pr.created_at, pr.id`,
		taskID, EvPullRequestAction, ActionRerunChecks)
	if err != nil {
		return l, err
	}
	prs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (LimitsPR, error) {
		var p LimitsPR
		return p, r.Scan(&p.Repo, &p.Number, &p.Head, &p.RerunClaimed)
	})
	if err != nil {
		return l, err
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
