package delivery

// Machine sizes: the organization's (machine_sizes, migration 064), named
// by each role's settings (machineSize, over the same layers as its model)
// and by a project's branch previews. What names none, or names a size
// that is gone, runs on the organization's default.

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// Machine is a size as a Run records what it ran on (runs.machine).
type Machine struct {
	SizeID    string  `json:"sizeId"`
	Name      string  `json:"name"`
	CPUs      float64 `json:"cpus"`
	MemoryMiB int64   `json:"memoryMiB"`
	DiskGiB   int64   `json:"diskGiB"`
	// lux's id for its pool; nil: the organization's default pool in lux.
	PoolID *string `json:"poolId"`
	// The pool's name in lux when the Run was submitted; filled in by the
	// submit from lux's list, nil when it names none or lux did not say.
	Pool *string `json:"pool"`
	// Where the size came from: "task" or "session" (a size an agent
	// raised), "project", "organization", "implementer" (a fixer with
	// none of its own), or "default".
	From string `json:"from"`
	// The raise it runs on, copied from the task's or session's layer:
	// the chip's tooltip reads it, so history stays true after a Reset.
	Raised *Raise `json:"raised,omitempty"`
	// Why the Run is not on the size its settings name, said at a resume
	// that could not resize it (phases.PlanResize); "" when it is.
	Note string `json:"note,omitempty"`
	// A smaller disk lux would not apply at a resume; DiskGiB is the disk
	// it kept.
	DiskKept *DiskKept `json:"diskKept,omitempty"`
	// The epoch of the placement this size was given at, by a resume; 0 is
	// the submit's. A memory limit of an earlier placement is not this
	// size's (phases.RecordMemoryLimit).
	SinceEpoch int `json:"sinceEpoch,omitempty"`
	// Not recorded: the size id the settings name that no longer exists,
	// when the default was taken because of it.
	Missing string `json:"-"`
}

// Raise is a size an agent asked for, kept on its task per role
// (tasks.agent_machines) or on its session (sessions.agent_machine).
type Raise struct {
	SizeID string `json:"sizeId"`
	MoveID string `json:"moveId"`
	// The Run that asked, and its settings role.
	AskedBy string    `json:"askedBy"`
	Role    string    `json:"role,omitempty"`
	Reason  string    `json:"reason"`
	At      time.Time `json:"at"`
}

// Raises are a task's raised sizes by settings role; a session's is the
// one under its agent's role.
type Raises map[string]Raise

// LoadTaskRaises reads a task's raised sizes.
func LoadTaskRaises(ctx context.Context, tx pgx.Tx, taskID string) (Raises, error) {
	var out Raises
	if taskID == "" {
		return out, nil
	}
	err := tx.QueryRow(ctx, `SELECT agent_machines FROM tasks WHERE id = $1`, taskID).Scan(&out)
	return out, err
}

// LoadSessionRaise reads a session's raised size, as Raises under role.
func LoadSessionRaise(ctx context.Context, tx pgx.Tx, sessionID, role string) (Raises, error) {
	var r *Raise
	if err := tx.QueryRow(ctx, `SELECT agent_machine FROM sessions WHERE id = $1`, sessionID).Scan(&r); err != nil || r == nil {
		return nil, err
	}
	return Raises{role: *r}, nil
}

// DiskKept is lux refusing to shrink a Run's disk at a resume: the disk
// asked for, and lux's reason.
type DiskKept struct {
	RequestedGiB int64  `json:"requestedGiB"`
	Reason       string `json:"reason,omitempty"`
}

// Machine move events (docs/design/agent-machine-request.md, packages/domain
// events/types.ts). requested and raised are the Run's at the agent's call,
// refused the Run's when lux refused the move, reset the task's or
// session's at a person's Reset.
const (
	EvMachineRequested = "machine.requested"
	EvMachineRaised    = "machine.raised"
	EvMachineRefused   = "machine.refused"
	EvMachineReset     = "machine.reset"
)

