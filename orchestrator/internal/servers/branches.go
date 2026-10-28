package servers

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/ids"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
	"github.com/marciomartins/dude/orchestrator/internal/phases"
)

// Branch previews: a preview of a project's branch, with no task. Started
// from the CLI, it serves every project repository at the branch where the
// repository has it (else its default branch), syncs when the branch is
// pushed to (a push delivery, BranchEvent), and ends when the branch is
// deleted in the primary repository (a push with deleted, a delete
// delivery, or the daily check, CheckBranches) or after 30 days unused
// (reapAfter). Only the wakeable path takes them: nothing else serves a
// preview with no task to end it.

// BranchPreview is a branch preview as the API shows it.
type BranchPreview struct {
	ID        string       `json:"id"`
	ProjectID string       `json:"projectId"`
	Branch    string       `json:"branch"`
	Status    string       `json:"status"`
	Servers   []PreviewURL `json:"servers"`
	CreatedAt time.Time    `json:"createdAt"`
	EndedAt   *time.Time   `json:"endedAt"`
	LastUsed  *time.Time   `json:"lastUsedAt"`
	StartedBy *PersonRef   `json:"startedBy"`
	Error     *string      `json:"error"`
	Created   bool         `json:"-"`
}

// PreviewURL is one of a preview's servers and where it is served.
type PreviewURL struct {
	Name string  `json:"name"`
	URL  *string `json:"url"`
}

// branchPreviewSelect reads BranchPreview rows (scanBranchPreview).
const branchPreviewSelect = `SELECT r.id, r.project_id, r.preview_branch, r.status::text, r.created_at, r.ended_at,
	(SELECT max(s.last_woken_at) FROM preview_servers s WHERE s.run_id = r.id),
	(SELECT json_build_object('id', p.id, 'name', p.name) FROM people p WHERE p.id = r.started_by),
	r.error,
	(SELECT COALESCE(json_agg(json_build_object('name', s.name, 'url', s.url) ORDER BY s.name), '[]')
	 FROM preview_servers s WHERE s.run_id = r.id AND s.deleted_at IS NULL)
	FROM runs r`

func scanBranchPreview(row pgx.Row) (BranchPreview, error) {
	var b BranchPreview
	err := row.Scan(&b.ID, &b.ProjectID, &b.Branch, &b.Status, &b.CreatedAt, &b.EndedAt, &b.LastUsed, &b.StartedBy, &b.Error, &b.Servers)
	return b, err
}

// liveBranch (SQL, over runs r): a branch preview not ended.
const liveBranch = `r.preview_branch IS NOT NULL AND ` + livePreview

// ValidBranch: a name git and GitHub take as a branch, as far as dude
// relies on it: no whitespace or control characters, no "..", no leading
// '-' or '/', at most 255 bytes.
func ValidBranch(b string) bool {
	if b == "" || len(b) > 255 || strings.HasPrefix(b, "-") || strings.HasPrefix(b, "/") || strings.HasSuffix(b, "/") ||
		strings.Contains(b, "..") || strings.HasSuffix(b, ".lock") {
		return false
	}
	for _, r := range b {
		if unicode.IsSpace(r) || unicode.IsControl(r) || strings.ContainsRune("~^:?*[\\", r) {
			return false
		}
	}
	return true
}

// primaryRepo is the repository a project's preview servers run in (the
// first by name, as taskRefs orders them): its slug on the forge, "" for
// one the forge does not hold.
func primaryRepoSlug(ctx context.Context, tx pgx.Tx, projectID string) (slug string, found bool, err error) {
	var url string
	err = tx.QueryRow(ctx, `SELECT url FROM repositories WHERE project_id = $1 ORDER BY name LIMIT 1`, projectID).Scan(&url)
	if db.IsNotFound(err) {
		return "", false, nil
	}
	return forge.SlugFromURL(url), err == nil, err
}

