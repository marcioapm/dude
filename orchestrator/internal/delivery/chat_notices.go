package delivery

import (
	"context"
	"strings"
	"unicode/utf8"

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
// or one whose kind was already recorded, records nothing. detail is why
// it failed (endedDetail); what it says of the task is its own key and
// title alone, whatever is hidden from the chat since. The lateral's
// OFFSET 0 keeps the session's origin one lookup, not one per reference.
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
			(SELECT s.origin FROM sessions s WHERE t.origin->>'via' = 'session' AND s.id = t.origin->>'from')) AS origin
			OFFSET 0) o
		WHERE t.id = $2 AND o.origin->>'provider' IS NOT NULL AND o.origin->>'conversationId' IS NOT NULL
		ON CONFLICT DO NOTHING`, ids.New(ids.ChatNotice), taskID, kind, detail)
	return err
}

// RecordPROpenedNoticeTx records a task's pull-request notice. Its text
// names the task alone, with dude's link: the pull request's URL names the
// organisation's repository, and a thread may hold guests. The URL stays
// on the task page.
func RecordPROpenedNoticeTx(ctx context.Context, tx pgx.Tx, taskID string) error {
	return recordNoticeTx(ctx, tx, taskID, NoticePROpened, "")
}

// statusNoticeTx records the notice a task's move to status gives.
func statusNoticeTx(ctx context.Context, tx pgx.Tx, taskID, status, reason string) error {
	kind, ok := noticeOfStatus[status]
	if !ok {
		return nil
	}
	detail := ""
	if kind == NoticeFailed {
		detail = endedDetail(status, reason)
	}
	return recordNoticeTx(ctx, tx, taskID, kind, detail)
}

// endedReasons are the reasons a task fails or is stopped with, as said in
// a thread that may hold guests. A reason not listed (a future writer's
// error text among them) is said as endedGeneric's.
var endedReasons = map[string]string{
	"a person stopped the task":           "a person stopped the task",
	"a person stopped it":                 "a person stopped the task",
	"a person aborted a run":              "a person stopped its run",
	"pull request closed without merging": "pull request closed without merging",
}

func endedGeneric(status string) string {
	if status == "aborted" {
		return "it was cancelled"
	}
	return "it failed"
}

// endedDetail is a failed or stopped notice's reason: a fixed phrase, or a
// Close's "closed as <resolution>", with the first name of who closed it
// (closeTx's reason).
func endedDetail(status, reason string) string {
	if phrase, ok := endedReasons[reason]; ok {
		return phrase
	}
	for _, label := range ResolutionLabel {
		closed := "closed as " + strings.ToLower(label)
		if reason == closed {
			return closed
		}
		if by, ok := strings.CutPrefix(reason, closed+" by "); ok && by != "" && len(strings.Fields(by)) == 1 && utf8.RuneCountInString(by) <= 40 {
			return closed + " by " + by
		}
	}
	return endedGeneric(status)
}
