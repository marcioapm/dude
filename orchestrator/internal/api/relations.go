package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// startOverride is what a start route takes to start a blocked task anyway.
type startOverride struct {
	Override bool   `json:"override"`
	Note     string `json:"note"`
}

// overrideFor is the person's override for a start this way, nil when none
// was asked. Only a person may override: a service or an agent gets 403.
func overrideFor(r *http.Request, o startOverride, how string) (*delivery.Override, error) {
	if !o.Override {
		return nil, nil
	}
	p := principalOf(r)
	if p.Person == "" {
		return nil, fail(http.StatusForbidden, "override_forbidden", "only a person can start a blocked task")
	}
	return &delivery.Override{ActorType: p.ActorType, ActorID: p.Actor, How: how, Note: strings.TrimSpace(o.Note)}, nil
}

// blockedFailure answers a start the rule refused: 409 blocked with the
// blockers in its details, 409 archived, 409 closed. Anything else passes
// through.
func blockedFailure(err error) error {
	if reason := delivery.StartRefusalReason(err); reason == delivery.RefusedArchived || reason == delivery.RefusedClosed {
		return fail(http.StatusConflict, reason, "%s", err.Error())
	}
	b, ok := delivery.AsBlocked(err)
	if !ok {
		return err
	}
	return &httpError{status: http.StatusConflict, code: "blocked", message: b.Error(),
		details: map[string]any{"blockers": delivery.BlockersPayload(b.Blockers)}}
}

// refusedStart records a start the rule refused (task.start_refused) in a
// transaction of its own, the refused one having rolled back, and answers
// it; any other error passes through.
func (s *Server) refusedStart(ctx context.Context, org, taskID, path string, p principal, err error) error {
	if delivery.StartRefusalReason(err) == "" {
		return err
	}
	_ = s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		return delivery.RecordRefusalTx(ctx, tx, org, taskID, path, p.ActorType, p.Actor, err)
	})
	return blockedFailure(err)
}