// StartBranchPreview starts a preview of a project's branch on behalf of
// actor, or returns the live one of that branch (Created false). Refused:
// a branch the forge does not have in the project's primary repository
// (branch_not_found), a project with no repository, or previews that do
// not wake on request (no preview domain, or no server marked to start in
// previews).
func (s *Service) StartBranchPreview(ctx context.Context, gh *forge.GitHub, org, projectID, branch, actor string, identity ...string) (BranchPreview, error) {
	actorType, personID := ledger.ActorHuman, ""
	if len(identity) == 2 {
		actorType, personID = identity[0], identity[1]
	}
	if !ValidBranch(branch) {
		return BranchPreview{}, refuse(http.StatusBadRequest, "bad_request", "%q is not a branch name", branch)
	}
	var slug string
	existing, err := s.liveBranchPreview(ctx, org, projectID, branch, func(tx pgx.Tx) error {
		var found bool
		var err error
		if slug, found, err = primaryRepoSlug(ctx, tx, projectID); err != nil {
			return err
		}
		if !found {
			return refuse(http.StatusConflict, "no_repository", "project %s has no repository to preview", projectID)
		}
		wakeable, err := s.wakeable(ctx, tx, projectID)
		if err != nil {
			return err
		}
		if !wakeable {
			return refuse(http.StatusConflict, "previews_unavailable",
				"project %s has no server marked to start in previews, or lux serves no previews", projectID)
		}
		return nil
	})
	if err != nil || existing != nil {
		if existing != nil {
			return *existing, nil
		}
		return BranchPreview{}, err
	}
	if slug == "" || gh == nil {
		return BranchPreview{}, refuse(http.StatusConflict, "branch_not_found", "the project's primary repository is not on the forge")
	}
	sha, err := gh.BranchSHA(ctx, slug, branch)
	if err != nil {
		return BranchPreview{}, refuse(http.StatusServiceUnavailable, "forge_unavailable", "asking GitHub about %s: %s", branch, err.Error())
	}
	if sha == "" {
		return BranchPreview{}, refuse(http.StatusUnprocessableEntity, "branch_not_found", "%s has no branch %s", slug, branch)
	}
	var out BranchPreview
	err = s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		id := ids.New(ids.Run)
		// One live preview per project branch (runs_live_branch_preview_idx):
		// a start that lost the race returns the winner's.
		tag, err := tx.Exec(ctx, `INSERT INTO runs (id, organization_id, project_id, preview_branch, preview_branch_checked_at,
				attempt, status, kind, started_by, wakeable)
			VALUES ($1, $2, $3, $4, now(), 1, 'pending', 'preview', COALESCE(NULLIF($6, ''),
				(SELECT k.person_id FROM api_keys k JOIN people p ON p.id = k.person_id
				 WHERE k.id = $5 AND k.organization_id = $2 AND p.organization_id = $2 AND k.revoked_at IS NULL AND p.removed_at IS NULL)), true)
			ON CONFLICT (project_id, preview_branch)
				WHERE preview_branch IS NOT NULL AND status IN ('pending', 'scheduled', 'starting', 'running', 'paused')
			DO NOTHING`, id, org, projectID, branch, actor, personID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() > 0 {
			if _, err := ledger.Append(ctx, tx, ledger.Event{Type: "run.created", OrganizationID: org, ProjectID: projectID,
				RunID: id, ActorType: actorType, ActorID: actor, Source: ledger.SourceOrchestrator,
				Payload: map[string]any{"kind": KindPreview, "wakeable": true, "branch": branch}}); err != nil {
				return err
			}
			if err := phases.ServersChanged(ctx, tx, org, projectID, "", id, map[string]any{"change": "created", "branch": branch}); err != nil {
				return err
			}
		}
		out, err = scanBranchPreview(tx.QueryRow(ctx, branchPreviewSelect+` WHERE r.project_id = $1 AND r.preview_branch = $2 AND `+livePreview,
			projectID, branch))
		out.Created = tag.RowsAffected() > 0
		return err
	})
	if err != nil {
		return BranchPreview{}, err
	}
	s.kick()
	return out, nil
}

// liveBranchPreview is the project's live preview of branch, nil for none;
// check runs first, in the same transaction, when there is none.
func (s *Service) liveBranchPreview(ctx context.Context, org, projectID, branch string, check func(pgx.Tx) error) (*BranchPreview, error) {
	var out *BranchPreview
	err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM projects WHERE id = $1)`, projectID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return refuse(http.StatusNotFound, "not_found", "project %s not found", projectID)
		}
		b, err := scanBranchPreview(tx.QueryRow(ctx, branchPreviewSelect+` WHERE r.project_id = $1 AND r.preview_branch = $2 AND `+livePreview,
			projectID, branch))
		switch {
		case err == nil:
			out = &b
			return nil
		case !db.IsNotFound(err):
			return err
		}
		return check(tx)
	})
	return out, err
}

// BranchPreviews are a project's live branch previews, newest first.
func (s *Service) BranchPreviews(ctx context.Context, org, projectID string) ([]BranchPreview, error) {
	out := []BranchPreview{}
	err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM projects WHERE id = $1)`, projectID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return refuse(http.StatusNotFound, "not_found", "project %s not found", projectID)
		}
		rows, err := tx.Query(ctx, branchPreviewSelect+` WHERE r.project_id = $1 AND `+liveBranch+` ORDER BY r.created_at DESC`, projectID)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (BranchPreview, error) { return scanBranchPreview(row) })
		return err
	})
	return out, err
}