// ErrPoolsUnread: lux's pool list could not be read, so a move's pool, and
// whether lux would take it, are unknown; nothing is moved.
var ErrPoolsUnread = errors.New("lux's pools could not be read")

// ErrMoveUnavailable: the Run was paused, stopped or given another control
// since it was read, or another move of it is unsettled.
var ErrMoveUnavailable = errors.New("the Run is no longer available for a machine move")

// MachineMove is a machine_moves row as a raise or a Reset writes it.
type MachineMove struct {
	ID, Org, TaskID, SessionID, RunID string
	// Settings role, and "raise" or "reset".
	Role, Kind, ToSizeID string
	// runs.machine before the move; a raise's carries previousRaise.
	From any
	// The agent's reason, or a Reset's note; ByPerson is a Reset's.
	Reason, ByPerson string
}

// StartMove records a move and, with park, parks its running Run for it
// (dude_pause 'machine'), which only a Run running with no control pending
// takes. A paused Run is not parked: it moves at its next resume.
func StartMove(ctx context.Context, tx pgx.Tx, m MachineMove, park bool) error {
	if park {
		reason := "machine move"
		if m.Kind == "reset" {
			reason = "machine reset"
		}
		tag, err := tx.Exec(ctx, `UPDATE runs SET control = 'pause_graceful', control_requested_at = now(), control_reason = $2, dude_pause = 'machine'
			WHERE id = $1 AND status = 'running' AND control = 'none'`, m.RunID, reason)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrMoveUnavailable
		}
	}
	_, err := tx.Exec(ctx, `INSERT INTO machine_moves (id, organization_id, task_id, session_id, run_id, role, kind, from_size, to_size_id, apply, reason, by_person)
		VALUES ($1, $2, NULLIF($3, ''), NULLIF($4, ''), $5, $6, $7, $8, $9, 'resize', NULLIF($10, ''), NULLIF($11, ''))`,
		m.ID, m.Org, m.TaskID, m.SessionID, m.RunID, m.Role, m.Kind, m.From, m.ToSizeID, m.Reason, m.ByPerson)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.ConstraintName == "machine_moves_one_at_a_time" {
		return ErrMoveUnavailable
	}
	return err
}

// PoolNames are lux's pool names by id, and its default pool's name for a
// size naming no pool.
type PoolNames struct {
	ByID    map[string]string
	Default string
}

// LoadPoolNames reads lux's pool names; lux out of reach is no names, and
// payloads then carry pool ids alone.
func LoadPoolNames(ctx context.Context, c lux.Client) PoolNames {
	_, names := LoadPools(ctx, c)
	return names
}

// LoadPools reads lux's pools once, with their names; lux out of reach is
// neither.
func LoadPools(ctx context.Context, c lux.Client) ([]lux.Pool, PoolNames) {
	var pools []lux.Pool
	if c != nil {
		if read, err := c.Pools(ctx); err == nil {
			pools = read
		}
	}
	return pools, PoolNamesOf(pools)
}

// PoolNamesOf names pools already read; nil names none.
func PoolNamesOf(pools []lux.Pool) PoolNames {
	out := PoolNames{ByID: map[string]string{}}
	for _, p := range pools {
		out.ByID[p.ID] = p.Name
		if p.IsDefault {
			out.Default = p.Name
		}
	}
	return out
}

// Name fills m.Pool from the names when lux named its pool.
func (n PoolNames) Name(m *Machine) {
	if m == nil || m.Pool != nil {
		return
	}
	name := n.Default
	if m.PoolID != nil {
		name = n.ByID[*m.PoolID]
	}
	if name != "" {
		m.Pool = &name
	}
}

