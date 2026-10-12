package delivery

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
)

// Peers: the tasks a task's agents may read, message and brief
// (docs/design/agent-coordination.md). Who is a peer is the SQL function
// task_peers (migration 111) and nothing else: every tool, briefing and
// API reads it through these helpers, so "depends on" links join it in one
// place.

// Peer is a task its peer may name: by key, with how it is a peer.
type Peer struct {
	ID, Key, Title, ProjectID, Via string
}

// peersSQL selects a task's ($1) peers as Peer, by key.
const peersSQL = `SELECT o.id, p.key_prefix || '-' || o.number, o.title, o.project_id, tp.via
	FROM task_peers($1) tp JOIN tasks o ON o.id = tp.peer_id JOIN projects p ON p.id = o.project_id`

// Peers are a task's peers, by project key and number. None for a task on
// no epic.
func Peers(ctx context.Context, tx pgx.Tx, taskID string) ([]Peer, error) {
	rows, err := tx.Query(ctx, peersSQL+` ORDER BY p.key_prefix, o.number`, taskID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Peer])
}

// PeerByKey is the peer of taskID that key names ("ABS-2", any case); ok
// false when key names no task that is its peer, which callers refuse as
// another task's Run is refused.
func PeerByKey(ctx context.Context, tx pgx.Tx, taskID, key string) (Peer, bool, error) {
	k, valid := TaskKey(key)
	if !valid {
		return Peer{}, false, nil
	}
	dash := strings.LastIndexByte(k, '-')
	var p Peer
	err := tx.QueryRow(ctx, peersSQL+` WHERE upper(p.key_prefix) = $2 AND o.number::text = $3`,
		taskID, k[:dash], k[dash+1:]).Scan(&p.ID, &p.Key, &p.Title, &p.ProjectID, &p.Via)
	if db.IsNotFound(err) {
		return Peer{}, false, nil
	}
	return p, err == nil, err
}

// IsPeer says whether other is a peer of taskID.
func IsPeer(ctx context.Context, tx pgx.Tx, taskID, other string) (bool, error) {
	var ok bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM task_peers($1) WHERE peer_id = $2)`, taskID, other).Scan(&ok)
	return ok, err
}

// NotAPeer is the refusal for a task that is not one of the caller's
// peers, whatever the tool.
func NotAPeer(key string) string {
	return fmt.Sprintf("%s is not one of your peers: only the other tasks on your epic are (list_tasks with peers: true shows "+
		"them)", key)
}
