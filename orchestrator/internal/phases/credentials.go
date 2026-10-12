package phases

import (
	"context"
	"log/slog"
	"slices"
	"sync/atomic"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// CredentialRepo is a repository lux clones with a forge token: its name
// (as dude or lux names it) and clone URL.
type CredentialRepo struct{ Name, URL string }

// forgeTokens are the tokens a Run's repositories clone and push with:
// the first remote repository's as the Run's token, and every remote
// repository's own by spec name. A PAT is the same for all; a GitHub App's
// is minted for the installation covering each repository's owner. A
// local repository (no slug) takes the Run's.
func forgeTokens(ctx context.Context, gh *forge.GitHub, repos []specRepo) (string, map[string]string, error) {
	perRepo := map[string]string{}
	first := ""
	for _, r := range repos {
		slug := forge.SlugFromURL(r.URL)
		if slug == "" {
			continue
		}
		token, err := gh.Token(ctx, slug)
		if err != nil {
			return "", nil, err
		}
		perRepo[lux.SpecName(r.Name)] = token
		if first == "" {
			first = token
		}
	}
	if first == "" {
		token, err := gh.Token(ctx, "")
		if err != nil {
			return "", nil, err
		}
		first = token
	}
	return first, perRepo, nil
}

// botOf is a GitHub App's bot identity for a Run's commits; nil for a PAT.
func botOf(ctx context.Context, gh *forge.GitHub) (*forge.BotIdentity, error) {
	if !gh.IsApp() {
		return nil, nil
	}
	bot, err := gh.Bot(ctx)
	if err != nil {
		return nil, err
	}
	return &bot, nil
}

// RepoCredentials are, for a spec built outside this package (a branch
// preview), the credential secret each repository names, by spec name, and
// the secrets to declare: the same scheme as a phase Run's
// (credentialSecrets). gh nil: none.
func RepoCredentials(ctx context.Context, gh *forge.GitHub, repos []CredentialRepo) (map[string]string, []lux.Secret, error) {
	if gh == nil {
		return map[string]string{}, nil, nil
	}
	in := specInput{}
	for _, r := range repos {
		in.Repos = append(in.Repos, specRepo{Name: r.Name, URL: r.URL})
	}
	var err error
	if in.ForgeToken, in.RepoTokens, err = forgeTokens(ctx, gh, in.Repos); err != nil {
		return nil, nil, err
	}
	s := credentialSecrets(in)
	return s.name, s.list, nil
}

// OperationCredentials are fresh tokens for a push or sync lux makes in a
// running Run, by lux's name for each repository the Run holds (have, as
// runs.lux_repositories): its own owner's for a GitHub App, the PAT
// otherwise, so both go the same way. repos give each one's clone URL; a
// repository lux holds that repos do not name gets none. gh nil: none.
func OperationCredentials(ctx context.Context, gh *forge.GitHub, have []string, repos []CredentialRepo) (map[string]string, error) {
	if gh == nil || len(have) == 0 {
		return nil, nil
	}
	out := map[string]string{}
	for _, r := range repos {
		name := lux.SpecName(r.Name)
		if !slices.Contains(have, name) {
			continue
		}
		if _, done := out[name]; done {
			continue
		}
		token, err := gh.Token(ctx, forge.SlugFromURL(r.URL))
		if err != nil {
			return nil, err
		}
		out[name] = token
	}
	return out, nil
}

// warnedNoCredentials makes the warning about a lux that takes no
// credentials on a push or sync once per process.
var warnedNoCredentials atomic.Bool

// withCredentials does op with credentials, and again without them when
// lux is a runner from before credentials on push and sync (409
// push_credentials_unsupported): that one goes on with the token its Run
// was started or resumed with, which a PAT's is and an App's may no
// longer be.
func withCredentials(log *slog.Logger, credentials map[string]string, op func(map[string]string) error) error {
	err := op(credentials)
	if len(credentials) == 0 || !lux.CredentialsUnsupported(err) {
		return err
	}
	if log != nil && warnedNoCredentials.CompareAndSwap(false, true) {
		log.Warn("lux takes no fresh credentials on a push or sync (409 push_credentials_unsupported): pushing with the token each Run was started or resumed with; a GitHub App's expires within the hour, so upgrade lux")
	}
	return op(nil)
}

// Push asks lux to push a Run's checkouts with fresh credentials, falling
// back to none on a lux that takes none.
func Push(ctx context.Context, c lux.Client, log *slog.Logger, runID, requestID string, credentials map[string]string) error {
	return withCredentials(log, credentials, func(creds map[string]string) error {
		return c.Push(ctx, runID, requestID, creds)
	})
}

// SyncRun asks lux to move a running Run's checkouts, as Push.
func SyncRun(ctx context.Context, c lux.Client, log *slog.Logger, runID, requestID string, refs []lux.SyncRef, credentials map[string]string) error {
	return withCredentials(log, credentials, func(creds map[string]string) error {
		return c.SyncRun(ctx, runID, requestID, refs, creds)
	})
}

// runCredentials are OperationCredentials for one of this syncer's Runs: a
// task's repositories, or a session's.
func (s *Syncer) runCredentials(ctx context.Context, org, runID string) (map[string]string, error) {
	gh, err := s.Forges.For(ctx, org)
	if err != nil || gh == nil {
		return nil, err
	}
	var have []string
	var repos []CredentialRepo
	if err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		var taskID, sessionID string
		if err := tx.QueryRow(ctx, `SELECT lux_repositories, COALESCE(task_id, ''), COALESCE(session_id, '') FROM runs WHERE id = $1`, runID).
			Scan(&have, &taskID, &sessionID); err != nil {
			return err
		}
		if sessionID != "" {
			list, err := delivery.SessionRepositories(ctx, tx, sessionID)
			for _, r := range list {
				repos = append(repos, CredentialRepo{Name: r.SpecName(), URL: r.URL})
			}
			return err
		}
		list, err := delivery.TaskRepositories(ctx, tx, taskID)
		for _, r := range list {
			repos = append(repos, CredentialRepo{Name: r.Name, URL: r.URL})
		}
		return err
	}); err != nil {
		return nil, err
	}
	return OperationCredentials(ctx, gh, have, repos)
}