// BranchPreview is one branch preview by id, ended or not.
func (s *Service) BranchPreview(ctx context.Context, org, id string) (BranchPreview, error) {
	var out BranchPreview
	err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) (err error) {
		out, err = scanBranchPreview(tx.QueryRow(ctx, branchPreviewSelect+` WHERE r.id = $1 AND r.preview_branch IS NOT NULL`, id))
		if db.IsNotFound(err) {
			return refuse(http.StatusNotFound, "not_found", "branch preview %s not found", id)
		}
		return err
	})
	return out, err
}

// StopBranchPreview ends a live branch preview for good; the sweep deletes
// its lux servers and cancels its Run. One already ended is answered as it
// is.
func (s *Service) StopBranchPreview(ctx context.Context, org, id, actor string, identity ...string) (BranchPreview, error) {
	actorType := ledger.ActorHuman
	if len(identity) == 2 {
		actorType = identity[0]
	}
	cur, err := s.BranchPreview(ctx, org, id)
	if err != nil {
		return cur, err
	}
	var ended bool
	if err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) (err error) {
		ended, err = endPreviewTx(ctx, tx, org, cur.ProjectID, "", id, "stopped", actorType, actor)
		return err
	}); err != nil {
		return BranchPreview{}, err
	}
	if ended {
		s.kick()
	}
	return s.BranchPreview(ctx, org, id)
}

