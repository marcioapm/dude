package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/ids"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// Brainstorm sessions (delivery/session.go). Every route acts for the
// person the backend names, and checks they are an accepted member before
// anything else (member): a non-member, an invitee who has not accepted
// and an organisation admin who is not in the session all get the same
// 404. Only the owner shares, changes roles, removes and hands over;
// readers write nothing.
func (s *Server) sessionRoutes(mux *http.ServeMux) {
	mux.Handle("POST /internal/sessions", s.auth(s.createSession))
	mux.Handle("GET /internal/sessions", s.auth(s.listSessions))
	mux.Handle("GET /internal/sessions/{id}", s.auth(s.getSession))
	mux.Handle("POST /internal/sessions/{id}/chat", s.auth(s.sessionChat))
	mux.Handle("POST /internal/sessions/{id}/link", s.auth(s.linkSession))
	mux.Handle("POST /internal/sessions/{id}/people", s.auth(s.inviteToSession))
	mux.Handle("POST /internal/sessions/{id}/people/{person}/role", s.auth(s.changeSessionRole))
	mux.Handle("POST /internal/sessions/{id}/people/{person}/remove", s.auth(s.removeFromSession))
	mux.Handle("POST /internal/sessions/{id}/owner", s.auth(s.handOverSession))
	mux.Handle("POST /internal/sessions/{id}/accept", s.auth(s.acceptSession))
	mux.Handle("POST /internal/sessions/{id}/decline", s.auth(s.declineSession))
	mux.Handle("POST /internal/sessions/{id}/file", s.auth(s.fileProposal))
}

// sessionTitleMax bounds a session's title, as the schema does.
const sessionTitleMax = 200

var errNoPerson = fail(http.StatusForbidden, "no_person", "sessions are a person's: sign in as one")

// member is the caller's role in the session, refusing anyone else with a
// 404 that does not say whether the session exists.
func member(ctx context.Context, tx pgx.Tx, sessionID, person string) (string, error) {
	role, err := delivery.SessionRole(ctx, tx, sessionID, person)
	if errors.Is(err, delivery.ErrNotMember) {
		return "", fail(http.StatusNotFound, "not_found", "session %s not found", sessionID)
	}
	return role, err
}

// owner refuses anyone but the session's owner.
func owner(ctx context.Context, tx pgx.Tx, sessionID, person, verb string) error {
	role, err := member(ctx, tx, sessionID, person)
	if err != nil {
		return err
	}
	if role != delivery.SessionOwner {
		return fail(http.StatusForbidden, "not_owner", "only the session's owner can %s", verb)
	}
	return nil
}

// writer refuses a reader: only the owner and members who can chat write
// to the agent or file its work.
func writer(ctx context.Context, tx pgx.Tx, sessionID, person, verb string) (string, error) {
	role, err := member(ctx, tx, sessionID, person)
	if err != nil {
		return "", err
	}
	if role == delivery.SessionRead {
		return "", fail(http.StatusForbidden, "read_only", "you can read this session; only its owner and members who can chat %s", verb)
	}
	return role, nil
}

func sessionPrincipal(r *http.Request) (principal, error) {
	p := principalOf(r)
	if p.Person == "" {
		return p, errNoPerson
	}
	return p, nil
}

func (p principal) writer() delivery.Writer {
	w := delivery.Writer{ActorType: p.ActorType, ActorID: p.Actor, Person: p.Person}
	if w.ActorType == "" {
		w.ActorType = ledger.ActorHuman
	}
	return w
}

type linkInput struct {
	ProjectID     string   `json:"projectId"`
	RepositoryIDs []string `json:"repositoryIds"`
}

// createSession starts a session owned by the caller: private until they
// share it.
func (s *Server) createSession(w http.ResponseWriter, r *http.Request, org string) error {
	p, err := sessionPrincipal(r)
	if err != nil {
		return err
	}
	var body struct {
		Title    string      `json:"title"`
		Projects []linkInput `json:"projects"`
	}
	if err := read(r, &body); err != nil {
		return err
	}
	title := strings.TrimSpace(body.Title)
	if title == "" || len([]rune(title)) > sessionTitleMax {
		return fail(http.StatusBadRequest, "bad_request", "a title of 1 to %d characters is required", sessionTitleMax)
	}
	id := ids.New(ids.Session)
	err = s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
		if _, err := tx.Exec(r.Context(), `INSERT INTO sessions (id, organization_id, title, created_by) VALUES ($1, $2, $3, $4)`,
			id, org, title, p.Person); err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `INSERT INTO session_people (session_id, person_id, organization_id, role, invited_by, accepted_at)
			VALUES ($1, $2, $3, 'owner', $2, now())`, id, p.Person, org); err != nil {
			return err
		}
		if err := setLinks(r.Context(), tx, org, id, body.Projects); err != nil {
			return err
		}
		return delivery.SessionEvent(r.Context(), tx, delivery.SessionRef(org, id), delivery.EvSessionCreated, p.ActorType, p.Actor,
			map[string]any{"title": title})
	})
	if err != nil {
		return err
	}
	write(w, http.StatusCreated, map[string]any{"id": id, "title": title})
	return nil
}

