package phases

import (
	"context"
	"strings"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
	"github.com/marciomartins/dude/orchestrator/internal/registry"
)

// RegistryLogin is the login lux's runner pulls a Run's image with.
type RegistryLogin struct{ Registry, Credential string }

// registrySecret holds the registry login. Runner-only in lux: named by
// image.registryAuth, it never enters the container.
const registrySecret = "DUDE_REGISTRY_AUTH"

// Apply adds the login to spec as image.registryAuth and its secret; a nil
// login adds nothing.
func (l *RegistryLogin) Apply(spec *lux.Spec) {
	if l == nil {
		return
	}
	spec.Image.RegistryAuth = []lux.RegistryAuth{{Registry: l.Registry, Secret: registrySecret}}
	spec.Secrets = append(spec.Secrets, lux.Secret{Name: registrySecret, Value: l.Credential})
}

// LoginFor is the login a start of any Run dude submits or resumes pulls
// image with: p's, if image is in p's registry. A resume (stored is lux's
// copy of the Run's spec) logs in where its submit did, whatever p or the
// image are now, or returns errLoginUnavailable when p cannot: lux would
// refuse the resume. errRegistry is a login that cannot be had now.
func LoginFor(ctx context.Context, p registry.Provider, image string, stored *lux.StoredSpec) (*RegistryLogin, error) {
	var host string
	if p != nil {
		host = p.Registry()
	}
	if stored != nil {
		auth := stored.Image.RegistryAuth
		if len(auth) == 0 {
			// Started without one: lux would take an unnamed secret as the
			// workload's.
			return nil, nil
		}
		if host == "" || len(auth) != 1 || auth[0].Registry != host || auth[0].Secret != registrySecret {
			wanted := make([]string, len(auth))
			for i, a := range auth {
				wanted[i] = a.Registry
			}
			return nil, errLoginUnavailable{Registry: strings.Join(wanted, ", "), Configured: host}
		}
	} else if host == "" || registry.ImageRegistry(image) != host {
		// No login, or another registry: a project's own image gets no
		// credential of the factory's.
		return nil, nil
	}
	credential, err := p.Credential(ctx)
	if err != nil {
		return nil, errRegistry{err}
	}
	return &RegistryLogin{Registry: host, Credential: credential}, nil
}
