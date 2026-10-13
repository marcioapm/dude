package agenttools

// An agent moving itself to a larger machine
// (docs/design/agent-machine-request.md, "The tool").

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/ids"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// Every role with a machine of its own may move; its permission key is
// runs.role (a fixer is an implementer), which PromptRoles covers.
var machineRoles = delivery.PromptRoles

const machineMovesPerRun = 3

type requestMachineIn struct {
	Size   string `json:"size" jsonschema:"one of the sizes listed in this tool's description, by name (Large)"`
	Reason string `json:"reason" jsonschema:"what ran out: the command, its exit or error, the workers or threads it used"`
}

type requestMachineOut struct {
	Size   string `json:"size"`
	Pool   string `json:"pool,omitempty"`
	Answer string `json:"answer"`
}

// machineChoices are the sizes the calling Run could move to, from its
// runs.machine, the organization's sizes, lux's pools (returned too, nil
// when unread) and lux's record of the Run; ok false when the Run records
// no machine.
func machineChoices(ctx context.Context, tx pgx.Tx, c Caller) (delivery.MachineChoices, delivery.Sizes, []lux.Pool, bool, error) {
	var current *delivery.Machine
	var live, org string
	if err := tx.QueryRow(ctx, `SELECT r.machine, COALESCE(r.lux_run_id, ''), o.name FROM runs r JOIN organizations o ON o.id = r.organization_id
		WHERE r.id = $1`, c.RunID).Scan(&current, &live, &org); err != nil {
		return delivery.MachineChoices{}, delivery.Sizes{}, nil, false, err
	}
	sizes, err := delivery.LoadSizes(ctx, tx)
	if err != nil || current == nil {
		return delivery.MachineChoices{}, sizes, nil, false, err
	}
	var pools []lux.Pool
	var run lux.Run
	if c.env.lux != nil {
		// Unread, either filters nothing: lux's dry run is the check.
		pools, _ = c.env.lux.Pools(ctx)
		if live != "" {
			run, _ = c.env.lux.Get(ctx, live)
		}
	}
	return sizes.Choices(*current, org, pools, run), sizes, pools, true, nil
}

// machineOffer is a machine tool's description for the calling Run: the
// sizes it could move to, or that there are none.
func machineOffer(request bool) func(ctx context.Context, tx pgx.Tx, c Caller) (string, error) {
	return func(ctx context.Context, tx pgx.Tx, c Caller) (string, error) {
		choices, _, _, ok, err := machineChoices(ctx, tx, c)
		if err != nil || !ok {
			return "", err
		}
		if request {
			return choices.Offer(), nil
		}
		if len(choices.Targets) == 0 {
			return "The larger machine sizes this Run may move to: none. " + choices.Nothing(), nil
		}
		return "The larger machine sizes this Run may move to, with their pools: " + choices.List() + ".", nil
	}
}

func listMachineSizes(ctx context.Context, tx pgx.Tx, c Caller, _ struct{}) ([]delivery.Machine, error) {
	choices, _, _, ok, err := machineChoices(ctx, tx, c)
	if err != nil || !ok || choices.Targets == nil {
		return []delivery.Machine{}, err
	}
	return choices.Targets, nil
}

