package forge

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
)

// Resolver finds an organization's GitHub client from its stored credential.
type Resolver struct {
	DB          *db.DB
	TestGitHost string
}

// For returns nil, nil when the organization has no forge credential: a
// local repository needs none, and not every step needs a forge. The
// client carries the organization's GitHub settings.
func (r Resolver) For(ctx context.Context, org string) (*GitHub, error) {
	var c Credential
	var settings []byte
	err := r.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT auth::text, secret, api_base_url, settings FROM forge_credentials WHERE forge = 'github' LIMIT 1`).
			Scan(&c.Auth, &c.Secret, &c.APIBaseURL, &settings)
	})
	if db.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	gh := NewGitHub(c, WithTestGitHost(r.TestGitHost))
	gh.Settings = ReadSettings(settings)
	return gh, nil
}