// setLinks makes the session's linked projects and repositories exactly
// those given: projects the caller's organisation has, and only their own
// repositories.
func setLinks(ctx context.Context, tx pgx.Tx, org, sessionID string, projects []linkInput) error {
	if len(projects) > 50 {
		return fail(http.StatusBadRequest, "bad_request", "at most 50 projects")
	}
	var projectIDs, repoIDs []string
	for _, l := range projects {
		if slices.Contains(projectIDs, l.ProjectID) {
			return fail(http.StatusBadRequest, "bad_request", "project %s is named twice", l.ProjectID)
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM projects WHERE id = $1`, l.ProjectID).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return fail(http.StatusNotFound, "not_found", "project %s not found", l.ProjectID)
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM repositories WHERE project_id = $1 AND id = ANY($2)`,
			l.ProjectID, l.RepositoryIDs).Scan(&n); err != nil {
			return err
		}
		if n != len(slices.Compact(slices.Sorted(slices.Values(l.RepositoryIDs)))) {
			return fail(http.StatusNotFound, "not_found", "a repository given is not one of project %s's", l.ProjectID)
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
	_, err := tx.Exec(ctx, `INSERT INTO session_repositories (session_id, repository_id, organization_id)
		SELECT $1, unnest($2::text[]), $3 ON CONFLICT DO NOTHING`, sessionID, db.NonNil(repoIDs), org)
	return err
}

// linkSession sets what the session reads. Repositories added reach a
// live agent through the resume path (the syncer parks it and resumes it
// with them); one unlinked is refused by its tools at once, its checkout
// left until the next fresh Run. Owner only.
func (s *Server) linkSession(w http.ResponseWriter, r *http.Request, org string) error {
	p, err := sessionPrincipal(r)
	if err != nil {
		return err
	}
	var body struct {
		Projects []linkInput `json:"projects"`
	}
	if err := read(r, &body); err != nil {
		return err
	}
	id := r.PathValue("id")
	err = s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
		if err := owner(r.Context(), tx, id, p.Person, "link projects"); err != nil {
			return err
		}
		if err := delivery.LockSession(r.Context(), tx, id); err != nil {
			return err
		}
		if err := setLinks(r.Context(), tx, org, id, body.Projects); err != nil {
			return err
		}
		return delivery.SessionEvent(r.Context(), tx, delivery.SessionRef(org, id), delivery.EvSessionLinked, p.ActorType, p.Actor,
			map[string]any{"projects": body.Projects})
	})
	if err != nil {
		return err
	}
	s.kick()
	write(w, http.StatusOK, map[string]any{"id": id})
	return nil
}

// listSessions is the caller's sessions — those they accepted — and their
// invitations, each only what an inbox line shows: never a word of the
// conversation.
func (s *Server) listSessions(w http.ResponseWriter, r *http.Request, org string) error {
	p, err := sessionPrincipal(r)
	if err != nil {
		return err
	}
	var sessions, invitations, questions json.RawMessage
	err = s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
		if err := tx.QueryRow(r.Context(), `SELECT COALESCE(json_agg(x ORDER BY x."lastActivityAt" DESC), '[]') FROM (
			SELECT s.id, s.title, me.role, s.created_at AS "createdAt",
				(SELECT person_ref(o) FROM session_people so JOIN people o ON o.id = so.person_id
				 WHERE so.session_id = s.id AND so.role = 'owner') AS owner,
				(SELECT count(*) FROM session_people sp WHERE sp.session_id = s.id AND sp.accepted_at IS NOT NULL) > 1 AS shared,
				`+sessionProjectsJSON+` AS projects,
				(SELECT r.status::text FROM runs r WHERE r.session_id = s.id ORDER BY r.created_at DESC LIMIT 1) AS "runStatus",
				(SELECT r.dude_pause FROM runs r WHERE r.session_id = s.id ORDER BY r.created_at DESC LIMIT 1) AS "dudePause",
				(SELECT count(*) FROM session_filings f WHERE f.session_id = s.id) AS filed,
				COALESCE((SELECT max(e.occurred_at) FROM events e WHERE e.session_id = s.id), s.created_at) AS "lastActivityAt"
			FROM sessions s JOIN session_people me ON me.session_id = s.id AND me.person_id = $1 AND me.accepted_at IS NOT NULL) x`,
			p.Person).Scan(&sessions); err != nil {
			return err
		}
		if err := tx.QueryRow(r.Context(), `SELECT COALESCE(json_agg(x ORDER BY x."invitedAt" DESC), '[]') FROM (
			SELECT s.id, s.title, me.role, me.becomes_owner AS "becomesOwner", me.invited_at AS "invitedAt",
				(SELECT person_ref(i) FROM people i WHERE i.id = me.invited_by) AS "invitedBy",
				(SELECT COALESCE(json_agg(person_ref(o) ORDER BY so.role <> 'owner', so.invited_at), '[]')
				 FROM session_people so JOIN people o ON o.id = so.person_id
				 WHERE so.session_id = s.id AND so.accepted_at IS NOT NULL) AS people,
				`+sessionProjectsJSON+` AS projects,
				(SELECT count(*) FROM events e WHERE e.session_id = s.id AND e.event_type IN ('chat.message', 'agent.message')) AS messages
			FROM sessions s JOIN session_people me ON me.session_id = s.id AND me.person_id = $1 AND me.accepted_at IS NULL) x`,
			p.Person).Scan(&invitations); err != nil {
			return err
		}
		return tx.QueryRow(r.Context(), `SELECT COALESCE(json_agg(x ORDER BY x."askedAt"), '[]') FROM (
			SELECT q.id, q.prompt, q.options, q.asked_at AS "askedAt", s.id AS "sessionId", s.title
			FROM questions q JOIN runs r ON r.id = q.run_id JOIN sessions s ON s.id = r.session_id
			WHERE q.status = 'open' AND session_role(s.id, $1) IN ('owner', 'chat')
			  AND (q.to_person = $1 OR (q.to_person IS NULL AND session_role(s.id, $1) = 'owner'))) x`,
			p.Person).Scan(&questions)
	})
	if err != nil {
		return err
	}
	write(w, http.StatusOK, map[string]any{"sessions": sessions, "invitations": invitations, "questions": questions})
	return nil
}

