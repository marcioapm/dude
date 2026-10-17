package delivery

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
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

// attachmentJSON (SQL, over attachments a) is an attachment as events and
// the API carry it: the domain's AttachmentInfo.
const attachmentJSON = `json_build_object('id', a.id, 'name', a.name, 'contentType', a.content_type,
	'width', a.width, 'height', a.height, 'bytes', a.bytes,
	'original', json_build_object('contentType', a.original_content_type, 'width', a.original_width,
		'height', a.original_height, 'bytes', a.original_bytes))`

// Attach marks the task's uploads ids as sent with the directive
// directiveID. Each must be this task's and not sent yet (nor shown in the
// task's text: the backend attaches those); together at most
// MaxAttachmentsPerMessage, and MaxMessageAttachmentBytes of what the agent
// is sent. Returns their metadata, in the order given, for the message's
// event. Locks the rows, so two messages cannot both take one.
func Attach(ctx context.Context, tx pgx.Tx, taskID, directiveID string, ids []string) ([]json.RawMessage, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	if len(ids) > MaxAttachmentsPerMessage {
		return nil, AttachmentError{fmt.Sprintf(mMessageCarriesAtMostImages, MaxAttachmentsPerMessage)}
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			return nil, AttachmentError{fmt.Sprintf(mImageNamedTwice, id)}
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
			return nil, AttachmentError{fmt.Sprintf(mImageNotOneThisTask, id)}
		}
		if found[i].Sent {
			return nil, AttachmentError{fmt.Sprintf(mImageWasAlreadySent, id)}
		}
		total += found[i].Bytes
	}
	if total > MaxMessageAttachmentBytes {
		return nil, AttachmentError{fmt.Sprintf(mMessageImagesAreAtMostMib, MaxMessageAttachmentBytes>>20)}
	}
	var out []json.RawMessage
	for pos, id := range ids {
		var info json.RawMessage
		if err := tx.QueryRow(ctx, `UPDATE attachments a SET directive_id = $2, position = $3, attached_at = now(),
			detached_at = NULL WHERE a.id = $1 RETURNING `+attachmentJSON,
			id, directiveID, pos).Scan(&info); err != nil {
			return nil, err
		}
		out = append(out, info)
	}
	return out, nil
}

// SentAttachment is one image as it is sent to the agent: its name, type
// and where its bytes are.
type SentAttachment struct {
	ID, Name, ContentType, ObjectKey string
	Bytes                            int64
}

// DirectiveAttachments is what the directive's words carry: the images of
// the directive itself, or — for a Retry or an Interrupt now, which repeat
// an earlier directive's words — of the directive they repeat.
func DirectiveAttachments(ctx context.Context, tx pgx.Tx, directiveID string) ([]SentAttachment, error) {
	rows, err := tx.Query(ctx, `SELECT a.id, a.name, a.content_type, a.object_key, a.bytes FROM attachments a
		WHERE a.id IN (`+directiveImages+`) ORDER BY a.position`, directiveID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[SentAttachment])
}

// directiveImages selects (SQL, $1 a directive id) the ids of the images
// its words carry (DirectiveAttachments).
const directiveImages = `WITH RECURSIVE chain (id, supersedes, resends, depth) AS (
		SELECT id, supersedes, resends, 0 FROM directives WHERE id = $1
		UNION ALL
		SELECT d.id, d.supersedes, d.resends, c.depth + 1 FROM chain c
		JOIN directives d ON d.id IN (c.supersedes, c.resends)
		  AND d.text = (SELECT text FROM directives WHERE id = $1)
		WHERE c.depth < 32)
	SELECT a.id FROM attachments a
	JOIN (SELECT id, min(depth) AS depth FROM chain GROUP BY id) c ON c.id = a.directive_id
	WHERE c.depth = (SELECT min(c2.depth) FROM chain c2 JOIN attachments a2 ON a2.directive_id = c2.id)`

// directiveAttachmentInfo is the metadata of the images directive id's
// words carry (DirectiveAttachments), in order, for its event.
func directiveAttachmentInfo(ctx context.Context, tx pgx.Tx, id string) ([]json.RawMessage, error) {
	rows, err := tx.Query(ctx, `SELECT `+attachmentJSON+` FROM attachments a
		WHERE a.id IN (`+directiveImages+`) ORDER BY a.position`, id)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[json.RawMessage])
}

// TaskImages are the images a task's goal and criteria show
// (TaskImageIDs: the goal's, then the criteria's, by first appearance),
// those the backend attached to the task's text when it was saved
// (for_prompt). A reference to anything else — another task's, removed,
// or not attached and so liable to be swept — is left out, and the prompt
// says it is unavailable.
func TaskImages(ctx context.Context, tx pgx.Tx, taskID, goal string, criteria []string) ([]SentAttachment, error) {
	ids := TaskImageIDs(goal, criteria)
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `SELECT a.id, a.name, a.content_type, a.object_key, a.bytes
		FROM unnest($2::text[]) WITH ORDINALITY AS ref(id, n) JOIN attachments a ON a.id = ref.id
		WHERE a.task_id = $1 AND a.for_prompt ORDER BY ref.n`, taskID, ids)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[SentAttachment])
}

