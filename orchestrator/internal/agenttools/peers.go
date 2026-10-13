package agenttools

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// list_tasks with peers: what each of the caller's peers is doing, read
// from what dude already stores — its Runs, its heads, the run_diffs
// snapshot of its code, its agent's last message. Bounded by the epic's
// size: a handful of rows.
const (
	peerFiles       = 10
	peerLastMessage = 600
)

type peerOut struct {
	Key    string `json:"key"`
	Title  string `json:"title"`
	Status string `json:"status"`
	// The caller's own task.
	Self bool `json:"self,omitempty"`
	// "epic" today; "depends on" links later.
	Via string `json:"via,omitempty"`
	// Its newest phase Run's phase and role, and whether that Run is live,
	// parked, or ended ("none": no Run going).
	Phase string `json:"phase,omitempty"`
	Role  string `json:"role,omitempty"`
	Run   string `json:"run"`
	RunID string `json:"runId,omitempty"`
	// The peer whose reply its Run waits on, by key.
	WaitingOn string     `json:"waitingOn,omitempty"`
	Heads     []peerHead `json:"heads"`
	// Its code by churn, the top peerFiles of FileCount, from the stored
	// snapshot as of DiffAt.
	Files     []peerFile `json:"files"`
	FileCount int        `json:"fileCount"`
	DiffAt    *time.Time `json:"diffAt,omitempty"`
	// Its agent's last message: one paragraph, at most peerLastMessage.
	LastSaid string `json:"lastSaid,omitempty"`
	// Its messages to the caller not yet read, and whether its Run waits on
	// the caller's reply to one.
	Waiting int  `json:"messagesForYou"`
	Waits   bool `json:"waitsOnYourReply,omitempty"`
}

type peerHead struct {
	Repo   string `json:"repo"`
	Branch string `json:"branch,omitempty"`
	Head   string `json:"head,omitempty"`
}

