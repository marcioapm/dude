package delivery

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/ids"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

// A person's feedback on a task (migration 127): items a person drafted
// privately in the backend and submitted together as a batch. This file is
// what happens to them once submitted — who hears of them first, what an
// agent is shown, how each is settled — and nothing here ever reads a
// draft: every query names status <> 'draft', so a draft's words and id
// reach no agent and no other person.
//
// Who hears first follows who drives the delivery:
//   - the conductor deciding: a wake reason (RecordWakeTx), delivered by
//     the syncer's wake sweep as every other; the items stay open until a
//     worker is given them, whatever the conductor decides;
//   - else an implementer or fixer of the current attempt still in its
//     turn: a directive at its next step (QueueDirective), the items'
//     words rendered by dude;
//   - else the delivery's own next step: the review loop and the pull
//     request gate count them as blocking, the pull request wait is
//     signalled (SignalHumanFeedback), work that ended ready to read starts
//     a fixer, a task never delivered is started.

// SignalHumanFeedback wakes a delivery waiting on its pull requests, or on
// a person reading its work, for feedback submitted or reopened.
const SignalHumanFeedback = "human.feedback"

// Ledger events: ids and counts only, never an item's words.
const (
	EvFeedbackSubmitted = "feedback.submitted"
	EvFeedbackDelivered = "feedback.delivered"
	EvFeedbackReplied   = "feedback.replied"
	EvFeedbackResolved  = "feedback.resolved"
	EvFeedbackReopened  = "feedback.reopened"
	EvFeedbackOutdated  = "feedback.outdated"
)

// Why dude reopened an agent's resolution (feedback.reopened's why).
const (
	// The Run ended (failed, aborted) without publishing.
	ReopenUnpublished = "unpublished"
	// The Run published, and the commit is not in it; or its candidate
	// round ended before the pull request took it.
	ReopenNotPublished = "not_published"
	// Its candidate's pull request merged or closed while it was checked.
	ReopenPullRequestEnded = "pull_request_ended"
)

// WakeFeedback is the conductor's reason for feedback (conductor_wakes).
const WakeFeedback = "feedback"

// Where a batch went (feedback_batches.routed_to).
const (
	RoutedConductor = "conductor"
	RoutedSteer     = "steer"
	RoutedWorkflow  = "workflow"
)

// feedbackBlocking (SQL, over feedback_items f): what blocks the pull
// request and the merge — open, given to an agent, or resolved by a commit
// not yet seen published.
const feedbackBlocking = `(f.status IN ('open', 'delivered') OR (f.status = 'resolved' AND f.resolved_verified_at IS NULL))`

// FeedbackError is a feedback request refused, with the API's code for it:
// bad_request, not_found, conflict.
type FeedbackError struct{ Kind, Msg string }

func (e FeedbackError) Error() string { return e.Msg }

func feedbackErr(kind, format string, a ...any) error {
	return FeedbackError{kind, fmt.Sprintf(format, a...)}
}

var errNotAPerson = FeedbackError{"forbidden", "feedback is a person's: sign in as one"}

// FeedbackAnchor is where an item points, as the backend built it.
type FeedbackAnchor struct {
	Kind      string `json:"kind"`
	RunID     string `json:"runId,omitempty"`
	Repo      string `json:"repo,omitempty"`
	Path      string `json:"path,omitempty"`
	Side      string `json:"side,omitempty"`
	StartLine int    `json:"startLine,omitempty"`
	EndLine   int    `json:"endLine,omitempty"`
	BaseSHA   string `json:"baseSha,omitempty"`
	HeadSHA   string `json:"headSha,omitempty"`
	Excerpt   []struct {
		Kind string `json:"kind"`
		Old  *int   `json:"old"`
		New  *int   `json:"new"`
		Text string `json:"text"`
	} `json:"excerpt,omitempty"`
	URL      string `json:"url,omitempty"`
	Selector string `json:"selector,omitempty"`
	Text     string `json:"text,omitempty"`
	Viewport *struct {
		Width  int `json:"width"`
		Height int `json:"height"`
	} `json:"viewport,omitempty"`
	// The element's box in the window, in CSS pixels.
	Rect *struct {
		X      float64 `json:"x"`
		Y      float64 `json:"y"`
		Width  float64 `json:"width"`
		Height float64 `json:"height"`
	} `json:"rect,omitempty"`
	AttachmentID string `json:"attachmentId,omitempty"`
	Component    *struct {
		Name string `json:"name,omitempty"`
		File string `json:"file,omitempty"`
		Line int    `json:"line,omitempty"`
	} `json:"component,omitempty"`
}

// Where is the anchor on one line: "api/src/vat.ts:11-12 (new)", a page, or "".
func (a FeedbackAnchor) Where() string {
	switch a.Kind {
	case "code":
		loc := a.Path
		if a.Repo != "" {
			loc = a.Repo + "/" + a.Path
		}
		lines := fmt.Sprint(a.StartLine)
		if a.EndLine != a.StartLine {
			lines += fmt.Sprintf("-%d", a.EndLine)
		}
		return fmt.Sprintf("%s:%s (%s side)", loc, lines, a.Side)
	case "preview":
		where := a.URL
		if a.Selector != "" {
			where += " · " + a.Selector
		}
		return where
	}
	return ""
}

// PageLine is where on its page a preview item was made: "Route /cart in
// a 1280×720 window · the element is 92×38 at 152, 431". Each clause only
// with its data; "" without a window or a box, as the URL is already said.
func (a FeedbackAnchor) PageLine() string {
	if a.Kind != "preview" || (a.Viewport == nil && a.Rect == nil) {
		return ""
	}
	route := "/"
	if u, err := url.Parse(a.URL); err == nil && u.EscapedPath() != "" {
		route = u.EscapedPath()
	}
	line := "Route " + route
	if v := a.Viewport; v != nil {
		line += fmt.Sprintf(" in a %d×%d window", v.Width, v.Height)
	}
	if r := a.Rect; r != nil {
		line += fmt.Sprintf(" · the element is %d×%d at %d, %d", px(r.Width), px(r.Height), px(r.X), px(r.Y))
	}
	return line
}

func px(v float64) int { return int(math.Round(v)) }

// pictureID is the upload a preview item's anchor names as its picture;
// "" for none and for any other kind of item.
func (a FeedbackAnchor) pictureID() string {
	if a.Kind != "preview" {
		return ""
	}
	return a.AttachmentID
}

// FeedbackItem is a submitted item as an agent is shown it: the person's
// own words, never a summary.
type FeedbackItem struct {
	ID       string         `json:"id"`
	Author   string         `json:"author"`
	Body     string         `json:"body"`
	Anchor   FeedbackAnchor `json:"anchor"`
	Status   string         `json:"status"`
	Attempt  int            `json:"attempt"`
	Outdated bool           `json:"outdated,omitempty"`
	// Fixer Runs it was sent to.
	FixAttempts int `json:"fixAttempts"`
	// A commit resolution not yet seen published.
	Pending bool                `json:"resolutionPending,omitempty"`
	Commit  string              `json:"commit,omitempty"`
	History []FeedbackHistoryAt `json:"history,omitempty"`
	// The name of its picture when the message carrying the item carries
	// the picture too (MarkPictures); "" otherwise, and never read from
	// the anchor alone.
	Picture string `json:"-"`
}

