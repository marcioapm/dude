package agenttools

import (
	"context"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// A session or task started from chat is fenced from the projects hidden
// from its provider (delivery.Fence). A session's own tools read projects
// through delivery.SessionProjects and OrgProjects, which leave fenced ones
// out; fenced is the check every other reach makes: a project or task named
// by key, a relation's other side, a peer, a memory's project.

// fenceOf is c's fence: its session's, else its task's. Hiding applies to
// new work: a task's (or a conductor's) own project is never fenced from
// it, so a task already in a project hidden since keeps working on it.
func fenceOf(ctx context.Context, tx pgx.Tx, c Caller) (delivery.Fence, error) {
	if c.AutomationID != "" {
		return delivery.Fence{}, nil
	}
	f, err := delivery.FenceOf(ctx, tx, c.SessionID, c.TaskID)
	if err != nil || c.SessionID != "" || c.ProjectID == "" || !f.Hides(c.ProjectID) {
		return f, err
	}
	f.Hidden = slices.DeleteFunc(slices.Clone(f.Hidden), func(p string) bool { return p == c.ProjectID })
	return f, nil
}

// fenced refuses project for c when it is hidden from c's chat provider;
// named is how the refusal names it.
func fenced(ctx context.Context, tx pgx.Tx, c Caller, project, named string) error {
	if project == "" {
		return nil
	}
	f, err := fenceOf(ctx, tx, c)
	if err != nil || !f.Hides(project) {
		return err
	}
	if strings.TrimSpace(named) == "" {
		var key string
		if err := tx.QueryRow(ctx, `SELECT key_prefix FROM projects WHERE id = $1`, project).Scan(&key); err == nil {
			named = key
		} else {
			named = "the project"
		}
	}
	return refuse(rProjectHiddenFromChat, named, f.Provider)
}

// hiddenNamed refuses key when it names (by key, name or id) a project
// fenced off from c, so the agent is told why rather than that it does not
// exist; nil otherwise.
func hiddenNamed(ctx context.Context, tx pgx.Tx, c Caller, key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil
	}
	f, err := fenceOf(ctx, tx, c)
	if err != nil || len(f.Hidden) == 0 {
		return err
	}
	var id, prefix string
	err = tx.QueryRow(ctx, `SELECT id, key_prefix FROM projects WHERE id = ANY($1)
		AND (id = $2 OR upper(key_prefix) = upper($2) OR lower(name) = lower($2)) LIMIT 1`, f.Hidden, key).Scan(&id, &prefix)
	if db.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return refuse(rProjectHiddenFromChat, prefix, f.Provider)
}

// sessionChild says c is a Run a session's agent started (start_agent): it
// has the session and neither task nor project, and reads as its session.
func sessionChild(c Caller) bool {
	return c.SessionID != "" && c.ProjectID == "" && c.TaskID == "" && !sessionAgent(c.Role)
}

// peersInReach are peers less those in a project fenced off from c.
func peersInReach(ctx context.Context, tx pgx.Tx, c Caller, peers []delivery.Peer) ([]delivery.Peer, error) {
	f, err := fenceOf(ctx, tx, c)
	if err != nil || len(f.Hidden) == 0 {
		return peers, err
	}
	return slices.DeleteFunc(peers, func(p delivery.Peer) bool { return f.Hides(p.ProjectID) }), nil
}

// peerInReach is delivery.PeerByKey, a peer in a fenced project being none.
func peerInReach(ctx context.Context, tx pgx.Tx, c Caller, key string) (delivery.Peer, bool, error) {
	p, ok, err := delivery.PeerByKey(ctx, tx, c.TaskID, key)
	if err != nil || !ok {
		return p, ok, err
	}
	f, err := fenceOf(ctx, tx, c)
	if err != nil || f.Hides(p.ProjectID) {
		return delivery.Peer{}, false, err
	}
	return p, true, nil
}
