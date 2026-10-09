package phases

// Resizing on resume (lux#51): a parked Run resumes at the size its
// settings name now. lux applies cpus and memory either way, a larger disk
// always and a smaller one only when its saved state fits (else it keeps
// the disk, saying why). A Run stays in the pool it was submitted to, so a
// size in another pool is not applied. runs.machine records what lux's
// answer says the Run has, never what was asked.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// EvRunResized: a resume changed what runs.machine says the Run is on (its
// size, or why it is not on its settings' size). Payload {machine}.
const EvRunResized = "run.resized"

// ResizePlan is what one resume of a parked Run does about its size.
type ResizePlan struct {
	// runs.machine before the resume, and the size its settings name now.
	Recorded, Target *delivery.Machine
	// The resources the resume asks for; nil asks for none.
	Send *lux.Resources
	// Why Target is not applied, when it differs and is not.
	Note string
	// The epoch the resumed placement will have.
	SinceEpoch int
	// lux's refusal of Send, after which the resume went without it.
	refused string
}

// PlanResize compares the Run's recorded size with target and decides what
// the resume sends. before is the lux Run as read before the resume.
//   - Same CPUs, memory and disk: nothing is sent.
//   - Settings naming a size that no longer exists (target is the default
//     only because of that): the Run keeps its size.
//   - A size in another pool: lux binds a Run to its pool at submit, and a
//     resume cannot move it; the Run keeps its size.
func PlanResize(ctx context.Context, c lux.Client, recorded, target *delivery.Machine, before lux.Run) *ResizePlan {
	if recorded == nil || target == nil {
		return nil
	}
	p := &ResizePlan{Recorded: recorded, Target: target, SinceEpoch: nextEpoch(before)}
	if delivery.SameSize(*recorded, *target) {
		return p
	}
	switch {
	case target.Missing != "":
		p.Note = fmt.Sprintf("Its settings name a machine size that no longer exists, so it keeps %s.", recorded.Name)
	case !samePool(ctx, c, recorded.PoolID, target.PoolID, before.PoolID):
		p.Note = fmt.Sprintf("Its settings now name %s, in another pool: a stopped Run cannot change pools, so it keeps %s. A new Run gets %s.",
			target.Name, recorded.Name, target.Name)
	default:
		p.Send = &lux.Resources{CPUs: target.CPUs, Memory: target.MemoryMiB << 20, Disk: target.DiskGiB << 30}
	}
	return p
}

// samePool: a size in pool target places a Run where one in recorded did.
// Null is the tenant's default pool in lux, so when lux says which pool the
// Run is bound to (luxPool), a target naming none is compared with lux's
// default; with no answer from lux, only equal ids are the same pool.
func samePool(ctx context.Context, c lux.Client, recorded, target *string, luxPool string) bool {
	deref := func(s *string) string {
		if s == nil {
			return ""
		}
		return *s
	}
	if deref(recorded) == deref(target) {
		return true
	}
	if luxPool == "" {
		return false
	}
	want := deref(target)
	if want == "" {
		pools, err := c.Pools(ctx)
		if err != nil {
			return false
		}
		for _, p := range pools {
			if p.IsDefault {
				want = p.ID
			}
		}
	}
	return want == luxPool
}

// ResumeSized resumes with the plan's resources. lux refusing them — a Run
// already resuming with others (409 not_resumable: lux's docs make a resume
// without resources the retry), or a lux from before resizing that does not
// know them (400, 422) — is answered by the same resume without them.
func ResumeSized(ctx context.Context, c lux.Client, runID string, in lux.ResumeInput, plan *ResizePlan) (lux.Run, error) {
	if plan == nil || plan.Send == nil || plan.refused != "" {
		in.Resources = nil
		return c.Resume(ctx, runID, in)
	}
	in.Resources = plan.Send
	res, err := c.Resume(ctx, runID, in)
	if le, ok := lux.AsError(err); ok && resourcesRefused(le) {
		plan.refused = le.Message
		in.Resources = nil
		return c.Resume(ctx, runID, in)
	}
	return res, err
}

func resourcesRefused(le *lux.Error) bool {
	switch le.Status {
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return le.Code != "secrets_required"
	case http.StatusConflict:
		return le.Code == "not_resumable"
	}
	return false
}