// BranchEvent applies a forge's word about a branch of a repository (its
// slug, owner/name) to the live branch previews of that branch in every
// project of org holding the repository: a push syncs them (the wakeable
// sweep's sync_wanted_at path); a deletion in the project's primary
// repository ends them. A deletion elsewhere is a push: that repository
// goes back to its default branch at the next sync.
func (s *Service) BranchEvent(ctx context.Context, org, slug, branch string, deleted bool, receivedAt time.Time) error {
	type hit struct {
		ID, ProjectID string
		Primary       bool
	}
	var hits []hit
	if err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT DISTINCT project_id FROM runs r
			WHERE r.preview_branch = $1 AND `+livePreview, branch)
		if err != nil {
			return err
		}
		candidates, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil || len(candidates) == 0 {
			return err
		}
		// Only inspect repositories of projects previewing this branch,
		// preserving SlugFromURL's URL variants and primary ordering.
		rows, err = tx.Query(ctx, `SELECT project_id, url FROM repositories
			WHERE project_id = ANY($1) ORDER BY project_id, name`, candidates)
		if err != nil {
			return err
		}
		type repo struct{ ProjectID, URL string }
		repos, err := pgx.CollectRows(rows, pgx.RowToStructByPos[repo])
		if err != nil {
			return err
		}
		projects := []string{}
		primary := map[string]bool{}
		last := ""
		for _, rp := range repos {
			first := rp.ProjectID != last
			last = rp.ProjectID
			if strings.EqualFold(forge.SlugFromURL(rp.URL), slug) {
				projects = append(projects, rp.ProjectID)
				primary[rp.ProjectID] = primary[rp.ProjectID] || first
			}
		}
		if len(projects) == 0 {
			return nil
		}
		rows, err = tx.Query(ctx, `SELECT r.id, r.project_id FROM runs r
			WHERE r.project_id = ANY($1) AND r.preview_branch = $2 AND `+livePreview+`
			AND (NOT $3 OR r.created_at <= $4)`, projects, branch, deleted, receivedAt)
		if err != nil {
			return err
		}
		for rows.Next() {
			var h hit
			if err := rows.Scan(&h.ID, &h.ProjectID); err != nil {
				rows.Close()
				return err
			}
			h.Primary = primary[h.ProjectID]
			hits = append(hits, h)
		}
		return rows.Err()
	}); err != nil {
		return err
	}
	acted := false
	for _, h := range hits {
		gone := false
		if deleted && h.Primary {
			// Receipt time fences backlog, not deletion time. A delayed receipt
			// or duplicate can describe a branch that has already been recreated.
			var err error
			gone, err = (&Previews{Service: s, Forges: forge.Resolver{DB: s.DB}}).branchGone(ctx, org, h.ProjectID, branch)
			if err != nil {
				return err
			}
		}
		if err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
			if gone {
				ended, err := endPreviewTx(ctx, tx, org, h.ProjectID, "", h.ID, "branch deleted", ledger.ActorSystem, h.ID)
				if err != nil {
					return err
				}
				acted = acted || ended
				return nil
			}
			tag, err := tx.Exec(ctx, `UPDATE runs r SET sync_wanted_at = now() WHERE r.id = $1 AND r.wakeable AND `+livePreview, h.ID)
			if err != nil {
				return err
			}
			acted = acted || tag.RowsAffected() > 0
			return nil
		}); err != nil {
			return err
		}
	}
	if acted {
		s.kick()
	}
	return nil
}

// branchCheckEvery: how often each live branch preview's branch is looked
// for on the forge, the backstop for a deletion whose delivery was lost.
const branchCheckEvery = 24 * time.Hour

// CheckBranches ends each live branch preview whose branch the forge
// definitely no longer has in the project's primary repository
// (forge.BranchGone), once per branchCheckEvery each. Any error, or no
// forge to ask, leaves the preview as it is, to be asked again in an hour.
// The returned processed count is scheduling progress, not the ended count:
// healthy full pages must drain without waiting for unrelated loop kicks.
func (p *Previews) CheckBranches(ctx context.Context) (int, error) {
	type due struct{ ID, Org, ProjectID, Branch string }
	var list []due
	if err := p.DB.InSystem(ctx, "previews", func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT r.id, r.organization_id, r.project_id, r.preview_branch FROM runs r
			WHERE `+liveBranch+` AND (r.preview_branch_checked_at IS NULL
				OR r.preview_branch_checked_at < now() - make_interval(secs => $1))
			ORDER BY r.preview_branch_checked_at NULLS FIRST LIMIT 50`, branchCheckEvery.Seconds())
		if err != nil {
			return err
		}
		list, err = pgx.CollectRows(rows, pgx.RowToStructByPos[due])
		return err
	}); err != nil {
		return 0, err
	}
	ended := 0
	for _, d := range list {
		gone, err := p.branchGone(ctx, d.Org, d.ProjectID, d.Branch)
		if err != nil {
			p.Log.Warn("checking a branch preview's branch failed; it is kept", "run", d.ID, "branch", d.Branch, "error", err)
		}
		if err := p.DB.InOrg(ctx, d.Org, func(tx pgx.Tx) error {
			if gone {
				ok, err := endPreviewTx(ctx, tx, d.Org, d.ProjectID, "", d.ID, "branch deleted", ledger.ActorSystem, d.ID)
				if ok {
					ended++
				}
				return err
			}
			// Checked now; one whose check failed is asked again in an hour.
			back := time.Duration(0)
			if err != nil {
				back = branchCheckEvery - time.Hour
			}
			_, uerr := tx.Exec(ctx, `UPDATE runs SET preview_branch_checked_at = now() - make_interval(secs => $2) WHERE id = $1`,
				d.ID, back.Seconds())
			return uerr
		}); err != nil {
			return 0, err
		}
	}
	if ended > 0 {
		p.kick()
	}
	return len(list), nil
}

// branchGone: the forge definitely has no branch in the project's primary
// repository. No forge, or a repository it does not hold, is not gone.
func (p *Previews) branchGone(ctx context.Context, org, projectID, branch string) (bool, error) {
	var slug string
	if err := p.DB.InOrg(ctx, org, func(tx pgx.Tx) (err error) {
		slug, _, err = primaryRepoSlug(ctx, tx, projectID)
		return err
	}); err != nil {
		return false, err
	}
	gh, err := p.forge(ctx, org)
	if err != nil || gh == nil || slug == "" {
		if err == nil {
			err = errors.New("no forge holds the primary repository")
		}
		return false, err
	}
	return gh.BranchGone(ctx, slug, branch)
}