// FeedbackHistoryAt is one reply, resolution or reopening.
type FeedbackHistoryAt struct {
	Kind   string `json:"kind"`
	By     string `json:"by"`
	Body   string `json:"body,omitempty"`
	Commit string `json:"commit,omitempty"`
}

const itemColumns = `f.id, COALESCE((SELECT p.name FROM people p WHERE p.id = f.author_person_id), 'a person'), f.body, f.anchor,
	f.status, f.attempt, f.outdated_at IS NOT NULL, f.fix_attempts,
	f.status = 'resolved' AND f.resolved_verified_at IS NULL, COALESCE(f.resolved_commit, '')`

func scanItems(rows pgx.Rows) ([]FeedbackItem, error) {
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (FeedbackItem, error) {
		var it FeedbackItem
		var anchor []byte
		err := r.Scan(&it.ID, &it.Author, &it.Body, &anchor, &it.Status, &it.Attempt, &it.Outdated, &it.FixAttempts,
			&it.Pending, &it.Commit)
		if err == nil {
			_ = json.Unmarshal(anchor, &it.Anchor)
		}
		return it, err
	})
}

// FeedbackItemsTx reads submitted items of the task by id, in the order
// asked; ids that are not the task's submitted items are left out.
func FeedbackItemsTx(ctx context.Context, tx pgx.Tx, taskID string, itemIDs []string) ([]FeedbackItem, error) {
	if len(itemIDs) == 0 {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `SELECT `+itemColumns+` FROM feedback_items f
		WHERE f.task_id = $1 AND f.id = ANY($2) AND f.status <> 'draft'
		ORDER BY array_position($2, f.id)`, taskID, itemIDs)
	if err != nil {
		return nil, err
	}
	return scanItems(rows)
}

// OpenFeedbackTx is the attempt's items still to be handled — open, or
// given to an agent that has not settled them — oldest first.
func OpenFeedbackTx(ctx context.Context, tx pgx.Tx, taskID string, attempt int) ([]FeedbackItem, error) {
	rows, err := tx.Query(ctx, `SELECT `+itemColumns+` FROM feedback_items f WHERE `+openItems+`
		ORDER BY f.submitted_at, f.id`, taskID, attempt)
	if err != nil {
		return nil, err
	}
	return scanItems(rows)
}

// attemptOr (SQL, $1 the task, $2 an attempt or 0): $2, or the task's current.
const attemptOr = `COALESCE(NULLIF($2::int, 0), task_attempt($1))`

// openItems (SQL, over feedback_items f, $1 the task, $2 an attempt or 0):
// the attempt's items still to be handled — open, or given to an agent.
const openItems = `f.task_id = $1 AND f.attempt = ` + attemptOr + ` AND f.status IN ('open', 'delivered')`

// BlockingFeedbackTx counts the attempt's items that hold the pull
// request and the merge (feedbackBlocking).
func BlockingFeedbackTx(ctx context.Context, tx pgx.Tx, taskID string, attempt int) (int, error) {
	var n int
	err := tx.QueryRow(ctx, `SELECT count(*) FROM feedback_items f
		WHERE f.task_id = $1 AND f.attempt = `+attemptOr+` AND `+feedbackBlocking, taskID, attempt).Scan(&n)
	return n, err
}

// FeedbackText is items as an agent is given them: each one's id, author,
// anchor and the person's words verbatim, then what to do with them.
func FeedbackText(items []FeedbackItem, cli bool) string {
	return render(feedbackTmpl, struct {
		Items []FeedbackItem
		CLI   bool
	}{items, cli})
}

func quote(s string) string {
	return "> " + strings.ReplaceAll(strings.TrimSpace(s), "\n", "\n> ")
}

// shaPattern is a commit as an agent names it: 7 to 40 hex digits.
var shaPattern = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

// RunFeedbackTx is what a phase Run was given (runs.feedback_item_ids),
// as it reads now: the same items and words on a restart.
func RunFeedbackTx(ctx context.Context, tx pgx.Tx, runID string) ([]FeedbackItem, error) {
	var taskID string
	var itemIDs []string
	if err := tx.QueryRow(ctx, `SELECT COALESCE(task_id, ''), feedback_item_ids FROM runs WHERE id = $1`, runID).
		Scan(&taskID, &itemIDs); err != nil {
		return nil, err
	}
	return FeedbackItemsTx(ctx, tx, taskID, itemIDs)
}

// ---- submitting -------------------------------------------------------

// Submission is one person submitting drafts as a batch.
type Submission struct {
	Org, TaskID, Person, Key string
	DraftIDs                 []string
	Writer                   Writer
}

// Submitted is the batch made (or found again for the same key).
type Submitted struct {
	BatchID  string   `json:"batchId"`
	ItemIDs  []string `json:"itemIds"`
	RoutedTo string   `json:"routedTo"`
	RunID    string   `json:"runId,omitempty"`
	// Why nobody works on it yet, when nobody does.
	Waiting string `json:"waiting,omitempty"`
	// A submit with this key was taken before: this is that batch.
	Repeated bool `json:"repeated,omitempty"`
}

