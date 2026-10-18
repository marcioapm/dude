package phases

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// The agent goldens for what the syncer tells a Run: they live with the
// prompt goldens, in delivery/agents/testdata, and -update rewrites them
// with this package's own.
func agentGolden(t *testing.T, name string, lines [][2]string) {
	t.Helper()
	var b strings.Builder
	for _, l := range lines {
		b.WriteString("## " + l[0] + "\n\n" + l[1] + "\n\n")
	}
	path := filepath.Join("..", "delivery", "agents", "testdata", name+".golden.md")
	if *update {
		if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s: %v (run with -update to create it)", name, err)
	}
	if b.String() != string(want) {
		t.Errorf("%s differs from %s", name, path)
	}
}

// What the syncer tells an agent of its own Run: a nudge, a move, a
// checkout that failed or was added.
func TestGoldenRunNotices(t *testing.T) {
	large := delivery.Machine{Name: "Large", CPUs: 4, MemoryMiB: 16384, DiskGiB: 40}
	agentGolden(t, "notices-run", [][2]string{
		{"resumed", resumeNudge},
		{"idle", idleNudge},
		{"stuck-turn", stuckTurnNudge},
		{"silent-turn", silentTurnNudge},
		{"host-moved", movedNudge},
		{"moved", movedTold(large, "")},
		{"move-kept", moveKeptTold("XL", "Large", "lux said no", false)},
		{"move-kept-reset", moveKeptTold("XL", "Large", "lux said no", true)},
		{"moved-pool", movedTold(large, " in highmem")},
		{"moved-back", movedBackTold("Standard", "Ana", "")},
		{"moved-back-note", movedBackTold("Standard", "Ana", "the suite fits again")},
		{"linked", linkedTold("GLD", "api", "/workspace/repos/GLD/api")},
		{"clone-failed-task", cloneFailedNote("api", "Cloning…\nfatal: repository 'x' not found", taskCloneRemedy)},
		{"clone-failed-session", cloneFailedNote("GLD/api", "", sessionCloneRemedy)},
		{"clone-failed-long", cloneFailedNote("api", strings.Repeat("e", 250), taskCloneRemedy)},
	})
}

// What a Run is told when a person approves its repositories, and a
// conductor whose checkout lux cannot keep current.
func TestGoldenApprovedAndReadOnly(t *testing.T) {
	agentGolden(t, "notices-approved", [][2]string{
		{"approved", approvedTold([]string{approvedRepoTold("docs", "/workspace/repos/docs", false),
			approvedRepoTold("web", "/workspace/repos/web", true)})},
		{"checkout-read-only", checkoutReadOnlyTold},
	})
}
