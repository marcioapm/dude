package phases

// Resizing on resume (lux#51, pools and dry runs lux#93): a parked Run
// resumes at the size its settings or task layer name now, in that size's
// pool. lux applies cpus and memory either way, a larger disk always and
// a smaller one only when its saved state fits (else it keeps the disk,
// saying why). runs.machine records what lux's answer says the Run has,
// never what was asked.

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
	// The pool the resume asks for (lux's id); "" stays in its pool.
	Pool string
	// Why Target is not applied, when it differs and is not.
	Note string
	// The epoch the resumed placement will have.
	SinceEpoch int
	// lux's pool list could not be read, so whether Target is in the Run's
	// pool is not known: nothing is sent and nothing recorded.
	unknown bool
	// lux's refusal of Send, after which the resume went without it.
	refused *lux.Error
	// Records a refusal durably before the fallback resume is asked for,
	// so a process that dies in between resumes the same way.
	onRefused func(*lux.Error) error
	// The Run's pending machine move: what its agent is told when the
	// move applies, which kind it is ("raise" or "reset", "" none) and the
	// name of the size it asked for (a refused raise's layer is restored
	// before a restart's resume, so Target may no longer be it).
	moveTold, moveKind, moveTo string
	// The pending move's refusal in dude's words (delivery.RefusalOf); nil
	// without a move.
	refusalOf func(*lux.Error) delivery.MoveRefusal
	// lux's record of the Run before the resume: its pool and placements.
	before lux.Run
}

// movedInput puts what the agent is told of its move before input.
func (p *ResizePlan) movedInput(input string) string {
	if p == nil || p.moveTold == "" {
		return input
	}
	return joinInput(p.moveTold, input)
}

// keptInput is what the agent is told when lux refused the move for good:
// the size asked for, the one it kept, and why in dude's words, in place of
// input's claim that it moved. A plan with no move, or a 409, keeps input
// as it was.
func (p *ResizePlan) keptInput(le *lux.Error, input string) string {
	if p.moveKind == "" || le.Status == http.StatusConflict {
		return input
	}
	refusal := p.refusalWords(le)
	told := fmt.Sprintf("You asked for %s; lux kept you on %s: %s", p.moveTo, p.Recorded.Name, refusal.ForAgent())
	if p.moveKind == "reset" {
		// A person's Reset: the agent has nothing to ask instead.
		told = fmt.Sprintf("You were to move back to %s; lux kept you on %s: %s.", p.moveTo, p.Recorded.Name, refusal.ForPerson())
	}
	return joinInput(told, input)
}

// refusalWords is le about the move's size, in dude's words. An older
// lux's 400 is to placement when this resume changes pools.
func (p *ResizePlan) refusalWords(le *lux.Error) delivery.MoveRefusal {
	r := delivery.RefusalOf(le, delivery.Machine{Name: p.moveTo}, nil, p.before)
	if p.refusalOf != nil {
		r = p.refusalOf(le)
	}
	r.OtherPool = p.Pool != ""
	return r
}

func joinInput(first, rest string) string {
	if rest == "" {
		return first
	}
	return first + "\n\n" + rest
}

// PlanResize compares the recorded size and pool with the target. A pool
// list failure leaves the Run unchanged; a missing settings size does too.
func PlanResize(ctx context.Context, c lux.Client, recorded, target *delivery.Machine, before lux.Run) *ResizePlan {
	if recorded == nil || target == nil {
		return nil
	}
	p := &ResizePlan{Recorded: recorded, Target: target, SinceEpoch: nextEpoch(before), before: before}
	if target.Missing != "" {
		p.Note = fmt.Sprintf("Its settings name a machine size that no longer exists, so it keeps %s.", recorded.Name)
		return p
	}
	pool, err := poolOf(ctx, c, recorded, target, before.PoolID)
	if err != nil {
		slog.Warn("reading lux's pools failed; the Run resumes on its size", "luxRun", before.ID, "error", err)
		p.unknown = true
		return p
	}
	p.Pool = pool
	if !delivery.SameSize(*recorded, *target) || p.Pool != "" {
		p.Send = &lux.Resources{CPUs: target.CPUs, Memory: target.MemoryMiB << 20, Disk: target.DiskGiB << 30}
	}
	return p
}

