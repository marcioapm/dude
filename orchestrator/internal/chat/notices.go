package chat

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// dude's notices in the chat thread a task came from (chat_notices,
// migration 219): its delivery started, a pull request opened, it was
// done, or it failed or was stopped. delivery records each, at most once
// per task and kind, with the move that makes it; PostNotices posts the
// due ones. dude posts these, not the session's agent: they follow the
// task's delivery, which no agent in the thread's session watches.
const (
	// noticeBatch bounds one PostNotices pass.
	noticeBatch = 50
	// noticeTries is how many times a notice is tried before dude gives up.
	noticeTries = MaxAttempts
)

type dueNotice struct {
	ID, Org string
}

type notice struct {
	ID, Kind, Provider, Workspace, Conversation, Thread string
	TaskID, Key, Title, Detail, Slug                    string
	Project, ConversationName, ProviderName             string
	Attempts                                            int
}

// noticeText is a notice's one line: the task's key and title, what
// happened, the pull request's link or the failure's reason. Plain words:
// the provider escapes them once.
func noticeText(n notice) string {
	head := n.Key + " " + n.Title
	switch n.Kind {
	case "started":
		return "Started " + head + "."
	case "pr_opened":
		return "Opened a pull request for " + head + ": " + n.Detail
	case "done":
		return "Done: " + head + "."
	}
	if n.Detail != "" {
		return "Stopped " + head + ": " + n.Detail + "."
	}
	return "Stopped " + head + "."
}

// PostNotices posts the notices that are due, at most noticeBatch a pass:
// each read and settled in its own organisation's transaction, the
// provider called with none open. A transient failure is tried again
// later, longer each time; past noticeTries, or on any other failure, dude
// gives up, recording the last error on the row. It returns how many it
// settled.
func (s *Service) PostNotices(ctx context.Context) (int, error) {
	var due []dueNotice
	if err := s.DB.InSystem(ctx, "chat-notices", func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id, organization_id FROM chat_notices
			WHERE posted_at IS NULL AND gave_up_at IS NULL AND next_attempt_at <= now()
			ORDER BY next_attempt_at, id LIMIT $1`, noticeBatch)
		if err != nil {
			return err
		}
		due, err = pgx.CollectRows(rows, pgx.RowToStructByPos[dueNotice])
		return err
	}); err != nil {
		return 0, err
	}
	settled := 0
	for _, d := range due {
		ok, err := s.postNotice(ctx, d)
		if err != nil {
			return settled, err
		}
		if ok {
			settled++
		}
	}
	return settled, nil
}

// postNotice posts one notice and records how it went. The row is claimed
// by moving its next attempt a lease ahead, so a second pass meanwhile
// skips it; a pass that dies leaves it due when the lease runs out.
func (s *Service) postNotice(ctx context.Context, d dueNotice) (bool, error) {
	var (
		n     notice
		inst  Installation
		found bool
		ok    bool
	)
	err := s.DB.InOrg(ctx, d.Org, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `UPDATE chat_notices c SET next_attempt_at = now() + $2::interval
			FROM organizations o, tasks t WHERE c.id = $1 AND o.id = c.organization_id AND t.id = c.task_id
			  AND c.posted_at IS NULL AND c.gave_up_at IS NULL AND c.next_attempt_at <= now()
			RETURNING c.id, c.kind, c.provider, c.external_workspace_id, c.conversation_id, c.thread_id,
				c.task_id, c.task_key, c.task_title, c.detail, o.slug, c.attempts, t.project_id, c.conversation_name`, d.ID, claimLease.String()).
			Scan(&n.ID, &n.Kind, &n.Provider, &n.Workspace, &n.Conversation, &n.Thread, &n.TaskID, &n.Key, &n.Title, &n.Detail, &n.Slug,
				&n.Attempts, &n.Project, &n.ConversationName)
		if db.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		n.ProviderName = n.Provider
		if p, err := s.provider(n.Provider); err == nil {
			n.ProviderName = p.Info().Name
		}
		inst, ok, err = loadInstallation(ctx, tx, n.Provider)
		return err
	})
	if err != nil || !found {
		return false, err
	}
	p, perr := s.provider(n.Provider)
	if !ok || perr != nil || inst.WorkspaceID != n.Workspace {
		return true, s.settleNotice(ctx, d.Org, n, "", fmt.Errorf("the %s installation this thread was in is gone", n.Provider), 0)
	}
	reply := Reply{Text: noticeText(n), LinkURL: s.link(n.Slug, "task/"+url.PathEscape(n.TaskID)), LinkLabel: "Open in dude"}
	ts, err := p.PostReply(ctx, inst, n.Conversation, n.Thread, reply)
	if err == nil {
		return true, s.settleNotice(ctx, d.Org, n, ts, nil, 0)
	}
	s.noteFailure(ctx, d.Org, n.Provider, err)
	if transient, after := IsTransient(err); transient && n.Attempts+1 < noticeTries {
		wait := min(max(backoffBase<<n.Attempts, after), backoffMax)
		s.log().Warn("a chat notice failed; trying again", "notice", n.ID, "attempt", n.Attempts+1, "in", wait, "error", err)
		return false, s.settleNotice(ctx, d.Org, n, "", err, wait)
	}
	s.log().Warn("a chat notice failed; giving up", "notice", n.ID, "attempts", n.Attempts+1, "error", err)
	return true, s.settleNotice(ctx, d.Org, n, "", err, 0)
}

// settleNotice records an attempt: posted (ts), due again after retry, or
// given up with failure.
func (s *Service) settleNotice(ctx context.Context, org string, n notice, ts string, failure error, retry time.Duration) error {
	return s.DB.InOrg(context.WithoutCancel(ctx), org, func(tx pgx.Tx) error {
		switch {
		case failure == nil:
			_, err := tx.Exec(ctx, `UPDATE chat_notices SET posted_at = now(), message_id = $2, attempts = attempts + 1, last_error = NULL
				WHERE id = $1`, n.ID, ts)
			return err
		case retry > 0:
			_, err := tx.Exec(ctx, `UPDATE chat_notices SET attempts = attempts + 1, last_error = $2, next_attempt_at = now() + $3::interval
				WHERE id = $1`, n.ID, clip(failure.Error(), 2000), retry.String())
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE chat_notices SET attempts = attempts + 1, last_error = $2, gave_up_at = now() WHERE id = $1`,
			n.ID, clip(strings.TrimSpace(failure.Error()), 2000))
		if err != nil {
			return err
		}
		// The task's Chat says the thread was not told, and why.
		_, err = ledger.Append(ctx, tx, ledger.Event{Type: delivery.EvChatNotice, OrganizationID: org, TaskID: n.TaskID,
			ProjectID: n.Project, ActorType: ledger.ActorSystem, ActorID: "dude", Source: ledger.SourceOrchestrator, CorrelationID: n.TaskID,
			Payload: map[string]any{"about": "thread_notice_failed", "kind": n.Kind, "noticeId": n.ID,
				"text": fmt.Sprintf("dude could not tell the %s thread in %s: %s", n.ProviderName, n.ConversationName, clip(failure.Error(), 300))}})
		return err
	})
}
