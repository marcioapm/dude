package prs

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

// Registration is which of an organization's repositories get dude's
// webhook, delivering to URL.
type Registration struct {
	URL string
	// One repository; "" for all of them.
	RepositoryID string
	// Skip a repository whose hook is registered to URL with no error:
	// connecting again asks GitHub only about the rest.
	OnlyMissing bool
	// Leave alone a repository GitHub refused within this long (0: none). A
	// failure with no attempt time (recorded before 080) is always due.
	Backoff time.Duration
	// At most this many repositories asked of GitHub (0: no bound).
	Limit int
}

// WebhookResult is one repository's registration.
type WebhookResult struct {
	RepositoryID string `json:"repositoryId"`
	Name         string `json:"name"`
	Slug         string `json:"slug"`
	HookID       string `json:"hookId,omitempty"`
	Error        string `json:"error,omitempty"`
}

// ErrNoWebhookSecret: the organization has no secret to sign deliveries
// with, so there is nothing to register yet.
var ErrNoWebhookSecret = errors.New("no webhook secret: connect GitHub first")

// registering serialises registrations of one organization in this
// process: a connect's background registration and the repair each read
// GitHub's hooks before creating one, and two at once would both create.
var registering sync.Map // organization id → *sync.Mutex

// RegisterWebhooks ensures dude's webhook on each repository reg selects,
// one at a time, recording each outcome on the repository. A rate limit
// stops it, reported as limited: the repository it hit is recorded as
// nothing, and those after it are left for the next pass. Other failures
// GitHub may not repeat, and refusals, are recorded on the repository and
// the rest go on.
func RegisterWebhooks(ctx context.Context, d *db.DB, gh *forge.GitHub, org string, reg Registration) ([]WebhookResult, bool, error) {
	mu, _ := registering.LoadOrStore(org, &sync.Mutex{})
	mu.(*sync.Mutex).Lock()
	defer mu.(*sync.Mutex).Unlock()
	type repo struct{ ID, Name, URL string }
	var secret string
	var repos []repo
	if err := d.InOrg(ctx, org, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT COALESCE(webhook_secret, '') FROM forge_credentials WHERE forge = 'github'`).
			Scan(&secret); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT id, name, url FROM repositories
			WHERE ($1 = '' OR id = $1)
			  AND NOT ($2 AND webhook_url = $3 AND webhook_id IS NOT NULL AND webhook_registered_at IS NOT NULL
			           AND webhook_error IS NULL)
			  AND (webhook_error IS NULL OR webhook_attempted_at IS NULL
			       OR webhook_attempted_at <= now() - $4::interval)
			ORDER BY webhook_attempted_at NULLS FIRST, name`, reg.RepositoryID, reg.OnlyMissing, reg.URL, reg.Backoff.String())
		if err != nil {
			return err
		}
		repos, err = pgx.CollectRows(rows, pgx.RowToStructByPos[repo])
		return err
	}); err != nil {
		return nil, false, err
	}
	if secret == "" {
		return nil, false, ErrNoWebhookSecret
	}
	results := []WebhookResult{}
	for _, rp := range repos {
		slug := forge.SlugFromURL(rp.URL)
		if slug == "" {
			continue // a local repository: nothing on GitHub to hook
		}
		if reg.Limit > 0 && len(results) == reg.Limit {
			break
		}
		res := WebhookResult{RepositoryID: rp.ID, Name: rp.Name, Slug: slug}
		id, err := gh.EnsureWebhook(ctx, slug, reg.URL, secret)
		if forge.RateLimited(err) {
			res.Error = err.Error()
			return append(results, res), true, nil
		}
		if err != nil {
			if !forge.Transient(err) && !forge.Refused(err) {
				return results, false, err
			}
			res.Error = err.Error()
		}
		res.HookID = id
		if err := d.InOrg(ctx, org, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE repositories SET
				webhook_id = COALESCE(NULLIF($2, ''), webhook_id),
				webhook_registered_at = CASE WHEN $3 = '' THEN now() ELSE webhook_registered_at END,
				webhook_url = CASE WHEN $3 = '' THEN $4 ELSE webhook_url END,
				webhook_error = NULLIF($3, ''), webhook_attempted_at = now() WHERE id = $1`, rp.ID, id, res.Error, reg.URL)
			return err
		}); err != nil {
			return results, false, err
		}
		results = append(results, res)
	}
	return results, false, nil
}

// RepairPerPass is how many repositories the repair asks GitHub about in
// one pass, across organizations: a few, so a backlog drains over passes
// without spending the rate limit at once.
const RepairPerPass = 5

// repairBackoff: how long a repository GitHub refused waits before the
// repair asks again (a token without the scope, a repository gone).
const repairBackoff = time.Hour

// RepairWebhooks registers dude's webhook on repositories that have none
// healthy, in organizations that asked for webhooks (forge_credentials
// .webhook_url), RepairPerPass at a time: what a registration cut short by
// a rate limit, a restart or a timeout left undone. Stops at a rate limit.
func (s *Syncer) RepairWebhooks(ctx context.Context) error {
	type asked struct{ Org, URL string }
	var orgs []asked
	if err := s.DB.InSystem(ctx, "webhook-repair", func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT c.organization_id, c.webhook_url FROM forge_credentials c
			WHERE c.forge = 'github' AND c.webhook_url <> ''
			  AND EXISTS (SELECT 1 FROM repositories r WHERE r.organization_id = c.organization_id
			    AND NOT (r.webhook_url IS NOT DISTINCT FROM c.webhook_url AND r.webhook_id IS NOT NULL
			             AND r.webhook_registered_at IS NOT NULL AND r.webhook_error IS NULL)
			    AND (r.webhook_error IS NULL OR r.webhook_attempted_at IS NULL
			         OR r.webhook_attempted_at <= now() - $1::interval))
			ORDER BY c.organization_id`, repairBackoff.String())
		if err != nil {
			return err
		}
		orgs, err = pgx.CollectRows(rows, pgx.RowToStructByPos[asked])
		return err
	}); err != nil {
		return err
	}
	done := 0
	for _, o := range orgs {
		if done >= RepairPerPass {
			break
		}
		gh, err := s.Forges.For(ctx, o.Org)
		if err != nil || gh == nil {
			continue
		}
		results, limited, err := RegisterWebhooks(ctx, s.DB, gh, o.Org, Registration{URL: o.URL, OnlyMissing: true,
			Backoff: repairBackoff, Limit: RepairPerPass - done})
		done += len(results)
		if err != nil && !errors.Is(err, ErrNoWebhookSecret) {
			s.Log.Warn("repairing webhooks failed", "organization", o.Org, "error", err)
		}
		if limited {
			s.Log.Info("GitHub's rate limit stopped the webhook repair; the next pass goes on", "organization", o.Org)
			break
		}
	}
	return nil
}
