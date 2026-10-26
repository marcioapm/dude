package chat

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// Installed is the organisation's installation of provider, read in tx (the
// organisation's); false without one. For callers outside this package that
// post as the organisation's bot (an automation's post_to_chat).
func Installed(ctx context.Context, tx pgx.Tx, provider string) (Installation, bool, error) {
	return loadInstallation(ctx, tx, provider)
}
