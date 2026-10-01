package delivery

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"
)

// What one message — a steer, an answer, the task's prompt — may carry,
// as packages/domain/src/attachments.ts says to the browser. The total
// keeps one input's body under lux's 8 MiB once base64.
const (
	MaxAttachmentsPerMessage  = 6
	MaxMessageAttachmentBytes = 5 << 20
)

// AttachmentError is a refusal of the attachment ids a message named.
type AttachmentError struct{ Message string }

func (e AttachmentError) Error() string { return e.Message }

// AttachmentInfo is an attachment as events and the API carry it.
const attachmentJSON = `json_build_object('id', a.id, 'name', a.name, 'contentType', a.content_type,
	'width', a.width, 'height', a.height, 'bytes', a.bytes,
	'original', json_build_object('contentType', a.original_content_type, 'width', a.original_width,
		'height', a.original_height, 'bytes', a.original_bytes))`

// Attach marks the task's uploads ids as sent with a message: with the
// directive directiveID, or with the task's prompt when it is "". Each
// must be this task's and not sent yet; together at most
// MaxAttachmentsPerMessage, and MaxMessageAttachmentBytes of what the agent
// is sent. Returns their metadata, in the order given, for the message's
// event. Locks the rows, so two messages cannot both take one.
func Attach(ctx context.Context, tx pgx.Tx, taskID, directiveID string, ids []string) ([]json.RawMessage, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	if len(ids) > MaxAttachmentsPerMessage {
		return nil, AttachmentError{fmt.Sprintf("a message carries at most %d images", MaxAttachmentsPerMessage)}
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			return nil, AttachmentError{fmt.Sprintf("image %s is named twice", id)}
		}
		seen[id] = true
	}
	rows, err := tx.Query(ctx, `SELECT a.id, a.task_id, a.attached_at IS NOT NULL, a.bytes FROM attachments a
		WHERE a.id = ANY($1) FOR UPDATE`, ids)
	if err != nil {
		return nil, err
	}
	type row struct {
		ID, TaskID string
		Sent       bool
		Bytes      int64
	}
	found, err := pgx.CollectRows(rows, pgx.RowToStructByPos[row])
	if err != nil {
		return nil, err
	}
	var total int64
	for _, id := range ids {
		i := slices.IndexFunc(found, func(r row) bool { return r.ID == id })
		// Another organization's is not visible here at all: the same
		// answer as one that does not exist.
		if i < 0 || found[i].TaskID != taskID {
			return nil, AttachmentError{fmt.Sprintf("image %s is not one of this task's", id)}
		}
		if found[i].Sent {
			return nil, AttachmentError{fmt.Sprintf("image %s was already sent", id)}
		}
		total += found[i].Bytes
	}
	if total > MaxMessageAttachmentBytes {
		return nil, AttachmentError{fmt.Sprintf("a message's images are at most %d MiB together", MaxMessageAttachmentBytes>>20)}
	}
	var out []json.RawMessage
	for pos, id := range ids {
		var info json.RawMessage
		if err := tx.QueryRow(ctx, `UPDATE attachments a SET directive_id = $2, for_prompt = $2::text IS NULL,
			position = $3, attached_at = now() WHERE a.id = $1 RETURNING `+attachmentJSON,
			id, nullable(directiveID), pos).Scan(&info); err != nil {
			return nil, err
		}
		out = append(out, info)
	}
	return out, nil
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// SentAttachment is one image as it is sent to the agent: its name, type
// and where its bytes are.
type SentAttachment struct {
	Name, ContentType, ObjectKey string
	Bytes                        int64
}

// DirectiveAttachments is what the directive's words carry: the images of
// the directive itself, or — for a Retry or an Interrupt now, which repeat
// an earlier directive's words — of the directive they repeat.
func DirectiveAttachments(ctx context.Context, tx pgx.Tx, directiveID string) ([]SentAttachment, error) {
	rows, err := tx.Query(ctx, `WITH RECURSIVE chain (id, supersedes, resends, depth) AS (
			SELECT id, supersedes, resends, 0 FROM directives WHERE id = $1
			UNION ALL
			SELECT d.id, d.supersedes, d.resends, c.depth + 1 FROM chain c
			JOIN directives d ON d.id IN (c.supersedes, c.resends)
			  AND d.text = (SELECT text FROM directives WHERE id = $1)
			WHERE c.depth < 32)
		SELECT a.name, a.content_type, a.object_key, a.bytes FROM attachments a
		JOIN (SELECT id, min(depth) AS depth FROM chain GROUP BY id) c ON c.id = a.directive_id
		WHERE c.depth = (SELECT min(c2.depth) FROM chain c2 JOIN attachments a2 ON a2.directive_id = c2.id)
		ORDER BY a.position`, directiveID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[SentAttachment])
}

// PromptAttachments are the images given with the task's prompt, for its
// first agent Run only: later phases are told what was decided, not shown
// the pictures again.
func PromptAttachments(ctx context.Context, tx pgx.Tx, runID string) ([]SentAttachment, error) {
	rows, err := tx.Query(ctx, `SELECT a.name, a.content_type, a.object_key, a.bytes
		FROM runs r JOIN attachments a ON a.task_id = r.task_id AND a.for_prompt
		WHERE r.id = $1 AND NOT EXISTS (SELECT 1 FROM runs o WHERE o.task_id = r.task_id AND o.kind = 'agent'
		  AND o.id <> r.id AND (o.created_at, o.id) < (r.created_at, r.id))
		ORDER BY a.position`, runID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[SentAttachment])
}

// PromptAttachmentInfo is the metadata of the images the Run was given
// with its prompt (PromptAttachments), for the transcript's prompt turn.
func PromptAttachmentInfo(ctx context.Context, tx pgx.Tx, runID string) ([]json.RawMessage, error) {
	rows, err := tx.Query(ctx, `SELECT `+attachmentJSON+`
		FROM runs r JOIN attachments a ON a.task_id = r.task_id AND a.for_prompt
		WHERE r.id = $1 AND NOT EXISTS (SELECT 1 FROM runs o WHERE o.task_id = r.task_id AND o.kind = 'agent'
		  AND o.id <> r.id AND (o.created_at, o.id) < (r.created_at, r.id))
		ORDER BY a.position`, runID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[json.RawMessage])
}
