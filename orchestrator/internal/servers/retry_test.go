package servers

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

// A preview whose sync failed is taken up again after 5 s, or after
// Previews.RetryBackOff where a test sets a positive one.
func TestAFailedPreviewSyncBacksOffForItsRetryBackOff(t *testing.T) {
	ctx := context.Background()
	app, owner := dbtest.Open(t)
	_, runs := previewRows(t, owner, 3, false)
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	for i, c := range []struct {
		set, want time.Duration
	}{{0, 5 * time.Second}, {-time.Second, 5 * time.Second}, {300 * time.Millisecond, 300 * time.Millisecond}} {
		p := &Previews{Service: &Service{DB: app, Log: quiet}, RetryBackOff: c.set}
		r := runs[i].previewRun
		var before time.Time
		if err := owner.QueryRow(ctx, `SELECT now()`).Scan(&before); err != nil {
			t.Fatal(err)
		}
		if err := p.retryLater(ctx, r); err != nil {
			t.Fatal(err)
		}
		var after, next time.Time
		if err := owner.QueryRow(ctx, `SELECT now(), next_attempt_at FROM runs WHERE id = $1`, r.ID).Scan(&after, &next); err != nil {
			t.Fatal(err)
		}
		if next.Before(before.Add(c.want)) || next.After(after.Add(c.want)) {
			t.Errorf("RetryBackOff %v: next attempt %v after the back-off began, want %v",
				c.set, next.Sub(before).Round(time.Millisecond), c.want)
		}
	}
}
