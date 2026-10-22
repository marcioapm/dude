package delivery

// Model tiers: the organization's (model_tiers, migration 069), named by
// each role's settings (tier, over the same layers as its machine). A tier
// names the one model dude requests from the LLM proxy for it, and how
// hard it thinks; what the proxy then serves is the proxy's to say.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Tier is a tier as a Run is built from it: its name, the model it
// requests ("" while none is set), its reasoning effort ("" the model's
// default), and the extra OpenCode model options and request headers its
// agent is requested with.
type Tier struct {
	ID, Name, Model, Effort string
	Description             string
	Position                int
	Options                 map[string]any
	Headers                 map[string]string
}

// TierFor reads the tier role resolves to, in the organization's
// transaction. It returns the reason in words a Run fails with when the role
// names no tier, names one that is gone, or its tier names no model yet.
func TierFor(ctx context.Context, tx pgx.Tx, role string, settings RoleSettings) (Tier, string, error) {
	if settings.Tier == "" {
		return TierIn(nil, role, settings)
	}
	tiers, err := loadTiers(ctx, tx, `WHERE id = $1`, settings.Tier)
	if err != nil {
		return Tier{}, "", err
	}
	return TierIn(tiers, role, settings)
}

// LoadTiers reads every tier of the organization, by id, for TierIn.
func LoadTiers(ctx context.Context, tx pgx.Tx) (map[string]Tier, error) {
	return loadTiers(ctx, tx, ``)
}

func loadTiers(ctx context.Context, tx pgx.Tx, where string, args ...any) (map[string]Tier, error) {
	rows, err := tx.Query(ctx, `SELECT id, name, COALESCE(model, ''), COALESCE(effort, ''), options, headers, description, position FROM model_tiers `+where, args...)
	if err != nil {
		return nil, fmt.Errorf("load model tier: %w", err)
	}
	out := map[string]Tier{}
	for rows.Next() {
		var t Tier
		if err := rows.Scan(&t.ID, &t.Name, &t.Model, &t.Effort, &t.Options, &t.Headers, &t.Description, &t.Position); err != nil {
			rows.Close()
			return nil, fmt.Errorf("load model tier: %w", err)
		}
		out[t.ID] = t
	}
	return out, rows.Err()
}

// TierIn is TierFor over tiers already read (LoadTiers).
func TierIn(tiers map[string]Tier, role string, settings RoleSettings) (Tier, string, error) {
	who := RoleName(role)
	if settings.Tier == "" {
		return Tier{}, fmt.Sprintf("The %s runs on no model tier. An admin picks one in Agents.", who), nil
	}
	t, ok := tiers[settings.Tier]
	if !ok {
		return Tier{}, fmt.Sprintf("The %s's model tier no longer exists. An admin picks another in Agents.", who), nil
	}
	if t.Model == "" {
		return t, fmt.Sprintf("The %s runs on %s, which names no model yet. An admin sets it in Models.", who, t.Name), nil
	}
	return t, "", nil
}

// RoleName is a settings role as a person reads it in a sentence: the
// fixer is a role of its own there, though it runs as the implementer.
func RoleName(role string) string {
	switch role {
	case "fixer":
		return "Fixer"
	case "qa_browser":
		return "Tester"
	}
	if label, ok := RoleLabel[role]; ok {
		return label
	}
	return role
}