// SubmitFeedbackTx moves the person's drafts into one batch and routes
// it, all in tx: every draft id must be the person's own, on the task's
// current attempt, or nothing moves. The same key again returns the same
// batch and routes nothing twice. Locks, in the order Chat takes them: the
// task's Chat lock, its delivery, the task, then the Run told.
func SubmitFeedbackTx(ctx context.Context, tx pgx.Tx, rt Starter, in Submission) (Submitted, error) {
	if in.Person == "" {
		return Submitted{}, errNotAPerson
	}
	if strings.TrimSpace(in.Key) == "" || len(in.DraftIDs) == 0 {
		return Submitted{}, feedbackErr("bad_request", "%s", mDraftidsAndSubmissionkeyAreRequired)
	}
	if err := LockChat(ctx, tx, in.TaskID); err != nil {
		return Submitted{}, err
	}
	d, err := LoadDelivery(ctx, tx, in.TaskID)
	if err != nil {
		return Submitted{}, err
	}
	projectID, status, err := lockTask(ctx, tx, in.TaskID)
	if err != nil {
		return Submitted{}, feedbackErr("not_found", mTaskNotFound, in.TaskID)
	}
	if out, found, err := repeatedSubmit(ctx, tx, in); found || err != nil {
		return out, err
	}
	if err := RefuseLifecycleTx(ctx, tx, in.TaskID); IsLifecycleRefusal(err) {
		// Closed or archived (path 12): the rule's words, not a recovery's.
		return Submitted{}, feedbackErr("conflict", "%s Your drafts are kept.", err.Error())
	} else if err != nil {
		return Submitted{}, err
	}
	if err := refuseEnded(status); err != nil {
		return Submitted{}, err
	}
	var attempt int
	if err := tx.QueryRow(ctx, `SELECT task_attempt($1)`, in.TaskID).Scan(&attempt); err != nil {
		return Submitted{}, err
	}
	rows, err := tx.Query(ctx, `SELECT id FROM feedback_items
		WHERE id = ANY($1) AND task_id = $2 AND author_person_id = $3 AND status = 'draft' AND attempt = $4
		ORDER BY created_at, id FOR UPDATE`, in.DraftIDs, in.TaskID, in.Person, attempt)
	if err != nil {
		return Submitted{}, err
	}
	mine, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return Submitted{}, err
	}
	if len(mine) != len(in.DraftIDs) {
		// Which ones is not said: another person's draft is not told to exist.
		return Submitted{}, feedbackErr("bad_request", mDraftsAreNotYourDraftsOn,
			len(in.DraftIDs)-len(mine), len(in.DraftIDs))
	}
	batchID := ids.New(ids.FeedbackBatch)
	if _, err := tx.Exec(ctx, `INSERT INTO feedback_batches (id, organization_id, task_id, attempt, author_person_id, submission_key)
		VALUES ($1, $2, $3, $4, $5, $6)`, batchID, in.Org, in.TaskID, attempt, in.Person, in.Key); err != nil {
		return Submitted{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE feedback_items SET status = 'open', batch_id = $2, submitted_at = now(), updated_at = now()
		WHERE id = ANY($1)`, mine, batchID); err != nil {
		return Submitted{}, err
	}
	out := Submitted{BatchID: batchID, ItemIDs: mine}
	ref := RunRef{Org: in.Org, ProjectID: projectID, TaskID: in.TaskID}
	if err := routeFeedbackTx(ctx, tx, rt, ref, d, status, attempt, mine, "feedback:"+batchID, in.Writer, &out); err != nil {
		return Submitted{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE feedback_batches SET routed_to = $2, routed_run_id = NULLIF($3, '') WHERE id = $1`,
		batchID, out.RoutedTo, out.RunID); err != nil {
		return Submitted{}, err
	}
	payload := map[string]any{"batchId": batchID, "itemIds": mine, "routedTo": out.RoutedTo, "attempt": attempt}
	if out.RunID != "" {
		payload["runId"] = out.RunID
	}
	if out.Waiting != "" {
		payload["waiting"] = out.Waiting
	}
	return out, feedbackEvent(ctx, tx, ref, EvFeedbackSubmitted, in.Writer, payload)
}

// repeatedSubmit finds the batch an earlier submit with the same key made:
// the same drafts are that batch again; others are a different submit
// under a key already taken, refused.
func repeatedSubmit(ctx context.Context, tx pgx.Tx, in Submission) (Submitted, bool, error) {
	var out Submitted
	err := tx.QueryRow(ctx, `SELECT b.id, COALESCE(b.routed_to, ''), COALESCE(b.routed_run_id, ''),
			COALESCE((SELECT array_agg(f.id ORDER BY f.created_at, f.id) FROM feedback_items f WHERE f.batch_id = b.id), '{}')
		FROM feedback_batches b WHERE b.task_id = $1 AND b.author_person_id = $2 AND b.submission_key = $3`,
		in.TaskID, in.Person, in.Key).Scan(&out.BatchID, &out.RoutedTo, &out.RunID, &out.ItemIDs)
	if db.IsNotFound(err) {
		return out, false, nil
	}
	if err != nil {
		return out, true, err
	}
	if !slices.Equal(slices.Sorted(slices.Values(out.ItemIDs)), slices.Sorted(slices.Values(in.DraftIDs))) {
		return Submitted{}, true, feedbackErr("conflict", mSubmissionKeyWasUsedForAnother, in.Key)
	}
	out.Repeated = true
	return out, true, nil
}

// refuseEnded refuses feedback on a task that will not take it: a done
// task, or one stopped, which is recovered first. The drafts are kept.
func refuseEnded(status string) error {
	switch status {
	case "done":
		return feedbackErr("conflict", "%s", mThisTaskDoneFeedbackOnGoes)
	case "failed", "aborted":
		return feedbackErr("conflict", mThisTaskRecoverFirstThenSubmit, status)
	}
	return nil
}

// workingRunSQL ($1 the task, $2 the attempt): its implementer or fixer of
// the attempt that can still hear a directive in its turn — live, not
// aborting, its turn not over (a turn that ended is publishing, and would
// never read it). One waiting on a person's answer is working: it hears
// the feedback with the answer, which it is not.
const workingRunSQL = `SELECT r.id FROM runs r WHERE r.task_id = $1 AND r.kind = 'agent' AND r.attempt = $2
	AND r.phase IN ('implement', 'fix') AND ` + liveRun + `
	AND r.control <> 'abort' AND r.push_request_id IS NULL AND r.turn_done_at IS NULL
	ORDER BY r.created_at DESC LIMIT 1 FOR NO KEY UPDATE`

// routeFeedbackTx tells whoever drives the task of items: the conductor,
// a working implementer or fixer, or the delivery's own next step.
func routeFeedbackTx(ctx context.Context, tx pgx.Tx, rt Starter, ref RunRef, d *Delivery, status string, attempt int,
	itemIDs []string, key string, w Writer, out *Submitted) error {
	signal := func() error {
		return workflow.SignalTx(ctx, tx, ref.Org, d.WorkflowID, SignalHumanFeedback, map[string]any{"itemIds": itemIDs}, key)
	}
	if d != nil && d.Live() && d.State.conducted() {
		out.RoutedTo = RoutedConductor
		if _, err := RecordWakeTx(ctx, tx, ref.Org, ref.TaskID, WakeFeedback, key, render(wakeFeedbackTmpl, itemIDs)); err != nil {
			return err
		}
		// Waiting on its pull requests or its reader, the delivery parks at
		// the conductor's decision on them, as pull request feedback does;
		// any other step reads the items again when it waits there.
		return signal()
	}
	var runID string
	err := tx.QueryRow(ctx, workingRunSQL, ref.TaskID, attempt).Scan(&runID)
	if err != nil && !db.IsNotFound(err) {
		return err
	}
	if runID != "" {
		out.RoutedTo, out.RunID = RoutedSteer, runID
		if err := steerFeedbackTx(ctx, tx, RunRef{Org: ref.Org, ProjectID: ref.ProjectID, TaskID: ref.TaskID, RunID: runID}, itemIDs); err != nil {
			return err
		}
		// Left unhandled by that Run, the items are open again when it
		// ends: a wait on the pull requests after it reads them then.
		if d != nil && d.Live() {
			return signal()
		}
		return nil
	}
	out.RoutedTo = RoutedWorkflow
	switch {
	case d == nil:
		// Nothing delivered yet: the submit starts it, as Deliver would,
		// blockers and all. Blocked, the items wait for its first Run.
		return startForFeedbackTx(ctx, tx, rt, ref, w, out)
	case !d.Live():
		if status == "review" && len(d.State.PullRequestIDs) == 0 {
			// Work that ended ready to read before it waited on a person
			// reading it: its delivery takes the feedback up again.
			return reviveForFeedbackTx(ctx, tx, ref, d)
		}
		out.Waiting = "the delivery has ended"
		return feedbackErr("conflict", "%s", mThisTaskDeliveryHasEndedStart)
	}
	// Every live delivery is signalled: only the waits on the pull
	// requests or the reader take it, each reading the items again, so one
	// submitted while the pull request is being opened is heard once it waits.
	if err := signal(); err != nil {
		return err
	}
	switch d.Step {
	case "awaitPullRequest", "awaitRead":
	case "decide":
		out.Waiting = "a person decides on the escalation first"
	case "implementRun":
		out.Waiting = "the task waits on its blockers"
	default:
		out.Waiting = "the delivery's next fix takes them up"
	}
	return nil
}