// sessionProjectsJSON (SQL, over sessions s): its linked projects, each
// with its linked repositories.
const sessionProjectsJSON = `(SELECT COALESCE(json_agg(json_build_object('id', p.id, 'key', p.key_prefix, 'name', p.name,
		'repositories', (SELECT COALESCE(json_agg(json_build_object('id', repo.id, 'name', repo.name, 'defaultBranch', repo.default_branch)
			ORDER BY repo.name), '[]') FROM session_repositories sr JOIN repositories repo ON repo.id = sr.repository_id
			WHERE sr.session_id = s.id AND repo.project_id = p.id)) ORDER BY p.key_prefix), '[]')
	FROM session_projects sp JOIN projects p ON p.id = sp.project_id WHERE sp.session_id = s.id)`

// getSession is one session as its members see it: who is in it (and who
// has it open), what it reads, its agent, its proposal card, and the
// question waiting for the caller, if any.
func (s *Server) getSession(w http.ResponseWriter, r *http.Request, org string) error {
	p, err := sessionPrincipal(r)
	if err != nil {
		return err
	}
	id := r.PathValue("id")
	var out map[string]any
	err = s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
		role, err := member(r.Context(), tx, id, p.Person)
		if err != nil {
			return err
		}
		var session json.RawMessage
		if err := tx.QueryRow(r.Context(), `SELECT json_build_object('id', s.id, 'title', s.title, 'createdAt', s.created_at,
				'people', (SELECT COALESCE(json_agg(json_build_object('person', person_ref(pp), 'role', sp.role,
					'accepted', sp.accepted_at IS NOT NULL, 'becomesOwner', sp.becomes_owner,
					'open', COALESCE(sp.open_at > now() - interval '90 seconds', false))
					ORDER BY sp.role <> 'owner', sp.invited_at), '[]')
					FROM session_people sp JOIN people pp ON pp.id = sp.person_id WHERE sp.session_id = s.id),
				'projects', `+sessionProjectsJSON+`,
				'run', (SELECT json_build_object('id', r.id, 'status', r.status, 'dudePause', r.dude_pause, 'model', r.model,
					'modelTier', r.model_tier, 'machine', r.machine->>'name', 'waiting', r.waiting_since IS NOT NULL)
					FROM runs r WHERE r.session_id = s.id ORDER BY r.created_at DESC LIMIT 1),
				'runs', (SELECT COALESCE(json_agg(r.id ORDER BY r.created_at), '[]') FROM runs r WHERE r.session_id = s.id),
				'costUsd', (SELECT COALESCE(sum(run_model_usd(r)), 0)::float8 FROM runs r WHERE r.session_id = s.id),
				'messages', (SELECT count(*) FROM events e WHERE e.session_id = s.id AND e.event_type IN ('chat.message', 'agent.message')))
			FROM sessions s WHERE s.id = $1`, id).Scan(&session); err != nil {
			return err
		}
		proposals, err := proposalsFor(r.Context(), tx, id, p.Person, role)
		if err != nil {
			return err
		}
		var question json.RawMessage
		if err := tx.QueryRow(r.Context(), `SELECT json_build_object('id', q.id, 'prompt', q.prompt, 'options', q.options,
				'askedAt', q.asked_at, 'to', (SELECT person_ref(t) FROM people t WHERE t.id = q.to_person),
				'yours', $2 <> 'read' AND (q.to_person = $3 OR q.to_person IS NULL))
			FROM questions q JOIN runs r ON r.id = q.run_id WHERE r.session_id = $1 AND q.status = 'open'
			ORDER BY q.asked_at DESC LIMIT 1`, id, role, p.Person).Scan(&question); err != nil && !db.IsNotFound(err) {
			return err
		}
		out = map[string]any{"session": session, "you": map[string]any{"id": p.Person, "role": role},
			"proposals": proposals, "question": question}
		return nil
	})
	if err != nil {
		return err
	}
	write(w, http.StatusOK, out)
	return nil
}

// proposalCard is a proposal as the card shows it to one member: each
// item, whether they can file it (and if not, who can), and who filed it.
type proposalCard struct {
	ID        string                  `json:"id"`
	RunID     string                  `json:"runId"`
	CreatedAt any                     `json:"createdAt"`
	Items     []delivery.ProposalItem `json:"items"`
	Status    []map[string]any        `json:"status"`
	raw       json.RawMessage
}

