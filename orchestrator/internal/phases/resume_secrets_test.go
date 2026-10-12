package phases

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

type forgeFor func(ctx context.Context, org string) (*forge.GitHub, error)

func (f forgeFor) For(ctx context.Context, org string) (*forge.GitHub, error) { return f(ctx, org) }

// A resume supplies every secret lux's stored spec declares: GIT_TOKEN
// from the forge when the fresh spec has none; one dude has no value for
// fails it for good, naming it.
func TestAResumeSuppliesEverySecretLuxDeclares(t *testing.T) {
	gh := forge.NewGitHub(forge.Credential{Auth: "pat", Secret: "ghp_now"})
	s := &Syncer{Forges: forgeFor(func(context.Context, string) (*forge.GitHub, error) { return gh, nil })}
	stored := func(names ...string) lux.StoredSpec {
		var out lux.StoredSpec
		for _, n := range names {
			out.Secrets = append(out.Secrets, lux.StoredSecret{Name: n})
		}
		return out
	}
	fresh := []lux.Secret{{Name: "DUDE_TOOLS_AUTH", Value: "Bearer t"}}

	got, err := s.storedSecrets(context.Background(), phaseRun{}, stored("DUDE_TOOLS_AUTH", "GIT_TOKEN"), fresh)
	if err != nil || !slices.Equal(got, []lux.Secret{fresh[0], {Name: "GIT_TOKEN", Value: "ghp_now"}}) {
		t.Errorf("GIT_TOKEN declared, none fresh: %v, %v", got, err)
	}
	if len(fresh) != 1 {
		t.Errorf("the fresh spec's secrets were changed: %v", fresh)
	}
	// Carried by the fresh spec already: sent once, as it is.
	withToken := append(slices.Clone(fresh), lux.Secret{Name: "GIT_TOKEN", Value: "ghp_spec"})
	if got, err := s.storedSecrets(context.Background(), phaseRun{}, stored("GIT_TOKEN"), withToken); err != nil || !slices.Equal(got, withToken) {
		t.Errorf("GIT_TOKEN fresh: %v, %v", got, err)
	}

	// Not declared by lux, none fresh: not supplied, or lux would declare it
	// on the resume and hand the forge's token to the agent's environment.
	if got, err := s.storedSecrets(context.Background(), phaseRun{}, stored("DUDE_TOOLS_AUTH"), fresh); err != nil || !slices.Equal(got, fresh) {
		t.Errorf("GIT_TOKEN undeclared: %v, %v, want exactly %v", got, err, fresh)
	}

	var cannot errCannotResume
	_, err = s.storedSecrets(context.Background(), phaseRun{}, stored("DUDE_TOOLS_AUTH", "NPM_TOKEN"), fresh)
	if !errors.As(err, &cannot) || err.Error() != "lux's Run declares the secret NPM_TOKEN, which dude cannot supply on a resume" {
		t.Errorf("an unknown secret: %v", err)
	}

	none := &Syncer{Forges: forgeFor(func(context.Context, string) (*forge.GitHub, error) { return nil, nil })}
	if _, err := none.storedSecrets(context.Background(), phaseRun{}, stored("GIT_TOKEN"), nil); !errors.As(err, &cannot) {
		t.Errorf("GIT_TOKEN with no forge: %v, want errCannotResume", err)
	}
	down := &Syncer{Forges: forgeFor(func(context.Context, string) (*forge.GitHub, error) { return nil, errors.New("db down") })}
	if _, err := down.storedSecrets(context.Background(), phaseRun{}, stored("GIT_TOKEN"), nil); !passing(err) {
		t.Errorf("GIT_TOKEN with the forge unreadable: %v, want a passing error", err)
	}
}
