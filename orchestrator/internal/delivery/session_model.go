package delivery

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/fakeagent"
	"github.com/marciomartins/dude/orchestrator/internal/llm"
)

// A session may choose its brainstorm's tier and harness (sessions.tier,
// sessions.harness, migration 105); each left NULL follows the
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

// IsHarness says whether h names a harness.
func IsHarness(h string) bool { return harnessLabel[h] != "" }

// HarnessLabel is a harness as a person reads it.
func HarnessLabel(h string) string { return harnessLabel[h] }

// SessionModelProblem is why a session cannot choose m, in words for a
// 400; "" when it can. The tier must be the organisation's (locked FOR KEY
// SHARE, so a concurrent removal waits for the caller's commit), the
// harness one of the three, and the pair the next start would use — each
// value the session's, else the organisation's — must fit (HarnessFits).
// A pair with no model to check (no tier, or a tier naming none) passes:
// its Run fails saying so, as an organisation's would. The scripted agent
// stands in for any harness.
func SessionModelProblem(ctx context.Context, tx pgx.Tx, orgModels json.RawMessage, m SessionModel) (string, error) {
	if m.Tier != nil {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM (SELECT 1 FROM model_tiers WHERE id = $1 FOR KEY SHARE) t`, *m.Tier).Scan(&n); err != nil {
			return "", err
		}
		if n == 0 {
			return fmt.Sprintf("there is no model tier %s", *m.Tier), nil
		}
	}
	if m.Harness != nil && !IsHarness(*m.Harness) {
		return fmt.Sprintf("there is no harness %q: opencode, claude-code or codex", *m.Harness), nil
	}
	eff := m.Over(ResolveRole(RoleBrainstorm, orgModels))
	if eff.Tier == "" {
		return "", nil
	}
	var name string
	var model *string
	err := tx.QueryRow(ctx, `SELECT name, model FROM model_tiers WHERE id = $1`, eff.Tier).Scan(&name, &model)
	if db.IsNotFound(err) || (err == nil && model == nil) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	harness := eff.HarnessName()
	if fakeagent.Is(*model) || HarnessFits(harness, *model, name, RoleName(RoleBrainstorm), llm.Provider(*model) == llm.ProviderAnthropic) == "" {
		return "", nil
	}
	wants := "an Anthropic model (claude-…)"
	if harness == HarnessCodex {
		wants = "an OpenAI model"
	}
	return fmt.Sprintf("%s takes %s, but the tier %s requests %s. Choose another harness or tier.",
		harnessLabel[harness], wants, name, *model), nil
}
