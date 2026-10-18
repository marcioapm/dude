package delivery

// What an agent and a person are told when lux refuses a machine move
// (docs/design/agent-machine-request.md, "The tool", refusals): lux's code
// put into dude's sentences, naming sizes and pools by name. lux's own
// message names pools by id and is never shown. lux's refusals carry no
// detail fields, so a pool's platforms and capabilities come from lux's
// pool list, and the image's platforms from the Run's placements.

import (
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
	poolID := ""
	if size.PoolID != nil {
		poolID = *size.PoolID
	}
	if i := slices.IndexFunc(pools, func(p lux.Pool) bool { return p.IsDefault }); poolID == "" && i >= 0 {
		poolID = pools[i].ID
	}
	PoolNamesOf(pools).Name(&out.Size)
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
	pool := ""
	if r.Size.Pool != nil {
		pool = *r.Size.Pool
	}
	arch := ""
	if len(r.PoolPlatforms) > 0 {
		arch = r.PoolPlatforms[0][strings.LastIndex(r.PoolPlatforms[0], "/")+1:]
	}
	data := struct {
		Status                        int
		Code, Size, Pool, CPUs        string
		Memory, Arch                  string
		PoolPlatforms, ImagePlatforms []string
		OtherPool, Nested, Vowel      bool
	}{r.Status, r.Code, r.Size.Name, pool, number(r.Size.CPUs), number(float64(r.Size.MemoryMiB) / 1024), arch,
		r.PoolPlatforms, r.ImagePlatforms, r.OtherPool, r.NoNested || strings.Contains(r.luxMessage, "nested containers"),
		arch != "" && strings.ContainsRune("aeiou", rune(arch[0]))}
	return render(moveRefusedSaidTmpl, data), render(moveRefusedAdviceTmpl, data)
}
