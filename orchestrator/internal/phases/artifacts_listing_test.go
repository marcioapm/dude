package phases

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// listedArtifact is one entry of the control plane's GET /v1/artifacts.
type listedArtifact struct {
	Name, Description string
	Version, Versions int
}

// listing is GET /v1/artifacts?taskId= for the world's task, answered by the
// control plane's own route over this test's database (testdata/list-artifacts.ts).
func (w *receiptWorld) listing() []listedArtifact {
	w.t.Helper()
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(file), "../../..")
	c := w.owner.Config()
	cmd := exec.Command("bun", "run", "orchestrator/internal/phases/testdata/list-artifacts.ts", w.org, w.tr.run.TaskID)
	cmd.Dir = root
	cmd.Env = append(cmd.Environ(), fmt.Sprintf("DATABASE_URL=postgres://dude_app:dude_app@%s:%d/%s", c.Host, c.Port, c.Database))
	out, err := cmd.Output()
	if err != nil {
		w.t.Fatalf("GET /v1/artifacts: %v\n%s", err, out)
	}
	var body struct{ Artifacts []listedArtifact }
	if err := json.Unmarshal(out, &body); err != nil {
		w.t.Fatalf("GET /v1/artifacts: %v\n%s", err, out)
	}
	return body.Artifacts
}

// Versions of one name the sweep records together, in one epoch, are listed
// in lux's order: the last lux lists is the latest, with its description.
// Many names, so that some pairs get artifact ids in the same millisecond,
// where only the recorded time can order them.
func TestVersionsSweptTogetherAreListedInLuxsOrder(t *testing.T) {
	w := newReceiptWorld(t)
	ctx := context.Background()
	if _, err := w.owner.Exec(ctx, `UPDATE runs SET lux_run_id = 'lrun_1', status = 'completed',
		artifacts_due_at = now(), artifacts_next_at = now() WHERE id = $1`, w.tr.run.ID); err != nil {
		t.Fatal(err)
	}
	const names = 40
	fake := &sweepLux{}
	for i := range names {
		path := fmt.Sprintf("%snote-%02d.md", lux.PublishedPrefix, i)
		for v := 1; v <= 2; v++ {
			fake.listed = append(fake.listed, lux.Artifact{ID: fmt.Sprintf("art_%02dv%d", i, v), Epoch: 2, Path: path, Version: v,
				Description: fmt.Sprintf("draft %d", v), Size: 1, SHA256: "ab", Available: true})
		}
	}
	if n, err := (&Artifacts{DB: w.s.DB, Lux: fake}).Sweep(ctx); err != nil || n != 1 {
		t.Fatalf("swept %d, %v", n, err)
	}

	listed := w.listing()
	if len(listed) != 2*names {
		t.Fatalf("listed %d, want %d", len(listed), 2*names)
	}
	for _, a := range listed {
		if want := fmt.Sprintf("draft %d", a.Version); a.Versions != 2 || a.Description != want {
			t.Errorf("%s version %d of %d says %q; want %q", a.Name, a.Version, a.Versions, a.Description, want)
		}
	}
}