// MovePayload is a move event's common payload: whole Machines, so a
// notice and a stepper need no other read, and pool {from, to} (ids and
// names) when the move changes pools.
func MovePayload(moveID, role string, from, to Machine, names PoolNames) map[string]any {
	names.Name(&from)
	names.Name(&to)
	out := map[string]any{"role": role, "from": from, "to": to}
	if moveID != "" {
		out["moveId"] = moveID
	}
	id := func(m Machine) string {
		if m.PoolID == nil {
			return ""
		}
		return *m.PoolID
	}
	if id(from) != id(to) {
		out["pool"] = map[string]any{
			"from": map[string]any{"id": from.PoolID, "name": from.Pool},
			"to":   map[string]any{"id": to.PoolID, "name": to.Pool},
		}
	}
	return out
}

// SameSize: a and b give a Run the same CPUs, memory and disk, whatever
// their names.
func SameSize(a, b Machine) bool {
	return a.CPUs == b.CPUs && a.MemoryMiB == b.MemoryMiB && a.DiskGiB == b.DiskGiB
}

// Sizes are an organization's machine sizes by id, and its default's id.
type Sizes struct {
	ByID    map[string]Machine
	Default string
}

// LoadSizes reads the organization's sizes, in its transaction.
func LoadSizes(ctx context.Context, tx pgx.Tx) (Sizes, error) {
	out := Sizes{ByID: map[string]Machine{}}
	rows, err := tx.Query(ctx, `SELECT id, name, cpus::float8, memory_mib, disk_gib, pool_id, is_default FROM machine_sizes`)
	if err != nil {
		return out, fmt.Errorf("load machine sizes: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var m Machine
		var isDefault bool
		if err := rows.Scan(&m.SizeID, &m.Name, &m.CPUs, &m.MemoryMiB, &m.DiskGiB, &m.PoolID, &isDefault); err != nil {
			return out, err
		}
		out.ByID[m.SizeID] = m
		if isDefault {
			out.Default = m.SizeID
		}
	}
	return out, rows.Err()
}

// ForRole is the size role runs on over the sizes its agents raised in
// the task (or session), a project's agent_models and its organization's:
// the first that names one of the sizes, the fixer then following the
// implementer (modelFallback) over the same layers, else the default.
// So implementer@task reaches the fixer unless the fixer names a size at
// some layer. false when the organization has no size at all.
func (s Sizes) ForRole(role string, raised Raises, project, org json.RawMessage) (Machine, bool) {
	chain := []string{role}
	if f, ok := modelFallback[role]; ok {
		chain = append(chain, f)
	}
	type layer struct {
		name  string
		roles map[string]roleLayer
	}
	layers := []layer{{name: "task"}, {name: "project"}, {name: "organization"}}
	if len(raised) > 0 {
		layers[0].roles = map[string]roleLayer{}
		for r, raise := range raised {
			id := raise.SizeID
			layers[0].roles[r] = roleLayer{MachineSize: &id}
		}
	}
	for i, raw := range []json.RawMessage{project, org} {
		if json.Unmarshal(raw, &layers[i+1].roles) != nil {
			layers[i+1].roles = nil
		}
	}
	missing := ""
	for _, r := range chain {
		for _, l := range layers {
			id := l.roles[r].MachineSize
			if id == nil {
				continue
			}
			if size, ok := s.ByID[*id]; ok {
				size.From = l.name
				if l.name == "task" {
					raise := raised[r]
					size.Raised = &raise
				} else if r != role {
					size.From = r
				}
				return size, true
			}
			missing = cmp.Or(missing, *id)
		}
	}
	size, ok := s.fallback()
	size.Missing = missing
	return size, ok
}

// ForPreview is the size a project's branch previews run on: the one its
// preview settings name, else the default.
func (s Sizes) ForPreview(sizeID string) (Machine, bool) {
	if size, ok := s.ByID[sizeID]; ok {
		size.From = "project"
		return size, true
	}
	size, ok := s.fallback()
	size.Missing = sizeID
	return size, ok
}

func (s Sizes) fallback() (Machine, bool) {
	size, ok := s.ByID[s.Default]
	size.From = "default"
	return size, ok
}
