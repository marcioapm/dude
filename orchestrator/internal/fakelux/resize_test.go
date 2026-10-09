package fakelux

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// submitSized submits a generic Run of 2 CPUs, 8 GiB and 20 GiB of disk and
// stops it once it runs.
func submitSized(t *testing.T, fake *Server, key string) (*lux.HTTPClient, string) {
	t.Helper()
	srv := httptest.NewServer(fake.Handler())
	t.Cleanup(srv.Close)
	client := lux.New(srv.URL, "k")
	run, err := client.Submit(context.Background(), lux.Spec{Image: lux.Image{Ref: "agent:1"},
		Workload:  lux.Workload{Adapter: "generic", Command: []string{"true"}},
		Resources: &lux.Resources{CPUs: 2, Memory: 8 * gib, Disk: 20 * gib}}, key)
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, client, run.ID, "running")
	if err := client.Stop(context.Background(), run.ID); err != nil {
		t.Fatal(err)
	}
	waitState(t, client, run.ID, "stopped")
	return client, run.ID
}

// As lux#51: a stopped Run's resume can change its cpus, memory and disk.
// What applies is written into its spec, and the answer's resize says what
// was asked and what the Run has; its next placement gets the new memory.
func TestAResumeResizesAStoppedRun(t *testing.T) {
	fake := New("", "k", func(map[string]any) Behaviour { return Behaviour{Hang: true} })
	fake.MemoryShare = 0.5
	client, id := submitSized(t, fake, "grow")
	ctx := context.Background()
	want := lux.Resources{CPUs: 0.5, Memory: 1 * gib, Disk: 40 * gib}
	res, err := client.Resume(ctx, id, lux.ResumeInput{Resources: &want})
	if err != nil {
		t.Fatal(err)
	}
	if res.Resize == nil || res.Resize.Requested != want || res.Resize.Applied != want || res.Resize.Disk != nil {
		t.Errorf("resize = %+v, want %+v asked and applied", res.Resize, want)
	}
	waitState(t, client, id, "running")
	got, _ := client.Get(ctx, id)
	if got.Spec.Resources == nil || *got.Spec.Resources != want {
		t.Errorf("stored spec resources = %+v, want %+v", got.Spec.Resources, want)
	}
	if got.Resize != nil {
		t.Errorf("GET carries a resize: %+v", got.Resize)
	}
	if l := got.Placements; len(l) != 2 || *l[0].MemoryLimit != 4*gib || *l[1].MemoryLimit != gib/2 {
		t.Errorf("placements' memory limits: %+v, want 4 GiB then 512 MiB", l)
	}
}

// A smaller disk applies only down to the measured peak of the snapshot's
// placement plus max(25%, 1 GiB); below, the Run keeps its disk, cpus and
// memory still apply, and the answer says why. A lost placement has no
// final measurement, so nothing shrinks it.
func TestAResumeKeepsADiskTooSmallForItsState(t *testing.T) {
	fake := New("", "k", func(map[string]any) Behaviour { return Behaviour{Hang: true} })
	fake.DiskUse = 8 * gib
	client, id := submitSized(t, fake, "shrink")
	ctx := context.Background()
	res, err := client.Resume(ctx, id, lux.ResumeInput{Resources: &lux.Resources{CPUs: 1, Memory: 2 * gib, Disk: 5 * gib}})
	if err != nil {
		t.Fatal(err)
	}
	kept := res.Resize.Disk
	if res.Resize.Applied != (lux.Resources{CPUs: 1, Memory: 2 * gib, Disk: 20 * gib}) || kept == nil || kept.Kept != 20*gib ||
		kept.Requested != 5*gib || kept.Measured == nil || *kept.Measured != 8*gib || kept.Needed != 10*gib || kept.Reason == "" {
		t.Errorf("resize = %+v, disk %+v; want 20 GiB kept, 10 GiB needed", res.Resize, kept)
	}
	waitState(t, client, id, "running")
	if got, _ := client.Get(ctx, id); *got.Spec.Resources != res.Resize.Applied {
		t.Errorf("spec = %+v, want what was applied", got.Spec.Resources)
	}

	// Enough room for its state: applied.
	if err := client.Stop(ctx, id); err != nil {
		t.Fatal(err)
	}
	waitState(t, client, id, "stopped")
	res, err = client.Resume(ctx, id, lux.ResumeInput{Resources: &lux.Resources{Disk: 10 * gib}})
	if err != nil || res.Resize.Disk != nil || res.Resize.Applied.Disk != 10*gib || res.Resize.Requested != (lux.Resources{Disk: 10 * gib}) {
		t.Errorf("a shrink that fits: %+v (%v)", res.Resize, err)
	}

	waitState(t, client, id, "running")
	fake.Lose(id)
	res, err = client.Resume(ctx, id, lux.ResumeInput{Resources: &lux.Resources{Disk: 9 * gib}})
	if err != nil || res.Resize.Disk == nil || res.Resize.Disk.Kept != 10*gib || res.Resize.Disk.Measured != nil {
		t.Errorf("after a lost placement: %+v (%v), want the disk kept without a measurement", res.Resize, err)
	}
}

