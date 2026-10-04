package delivery

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// EvRunSteered is a steer queued for a Run's agent: a person's, or the
// task's conductor's (payload by "conductor").
const EvRunSteered = "run.steered"

// SteerTextMax bounds a steer's words, in bytes, a person's and the
// conductor's alike: the API's request body bound (api.read), which is
// what limited a person's steer.
const SteerTextMax = 1 << 20

// SteerError is a steer refused, with the kind the API answers it as:
// bad_request, not_found, conflict, not_an_agent or invalid_attachment.
type SteerError struct{ Kind, Msg string }

func (e SteerError) Error() string { return e.Msg }

func steerErr(kind, format string, a ...any) error {
	return SteerError{kind, fmt.Sprintf(format, a...)}
}

// SteerInput is one steer. Actor is who wrote it, as the ledger records it;
// Conductor, the conductor Run that wrote it ("" for a person). A
// conductor's carries no images and repeats nothing.
type SteerInput struct {
	RunID, Text, Scope, Supersedes string
	Interrupt                      bool
	AttachmentIDs                  []string
	Actor                          Writer
	Conductor                      string
}

// Steered is a steer queued: its directive and event payload.
type Steered struct {
	ID, TaskID, Scope string
	CreatedAt         time.Time
	Attachments       []json.RawMessage
}

// Steer queues a steer for a live agent Run of org — its directive, its
// images, and its run.steered event — as the phase syncer then delivers
// it. Refused with a SteerError. Locks the Run.
func Steer(ctx context.Context, tx pgx.Tx, org string, in SteerInput) (Steered, error) {
	if len(in.Text) > SteerTextMax {
		return Steered{}, steerErr("bad_request", "a steer is at most %d KiB of text", SteerTextMax>>10)
	}
	if strings.TrimSpace(in.Text) == "" && len(in.AttachmentIDs) == 0 && in.Supersedes == "" {
		return Steered{}, steerErr("bad_request", "text or an image is required")
	}
	if in.Scope == "" {
		in.Scope = "run"
	}
	var projectID, taskID, status, kind string
	err := tx.QueryRow(ctx, `SELECT project_id, task_id, status::text, kind FROM runs WHERE id = $1 FOR UPDATE`, in.RunID).
		Scan(&projectID, &taskID, &status, &kind)
	if db.IsNotFound(err) {
		return Steered{}, steerErr("not_found", "run %s not found", in.RunID)
	}
	if err != nil {
		return Steered{}, err
	}
	if kind != "agent" {
		return Steered{}, steerErr("not_an_agent", "run %s is a branch preview, not an agent's", in.RunID)
	}
	switch status {
	case "pending", "scheduled", "starting", "running", "paused":
	default:
		return Steered{}, steerErr("conflict", "run %s is %s and can no longer be steered", in.RunID, status)
	}
	if in.Supersedes != "" {
		if err := checkRepeat(ctx, tx, in.RunID, in.Supersedes, in.Text, len(in.AttachmentIDs) > 0); err != nil {
			return Steered{}, err
		}
	}
	ref := RunRef{Org: org, ProjectID: projectID, TaskID: taskID, RunID: in.RunID}
	id, createdAt, err := QueueDirective(ctx, tx, ref, Directive{Text: in.Text, Scope: in.Scope, Supersedes: in.Supersedes,
		Interrupt: in.Interrupt})
	if err != nil {
		return Steered{}, err
	}
	payload := map[string]any{"directiveId": id, "text": in.Text, "scope": in.Scope, "supersedes": db.Nullable(in.Supersedes),
		"interrupt": in.Interrupt}
	if in.Conductor != "" {
		if _, err := tx.Exec(ctx, `UPDATE directives SET conductor_run_id = $2 WHERE id = $1`, id, in.Conductor); err != nil {
			return Steered{}, err
		}
		payload["by"], payload["conductorRunId"] = RoleConductor, in.Conductor
	}
	attached, err := Attach(ctx, tx, taskID, id, in.AttachmentIDs)
	if refused, ok := err.(AttachmentError); ok {
		return Steered{}, steerErr("invalid_attachment", "%s", refused.Message)
	}
	if err != nil {
		return Steered{}, err
	}
	if len(attached) > 0 {
		payload["attachments"] = attached
	}
	ev := ref.Event(EvRunSteered, in.Actor.ActorType, payload)
	ev.ActorID = in.Actor.ActorID
	if _, err := ledger.Append(ctx, tx, ev); err != nil {
		return Steered{}, err
	}
	return Steered{ID: id, TaskID: taskID, Scope: in.Scope, CreatedAt: createdAt, Attachments: attached}, nil
}

// checkRepeat vets a steer superseding directive superseded of the Run.
// Repeating its words (Retry, Interrupt now) carries its images
// (DirectiveAttachments), so the repeat may have no words of its own, but
// only if there is something to repeat; and it cannot bring new images,
// which would be attached and never sent.
func checkRepeat(ctx context.Context, tx pgx.Tx, runID, superseded, text string, newImages bool) error {
	required := steerErr("bad_request", "text or an image is required")
	var words string
	err := tx.QueryRow(ctx, `SELECT text FROM directives WHERE id = $1 AND run_id = $2`, superseded, runID).Scan(&words)
	if err != nil && !db.IsNotFound(err) {
		return err
	}
	if err != nil || words != text {
		// New words: a message of its own.
		if strings.TrimSpace(text) == "" && !newImages {
			return required
		}
		return nil
	}
	if newImages {
		return steerErr("invalid_attachment", "a message sent again carries the images it had: send new images in a new message")
	}
	if strings.TrimSpace(words) == "" {
		carried, err := DirectiveAttachments(ctx, tx, superseded)
		if err != nil {
			return err
		}
		if len(carried) == 0 {
			return required
		}
	}
	return nil
}
