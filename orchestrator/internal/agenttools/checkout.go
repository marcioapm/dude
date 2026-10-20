package agenttools

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// checkoutsPerRun bounds refresh_checkout: each asks lux for a resume.
const checkoutsPerRun = 3

type refreshCheckoutIn struct {
	Project string `json:"project,omitempty"`
}

type cloneFailureOut struct {
	At time.Time `json:"at"`
	// dude's words; Git is git's own, quoted as external text.
	Reason string `json:"reason"`
	Git    string `json:"git,omitempty"`
}

type checkoutOut struct {
	Name string `json:"name"`
	Path string `json:"path"`
	// checked_out, requested (asked for again by this call), pending
	// (linked, not cloned yet) or not_linked.
	Status      string           `json:"status"`
	LastFailure *cloneFailureOut `json:"lastFailure,omitempty"`
}

type refreshCheckoutOut struct {
	Project      string        `json:"project"`
	External     string        `json:"external,omitempty"`
	Repositories []checkoutOut `json:"repositories"`
	Next         string        `json:"next"`
}

// refreshCheckout asks again for the checkouts of a linked project's
// repositories whose clone failed on this session: each is linked to the
// session again, as a member's link does, and the syncer adds it to the
// agent's lux Run at its next resume (sessionRepoMissing, sessionResume),
// whose git.clone settles it (settleClone) and tells the agent a failure.
// Nothing here clones, and the agent links no repository a member did not.
func refreshCheckout(ctx context.Context, tx pgx.Tx, c Caller, in refreshCheckoutIn) (refreshCheckoutOut, error) {
	p, err := linkedProject(ctx, tx, c, in.Project)
	if err != nil {
		return refreshCheckoutOut{}, err
	}
	// Each repository of the project: linked, held by this Run's lux Run,
	// and its newest failed clone on the session, if any.
	rows, err := tx.Query(ctx, `SELECT repo.id, repo.name,
			EXISTS (SELECT 1 FROM session_repositories sr WHERE sr.session_id = $2 AND sr.repository_id = repo.id),
			lux_name($3 || '-' || repo.name) = ANY (r.lux_repositories),
			f.occurred_at, COALESCE(f.payload->>'reason', ''), COALESCE(f.payload->>'error', '')
		FROM repositories repo JOIN runs r ON r.id = $4
		LEFT JOIN LATERAL (SELECT e.occurred_at, e.payload FROM events e
			WHERE e.session_id = $2 AND e.event_type = 'repository.clone_failed'
			  AND e.payload->>'key' = $3 AND e.payload->>'name' = repo.name
			ORDER BY e.occurred_at DESC LIMIT 1) f ON true
		WHERE repo.project_id = $1 ORDER BY repo.name`, p.ID, c.SessionID, p.Key, c.RunID)
	if err != nil {
		return refreshCheckoutOut{}, err
	}
	type repoRow struct {
		ID, Name       string
		Linked, Held   bool
		FailedAt       *time.Time
		Reason, GitErr string
	}
	repos, err := pgx.CollectRows(rows, pgx.RowToStructByPos[repoRow])
	if err != nil {
		return refreshCheckoutOut{}, err
	}
	w := newWrapper()
	out := refreshCheckoutOut{Project: p.Key, Repositories: []checkoutOut{}}
	var requested []map[string]any
	var names struct{ Requested, Pending, Unlinked []string }
	for _, r := range repos {
		line := checkoutOut{Name: r.Name, Path: delivery.SessionRepoPath(p.Key, r.Name)}
		if r.FailedAt != nil && !r.Held {
			line.LastFailure = &cloneFailureOut{At: *r.FailedAt, Reason: cloneFailureReason(r.Reason)}
			if short := delivery.ShortCloneError(r.GitErr); short != "" {
				line.LastFailure.Git = w.text(short, 1000)
				out.External = checkoutText("external", delivery.ExternalText(w.id, "…"))
			}
		}
		switch {
		case r.Linked && r.Held:
			line.Status = "checked_out"
		case r.Linked:
			line.Status = "pending"
			names.Pending = append(names.Pending, p.Key+"/"+r.Name)
		case r.FailedAt != nil:
			line.Status = "requested"
			names.Requested = append(names.Requested, p.Key+"/"+r.Name)
			requested = append(requested, map[string]any{"id": r.ID, "name": r.Name})
			if _, err := tx.Exec(ctx, `INSERT INTO session_repositories (session_id, repository_id, organization_id)
				VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`, c.SessionID, r.ID, c.Org); err != nil {
				return refreshCheckoutOut{}, err
			}
		default:
			line.Status = "not_linked"
			names.Unlinked = append(names.Unlinked, p.Key+"/"+r.Name)
		}
		out.Repositories = append(out.Repositories, line)
	}
	out.Next = checkoutText("next", struct{ Requested, Pending, Unlinked string }{
		joinNames(names.Requested), joinNames(names.Pending), joinNames(names.Unlinked)})
	if len(requested) > 0 {
		if err := delivery.SessionEvent(ctx, tx, c.run(), delivery.EvCheckoutRequested, "agent", c.RunID,
			map[string]any{"project": p.Key, "repositories": requested}); err != nil {
			return refreshCheckoutOut{}, err
		}
	}
	return out, nil
}

// cloneFailureReason is what dude says of a failed clone: the members'
// line when it is dude's own (a sign-in), else that git refused it, git's
// words following as external text.
func cloneFailureReason(reason string) string {
	if reason != "" && !strings.HasPrefix(reason, "git said:") {
		return reason
	}
	return checkoutText("git-refused", nil)
}

func joinNames(names []string) string { return strings.Join(names, ", ") }

func checkoutText(name string, data any) string { return checkoutTexts[name].Render(data) }

var checkoutTexts = func() map[string]delivery.Text {
	out := map[string]delivery.Text{}
	for _, name := range []string{"external", "next", "git-refused"} {
		out[name] = delivery.MustText("results/refresh-checkout/" + name)
	}
	return out
}()