// declaredTokens drops from a resume's secrets an owner's token
// (GIT_TOKEN_<OWNER>, which only a GitHub App's spec names) that the Run's
// stored spec does not declare and no repository the resume adds names as
// its credential: lux would declare it on the resume as the workload's, in
// the agent's environment. GIT_TOKEN is left as before.
func declaredTokens(secrets []lux.Secret, stored lux.StoredSpec, added []lux.Repository) []lux.Secret {
	var out []lux.Secret
	for _, s := range secrets {
		declared := slices.ContainsFunc(stored.Secrets, func(d lux.StoredSecret) bool { return d.Name == s.Name }) ||
			slices.ContainsFunc(added, func(r lux.Repository) bool { return r.Credential == s.Name })
		if ownerOfSecret(s.Name) == "" || declared {
			out = append(out, s)
		}
	}
	return out
}

// ResumeCredentials are fresh values for every forge-token secret a stored
// spec declares (lux keeps none): GIT_TOKEN as repos' first remote
// repository's token, an owner's GIT_TOKEN_<OWNER> as that owner's
// installation's. Other secrets are the caller's.
func ResumeCredentials(ctx context.Context, gh *forge.GitHub, stored lux.StoredSpec, repos []CredentialRepo) ([]lux.Secret, error) {
	if gh == nil {
		return nil, nil
	}
	var out []lux.Secret
	for _, sec := range stored.Secrets {
		// dude declares its tokens runner-only; a preview's own secret of a
		// like name is the workload's and keeps the project's value.
		if !sec.RunnerOnly {
			continue
		}
		scope := ownerOfSecret(sec.Name)
		if sec.Name != gitTokenSecret && scope == "" {
			continue
		}
		if sec.Name == gitTokenSecret {
			for _, r := range repos {
				if slug := forge.SlugFromURL(r.URL); slug != "" {
					scope = slug
					break
				}
			}
		}
		token, err := gh.Token(ctx, scope)
		if err != nil {
			return nil, err
		}
		out = append(out, lux.Secret{Name: sec.Name, Value: token})
	}
	return out, nil
}