func proposalsFor(ctx context.Context, tx pgx.Tx, sessionID, person, role string) ([]proposalCard, error) {
	rows, err := tx.Query(ctx, `SELECT id, COALESCE(run_id, ''), created_at, items FROM session_proposals
		WHERE session_id = $1 ORDER BY created_at`, sessionID)
	if err != nil {
		return nil, err
	}
	var cards []proposalCard
	for rows.Next() {
		var c proposalCard
		if err := rows.Scan(&c.ID, &c.RunID, &c.CreatedAt, &c.raw); err != nil {
			rows.Close()
			return nil, err
		}
		_ = json.Unmarshal(c.raw, &c.Items)
		cards = append(cards, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	projects, err := linkedProjectIDs(ctx, tx, sessionID)
	if err != nil {
		return nil, err
	}
	for i := range cards {
		c := &cards[i]
		c.Status = make([]map[string]any, len(c.Items))
		for n, item := range c.Items {
			st := map[string]any{}
			var filedBy, key string
			err := tx.QueryRow(ctx, `SELECT COALESCE((SELECT name FROM people WHERE id = f.filed_by), ''), f.key
				FROM session_filings f WHERE f.proposal_id = $1 AND f.item = $2`, c.ID, n).Scan(&filedBy, &key)
			switch {
			case err == nil:
				st["filed"], st["filedBy"], st["key"] = true, filedBy, key
			case !db.IsNotFound(err):
				return nil, err
			default:
				why, err := cannotFile(ctx, tx, projects, item, person, role)
				if err != nil {
					return nil, err
				}
				st["canFile"] = why == ""
				if why != "" {
					st["why"] = why
				}
			}
			c.Status[n] = st
		}
	}
	return cards, nil
}

func linkedProjectIDs(ctx context.Context, tx pgx.Tx, sessionID string) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT project_id FROM session_projects WHERE session_id = $1`, sessionID)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	return db.NonNil(out), err
}

// cannotFile says why person may not file item, "" when they may: a
// reader files nothing; an edit is its task's owner's alone, and only
// before it has started (the control plane's rule for a task's text); a
// task or epic needs its project still linked.
func cannotFile(ctx context.Context, tx pgx.Tx, linked []string, item delivery.ProposalItem, person, role string) (string, error) {
	if role == delivery.SessionRead {
		return "readers can't file", nil
	}
	switch item.Kind {
	case "epic", "task":
		var id string
		err := tx.QueryRow(ctx, `SELECT id FROM projects WHERE upper(key_prefix) = upper($1) AND id = ANY($2) LIMIT 1`,
			item.Project, linked).Scan(&id)
		if db.IsNotFound(err) {
			return fmt.Sprintf("project %s is not linked to this session", item.Project), nil
		}
		return "", err
	case "edit", "comment":
		taskID, _, err := delivery.TaskByKey(ctx, tx, linked, item.Task)
		if err != nil {
			return "", err
		}
		if taskID == "" {
			return fmt.Sprintf("%s is not a task of a linked project", item.Task), nil
		}
		if item.Kind == "comment" {
			return "", nil
		}
		ownerID, ownerName, err := delivery.TaskOwnerNamed(ctx, tx, taskID)
		if err != nil {
			return "", err
		}
		if ownerID != "" && ownerID != person {
			return fmt.Sprintf("only %s can file this: it's their task", ownerName), nil
		}
		started, status, err := delivery.TaskStarted(ctx, tx, taskID)
		if err != nil {
			return "", err
		}
		if started {
			return fmt.Sprintf("%s has started (%s): its text can no longer change", item.Task, status), nil
		}
		return "", nil
	}
	return "unknown item", nil
}

// fileProposal files the items a member kept, as that member. Items they
// may not file are refused, each saying why, and stay on the card for
// whoever can; the rest are filed in one transaction.
func (s *Server) fileProposal(w http.ResponseWriter, r *http.Request, org string) error {
	p, err := sessionPrincipal(r)
	if err != nil {
		return err
	}
	var body struct {
		ProposalID string `json:"proposalId"`
		Items      []int  `json:"items"`
	}
	if err := read(r, &body); err != nil {
		return err
	}
	if len(body.Items) == 0 {
		return fail(http.StatusBadRequest, "bad_request", "name the items to file")
	}
	id := r.PathValue("id")
	f := delivery.Filer{ActorType: p.ActorType, ActorID: p.Actor, Person: p.Person}
	if f.ActorType == "" {
		f.ActorType = ledger.ActorHuman
	}
	var results []map[string]any
	err = s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
		role, err := writer(r.Context(), tx, id, p.Person, "file work")
		if err != nil {
			return err
		}
		if err := delivery.LockSession(r.Context(), tx, id); err != nil {
			return err
		}
		var raw json.RawMessage
		if err := tx.QueryRow(r.Context(), `SELECT items FROM session_proposals WHERE id = $1 AND session_id = $2`,
			body.ProposalID, id).Scan(&raw); err != nil {
			if db.IsNotFound(err) {
				return fail(http.StatusNotFound, "not_found", "proposal %s not found", body.ProposalID)
			}
			return err
		}
		var items []delivery.ProposalItem
		_ = json.Unmarshal(raw, &items)
		linked, err := linkedProjectIDs(r.Context(), tx, id)
		if err != nil {
			return err
		}
		// New epics first, so the tasks under them find them.
		order := slices.Clone(body.Items)
		slices.SortStableFunc(order, func(a, b int) int {
			ea := a >= 0 && a < len(items) && items[a].Kind == "epic"
			eb := b >= 0 && b < len(items) && items[b].Kind == "epic"
			switch {
			case ea && !eb:
				return -1
			case eb && !ea:
				return 1
			}
			return 0
		})
		var filed []map[string]any
		for _, n := range order {
			if n < 0 || n >= len(items) {
				results = append(results, map[string]any{"item": n, "status": "refused", "why": "no such item"})
				continue
			}
			item := items[n]
			var done int
			if err := tx.QueryRow(r.Context(), `SELECT count(*) FROM session_filings WHERE proposal_id = $1 AND item = $2`,
				body.ProposalID, n).Scan(&done); err != nil {
				return err
			}
			if done > 0 {
				results = append(results, map[string]any{"item": n, "status": "refused", "why": "already filed"})
				continue
			}
			why, err := cannotFile(r.Context(), tx, linked, item, p.Person, role)
			if err != nil {
				return err
			}
			if why != "" {
				results = append(results, map[string]any{"item": n, "status": "refused", "why": why})
				continue
			}
			key, taskID, epicID, err := fileItem(r.Context(), tx, org, id, body.ProposalID, linked, items, item, f)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(r.Context(), `INSERT INTO session_filings (proposal_id, item, organization_id, session_id, filed_by, key, task_id, epic_id)
				VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7, ''), NULLIF($8, ''))`,
				body.ProposalID, n, org, id, p.Person, key, taskID, epicID); err != nil {
				return err
			}
			entry := map[string]any{"item": n, "status": "filed", "key": key, "kind": item.Kind}
			results = append(results, entry)
			filed = append(filed, entry)
		}
		if len(filed) == 0 {
			return nil
		}
		name, err := delivery.PersonName(r.Context(), tx, p.Person)
		if err != nil {
			return err
		}
		return delivery.SessionEvent(r.Context(), tx, delivery.SessionRef(org, id), delivery.EvSessionFiled, p.ActorType, p.Actor,
			map[string]any{"proposalId": body.ProposalID, "by": name, "filed": filed})
	})
	if err != nil {
		return err
	}
	write(w, http.StatusOK, map[string]any{"results": results})
	return nil
}

// fileItem files one item as f; returns what it made: a task's key (or an
// epic's title), and the task or epic.
func fileItem(ctx context.Context, tx pgx.Tx, org, sessionID, proposalID string, linked []string, items []delivery.ProposalItem,
	item delivery.ProposalItem, f delivery.Filer) (key, taskID, epicID string, err error) {
	projectOf := func(k string) (string, error) {
		var id string
		err := tx.QueryRow(ctx, `SELECT id FROM projects WHERE upper(key_prefix) = upper($1) AND id = ANY($2) LIMIT 1`, k, linked).Scan(&id)
		return id, err
	}
	switch item.Kind {
	case "epic":
		project, err := projectOf(item.Project)
		if err != nil {
			return "", "", "", err
		}
		epicID, err := delivery.CreateEpicTx(ctx, tx, org, project, f, item.Title, item.Description)
		return item.Title, "", epicID, err
	case "task":
		project, err := projectOf(item.Project)
		if err != nil {
			return "", "", "", err
		}
		epic := ""
		if t := strings.TrimSpace(item.Epic); t != "" {
			// A new epic of this proposal, filed already; else one the project has.
			err := tx.QueryRow(ctx, `SELECT f.epic_id FROM session_filings f JOIN epics e ON e.id = f.epic_id
				WHERE f.proposal_id = $1 AND e.project_id = $2 AND lower(e.title) = lower($3) LIMIT 1`, proposalID, project, t).Scan(&epic)
			if db.IsNotFound(err) {
				err = tx.QueryRow(ctx, `SELECT id FROM epics WHERE project_id = $1 AND lower(title) = lower($2)
					ORDER BY created_at LIMIT 1`, project, t).Scan(&epic)
			}
			if err != nil && !db.IsNotFound(err) {
				return "", "", "", err
			}
		}
		id, key, err := delivery.CreateTaskTx(ctx, tx, org, project, f, epic, item.Title, strings.TrimSpace(item.Goal), item.AcceptanceCriteria)
		return key, id, "", err
	case "edit":
		id, _, err := delivery.TaskByKey(ctx, tx, linked, item.Task)
		if err != nil {
			return "", "", "", err
		}
		var after delivery.TaskText
		if item.After != nil {
			after = *item.After
		}
		return strings.ToUpper(item.Task), id, "", delivery.EditTaskTx(ctx, tx, org, id, f, after)
	case "comment":
		id, _, err := delivery.TaskByKey(ctx, tx, linked, item.Task)
		if err != nil {
			return "", "", "", err
		}
		return strings.ToUpper(item.Task), id, "", delivery.CommentTx(ctx, tx, org, id, f, item.Text)
	}
	return "", "", "", fmt.Errorf("unknown item kind %q", item.Kind)
}

// sessionChat is a member writing to the session's agent. The agent is
// told who wrote it (delivery.Attributed). With no live agent the message
// starts one, briefed; with one, it is its next input, delivered at its
// next step (a directive), resuming it if parked. While the agent waits on
// a question, the person it is for answers it with their message; anyone
// else's is held and goes with the answer, not as it.
func (s *Server) sessionChat(w http.ResponseWriter, r *http.Request, org string) error {
	p, err := sessionPrincipal(r)
	if err != nil {
		return err
	}
	var body struct {
		Text string `json:"text"`
	}
	if err := read(r, &body); err != nil {
		return err
	}
	if strings.TrimSpace(body.Text) == "" {
		return fail(http.StatusBadRequest, "bad_request", "text is required")
	}
	if len(body.Text) > delivery.ChatMessageMax {
		return fail(http.StatusBadRequest, "bad_request", "a message is at most %d bytes", delivery.ChatMessageMax)
	}
	id := r.PathValue("id")
	wr := p.writer()
	var out map[string]any
	created := false
	err = s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
		if _, err := writer(r.Context(), tx, id, p.Person, "write to the agent"); err != nil {
			return err
		}
		if err := delivery.LockSession(r.Context(), tx, id); err != nil {
			return err
		}
		name, err := delivery.PersonName(r.Context(), tx, p.Person)
		if err != nil {
			return err
		}
		told := delivery.Attributed(name, body.Text)
		var runID string
		var ending bool
		find := func() error {
			return tx.QueryRow(r.Context(), `SELECT r.id, `+delivery.Ending+` FROM runs r
				WHERE r.session_id = $1 AND `+delivery.LiveBrainstorm+` FOR NO KEY UPDATE`, id).Scan(&runID, &ending)
		}
		err = find()
		if err == nil && ending {
			ref := delivery.RunRef{Org: org, SessionID: id, RunID: runID}
			if err := delivery.EndBrainstorm(r.Context(), tx, ref, "its container stopped"); err != nil {
				return err
			}
			err = find()
		}
		if db.IsNotFound(err) {
			created = true
			runID, err = delivery.StartBrainstorm(r.Context(), tx, org, id, wr, told, body.Text)
			out = map[string]any{"runId": runID, "created": true}
			return err
		}
		if err != nil {
			return err
		}
		ref := delivery.RunRef{Org: org, SessionID: id, RunID: runID}
		var questionID, prompt, to string
		qerr := tx.QueryRow(r.Context(), `SELECT q.id, q.prompt, COALESCE(q.to_person, '') FROM questions q
			WHERE q.run_id = $1 AND q.status = 'open' ORDER BY q.asked_at DESC LIMIT 1 FOR UPDATE`, runID).Scan(&questionID, &prompt, &to)
		if qerr != nil && !db.IsNotFound(qerr) {
			return qerr
		}
		if qerr == nil && (to == "" || to == p.Person) {
			directiveID, err := answerSessionQuestion(r.Context(), tx, ref, questionID, prompt, name, body.Text, p)
			if err != nil {
				return err
			}
			out = map[string]any{"runId": runID, "created": false, "questionId": questionID, "directiveId": directiveID}
			return nil
		}
		directiveID, _, err := delivery.QueueDirective(r.Context(), tx, ref, delivery.Directive{Text: told, Scope: "run"})
		if err != nil {
			return err
		}
		payload := map[string]any{"text": body.Text, "directiveId": directiveID}
		if qerr == nil {
			// Waiting on another member's answer: held until it comes.
			if _, err := tx.Exec(r.Context(), `UPDATE directives SET held_for = $2 WHERE id = $1`, directiveID, questionID); err != nil {
				return err
			}
			payload["heldFor"] = questionID
		}
		if err := delivery.RequestResumeForMessage(r.Context(), tx, runID, "a message in the session"); err != nil {
			return err
		}
		out = map[string]any{"runId": runID, "created": false, "directiveId": directiveID}
		return delivery.ChatEvent(r.Context(), tx, ref, wr, payload)
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.ConstraintName == "runs_live_brainstorm_idx" {
		return fail(http.StatusConflict, "conflict", "the session's agent was just started; send again")
	}
	if err != nil {
		return err
	}
	s.kick()
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	write(w, status, out)
	return nil
}

// answerSessionQuestion settles the agent's open question with a member's
// answer, queued as its next input, naming who answered.
func answerSessionQuestion(ctx context.Context, tx pgx.Tx, ref delivery.RunRef, questionID, prompt, name, text string, p principal) (string, error) {
	if _, err := tx.Exec(ctx, `UPDATE questions SET status = 'answered', answer = $2, answered_at = now(),
		answered_by_person = $3 WHERE id = $1`, questionID, text, p.Person); err != nil {
		return "", err
	}
	directiveID, _, err := delivery.QueueDirective(ctx, tx, ref, delivery.Directive{
		Text: fmt.Sprintf("%s answered your question %q:\n\n%s", name, prompt, text), Scope: "run"})
	if err != nil {
		return "", err
	}
	ev := ref.Event("question.answered", p.writer().ActorType, map[string]any{"questionId": questionID, "answer": text,
		"directiveId": directiveID})
	ev.ActorID = p.Actor
	_, err = ledger.Append(ctx, tx, ev)
	return directiveID, err
}

// inviteToSession adds people as chat or read, owner only. They see the
// whole conversation once they accept; until then only their invitation.
func (s *Server) inviteToSession(w http.ResponseWriter, r *http.Request, org string) error {
	p, err := sessionPrincipal(r)
	if err != nil {
		return err
	}
	var body struct {
		People []string `json:"people"`
		Role   string   `json:"role"`
	}
	if err := read(r, &body); err != nil {
		return err
	}
	if body.Role == "" {
		body.Role = delivery.SessionChat
	}
	if body.Role != delivery.SessionChat && body.Role != delivery.SessionRead {
		return fail(http.StatusBadRequest, "bad_request", "role is chat or read")
	}
	if len(body.People) == 0 || len(body.People) > 50 {
		return fail(http.StatusBadRequest, "bad_request", "name 1 to 50 people")
	}
	id := r.PathValue("id")
	err = s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
		if err := owner(r.Context(), tx, id, p.Person, "share it"); err != nil {
			return err
		}
		var invited []string
		for _, person := range body.People {
			var ok bool
			if err := tx.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM people WHERE id = $1 AND removed_at IS NULL)`,
				person).Scan(&ok); err != nil {
				return err
			}
			if !ok {
				return fail(http.StatusNotFound, "not_found", "%s is not one of this organisation's people", person)
			}
			tag, err := tx.Exec(r.Context(), `INSERT INTO session_people (session_id, person_id, organization_id, role, invited_by)
				VALUES ($1, $2, $3, $4, $5) ON CONFLICT DO NOTHING`, id, person, org, body.Role, p.Person)
			if err != nil {
				return err
			}
			if tag.RowsAffected() > 0 {
				invited = append(invited, person)
			}
		}
		if len(invited) == 0 {
			return nil
		}
		return delivery.SessionEvent(r.Context(), tx, delivery.SessionRef(org, id), delivery.EvSessionShared, p.ActorType, p.Actor,
			map[string]any{"people": invited, "role": body.Role})
	})
	if err != nil {
		return err
	}
	write(w, http.StatusOK, map[string]any{"id": id})
	return nil
}

