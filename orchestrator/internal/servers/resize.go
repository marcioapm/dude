package servers

import (
	"context"

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

// planResize reads, in one transaction, the lux_start_event the resume's
// answer is newer than, the size runs.machine records and the one the
// project's preview settings name now, and plans the resume of the
// preview's lux Run (before, as read) from them (phases.PlanResize).
func (p *Previews) planResize(ctx context.Context, r previewRun, before lux.Run) (int64, *phases.ResizePlan, error) {
	var startBefore int64
	var recorded, target *delivery.Machine
	err := p.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		var settings PreviewSettings
		if err := tx.QueryRow(ctx, `SELECT r.lux_start_event, r.machine, preview_settings(p)
			FROM runs r JOIN projects p ON p.id = r.project_id WHERE r.id = $1`, r.ID).Scan(&startBefore, &recorded, &settings); err != nil {
			return err
		}
		var err error
		target, err = previewSize(ctx, tx, settings)
		return err
	})
	if err != nil {
		return 0, nil, err
	}
	return startBefore, phases.PlanResize(ctx, p.Lux, recorded, target, before), nil
}

// recordResize records what lux's answer to the resume says the preview's
// Run is on (phases.RecordResize).
func (p *Previews) recordResize(ctx context.Context, r previewRun, plan *phases.ResizePlan, answer lux.Run) {
	ref := delivery.RunRef{Org: r.Org, ProjectID: r.ProjectID, TaskID: r.TaskID, RunID: r.ID}
	phases.RecordResize(ctx, p.DB, p.Lux, p.Log, ref, r.LuxRunID, plan, answer)
}
