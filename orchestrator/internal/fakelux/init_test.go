package fakelux

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// As lux's shim: init runs before the workload on every placement, so a
// resumed Run runs it again, in the same /workspace, from the same spec.
func TestInitRunsOnEveryPlacementFromTheSameSpec(t *testing.T) {
	fake := New("", "k", func(map[string]any) Behaviour { return Behaviour{Hang: true} })
	t.Cleanup(fake.Close)
	srv := httptest.NewServer(fake.Handler())
	t.Cleanup(srv.Close)
	client := lux.New(srv.URL, "k")
	ctx := context.Background()
	// The here-document's /workspace is data: only the commands' is the
	// Run's directory.
	script := "set -eu\necho placed >> /workspace/placements\ncat > /workspace/f.tmp <<'X'\nkept /workspace $HOME `x`\nX\nmv /workspace/f.tmp /workspace/f\n"
	run, err := client.Submit(ctx, lux.Spec{Image: lux.Image{Ref: "agent:1"},
		Workload: lux.Workload{Adapter: "generic", Command: []string{"true"}},
		Init:     &lux.Init{Script: script}}, "init")
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, client, run.ID, "running")
	dir := fake.Workspace(run.ID)
	first, err := os.ReadFile(filepath.Join(dir, "f"))
	if err != nil || string(first) != "kept /workspace $HOME `x`\n" {
		t.Fatalf("first placement wrote %q: %v", first, err)
	}
	if err := client.Stop(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	waitState(t, client, run.ID, "stopped")
	if _, err := client.Resume(ctx, run.ID, lux.ResumeInput{}); err != nil {
		t.Fatal(err)
	}
	waitState(t, client, run.ID, "running")
	second, err := os.ReadFile(filepath.Join(dir, "f"))
	if err != nil || string(second) != string(first) {
		t.Fatalf("second placement wrote %q, first %q: %v", second, first, err)
	}
	placements, _ := os.ReadFile(filepath.Join(dir, "placements"))
	if string(placements) != "placed\nplaced\n" || fake.Workspace(run.ID) != dir {
		t.Errorf("init ran %q in %s, first in %s", placements, fake.Workspace(run.ID), dir)
	}
	fake.mu.Lock()
	inits := fake.runs[run.ID].inits
	fake.mu.Unlock()
	if inits != 2 {
		t.Errorf("init ran %d times over two placements", inits)
	}
}

func TestWorkloadReadsInitMarkerOnEveryPlacement(t *testing.T) {
	fake := New("", "k", func(map[string]any) Behaviour {
		return Behaviour{Hang: true, Command: []string{"sh", "-eu", "-c", "cat /workspace/marker; rm /workspace/marker"}}
	})
	t.Cleanup(fake.Close)
	srv := httptest.NewServer(fake.Handler())
	t.Cleanup(srv.Close)
	client := lux.New(srv.URL, "k")
	ctx := context.Background()
	run, err := client.Submit(ctx, lux.Spec{Image: lux.Image{Ref: "agent:1"},
		Workload: lux.Workload{Adapter: "acp"},
		Init:     &lux.Init{Script: "cat > /workspace/marker <<'MARKER'\ninit bytes: $HOME /workspace `x`\nMARKER\n"}}, "init-observed")
	if err != nil {
		t.Fatal(err)
	}
	cursor := ""
	for epoch := 1; epoch <= 2; epoch++ {
		if epoch == 2 {
			if err := client.Stop(ctx, run.ID); err != nil {
				t.Fatal(err)
			}
			waitState(t, client, run.ID, "stopped")
			if _, err := client.Resume(ctx, run.ID, lux.ResumeInput{}); err != nil {
				t.Fatal(err)
			}
		}
		readCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		observed := ""
		stop := errors.New("observed workload output")
		err := client.Output(readCtx, run.ID, cursor, 0, func(f lux.Frame) error {
			if f.Event == nil || f.Event.Type != "acp.agent_message_chunk" || f.Epoch != epoch {
				return nil
			}
			var update struct {
				Content struct{ Text string } `json:"content"`
			}
			if err := json.Unmarshal(f.Event.Data, &update); err != nil {
				return err
			}
			observed = update.Content.Text
			cursor = f.Cursor
			return stop
		})
		cancel()
		if !errors.Is(err, stop) || observed != "init bytes: $HOME /workspace `x`\n" {
			t.Fatalf("placement %d workload observed %q: %v", epoch, observed, err)
		}
		got, err := client.Get(ctx, run.ID)
		if err != nil || got.State != "running" {
			t.Fatalf("placement %d run = %+v: %v", epoch, got, err)
		}
	}
}

