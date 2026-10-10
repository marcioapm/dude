package api

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// checkSessionModel refuses, with a 400 in words, a tier and harness a
// session cannot choose (delivery.SessionModelProblem). A session that
// chooses neither follows the organisation and is not checked: a misfit
// there is the organisation's, and its Run says so.
func checkSessionModel(ctx context.Context, tx pgx.Tx, m delivery.SessionModel) error {
	if !m.Chose() {
		return nil
	}
	var orgModels json.RawMessage
	if err := tx.QueryRow(ctx, `SELECT default_agent_models FROM organizations WHERE id = current_organization_id()`).Scan(&orgModels); err != nil {
		return err
	}
	problem, err := delivery.SessionModelProblem(ctx, tx, orgModels, m)
	if err != nil {
		return err
	}
	if problem != "" {
		return fail(http.StatusBadRequest, "bad_model", "%s", problem)
	}
	return nil
}

// setSessionModel is the owner choosing the session's tier and harness;
// null (or absent) sets one back to the organisation's. It changes no Run:
// the next start of the session's agent reads it (phases.brainstormSpec),
// and a live or parked one goes on with what it was submitted with.
func (s *Server) setSessionModel(w http.ResponseWriter, r *http.Request, org string) error {
	p, err := sessionPrincipal(r)
	if err != nil {
		return err
	}
	var body struct {
		Tier    *string `json:"tier"`
		Harness *string `json:"harness"`
	}
	if err := read(r, &body); err != nil {
		return err
	}
	id := r.PathValue("id")
	chosen := delivery.SessionModel{Tier: body.Tier, Harness: body.Harness}
	var view map[string]any
	err = s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
		if err := lockedOwner(r.Context(), tx, id, p.Person, "choose its model"); err != nil {
			return err
		}
		if err := checkSessionModel(r.Context(), tx, chosen); err != nil {
			return err
		}
		before, err := delivery.LoadSessionModel(r.Context(), tx, id)
		if err != nil {
			return err
		}
		if !sameChoice(before.Tier, chosen.Tier) || !sameChoice(before.Harness, chosen.Harness) {
			if _, err := tx.Exec(r.Context(), `UPDATE sessions SET tier = $2, harness = $3, updated_at = now() WHERE id = $1`,
				id, chosen.Tier, chosen.Harness); err != nil {
				return err
			}
			var tier any
			if chosen.Tier != nil {
				var name string
				if err := tx.QueryRow(r.Context(), `SELECT name FROM model_tiers WHERE id = $1`, *chosen.Tier).Scan(&name); err != nil {
					return err
				}
				tier = map[string]any{"id": *chosen.Tier, "name": name}
			}
			if err := delivery.SessionEvent(r.Context(), tx, delivery.SessionRef(org, id), delivery.EvSessionModelChanged,
				p.writer().ActorType, p.Actor, map[string]any{"by": p.Person, "tier": tier, "harness": chosen.Harness}); err != nil {
				return err
			}
		}
		view, err = sessionModelView(r.Context(), tx, id)
		return err
	})
	if err != nil {
		return err
	}
	write(w, http.StatusOK, map[string]any{"id": id, "model": view})
	return nil
}

func sameChoice(a, b *string) bool { return (a == nil) == (b == nil) && (a == nil || *a == *b) }

// sessionModelView is a session's model as its detail shows it: what it
// chose (tier, harness; null follows the organisation), what the
// organisation's Brainstorm setting is, and what the agent's next start
// would use (effective), with misfit saying why that pair cannot run (the
// session's wording when it chose either half, as its Run fails with). A
// tier that names no model, or no tier at all, leaves the effective
// tierName or model null.
func sessionModelView(ctx context.Context, tx pgx.Tx, sessionID string) (map[string]any, error) {
	var orgModels json.RawMessage
	if err := tx.QueryRow(ctx, `SELECT default_agent_models FROM organizations WHERE id = current_organization_id()`).Scan(&orgModels); err != nil {
		return nil, err
	}
	chosen, err := delivery.LoadSessionModel(ctx, tx, sessionID)
	if err != nil {
		return nil, err
	}
	org := delivery.ResolveRole(delivery.RoleBrainstorm, orgModels)
	eff := chosen.Over(org)
	type tierRow struct {
		ID     string  `json:"id"`
		Name   string  `json:"name"`
		Model  *string `json:"model"`
		Effort *string `json:"effort"`
	}
	tierOf := func(id string) (*tierRow, error) {
		if id == "" {
			return nil, nil
		}
		t := tierRow{ID: id}
		err := tx.QueryRow(ctx, `SELECT name, model, effort FROM model_tiers WHERE id = $1`, id).Scan(&t.Name, &t.Model, &t.Effort)
		if db.IsNotFound(err) {
			return nil, nil
		}
		return &t, err
	}
	var own, orgTier, effTier *tierRow
	if chosen.Tier != nil {
		if own, err = tierOf(*chosen.Tier); err != nil {
			return nil, err
		}
	}
	if orgTier, err = tierOf(org.Tier); err != nil {
		return nil, err
	}
	effTier = orgTier
	if own != nil {
		effTier = own
	}
	effective := map[string]any{"tierName": nil, "model": nil, "harness": eff.HarnessName()}
	var misfit any
	if effTier != nil {
		effective["tierName"], effective["model"] = effTier.Name, effTier.Model
		if effTier.Model != nil {
			misfit = db.Nullable(delivery.BrainstormMisfit(chosen, eff.HarnessName(), delivery.Tier{Name: effTier.Name, Model: *effTier.Model}))
		}
	}
	organization := map[string]any{"tier": orgTier, "harness": org.HarnessName()}
	return map[string]any{"tier": own, "harness": chosen.Harness, "effective": effective, "organization": organization, "misfit": misfit}, nil
}
