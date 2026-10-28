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

const (
	// noticeBatch bounds one PostNotices pass.
	noticeBatch = 50
	// noticeWindow is how long after it was recorded a notice is still
	// tried on a transient failure: a provider's outage of hours, not a
	// count shared with deliveries, where a person is waiting.
	noticeWindow = 6 * time.Hour
)

type dueNotice struct {
	ID, Org string
}

type notice struct {
	ID, Kind, Provider, Workspace, Conversation, Thread string
	// One of TaskID and EpicID is set: the notice's subject. TriggerTaskID
	// is an epic notice's task whose move or leaving recorded it, if kept.
	TaskID, EpicID, TriggerTaskID, Key, Title, Detail, Slug string
	Project, ConversationName, ProviderName                 string
	Attempts                                                int
	// Recorded more than noticeWindow ago: a failure now gives up.
	Expired bool
}

// noticeText is a notice's one line: the task's key and title, or the
// epic's project key and title, what happened, and why a task failed or
// stopped or how an epic's tasks ended. Plain words: the provider escapes
// them once. The Open in dude button links the task or epic.
func noticeText(n notice) string {
	head := n.Key + " " + n.Title
	switch n.Kind {
	case delivery.NoticeStarted:
		return "Started " + head + "."
	case delivery.NoticePROpened:
		return "Opened a pull request for " + head + "."
	case delivery.NoticeDone:
		return "Done: " + head + "."
	case delivery.NoticeEpicStarted:
		return "Started the epic " + n.Key + " › " + n.Title + "."
	case delivery.NoticeEpicPROpened:
		return "Opened the first pull request for the epic " + n.Key + " › " + n.Title + "."
	case delivery.NoticeEpicFinished:
		return "The epic " + n.Key + " › " + n.Title + " finished: " + n.Detail + "."
	}
	if n.Detail != "" {
		return "Stopped " + head + ": " + n.Detail + "."
	}
	return "Stopped " + head + "."
}

// PostNotices posts at most noticeBatch due notices, returning how many
// settled. Provider calls hold no transaction. Transient failures retry
// until noticeWindow after creation; other failures give up immediately.
// Delivery is at least once: a crash after posting can repeat the post
// when the claim's lease expires.
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
			FROM organizations o WHERE c.id = $1 AND o.id = c.organization_id
			  AND c.posted_at IS NULL AND c.gave_up_at IS NULL AND c.next_attempt_at <= now()
			RETURNING c.id, c.kind, c.provider, c.external_workspace_id, c.conversation_id, c.thread_id,
				COALESCE(c.task_id, ''), COALESCE(c.epic_id, ''), COALESCE(c.trigger_task_id, ''), c.subject_key, c.subject_title, c.detail, o.slug, c.attempts,
				COALESCE((SELECT t.project_id FROM tasks t WHERE t.id = c.task_id), (SELECT e.project_id FROM epics e WHERE e.id = c.epic_id)),
				c.conversation_name, c.created_at <= now() - $3::interval`, d.ID, claimLease.String(), noticeWindow.String()).
			Scan(&n.ID, &n.Kind, &n.Provider, &n.Workspace, &n.Conversation, &n.Thread, &n.TaskID, &n.EpicID, &n.TriggerTaskID, &n.Key, &n.Title, &n.Detail,
				&n.Slug, &n.Attempts, &n.Project, &n.ConversationName, &n.Expired)
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
	page := "task/" + url.PathEscape(n.TaskID)
	if n.EpicID != "" {
		page = "epic/" + url.PathEscape(n.EpicID)
	}
	reply := Reply{Text: noticeText(n), LinkURL: s.link(n.Slug, page), LinkLabel: "Open in dude"}
	ts, err := p.PostReply(ctx, inst, n.Conversation, n.Thread, reply)
	if err == nil {
		return true, s.settleNotice(ctx, d.Org, n, ts, nil, 0)
	}
	s.noteFailure(ctx, d.Org, n.Provider, err)
	if transient, after := IsTransient(err); transient && !n.Expired {
		wait := retryBackoff(n.Attempts, after)
		s.log().Warn("a chat notice failed; trying again", "notice", n.ID, "attempt", n.Attempts+1, "in", wait, "error", err)
		return false, s.settleNotice(ctx, d.Org, n, "", err, wait)
	}
	s.log().Warn("a chat notice failed; giving up", "notice", n.ID, "attempts", n.Attempts+1, "error", err)
	return true, s.settleNotice(ctx, d.Org, n, "", err, 0)
}

// settleNotice records an attempt: posted (ts), due again after retry, or
// given up with failure. A post the provider took is recorded even when
// ctx is cancelled (a shutdown), or it would be posted again.
func (s *Service) settleNotice(ctx context.Context, org string, n notice, ts string, failure error, retry time.Duration) error {
	ctx = context.WithoutCancel(ctx)
	return s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
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
		// The task's Chat says the thread was not told, and why. An epic
		// has no Chat: the line goes on its project's ledger, correlated
		// with the epic as its other events are, and in the Chat of the
		// task whose move or leaving recorded it.
		text := fmt.Sprintf("dude could not tell the %s thread in %s: %s", n.ProviderName, n.ConversationName, clip(failure.Error(), 300))
		if n.EpicID != "" {
			text = fmt.Sprintf("dude could not tell the %s thread in %s of the epic %s › %s: %s", n.ProviderName, n.ConversationName,
				n.Key, n.Title, clip(failure.Error(), 300))
		}
		payload := map[string]any{"about": "thread_notice_failed", "kind": n.Kind, "noticeId": n.ID, "text": text}
		correlation := n.TaskID
		if n.EpicID != "" {
			payload["epicId"], correlation = n.EpicID, n.EpicID
		}
		_, err = ledger.Append(ctx, tx, ledger.Event{Type: delivery.EvChatNotice, OrganizationID: org, TaskID: n.TaskID,
			ProjectID: n.Project, ActorType: ledger.ActorSystem, ActorID: "dude", Source: ledger.SourceOrchestrator, CorrelationID: correlation,
			Payload: payload})
		if err != nil || n.TriggerTaskID == "" {
			return err
		}
		_, err = ledger.Append(ctx, tx, ledger.Event{Type: delivery.EvChatNotice, OrganizationID: org, TaskID: n.TriggerTaskID,
			ProjectID: n.Project, ActorType: ledger.ActorSystem, ActorID: "dude", Source: ledger.SourceOrchestrator, CorrelationID: n.TriggerTaskID,
			Payload: payload})
		return err
	})
}