// steerFeedbackTx queues items for a working Run as a directive at its
// next step, never interrupting, and records them as given to it: a
// restart of the Run is given them again. Each item stays open until the
// agent reads the directive (FeedbackHeardTx).
func steerFeedbackTx(ctx context.Context, tx pgx.Tx, ref RunRef, itemIDs []string) error {
	items, err := FeedbackItemsTx(ctx, tx, ref.TaskID, itemIDs)
	if err != nil {
		return err
	}
	// Only a picture no message has carried yet can go with this one
	// (Attach): one already sent with an earlier steer reaches the next
	// fixer with its prompt instead (PromptAttachments).
	pictures, err := feedbackPicturesTx(ctx, tx, ref.TaskID, items, true, MaxAttachmentsPerMessage, MaxMessageAttachmentBytes)
	if err != nil {
		return err
	}
	MarkPictures(items, pictures)
	directiveID, _, err := QueueDirective(ctx, tx, ref, Directive{Text: FeedbackText(items, true), Scope: "run"})
	if err != nil {
		return err
	}
	ids := make([]string, len(pictures))
	for i, p := range pictures {
		ids[i] = p.ID
	}
	if _, err := Attach(ctx, tx, ref.TaskID, directiveID, ids); err != nil {
		return err
	}
	return recordGivenTx(ctx, tx, ref.RunID, directiveID, itemIDs)
}

// feedbackPicturesTx are the pictures of the preview items among items
// that one message can carry: each the task's own upload, in the items'
// order, at most room images and roomBytes bytes together; with unsent,
// only those no message carries yet. A picture past the room, or gone
// (swept, removed), is left out, and its item is sent without it.
func feedbackPicturesTx(ctx context.Context, tx pgx.Tx, taskID string, items []FeedbackItem, unsent bool, room int, roomBytes int64) ([]SentAttachment, error) {
	var ids []string
	for _, it := range items {
		if id := it.Anchor.pictureID(); id != "" && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 || room <= 0 {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `SELECT a.id, a.name, a.content_type, a.object_key, a.bytes
		FROM unnest($2::text[]) WITH ORDINALITY AS ref(id, n) JOIN attachments a ON a.id = ref.id
		WHERE a.task_id = $1 AND (NOT $3 OR a.attached_at IS NULL) ORDER BY ref.n`, taskID, ids, unsent)
	if err != nil {
		return nil, err
	}
	found, err := pgx.CollectRows(rows, pgx.RowToStructByPos[SentAttachment])
	if err != nil {
		return nil, err
	}
	var out []SentAttachment
	for _, a := range found {
		if len(out) == room || a.Bytes > roomBytes {
			continue
		}
		roomBytes -= a.Bytes
		out = append(out, a)
	}
	return out, nil
}

// MarkPictures names on each preview item the picture sent with it, of
// those a message carries: what FeedbackText says is attached.
func MarkPictures(items []FeedbackItem, sent []SentAttachment) {
	for i := range items {
		items[i].Picture = ""
		id := items[i].Anchor.pictureID()
		if id == "" {
			continue
		}
		for _, a := range sent {
			if a.ID == id {
				items[i].Picture = a.Name
			}
		}
	}
}

// recordGivenTx records items as carried to Run runID by a directive, and
// part of what the Run was given (a restart is given them again).
func recordGivenTx(ctx context.Context, tx pgx.Tx, runID, directiveID string, itemIDs []string) error {
	if _, err := tx.Exec(ctx, `UPDATE feedback_items SET delivered_run_id = $2, directive_id = $3, updated_at = now()
		WHERE id = ANY($1)`, itemIDs, runID, directiveID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE runs SET feedback_item_ids = feedback_item_ids || ARRAY(
			SELECT unnest($2::text[]) EXCEPT SELECT unnest(feedback_item_ids)) WHERE id = $1`, runID, itemIDs)
	return err
}

// startForFeedbackTx starts a task never delivered, as Deliver would
// (StartTx, its blocker guard included), in a savepoint: refused as
// blocked, the batch stands and waits for the task's first Run.
func startForFeedbackTx(ctx context.Context, tx pgx.Tx, rt Starter, ref RunRef, w Writer, out *Submitted) error {
	sp, err := tx.Begin(ctx)
	if err != nil {
		return err
	}
	if _, err := StartTx(ctx, sp, rt, StartInput{Org: ref.Org, TaskID: ref.TaskID, How: HowDeliver, Writer: w}); err != nil {
		_ = sp.Rollback(ctx)
		if blocked, ok := AsBlocked(err); ok {
			out.Waiting = blocked.Error() + " Its first implementer is given them once it starts."
			return nil
		}
		return err
	}
	return sp.Commit(ctx)
}

// reviveForFeedbackTx takes up the delivery of work that ended ready to
// read with no pull request, from before it waited on its reader: back at
// its fix step, as a person's retry would, with a fresh loop budget.
func reviveForFeedbackTx(ctx context.Context, tx pgx.Tx, ref RunRef, d *Delivery) error {
	st := d.State
	st.Iteration, st.PendingRunIDs, st.Retries = 0, nil, st.Retries+1
	next, err := json.Marshal(st)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE workflow_runs SET state = $2::jsonb, status = 'running', step = 'fix',
			awaiting_signals = '[]'::jsonb, wake_at = NULL, last_error = NULL, attempt = 0, locked_by = NULL, locked_until = NULL
		WHERE id = $1`, d.WorkflowID, next); err != nil {
		return err
	}
	_, err = SetTaskStatusTx(ctx, tx, ref.Org, ref.ProjectID, ref.TaskID, "review", "running", "a person gave feedback on it")
	return err
}

// ---- receipts ---------------------------------------------------------

// giveFeedbackTx records items given to a new phase Run in its prompt:
// delivered to it (its prompt is what it reads first), and, for a fix, one
// more fix attempt each — the bound a finding's attempts are. Only the
// attempt's items still open or with another Run are given.
func giveFeedbackTx(ctx context.Context, tx pgx.Tx, ref RunRef, attempt int, itemIDs []string, count bool) error {
	rows, err := tx.Query(ctx, `UPDATE feedback_items SET status = 'delivered', delivered_run_id = $2, directive_id = NULL,
			fix_attempts = fix_attempts + CASE WHEN $4 THEN 1 ELSE 0 END, updated_at = now()
		WHERE id = ANY($1) AND task_id = $3 AND attempt = $5 AND status IN ('open', 'delivered') RETURNING id`,
		itemIDs, ref.RunID, ref.TaskID, count, attempt)
	if err != nil {
		return err
	}
	given, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil || len(given) == 0 {
		return err
	}
	_, err = ledger.Append(ctx, tx, ref.Event(EvFeedbackDelivered, ledger.ActorSystem,
		map[string]any{"itemIds": given, "how": "prompt"}))
	return err
}

// OpenFeedbackIDs is the attempt's items still to be handled, for the
// next implement or fix Run's prompt.
func (s *Store) OpenFeedbackIDs(ctx context.Context, org string, st *State) ([]string, error) {
	var out []string
	err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id FROM feedback_items f WHERE `+openItems+` ORDER BY f.submitted_at, f.id`,
			st.TaskID, st.Attempt)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	})
	return out, err
}

// FeedbackStates is the attempt's feedback as the review loop weighs it:
// each still to be handled blocks whatever the policy's severities.
func (s *Store) FeedbackStates(ctx context.Context, org string, st *State) ([]FindingState, error) {
	var out []FindingState
	err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id, fix_attempts FROM feedback_items f WHERE `+openItems+` ORDER BY f.submitted_at, f.id`,
			st.TaskID, st.Attempt)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (FindingState, error) {
			f := FindingState{Status: "open", Human: true}
			return f, r.Scan(&f.ID, &f.FixAttempts)
		})
		return err
	})
	return out, err
}

