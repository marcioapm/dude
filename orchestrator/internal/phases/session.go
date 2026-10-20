package phases

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/capabilities"
	"github.com/marciomartins/dude/orchestrator/internal/db"
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

// brainstormAnywhere is whether a brainstorm may reach any host, the one
// answer its spec (its network, and Codex's search forced live over the
// tier's) and its prompt both take: once submitted, the network recorded
// on the Run, which its resumes keep; before that, the network its submit
// will compute (runNetwork), so the organisation's brainstorm_anywhere or
// "*" in either list both count.
func (s *Syncer) brainstormAnywhere(ctx context.Context, tx pgx.Tx, run string) (bool, error) {
	var submitted, recorded bool
	var in specInput
	if err := tx.QueryRow(ctx, `SELECT r.network IS NOT NULL, COALESCE((r.network->>'unrestricted')::boolean, false),
		o.brainstorm_anywhere, o.agent_egress
		FROM runs r JOIN organizations o ON o.id = r.organization_id WHERE r.id = $1`, run).
		Scan(&submitted, &recorded, &in.Anywhere, &in.Egress); err != nil {
		return false, fmt.Errorf("load the brainstorm's network: %w", err)
	}
	if submitted {
		return recorded, nil
	}
	return runNetwork(s.Agent, in).Unrestricted, nil
}

func (s *Syncer) brainstormSpec(ctx context.Context, r phaseRun, stored *lux.StoredSpec, image chosenImage) (lux.Spec, runSizes, error) {
	var in specInput
	var orgModels json.RawMessage
	var briefing string
	var repos []delivery.SessionRepo
	var tier delivery.Tier
	var noTier string
	var sizes delivery.Sizes
	var raised delivery.Raises
	var recorded *delivery.Machine
	var prompt, orgName string
	// A resumed Run's repositories as lux holds them (forgeTokens).
	var submitted []string
	role := delivery.RoleBrainstorm
	var settings delivery.RoleSettings
	var grants capabilities.Grants
	// What a fresh agent is told of a web unlike its predecessor's; "" alike.
	var webChanged string
	err := s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		// A session spans projects: its organisation's list alone, or any
		// host when the brainstorm may reach any (brainstormAnywhere).
		if err := tx.QueryRow(ctx, `SELECT o.default_agent_models, COALESCE(r.prompt, ''), o.agent_egress, o.name FROM runs r
			JOIN organizations o ON o.id = r.organization_id WHERE r.id = $1`, r.ID).Scan(&orgModels, &briefing, &in.Egress, &orgName); err != nil {
			return fmt.Errorf("load the session's run: %w", err)
		}
		var err error
		if in.Anywhere, err = s.brainstormAnywhere(ctx, tx, r.ID); err != nil {
			return err
		}
		// The session's own tier and harness, each in place of the
		// organisation's; a resume below keeps what it was submitted with.
		chosen, err := delivery.LoadSessionModel(ctx, tx, r.SessionID)
		if err != nil {
			return fmt.Errorf("load the session's model: %w", err)
		}
		settings = chosen.Over(delivery.ResolveRole(role, orgModels))
		if grants, err = capabilities.Resolve(ctx, tx, capabilities.Agent{Role: role, Org: r.Org, Run: r.ID, Session: r.SessionID}); err != nil {
			return err
		}
		if stored != nil {
			var ranOn string
			if err := tx.QueryRow(ctx, `SELECT COALESCE(model, ''), COALESCE(model_tier, ''), COALESCE(effort, ''), COALESCE(harness, ''), machine, lux_repositories FROM runs WHERE id = $1`, r.ID).
				Scan(&tier.Model, &tier.Name, &tier.Effort, &ranOn, &recorded, &submitted); err != nil {
				return fmt.Errorf("load run model: %w", err)
			}
			settings.Harness = submittedHarness(ranOn, settings.Harness)
		} else if tier, noTier, err = delivery.TierFor(ctx, tx, role, settings); err != nil || noTier != "" {
			return err
		}
		if sizes, err = delivery.LoadSizes(ctx, tx); err != nil {
			return err
		}
		if raised, err = delivery.LoadSessionRaise(ctx, tx, r.SessionID, role); err != nil {
			return fmt.Errorf("load the session's raised machine: %w", err)
		}
		// A resume sends no prompt: lux keeps the submitted one.
		if stored == nil {
			talker, err := s.talkerInput(ctx, tx, r, nil)
			if err != nil {
				return err
			}
			prompt = delivery.BrainstormPrompt(briefing, talker)
			web := runWeb(s.Agent, specInput{Role: role, Harness: settings.HarnessName(), Options: tier.Options,
				Anywhere: in.Anywhere, Egress: in.Egress, Grants: &grants})
			instructions := talker.InstructionsInput()
			instructions.Grants, instructions.Web = &grants, &web
			in.Instructions = delivery.Instructions(role, "", instructions)
			if webChanged, err = s.sessionWebChanged(ctx, tx, r, web); err != nil {
				return err
			}
		}
		repos, err = delivery.SessionRepositories(ctx, tx, r.SessionID)
		return err
	})
	if err != nil {
		return lux.Spec{}, runSizes{}, err
	}
	if noTier != "" {
		return lux.Spec{}, runSizes{}, errNoModel(noTier)
	}
	for _, repo := range repos {
		in.Repos = append(in.Repos, specRepo{Name: repo.SpecName(), URL: repo.URL, Ref: repo.DefaultBranch, ReadOnly: true,
			Path: delivery.SessionRepoPath(repo.Key, repo.Name), Setup: repo.SetupScript})
	}
	in.RunID, in.OrganizationID, in.SessionID, in.Role, in.Grants = r.ID, r.Org, r.SessionID, role, &grants
	in.Model, in.ModelTier, in.Effort, in.Options, in.Headers = tier.Model, tier.Name, tier.Effort, tier.Options, tier.Headers
	in.Harness = settings.HarnessName()
	s.logIgnoredOptions(r, in)
	if m, ok := sizes.ForRole(role, raised, nil, orgModels); ok {
		if m.Raised != nil {
			m.From = "session"
		}
		in.Machine = &m
	}
	in.Image, in.NestedContainers = image.Ref, image.Nested
	if stored != nil {
		in.Image = stored.Image.Ref
	}
	if in.Registry, err = LoginFor(ctx, s.Registry, in.Image, stored); err != nil {
		return lux.Spec{}, runSizes{}, err
	}
	in.Prompt = prompt
	if stored == nil {
		// A session agent has no CLI: it calls request_machine itself.
		if line := s.machineLine(ctx, in.Machine, sizes, orgName, false); line != "" {
			in.Prompt += "\n\n" + line
		}
		if webChanged != "" {
			in.Prompt += "\n\n" + webChanged
		}
	}
	gh, err := s.Forges.For(ctx, r.Org)
	if err != nil {
		return lux.Spec{}, runSizes{}, errForge{err}
	}
	if gh != nil {
		if in.ForgeToken, in.RepoTokens, err = forgeTokens(ctx, gh, in.Repos, submitted); err != nil {
			return lux.Spec{}, runSizes{}, forgeFailure(err)
		}
		if in.Bot, err = botOf(ctx, gh); err != nil {
			return lux.Spec{}, runSizes{}, forgeFailure(err)
		}
	}
	if s.Agent.ToolsURL != "" {
		if in.ToolsToken, err = s.toolsToken(ctx, r); err != nil {
			return lux.Spec{}, runSizes{}, err
		}
	}
	spec, err := runSpec(s.Agent, in)
	return spec, runSizes{Now: in.Machine, Recorded: recorded}, err
}