func TestBehaviourCommandCancellation(t *testing.T) {
	for _, action := range []string{"stop", "terminate", "cancel", "legacy-cancel", "close"} {
		t.Run(action, func(t *testing.T) {
			fake := New("", "k", func(map[string]any) Behaviour {
				return Behaviour{Hang: true, Command: []string{"sh", "-eu", "-c",
					"sleep 60 & child=$!; echo buffered-before-exit; echo $$ $child > /workspace/ready.tmp; mv /workspace/ready.tmp /workspace/ready; wait $child"}}
			})
			fake.CancelledState = action == "legacy-cancel"
			t.Cleanup(fake.Close)
			releaseResult := make(chan struct{})
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(releaseResult) }) }
			t.Cleanup(release)
			fake.beforeCommandResult = func(epoch int) {
				if action == "stop" && epoch == 1 {
					<-releaseResult
				}
			}
			finished := make(chan int, 2)
			fake.AfterPlacement(func(_ string, epoch int) { finished <- epoch })
			srv := httptest.NewServer(fake.Handler())
			t.Cleanup(srv.Close)
			client := lux.New(srv.URL, "k")
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			run, err := client.Submit(ctx, lux.Spec{Image: lux.Image{Ref: "agent:1"},
				Workload: lux.Workload{Adapter: "acp"}}, "command-cancel")
			if err != nil {
				t.Fatal(err)
			}
			pids := waitCommandReady(t, fake, run.ID)
			if err := client.Input(ctx, run.ID, lux.InputRequest{RequestID: "queued", Text: "wait for command"}); err != nil {
				t.Fatal(err)
			}
			fake.mu.Lock()
			turns := fake.runs[run.ID].turnsEnded
			records := len(fake.runs[run.ID].records)
			fake.mu.Unlock()
			if turns != 0 || records != 0 {
				t.Fatal("input started the agent before command completion")
			}
			fake.mu.Lock()
			command := fake.runs[run.ID].command
			fake.mu.Unlock()
			if command == nil {
				t.Fatal("ready command is not registered")
			}
			requestCtx, requestCancel := context.WithTimeout(ctx, 3*time.Second)
			defer requestCancel()
			done := make(chan error, 1)
			go func() {
				if action == "close" {
					fake.Close()
					done <- nil
					return
				}
				endpoint := action
				if endpoint == "legacy-cancel" {
					endpoint = "cancel"
				}
				req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, srv.URL+"/v1/runs/"+run.ID+"/"+endpoint, nil)
				if err == nil {
					req.Header.Set("Authorization", "Bearer k")
					var resp *http.Response
					resp, err = http.DefaultClient.Do(req)
					if err == nil {
						resp.Body.Close()
						if resp.StatusCode != http.StatusAccepted {
							err = errors.New(resp.Status)
						}
					}
				}
				done <- err
			}()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-requestCtx.Done():
				t.Fatal("lifecycle request waited for the workload command")
			}
			if action == "stop" {
				waitState(t, client, run.ID, "stopped")
				// Replace the placement before waiting for its completion callback.
				if err := os.Remove(filepath.Join(fake.Workspace(run.ID), "ready")); err != nil {
					t.Fatal(err)
				}
				if _, err := client.Resume(ctx, run.ID, lux.ResumeInput{}); err != nil {
					t.Fatal(err)
				}
				second := waitCommandReady(t, fake, run.ID)
				if second[0] == pids[0] {
					t.Fatal("resume did not start a new command")
				}
				release()
			}
			select {
			case <-command.done:
			case <-ctx.Done():
				t.Fatal("command was not reaped")
			}
			select {
			case epoch := <-finished:
				if epoch != 1 {
					t.Fatalf("completed epoch %d, want 1", epoch)
				}
			case <-ctx.Done():
				t.Fatal("old placement did not finish")
			}
			for _, pid := range pids {
				waitCommandGone(t, pid)
			}
			got, err := client.Get(ctx, run.ID)
			want := "terminated"
			switch action {
			case "stop", "close":
				want = "running"
			case "legacy-cancel":
				want = "cancelled"
			}
			if err != nil || got.State != want || got.StateReason == "workload-failed" {
				t.Fatalf("after old command completion: %+v, %v; want %s", got, err, want)
			}
			if records := fake.Records(run.ID); len(records) != 0 {
				t.Fatalf("cancelled command published buffered output or continued the agent: %+v", records)
			}
			if action == "stop" {
				fake.Close()
				for _, pid := range waitReadyPIDs(t, fake.Workspace(run.ID)) {
					waitCommandGone(t, pid)
				}
			}
		})
	}
}

func waitCommandReady(t *testing.T, fake *Server, id string) []int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if dir := fake.Workspace(id); dir != "" {
			if _, err := os.Stat(filepath.Join(dir, "ready")); err == nil {
				return waitReadyPIDs(t, dir)
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("command did not write its readiness marker")
	return nil
}

func waitReadyPIDs(t *testing.T, dir string) []int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "ready"))
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(string(data))
	if len(fields) != 2 {
		t.Fatalf("invalid readiness marker %q", data)
	}
	var pids []int
	for _, field := range fields {
		pid, err := strconv.Atoi(field)
		if err != nil || pid <= 0 {
			t.Fatalf("invalid PID %q: %v", field, err)
		}
		pids = append(pids, pid)
	}
	return pids
}

func waitCommandGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
			return
		}
		// An orphaned child can remain a zombie until the host's init reaps
		// it; it no longer executes or holds the command's output pipes.
		out, _ := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
		if strings.HasPrefix(strings.TrimSpace(string(out)), "Z") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("command process %d still alive", pid)
}

// An init that exits non-zero fails the placement before the workload
// starts, with lux's reason.
func TestAFailingInitFailsThePlacement(t *testing.T) {
	fake := New("", "k", func(map[string]any) Behaviour { return Behaviour{Hang: true} })
	t.Cleanup(fake.Close)
	srv := httptest.NewServer(fake.Handler())
	t.Cleanup(srv.Close)
	client := lux.New(srv.URL, "k")
	run, err := client.Submit(context.Background(), lux.Spec{Image: lux.Image{Ref: "agent:1"},
		Workload: lux.Workload{Adapter: "generic", Command: []string{"true"}},
		Init:     &lux.Init{Script: "exit 3"}}, "init-fails")
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, client, run.ID, "failed")
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if got, _ := client.Get(context.Background(), run.ID); got.StateReason == "init-failed" {
			return
		}
	}
	got, _ := client.Get(context.Background(), run.ID)
	t.Errorf("failed with reason %q", got.StateReason)
}
