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
	// Target's id, when lux's pool list put it in another pool (Machine.OtherPool).
	otherPool string
	// lux's pool list could not be read, so whether Target is in the Run's
	// pool is not known: nothing is sent and nothing recorded.
	unknown bool
	// lux's refusal of Send, after which the resume went without it.
	refused *lux.Error
}

// PlanResize compares the Run's recorded size with target and decides what
// the resume sends. before is the lux Run as read before the resume.
//   - Same CPUs, memory and disk: nothing is sent.
//   - Settings naming a size that no longer exists (target is the default
//     only because of that): the Run keeps its size.
//   - A size in another pool: lux binds a Run to its pool at submit, and a
//     resume cannot move it; the Run keeps its size.
//   - lux's pool list failing: the Run resumes on its size, its record left
//     as it is.
func PlanResize(ctx context.Context, c lux.Client, recorded, target *delivery.Machine, before lux.Run) *ResizePlan {
	if recorded == nil || target == nil {
		return nil
	}
	p := &ResizePlan{Recorded: recorded, Target: target, SinceEpoch: nextEpoch(before)}
	if delivery.SameSize(*recorded, *target) {
		return p
	}
	if target.Missing != "" {
		p.Note = fmt.Sprintf("Its settings name a machine size that no longer exists, so it keeps %s.", recorded.Name)
		return p
	}
	match, err := poolOf(ctx, c, recorded, target, before.PoolID)
	switch {
	case err != nil:
		slog.Warn("reading lux's pools failed; the Run resumes on its size", "luxRun", before.ID, "error", err)
		p.unknown = true
	case match == poolSame:
		p.Send = &lux.Resources{CPUs: target.CPUs, Memory: target.MemoryMiB << 20, Disk: target.DiskGiB << 30}
	default:
		if match == poolNotDefault {
			p.otherPool = target.SizeID
		}
		p.Note = fmt.Sprintf("Its settings now name %s, in another pool: a stopped Run cannot change pools, so it keeps %s. A new Run gets %s.",
			target.Name, recorded.Name, target.Name)
	}
	return p
}

type poolMatch int

const (
	poolSame poolMatch = iota
	poolOther
	// Another pool: lux's list says the Run's pool is not its default, and
	// the target names none.
	poolNotDefault
)

// poolOf says whether target's pool places a Run where recorded's did.
// Null is the tenant's default pool in lux, so when lux says which pool
// the Run is bound to (luxPool), a target naming a pool is compared with
// it, and one naming none with lux's default; with no answer from lux,
// only equal ids are the same pool. The pool list is read only for a
// target naming none on a Run in a named pool, and not again for the
// target it last said was elsewhere (recorded.OtherPool).
func poolOf(ctx context.Context, c lux.Client, recorded, target *delivery.Machine, luxPool string) (poolMatch, error) {
	deref := func(s *string) string {
		if s == nil {
			return ""
		}
		return *s
	}
	want := deref(target.PoolID)
	switch {
	case deref(recorded.PoolID) == want:
		return poolSame, nil
	case luxPool == "":
		return poolOther, nil
	case want != "":
		if want == luxPool {
			return poolSame, nil
		}
		return poolOther, nil
	case recorded.OtherPool != "" && recorded.OtherPool == target.SizeID:
		return poolNotDefault, nil
	}
	pools, err := c.Pools(ctx)
	if err != nil {
		return 0, err
	}
	for _, p := range pools {
		if p.IsDefault && p.ID == luxPool {
			return poolSame, nil
		}
	}
	return poolNotDefault, nil
}

