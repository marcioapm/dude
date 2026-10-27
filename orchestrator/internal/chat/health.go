package chat

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
)

// NoteFailure counts a failed call to provider for org's installation's
// health (last_failure, failed_today), as every chat call's failure is
// counted; nil failure is a no-op. For callers outside the chat service
// that call a provider directly (an automation's post_to_chat).
func NoteFailure(ctx context.Context, d *db.DB, log *slog.Logger, org, provider string, failure error) {
	if failure == nil {
		return
	}
	if log == nil {
		log = slog.Default()
	}
	log.Warn("chat provider call failed", "organization", org, "provider", provider, "error", failure)
	_ = d.InOrg(context.WithoutCancel(ctx), org, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE chat_installations SET last_failure = $2,
			failed_today = CASE WHEN failures_day = current_date THEN failed_today + 1 ELSE 1 END, failures_day = current_date
			WHERE provider = $1`, provider, clip(failure.Error(), 500))
		return err
	})
}