// resolvedByPerson (SQL SET list, the person's column set after it): an
// item a person resolved, verified at once and with no commit.
const resolvedByPerson = `status = 'resolved', resolved_at = now(), resolved_verified_at = now(), resolved_commit = NULL,
	resolved_by_run_id = NULL, updated_at = now()`

// AcceptFeedbackTx is a person deciding the task ships with the items a
// stuck escalation named: resolved by that person, each.
func AcceptFeedbackTx(ctx context.Context, tx pgx.Tx, org, taskID, person string, itemIDs []string) error {
	if len(itemIDs) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, `UPDATE feedback_items SET `+resolvedByPerson+`, resolved_by_person_id = NULLIF($3, '')
		WHERE task_id = $1 AND id = ANY($2) AND status IN ('open', 'delivered') RETURNING id`, taskID, itemIDs, person)
	if err != nil {
		return err
	}
	accepted, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	ref := RunRef{Org: org, TaskID: taskID}
	for _, id := range accepted {
		if person != "" {
			if _, err := historyTx(ctx, tx, org, id, "resolve", person, "", "accepted at the escalation", ""); err != nil {
				return err
			}
		}
		if _, err := ledger.Append(ctx, tx, ref.Event(EvFeedbackResolved, ledger.ActorSystem,
			map[string]any{"itemId": id, "by": "person", "accepted": true})); err != nil {
			return err
		}
	}
	return nil
}

// FeedbackHeardTx records that the agent read the directive that carried
// items: they are delivered to its Run.
func FeedbackHeardTx(ctx context.Context, tx pgx.Tx, org, directiveID string) error {
	rows, err := tx.Query(ctx, `UPDATE feedback_items SET status = 'delivered', updated_at = now()
		WHERE directive_id = $1 AND status = 'open' RETURNING id, task_id, delivered_run_id`, directiveID)
	if err != nil {
		return err
	}
	type heard struct{ ID, TaskID, RunID string }
	got, err := pgx.CollectRows(rows, pgx.RowToStructByPos[heard])
	if err != nil || len(got) == 0 {
		return err
	}
	itemIDs := make([]string, len(got))
	for i, h := range got {
		itemIDs[i] = h.ID
	}
	var projectID string
	if err := tx.QueryRow(ctx, `SELECT project_id FROM tasks WHERE id = $1`, got[0].TaskID).Scan(&projectID); err != nil {
		return err
	}
	ref := RunRef{Org: org, ProjectID: projectID, TaskID: got[0].TaskID, RunID: got[0].RunID}
	_, err = ledger.Append(ctx, tx, ref.Event(EvFeedbackDelivered, ledger.ActorSystem,
		map[string]any{"itemIds": itemIDs, "directiveId": directiveID}))
	return err
}

// FeedbackUnheardTx puts items back to open when the directive carrying
// them failed unread: the next fixer is given them.
func FeedbackUnheardTx(ctx context.Context, tx pgx.Tx, directiveID string) error {
	_, err := tx.Exec(ctx, `UPDATE feedback_items SET delivered_run_id = NULL, directive_id = NULL, updated_at = now()
		WHERE directive_id = $1 AND status = 'open'`, directiveID)
	return err
}

// FeedbackRunEndedTx puts back to open what a Run ending was given and did
// not settle, so the next fixer is given it; on a Run that failed or was
// aborted, what it resolved with a commit not yet published is reopened
// too, since that commit will not be.
func FeedbackRunEndedTx(ctx context.Context, tx pgx.Tx, runID string, published bool) error {
	if _, err := tx.Exec(ctx, `UPDATE feedback_items SET status = 'open', delivered_run_id = NULL, directive_id = NULL,
			updated_at = now()
		WHERE delivered_run_id = $1 AND status IN ('open', 'delivered')`, runID); err != nil {
		return err
	}
	if published {
		return nil
	}
	return reopenUnpublishedTx(ctx, tx, runID)
}

// reopenAgentResolution (SQL SET list): an agent's commit resolution, or
// what an agent was given, put back to open for the next fixer.
const reopenAgentResolution = `status = 'open', resolved_commit = NULL, resolved_repo = NULL,
	resolved_by_run_id = NULL, resolved_at = NULL, delivered_run_id = NULL, directive_id = NULL, updated_at = now()`

func reopenUnpublishedTx(ctx context.Context, tx pgx.Tx, runID string) error {
	rows, err := tx.Query(ctx, `UPDATE feedback_items SET `+reopenAgentResolution+`
		WHERE resolved_by_run_id = $1 AND status = 'resolved' AND resolved_verified_at IS NULL
		RETURNING id, organization_id, task_id`, runID)
	if err != nil {
		return err
	}
	type reopened struct{ ID, Org, TaskID string }
	got, err := pgx.CollectRows(rows, pgx.RowToStructByPos[reopened])
	if err != nil {
		return err
	}
	for _, r := range got {
		if _, err := historyTx(ctx, tx, r.Org, r.ID, "reopen", "", runID, "its commit was never published", ""); err != nil {
			return err
		}
		ref := RunRef{Org: r.Org, TaskID: r.TaskID, RunID: runID}
		if _, err := ledger.Append(ctx, tx, ref.Event(EvFeedbackReopened, ledger.ActorSystem,
			map[string]any{"itemId": r.ID, "why": ReopenUnpublished})); err != nil {
			return err
		}
	}
	return nil
}

// ---- a person settling an item -----------------------------------------

// PersonAction is a person replying to, resolving or reopening an item.
type PersonAction struct {
	Org, ItemID, Person string
	Body                string
	Writer              Writer
}