func requestMachine(ctx context.Context, tx pgx.Tx, c Caller, in requestMachineIn) (requestMachineOut, error) {
	var out requestMachineOut
	reason := strings.TrimSpace(in.Reason)
	if reason == "" || len([]rune(reason)) > 2000 {
		return out, refuse("say what ran out: the command, its error or exit code (at most 2000 characters)")
	}
	var current *delivery.Machine
	var live, status, control, phase string
	if err := tx.QueryRow(ctx, `SELECT machine, COALESCE(lux_run_id, ''), status::text, control, COALESCE(phase::text, '') FROM runs WHERE id = $1`, c.RunID).Scan(&current, &live, &status, &control, &phase); err != nil {
		return out, err
	}
	// The layer and the move are keyed by settings role: a fix Run's
	// runs.role is implementer, its settings role fixer.
	role := cmp.Or(delivery.PromptRoleFor(phase, c.Role), c.Role)
	var pending, used int
	var pendingTo string
	// A move counts unless lux did not apply it: refused for good (refused),
	// or kept on another size without a refusal (refused_message alone).
	if err := tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE m.settled_at IS NULL),
			count(*) FILTER (WHERE m.kind = 'raise' AND (m.settled_at IS NULL OR (m.refused IS NULL AND m.refused_message IS NULL))),
			COALESCE(max(s.name) FILTER (WHERE m.settled_at IS NULL), '')
		FROM machine_moves m LEFT JOIN machine_sizes s ON s.id = m.to_size_id WHERE m.run_id = $1`, c.RunID).Scan(&pending, &used, &pendingTo); err != nil {
		return out, err
	}
	if pending > 0 {
		return out, refuse("you are being moved to %s now; ask again once you are there, if it is still too small", cmp.Or(pendingTo, "another size"))
	}
	if used >= machineMovesPerRun {
		on := "your machine"
		if current != nil {
			on = current.Name
		}
		return out, refuse("This Run has moved %d times, the limit for one Run; a Reset does not give a move back. "+
			"Carry on on %s, or say in your work what did not fit.", machineMovesPerRun, on)
	}
	if current == nil || live == "" || status != "running" || control != "none" {
		return out, refusal{msg: "this Run is not live and available for a machine move", code: "not_live"}
	}
	choices, sizes, pools, _, err := machineChoices(ctx, tx, c)
	if err != nil {
		return out, err
	}
	if choices.Largest {
		return out, refuse("%s", choices.Nothing())
	}
	found, ok := sizes.Named(in.Size)
	if !ok {
		if len(choices.Targets) == 0 {
			return out, refuse("no size named %s. %s", in.Size, choices.Nothing())
		}
		return out, refuse("no size named %s; you may move to: %s", in.Size, choices.List())
	}
	target := choices.Named(found)
	names := delivery.PoolNamesOf(pools)
	// A refusal once both sizes are known is recorded as machine.requested
	// with refused and no moveId, so who asked for what has one source.
	refusedAs := func(msg, code, recorded string) error {
		payload := delivery.MovePayload("", role, *current, target, names)
		payload["reason"], payload["refused"], payload["message"] = reason, recorded, msg
		ev := c.event(delivery.EvMachineRequested, payload)
		return refusal{msg: msg, code: code, record: &ev}
	}
	if !delivery.Larger(target, *current) {
		msg := fmt.Sprintf("you run on %s (%s); ask for one larger: %s", current.Name, current.Spec(), choices.Names())
		if len(choices.Targets) == 0 {
			msg = choices.Nothing()
		}
		return out, refusedAs(msg, "not_larger", "not_larger")
	}
	if c.env.lux == nil || c.env.validateMachine == nil {
		return out, refuse("machine validation is unavailable")
	}
	before, err := c.env.lux.Get(ctx, live)
	if err != nil {
		return out, err
	}
	if err := c.env.validateMachine(ctx, live, current, &target, before); err != nil {
		if errors.Is(err, delivery.ErrPoolsUnread) {
			return out, refuse("lux's pools could not be read; ask again shortly")
		}
		if le, ok := lux.AsError(err); ok {
			// Unread before, the pools are read once more so the sentence can name them.
			if pools == nil {
				pools, _ = c.env.lux.Pools(ctx)
				names = delivery.PoolNamesOf(pools)
			}
			// lux's code in dude's words; its own message names pools by id.
			return out, refusedAs(delivery.RefusalOf(le, target, pools, before).ForAgent(), le.Code, le.Code)
		}
		return out, err
	}
	id := ids.New(ids.MachineMove)
	raise := delivery.Raise{SizeID: target.SizeID, MoveID: id, AskedBy: c.RunID, Role: role, Reason: reason, At: time.Now().UTC()}
	var previous *delivery.Raise
	if c.SessionID != "" {
		if err := tx.QueryRow(ctx, `SELECT agent_machine FROM sessions WHERE id = $1`, c.SessionID).Scan(&previous); err != nil {
			return out, err
		}
	} else {
		if err := tx.QueryRow(ctx, `SELECT agent_machines->$2 FROM tasks WHERE id = $1`, c.TaskID, role).Scan(&previous); err != nil {
			return out, err
		}
	}
	from := struct {
		delivery.Machine
		PreviousRaise *delivery.Raise `json:"previousRaise"`
	}{*current, previous}
	layer, _ := json.Marshal(raise)
	err = delivery.StartMove(ctx, tx, delivery.MachineMove{ID: id, Org: c.Org, TaskID: c.TaskID, SessionID: c.SessionID, RunID: c.RunID,
		Role: role, Kind: "raise", ToSizeID: target.SizeID, From: from, Reason: reason}, true)
	if errors.Is(err, delivery.ErrMoveUnavailable) {
		return out, refuse("this Run is no longer available for a machine move")
	}
	if err != nil {
		return out, err
	}
	if c.SessionID != "" {
		_, err = tx.Exec(ctx, `UPDATE sessions SET agent_machine = $2 WHERE id = $1`, c.SessionID, layer)
	} else {
		_, err = tx.Exec(ctx, `UPDATE tasks SET agent_machines = jsonb_set(agent_machines, ARRAY[$2], $3::jsonb) WHERE id = $1`, c.TaskID, role, layer)
	}
	if err != nil {
		return out, err
	}
	payload := delivery.MovePayload(id, role, *current, target, names)
	payload["reason"] = reason
	if _, err := ledger.Append(ctx, tx, c.event(delivery.EvMachineRequested, payload)); err != nil {
		return out, err
	}
	// The layer is written and the move starts: the notice and the
	// stepper's pool come from this, whole Machines and pool names.
	raised := delivery.MovePayload(id, role, *current, target, names)
	if _, err := ledger.Append(ctx, tx, c.event(delivery.EvMachineRaised, raised)); err != nil {
		return out, err
	}
	return requestMachineOut{Size: target.Name, Pool: choices.OtherPool(target), Answer: movedAnswer(c.SessionID != "", role, choices, target)}, nil
}

// movedAnswer is the design's answer to an accepted request; role is the
// caller's settings role.
func movedAnswer(session bool, role string, choices delivery.MachineChoices, target delivery.Machine) string {
	where := ""
	if pool := choices.OtherPool(target); pool != "" {
		where = " in the " + pool + " pool"
	}
	later := "Later " + strings.ToLower(delivery.RoleLabel[role]) + " Runs in this task run on " + target.Name + " too."
	switch {
	case session:
		later = "This session's agent runs on " + target.Name + " from now on."
	case role == "implementer":
		later = "Later implementer and fixer Runs in this task run on " + target.Name + " too."
	case role == delivery.RoleConductor:
		later = "Later conductors in this task run on " + target.Name + " too."
	case delivery.RoleLabel[role] == "":
		later = "Later " + role + " Runs in this task run on " + target.Name + " too."
	}
	return fmt.Sprintf("Moved: you run on %s (%s)%s from your next step. Your conversation and your checkout are kept, "+
		"committed or not, with everything else on /workspace and your home. Running processes and /tmp are not: start "+
		"your servers and watchers again. %s End your turn now; you are resumed on %s.",
		target.Name, target.Spec(), where, later, target.Name)
}
