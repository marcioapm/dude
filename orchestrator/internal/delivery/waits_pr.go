package delivery

import (
	"cmp"
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// prEnded: any pull request the organisation's GitHub installation can
// read being merged or closed. One dude tracks (a pull_requests row of the
// organisation) is read from its row, which webhooks and the reconciler
// keep fresh: no read of GitHub. Any other is read once by wait_for, before
// its transaction (PrereadWaitPRs), and then by the sweep's ObserveWaits at
// most every prObserveEvery, each read spending one of the task's hourly
// GitHub reads.

// PRRead is one read of a pull request dude does not track, made for a
// wait_for call before its transaction: what GitHub said, or why it said
// nothing (Err, a forge error; Budget, the task's reads are spent).
type PRRead struct {
	End    forge.PullRequestEnd
	At     time.Time
	Err    error
	Budget bool
}

// prReadKey names a pull request across reads: owner/repo#N, lower-cased.
func prReadKey(slug string, number int) string {
	return strings.ToLower(slug) + "#" + strconv.Itoa(number)
}

// prTarget is a prEnded condition's pull request as named: its repository
// (owner/repo) and number, and its pull_requests row when dude tracks it.
type prTarget struct {
	Slug, Row, Name string
	Number          int
}

// resolvePRTx reads a prEnded's pull request name as prChecksFinal's are
// read (parsePR, namedBy): owner/repo#N names any repository; repo#N and #N
// name one of this task's project's repositories, by dude's name, and #N
// only when the project has one. The pull request is the organisation's
// pull_requests row for it when there is one.
func resolvePRTx(ctx context.Context, tx pgx.Tx, taskID, name string) (prTarget, error) {
	owner, repo, number, err := parsePR(name)
	if err != nil {
		return prTarget{}, err
	}
	t := prTarget{Number: number}
	if owner != "" {
		t.Slug = owner + "/" + repo
	} else {
		rows, err := tx.Query(ctx, `SELECT r.name, r.url FROM repositories r JOIN tasks t ON t.project_id = r.project_id
			WHERE t.id = $1 ORDER BY r.name`, taskID)
		if err != nil {
			return t, err
		}
		type repoRow struct{ Name, URL string }
		repos, err := pgx.CollectRows(rows, pgx.RowToStructByPos[repoRow])
		if err != nil {
			return t, err
		}
		var slugs []string
		for _, r := range repos {
			if s := forge.SlugFromURL(r.URL); s != "" && namedBy(r.URL, r.Name, "", repo) {
				slugs = append(slugs, s)
			}
		}
		switch len(slugs) {
		case 0:
			return t, refusef(rWaitPRRepoUnknown, strings.TrimSpace(name))
		case 1:
			t.Slug = slugs[0]
		default:
			return t, refusef(rWaitPRRepoAmbiguous, strings.TrimSpace(name))
		}
	}
	t.Name = t.Slug + "#" + strconv.Itoa(number)
	rows, err := tx.Query(ctx, `SELECT pr.id, r.url FROM pull_requests pr JOIN repositories r ON r.id = pr.repository_id
		WHERE pr.number = $1 AND pr.organization_id = (SELECT organization_id FROM tasks WHERE id = $2)
		ORDER BY pr.created_at DESC`, number, taskID)
	if err != nil {
		return t, err
	}
	type prRow struct{ ID, URL string }
	prs, err := pgx.CollectRows(rows, pgx.RowToStructByPos[prRow])
	if err != nil {
		return t, err
	}
	for _, p := range prs {
		if strings.EqualFold(forge.SlugFromURL(p.URL), t.Slug) {
			t.Row = p.ID
			break
		}
	}
	return t, nil
}

// PrereadWaitPRs reads, before wait_for's transaction, each pull request a
// prEnded condition names that dude does not track: once, through the
// organisation's GitHub, with no transaction open. A name it cannot read
// is left for OpenConductorWaitTx to refuse. With the task's GitHub reads
// spent it reads nothing and says so (PRRead.Budget).
func PrereadWaitPRs(ctx context.Context, d *db.DB, forges Forges, ref RunRef, conds []WaitCondition) map[string]PRRead {
	var targets []prTarget
	spent := false
	if err := d.InOrg(ctx, ref.Org, func(tx pgx.Tx) error {
		for _, c := range conds {
			if c.PREnded == nil {
				continue
			}
			t, err := resolvePRTx(ctx, tx, ref.TaskID, c.PREnded.PR)
			if err != nil {
				if IsRefusal(err) {
					continue
				}
				return err
			}
			if t.Row == "" {
				targets = append(targets, t)
			}
		}
		if len(targets) == 0 {
			return nil
		}
		reads, err := taskGitHubReadsTx(ctx, tx, ref.TaskID)
		spent = reads+len(targets) > GitHubReadsPerHour
		return err
	}); err != nil || len(targets) == 0 {
		return nil
	}
	out := map[string]PRRead{}
	var gh *forge.GitHub
	var ghErr error
	if !spent && forges != nil {
		gh, ghErr = forges.For(ctx, ref.Org)
	}
	for _, t := range targets {
		key := prReadKey(t.Slug, t.Number)
		if _, ok := out[key]; ok {
			continue
		}
		switch {
		case spent:
			out[key] = PRRead{Budget: true}
		case ghErr != nil:
			out[key] = PRRead{Err: ghErr}
		case gh == nil:
			out[key] = PRRead{Err: errNoGitHub}
		default:
			end, err := gh.PullRequestEnded(ctx, t.Slug, t.Number)
			out[key] = PRRead{End: end, Err: err, At: time.Now().UTC()}
		}
	}
	return out
}

var errNoGitHub = errors.New("this organisation has no GitHub connection")

// prEndedCondTx checks a prEnded condition at wait_for: how is merged,
// closed or any; a pull request dude tracks is its row; any other must have
// been read (reads, PrereadWaitPRs) and seen by GitHub, and that reading is
// its first observation.
func prEndedCondTx(ctx context.Context, tx pgx.Tx, taskID string, ask PREndedAsk, reads map[string]PRRead) (waitCond, error) {
	how := strings.TrimSpace(ask.How)
	switch how {
	case "":
		how = PREndedAny
	case PREndedMerged, PREndedClosed, PREndedAny:
	default:
		return waitCond{}, refusef(rWaitPRHowUnknown, how)
	}
	t, err := resolvePRTx(ctx, tx, taskID, ask.PR)
	if err != nil {
		return waitCond{}, err
	}
	c := waitCond{Kind: condPREnded, How: how, PRName: t.Name}
	if t.Row != "" {
		c.PR = t.Row
		return c, nil
	}
	read, ok := reads[prReadKey(t.Slug, t.Number)]
	switch {
	case !ok:
		return c, refusef(rWaitPRUnread, t.Name, "dude could not read it")
	case read.Budget:
		return c, refusef(rWaitPRBudget, t.Name, GitHubReadsPerHour)
	case read.Err != nil:
		return c, refusef(rWaitPRUnread, t.Name, prReadWhy(read.Err))
	}
	c.Slug, c.Number = t.Slug, t.Number
	c.Title = clip(oneLine(read.End.Title), 120)
	c.Obs = prObs(read.End, read.At)
	return c, nil
}

// prReadWhy is why GitHub did not answer a read of a pull request, in
// dude's words.
func prReadWhy(err error) string {
	var e *forge.Error
	switch {
	case errors.Is(err, errNoGitHub):
		return err.Error()
	case !errors.As(err, &e):
		return "GitHub did not answer"
	case e.NotFound():
		return "GitHub does not show it to this organisation's installation (not found)"
	case forge.RateLimited(err):
		return "GitHub's rate limit"
	case e.Status == 403:
		return "GitHub refused the read (forbidden)"
	}
	return "GitHub refused the read (" + strconv.Itoa(e.Status) + ")"
}

func prObs(end forge.PullRequestEnd, at time.Time) *waitObs {
	o := &waitObs{At: at, State: end.State, MergedBy: end.MergedBy}
	switch end.State {
	case forge.StateMerged:
		o.EndedAt = cmp.Or(end.MergedAt, end.ClosedAt)
	case forge.StateClosed:
		o.EndedAt = end.ClosedAt
	}
	return o
}

// spendGitHubRead records one GitHub read made for a wait on the task's
// budget: a counted attempt (tool wait_for, marked attempted so the Run's
// call counts are not spent) and a github.read event as the GitHub tools
// record theirs (tool, repo, number).
func spendGitHubRead(ctx context.Context, tx pgx.Tx, event func(typ, actor string, payload map[string]any) ledger.Event, c waitCond) error {
	if _, err := ledger.Append(ctx, tx, event(EvToolAttempted, ledger.ActorAgent, map[string]any{
		"tool": "wait_for", "attempted": true, "repo": c.Slug, "number": c.Number})); err != nil {
		return err
	}
	_, err := ledger.Append(ctx, tx, event(EvGitHubRead, ledger.ActorAgent, map[string]any{
		"tool": "wait_for", "repo": c.Slug, "number": c.Number}))
	return err
}

// reservePRReadTx spends one of the task's GitHub reads for an observation
// of o's pull request, under the budget's lock, before it is read: false,
// and nothing spent, when they are spent.
func reservePRReadTx(ctx context.Context, d *db.DB, o WaitObservation) (bool, error) {
	ok := false
	err := d.InOrg(ctx, o.Org, func(tx pgx.Tx) error {
		if err := LockGitHubReadsTx(ctx, tx, "task:"+o.TaskID); err != nil {
			return err
		}
		reads, err := taskGitHubReadsTx(ctx, tx, o.TaskID)
		if err != nil || reads >= GitHubReadsPerHour {
			return err
		}
		ok = true
		ref := RunRef{Org: o.Org, ProjectID: o.ProjectID, TaskID: o.TaskID, RunID: o.Conductor}
		return spendGitHubRead(ctx, tx, ref.Event, o.cond)
	})
	return ok && err == nil, err
}

// observePR reads o's pull request once, with no transaction open.
func observePR(ctx context.Context, forges Forges, o WaitObservation) waitObs {
	if forges == nil {
		return waitObs{Error: "unread: " + errNoGitHub.Error()}
	}
	gh, err := forges.For(ctx, o.Org)
	if err == nil && gh == nil {
		err = errNoGitHub
	}
	if err != nil {
		return waitObs{Error: "unread: " + prReadWhy(err)}
	}
	end, err := gh.PullRequestEnded(ctx, o.cond.Slug, o.cond.Number)
	if err != nil {
		return waitObs{Error: "unread: " + prReadWhy(err)}
	}
	return *prObs(end, time.Time{})
}

// prEndedWants says whether a pull request in state ended the way how
// asks: merged is merged; closed is closed without merging; any is either.
func prEndedWants(how, state string) bool {
	switch how {
	case PREndedMerged:
		return state == forge.StateMerged
	case PREndedClosed:
		return state == forge.StateClosed
	default:
		return state == forge.StateMerged || state == forge.StateClosed
	}
}

// prEndedItemTx reads a prEnded condition with no locks: a tracked pull
// request's row, or an untracked one's last observation.
func prEndedItemTx(ctx context.Context, tx pgx.Tx, c waitCond) (waitItem, error) {
	it := waitItem{Kind: "prEnded", PR: c.PRName, How: c.How, rank: rankWaiting}
	var title string
	var at *time.Time
	if c.PR != "" {
		var merged, closed *time.Time
		if err := tx.QueryRow(ctx, `SELECT state::text, title, merged_at, closed_at FROM pull_requests WHERE id = $1`, c.PR).
			Scan(&it.State, &title, &merged, &closed); err != nil {
			return it, err
		}
		if it.State == forge.StateMerged {
			at = cmp.Or(merged, closed)
		} else {
			at = closed
		}
	} else {
		title = c.Title
		if o := c.Obs; o != nil {
			it.Read = o.At.UTC().Format("15:04")
			if o.Error != "" {
				it.Error = o.Error
			} else {
				it.State, it.By, at = o.State, o.MergedBy, o.EndedAt
			}
		}
	}
	if title != "" {
		it.Title = ExternalText(c.PRName, clip(oneLine(title), 120))
	}
	if at != nil {
		it.At = at.UTC().Format("2006-01-02 15:04")
	}
	if prEndedWants(c.How, it.State) {
		it.Ended = true
		it.rank = rankGood
		if it.State == forge.StateClosed {
			it.rank = rankBad
		}
	}
	return it, nil
}

// condsForLedger is the conditions as the conductor.waiting event tells
// them: a URL's query (which may hold a token) left out.
func condsForLedger(conds []waitCond) []waitCond {
	out := make([]waitCond, len(conds))
	for i, c := range conds {
		if c.Kind == condURL {
			c.URL = ""
		}
		out[i] = c
	}
	return out
}
