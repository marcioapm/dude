package servers

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
	"github.com/marciomartins/dude/orchestrator/internal/phases"
)

// previewSize is the size a project's previews run on now; nil when the
// organization has none.
func previewSize(ctx context.Context, tx pgx.Tx, settings PreviewSettings) (*delivery.Machine, error) {
	sizes, err := delivery.LoadSizes(ctx, tx)
	if err != nil {
		return nil, err
	}
	sizeID := ""
	if settings.MachineSize != nil {
		sizeID = *settings.MachineSize
	}
	if m, ok := sizes.ForPreview(sizeID); ok {
		return &m, nil
	}
	return nil, nil
}

// resizePlan is what resuming the preview's lux Run (before, as read) does
// about its size: the one its project's preview settings name now, against
// the one it recorded (phases.PlanResize).
func (p *Previews) resizePlan(ctx context.Context, r previewRun, before lux.Run) (*phases.ResizePlan, error) {
	var recorded, target *delivery.Machine
	if err := p.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		var raw []byte
		if err := tx.QueryRow(ctx, `SELECT preview_settings(p) FROM projects p WHERE p.id = $1`, r.ProjectID).Scan(&raw); err != nil {
			return err
		}
		var settings PreviewSettings
		if err := json.Unmarshal(raw, &settings); err != nil {
			return err
		}
		var err error
		if target, err = previewSize(ctx, tx, settings); err != nil {
			return err
		}
		recorded, err = phases.LoadMachine(ctx, tx, r.ID)
		return err
	}); err != nil {
		return nil, err
	}
	return phases.PlanResize(ctx, p.Lux, recorded, target, before), nil
}

// recordResize records what lux's answer to the resume says the preview's
// Run is on (phases.RecordResize).
func (p *Previews) recordResize(ctx context.Context, r previewRun, plan *phases.ResizePlan, answer lux.Run) {
	ref := delivery.RunRef{Org: r.Org, ProjectID: r.ProjectID, TaskID: r.TaskID, RunID: r.ID}
	phases.RecordResize(ctx, p.DB, p.Lux, p.Log, ref, r.LuxRunID, plan, answer)
}
