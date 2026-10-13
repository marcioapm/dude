package phases

import (
	"slices"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// A GitHub App's Run commits as its bot, on every harness and the scripted
// agent; a PAT's sets no identity, leaving the image's.
func TestOnlyAnAppsRunCommitsAsItsBot(t *testing.T) {
	bot := &forge.BotIdentity{Name: "dude-app[bot]", Email: "42+dude-app[bot]@users.noreply.github.com"}
	for _, harness := range []string{"", delivery.HarnessClaudeCode, delivery.HarnessCodex} {
		for _, model := range []string{"claude-opus-5-5", "fake/scripted"} {
			c, in := goldenInput(model)
			in.Harness = harness
			pat := buildSpec(c, in).Env
			for _, k := range []string{"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL"} {
				if _, ok := pat[k]; ok {
					t.Errorf("%s/%s with a PAT sets %s", harness, model, k)
				}
			}
			in.Bot = bot
			env := buildSpec(c, in).Env
			if env["GIT_AUTHOR_NAME"] != bot.Name || env["GIT_COMMITTER_NAME"] != bot.Name ||
				env["GIT_AUTHOR_EMAIL"] != bot.Email || env["GIT_COMMITTER_EMAIL"] != bot.Email {
				t.Errorf("%s/%s with an App: env %v, want the bot as author and committer", harness, model, env)
			}
		}
	}
}

// Each repository clones with its own owner's token where an App's
// installation differs; GIT_TOKEN stays the first's, and a PAT's spec
// declares only it.
func TestARepositoryOfAnotherOwnerHasItsOwnTokenSecret(t *testing.T) {
	c, in := goldenInput("claude-opus-5-5")
	in.Repos[1].URL = "https://github.com/marcio-m/web.git"
	in.ForgeToken = "ghs_acme"
	in.RepoTokens = map[string]string{"api": "ghs_acme", "web": "ghs_marcio"}
	spec := buildSpec(c, in)
	creds := map[string]string{}
	for _, r := range spec.Git.Repositories {
		creds[r.Name] = r.Credential
	}
	if creds["api"] != "GIT_TOKEN" || creds["web"] != "GIT_TOKEN_MARCIO_M" {
		t.Errorf("credentials %v", creds)
	}
	values := map[string]string{}
	for _, s := range spec.Secrets {
		values[s.Name] = s.Value
	}
	if values["GIT_TOKEN"] != "ghs_acme" || values["GIT_TOKEN_MARCIO_M"] != "ghs_marcio" {
		t.Errorf("secrets %v", values)
	}
	if got := ownerOfSecret("GIT_TOKEN_MARCIO_M"); got != "marcio-m" {
		t.Errorf("ownerOfSecret = %q", got)
	}
}

// A resume sends an owner's token only where lux would take it as a
// credential: the stored spec declares it, or a repository the resume adds
// names it. Any other would be declared as the workload's, in the agent's
// environment.
func TestAResumeSendsOnlyTheOwnersTokensLuxDeclares(t *testing.T) {
	fresh := []lux.Secret{{Name: "GIT_TOKEN", Value: "a"}, {Name: "GIT_TOKEN_MARCIO", Value: "m"}, {Name: "DUDE_TOOLS_AUTH", Value: "t"}}
	names := func(list []lux.Secret) []string {
		var out []string
		for _, s := range list {
			out = append(out, s.Name)
		}
		return out
	}
	stored := lux.StoredSpec{Secrets: []lux.StoredSecret{{Name: "GIT_TOKEN", RunnerOnly: true}, {Name: "DUDE_TOOLS_AUTH"}}}
	if got := names(declaredTokens(fresh, stored, nil)); !slices.Equal(got, []string{"GIT_TOKEN", "DUDE_TOOLS_AUTH"}) {
		t.Errorf("stored spec declares GIT_TOKEN only: sent %v", got)
	}
	added := []lux.Repository{{Name: "web", Credential: "GIT_TOKEN_MARCIO"}}
	if got := names(declaredTokens(fresh, stored, added)); !slices.Equal(got, []string{"GIT_TOKEN", "GIT_TOKEN_MARCIO", "DUDE_TOOLS_AUTH"}) {
		t.Errorf("an added repository names GIT_TOKEN_MARCIO: sent %v", got)
	}
	stored.Secrets = append(stored.Secrets, lux.StoredSecret{Name: "GIT_TOKEN_MARCIO", RunnerOnly: true})
	if got := names(declaredTokens(fresh, stored, nil)); !slices.Equal(got, []string{"GIT_TOKEN", "GIT_TOKEN_MARCIO", "DUDE_TOOLS_AUTH"}) {
		t.Errorf("stored spec declares GIT_TOKEN_MARCIO: sent %v", got)
	}
}
