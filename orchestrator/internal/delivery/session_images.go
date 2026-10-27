package delivery

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/ids"
)

// A task item of a brainstorm's proposal may name images a member shared in
// the session (ProposalItem.Images). Filed, each becomes an upload of the
// new task: a row of its own, owned by the task, pointing at the same
// stored objects (the backend's sweeper deletes an object only once no row
// names it), shown in the task's goal so every agent on the task is given
// it as a person's image in the task's text is.

// SessionImage is an image shared in a session, as an item names it.
type SessionImage struct {
	ID, Name string
	Bytes    int64
}

// SessionImagesTx reads the images ids names, each one a member sent the
// session's agent (an upload of sessionID attached to a message; every
// upload is an image, migration 071), in the order given, locked against
// removal (FOR SHARE). Refused: more than MaxAttachmentsPerMessage, one
// named twice, one not of this session or never sent, or more than
// MaxMessageAttachmentBytes together.
func SessionImagesTx(ctx context.Context, tx pgx.Tx, sessionID string, imageIDs []string) ([]SessionImage, error) {
	if len(imageIDs) == 0 {
		return nil, nil
	}
	if len(imageIDs) > MaxAttachmentsPerMessage {
		return nil, AttachmentError{fmt.Sprintf(mTaskTextShowsAtMostImages, MaxAttachmentsPerMessage)}
	}
	for i, id := range imageIDs {
		if slices.Contains(imageIDs[:i], id) {
			return nil, AttachmentError{fmt.Sprintf(mImageNamedTwice, id)}
		}
	}
	rows, err := tx.Query(ctx, `SELECT a.id, a.name, a.bytes FROM attachments a
		WHERE a.id = ANY($1) AND a.session_id = $2 AND a.attached_at IS NOT NULL FOR SHARE`, imageIDs, sessionID)
	if err != nil {
		return nil, err
	}
	found, err := pgx.CollectRows(rows, pgx.RowToStructByPos[SessionImage])
	if err != nil {
		return nil, err
	}
	out := make([]SessionImage, 0, len(imageIDs))
	var total int64
	for _, id := range imageIDs {
		i := slices.IndexFunc(found, func(s SessionImage) bool { return s.ID == id })
		if i < 0 {
			return nil, AttachmentError{fmt.Sprintf(mImageNotSharedInThisSession, id)}
		}
		total += found[i].Bytes
		out = append(out, found[i])
	}
	if total > MaxMessageAttachmentBytes {
		return nil, AttachmentError{fmt.Sprintf(mTaskImagesAreAtMostMib, MaxMessageAttachmentBytes>>20)}
	}
	return out, nil
}

// SessionImageInfo is the metadata (AttachmentInfo) of the session's
// uploads among ids, by id, for its proposal cards; one gone is left out.
func SessionImageInfo(ctx context.Context, tx pgx.Tx, sessionID string, imageIDs []string) (map[string]json.RawMessage, error) {
	out := map[string]json.RawMessage{}
	if len(imageIDs) == 0 {
		return out, nil
	}
	rows, err := tx.Query(ctx, `SELECT a.id, `+attachmentJSON+` FROM attachments a WHERE a.id = ANY($1) AND a.session_id = $2`,
		imageIDs, sessionID)
	if err != nil {
		return nil, err
	}
	var id string
	var info json.RawMessage
	_, err = pgx.ForEachRow(rows, []any{&id, &info}, func() error {
		out[id] = slices.Clone(info)
		return nil
	})
	return out, err
}

// WithSharedImageIDs is a session agent's input text with the ids of the
// images it carries, which propose's task items name them by.
func WithSharedImageIDs(text string, images []SentAttachment) string {
	if len(images) == 0 {
		return text
	}
	type shared struct{ ID, Name string }
	list := make([]shared, len(images))
	for i, a := range images {
		list[i] = shared{a.ID, a.Name}
	}
	line := render(sharedImageIDsTmpl, list)
	if strings.TrimSpace(text) == "" {
		return line
	}
	return text + "\n\n" + line
}

// ImageMarkdown is the reference a task's text shows an upload by, as
// packages/domain attachmentMarkdown writes it: brackets in the name made
// parentheses, a newline a space.
func ImageMarkdown(name, id string) string {
	name = strings.NewReplacer("[", "(", "]", ")", "\n", " ").Replace(name)
	return "![" + name + "](attachment:" + id + ")"
}

// WithSessionImages is goal with the section that shows images, each by
// its id as the task's upload; goal alone without any.
func WithSessionImages(goal string, images []SessionImage) string {
	if len(images) == 0 {
		return goal
	}
	refs := make([]string, len(images))
	for i, img := range images {
		refs[i] = ImageMarkdown(img.Name, img.ID)
	}
	section := render(sessionImagesSectionTmpl, refs)
	if fence := UnclosedFence(goal); fence != "" {
		// A goal ending inside a code block would show the section as code.
		goal += "\n" + fence
	}
	return goal + "\n\n" + section
}

// CreateTaskWithSessionImagesTx is CreateTaskTx for a proposal's task
// item: images (SessionImagesTx, of sessionID) become the new task's
// uploads, and its goal ends with the section showing them. A refused image
// is an AttachmentError, before anything is written.
func CreateTaskWithSessionImagesTx(ctx context.Context, tx pgx.Tx, org, projectID string, f Filer, epicID, title, goal string,
	criteria []string, sessionID string, imageIDs []string) (string, string, error) {
	sources, err := SessionImagesTx(ctx, tx, sessionID, imageIDs)
	if err != nil || len(sources) == 0 {
		if err != nil {
			return "", "", err
		}
		return CreateTaskTx(ctx, tx, org, projectID, f, epicID, title, goal, criteria)
	}
	copies := make([]SessionImage, len(sources))
	for i, s := range sources {
		copies[i] = SessionImage{ID: ids.New(ids.Attachment), Name: s.Name, Bytes: s.Bytes}
	}
	goal = WithSessionImages(goal, copies)
	// The task is made first (the copies reference it); its goal names the
	// copies, which CreateTaskTx's image sync leaves as text until they exist.
	id, key, err := CreateTaskTx(ctx, tx, org, projectID, f, epicID, title, goal, criteria)
	if err != nil {
		return "", "", err
	}
	for i, s := range sources {
		if _, err := tx.Exec(ctx, `INSERT INTO attachments (id, organization_id, task_id, uploaded_by, name, content_type, width, height,
				bytes, sha256, object_key, original_content_type, original_width, original_height, original_bytes, original_key)
			SELECT $1, organization_id, $2, uploaded_by, name, content_type, width, height, bytes, sha256, object_key,
				original_content_type, original_width, original_height, original_bytes, original_key
			FROM attachments WHERE id = $3`, copies[i].ID, id, s.ID); err != nil {
			return "", "", err
		}
	}
	return id, key, SyncTaskImagesTx(ctx, tx, id, goal, criteria)
}