// SyncTaskImagesTx makes the task's images the ones its goal and criteria
// reference, in tx after the text is written: the backend's
// syncTaskAttachments (apps/control-plane/src/api/routes/attachments.ts)
// in Go, for a text the conductor writes. Each referenced image becomes the
// prompt's (for_prompt, attached, position its first appearance), and each
// no longer referenced is let go (detached_at), for the sweeper. Another
// task's image, one a message carried, or more than a message's count or
// bytes is an AttachmentError; an id with no row stays text.
func SyncTaskImagesTx(ctx context.Context, tx pgx.Tx, taskID, goal string, criteria []string) error {
	ids := TaskImageIDs(goal, criteria)
	rows, err := tx.Query(ctx, `SELECT id, task_id, directive_id IS NOT NULL, bytes FROM attachments
		WHERE id = ANY($1) ORDER BY id FOR UPDATE`, ids)
	if err != nil {
		return err
	}
	type row struct {
		ID, TaskID string
		Sent       bool
		Bytes      int64
	}
	found, err := pgx.CollectRows(rows, pgx.RowToStructByPos[row])
	if err != nil {
		return err
	}
	var total int64
	for _, r := range found {
		if r.TaskID != taskID {
			return AttachmentError{fmt.Sprintf(mImageAnotherTaskUploadThisOne, r.ID)}
		}
		if r.Sent {
			return AttachmentError{fmt.Sprintf(mImageWasSentWithMessageUpload, r.ID)}
		}
		total += r.Bytes
	}
	if len(found) > MaxAttachmentsPerMessage {
		return AttachmentError{fmt.Sprintf(mTaskTextShowsAtMostImages, MaxAttachmentsPerMessage)}
	}
	if total > MaxMessageAttachmentBytes {
		return AttachmentError{fmt.Sprintf(mTaskImagesAreAtMostMib, MaxMessageAttachmentBytes>>20)}
	}
	// Never nil: NOT (id = ANY(NULL)) is NULL, which would let none go.
	kept := []string{}
	for _, id := range ids {
		if slices.ContainsFunc(found, func(r row) bool { return r.ID == id }) {
			kept = append(kept, id)
		}
	}
	for pos, id := range kept {
		if _, err := tx.Exec(ctx, `UPDATE attachments SET for_prompt = true, attached_at = COALESCE(attached_at, now()),
			detached_at = NULL, position = $2 WHERE id = $1`, id, pos); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `UPDATE attachments SET for_prompt = false, attached_at = NULL, position = 0, detached_at = now()
		WHERE task_id = $1 AND for_prompt AND NOT (id = ANY($2))`, taskID, kept)
	return err
}

// PromptImages are the images as the prompt numbers them.
func PromptImages(images []SentAttachment) []PromptImage {
	out := make([]PromptImage, len(images))
	for i, a := range images {
		out[i] = PromptImage{ID: a.ID, Name: a.Name}
	}
	return out
}

// PromptAttachments are the images given with the Run's prompt: its
// task's (TaskImages), for every agent Run, since every phase's prompt
// carries the task (Prompt: the implementer and investigator are given it
// to do, reviewers and the tester check the work against it, the fixer and
// simplifier keep to it). A branch preview has no prompt and gets none.
// After them, the pictures of the people's preview items the Run was
// given (runs.feedback_item_ids), in the room one message has left.
func PromptAttachments(ctx context.Context, tx pgx.Tx, runID string) ([]SentAttachment, error) {
	var taskID, goal string
	var raw []byte
	var itemIDs []string
	err := tx.QueryRow(ctx, `SELECT t.id, t.goal, t.acceptance_criteria, r.feedback_item_ids FROM runs r JOIN tasks t ON t.id = r.task_id
		WHERE r.id = $1 AND r.kind = 'agent'`, runID).Scan(&taskID, &goal, &raw, &itemIDs)
	if db.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var criteria []string
	_ = json.Unmarshal(raw, &criteria)
	images, err := TaskImages(ctx, tx, taskID, goal, criteria)
	if err != nil || len(itemIDs) == 0 {
		return images, err
	}
	items, err := FeedbackItemsTx(ctx, tx, taskID, itemIDs)
	if err != nil {
		return nil, err
	}
	var used int64
	for _, a := range images {
		used += a.Bytes
	}
	pictures, err := feedbackPicturesTx(ctx, tx, taskID, items, false, MaxAttachmentsPerMessage-len(images), MaxMessageAttachmentBytes-used)
	if err != nil {
		return nil, err
	}
	for _, p := range pictures {
		if !slices.ContainsFunc(images, func(a SentAttachment) bool { return a.ID == p.ID }) {
			images = append(images, p)
		}
	}
	return images, nil
}

// PromptAttachmentInfo is the metadata of the images the Run was given
// with its prompt (PromptAttachments), for the transcript's prompt turn.
func PromptAttachmentInfo(ctx context.Context, tx pgx.Tx, runID string) ([]json.RawMessage, error) {
	sent, err := PromptAttachments(ctx, tx, runID)
	if err != nil || len(sent) == 0 {
		return nil, err
	}
	ids := make([]string, len(sent))
	for i, a := range sent {
		ids[i] = a.ID
	}
	rows, err := tx.Query(ctx, `SELECT `+attachmentJSON+` FROM unnest($1::text[]) WITH ORDINALITY AS ref(id, n)
		JOIN attachments a ON a.id = ref.id ORDER BY ref.n`, ids)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[json.RawMessage])
}
