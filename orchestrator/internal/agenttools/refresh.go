package agenttools

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/prs"
)

// refresh_pull_requests (capabilities.PRRefresh) reads the task's open pull
// requests from GitHub now, through prs.Syncer.Sync, and answers the
// pull_requests listing. It is checked and its GitHub read spent in the
// call's transaction, synced with none open (Sync opens its own and retries
// a sync another made at the same time), and listed in a second.

type refreshIn struct{}

type sessionRefreshIn struct {
	Project string `json:"project,omitempty"`
	Task    string `json:"task"`
}

// refreshCheck is a refresh that passed its checks: the task (as the
// listing reads it) and its pull requests to sync.
type refreshCheck struct {
	scoped Caller
	prIDs  []string
}

func checkRefresh(ctx context.Context, tx pgx.Tx, c, scoped Caller) (refreshCheck, error) {
	gh, err := githubOf(c)
	if err != nil {
		return refreshCheck{}, err
	}
	if gh == nil || c.env.syncPR == nil {
		return refreshCheck{}, refuseText(rThisOrganisationHasNoGithubConnection)
	}
	rows, err := tx.Query(ctx, `SELECT pr.id, r.url FROM pull_requests pr JOIN repositories r ON r.id = pr.repository_id
		WHERE pr.task_id = $1 AND pr.state IN ('open', 'draft') ORDER BY pr.created_at, pr.id`, scoped.TaskID)
	if err != nil {
		return refreshCheck{}, err
	}
	type openPR struct{ id, url string }
	open, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (openPR, error) {
		var p openPR
		return p, r.Scan(&p.id, &p.url)
	})
	if err != nil {
		return refreshCheck{}, err
	}
	if len(open) == 0 {
		return refreshCheck{}, refuseText(rNoOpenPullRequestRefresh)
	}
	// Sync reads nothing of a repository not on GitHub: not one to charge.
	var ids []string
	for _, p := range open {
		if forge.SlugFromURL(p.url) != "" {
			ids = append(ids, p.id)
		}
	}
	if len(ids) == 0 {
		return refreshCheck{}, refuseText(rNoOpenPullRequestOnGithub)
	}
	// One read per pull request synced (a sync makes several requests, which
	// Sync does not report): the whole batch fits, or nothing is synced.
	if err := checkGitHubBudget(ctx, tx, c, len(ids)); err != nil {
		return refreshCheck{}, err
	}
	return refreshCheck{scoped: scoped, prIDs: ids}, nil
}

func (r refreshCheck) githubReads() int { return len(r.prIDs) }

func refreshPullRequests(ctx context.Context, tx pgx.Tx, c Caller, _ refreshIn) (refreshCheck, error) {
	return checkRefresh(ctx, tx, c, c)
}

func sessionRefreshPullRequests(ctx context.Context, tx pgx.Tx, c Caller, in sessionRefreshIn) (refreshCheck, error) {
	if strings.TrimSpace(in.Task) == "" {
		return refreshCheck{}, refuseText(rNameTaskWhosePullRequestsRefresh)
	}
	scoped, _, _, err := taskOf(ctx, tx, c, taskIn{Project: in.Project, Task: in.Task})
	if err != nil {
		return refreshCheck{}, err
	}
	return checkRefresh(ctx, tx, c, scoped)
}

// syncNow syncs each pull request with no transaction open. One GitHub
// would not answer is said beside the listing, which keeps dude's last
// poll of it; one another sync recorded first was refreshed by it; a
// database failure is the call's.
func syncNow(ctx context.Context, c Caller, r refreshCheck) ([]string, error) {
	var failed []string
	for _, id := range r.prIDs {
		if err := c.env.syncPR(ctx, c.Org, id); err != nil && !errors.Is(err, prs.ErrRaced) {
			if ctx.Err() != nil {
				return nil, err
			}
			var pg interface{ SQLState() string }
			if errors.As(err, &pg) {
				return nil, err
			}
			failed = append(failed, id)
		}
	}
	return failed, nil
}

func listRefreshed(ctx context.Context, tx pgx.Tx, _ Caller, r refreshCheck, failed []string) (any, error) {
	listed, err := pullRequests(ctx, tx, r.scoped, pullRequestsIn{})
	if err != nil {
		return nil, err
	}
	out := listed.(map[string]any)
	out["refreshed"] = len(r.prIDs) - len(failed)
	if len(failed) > 0 {
		rows, err := tx.Query(ctx, `SELECT r.name || '#' || pr.number FROM pull_requests pr JOIN repositories r ON r.id = pr.repository_id
			WHERE pr.id = ANY($1) ORDER BY pr.created_at, pr.id`, failed)
		if err != nil {
			return nil, err
		}
		names, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return nil, err
		}
		out["notRefreshed"] = refreshText("not-refreshed", strings.Join(names, ", "))
	}
	return out, nil
}

// refreshText renders results/refresh-pull-requests.md's text name.
func refreshText(name string, data any) string { return refreshTexts[name].Render(data) }

var refreshTexts = func() map[string]delivery.Text {
	out := map[string]delivery.Text{}
	for _, name := range []string{"not-refreshed"} {
		out[name] = delivery.MustText("results/refresh-pull-requests/" + name)
	}
	return out
}()

var refreshTools = []tool{
	around(define("refresh_pull_requests", refreshPullRequests).before(forgeFor).countingAttempts(), syncNow, listRefreshed),
	around(defineSession("refresh_pull_requests", sessionRefreshPullRequests).before(forgeFor).countingAttempts(), syncNow, listRefreshed),
}
