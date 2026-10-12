package phases

import (
	"context"
	"slices"

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
