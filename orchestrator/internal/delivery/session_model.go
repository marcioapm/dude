package delivery

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
)

// A session may choose its brainstorm's tier and harness (sessions.tier,
// sessions.harness, migration 106); each left NULL follows the
// organisation's Brainstorm setting. Read when a Run is built, so a change
// applies at the agent's next start.

const (
	EvSessionModelChanged  = "session.model.changed"
	EvSessionModelFallback = "session.model.fallback"
)

// SessionModel is what a session chose; nil follows the organisation.
type SessionModel struct {
	Tier, Harness *string
}

// LoadSessionModel reads a session's choice.
func LoadSessionModel(ctx context.Context, tx pgx.Tx, sessionID string) (SessionModel, error) {
	var m SessionModel
	err := tx.QueryRow(ctx, `SELECT tier, harness FROM sessions WHERE id = $1`, sessionID).Scan(&m.Tier, &m.Harness)
	return m, err
}

// Over is the organisation's Brainstorm settings with the session's choice
// in place of each value it sets.
func (m SessionModel) Over(org RoleSettings) RoleSettings {
	if m.Tier != nil {
		org.Tier = *m.Tier
	}
	if m.Harness != nil {
		org.Harness = *m.Harness
	}
	return org
}

// SessionModelProblem is why a session cannot choose m, in words for a
// 400; "" when it can. The tier must be the organisation's (locked FOR KEY
// SHARE, so a concurrent removal waits for the caller's commit) and name a
// model, and the harness one of the three.
func SessionModelProblem(ctx context.Context, tx pgx.Tx, m SessionModel) (string, error) {
	if m.Tier != nil {
		var name string
		var model *string
		err := tx.QueryRow(ctx, `SELECT name, model FROM model_tiers WHERE id = $1 FOR KEY SHARE`, *m.Tier).Scan(&name, &model)
		if db.IsNotFound(err) {
			return fmt.Sprintf("there is no model tier %s", *m.Tier), nil
		}
		if err != nil {
			return "", err
		}
		if model == nil {
			return fmt.Sprintf("the tier %s names no model yet", name), nil
		}
	}
	if m.Harness != nil && !IsHarness(*m.Harness) {
		return fmt.Sprintf("there is no harness %q: opencode, claude-code or codex", *m.Harness), nil
	}
	return "", nil
}
