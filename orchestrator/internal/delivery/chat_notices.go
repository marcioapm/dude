package delivery

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
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

// What dude says in the thread an epic came from, once per epic ever: its
// first task started, its first pull request opened, and every current
// task ended, with the counts.
const (
	NoticeEpicStarted  = "epic_started"
	NoticeEpicPROpened = "epic_pr_opened"
	NoticeEpicFinished = "epic_finished"
)

// noticeOfStatus is the notice a task's move to status gives, if any.
var noticeOfStatus = map[string]string{"running": NoticeStarted, "done": NoticeDone, "failed": NoticeFailed, "aborted": NoticeFailed}

// noticeDestSQL is the chat destination of a task's or epic's origin o: o
// itself when it names a conversation, the destination snapshotted on a
// task an agent made (notify, InheritOrigin), or the origin of the session
// it was filed from. NULL when none.
func noticeDestSQL(o string) string {
	return fmt.Sprintf(`COALESCE(CASE WHEN %[1]s ? 'conversationId' THEN %[1]s END, %[1]s->'notify',
		(SELECT s.origin FROM sessions s WHERE %[1]s->>'via' = 'session' AND s.id = %[1]s->>'from'))`, o)
}

// noticeColumnsSQL is the destination d's columns of a chat_notices row,
// from provider to thread_id. A direct message's top-level mention is
// answered at the top level.
const noticeColumnsSQL = `d.dest->>'provider', COALESCE(d.dest->>'workspaceId', ''),
	d.dest->>'conversationId', COALESCE(d.dest->>'conversationName', ''),
	CASE WHEN d.dest->>'conversationKind' = 'dm' AND COALESCE((d.dest->>'topLevel')::boolean, false) THEN ''
	     ELSE COALESCE(d.dest->>'threadId', '') END`

// recordNoticeTx snapshots a task's notice once per kind, where its origin
// resolves to a conversation (noticeDestSQL). Tasks with none record
// nothing. OFFSET 0 keeps the destination to one lookup, not one per
// reference. clock_timestamp orders an epic's notice after its task's when
// one move records both.
func recordNoticeTx(ctx context.Context, tx pgx.Tx, taskID, kind, detail string) error {
	_, err := tx.Exec(ctx, `INSERT INTO chat_notices (id, organization_id, task_id, kind, provider, external_workspace_id,
			conversation_id, conversation_name, thread_id, subject_key, subject_title, detail, next_attempt_at)
		SELECT $1, t.organization_id, t.id, $3, `+noticeColumnsSQL+`,
			p.key_prefix || '-' || t.number, t.title, $4, clock_timestamp()
		FROM tasks t JOIN projects p ON p.id = t.project_id
		CROSS JOIN LATERAL (SELECT `+noticeDestSQL("t.origin")+` AS dest OFFSET 0) d
		WHERE t.id = $2 AND d.dest->>'provider' IS NOT NULL AND d.dest->>'conversationId' IS NOT NULL
		ON CONFLICT DO NOTHING`, ids.New(ids.ChatNotice), taskID, kind, detail)
	return err
}

// RecordPROpenedNoticeTx names only the task, and its epic's first pull
// request names only the epic: repository URLs stay on dude's pages
// because the thread may include guests.
func RecordPROpenedNoticeTx(ctx context.Context, tx pgx.Tx, taskID string) error {
	if err := recordNoticeTx(ctx, tx, taskID, NoticePROpened, ""); err != nil {
		return err
	}
	return epicNoticeTx(ctx, tx, taskID, NoticeEpicPROpened)
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

// epicStatusNoticeTx records the epic notice a task's move to status may
// give: started on a move to running, finished on a move to an end.
func epicStatusNoticeTx(ctx context.Context, tx pgx.Tx, taskID, status string) error {
	switch noticeOfStatus[status] {
	case NoticeStarted:
		return epicNoticeTx(ctx, tx, taskID, NoticeEpicStarted)
	case NoticeDone, NoticeFailed:
		return epicNoticeTx(ctx, tx, taskID, NoticeEpicFinished)
	}
	return nil
}

// LockEpicNoticesTx serialises the epic notices of one epic for the rest of
// tx, so two of its tasks ending in overlapping transactions do not each
// count the other still running: the second waits here, and its count, a
// later statement, sees the first's commit. Taken after the moved task's
// row, and by nothing else; the epic's row would deadlock against
// ArchiveEpicTx, which locks the epic before its tasks.
func LockEpicNoticesTx(ctx context.Context, tx pgx.Tx, epicID string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('epic_notices:' || $1))`, epicID)
	return err
}

// epicNoticeTx records kind for the epic of taskID, once per epic ever,
// when the epic's origin resolves to a conversation. Finished is recorded
// only once the epic has a current (non-archived) task and every current
// task has ended, with how many were done, failed and cancelled. An epic's
// display state (planned, active, done) plays no part.
func epicNoticeTx(ctx context.Context, tx pgx.Tx, taskID, kind string) error {
	var epicID string
	err := tx.QueryRow(ctx, `SELECT e.id FROM tasks t JOIN epics e ON e.id = t.epic_id
		CROSS JOIN LATERAL (SELECT `+noticeDestSQL("e.origin")+` AS dest OFFSET 0) d
		WHERE t.id = $1 AND d.dest->>'provider' IS NOT NULL AND d.dest->>'conversationId' IS NOT NULL`, taskID).Scan(&epicID)
	if db.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := LockEpicNoticesTx(ctx, tx, epicID); err != nil {
		return err
	}
	detail := ""
	if kind == NoticeEpicFinished {
		var current, done, failed, cancelled int
		if err := tx.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE status = 'done'),
				count(*) FILTER (WHERE status = 'failed'), count(*) FILTER (WHERE status = 'aborted')
			FROM tasks WHERE epic_id = $1 AND archived_at IS NULL`, epicID).Scan(&current, &done, &failed, &cancelled); err != nil {
			return err
		}
		if current == 0 || done+failed+cancelled != current {
			return nil
		}
		detail = fmt.Sprintf("%d done, %d failed, %d cancelled", done, failed, cancelled)
	}
	_, err = tx.Exec(ctx, `INSERT INTO chat_notices (id, organization_id, epic_id, kind, provider, external_workspace_id,
			conversation_id, conversation_name, thread_id, subject_key, subject_title, detail, next_attempt_at)
		SELECT $1, e.organization_id, e.id, $3, `+noticeColumnsSQL+`, p.key_prefix, e.title, $4, clock_timestamp()
		FROM epics e JOIN projects p ON p.id = e.project_id
		CROSS JOIN LATERAL (SELECT `+noticeDestSQL("e.origin")+` AS dest OFFSET 0) d
		WHERE e.id = $2
		ON CONFLICT DO NOTHING`, ids.New(ids.ChatNotice), epicID, kind, detail)
	return err
}

// endedReasons are the reasons a task fails or is stopped with, as said in
// a thread that may hold guests. Unrecognised reasons use a generic phrase.
var endedReasons = map[string]string{
	"a person stopped the task":           "a person stopped the task",
	"a person stopped it":                 "a person stopped the task",
	"a person aborted a run":              "a person stopped its run",
	"pull request closed without merging": "pull request closed without merging",
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
			return reason
		}
		if by, ok := strings.CutPrefix(reason, closed+" by "); ok && by != "" && len(strings.Fields(by)) == 1 && utf8.RuneCountInString(by) <= 40 {
			return reason
		}
	}
	if status == "aborted" {
		return "it was cancelled"
	}
	return "it failed"
}