// changeSessionRole is the owner making a member chat or read.
func (s *Server) changeSessionRole(w http.ResponseWriter, r *http.Request, org string) error {
	p, err := sessionPrincipal(r)
	if err != nil {
		return err
	}
	var body struct {
		Role string `json:"role"`
	}
	if err := read(r, &body); err != nil {
		return err
	}
	if body.Role != delivery.SessionChat && body.Role != delivery.SessionRead {
		return fail(http.StatusBadRequest, "bad_request", "role is chat or read; to make someone owner, hand the session over")
	}
	id, person := r.PathValue("id"), r.PathValue("person")
	err = s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
		if err := owner(r.Context(), tx, id, p.Person, "change roles"); err != nil {
			return err
		}
		tag, err := tx.Exec(r.Context(), `UPDATE session_people SET role = $3 WHERE session_id = $1 AND person_id = $2 AND role <> 'owner'`,
			id, person, body.Role)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return fail(http.StatusNotFound, "not_found", "%s is not a member you can change", person)
		}
		return delivery.SessionEvent(r.Context(), tx, delivery.SessionRef(org, id), delivery.EvSessionRoleChanged, p.ActorType, p.Actor,
			map[string]any{"person": person, "role": body.Role})
	})
	if err != nil {
		return err
	}
	write(w, http.StatusOK, map[string]any{"id": id})
	return nil
}