// ResumeSized resumes with the plan's resources. lux refusing them — a Run
// already resuming with others (409 not_resumable: lux's docs make a resume
// without resources the retry), a lux from before lux#51 that does not know
// them (400), or lux refusing these resources (422) — is answered by the
// same resume without them.
func ResumeSized(ctx context.Context, c lux.Client, runID string, in lux.ResumeInput, plan *ResizePlan) (lux.Run, error) {
	if plan == nil || plan.Send == nil || plan.refused != nil {
		in.Resources = nil
		return c.Resume(ctx, runID, in)
	}
	in.Resources = plan.Send
	res, err := c.Resume(ctx, runID, in)
	if le, ok := lux.AsError(err); ok && resourcesRefused(le) {
		slog.Warn("lux refused a resume's resources; resuming without them", "luxRun", runID,
			"status", le.Status, "code", le.Code, "error", le.Message)
		plan.refused = le
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
// not say, or the plan could not tell, and nothing is recorded. sinceEpoch
// moves only with the size's numbers or its id: a resume that changes
// neither (a disk lux keeps again) records nothing new.
func (p *ResizePlan) Outcome(answer lux.Run) *delivery.Machine {
	if p == nil || p.unknown {
		return nil
	}
	rec := *p.Recorded
	rec.Note, rec.DiskKept, rec.OtherPool = p.Note, nil, p.otherPool
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
	got := delivery.Machine{CPUs: applied.CPUs, MemoryMiB: applied.Memory >> 20, DiskGiB: applied.Disk >> 30}
	partial := fmt.Sprintf("Its settings name %s; lux applied only part of it, as shown.", p.Target.Name)
	switch {
	case answer.Resize == nil && delivery.SameSize(got, rec):
		// lux left the Run as it was; numbers a partial apply recorded
		// keep that note unless lux refused the resources this time.
		if rec.Note = p.keptNote(); p.refused == nil && p.Recorded.Note == partial {
			rec.Note = partial
		}
		return &rec
	case answer.Resize == nil && !delivery.SameSize(got, *p.Target):
		// On neither size: a lux that applied part of it (one from before
		// lux#51 grows only the disk), or a retry of a resume lux had taken
		// with other resources. It keeps its size's name.
		rec.Note = partial
	default:
		rec.SizeID, rec.Name, rec.From = p.Target.SizeID, p.Target.Name, p.Target.From
	}
	if r := answer.Resize; r != nil && r.Disk != nil {
		rec.DiskKept = &delivery.DiskKept{RequestedGiB: r.Disk.Requested >> 30, Reason: r.Disk.Reason}
	}
	if !delivery.SameSize(got, rec) || rec.SizeID != p.Recorded.SizeID {
		rec.CPUs, rec.MemoryMiB, rec.DiskGiB = got.CPUs, got.MemoryMiB, got.DiskGiB
		rec.SinceEpoch = p.SinceEpoch
	}
	return &rec
}

// keptNote says why the resume left the Run on its size. Only a Run lux
// was resuming already (409) may get its size at the next resume: a lux
// from before lux#51 (400) never changes cpus or memory; a 422 is lux
// refusing these resources.
func (p *ResizePlan) keptNote() string {
	kept := fmt.Sprintf("Its settings now name %s, but this resume kept %s", p.Target.Name, p.Recorded.Name)
	switch le := p.refused; {
	case le == nil:
		return kept + " (lux did not apply it)."
	case le.Status == http.StatusConflict:
		return kept + " (lux said: " + le.Message + "). The next resume tries again."
	case le.Status == http.StatusBadRequest:
		return kept + ": this lux cannot change a stopped Run's CPUs or memory."
	default:
		return kept + " (lux refused its resources)."
	}
}

// runSizes is the size an agent Run's role names now (nil: the
// organization has none) and, for a resume, the one runs.machine records.
type runSizes struct {
	Now, Recorded *delivery.Machine
}

// RecordResize writes the plan's outcome over runs.machine when it differs
// from what is recorded, with run.resized. An answer without the Run's
// resources (only a lux that omits spec.resources) is read again with a
// GET, so what is recorded is lux's word. The memory limit lux reported is
// kept while sinceEpoch is: it is of a placement on this size. A failed
// read or write is logged; the next resume compares and records again.
func RecordResize(ctx context.Context, d *db.DB, c lux.Client, log *slog.Logger, ref delivery.RunRef, luxRunID string, plan *ResizePlan, answer lux.Run) {
	if log == nil {
		log = slog.Default()
	}
	if plan == nil || plan.unknown {
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
	samePlacement := rec.SinceEpoch == plan.Recorded.SinceEpoch
	err = d.InOrg(ctx, ref.Org, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE runs SET machine = $2::jsonb ||
				CASE WHEN $3 AND machine ? 'memoryLimit' THEN jsonb_build_object('memoryLimit', machine->'memoryLimit') ELSE '{}' END
			WHERE id = $1`, ref.RunID, now, samePlacement); err != nil {
			return err
		}
		_, err := ledger.Append(ctx, tx, ref.Event(EvRunResized, ledger.ActorSystem, map[string]any{"machine": rec}))
		return err
	})
	if err != nil && ctx.Err() == nil {
		log.Warn("recording a resumed Run's size failed", "run", ref.RunID, "error", err)
	}
}
