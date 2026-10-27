package delivery

import (
	"context"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
)

// LinksMax is the most projects a session links.
const LinksMax = 50

// Link is one project a session reads, with those of its repositories it reads.
type Link struct {
	ProjectID     string   `json:"projectId"`
	RepositoryIDs []string `json:"repositoryIds"`
}

// LinkRefusal is a set of links that cannot be made. Code is bad_request,
// not_found or hidden_from_chat.
type LinkRefusal struct{ Code, Msg string }

func (e LinkRefusal) Error() string { return e.Msg }

// SetSessionLinks makes the session's linked projects and repositories
// exactly those given: projects of the organisation in tx, and only their
// own repositories.
func SetSessionLinks(ctx context.Context, tx pgx.Tx, org, sessionID string, links []Link) error {
	if len(links) > LinksMax {
		return LinkRefusal{"bad_request", fmt.Sprintf("at most %d projects", LinksMax)}
	}
	var projectIDs, repoIDs []string
	// A session started from chat can never see a project hidden from its
	// provider, whoever links it: a member's new link by hand is refused
	// too. One linked before it was hidden may stay in the set (every read
	// leaves it out).
	fence, err := FenceOf(ctx, tx, sessionID, "")
	if err != nil {
		return err
	}
	for _, l := range links {
		if slices.Contains(projectIDs, l.ProjectID) {
			return LinkRefusal{"bad_request", fmt.Sprintf("project %s is named twice", l.ProjectID)}
		}
		if fence.Hides(l.ProjectID) {
			var linked bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM session_projects WHERE session_id = $1 AND project_id = $2)`,
				sessionID, l.ProjectID).Scan(&linked); err != nil {
				return err
			}
			if !linked {
				return LinkRefusal{"hidden_from_chat", fmt.Sprintf(
					"this session was started from %s, and an admin hid project %s from it: it can't be linked here", fence.Provider, l.ProjectID)}
			}
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM projects WHERE id = $1`, l.ProjectID).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return LinkRefusal{"not_found", fmt.Sprintf("project %s not found", l.ProjectID)}
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM repositories WHERE project_id = $1 AND id = ANY($2)`,
			l.ProjectID, db.NonNil(l.RepositoryIDs)).Scan(&n); err != nil {
			return err
		}
		if n != len(slices.Compact(slices.Sorted(slices.Values(l.RepositoryIDs)))) {
			return LinkRefusal{"not_found", fmt.Sprintf("a repository given is not one of project %s's", l.ProjectID)}
		}
		projectIDs = append(projectIDs, l.ProjectID)
		repoIDs = append(repoIDs, l.RepositoryIDs...)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM session_projects WHERE session_id = $1 AND NOT (project_id = ANY($2))`,
		sessionID, db.NonNil(projectIDs)); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM session_repositories WHERE session_id = $1 AND NOT (repository_id = ANY($2))`,
		sessionID, db.NonNil(repoIDs)); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO session_projects (session_id, project_id, organization_id)
		SELECT $1, unnest($2::text[]), $3 ON CONFLICT DO NOTHING`, sessionID, db.NonNil(projectIDs), org); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO session_repositories (session_id, repository_id, organization_id)
		SELECT $1, unnest($2::text[]), $3 ON CONFLICT DO NOTHING`, sessionID, db.NonNil(repoIDs), org)
	return err
}

// AddSessionProject links one more project, with every repository of it,
// beside what the session links now, through SetSessionLinks, and returns
// the session's whole set as session.linked records it.
func AddSessionProject(ctx context.Context, tx pgx.Tx, org, sessionID, projectID string) ([]Link, error) {
	rows, err := tx.Query(ctx, `SELECT sp.project_id, COALESCE(array_agg(sr.repository_id ORDER BY sr.repository_id)
			FILTER (WHERE sr.repository_id IS NOT NULL), '{}')
		FROM session_projects sp LEFT JOIN repositories r ON r.project_id = sp.project_id
		LEFT JOIN session_repositories sr ON sr.session_id = sp.session_id AND sr.repository_id = r.id
		WHERE sp.session_id = $1 GROUP BY sp.project_id ORDER BY sp.project_id`, sessionID)
	if err != nil {
		return nil, err
	}
	links, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Link, error) {
		var l Link
		err := row.Scan(&l.ProjectID, &l.RepositoryIDs)
		return l, err
	})
	if err != nil {
		return nil, err
	}
	rows, err = tx.Query(ctx, `SELECT id FROM repositories WHERE project_id = $1 ORDER BY id`, projectID)
	if err != nil {
		return nil, err
	}
	repoIDs, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	links = append(links, Link{ProjectID: projectID, RepositoryIDs: db.NonNil(repoIDs)})
	return links, SetSessionLinks(ctx, tx, org, sessionID, links)
}