// removeFromSession is the owner taking someone out (or withdrawing an
// invitation). The owner is never removed: they hand it over. What the
// person filed stays: those are ordinary tasks.
func (s *Server) removeFromSession(w http.ResponseWriter, r *http.Request, org string) error {
	p, err := sessionPrincipal(r)
	if err != nil {
		return err
	}
	id, person := r.PathValue("id"), r.PathValue("person")
	err = s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
		if err := owner(r.Context(), tx, id, p.Person, "remove people"); err != nil {
			return err
		}
		tag, err := tx.Exec(r.Context(), `DELETE FROM session_people WHERE session_id = $1 AND person_id = $2 AND role <> 'owner'`, id, person)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return fail(http.StatusNotFound, "not_found", "%s is not a member you can remove", person)
		}
		return delivery.SessionEvent(r.Context(), tx, delivery.SessionRef(org, id), delivery.EvSessionRemoved, p.ActorType, p.Actor,
			map[string]any{"person": person})
	})
	if err != nil {
		return err
	}
	write(w, http.StatusOK, map[string]any{"id": id})
	return nil
}

// handOverSession makes someone else the owner. The same Run continues; the
// agent is told who owns it now; what was filed stays whose it was. The
// owner before keeps chat or read, or leaves. To someone not in the
// session it is their invitation, as owner, and takes effect when they
// accept.
func (s *Server) handOverSession(w http.ResponseWriter, r *http.Request, org string) error {
	p, err := sessionPrincipal(r)
	if err != nil {
		return err
	}
	var body struct {
		Person string `json:"person"`
		Keep   string `json:"keep"`
	}
	if err := read(r, &body); err != nil {
		return err
	}
	if body.Keep == "" {
		body.Keep = delivery.SessionChat
	}
	if body.Keep != delivery.SessionChat && body.Keep != delivery.SessionRead && body.Keep != "leave" {
		return fail(http.StatusBadRequest, "bad_request", "keep is chat, read or leave")
	}
	id := r.PathValue("id")
	pending := false
	err = s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
		if err := owner(r.Context(), tx, id, p.Person, "hand it over"); err != nil {
			return err
		}
		if body.Person == p.Person {
			return fail(http.StatusBadRequest, "bad_request", "you already own it")
		}
		if err := delivery.LockSession(r.Context(), tx, id); err != nil {
			return err
		}
		var ok bool
		if err := tx.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM people WHERE id = $1 AND removed_at IS NULL)`,
			body.Person).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return fail(http.StatusNotFound, "not_found", "%s is not one of this organisation's people", body.Person)
		}
		var accepted *bool
		err := tx.QueryRow(r.Context(), `SELECT accepted_at IS NOT NULL FROM session_people WHERE session_id = $1 AND person_id = $2`,
			id, body.Person).Scan(&accepted)
		if err != nil && !db.IsNotFound(err) {
			return err
		}
		if accepted != nil && *accepted {
			return makeOwner(r.Context(), tx, org, id, p.Person, body.Person, body.Keep, p)
		}
		// Not in it yet: their invitation, as owner, waiting for them.
		pending = true
		if _, err := tx.Exec(r.Context(), `UPDATE session_people SET becomes_owner = false, handover_keep = NULL
			WHERE session_id = $1 AND becomes_owner`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `INSERT INTO session_people (session_id, person_id, organization_id, role, invited_by,
				becomes_owner, handover_keep)
			VALUES ($1, $2, $3, 'chat', $4, true, $5)
			ON CONFLICT (session_id, person_id) DO UPDATE SET becomes_owner = true, handover_keep = $5, invited_by = $4`,
			id, body.Person, org, p.Person, body.Keep); err != nil {
			return err
		}
		return delivery.SessionEvent(r.Context(), tx, delivery.SessionRef(org, id), delivery.EvSessionShared, p.ActorType, p.Actor,
			map[string]any{"people": []string{body.Person}, "role": "owner"})
	})
	if err != nil {
		return err
	}
	s.kick()
	write(w, http.StatusOK, map[string]any{"id": id, "pending": pending})
	return nil
}

// makeOwner swaps the owner within tx (the one-owner check runs at commit)
// and tells the agent.
func makeOwner(ctx context.Context, tx pgx.Tx, org, sessionID, from, to, keep string, by principal) error {
	if keep == "leave" {
		if _, err := tx.Exec(ctx, `DELETE FROM session_people WHERE session_id = $1 AND person_id = $2`, sessionID, from); err != nil {
			return err
		}
	} else if _, err := tx.Exec(ctx, `UPDATE session_people SET role = $3 WHERE session_id = $1 AND person_id = $2`,
		sessionID, from, keep); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE session_people SET role = 'owner', accepted_at = COALESCE(accepted_at, now()),
		becomes_owner = false, handover_keep = NULL WHERE session_id = $1 AND person_id = $2`, sessionID, to); err != nil {
		return err
	}
	toName, err := delivery.PersonName(ctx, tx, to)
	if err != nil {
		return err
	}
	fromName, err := delivery.PersonName(ctx, tx, from)
	if err != nil {
		return err
	}
	if err := delivery.SessionEvent(ctx, tx, delivery.SessionRef(org, sessionID), delivery.EvSessionOwner, by.ActorType, by.Actor,
		map[string]any{"from": from, "to": to, "keep": keep, "fromName": fromName, "toName": toName}); err != nil {
		return err
	}
	stays := fromName + " stays in it and can chat."
	switch keep {
	case delivery.SessionRead:
		stays = fromName + " stays in it and can read."
	case "leave":
		stays = fromName + " left it."
	}
	_, err = delivery.TellBrainstorm(ctx, tx, org, sessionID, toName+" owns this session now. "+stays+
		" Edits you propose are filed by whoever owns the task, as before.")
	return err
}

