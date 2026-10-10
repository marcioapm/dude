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

// resumeSizes is what a resume of the preview's lux Run reads in tx: the
// lux_start_event the resume's answer is newer than (startBefore), the
// size runs.machine records, and the one its project's preview settings
// name now.
type resumeSizes struct {
	startBefore      int64
	recorded, target *delivery.Machine
}

func loadResumeSizes(ctx context.Context, tx pgx.Tx, runID string) (resumeSizes, error) {
	var out resumeSizes
	var machine, rawSettings []byte
	if err := tx.QueryRow(ctx, `SELECT r.lux_start_event, r.machine, preview_settings(p)
		FROM runs r JOIN projects p ON p.id = r.project_id WHERE r.id = $1`, runID).Scan(&out.startBefore, &machine, &rawSettings); err != nil {
		return out, err
	}
	var settings PreviewSettings
	if err := json.Unmarshal(rawSettings, &settings); err != nil {
		return out, err
	}
	var err error
	if out.recorded, err = phases.ScanMachine(machine); err != nil {
		return out, err
	}
	out.target, err = previewSize(ctx, tx, settings)
	return out, err
}

// plan is what resuming the preview's lux Run (before, as read) does about
// its size (phases.PlanResize).
func (s resumeSizes) plan(ctx context.Context, c lux.Client, before lux.Run) *phases.ResizePlan {
	return phases.PlanResize(ctx, c, s.recorded, s.target, before)
}

// recordResize records what lux's answer to the resume says the preview's
// Run is on (phases.RecordResize).
func (p *Previews) recordResize(ctx context.Context, r previewRun, plan *phases.ResizePlan, answer lux.Run) {
	ref := delivery.RunRef{Org: r.Org, ProjectID: r.ProjectID, TaskID: r.TaskID, RunID: r.ID}
	phases.RecordResize(ctx, p.DB, p.Lux, p.Log, ref, r.LuxRunID, plan, answer)
}
