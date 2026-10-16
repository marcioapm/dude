package delivery_test

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// image records an upload of the task's of size bytes.
func (w *feedbackWorld) image(t *testing.T, id string, size int64) {
	t.Helper()
	w.exec(t, `INSERT INTO attachments (id, organization_id, task_id, name, content_type, width, height, bytes,
		sha256, object_key, original_content_type, original_width, original_height, original_bytes, original_key)
		VALUES ($1, $2, $3, $1||'.png', 'image/png', 10, 10, $4, 'x', 'k/'||$1, 'image/png', 20, 20, $4, 'k/'||$1||'.o')`,
		id, w.org, w.task, size)
}

// showImages makes the task's goal show ids, attached to its text as the
// backend's save does.
func (w *feedbackWorld) showImages(t *testing.T, ids ...string) {
	t.Helper()
	goal := "Fix the cart."
	for _, id := range ids {
		goal += fmt.Sprintf(" ![%s](attachment:%s)", id, id)
	}
	w.exec(t, `UPDATE tasks SET goal = $2, acceptance_criteria = '[]'::jsonb WHERE id = $1`, w.task, goal)
	w.exec(t, `UPDATE attachments SET for_prompt = true, attached_at = now() WHERE task_id = $1 AND id = ANY($2)`, w.task, ids)
}

// previewDraft is a person's preview draft naming picture.
func (w *feedbackWorld) previewDraft(t *testing.T, id, body, picture string) {
	t.Helper()
	anchor, _ := json.Marshal(map[string]any{"kind": "preview", "url": "https://web.preview.example/cart",
		"viewport": map[string]int{"width": 1280, "height": 720}, "attachmentId": picture})
	w.exec(t, `INSERT INTO feedback_items (id, organization_id, task_id, attempt, author_person_id, body, anchor_kind, anchor)
		VALUES ($1, $2, $3, 1, $4, $5, 'preview', $6::jsonb)`, id, w.org, w.task, w.ana, body, string(anchor))
}

// A fix Run's prompt carries the task's images, then the pictures of the
// preview items it was given, in their order, in the room one message has
// left by count and bytes; a picture past the room is left out, a later one
// that fits is taken, and the words claim only the pictures sent.
func TestAFixersPicturesFollowTheTasksImagesWithinOneMessage(t *testing.T) {
	ids := func(n int) []string {
		var out []string
		for i := 1; i <= n; i++ {
			out = append(out, fmt.Sprintf("att_t%d", i))
		}
		return out
	}
	cases := []struct {
		name           string
		taskImages     []string
		taskBytes      int64
		picture1Bytes  int64
		want           []string
		wantPictureFor []string
	}{
		{name: "the task shows one image", taskImages: ids(1), taskBytes: 100, picture1Bytes: 100,
			want: []string{"att_t1", "att_p1", "att_p2"}, wantPictureFor: []string{"fbi_1", "fbi_2"}},
		{name: "room for one more", taskImages: ids(delivery.MaxAttachmentsPerMessage - 1), taskBytes: 100, picture1Bytes: 100,
			want: append(ids(delivery.MaxAttachmentsPerMessage-1), "att_p1"), wantPictureFor: []string{"fbi_1"}},
		{name: "the first picture past the bytes left", taskImages: ids(1), taskBytes: delivery.MaxMessageAttachmentBytes - 150,
			picture1Bytes: 200, want: []string{"att_t1", "att_p2"}, wantPictureFor: []string{"fbi_2"}},
		{name: "the task's images fill the message", taskImages: ids(delivery.MaxAttachmentsPerMessage), taskBytes: 100,
			picture1Bytes: 100, want: ids(delivery.MaxAttachmentsPerMessage)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newFeedbackWorld(t)
			w.delivery(t, "awaitReview", "policy", `["phase.finished"]`)
			for i, id := range c.taskImages {
				size := int64(100)
				if i == 0 {
					size = c.taskBytes
				}
				w.image(t, id, size)
			}
			w.showImages(t, c.taskImages...)
			w.image(t, "att_p1", c.picture1Bytes)
			w.image(t, "att_p2", 100)
			w.previewDraft(t, "fbi_1", "The Pay button is cut off.", "att_p1")
			w.previewDraft(t, "fbi_2", "The total wraps.", "att_p2")
			if _, err := w.submit(t, w.ana, "k", "fbi_1", "fbi_2"); err != nil {
				t.Fatal(err)
			}
			w.run(t, "run_fix", "ABS-1", "fix", "implementer", "pending")
			w.exec(t, `UPDATE runs SET feedback_item_ids = '{fbi_1,fbi_2}' WHERE id = 'run_fix'`)

			var sent []delivery.SentAttachment
			var items []delivery.FeedbackItem
			w.in(t, func(tx pgx.Tx) (err error) {
				if sent, err = delivery.PromptAttachments(context.Background(), tx, "run_fix"); err != nil {
					return err
				}
				items, err = delivery.RunFeedbackTx(context.Background(), tx, "run_fix")
				return err
			})
			got := make([]string, len(sent))
			for i, a := range sent {
				got[i] = a.ID
			}
			if !slices.Equal(got, c.want) {
				t.Fatalf("the prompt carries %v, want %v", got, c.want)
			}
			delivery.MarkPictures(items, sent)
			text := delivery.FeedbackText(items, true)
			for _, it := range items {
				line := "Picture: " + it.Anchor.AttachmentID + ".png, attached"
				if want := slices.Contains(c.wantPictureFor, it.ID); strings.Contains(text, line) != want {
					t.Errorf("%s: the words say %q: %v, want %v\n%s", it.ID, line, !want, want, text)
				}
			}
			if n := strings.Count(text, "Picture:"); n != len(c.wantPictureFor) {
				t.Errorf("%d Picture: lines, want %d\n%s", n, len(c.wantPictureFor), text)
			}
		})
	}
}
