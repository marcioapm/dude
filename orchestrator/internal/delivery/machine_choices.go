package delivery

// What an agent is told about moving to a larger machine
// (docs/design/agent-machine-request.md, "The prompt line"): the sizes it
// could move to, filtered as list_machine_sizes filters them, in the tool's
// description, the prompt and the refusals.

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// Larger: strictly more memory, or the same memory and more CPUs.
func Larger(target, current Machine) bool {
	return target.MemoryMiB > current.MemoryMiB || target.MemoryMiB == current.MemoryMiB && target.CPUs > current.CPUs
}

// MachineChoices are the sizes an agent on Current could move to.
type MachineChoices struct {
	Current Machine
	// Larger sizes lux is not already known to refuse, smallest first,
	// pool names filled in.
	Targets []Machine
	// No size of the organization is larger than Current.
	Largest bool
	// The organization's name, for the largest-size sentence.
	Org string
	// The pool ids Current runs in and lux's default ("" when lux's pool
	// list was not read).
	currentPool, defaultPool string
	// lux's pool names, read with the list.
	names PoolNames
}

// Named is m with its pool's name filled in from lux's list.
func (c MachineChoices) Named(m Machine) Machine {
	m.Pool = nil
	c.names.Name(&m)
	return m
}

// Choices filters the organization's sizes for an agent on current: larger
// ones whose pool lux lists as able to take the Run (fits a host, offers
// nested containers when the Run needs them, runs one of the image's
// platforms). A pool or image lux says nothing about filters nothing: the
// dry run is the check, this list a courtesy.
func (s Sizes) Choices(current Machine, org string, pools []lux.Pool, run lux.Run) MachineChoices {
	out := MachineChoices{Current: current, Org: org, Largest: true}
	defaultPool := ""
	for _, p := range pools {
		if p.IsDefault {
			defaultPool = p.ID
		}
	}
	poolID := func(m Machine) string {
		if m.PoolID != nil && *m.PoolID != "" {
			return *m.PoolID
		}
		return defaultPool
	}
	out.currentPool, out.defaultPool = poolID(current), defaultPool
	if run.PoolID != "" {
		out.currentPool = run.PoolID
	}
	names := PoolNames{ByID: map[string]string{}}
	for _, p := range pools {
		names.ByID[p.ID] = p.Name
		if p.IsDefault {
			names.Default = p.Name
		}
	}
	out.names = names
	for _, size := range s.ByID {
		if !Larger(size, current) {
			continue
		}
		out.Largest = false
		if !fitsPool(size, poolID(size), pools, run) {
			continue
		}
		size.Pool = nil
		names.Name(&size)
		out.Targets = append(out.Targets, size)
	}
	slices.SortFunc(out.Targets, func(a, b Machine) int {
		return cmp.Or(cmp.Compare(a.MemoryMiB, b.MemoryMiB), cmp.Compare(a.CPUs, b.CPUs), strings.Compare(a.Name, b.Name))
	})
	return out
}

func fitsPool(size Machine, poolID string, pools []lux.Pool, run lux.Run) bool {
	i := slices.IndexFunc(pools, func(p lux.Pool) bool { return p.ID == poolID })
	if i < 0 {
		return true
	}
	pool := pools[i]
	if h := pool.HostSize; h != nil && (size.CPUs > h.CPUs || size.MemoryMiB<<20 > h.Memory || h.Disk > 0 && size.DiskGiB<<30 > h.Disk) {
		return false
	}
	nested := run.Spec.Sandbox != nil && run.Spec.Sandbox.NestedContainers
	if c := pool.Capabilities; nested && c != nil && c.NestedContainers != nil && !*c.NestedContainers {
		return false
	}
	// The latest placement that reported its image's platforms (lux#91).
	for i := len(run.Placements) - 1; i >= 0; i-- {
		image := run.Placements[i].ImagePlatforms
		if len(image) == 0 {
			continue
		}
		return len(pool.Platforms) == 0 || slices.ContainsFunc(image, func(p string) bool { return slices.Contains(pool.Platforms, p) })
	}
	return true
}

// Named finds a size by name, case-insensitively.
func (s Sizes) Named(name string) (Machine, bool) {
	for _, size := range s.ByID {
		if strings.EqualFold(size.Name, strings.TrimSpace(name)) {
			return size, true
		}
	}
	return Machine{}, false
}

