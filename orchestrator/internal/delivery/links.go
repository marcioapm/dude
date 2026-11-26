package delivery

import (
	"context"
	"fmt"
	"slices"
	"strings"

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

// LinkSet is a session's linked projects and repositories at one moment,
// as LinkSetOf reads it: the before of TellLinksChanged.
type LinkSet struct {
	Projects []string
	// Repository id to its project id.
	Repos map[string]string
}

// LockLiveBrainstorm locks the session's live brainstorm Run, as
// TellBrainstorm does. A link writer takes it before its write: the syncer
// takes it before it deletes a session_repositories row (a failed clone),
// so a writer that touched those rows first and then locked the Run would
// deadlock with it.
func LockLiveBrainstorm(ctx context.Context, tx pgx.Tx, sessionID string) error {
	_, err := tx.Exec(ctx, `SELECT 1 FROM runs r WHERE r.session_id = $1 AND `+LiveBrainstorm+` FOR NO KEY UPDATE`, sessionID)
	return err
}

// LinkSetOf reads the session's linked set.
func LinkSetOf(ctx context.Context, tx pgx.Tx, sessionID string) (LinkSet, error) {
	rows, err := tx.Query(ctx, `SELECT project_id FROM session_projects WHERE session_id = $1 ORDER BY project_id`, sessionID)
	if err != nil {
		return LinkSet{}, err
	}
	projects, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return LinkSet{}, err
	}
	rows, err = tx.Query(ctx, `SELECT r.id, r.project_id FROM session_repositories sr JOIN repositories r ON r.id = sr.repository_id
		WHERE sr.session_id = $1`, sessionID)
	if err != nil {
		return LinkSet{}, err
	}
	pairs, err := pgx.CollectRows(rows, pgx.RowToStructByPos[struct{ ID, Project string }])
	if err != nil {
		return LinkSet{}, err
	}
	set := LinkSet{Projects: projects, Repos: map[string]string{}}
	for _, p := range pairs {
		set.Repos[p.ID] = p.Project
	}
	return set, nil
}

var linksChangedTmpl = mustTemplate("notices/links-changed")

// TellLinksChanged tells the session's live agent what a link write
// changed since before (LinkSetOf, read in the same transaction under the
// session's lock): each project added, each project removed, and each
// repository removed while its project stays. A repository added is not
// named: the resume that checks it out tells it (notices/linked). Nothing
// of a project hidden from the session's chat is named. With no live
// agent, or nothing to tell, nothing is queued: a fresh agent's briefing
// lists what is linked.
func TellLinksChanged(ctx context.Context, tx pgx.Tx, org, sessionID string, before LinkSet) error {
	after, err := LinkSetOf(ctx, tx, sessionID)
	if err != nil {
		return err
	}
	fence, err := FenceOf(ctx, tx, sessionID, "")
	if err != nil {
		return err
	}
	type project struct {
		Key, Name string
		Repos     bool
	}
	type repo struct{ Key, Name string }
	var data struct {
		Added, Removed []project
		Repos          []repo
	}
	var ids []string
	for _, p := range after.Projects {
		if !slices.Contains(before.Projects, p) && !fence.Hides(p) {
			ids = append(ids, p)
		}
	}
	for _, p := range before.Projects {
		if !slices.Contains(after.Projects, p) && !fence.Hides(p) {
			ids = append(ids, p)
		}
	}
	// A repository is linked only under its linked project, so one in
	// before had its project in before.
	var repoIDs []string
	for r, p := range before.Repos {
		if _, kept := after.Repos[r]; !kept && slices.Contains(after.Projects, p) && !fence.Hides(p) {
			repoIDs = append(repoIDs, r)
		}
	}
	if len(ids) == 0 && len(repoIDs) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, `SELECT id, key_prefix, name FROM projects WHERE id = ANY($1) ORDER BY key_prefix, id`, db.NonNil(ids))
	if err != nil {
		return err
	}
	named, err := pgx.CollectRows(rows, pgx.RowToStructByPos[struct{ ID, Key, Name string }])
	if err != nil {
		return err
	}
	withRepos := map[string]bool{}
	for _, p := range after.Repos {
		withRepos[p] = true
	}
	for _, n := range named {
		if slices.Contains(after.Projects, n.ID) {
			data.Added = append(data.Added, project{n.Key, n.Name, withRepos[n.ID]})
		} else {
			data.Removed = append(data.Removed, project{Key: n.Key, Name: n.Name})
		}
	}
	rows, err = tx.Query(ctx, `SELECT p.key_prefix, r.name FROM repositories r JOIN projects p ON p.id = r.project_id
		WHERE r.id = ANY($1) ORDER BY p.key_prefix, r.name`, db.NonNil(repoIDs))
	if err != nil {
		return err
	}
	if data.Repos, err = pgx.CollectRows(rows, pgx.RowToStructByPos[repo]); err != nil {
		return err
	}
	return TellBrainstorm(ctx, tx, org, sessionID, strings.TrimSpace(render(linksChangedTmpl, data)))
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
	links, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Link])
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
