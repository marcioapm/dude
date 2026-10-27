package delivery

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/ids"
)

// What dude says in the chat thread a task came from, once each
// (chat_notices, migration 219): its delivery started, a pull request
// opened, it was done, or it failed or was stopped. Recorded in the
// transaction that moves the task; chat.Service.PostNotices posts it.
const (
	NoticeStarted  = "started"
	NoticePROpened = "pr_opened"
	NoticeDone     = "done"
	NoticeFailed   = "failed"
)

// noticeOfStatus is the notice a task's move to status gives, if any.
var noticeOfStatus = map[string]string{"running": NoticeStarted, "done": NoticeDone, "failed": NoticeFailed, "aborted": NoticeFailed}

// recordNoticeTx records kind's notice for a task whose origin is a chat
// thread: its own, or for a task filed from a chat-born session before its
// origin carried the thread, the session's. A task with no such origin,
// or one whose kind was already recorded, records nothing. detail is the
// pull request's link or why it failed; what it says of the task is its
// own key and title alone, whatever is hidden from the chat since.
func recordNoticeTx(ctx context.Context, tx pgx.Tx, taskID, kind, detail string) error {
	_, err := tx.Exec(ctx, `INSERT INTO chat_notices (id, organization_id, task_id, kind, provider, external_workspace_id,
			conversation_id, conversation_name, thread_id, task_key, task_title, detail)
		SELECT $1, t.organization_id, t.id, $3, o.origin->>'provider', COALESCE(o.origin->>'workspaceId', ''),
			o.origin->>'conversationId', COALESCE(o.origin->>'conversationName', ''),
			CASE WHEN o.origin->>'conversationKind' = 'dm' AND COALESCE((o.origin->>'topLevel')::boolean, false) THEN ''
			     ELSE COALESCE(o.origin->>'threadId', '') END,
			p.key_prefix || '-' || t.number, t.title, $4
		FROM tasks t JOIN projects p ON p.id = t.project_id
		CROSS JOIN LATERAL (SELECT COALESCE(CASE WHEN t.origin ? 'conversationId' THEN t.origin END,
			(SELECT s.origin FROM sessions s WHERE t.origin->>'via' = 'session' AND s.id = t.origin->>'from')) AS origin) o
		WHERE t.id = $2 AND o.origin->>'provider' IS NOT NULL AND o.origin->>'conversationId' IS NOT NULL
		ON CONFLICT DO NOTHING`, ids.New(ids.ChatNotice), taskID, kind, detail)
	return err
}

// RecordPROpenedNoticeTx records a task's pull-request notice, with url.
func RecordPROpenedNoticeTx(ctx context.Context, tx pgx.Tx, taskID, url string) error {
	return recordNoticeTx(ctx, tx, taskID, NoticePROpened, url)
}

// statusNoticeTx records the notice a task's move to status gives.
func statusNoticeTx(ctx context.Context, tx pgx.Tx, taskID, status, reason string) error {
	kind, ok := noticeOfStatus[status]
	if !ok {
		return nil
	}
	detail := ""
	if kind == NoticeFailed {
		detail = reason
	}
	return recordNoticeTx(ctx, tx, taskID, kind, clip(detail, 300))
}
