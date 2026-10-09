package phases

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// A session's agent (role brainstorm), as the syncer runs it: no task, its
// session's linked repositories each at repos/<project key>/<name>, named
// SessionRepo.SpecName in the spec, never pushed, and no push branch at all.

// sessionRepoMissing (SQL, over runs r): a repository linked to the Run's
// session that its lux Run does not hold yet — brought by a resume.
const sessionRepoMissing = `EXISTS (SELECT 1 FROM session_repositories sr
	JOIN repositories repo ON repo.id = sr.repository_id JOIN projects p ON p.id = repo.project_id
	JOIN session_projects spj ON spj.session_id = sr.session_id AND spj.project_id = repo.project_id
	WHERE sr.session_id = r.session_id AND NOT (` + delivery.SessionSpecNameSQL + ` = ANY (r.lux_repositories)))`

func (s *Syncer) brainstormSpec(ctx context.Context, r phaseRun, stored *lux.StoredSpec, image string) (lux.Spec, *delivery.Machine, error) {
	var in specInput
	var orgModels json.RawMessage
	var briefing string
	var repos []delivery.SessionRepo
	var tier delivery.Tier
	var noTier string
	var sizes delivery.Sizes
	var prompts delivery.Prompts
	role := delivery.RoleBrainstorm
	var settings delivery.RoleSettings
	err := s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		// A session spans projects: its organisation's list alone.
		if err := tx.QueryRow(ctx, `SELECT o.default_agent_models, COALESCE(r.prompt, ''), o.agent_egress FROM runs r
			JOIN organizations o ON o.id = r.organization_id WHERE r.id = $1`, r.ID).Scan(&orgModels, &briefing, &in.Egress); err != nil {
			return fmt.Errorf("load the session's run: %w", err)
		}
		settings = delivery.ResolveRole(role, orgModels)
		var err error
		if stored != nil {
			var ranOn string
			if err := tx.QueryRow(ctx, `SELECT COALESCE(model, ''), COALESCE(model_tier, ''), COALESCE(effort, ''), COALESCE(harness, '') FROM runs WHERE id = $1`, r.ID).
				Scan(&tier.Model, &tier.Name, &tier.Effort, &ranOn); err != nil {
				return fmt.Errorf("load run model: %w", err)
			}
			settings.Harness = submittedHarness(ranOn, settings.Harness)
		} else if tier, noTier, err = delivery.TierFor(ctx, tx, role, settings); err != nil || noTier != "" {
			return err
		} else if noTier = harnessMisfit(settings, tier, role); noTier != "" {
			return nil
		}
		if sizes, err = delivery.LoadSizes(ctx, tx); err != nil {
			return err
		}
		if prompts, err = delivery.LoadPrompts(ctx, tx, r.ID, "", role); err != nil {
			return err
		}
		repos, err = delivery.SessionRepositories(ctx, tx, r.SessionID)
		return err
	})
	if err != nil {
		return lux.Spec{}, nil, err
	}
	if noTier != "" {
		return lux.Spec{}, nil, errNoModel(noTier)
	}
	var promptRepos []delivery.PromptRepo
	for _, repo := range repos {
		in.Repos = append(in.Repos, specRepo{Name: repo.SpecName(), URL: repo.URL, Ref: repo.DefaultBranch, ReadOnly: true,
			Path: delivery.SessionRepoPath(repo.Key, repo.Name)})
		promptRepos = append(promptRepos, delivery.PromptRepo{Name: repo.Key + "/" + repo.Name,
			Path: delivery.SessionRepoPath(repo.Key, repo.Name), ReadOnly: true})
	}
	in.RunID, in.OrganizationID, in.SessionID, in.Role = r.ID, r.Org, r.SessionID, role
	in.Model, in.ModelTier, in.Effort, in.Options, in.Headers = tier.Model, tier.Name, tier.Effort, tier.Options, tier.Headers
	in.Harness = settings.HarnessName()
	s.logIgnoredOptions(r, in)
	if m, ok := sizes.ForRole(role, nil, orgModels); ok {
		in.Machine = &m
	}
	in.Image = image
	if stored != nil {
		in.Image = stored.Image.Ref
	}
	if in.Registry, err = LoginFor(ctx, s.Registry, in.Image, stored); err != nil {
		return lux.Spec{}, nil, err
	}
	in.Prompt = delivery.BrainstormPrompt(briefing, delivery.PromptInput{Repositories: promptRepos,
		Tools: s.Agent.ToolsURL != "", Context: settings.Context, OrgPrompt: prompts.Org})
	gh, err := s.Forges.For(ctx, r.Org)
	if err != nil {
		return lux.Spec{}, nil, errForge{err}
	}
	if gh != nil {
		if in.ForgeToken, err = gh.Token(); err != nil {
			return lux.Spec{}, nil, errForge{err}
		}
	}
	if s.Agent.ToolsURL != "" {
		if in.ToolsToken, err = s.toolsToken(ctx, r); err != nil {
			return lux.Spec{}, nil, err
		}
	}
	return buildSpec(s.Agent, in), in.Machine, nil
}

// sessionResume puts on a session agent's resume the repositories linked
// since it last ran, as an approved request_repository's are added, and
// tells it where each is; and brings every checkout it holds to its
// default branch, fast-forward only ("fetch the latest").
func (s *Syncer) sessionResume(ctx context.Context, r phaseRun, spec lux.Spec, in *lux.ResumeInput) error {
	var have []string
	var repos []delivery.SessionRepo
	if err := s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT lux_repositories FROM runs WHERE id = $1`, r.ID).Scan(&have); err != nil {
			return err
		}
		var err error
		repos, err = delivery.SessionRepositories(ctx, tx, r.SessionID)
		return err
	}); err != nil {
		return err
	}
	bySpec := map[string]lux.Repository{}
	if spec.Git != nil {
		for _, repo := range spec.Git.Repositories {
			bySpec[repo.Name] = repo
		}
	}
	var told []string
	for _, repo := range repos {
		name := repo.SpecName()
		if slices.Contains(have, name) {
			in.Sync = append(in.Sync, lux.SyncRef{Repo: name, Ref: repo.DefaultBranch, Mode: lux.SyncFastForward})
			continue
		}
		if added, ok := bySpec[name]; ok {
			in.AddRepositories = append(in.AddRepositories, added)
			told = append(told, fmt.Sprintf("%s/%s is now checked out at %s (read only).", repo.Key, repo.Name, added.Path))
		}
	}
	if len(in.AddRepositories) > 0 {
		// One request per set of checkouts added: lux's git.clone for each
		// carries it. Told where they are, it needs no "carry on".
		in.RequestID = fmt.Sprintf("link-%s-%d", r.ID, len(have))
		if in.Input == resumeNudge {
			in.Input = ""
		}
		in.Input = strings.TrimSpace("A member linked more of a project to this session. " + strings.Join(told, " ") +
			"\n\n" + in.Input)
	}
	return nil
}
