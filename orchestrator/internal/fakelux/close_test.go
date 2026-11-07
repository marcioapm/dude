package fakelux

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// The follower may hold checkout/sync until it has observed running. Init
// may still be in flight: running is a lifecycle event, not workload readiness.
func TestRunningPrecedesHeldCheckoutAndSync(t *testing.T) {
	for _, resumed := range []bool{false, true} {
		t.Run(map[bool]string{false: "checkout", true: "sync"}[resumed], func(t *testing.T) {
			bare := t.TempDir()
			git := func(args ...string) string {
				t.Helper()
				out, err := exec.Command("git", append([]string{"-c", "user.name=Test", "-c", "user.email=test@example.com"}, args...)...).CombinedOutput()
				if err != nil {
					t.Fatalf("git %v: %v: %s", args, err, out)
				}
				return strings.TrimSpace(string(out))
			}
			git("init", "--bare", "--initial-branch=main", bare)
			tree := git("-C", bare, "mktree")
			sha := git("-C", bare, "commit-tree", tree, "-m", "initial")
			git("-C", bare, "update-ref", "refs/heads/main", sha)
			fake := New(bare, "k", func(map[string]any) Behaviour { return Behaviour{Hang: true} })
			fake.Workspaces = t.TempDir()
			client := c0(t, fake)
			t.Cleanup(fake.Close)
			ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
			defer cancel()
			run, err := client.Submit(ctx, lux.Spec{
				Workload: lux.Workload{Adapter: "opencode", Prompt: "go"},
				Git:      &lux.Git{Repositories: []lux.Repository{{Name: "repo", URL: bare, Ref: "main"}}},
				Init:     &lux.Init{Script: "echo ready > /workspace/ready"},
			}, "")
			if err != nil {
				t.Fatal(err)
			}
			epoch, held := 1, "git.checkout"
			if resumed {
				waitState(t, client, run.ID, "running")
				if err := client.Stop(ctx, run.ID); err != nil {
					t.Fatal(err)
				}
				waitState(t, client, run.ID, "stopped")
				_, err = client.Resume(ctx, run.ID, lux.ResumeInput{Sync: []lux.SyncRef{{Repo: "repo", Ref: "main"}}})
				if err != nil {
					t.Fatal(err)
				}
				epoch, held = 2, "git.sync"
			}
			running := make(chan struct{})
			var observed sync.Once
			fake.BeforeEvent(func(id string, eventID int64, typ string) {
				fake.mu.Lock()
				eventEpoch := fake.runs[id].events[eventID-1].Epoch
				fake.mu.Unlock()
				if typ == held && eventEpoch == epoch {
					select {
					case <-running:
					case <-ctx.Done():
						t.Error("held checkout/sync never observed running")
					}
				}
			})
			err = client.Output(ctx, run.ID, "", 0, func(f lux.Frame) error {
				if f.Kind != "lux" || f.Epoch != epoch {
					return nil
				}
				if f.EventType == "state" {
					var data struct{ State string }
					_ = json.Unmarshal(f.EventData, &data)
					if data.State == "running" {
						observed.Do(func() { close(running) })
					}
				}
				if f.EventType == held {
					return errStreamDone
				}
				return nil
			})
			if err != errStreamDone {
				t.Fatalf("stream: %v", err)
			}
		})
	}
}

func TestCloseCancelsExecWriter(t *testing.T) {
	fake := New("", "k", func(map[string]any) Behaviour { return Behaviour{Hang: true} })
	fake.Workspaces = t.TempDir()
	client, run := submitRun(t, fake)
	waitState(t, client, run.ID, "running")
	probe := newCommandProbe(t)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = client.Exec(context.Background(), run.ID, []string{"sh", "-c", holdScript + "\necho late > /workspace/late", "probe", probe.dir})
	}()
	ready := probe.awaitReady(t)
	fake.Close()
	probe.awaitGone(t, ready)
	select {
	case <-done:
	case <-time.After(probeTimeout):
		t.Fatal("exec request outlived Close")
	}
	if _, err := os.Stat(filepath.Join(fake.Workspace(run.ID), "late")); !os.IsNotExist(err) {
		t.Fatalf("late exec write: %v", err)
	}
	if _, err := client.Exec(context.Background(), run.ID, []string{"touch", "/workspace/late"}); err == nil {
		t.Fatal("exec admitted after Close")
	}
}

func TestCloseCancelsBeforeStopHTTPWriter(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(entered) })
		<-release
		_, _ = w.Write([]byte("{}"))
	}))
	t.Cleanup(service.Close)
	t.Cleanup(func() { close(release) })
	fake := New("", "k", func(map[string]any) Behaviour { return Behaviour{Hang: true, Command: []string{"true"}} })
	fake.Workspaces = t.TempDir()
	client := c0(t, fake)
	run, err := client.Submit(context.Background(), lux.Spec{Workload: lux.Workload{
		Adapter: "opencode", Prompt: "go",
		BeforeStop: &lux.BeforeStop{Command: []string{"sh", "-c", "echo late > /workspace/late"}},
		Services:   []lux.Service{{Name: "dude", URL: service.URL}},
	}}, "")
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, client, run.ID, "running")
	// Wait for the command to create the checkout before beforeStop inspects it.
	awaitRun(t, fake, run.ID, "workload never finished startup", func(r *Run) bool { return r.busy })
	done := make(chan struct{})
	go func() { defer close(done); _ = client.Stop(context.Background(), run.ID) }()
	select {
	case <-entered:
	case <-time.After(probeTimeout):
		t.Fatal("beforeStop never reached HTTP")
	}
	fake.Close()
	select {
	case <-done:
	case <-time.After(probeTimeout):
		t.Fatal("stop request outlived Close")
	}
	if _, err := os.Stat(filepath.Join(fake.Workspace(run.ID), "late")); !os.IsNotExist(err) {
		t.Fatalf("late beforeStop write: %v", err)
	}
}