type lockedItem struct {
	TaskID, ProjectID, Status, TaskStatus string
	Attempt, Current                      int
	// Resolved by a commit not yet seen published.
	Pending bool
	d       *Delivery
}

// lockItem locks a submitted item for a change, after its task's Chat lock,
// delivery and row (SubmitFeedbackTx's order), refusing a draft (not
// found: a draft is its author's alone, and not changed here) or an
// earlier attempt's.
func lockItem(ctx context.Context, tx pgx.Tx, itemID string) (lockedItem, error) {
	var it lockedItem
	err := tx.QueryRow(ctx, `SELECT task_id FROM feedback_items WHERE id = $1 AND status <> 'draft'`, itemID).Scan(&it.TaskID)
	if db.IsNotFound(err) {
		return it, feedbackErr("not_found", mFeedbackNotFound, itemID)
	}
	if err != nil {
		return it, err
	}
	if err := LockChat(ctx, tx, it.TaskID); err != nil {
		return it, err
	}
	if it.d, err = LoadDelivery(ctx, tx, it.TaskID); err != nil {
		return it, err
	}
	if it.ProjectID, it.TaskStatus, err = lockTask(ctx, tx, it.TaskID); err != nil {
		return it, err
	}
	if err := tx.QueryRow(ctx, `SELECT status, attempt, task_attempt(task_id), status = 'resolved' AND resolved_verified_at IS NULL
		FROM feedback_items WHERE id = $1 FOR UPDATE`, itemID).
		Scan(&it.Status, &it.Attempt, &it.Current, &it.Pending); err != nil {
		return it, err
	}
	if it.Attempt != it.Current {
		return it, feedbackErr("conflict", mFeedbackOnAttemptWhichReadOnly, itemID, it.Attempt)
	}
	return it, nil
}

func historyTx(ctx context.Context, tx pgx.Tx, org, itemID, kind, person, runID, body, commit string) (string, error) {
	id := ids.New(ids.FeedbackReply)
	_, err := tx.Exec(ctx, `INSERT INTO feedback_replies (id, organization_id, item_id, kind, person_id, run_id, body, commit_sha)
		VALUES ($1, $2, $3, $4, NULLIF($5, ''), NULLIF($6, ''), $7, NULLIF($8, ''))`,
		id, org, itemID, kind, person, runID, body, commit)
	return id, err
}

func feedbackEvent(ctx context.Context, tx pgx.Tx, ref RunRef, typ string, w Writer, payload map[string]any) error {
	actorType, actorID := w.ActorType, w.ActorID
	if actorType == "" {
		actorType, actorID = ledger.ActorSystem, "dude"
	}
	_, err := ledger.Append(ctx, tx, ledger.Event{Type: typ, OrganizationID: ref.Org, ProjectID: ref.ProjectID,
		TaskID: ref.TaskID, RunID: ref.RunID, ActorType: actorType, ActorID: actorID, Source: ledger.SourceOrchestrator,
		CorrelationID: ref.TaskID, Payload: payload})
	return err
}

// gateMoved tells a delivery waiting on its pull requests (or on its
// reader) that what holds them changed: it weighs readiness again.
func gateMoved(ctx context.Context, tx pgx.Tx, org string, d *Delivery, key string) error {
	if d == nil || !d.Live() || d.Step != "awaitPullRequest" {
		return nil
	}
	return workflow.SignalTx(ctx, tx, org, d.WorkflowID, SignalPRFeedback, map[string]any{"kind": "readiness"}, key)
}

// PersonReplyTx is a person's reply to an item: the conversation goes on,
// and the item no longer holds anything (replied).
func PersonReplyTx(ctx context.Context, tx pgx.Tx, in PersonAction) error {
	return personSettleTx(ctx, tx, in, "reply")
}

// PersonResolveTx is a person resolving an item: done with, at once.
func PersonResolveTx(ctx context.Context, tx pgx.Tx, in PersonAction) error {
	return personSettleTx(ctx, tx, in, "resolve")
}

func personSettleTx(ctx context.Context, tx pgx.Tx, in PersonAction, kind string) error {
	if in.Person == "" {
		return errNotAPerson
	}
	body := strings.TrimSpace(in.Body)
	if kind == "reply" && body == "" {
		return feedbackErr("bad_request", "%s", mReplyNeedsWords)
	}
	it, err := lockItem(ctx, tx, in.ItemID)
	if err != nil {
		return err
	}
	ref := RunRef{Org: in.Org, ProjectID: it.ProjectID, TaskID: it.TaskID}
	var ev string
	if kind == "reply" {
		if it.Pending {
			// A reply would stop a resolution not yet verified from holding
			// the gate, and its Run failing would no longer reopen it.
			return feedbackErr("conflict", mFeedbackResolvedByCommitNotYet, in.ItemID)
		}
		ev = EvFeedbackReplied
		if _, err := tx.Exec(ctx, `UPDATE feedback_items SET status = 'replied', updated_at = now() WHERE id = $1`, in.ItemID); err != nil {
			return err
		}
	} else {
		if it.Status == "resolved" {
			// A provisional resolution the person confirms holds now.
			if _, err := tx.Exec(ctx, `UPDATE feedback_items SET resolved_verified_at = COALESCE(resolved_verified_at, now()),
				resolved_by_person_id = $2, updated_at = now() WHERE id = $1`, in.ItemID, in.Person); err != nil {
				return err
			}
		} else if _, err := tx.Exec(ctx, `UPDATE feedback_items SET `+resolvedByPerson+`, resolved_by_person_id = $2
			WHERE id = $1`, in.ItemID, in.Person); err != nil {
			return err
		}
		ev = EvFeedbackResolved
	}
	hid, err := historyTx(ctx, tx, in.Org, in.ItemID, kind, in.Person, "", body, "")
	if err != nil {
		return err
	}
	if err := feedbackEvent(ctx, tx, ref, ev, in.Writer, map[string]any{"itemId": in.ItemID, "by": "person"}); err != nil {
		return err
	}
	return gateMoved(ctx, tx, in.Org, it.d, ev+":"+hid)
}

