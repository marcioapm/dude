package delivery

import (
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

func choicesFixture() (Sizes, []lux.Pool) {
	high, tiny := "pool_high", "pool_tiny"
	no := false
	sizes := Sizes{Default: "std", ByID: map[string]Machine{
		"small": {SizeID: "small", Name: "Small", CPUs: 0.5, MemoryMiB: 1024, DiskGiB: 10},
		"std":   {SizeID: "std", Name: "Standard", CPUs: 2, MemoryMiB: 8192, DiskGiB: 20},
		"large": {SizeID: "large", Name: "Large", CPUs: 4, MemoryMiB: 16384, DiskGiB: 40},
		"xl":    {SizeID: "xl", Name: "XL", CPUs: 8, MemoryMiB: 32768, DiskGiB: 80, PoolID: &high},
		"arm":   {SizeID: "arm", Name: "Arm", CPUs: 8, MemoryMiB: 24576, DiskGiB: 80, PoolID: &tiny},
		"huge":  {SizeID: "huge", Name: "Huge", CPUs: 64, MemoryMiB: 524288, DiskGiB: 80},
	}}
	pools := []lux.Pool{
		{ID: "pool_gen", Name: "general", IsDefault: true, HostSize: &lux.HostSize{CPUs: 16, Memory: 64 << 30}},
		{ID: high, Name: "highmem", Platforms: []string{"linux/amd64"}},
		{ID: tiny, Name: "armpool", Platforms: []string{"linux/arm64"}, Capabilities: &lux.PoolCapabilities{NestedContainers: &no}},
	}
	return sizes, pools
}

// The list holds only sizes larger than the agent's that lux is not known
// to refuse, smallest first, other pools named.
func TestMachineChoicesFilterAsListMachineSizesDoes(t *testing.T) {
	sizes, pools := choicesFixture()
	run := lux.Run{Placements: []lux.Placement{{ImagePlatforms: []string{"linux/amd64"}}}}
	got := sizes.Choices(sizes.ByID["std"], "Acme", pools, run)
	if got.Largest || got.List() != "Large (4 · 16), XL (8 · 32, highmem pool)" || got.Names() != "Large, XL" {
		t.Fatalf("choices = %q (%q) largest=%v", got.List(), got.Names(), got.Largest)
	}
	// An image whose platforms lux has not reported filters nothing by
	// platform; Huge still fits no host of its pool.
	if got := sizes.Choices(sizes.ByID["std"], "Acme", pools, lux.Run{}); got.Names() != "Large, Arm, XL" {
		t.Fatalf("no image platforms: %q", got.Names())
	}
	// A Run needing nested containers is not offered a pool without them.
	nested := lux.Run{Spec: lux.StoredSpec{Sandbox: &lux.Sandbox{NestedContainers: true}}}
	if got := sizes.Choices(sizes.ByID["std"], "Acme", pools, nested); got.Names() != "Large, XL" {
		t.Fatalf("nested: %q", got.Names())
	}
	// lux's pools unread: nothing is filtered, and no pool is named.
	if got := sizes.Choices(sizes.ByID["std"], "Acme", nil, run); got.List() != "Large (4 · 16), Arm (8 · 24), XL (8 · 32), Huge (64 · 512)" {
		t.Fatalf("no pools: %q", got.List())
	}
}

func TestTheLargestSizeOffersNothing(t *testing.T) {
	sizes, pools := choicesFixture()
	got := sizes.Choices(sizes.ByID["huge"], "Acme", pools, lux.Run{})
	want := "You run on Huge (64 CPUs · 512 GiB · 80 GiB), Acme's largest size: there is none larger to ask for. " +
		"Use fewer workers or less data, and say in your work what did not fit."
	if !got.Largest || len(got.Targets) != 0 || got.Nothing() != want {
		t.Fatalf("largest: %+v %q", got, got.Nothing())
	}
	if line := got.Line(true); line != "## Your machine\n\n"+want {
		t.Fatalf("line: %q", line)
	}
	if offer := got.Offer(); offer != "Move to a larger machine. "+want {
		t.Fatalf("offer: %q", offer)
	}
	// Larger sizes all left out by the filter: not the largest, still none.
	run := lux.Run{Placements: []lux.Placement{{ImagePlatforms: []string{"linux/riscv64"}}}}
	sizes.ByID = map[string]Machine{"std": sizes.ByID["std"], "xl": sizes.ByID["xl"]}
	if got := sizes.Choices(sizes.ByID["std"], "Acme", pools, run); got.Largest || len(got.Targets) != 0 ||
		!strings.Contains(got.Nothing(), "no larger size can take this agent") {
		t.Fatalf("filtered: %+v %q", got, got.Nothing())
	}
}

func TestThePromptLineNamesTheSizesAndTheRaise(t *testing.T) {
	sizes, pools := choicesFixture()
	run := lux.Run{Placements: []lux.Placement{{ImagePlatforms: []string{"linux/amd64"}}}}
	got := sizes.Choices(sizes.ByID["std"], "Acme", pools, run).Line(true)
	want := "## Your machine\n\nYou run on Standard (2 CPUs · 8 GiB · 20 GiB). Larger sizes you may move to: Large (4 · 16), " +
		"XL (8 · 32, highmem pool). If something you need to run is killed for memory (exit 137, \"Killed\", an out-of-memory " +
		"error) or is far too slow for its CPUs, do not call it flaky: run it again with fewer workers if that is enough, and " +
		"otherwise move with `dude machine request SIZE --reason \"…\"`, saying what ran out. A move keeps your conversation " +
		"and checkout; running processes and /tmp are not kept."
	if got != want {
		t.Fatalf("line:\n%s\nwant:\n%s", got, want)
	}
	large := sizes.ByID["large"]
	large.From, large.Raised = "task", &Raise{Role: "implementer", Reason: "jest killed, exit 137"}
	got = sizes.Choices(large, "Acme", pools, run).Line(false)
	if !strings.HasPrefix(got, "## Your machine\n\nYou run on Large (4 CPUs · 16 GiB · 40 GiB), raised for this task after the implementer asked: \"jest killed, exit 137\". Larger sizes you may move to: XL (8 · 32, highmem pool).") ||
		!strings.Contains(got, "call request_machine with the size and your reason") {
		t.Fatalf("raised line: %q", got)
	}
	// An agent already in the highmem pool: its own pool is not named.
	xlPool := "pool_high"
	in := Machine{SizeID: "mid", Name: "Mid", CPUs: 2, MemoryMiB: 16384, PoolID: &xlPool}
	if got := sizes.Choices(in, "Acme", pools, run).List(); got != "Large (4 · 16, general pool), XL (8 · 32)" {
		t.Fatalf("from highmem: %q", got)
	}
}
