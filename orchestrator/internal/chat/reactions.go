package chat

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// What a mention's mark follows. 👀 is put as a delivery's first stage,
// once (chat_events.working_at), then:
//   - a session (a new organisation, shared or private session, or a
//     message into a bound or resumed one): 👀 stays after the reply with
//     the link; SettleMarks puts ✅ on the session agent's first
//     agent.message or agent.tool.called after the mention went in, ❌
//     when its agent ends first, and leaves 👀 after markWaitLimit of
//     neither;
//   - a task, an epic or a bound thread's task_link: nothing runs, so ✅
//     goes with the reply;
//   - a clarifying question: the question is the answer, the 👀 is cleared;
//   - a refusal: cleared, with the private note; a failure: ❌.
//
// A mark is cosmetic: no request's route or outcome depends on it, and a
// provider refusing one is noted for the installation's health, never retried.
const (
	// markWaitLimit is how long a session's mark waits for its agent.
	markWaitLimit = 24 * time.Hour
	// markBatch bounds one SettleMarks pass.
	markBatch = 50
)

// awaitSession records, in the request's transaction (which holds the
// session's lock), the session's last event so far: its agent's output
// after that is the mention's answer.
func awaitSession(ctx context.Context, tx pgx.Tx, r *request) error {
	var after int64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(max(cursor), 0) FROM events WHERE session_id = $1`, r.ResultID).Scan(&after); err != nil {
		return err
	}
	r.AwaitsAfter = &after
	return nil
}

type awaiting struct {
	ID, Org, Provider, Session string
	After                      int64
	CreatedAt                  time.Time
}

// markPage is where the next SettleMarks pass reads from: requests still
// waiting are paged through in (created_at, id) order, so a page of
// sessions whose agents are slow never hides the ones after it.
type markPage struct {
	at time.Time
	id string
}

// SettleMarks swaps the marks of replied requests whose session showed
// life (✅) or whose agent ended with nothing said or done (❌), and stops
// waiting on those past markWaitLimit (left 👀): at most markBatch
// requests per pass. Each is settled on its row first, in its own
// transaction, and the provider called after that commits, so no
// transaction is held across a provider call and a mark is put at most
// once whatever restarts. It returns how many it settled.
func (s *Service) SettleMarks(ctx context.Context) (int, error) {
	var batch []awaiting
	from := s.marks
	if err := s.DB.InSystem(ctx, "chat-reactions", func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id, organization_id, provider, result_id, awaits_session_after, created_at FROM chat_requests
			WHERE awaits_session_after IS NOT NULL AND mark_settled_at IS NULL AND replied_at IS NOT NULL
			  AND (created_at, id) > ($2, $3)
			ORDER BY created_at, id LIMIT $1`, markBatch, from.at, from.id)
		if err != nil {
			return err
		}
		batch, err = pgx.CollectRows(rows, pgx.RowToStructByPos[awaiting])
		return err
	}); err != nil {
		return 0, err
	}
	if len(batch) < markBatch {
		s.marks = markPage{}
	} else {
		last := batch[len(batch)-1]
		s.marks = markPage{at: last.CreatedAt, id: last.ID}
	}
	settled := 0
	for _, a := range batch {
		ok, err := s.settleMark(ctx, a)
		if err != nil {
			return settled, err
		}
		if ok {
			settled++
		}
	}
	return settled, nil
}

// sessionProbe reads, in one snapshot so that output committed with its
// Run's end is never missed:
//   - spoke: the session's own agent (its brainstorm Runs, not the
//     investigators it starts) said or did something past the cursor, read
//     from events_session_cursor_idx with a primary-key lookup per event;
//   - ended: no live brainstorm Run is left (runs_live_brainstorm_idx) and
//     no ended one holds an unread message the syncer will hand to a
//     successor (delivery.UnheardBrainstorm: completed or failed, through
//     runs_session_idx and directives_unsettled_idx).
const sessionProbe = `SELECT
	EXISTS (SELECT 1 FROM events e WHERE e.session_id = $1 AND e.cursor > $2
		AND e.event_type IN ('agent.message', 'agent.tool.called')
		AND EXISTS (SELECT 1 FROM runs r WHERE r.id = e.run_id AND r.role = 'brainstorm' AND r.kind = 'agent')),
	NOT EXISTS (SELECT 1 FROM runs r WHERE r.session_id = $1 AND ` + delivery.LiveBrainstorm + `)
	AND NOT EXISTS (SELECT 1 FROM runs r JOIN directives d ON d.run_id = r.id WHERE r.session_id = $1
		AND r.role = 'brainstorm' AND r.status IN ('completed', 'failed') AND d.delivered_at IS NULL AND d.failed_at IS NULL)`

// settleMark decides one request's mark and, once it is decided and
// recorded, puts it.
func (s *Service) settleMark(ctx context.Context, a awaiting) (bool, error) {
	var (
		outcome string
		m       Mention
		inst    Installation
		ok      bool
	)
	err := s.DB.InOrg(ctx, a.Org, func(tx pgx.Tx) error {
		var spoke, ended bool
		if err := tx.QueryRow(ctx, sessionProbe, a.Session, a.After).Scan(&spoke, &ended); err != nil {
			return err
		}
		outcome = "done"
		if !spoke {
			switch {
			case ended:
				outcome = "failed"
			case s.now().Sub(a.CreatedAt) > markWaitLimit:
				outcome = "expired"
			default:
				outcome = ""
				return nil
			}
		}
		var mRaw []byte
		err := tx.QueryRow(ctx, `UPDATE chat_requests SET mark_settled = $2, mark_settled_at = now()
			WHERE id = $1 AND mark_settled_at IS NULL RETURNING mention`, a.ID, outcome).Scan(&mRaw)
		if db.IsNotFound(err) {
			// Settled by another pass meanwhile, or gone with retention.
			outcome = ""
			return nil
		}
		if err != nil {
			return err
		}
		if len(mRaw) > 0 {
			_ = json.Unmarshal(mRaw, &m)
		}
		inst, ok, err = loadInstallation(ctx, tx, a.Provider)
		return err
	})
	if err != nil || outcome == "" {
		return false, err
	}
	p, perr := s.provider(a.Provider)
	if outcome == "expired" || !ok || perr != nil || m.MessageID == "" {
		return true, nil
	}
	mark := ReactionDone
	if outcome == "failed" {
		mark = ReactionFailed
	}
	if err := p.React(ctx, inst, m.ConversationID, m.MessageID, mark); err != nil {
		s.noteFailure(ctx, a.Org, a.Provider, err)
		s.log().Warn("marking a chat mention failed", "provider", a.Provider, "request", a.ID, "error", err)
	}
	return true, nil
}
