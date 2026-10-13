package delivery

// What an agent and a person are told when lux refuses a machine move
// (docs/design/agent-machine-request.md, "The tool", refusals): lux's code
// put into dude's sentences, naming sizes and pools by name. lux's own
// message names pools by id and is never shown. lux's refusals carry no
// detail fields, so a pool's platforms and capabilities come from lux's
// pool list, and the image's platforms from the Run's placements.

import (
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// MoveRefusal is lux refusing a move to Size, with the facts its sentence
// names.
type MoveRefusal struct {
	Status int
	Code   string
	// The size asked for, its pool's name filled in when lux's list has it.
	Size Machine
	// The pool's platforms and the image's, when lux reports them.
	PoolPlatforms, ImagePlatforms []string
	// The Run needs nested containers and the pool says it has none.
	NoNested bool
	// The move changes pools: an older lux's 400 is then to placement.
	OtherPool bool
	// lux's own words: never shown, read only to tell pool_unfit's
	// nested-containers case when the pool list does not say.
	luxMessage string
}

// RefusalOf gathers what a refusal of a move to size says, from lux's pool
// list (nil when unread) and its record of the Run.
func RefusalOf(le *lux.Error, size Machine, pools []lux.Pool, run lux.Run) MoveRefusal {
	out := MoveRefusal{Status: le.Status, Code: le.Code, Size: size, luxMessage: le.Message}
	names := PoolNames{ByID: map[string]string{}}
	poolID := ""
	if size.PoolID != nil {
		poolID = *size.PoolID
	}
	for _, p := range pools {
		names.ByID[p.ID] = p.Name
		if p.IsDefault {
			names.Default = p.Name
			if poolID == "" {
				poolID = p.ID
			}
		}
	}
	if out.Size.Pool == nil {
		names.Name(&out.Size)
	}
	out.OtherPool = poolID != "" && run.PoolID != "" && poolID != run.PoolID
	if i := slices.IndexFunc(pools, func(p lux.Pool) bool { return p.ID == poolID }); i >= 0 {
		pool := pools[i]
		out.PoolPlatforms = pool.Platforms
		nested := run.Spec.Sandbox != nil && run.Spec.Sandbox.NestedContainers
		out.NoNested = nested && pool.Capabilities != nil && pool.Capabilities.NestedContainers != nil && !*pool.Capabilities.NestedContainers
	}
	for i := len(run.Placements) - 1; i >= 0; i-- {
		if p := run.Placements[i].ImagePlatforms; len(p) > 0 {
			out.ImagePlatforms = p
			break
		}
	}
	return out
}

// ForAgent is what the agent that asked is told: what lux refused, and
// what it may do instead.
func (r MoveRefusal) ForAgent() string {
	said, advice := r.words()
	return said + advice
}

// ForPerson is what lux refused, without the agent's advice: the reason a
// person reads beside the size the Run kept.
func (r MoveRefusal) ForPerson() string {
	said, _ := r.words()
	return strings.TrimSuffix(said, ".")
}

func (r MoveRefusal) words() (said, advice string) {
	size := r.Size.Name
	in := "its pool"
	if r.Size.Pool != nil && *r.Size.Pool != "" {
		in = *r.Size.Pool
	}
	switch {
	case r.Status == http.StatusBadRequest && r.OtherPool:
		return "this lux cannot move a Run between pools yet.", " Ask for a size in your own pool."
	case r.Status == http.StatusBadRequest:
		return "this lux cannot change a stopped Run's CPUs or memory.", " Use fewer workers or less data."
	}
	switch r.Code {
	case lux.CodeUnknownPool:
		return size + "'s pool has no host it fits", "; ask for another size"
	case lux.CodePlatformMismatch:
		if len(r.PoolPlatforms) > 0 && len(r.ImagePlatforms) > 0 {
			return fmt.Sprintf("%s is in %s, whose machines are %s; this agent's image is %s only.", size, in,
					strings.Join(r.PoolPlatforms, ", "), strings.Join(r.ImagePlatforms, ", ")),
				fmt.Sprintf(" Ask for another size, or ask a person to add %s image.", anArch(r.PoolPlatforms[0]))
		}
		return fmt.Sprintf("%s is in %s, whose machines cannot run this agent's image.", size, in),
			" Ask for another size, or ask a person to add an image for them."
	case lux.CodePoolUnfit:
		if r.NoNested || strings.Contains(r.luxMessage, "nested containers") {
			return fmt.Sprintf("%s is in %s, which can't run nested containers, and this agent's image needs them.", size, in),
				" Ask for another size."
		}
		return fmt.Sprintf("%s (%s CPUs · %s GiB) is larger than any machine in %s.", size, number(r.Size.CPUs),
			number(float64(r.Size.MemoryMiB)/1024), in), " Ask for another size."
	case lux.CodeInvalidSpec:
		return fmt.Sprintf("lux cannot give this agent %s as the size is set up.", size),
			fmt.Sprintf(" Ask for another size, or ask a person to check %s in Machines.", size)
	case "not_resumable":
		return fmt.Sprintf("lux was already resuming this Run, so it could not move it to %s.", size), " Ask again once you are running."
	}
	return fmt.Sprintf("lux would not move this agent to %s.", size), " Ask for another size."
}

// anArch is "an arm64 image"'s article and arch, from "linux/arm64".
func anArch(platform string) string {
	arch := platform[strings.LastIndex(platform, "/")+1:]
	if arch != "" && strings.ContainsRune("aeiou", rune(arch[0])) {
		return "an " + arch
	}
	return "a " + arch
}