// poolOf is the pool a resume to target asks lux for, "" for the Run's
// own, resolving a size naming no pool to lux's current default. The Run's
// binding wins over the recorded size's submit-time pool when lux says it.
func poolOf(ctx context.Context, c lux.Client, recorded, target *delivery.Machine, luxPool string) (string, error) {
	deref := func(s *string) string {
		if s == nil {
			return ""
		}
		return *s
	}
	want, current := deref(target.PoolID), luxPool
	if current == "" {
		current = deref(recorded.PoolID)
	}
	if want == current {
		return "", nil
	}
	if want != "" {
		return want, nil
	}
	pools, err := c.Pools(ctx)
	if err != nil {
		return "", err
	}
	for _, p := range pools {
		if p.IsDefault {
			if p.ID == current {
				return "", nil
			}
			return p.ID, nil
		}
	}
	return "", fmt.Errorf("lux reports no default pool")
}

// ValidateResize checks the exact sized resume without stopping the Run.
// Older lux refuses validateOnly with 400: the real resume keeps its safe
// fallback, so lack of dry-run support never fails the agent's Run. A plan
// lux's pools could not be read for is delivery.ErrPoolsUnread: there is
// nothing to check, and nothing may be parked.
func ValidateResize(ctx context.Context, c lux.Client, runID string, plan *ResizePlan) error {
	if plan != nil && plan.unknown {
		return delivery.ErrPoolsUnread
	}
	if plan == nil || plan.Send == nil {
		return nil
	}
	in := lux.ResumeInput{Resources: plan.Send, ValidateOnly: true}
	if plan.Pool != "" {
		in.Placement = &lux.PlacementSpec{PoolID: plan.Pool}
	}
	_, err := c.Resume(ctx, runID, in)
	if le, ok := lux.AsError(err); ok && le.Status == http.StatusBadRequest {
		return nil
	}
	return err
}

// ResumeSized resumes with the plan's resources and placement. lux refusing
// them — a Run already resuming with others (409 not_resumable: lux's docs
// make a resume without resources the retry), a lux from before lux#51 that
// does not know them (400), or lux refusing these resources (422) — is
// answered by the same resume without resources or placement. The input is
// rewritten for a pending machine move: told it moved when lux is asked for
// the size, told what lux kept and why after a 400 or 422; otherwise kept.
func ResumeSized(ctx context.Context, c lux.Client, runID string, in lux.ResumeInput, plan *ResizePlan) (lux.Run, error) {
	if plan == nil || plan.Send == nil || plan.refused != nil {
		in.Resources, in.Placement = nil, nil
		switch {
		case plan != nil && plan.refused != nil && plan.refused.Status != http.StatusConflict:
			in.Input = plan.keptInput(plan.refused, in.Input)
		case plan != nil && plan.refused == nil:
			in.Input = plan.movedInput(in.Input)
		}
		return c.Resume(ctx, runID, in)
	}
	asked := in.Input
	in.Resources, in.Input = plan.Send, plan.movedInput(asked)
	if plan.Pool != "" {
		in.Placement = &lux.PlacementSpec{PoolID: plan.Pool}
	}
	res, err := c.Resume(ctx, runID, in)
	if le, ok := lux.AsError(err); ok && resourcesRefused(le) {
		slog.Warn("lux refused a resume's resources; resuming without them", "luxRun", runID,
			"status", le.Status, "code", le.Code, "error", le.Message)
		plan.refused = le
		// A 409 is a resume under way, whose outcome the answer shows; 400
		// and 422 refuse these resources for good and are kept.
		if plan.onRefused != nil && le.Status != http.StatusConflict {
			if err := plan.onRefused(le); err != nil {
				return lux.Run{}, err
			}
		}
		in.Resources, in.Placement = nil, nil
		in.Input = plan.keptInput(le, asked)
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
	rec.Note, rec.DiskKept = p.Note, nil
	if p.Send != nil && answer.PoolID != "" {
		pool := answer.PoolID
		rec.PoolID = &pool
	}
	if answer.Placement != nil {
		pool, name := answer.Placement.PoolID, answer.Placement.Pool
		rec.PoolID, rec.Pool = &pool, &name
	}
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
		rec.Raised = p.Target.Raised
		if answer.PoolID == "" && answer.Placement == nil {
			rec.PoolID, rec.Pool = p.Target.PoolID, p.Target.Pool
		}
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
	case le.Status == http.StatusBadRequest && p.Pool != "":
		return kept + ": this lux cannot move a stopped Run to another pool."
	case le.Status == http.StatusBadRequest:
		return kept + ": this lux cannot change a stopped Run's CPUs or memory."
	default:
		return kept + " (lux refused its resources)."
	}
}

