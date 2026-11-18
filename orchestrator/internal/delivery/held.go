package delivery

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

// A fixer's replies and thread resolutions wait for its fix (#485). A
// fixer answering pull request feedback (phase fix) is checked as before,
// GitHub read as before (a resolution's thread must be its batch's), and
// then held in pr_held_actions instead of posted: its commits pass the
// checks gate and are pushed only after its turn, and a "fixed" reply or a
// resolved thread on GitHub before then names a fix the pull request does
// not have. Once the Run completes — its push published to the pull
// request's branch, so the head holds its commits — PostHeld posts them in
// the order they were made, once each. A Run that ends any other way drops
// them, and its conductor is told once (WakeHeldDropped).
//
// The conductor's own replies and actions are not held: they claim no fix
// of their own.

// WakeHeldDropped is the conductor's reason when a fixer's held replies
// and resolutions were dropped with its Run (conductor_wakes).
const WakeHeldDropped = "held_dropped"

// HeldQueued is what a held call answers.
const HeldQueued = "queued: dude posts it once your fix is on the pull request"

// heldAction is a held reply or resolution, as pr_held_actions keeps it.
type heldAction struct {
	Ref    RunRef `json:"ref"`
	Repo   string `json:"repo"`
	Slug   string `json:"slug"`
	Number int    `json:"number"`
	PRID   string `json:"prId,omitempty"`
	// A reply: its text and body, where (Thread, a line comment's), and
	// in answer to what.
	Text      string `json:"text,omitempty"`
	Body      string `json:"body,omitempty"`
	InReplyTo string `json:"inReplyTo,omitempty"`
	Thread    int64  `json:"thread,omitempty"`
	// A resolution: GitHub's thread id, read when it was checked.
	ThreadID string `json:"threadId,omitempty"`
}

// holdTx keeps a fixer's checked call for its push. A Run already ended
// unpublished drops it at once, as its end would have.
func holdTx(ctx context.Context, tx pgx.Tx, kind string, a heldAction) error {
	raw, err := json.Marshal(a)
	if err != nil {
		return err
	}
	var status string
	if err := tx.QueryRow(ctx, `WITH held AS (INSERT INTO pr_held_actions (organization_id, task_id, run_id, kind, action)
		VALUES ($1, $2, $3, $4, $5) RETURNING run_id)
		SELECT r.status::text FROM runs r JOIN held ON held.run_id = r.id`,
		a.Ref.Org, a.Ref.TaskID, a.Ref.RunID, kind, raw).Scan(&status); err != nil {
		return err
	}
	if status == "failed" || status == "aborted" {
		return DropHeldTx(ctx, tx, a.Ref)
	}
	return nil
}

// DropHeldTx drops what Run r held: it ended without its fix reaching the
// pull request. Its conductor is told once.
func DropHeldTx(ctx context.Context, tx pgx.Tx, r RunRef) error {
	tag, err := tx.Exec(ctx, `UPDATE pr_held_actions SET dropped_at = now()
		WHERE run_id = $1 AND posted_at IS NULL AND dropped_at IS NULL`, r.RunID)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	_, err = RecordWakeTx(ctx, tx, r.Org, r.TaskID, WakeHeldDropped, "held-dropped:"+r.RunID,
		fmt.Sprintf("Fixer Run %s ended before its fix reached the pull request: %d of its replies and thread resolutions there were not posted.",
			r.RunID, tag.RowsAffected()))
	return err
}

// HeldDue are the organizations and Runs with held calls to post: their Run
// completed. Read across tenants by the sweep.
const HeldDue = `pr_held_actions h JOIN runs r ON r.id = h.run_id
	WHERE h.posted_at IS NULL AND h.dropped_at IS NULL AND r.status = 'completed'`

// claimHeldTx claims Run runID's next held call, oldest first, for
// posting: false when none is left. Claimed before GitHub is called, so it
// is posted at most once.
func claimHeldTx(ctx context.Context, tx pgx.Tx, runID string) (id int64, kind string, a heldAction, ok bool, err error) {
	var raw []byte
	err = tx.QueryRow(ctx, `UPDATE pr_held_actions SET posted_at = now() WHERE id = (SELECT h.id FROM pr_held_actions h
		JOIN runs r ON r.id = h.run_id AND r.status = 'completed'
		WHERE h.run_id = $1 AND h.posted_at IS NULL AND h.dropped_at IS NULL ORDER BY h.id LIMIT 1 FOR UPDATE OF h)
		RETURNING id, kind, action`, runID).Scan(&id, &kind, &raw)
	if err == pgx.ErrNoRows {
		return 0, "", a, false, nil
	}
	if err != nil {
		return 0, "", a, false, err
	}
	return id, kind, a, true, json.Unmarshal(raw, &a)
}

// InOrg runs fn in a transaction of org's (db.DB.InOrg).
type InOrg func(ctx context.Context, org string, fn func(pgx.Tx) error) error

// PostHeld posts completed Run runID's held calls on GitHub in the order
// they were made, each recorded as it would have been at once. Each is
// claimed before GitHub is called, so it is posted at most once: GitHub
// down or limiting gives it back for the next sweep; a refusal loses it,
// as a reply GitHub refused at once is.
func PostHeld(ctx context.Context, inOrg InOrg, gh *forge.GitHub, org, runID string) error {
	for {
		var kind string
		var a heldAction
		var ok bool
		var id int64
		if err := inOrg(ctx, org, func(tx pgx.Tx) (err error) {
			id, kind, a, ok, err = claimHeldTx(ctx, tx, runID)
			return err
		}); err != nil || !ok {
			return err
		}
		if err := postHeld(ctx, inOrg, gh, org, kind, a); err != nil {
			if forge.Transient(err) {
				_ = inOrg(ctx, org, func(tx pgx.Tx) error {
					_, err := tx.Exec(ctx, `UPDATE pr_held_actions SET posted_at = NULL WHERE id = $1`, id)
					return err
				})
			}
			return err
		}
	}
}

func postHeld(ctx context.Context, inOrg InOrg, gh *forge.GitHub, org, kind string, a heldAction) error {
	if kind == ActionResolveThread {
		c := PRActionCheck{ref: a.Ref, action: ActionResolveThread, prID: a.PRID, repo: a.Repo, slug: a.Slug, number: a.Number}
		if err := gh.ResolveReviewThread(ctx, a.Slug, a.ThreadID); err != nil {
			return err
		}
		return inOrg(ctx, org, func(tx pgx.Tx) error {
			if err := tx.QueryRow(ctx, `SELECT COALESCE(head_sha, '') FROM pull_requests WHERE id = $1`, a.PRID).Scan(&c.head); err != nil {
				return err
			}
			_, err := RecordPRActionTx(ctx, tx, c, PRActionDone{Thread: a.ThreadID})
			return err
		})
	}
	p := ReplyPost{ref: a.Ref, repo: a.Repo, number: a.Number, slug: a.Slug, text: a.Text, inReplyTo: a.InReplyTo,
		thread: a.Thread, body: a.Body, by: "fixer"}
	posted, err := PostReply(ctx, gh, p)
	if err != nil {
		return err
	}
	return inOrg(ctx, org, func(tx pgx.Tx) error {
		_, err := RecordReplyTx(ctx, tx, p, posted)
		return err
	})
}
