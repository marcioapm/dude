package phases

import (
	"cmp"
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/ledger"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// EvRunStage is where an agent's Run is while it starts, as lux announced
// it (RunStage in packages/domain/src/events/types.ts). Payload: { stage,
// since, epoch, reason? }, since in UTC.
const EvRunStage = "run.stage"

// stageParked is the stage a move's stop is written as: the Run stopped
// with its work kept, before it waits for a host elsewhere.
const stageParked = "park"

// startStages are lux's stages of a start, written while the Run starts.
var startStages = map[string]bool{"waiting": true, "image": true, "volumes": true, "repositories": true, "container": true, "running": true}

// StartStageJSON (SQL, over runs r): where the Run's start is, as the API
// gives it ({stage, since, epoch} from its latest run.stage), while it is
// live and lux has not reported it running; NULL otherwise. The ledger is
// the source: no column holds it. Its twin over runs is the control
// plane's startStage (apps/control-plane/src/api/routes/work.ts).
const StartStageJSON = `(SELECT json_build_object('stage', e.payload->>'stage', 'since', e.payload->>'since',
		'epoch', (e.payload->>'epoch')::int)
	FROM events e WHERE e.run_id = r.id AND e.event_type = 'run.stage'
	  AND r.status IN ('scheduled', 'starting', 'running') AND COALESCE(r.lux_state, '') <> 'running'
	ORDER BY e.cursor DESC LIMIT 1)`

// stage records lux's stage event while the Run is starting: each stage
// before running, and running while lux's state is not yet running (the
// harness coming up). A move's stopping (a drain, a preemption, a
// migrate) is written as park; any other stopping and the resting stages
// are not, since the state events already say them. lux announces each
// change once, so there is one event per stage and no dedupe.
func (t *translator) stage(ctx context.Context, tx pgx.Tx, s *Syncer, f lux.Frame) error {
	var ev struct {
		Stage  string     `json:"stage"`
		Since  *time.Time `json:"since"`
		Epoch  int        `json:"epoch"`
		Reason string     `json:"reason"`
	}
	if json.Unmarshal(f.EventData, &ev) != nil {
		return nil
	}
	stage := ev.Stage
	switch {
	case stage == "stopping" && lux.Moved(ev.Reason):
		stage = stageParked
	case !startStages[stage]:
		return nil
	}
	if stage == "running" {
		var luxState string
		if err := tx.QueryRow(ctx, `SELECT COALESCE(lux_state, '') FROM runs WHERE id = $1`, t.run.ID).Scan(&luxState); err != nil {
			return err
		}
		if luxState == "running" {
			return nil
		}
	}
	payload := map[string]any{"stage": stage, "epoch": cmp.Or(ev.Epoch, f.Epoch)}
	if ev.Since != nil {
		payload["since"] = ev.Since.UTC()
	}
	if ev.Reason != "" {
		payload["reason"] = ev.Reason
	}
	return s.event(ctx, tx, t.run, EvRunStage, ledger.ActorSystem, payload)
}

// beforeRunning says lux state was is one of a start's, before lux
// reported the Run running: none yet, waiting for a host, or placed and
// starting. A Run that ran ends from running or stopping instead.
func beforeRunning(was string) bool {
	return was == "" || lux.Waiting(was) || was == "scheduled" || was == "starting"
}

// failedStage is the stage a start that failed was in: the last run.stage
// of epoch, or of a resume's waiting, which lux announces under the epoch
// it left (it moves the epoch at assignment). That waiting leg also matches
// the first start's waiting when a resume fails before lux announces its
// own; the resume then names waiting, which it was (waiting for a host).
// was is lux_state before this event. "" when was is not a start's
// (beforeRunning), so an end after running costs no query; when the last
// stage is park, a move's stop rather than a start (a Run whose
// resumePolicy fails it on a move); and from a lux with no stages.
func (t *translator) failedStage(ctx context.Context, tx pgx.Tx, epoch int, was string) (string, error) {
	if !beforeRunning(was) {
		return "", nil
	}
	var stage string
	err := tx.QueryRow(ctx, `SELECT payload->>'stage' FROM events WHERE run_id = $1 AND event_type = $2
		AND ($3 = 0 OR (payload->>'epoch')::int = $3 OR (payload->>'stage' = 'waiting' AND (payload->>'epoch')::int = $3 - 1))
		ORDER BY cursor DESC LIMIT 1`, t.run.ID, EvRunStage, epoch).Scan(&stage)
	if err == pgx.ErrNoRows || stage == stageParked {
		return "", nil
	}
	return stage, err
}