// A resume of a Run already resuming is compared with what that resume
// asked for: the same resources get its resize, none get none, others
// 409 not_resumable; and invalid values are 422 invalid_spec either way.
func TestAResumeWhileResumingComparesResources(t *testing.T) {
	fake := New("", "k", func(map[string]any) Behaviour { return Behaviour{Hang: true} })
	holds := holdStarts(t, fake, "2/"+holdAssign, "3/"+holdAssign)
	client, id := submitSized(t, fake, "retry")
	ctx := context.Background()
	asked := lux.Resources{CPUs: 4, Memory: 16 * gib}
	if _, err := client.Resume(ctx, id, lux.ResumeInput{Resources: &asked}); err != nil {
		t.Fatal(err)
	}
	holds.reached(2, holdAssign)
	same, err := client.Resume(ctx, id, lux.ResumeInput{Resources: &asked})
	if err != nil || same.Resize == nil || same.Resize.Requested != asked {
		t.Errorf("the same resources again: %+v (%v), want the first resume's resize", same.Resize, err)
	}
	if none, err := client.Resume(ctx, id, lux.ResumeInput{}); err != nil || none.Resize != nil {
		t.Errorf("no resources: %+v (%v), want 202 without a resize", none.Resize, err)
	}
	_, err = client.Resume(ctx, id, lux.ResumeInput{Resources: &lux.Resources{CPUs: 1}})
	if le, ok := lux.AsError(err); !ok || le.Status != http.StatusConflict || le.Code != "not_resumable" {
		t.Errorf("other resources: %v, want 409 not_resumable", err)
	}
	_, err = client.Resume(ctx, id, lux.ResumeInput{Resources: &lux.Resources{Disk: -1}})
	if le, ok := lux.AsError(err); !ok || le.Status != http.StatusUnprocessableEntity || le.Code != "invalid_spec" {
		t.Errorf("a negative disk: %v, want 422 invalid_spec", err)
	}
	holds.let(2, holdAssign)
	waitState(t, client, id, "running")

	// A resume that asked for none: any resources are refused while it waits.
	if err := client.Stop(ctx, id); err != nil {
		t.Fatal(err)
	}
	waitState(t, client, id, "stopped")
	if _, err := client.Resume(ctx, id, lux.ResumeInput{}); err != nil {
		t.Fatal(err)
	}
	holds.reached(3, holdAssign)
	_, err = client.Resume(ctx, id, lux.ResumeInput{Resources: &asked})
	if le, ok := lux.AsError(err); !ok || le.Status != http.StatusConflict {
		t.Errorf("resources after a resume with none: %v, want 409", err)
	}
	holds.let(3, holdAssign)
}

// A lux from before lux#51 takes only a disk on resume, larger or smaller,
// and refuses cpus or memory as an unknown field.
func TestALuxFromBeforeResizingTakesOnlyADisk(t *testing.T) {
	fake := New("", "k", func(map[string]any) Behaviour { return Behaviour{Hang: true} })
	fake.NoResize = true
	client, id := submitSized(t, fake, "old")
	ctx := context.Background()
	_, err := client.Resume(ctx, id, lux.ResumeInput{Resources: &lux.Resources{CPUs: 1, Memory: gib, Disk: 5 * gib}})
	if le, ok := lux.AsError(err); !ok || le.Status != http.StatusBadRequest {
		t.Errorf("cpus to an old lux: %v, want 400", err)
	}
	res, err := client.Resume(ctx, id, lux.ResumeInput{Resources: &lux.Resources{Disk: 5 * gib}})
	if err != nil || res.Resize != nil || res.Spec.Resources.Disk != 5*gib || res.Spec.Resources.CPUs != 2 {
		t.Errorf("a disk to an old lux: resize %+v spec %+v (%v)", res.Resize, res.Spec.Resources, err)
	}
}