// acceptSession is an invitee joining: they see the whole conversation from
// now on. A handover's invitation makes them the owner.
func (s *Server) acceptSession(w http.ResponseWriter, r *http.Request, org string) error {
	p, err := sessionPrincipal(r)
	if err != nil {
		return err
	}
	id := r.PathValue("id")
	err = s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
		var becomesOwner bool
		var keep *string
		if err := tx.QueryRow(r.Context(), `SELECT becomes_owner, handover_keep FROM session_people
			WHERE session_id = $1 AND person_id = $2 AND accepted_at IS NULL FOR UPDATE`, id, p.Person).Scan(&becomesOwner, &keep); err != nil {
			if db.IsNotFound(err) {
				return fail(http.StatusNotFound, "not_found", "no invitation to session %s", id)
			}
			return err
		}
		if err := delivery.LockSession(r.Context(), tx, id); err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `UPDATE session_people SET accepted_at = now(), becomes_owner = false, handover_keep = NULL
			WHERE session_id = $1 AND person_id = $2`, id, p.Person); err != nil {
			return err
		}
		if err := delivery.SessionEvent(r.Context(), tx, delivery.SessionRef(org, id), delivery.EvSessionJoined, p.ActorType, p.Actor,
			map[string]any{"person": p.Person}); err != nil {
			return err
		}
		if !becomesOwner {
			return nil
		}
		var from string
		if err := tx.QueryRow(r.Context(), `SELECT person_id FROM session_people WHERE session_id = $1 AND role = 'owner'`, id).
			Scan(&from); err != nil {
			return err
		}
		return makeOwner(r.Context(), tx, org, id, from, p.Person, *keep, p)
	})
	if err != nil {
		return err
	}
	s.kick()
	write(w, http.StatusOK, map[string]any{"id": id})
	return nil
}

// declineSession is an invitee saying no: their invitation goes.
func (s *Server) declineSession(w http.ResponseWriter, r *http.Request, org string) error {
	p, err := sessionPrincipal(r)
	if err != nil {
		return err
	}
	id := r.PathValue("id")
	err = s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
		tag, err := tx.Exec(r.Context(), `DELETE FROM session_people WHERE session_id = $1 AND person_id = $2 AND accepted_at IS NULL`,
			id, p.Person)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return fail(http.StatusNotFound, "not_found", "no invitation to session %s", id)
		}
		return delivery.SessionEvent(r.Context(), tx, delivery.SessionRef(org, id), delivery.EvSessionDeclined, p.ActorType, p.Actor,
			map[string]any{"person": p.Person})
	})
	if err != nil {
		return err
	}
	write(w, http.StatusOK, map[string]any{"id": id})
	return nil
}