// Outcome is runs.machine after a resume lux accepted, from its answer:
// what its resize applied, else what its stored spec holds (an older lux,
// or a retry without resources, answers no resize). nil: the answer does
// not say, and nothing is recorded.
func (p *ResizePlan) Outcome(answer lux.Run) *delivery.Machine {
	if p == nil {
		return nil
	}
	rec := *p.Recorded
	rec.Note, rec.DiskKept = p.Note, nil
	if p.Send == nil {
		return &rec
	}
	var applied lux.Resources
	switch {
	case answer.Resize != nil:
		applied = answer.Resize.Applied
	case answer.Spec.Resources != nil:
		applied = *answer.Spec.Resources
	default:
		return nil
	}
	cpus, mem, disk := applied.CPUs, applied.Memory>>20, applied.Disk>>30
	if answer.Resize == nil && cpus == rec.CPUs && mem == rec.MemoryMiB && disk == rec.DiskGiB {
		why := "lux did not apply it"
		if p.refused != "" {
			why = "lux said: " + p.refused
		}
		rec.Note = fmt.Sprintf("Its settings now name %s, but this resume kept %s (%s). The next resume tries again.", p.Target.Name, rec.Name, why)
		return &rec
	}
	rec.SizeID, rec.Name, rec.From = p.Target.SizeID, p.Target.Name, p.Target.From
	rec.CPUs, rec.MemoryMiB, rec.DiskGiB = cpus, mem, disk
	rec.SinceEpoch, rec.Note = p.SinceEpoch, ""
	if r := answer.Resize; r != nil && r.Disk != nil {
		rec.DiskKept = &delivery.DiskKept{RequestedGiB: r.Disk.Requested >> 30, Reason: r.Disk.Reason}
	} else if answer.Resize == nil && (cpus != p.Target.CPUs || mem != p.Target.MemoryMiB || disk != p.Target.DiskGiB) {
		// No resize in the answer, and lux's spec is neither size: a lux
		// that applied part of it (one from before lux#51 grows only the
		// disk), or a retry of a resume lux had taken with other resources.
		rec.Note = fmt.Sprintf("Its settings name %s; lux gave it only part of that, as shown.", p.Target.Name)
	}
	return &rec
}

// LoadMachine is runs.machine; nil for a Run that recorded none.
func LoadMachine(ctx context.Context, tx pgx.Tx, runID string) (*delivery.Machine, error) {
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT machine FROM runs WHERE id = $1`, runID).Scan(&raw); err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, nil
	}
	var m delivery.Machine
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// RecordResize writes the plan's outcome over runs.machine when it differs
// from what is recorded, with run.resized. An answer that says nothing of
// the Run's resources is read again from lux (GET), so what is recorded is
// always lux's word. The memory limit lux reported is kept while the size
// is: it is the size's, on another placement. A failed read or write is
// logged: the next resume compares and records again.
func RecordResize(ctx context.Context, d *db.DB, c lux.Client, log *slog.Logger, ref delivery.RunRef, luxRunID string, plan *ResizePlan, answer lux.Run) {
	if log == nil {
		log = slog.Default()
	}
	if plan == nil {
		return
	}
	if plan.Send != nil && answer.Resize == nil && answer.Spec.Resources == nil {
		var err error
		if answer, err = c.Get(ctx, luxRunID); err != nil {
			log.Warn("reading a resumed Run's size from lux failed", "run", ref.RunID, "error", err)
			return
		}
	}
	rec := plan.Outcome(answer)
	if rec == nil {
		return
	}
	was, _ := json.Marshal(plan.Recorded)
	now, err := json.Marshal(rec)
	if err != nil || bytes.Equal(was, now) {
		return
	}
	same := delivery.SameSize(*plan.Recorded, *rec) && plan.Recorded.SizeID == rec.SizeID
	err = d.InOrg(ctx, ref.Org, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE runs SET machine = $2::jsonb ||
				CASE WHEN $3 AND machine ? 'memoryLimit' THEN jsonb_build_object('memoryLimit', machine->'memoryLimit') ELSE '{}' END
			WHERE id = $1`, ref.RunID, now, same); err != nil {
			return err
		}
		_, err := ledger.Append(ctx, tx, ref.Event(EvRunResized, ledger.ActorSystem, map[string]any{"machine": rec}))
		return err
	})
	if err != nil && ctx.Err() == nil {
		log.Warn("recording a resumed Run's size failed", "run", ref.RunID, "error", err)
	}
}