type peerFile struct {
	Path      string `json:"path"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
}

type listPeersOut struct {
	Tasks []peerOut `json:"tasks"`
}

// peerStateRun (SQL, $1 a task): the Run a task's state is read from: its
// newest phase Run, a live or parked one first.
const peerStateRun = `SELECT r.id, r.phase::text, r.status::text FROM runs r
	WHERE r.task_id = $1 AND r.phase IS NOT NULL AND r.kind = 'agent'
	ORDER BY r.status IN ('pending', 'scheduled', 'starting', 'running', 'paused') DESC, r.created_at DESC LIMIT 1`

// peerDiffRun (SQL, $1 a task): the Run whose stored diff stands for a
// task's code: its newest Run of a phase that changes code, a live one
// first; else its newest phase Run.
const peerDiffRun = `SELECT r.id FROM runs r
	WHERE r.task_id = $1 AND r.phase IS NOT NULL AND r.kind = 'agent'
	ORDER BY r.phase IN ('implement', 'fix', 'simplify') DESC,
		r.status IN ('pending', 'scheduled', 'starting', 'running', 'paused') DESC, r.created_at DESC LIMIT 1`

func listPeers(ctx context.Context, tx pgx.Tx, c Caller) (listPeersOut, error) {
	out := listPeersOut{Tasks: []peerOut{}}
	peers, err := delivery.Peers(ctx, tx, c.TaskID)
	if err != nil || len(peers) == 0 {
		return out, err
	}
	var self delivery.Peer
	if err := tx.QueryRow(ctx, `SELECT t.id, p.key_prefix || '-' || t.number, t.title, t.project_id, ''
		FROM tasks t JOIN projects p ON p.id = t.project_id WHERE t.id = $1`, c.TaskID).
		Scan(&self.ID, &self.Key, &self.Title, &self.ProjectID, &self.Via); err != nil {
		return out, err
	}
	for _, p := range append([]delivery.Peer{self}, peers...) {
		row, err := peerRow(ctx, tx, c, p)
		if err != nil {
			return out, err
		}
		row.Self = p.ID == c.TaskID
		out.Tasks = append(out.Tasks, row)
	}
	return out, nil
}

func peerRow(ctx context.Context, tx pgx.Tx, c Caller, p delivery.Peer) (peerOut, error) {
	out := peerOut{Key: p.Key, Title: p.Title, Via: p.Via, Run: "none", Heads: []peerHead{}, Files: []peerFile{}}
	if err := tx.QueryRow(ctx, `SELECT status::text FROM tasks WHERE id = $1`, p.ID).Scan(&out.Status); err != nil {
		return out, err
	}
	var runID, phase, status string
	err := tx.QueryRow(ctx, peerStateRun, p.ID).Scan(&runID, &phase, &status)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return out, err
	default:
		out.Phase, out.Role = phase, delivery.PromptRoleForPhase[phase]
		switch status {
		case "paused":
			out.Run, out.RunID = "parked", runID
		case "pending", "scheduled", "starting", "running":
			out.Run, out.RunID = "live", runID
		}
	}
	if out.RunID != "" {
		if err := tx.QueryRow(ctx, `SELECT COALESCE((SELECT pr.key_prefix || '-' || t.number FROM peer_waits w
				JOIN peer_messages m ON m.id = w.message_id JOIN tasks t ON t.id = m.to_task_id JOIN projects pr ON pr.id = t.project_id
				WHERE w.run_id = $1 AND w.closed_at IS NULL), '')`, out.RunID).Scan(&out.WaitingOn); err != nil {
			return out, err
		}
	}
	heads, err := delivery.TaskHeads(ctx, tx, p.ID)
	if err != nil {
		return out, err
	}
	for _, h := range heads {
		out.Heads = append(out.Heads, peerHead{Repo: h.Repo, Branch: h.Branch, Head: h.SHA})
	}
	// Files by churn, from the stored snapshot: never a live checkout.
	var diffAt *time.Time
	rows, err := tx.Query(ctx, `SELECT f->>'path', COALESCE((f->>'additions')::int, 0), COALESCE((f->>'deletions')::int, 0),
			d.updated_at, jsonb_array_length(d.files)
		FROM run_diffs d CROSS JOIN LATERAL jsonb_array_elements(d.files) f
		WHERE d.run_id = (`+peerDiffRun+`)
		ORDER BY COALESCE((f->>'additions')::int, 0) + COALESCE((f->>'deletions')::int, 0) DESC, f->>'path' LIMIT $2`, p.ID, peerFiles)
	if err != nil {
		return out, err
	}
	var f peerFile
	if _, err := pgx.ForEachRow(rows, []any{&f.Path, &f.Additions, &f.Deletions, &diffAt, &out.FileCount}, func() error {
		out.Files = append(out.Files, f)
		out.DiffAt = diffAt
		return nil
	}); err != nil {
		return out, err
	}
	var said string
	if err := tx.QueryRow(ctx, `SELECT COALESCE((SELECT last.payload->>'text' FROM runs r CROSS JOIN LATERAL (
			SELECT e.payload, e.cursor FROM events e WHERE e.run_id = r.id AND e.event_type = 'agent.message'
			ORDER BY e.cursor DESC LIMIT 1) last
			WHERE r.task_id = $1 AND r.phase IS NOT NULL
			ORDER BY last.cursor DESC LIMIT 1), '')`, p.ID).Scan(&said); err != nil {
		return out, err
	}
	out.LastSaid = paragraph(said, peerLastMessage)
	if p.ID == c.TaskID {
		return out, nil
	}
	err = tx.QueryRow(ctx, `SELECT count(*),
			COALESCE(bool_or(EXISTS (SELECT 1 FROM peer_waits w WHERE w.message_id = m.id AND w.closed_at IS NULL)), false)
		FROM peer_messages m WHERE m.from_task_id = $1 AND m.to_task_id = $2 AND m.read_at IS NULL AND m.briefed_run_id IS NULL`,
		p.ID, c.TaskID).Scan(&out.Waiting, &out.Waits)
	return out, err
}

// paragraph is the first paragraph of s on one line, cut to n characters.
func paragraph(s string, n int) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "\n\n"); i >= 0 {
		s = s[:i]
	}
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}
