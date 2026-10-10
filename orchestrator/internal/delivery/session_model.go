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

// Chose says whether the session set either half.
func (m SessionModel) Chose() bool { return m.Tier != nil || m.Harness != nil }

// SessionMisfit is why harness cannot run the tier's model, in words for
// the session's owner, whose Model it is changed in; "" when it can, or
// when there is no model to check. The scripted agent stands in for any
// harness.
func SessionMisfit(harness, model, tier string) string {
	if model == "" || fakeagent.Is(model) || harnessRuns(harness, llm.Provider(model) == llm.ProviderAnthropic) {
		return ""
	}
	return fmt.Sprintf("%s takes %s, but the tier %s requests %s. Choose another harness or tier in the session's Model.",
		harnessLabel[harness], harnessWants(harness), tier, model)
}

// BrainstormMisfit is why a session's agent cannot start on harness and
// tier, as its Run fails saying so: the owner's words when the session
// chose either half (the fix is in its Model), the organisation's role
// sentence when it follows the organisation in both.
func BrainstormMisfit(chosen SessionModel, harness string, tier Tier) string {
	if chosen.Chose() {
		return SessionMisfit(harness, tier.Model, tier.Name)
	}
	if tier.Model == "" || fakeagent.Is(tier.Model) {
		return ""
	}
	return HarnessFits(harness, tier.Model, tier.Name, RoleName(RoleBrainstorm), llm.Provider(tier.Model) == llm.ProviderAnthropic)
}

// SessionModelProblem is why a session cannot choose m, in words for a
// 400; "" when it can. The tier must be the organisation's (locked FOR KEY
// SHARE, so a concurrent removal waits for the caller's commit) and name a
// model, the harness one of the three, and the pair the next start would
// use — each value the session's, else the organisation's — must fit
// (SessionMisfit). An organisation's tier that names no model passes: its
// Run fails saying so, as without a choice.
func SessionModelProblem(ctx context.Context, tx pgx.Tx, orgModels json.RawMessage, m SessionModel) (string, error) {
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
	return SessionMisfit(eff.HarnessName(), *model, name), nil
}