// PersonReopenTx is a person reopening an item a reply or resolution
// settled: open again, routed as a new submission is.
func PersonReopenTx(ctx context.Context, tx pgx.Tx, rt Starter, in PersonAction) (Submitted, error) {
	if in.Person == "" {
		return Submitted{}, errNotAPerson
	}
	it, err := lockItem(ctx, tx, in.ItemID)
	if err != nil {
		return Submitted{}, err
	}
	if it.Status != "resolved" && it.Status != "replied" {
		return Submitted{}, feedbackErr("conflict", mFeedbackOnlyResolvedOrRepliedItem, in.ItemID, it.Status)
	}
	if err := refuseEnded(it.TaskStatus); err != nil {
		return Submitted{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE feedback_items SET `+reopenAgentResolution+`, resolved_by_person_id = NULL,
			resolved_verified_at = NULL
		WHERE id = $1`, in.ItemID); err != nil {
		return Submitted{}, err
	}
	replyID, err := historyTx(ctx, tx, in.Org, in.ItemID, "reopen", in.Person, "", strings.TrimSpace(in.Body), "")
	if err != nil {
		return Submitted{}, err
	}
	ref := RunRef{Org: in.Org, ProjectID: it.ProjectID, TaskID: it.TaskID}
	out := Submitted{ItemIDs: []string{in.ItemID}}
	if err := routeFeedbackTx(ctx, tx, rt, ref, it.d, it.TaskStatus, it.Current, out.ItemIDs, "feedback:reopen:"+replyID,
		in.Writer, &out); err != nil {
		return Submitted{}, err
	}
	payload := map[string]any{"itemId": in.ItemID, "routedTo": out.RoutedTo}
	if out.RunID != "" {
		payload["runId"] = out.RunID
	}
	return out, feedbackEvent(ctx, tx, ref, EvFeedbackReopened, in.Writer, payload)
}

// ---- an agent settling an item ----------------------------------------

// AgentReplyTx is an agent's reply to an item of its task's current
// attempt, saying why it will not change it: the item is replied, which
// holds nothing; the person may reopen it.
func AgentReplyTx(ctx context.Context, tx pgx.Tx, ref RunRef, itemID, body string) error {
	body = strings.TrimSpace(body)
	if body == "" {
		return feedbackErr("bad_request", "%s", mSayWhyReplyNeedsWords)
	}
	it, err := agentItem(ctx, tx, ref, itemID)
	if err != nil {
		return err
	}
	if it.Status != "open" && it.Status != "delivered" {
		return feedbackErr("conflict", mFeedbackThereNothingReplyUntilPerson, itemID, it.Status)
	}
	if _, err := tx.Exec(ctx, `UPDATE feedback_items SET status = 'replied', updated_at = now() WHERE id = $1`, itemID); err != nil {
		return err
	}
	hid, err := historyTx(ctx, tx, ref.Org, itemID, "reply", "", ref.RunID, body, "")
	if err != nil {
		return err
	}
	if _, err := ledger.Append(ctx, tx, ref.Event(EvFeedbackReplied, ledger.ActorAgent, map[string]any{"itemId": itemID, "by": "agent"})); err != nil {
		return err
	}
	return gateMoved(ctx, tx, ref.Org, it.d, "feedback.replied:"+hid)
}

// agentItem locks an item of the agent's task and current attempt, the
// same refusal for any other: a draft, another task's, an earlier attempt's.
func agentItem(ctx context.Context, tx pgx.Tx, ref RunRef, itemID string) (lockedItem, error) {
	var taskID string
	err := tx.QueryRow(ctx, `SELECT task_id FROM feedback_items WHERE id = $1 AND status <> 'draft'`, itemID).Scan(&taskID)
	if db.IsNotFound(err) || err == nil && taskID != ref.TaskID {
		return lockedItem{}, feedbackErr("not_found", mFeedbackNotYourTask, itemID)
	}
	if err != nil {
		return lockedItem{}, err
	}
	return lockItem(ctx, tx, itemID)
}

// Resolution is an agent resolving an item with the commit that fixed it.
type Resolution struct {
	ItemID, Commit, Repo string
}

// AgentResolveTx resolves an item with the commit that fixed it, which
// must be a full or abbreviated SHA of the repository the item's anchor
// names (or, for an item with none, the one repository the task changes,
// or the one named). Whether the commit holds is decided as the Run's
// work is published (SettleResolutionsTx): until then the item still
// holds the pull request. A Run that ends without publishing reopens it.
func AgentResolveTx(ctx context.Context, tx pgx.Tx, ref RunRef, in Resolution) (string, error) {
	commit := strings.ToLower(strings.TrimSpace(in.Commit))
	if !shaPattern.MatchString(commit) {
		return "", feedbackErr("bad_request", mCommitNotCommitShaResolveWith, in.Commit)
	}
	it, err := agentItem(ctx, tx, ref, in.ItemID)
	if err != nil {
		return "", err
	}
	if it.Status != "open" && it.Status != "delivered" {
		return "", feedbackErr("conflict", mFeedbackAlready, in.ItemID, it.Status)
	}
	var phase string
	if err := tx.QueryRow(ctx, `SELECT COALESCE(phase::text, '') FROM runs WHERE id = $1`, ref.RunID).Scan(&phase); err != nil {
		return "", err
	}
	if !Publishes[phase] {
		return "", feedbackErr("forbidden", "%s", mOnlyImplementerOrFixerWhoseCommit)
	}
	repo, err := resolutionRepo(ctx, tx, ref.TaskID, in.ItemID, in.Repo)
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `UPDATE feedback_items SET status = 'resolved', resolved_commit = $2, resolved_repo = $3,
			resolved_by_run_id = $4, resolved_at = now(), resolved_verified_at = NULL, updated_at = now()
		WHERE id = $1`, in.ItemID, commit, repo, ref.RunID); err != nil {
		return "", err
	}
	if _, err := historyTx(ctx, tx, ref.Org, in.ItemID, "resolve", "", ref.RunID, "", commit); err != nil {
		return "", err
	}
	_, err = ledger.Append(ctx, tx, ref.Event(EvFeedbackResolved, ledger.ActorAgent,
		map[string]any{"itemId": in.ItemID, "by": "agent", "commit": commit, "repo": repo, "pending": true}))
	return repo, err
}

// resolutionRepo is the repository a commit resolving an item must be in:
// a code item's anchor's; for any other, the one named, else the task's
// only writable repository. Any other name is refused.
func resolutionRepo(ctx context.Context, tx pgx.Tx, taskID, itemID, named string) (string, error) {
	var kind, anchored string
	if err := tx.QueryRow(ctx, `SELECT anchor_kind, COALESCE(anchor->>'repo', '') FROM feedback_items WHERE id = $1`, itemID).
		Scan(&kind, &anchored); err != nil {
		return "", err
	}
	rows, err := tx.Query(ctx, `SELECT r.name FROM task_repositories tr JOIN repositories r ON r.id = tr.repository_id
		WHERE tr.task_id = $1 AND tr.access = 'write' ORDER BY r.name`, taskID)
	if err != nil {
		return "", err
	}
	repos, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return "", err
	}
	switch {
	case kind == "code" && anchored != "":
		if named != "" && named != anchored {
			return "", feedbackErr("bad_request", mFeedbackOnResolveWithCommitThere, itemID, anchored, named)
		}
		return anchored, nil
	case named != "":
		if !slices.Contains(repos, named) {
			return "", feedbackErr("bad_request", mNotRepositoryThisTaskChanges, named, strings.Join(repos, ", "))
		}
		return named, nil
	case len(repos) == 1:
		return repos[0], nil
	}
	return "", feedbackErr("bad_request", mThisTaskChangesNameRepositoryYour, strings.Join(repos, ", "))
}

// PendingResolution is an agent's resolution waiting on its Run's publish.
type PendingResolution struct{ ID, TaskID, Commit, Repo string }

// PendingResolutionsTx are the resolutions Run runID made with a commit
// not yet seen published.
func PendingResolutionsTx(ctx context.Context, tx pgx.Tx, runID string) ([]PendingResolution, error) {
	rows, err := tx.Query(ctx, `SELECT id, task_id, resolved_commit, COALESCE(resolved_repo, '') FROM feedback_items
		WHERE resolved_by_run_id = $1 AND status = 'resolved' AND resolved_verified_at IS NULL ORDER BY id`, runID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[PendingResolution])
}

// Comparer is GitHub's compare of two commits (forge.GitHub.Compare).
type Comparer func(ctx context.Context, repo, base, head string) (aheadBy, behindBy int, err error)

// ResolutionHolds says whether commit is the work of a Run that started
// at base and published head in its repository: in head (comparing
// commit as base with head, nothing of commit's is missing from head:
// behind_by 0) and not in base (comparing base with commit, commit has
// something base lacks: ahead_by > 0). A commit already on the branch
// before the Run, or one never pushed, does not hold. head "" (nothing
// published there) never holds.
func ResolutionHolds(ctx context.Context, cmp Comparer, repo, base, head, commit string) (bool, error) {
	if head == "" || commit == "" {
		return false, nil
	}
	if base != "" && strings.HasPrefix(base, commit) {
		return false, nil
	}
	if !strings.HasPrefix(head, commit) {
		if cmp == nil {
			return false, nil
		}
		_, behind, err := cmp(ctx, repo, commit, head)
		if err != nil || behind != 0 {
			return false, err
		}
	}
	if base == "" || cmp == nil {
		return base == "" || strings.HasPrefix(head, commit), nil
	}
	ahead, _, err := cmp(ctx, repo, base, commit)
	return err == nil && ahead > 0, err
}

// SettleResolutionsTx settles a published Run's pending resolutions by
// held (item → whether its commit holds, ResolutionHolds): verified, or
// open again for the next fixer. A resolution left out of held is
// reopened: its commit was not published.
func SettleResolutionsTx(ctx context.Context, tx pgx.Tx, org, runID string, held map[string]bool) error {
	return settleResolutionsTx(ctx, tx, org, runID, held, true)
}

// SettleCandidateResolutionsTx is SettleResolutionsTx for a Run that
// published to a candidate branch: what does not hold is reopened, and
// what holds stays pending (still blocking) until the pull request's
// branch reaches the candidate's head (CandidateMovedTx).
func SettleCandidateResolutionsTx(ctx context.Context, tx pgx.Tx, org, runID string, held map[string]bool) error {
	return settleResolutionsTx(ctx, tx, org, runID, held, false)
}

func settleResolutionsTx(ctx context.Context, tx pgx.Tx, org, runID string, held map[string]bool, verify bool) error {
	pending, err := PendingResolutionsTx(ctx, tx, runID)
	if err != nil {
		return err
	}
	for _, p := range pending {
		ref := RunRef{Org: org, TaskID: p.TaskID, RunID: runID}
		var err error
		switch {
		case !held[p.ID]:
			err = reopenResolutionTx(ctx, tx, ref, p.ID, ReopenNotPublished, "its commit is not in what was published")
		case verify:
			err = verifyResolutionTx(ctx, tx, ref, p.ID, p.Commit, p.Repo)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// verifyResolutionTx counts item's resolution by ref's Run, at commit in repo.
func verifyResolutionTx(ctx context.Context, tx pgx.Tx, ref RunRef, itemID, commit, repo string) error {
	if _, err := tx.Exec(ctx, `UPDATE feedback_items SET resolved_verified_at = now(), updated_at = now() WHERE id = $1`, itemID); err != nil {
		return err
	}
	_, err := ledger.Append(ctx, tx, ref.Event(EvFeedbackResolved, ledger.ActorSystem,
		map[string]any{"itemId": itemID, "commit": commit, "repo": repo, "verified": true}))
	return err
}

// reopenResolutionTx puts item's resolution by ref's Run back to open, with
// why (feedback.reopened) and body (the item's history).
func reopenResolutionTx(ctx context.Context, tx pgx.Tx, ref RunRef, itemID, why, body string) error {
	if _, err := tx.Exec(ctx, `UPDATE feedback_items SET `+reopenAgentResolution+` WHERE id = $1`, itemID); err != nil {
		return err
	}
	if _, err := historyTx(ctx, tx, ref.Org, itemID, "reopen", "", ref.RunID, body, ""); err != nil {
		return err
	}
	_, err := ledger.Append(ctx, tx, ref.Event(EvFeedbackReopened, ledger.ActorSystem,
		map[string]any{"itemId": itemID, "why": why}))
	return err
}

// ---- reading, for an agent ---------------------------------------------

// FeedbackList is the agent's view of its task's feedback.
type FeedbackList struct {
	Items   []FeedbackItem `json:"items"`
	Unknown []string       `json:"unknown,omitempty"`
}

// AgentFeedbackTx is the task's submitted feedback on the current attempt:
// open and delivered ones by default, or, with ids (at most 20), those in
// full with their history. Drafts are never anyone's here.
func AgentFeedbackTx(ctx context.Context, tx pgx.Tx, taskID string, itemIDs []string, all bool) (FeedbackList, error) {
	out := FeedbackList{Items: []FeedbackItem{}}
	var rows pgx.Rows
	var err error
	if len(itemIDs) > 0 {
		if len(itemIDs) > 20 {
			return out, feedbackErr("bad_request", "%s", mAtMost20IdsAtTime)
		}
		rows, err = tx.Query(ctx, `SELECT `+itemColumns+` FROM feedback_items f
			WHERE f.task_id = $1 AND f.id = ANY($2) AND f.status <> 'draft' AND f.attempt = task_attempt($1)
			ORDER BY array_position($2, f.id)`, taskID, itemIDs)
	} else {
		rows, err = tx.Query(ctx, `SELECT `+itemColumns+` FROM feedback_items f
			WHERE f.task_id = $1 AND f.status <> 'draft' AND f.attempt = task_attempt($1)
			  AND ($2 OR `+feedbackBlocking+`) ORDER BY f.submitted_at, f.id LIMIT 200`, taskID, all)
	}
	if err != nil {
		return out, err
	}
	if out.Items, err = scanItems(rows); err != nil {
		return out, err
	}
	for i := range out.Items {
		if out.Items[i].History, err = itemHistory(ctx, tx, out.Items[i].ID); err != nil {
			return out, err
		}
	}
	for _, id := range itemIDs {
		if !slices.ContainsFunc(out.Items, func(it FeedbackItem) bool { return it.ID == id }) {
			out.Unknown = append(out.Unknown, id)
		}
	}
	return out, nil
}

func itemHistory(ctx context.Context, tx pgx.Tx, itemID string) ([]FeedbackHistoryAt, error) {
	rows, err := tx.Query(ctx, `SELECT h.kind,
			CASE WHEN h.run_id IS NOT NULL THEN 'agent' ELSE COALESCE((SELECT p.name FROM people p WHERE p.id = h.person_id), 'a person') END,
			h.body, COALESCE(h.commit_sha, '')
		FROM feedback_replies h WHERE h.item_id = $1 ORDER BY h.created_at, h.id`, itemID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[FeedbackHistoryAt])
}