// pendingMove is a Run's unsettled machine move.
type pendingMove struct {
	ID, Role, Kind, To string
	From               struct {
		delivery.Machine
		PreviousRaise *delivery.Raise `json:"previousRaise"`
	}
	// lux's definitive refusal, kept before the fallback resume.
	Code, Message string
	// A Reset's person and note.
	By, Note string
}

func loadPendingMove(ctx context.Context, tx pgx.Tx, runID string) (*pendingMove, error) {
	var m pendingMove
	err := tx.QueryRow(ctx, `SELECT id, role, kind, COALESCE(to_size_id, ''), from_size, COALESCE(refused, ''), COALESCE(refused_message, ''),
			COALESCE(by_person, ''), COALESCE(reason, '')
		FROM machine_moves WHERE run_id = $1 AND settled_at IS NULL`, runID).Scan(&m.ID, &m.Role, &m.Kind, &m.To, &m.From, &m.Code, &m.Message, &m.By, &m.Note)
	if db.IsNotFound(err) {
		return nil, nil
	}
	return &m, err
}

// movePlan ties a resume's plan to its Run's pending move: what the agent
// is told when it applies, a refusal kept by an earlier attempt (resumed
// without resources, told why), and a new refusal kept before the
// fallback resume is asked for.
func (s *Syncer) movePlan(ctx context.Context, r phaseRun, plan *ResizePlan) error {
	if plan == nil {
		return nil
	}
	var move *pendingMove
	var person string
	var asked delivery.Machine
	if err := s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) (err error) {
		if move, err = loadPendingMove(ctx, tx, r.ID); err != nil || move == nil {
			return err
		}
		sizes, err := delivery.LoadSizes(ctx, tx)
		if err != nil {
			return err
		}
		asked = sizes.ByID[move.To]
		plan.moveTo = asked.Name
		if move.Kind != "reset" {
			return nil
		}
		person, err = delivery.PersonName(ctx, tx, move.By)
		return err
	}); err != nil || move == nil {
		return err
	}
	if plan.unknown {
		// The move's size cannot be planned without lux's pools, and an
		// unsized resume would never settle it: the resume is retried.
		return fmt.Errorf("machine move %s: lux's pools could not be read", move.ID)
	}
	if plan.moveTo == "" && plan.Target != nil {
		plan.moveTo = plan.Target.Name
		asked = *plan.Target
	}
	plan.moveKind = move.Kind
	// onRefused and keptInput both word the same refusal: lux's pools are
	// read once for it.
	var pools []lux.Pool
	poolsRead := false
	plan.refusalOf = func(le *lux.Error) delivery.MoveRefusal {
		if !poolsRead {
			pools, _ = s.Lux.Pools(ctx)
			poolsRead = true
		}
		return delivery.RefusalOf(le, asked, pools, plan.before)
	}
	// Told only of a move this resume asks lux for.
	if plan.Target != nil && plan.Note == "" {
		if move.Kind == "reset" {
			plan.moveTold = "Moved back to " + plan.Target.Name + " by " + person + "."
			if move.Note != "" {
				plan.moveTold = "Moved back to " + plan.Target.Name + " by " + person + ": " + move.Note
			}
		} else {
			where := ""
			if plan.Pool != "" {
				target := *plan.Target
				target.Pool = nil
				delivery.LoadPoolNames(ctx, s.Lux).Name(&target)
				if target.Pool != nil {
					where = " in " + *target.Pool
				}
			}
			plan.moveTold = fmt.Sprintf("You now run on %s (%s)%s. Start again any server or watcher you had running. Carry on.",
				plan.Target.Name, plan.Target.Spec(), where)
		}
	}
	if move.Code != "" || move.Message != "" {
		status := http.StatusUnprocessableEntity
		if move.Code == "bad_request" {
			// An older lux's unknown field, kept: told as one.
			status = http.StatusBadRequest
		}
		plan.refused = &lux.Error{Status: status, Code: move.Code, Message: move.Message}
	}
	plan.onRefused = func(le *lux.Error) error {
		// Kept in dude's words: the agent's resume and the person's notice
		// both read them, never lux's message with its pool ids.
		said := plan.refusalWords(le).ForPerson()
		return s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
			tag, err := tx.Exec(ctx, `UPDATE machine_moves SET refused = $2, refused_message = $3 WHERE id = $1 AND settled_at IS NULL`,
				move.ID, le.Code, said)
			if err != nil || tag.RowsAffected() == 0 {
				return err
			}
			return restoreLayer(ctx, tx, r, move)
		})
	}
	return nil
}

