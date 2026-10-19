package phases

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// sessionChildSpec gives an investigator the session's read-only checkout
// policy without turning it into a brainstorm talker or a task phase.
func (s *Syncer) sessionChildSpec(ctx context.Context, r phaseRun, stored *lux.StoredSpec, image chosenImage) (lux.Spec, runSizes, []delivery.SentAttachment, []lux.Secret, error) {
	var spec lux.Spec
	var sizes runSizes
	err := s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		var models json.RawMessage
		var prompt, note, tierOverride string
		if err := tx.QueryRow(ctx, `SELECT default_agent_models, COALESCE(r.child_prompt,''), COALESCE(r.conductor_note,''), COALESCE(r.tier_override,'')
			FROM organizations o JOIN runs r ON r.organization_id=o.id WHERE r.id=$1`, r.ID).Scan(&models, &prompt, &note, &tierOverride); err != nil {
			return err
		}
		settings := delivery.ResolveRole(delivery.PromptRoleForPhase[r.Phase], models, models)
		if tierOverride != "" {
			settings.Tier = tierOverride
		}
		var recorded delivery.Tier
		if stored != nil {
			if err := tx.QueryRow(ctx, `SELECT COALESCE(model,''), COALESCE(model_tier,''), COALESCE(effort,''), COALESCE(harness,'') FROM runs WHERE id=$1`, r.ID).Scan(&recorded.Model, &recorded.Name, &recorded.Effort, &settings.Harness); err != nil {
				return err
			}
		}
		tier, noTier, err := delivery.TierFor(ctx, tx, delivery.PromptRoleForPhase[r.Phase], settings)
		if err != nil {
			return err
		}
		if noTier != "" {
			return errNoModel(noTier)
		}
		if stored != nil {
			tier = recorded
		}
		repos, err := delivery.SessionRepositories(ctx, tx, r.SessionID)
		if err != nil {
			return err
		}
		var in specInput
		in.RunID, in.OrganizationID, in.SessionID, in.Phase, in.Role = r.ID, r.Org, r.SessionID, r.Phase, delivery.RoleForPhase[r.Phase]
		in.Model, in.ModelTier, in.Effort, in.Options, in.Headers = tier.Model, tier.Name, tier.Effort, tier.Options, tier.Headers
		in.Harness, in.Prompt = settings.HarnessName(), prompt
		if note != "" {
			in.Prompt += "\n\n" + note
		}
		in.Instructions = delivery.Instructions(delivery.RoleForPhase[r.Phase], "", delivery.PromptInput{Child: true, Tools: s.Agent.ToolsURL != "", CLI: s.Agent.ToolsURL != "" && s.Agent.ToolsService}.InstructionsInput())
		for _, repo := range repos {
			in.Repos = append(in.Repos, specRepo{Name: repo.SpecName(), URL: repo.URL, Ref: repo.DefaultBranch, ReadOnly: true, Path: delivery.SessionRepoPath(repo.Key, repo.Name), Setup: repo.SetupScript})
		}
		in.Image, in.NestedContainers = image.Ref, image.Nested
		if stored != nil {
			in.Image = stored.Image.Ref
		}
		if in.Registry, err = LoginFor(ctx, s.Registry, in.Image, stored); err != nil {
			return err
		}
		if s.Agent.ToolsURL != "" {
			if in.ToolsToken, err = s.toolsToken(ctx, r); err != nil {
				return err
			}
		}
		gh, err := s.Forges.For(ctx, r.Org)
		if err != nil {
			return forgeFailure(err)
		}
		if gh != nil {
			if in.ForgeToken, in.RepoTokens, err = forgeTokens(ctx, gh, in.Repos, nil); err != nil {
				return forgeFailure(err)
			}
			if in.Bot, err = botOf(ctx, gh); err != nil {
				return forgeFailure(err)
			}
		}
		spec, err = runSpec(s.Agent, in)
		sizes = runSizes{Now: in.Machine}
		return nil
	})
	return spec, sizes, nil, nil, err
}