func number(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

// Spec is a size's machine in words: "4 CPUs · 16 GiB · 40 GiB".
func (m Machine) Spec() string {
	cpus := number(m.CPUs) + " CPUs"
	if m.CPUs == 1 {
		cpus = "1 CPU"
	}
	return fmt.Sprintf("%s · %s GiB · %d GiB", cpus, number(float64(m.MemoryMiB)/1024), m.DiskGiB)
}

// short is a target as the list names it: "Large (4 · 16)", with its pool
// when it is not the agent's: "XL (8 · 32, highmem pool)".
func (c MachineChoices) short(m Machine) string {
	s := fmt.Sprintf("%s (%s · %s", m.Name, number(m.CPUs), number(float64(m.MemoryMiB)/1024))
	if pool := c.OtherPool(m); pool != "" {
		s += ", " + pool + " pool"
	}
	return s + ")"
}

// OtherPool is m's pool name when m (its Pool filled in) is in another
// pool than the agent's; "" when it is the same, or unnamed.
func (c MachineChoices) OtherPool(m Machine) string {
	id := c.defaultPool
	if m.PoolID != nil && *m.PoolID != "" {
		id = *m.PoolID
	}
	if m.Pool == nil || id == "" || id == c.currentPool {
		return ""
	}
	return *m.Pool
}

// List is the targets as the agent is offered them: "Large (4 · 16), XL
// (8 · 32, highmem pool)".
func (c MachineChoices) List() string {
	out := make([]string, len(c.Targets))
	for i, m := range c.Targets {
		out[i] = c.short(m)
	}
	return strings.Join(out, ", ")
}

// Names are the targets' names: "Large, XL".
func (c MachineChoices) Names() string {
	out := make([]string, len(c.Targets))
	for i, m := range c.Targets {
		out[i] = m.Name
	}
	return strings.Join(out, ", ")
}

// Nothing is what an agent with no size to move to is told: at the
// organization's largest, or every larger size left out.
func (c MachineChoices) Nothing() string {
	if c.Largest {
		org := c.Org + "'s"
		if c.Org == "" {
			org = "the organization's"
		}
		return fmt.Sprintf("You run on %s (%s), %s largest size: there is none larger to ask for. "+
			"Use fewer workers or less data, and say in your work what did not fit.", c.Current.Name, c.Current.Spec(), org)
	}
	return fmt.Sprintf("You run on %s (%s), and no larger size can take this agent: their pools lack its platform, "+
		"nested containers or a host they fit. Use fewer workers or less data, and say in your work what did not fit.",
		c.Current.Name, c.Current.Spec())
}

const machineKept = "A move keeps your conversation and checkout; running processes and /tmp are not kept."

// Offer is the request tool's description: the sizes and what a move
// keeps, so a harness that drops the prompt's tail still has them.
func (c MachineChoices) Offer() string {
	if len(c.Targets) == 0 {
		return "Move to a larger machine. " + c.Nothing()
	}
	return fmt.Sprintf("Move to a larger machine, giving the size by name and a reason naming the command, its exit "+
		"or error, and the workers or threads it used. You run on %s (%s). Larger sizes you may move to: %s. "+
		"A successful request ends your turn: you are resumed on the new size. %s",
		c.Current.Name, c.Current.Spec(), c.List(), machineKept)
}

// Line is the prompt's paragraph for an agent with a machine: where it
// runs (and why, when an agent raised it), what it may move to, and when.
func (c MachineChoices) Line(cli bool) string {
	if len(c.Targets) == 0 {
		return "## Your machine\n\n" + c.Nothing()
	}
	first := fmt.Sprintf("You run on %s (%s).", c.Current.Name, c.Current.Spec())
	if r := c.Current.Raised; r != nil {
		scope, who := "task", "an agent"
		if c.Current.From == "session" {
			scope = "session"
		}
		if label := RoleLabel[r.Role]; label != "" {
			who = "the " + strings.ToLower(label)
		}
		first = fmt.Sprintf("You run on %s (%s), raised for this %s after %s asked: %q.", c.Current.Name, c.Current.Spec(), scope, who, r.Reason)
	}
	how := "call request_machine with the size and your reason"
	if cli {
		how = "move with `dude machine request SIZE --reason \"…\"`"
	}
	return fmt.Sprintf("## Your machine\n\n%s Larger sizes you may move to: %s. If something you need to run is killed "+
		"for memory (exit 137, \"Killed\", an out-of-memory error) or is far too slow for its CPUs, do not call it "+
		"flaky: run it again with fewer workers if that is enough, and otherwise %s, saying what ran out. %s",
		first, c.List(), how, machineKept)
}