// sessionResume puts on a session agent's resume the repositories linked
// since it last ran, as an approved request_repository's are added, and
// tells it where each is; and brings every checkout it holds to its
// default branch, fast-forward only ("fetch the latest").
func (s *Syncer) sessionResume(ctx context.Context, r phaseRun, spec lux.Spec, in *lux.ResumeInput) error {
	var have, again []string
	var repos []delivery.SessionRepo
	if err := s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT lux_repositories FROM runs WHERE id = $1`, r.ID).Scan(&have); err != nil {
			return err
		}
		// Asked for again by the agent (refresh_checkout) after its clone
		// failed on this session: by spec name.
		rows, err := tx.Query(ctx, `SELECT DISTINCT f.payload->>'repository' FROM events f
			WHERE f.session_id = $1 AND f.event_type = 'repository.clone_failed' AND EXISTS (SELECT 1 FROM events q
				WHERE q.session_id = $1 AND q.event_type = $2 AND q.occurred_at > f.occurred_at
				  AND q.payload->>'project' = f.payload->>'key'
				  AND q.payload->'repositories' @> jsonb_build_array(jsonb_build_object('name', f.payload->>'name')))`,
			r.SessionID, delivery.EvCheckoutRequested)
		if err != nil {
			return err
		}
		if again, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
			return err
		}
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
			// Written before lux clones it, so hedged: settleClone tells a failure.
			if slices.Contains(again, name) {
				told = append(told, checkoutAgain.Render(struct{ Key, Name, Path string }{repo.Key, repo.Name, added.Path}))
			} else {
				told = append(told, linkedTold(repo.Key, repo.Name, added.Path))
			}
		}
	}
	if len(in.AddRepositories) > 0 {
		// One request per set of checkouts added: lux's git.clone for each
		// carries it. Told where they are, it needs no "carry on".
		in.RequestID = fmt.Sprintf("link-%s-%d", r.ID, len(have))
		if in.Input == resumeNudge {
			in.Input = ""
		}
		in.Input = strings.TrimSpace(strings.Join(told, " ") + "\n\n" + in.Input)
	}
	return nil
}

// sessionWebChanged records on r, a session's agent about to be submitted,
// the web its spec gives it (runs.web), and is what it is told when the
// session's agent before it had another: its briefing replays that one's
// searches and fetches. "" when there was none before, or the same web.
func (s *Syncer) sessionWebChanged(ctx context.Context, tx pgx.Tx, r phaseRun, web delivery.Web) (string, error) {
	raw, err := json.Marshal(web)
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `UPDATE runs SET web = $2::jsonb WHERE id = $1`, r.ID, raw); err != nil {
		return "", fmt.Errorf("record the brainstorm's web: %w", err)
	}
	var before delivery.Web
	err = tx.QueryRow(ctx, `SELECT p.web FROM runs p JOIN runs r ON r.id = $2
		WHERE p.session_id = $1 AND p.id <> r.id AND p.role = 'brainstorm' AND p.web IS NOT NULL AND p.created_at <= r.created_at
		ORDER BY p.created_at DESC, p.id DESC LIMIT 1`, r.SessionID, r.ID).Scan(&before)
	if db.IsNotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("load the session's earlier web: %w", err)
	}
	if before.Equal(web) {
		return "", nil
	}
	return delivery.WebChanged(before, web), nil
}

// linkedTold is what a session's agent is told of a repository a member
// linked, checked out at path.
func linkedTold(key, name, path string) string {
	return linked.Render(struct{ Key, Name, Path string }{key, name, path})
}

var (
	linked        = delivery.MustText("notices/linked")
	checkoutAgain = delivery.MustText("notices/checkout-again")
)
