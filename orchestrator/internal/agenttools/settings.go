package agenttools

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// settings (capabilities.SettingsRead) is dude's settings as they apply to
// the caller, each value with the layer it is set in.

type settingsIn struct {
	Project string `json:"project,omitempty"`
	Task    string `json:"task,omitempty"`
	Section string `json:"section,omitempty"`
}

func readSettings(ctx context.Context, tx pgx.Tx, c Caller, in settingsIn) (map[string]any, error) {
	return map[string]any{}, nil
}