// restoreLayer puts back the layer a refused raise replaced, only while the
// layer is still the one this move wrote: a later Reset or raise wins.
func restoreLayer(ctx context.Context, tx pgx.Tx, r phaseRun, move *pendingMove) error {
	if move.Kind != "raise" {
		return nil
	}
	previous, _ := json.Marshal(move.From.PreviousRaise)
	var err error
	if r.SessionID != "" {
		_, err = tx.Exec(ctx, `UPDATE sessions SET agent_machine = NULLIF($2::jsonb, 'null'::jsonb) WHERE id = $1 AND agent_machine->>'moveId' = $3`, r.SessionID, previous, move.ID)
	} else {
		_, err = tx.Exec(ctx, `UPDATE tasks SET agent_machines = CASE WHEN $3::jsonb = 'null'::jsonb THEN agent_machines - $2 ELSE jsonb_set(agent_machines, ARRAY[$2], $3::jsonb) END WHERE id = $1 AND agent_machines->$2->>'moveId' = $4`, r.TaskID, move.Role, previous, move.ID)
	}
	return err
}

// settleMachine settles the Run's pending move from what lux reports the
// resumed Run has: moved when it is on the move's size, else refused. An
// answer that does not say leaves it for the next resume.
func (s *Syncer) settleMachine(ctx context.Context, r phaseRun, plan *ResizePlan, answer lux.Run) error {
	if plan == nil {
		return nil
	}
	if plan.Send != nil && answer.Resize == nil && answer.Spec.Resources == nil {
		got, err := s.Lux.Get(ctx, r.LuxRunID)
		if err != nil {
			return err
		}
		answer = got
	}
	actual := plan.Outcome(answer)
	if actual == nil {
		return nil
	}
	names := delivery.LoadPoolNames(ctx, s.Lux)
	return s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		move, err := loadPendingMove(ctx, tx, r.ID)
		if err != nil || move == nil {
			return err
		}
		code, message := move.Code, move.Message
		// A target removed meanwhile ("") left the layer to what the
		// removal moved it to: whatever lux applied is the move's end.
		moved := move.To == "" && code == "" && message == "" || actual.SizeID == move.To
		if moved {
			code, message = "", ""
		} else if code == "" && message == "" {
			// lux kept the Run on another size without refusing the move
			// for good (a resume already under way): the layer stays.
			message = actual.Note
		}
		tag, err := tx.Exec(ctx, `UPDATE machine_moves SET settled_at = now(), refused = $2, refused_message = $3,
			pool = CASE WHEN $4::text IS DISTINCT FROM $5::text THEN jsonb_build_object('from', $4::text, 'to', $5::text) END
			WHERE id = $1 AND settled_at IS NULL`, move.ID, db.Nullable(code), db.Nullable(message), move.From.PoolID, actual.PoolID)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		if moved {
			// machine.raised (or machine.reset) told when the move started;
			// run.resized says what lux applied.
			return nil
		}
		sizes, err := delivery.LoadSizes(ctx, tx)
		if err != nil {
			return err
		}
		asked, ok := sizes.ByID[move.To]
		if !ok {
			asked = delivery.Machine{SizeID: move.To}
		}
		payload := delivery.MovePayload(move.ID, move.Role, move.From.Machine, asked, names)
		names.Name(actual)
		payload["kind"], payload["kept"], payload["code"], payload["message"] = move.Kind, actual, code, message
		if move.Code != "" || move.Message != "" {
			if err := restoreLayer(ctx, tx, r, move); err != nil {
				return err
			}
		}
		_, err = ledger.Append(ctx, tx, r.ref().Event(delivery.EvMachineRefused, ledger.ActorSystem, payload))
		return err
	})
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
