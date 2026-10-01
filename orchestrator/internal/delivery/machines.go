package delivery

// Machine sizes: the organization's (machine_sizes, migration 063), named
// by each role's settings (machineSize, over the same layers as its model)
// and by a project's branch previews. What names none, or names a size
// that is gone, runs on the organization's default.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
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
	// Where the size came from: "project", "organization", "implementer"
	// (a fixer with none of its own), or "default".
	From string `json:"from"`
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

// ForRole is the size role runs on over a project's agent_models and its
// organization's: the first that names one of the sizes, the fixer then
// following the implementer (modelFallback), else the default. false when
// the organization has no size at all.
func (s Sizes) ForRole(role string, project, org json.RawMessage) (Machine, bool) {
	chain := []string{role}
	if f, ok := modelFallback[role]; ok {
		chain = append(chain, f)
	}
	type layer struct {
		name  string
		roles map[string]roleLayer
	}
	layers := []layer{{name: "project"}, {name: "organization"}}
	for i, raw := range []json.RawMessage{project, org} {
		if json.Unmarshal(raw, &layers[i].roles) != nil {
			layers[i].roles = nil
		}
	}
	for _, r := range chain {
		for _, l := range layers {
			id := l.roles[r].MachineSize
			if id == nil {
				continue
			}
			if size, ok := s.ByID[*id]; ok {
				size.From = l.name
				if r != role {
					size.From = r
				}
				return size, true
			}
		}
	}
	return s.fallback()
}

// ForPreview is the size a project's branch previews run on: the one its
// preview settings name, else the default.
func (s Sizes) ForPreview(sizeID string) (Machine, bool) {
	if size, ok := s.ByID[sizeID]; ok {
		size.From = "project"
		return size, true
	}
	return s.fallback()
}

func (s Sizes) fallback() (Machine, bool) {
	size, ok := s.ByID[s.Default]
	size.From = "default"
	return size, ok
}
