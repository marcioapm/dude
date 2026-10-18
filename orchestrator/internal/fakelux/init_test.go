package fakelux

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
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

// holdScript is a workload command for a commandProbe ($1): it starts a
// descendant that inherits the command's stdout, records both PIDs, and
// blocks until the test releases it.
const holdScript = `ctl=$1
mkfifo "$ctl/alive-$$"
exec 5<>"$ctl/alive-$$"
sleep 300 &
child=$!
echo buffered-before-exit
printf '%s %s\n' "$$" "$child" > "$ctl/ready-$$.tmp"
mv "$ctl/ready-$$.tmp" "$ctl/ready-$$"
printf '%s\n' "$$" > "$ctl/notify"
read -r _ < "$ctl/release"
wait "$child"
`

func TestBehaviourCommandCancellation(t *testing.T) {
	for _, action := range []string{"stop", "terminate", "cancel", "legacy-cancel", "close"} {
		t.Run(action, func(t *testing.T) {
			probe := newCommandProbe(t)
			fake := New("", "k", func(map[string]any) Behaviour {
				return Behaviour{Hang: true, Command: []string{"sh", "-eu", "-c", holdScript, "sh", probe.dir}}
			})
			fake.Workspaces = t.TempDir()
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
			first := probe.awaitReady(t)
			// Held input is checked at the end: no record of it may appear.
			if err := client.Input(ctx, run.ID, lux.InputRequest{RequestID: "queued", Text: "wait for command"}); err != nil {
				t.Fatal(err)
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
			var second commandReady
			if action == "stop" {
				awaitStateEvent(t, ctx, client, run.ID, "stopped")
				if _, err := client.Resume(ctx, run.ID, lux.ResumeInput{}); err != nil {
					t.Fatal(err)
				}
				// The first placement's completion is held until a newer
				// placement owns the Run, so it is stale when it lands.
				second = probe.awaitReady(t)
				if second.shell == first.shell {
					t.Fatal("resume did not start a new command")
				}
				release()
			}
			select {
			case epoch := <-finished:
				if epoch != 1 {
					t.Fatalf("completed epoch %d, want 1", epoch)
				}
			case <-ctx.Done():
				t.Fatal("old placement did not finish")
			}
			probe.awaitGone(t, first)
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
				t.Fatalf("cancelled command published buffered output, input or an agent turn: %+v", records)
			}
			if action == "stop" {
				fake.Close()
				probe.awaitGone(t, second)
			}
		})
	}
}

func TestBehaviourCommandSuccessEmitsBufferedOutputThenRunsTheAgent(t *testing.T) {
	probe := newCommandProbe(t)
	script := `ctl=$1
mkfifo "$ctl/alive-$$"
exec 5<>"$ctl/alive-$$"
sleep 300 >/dev/null 2>&1 &
child=$!
echo first
printf '%s %s\n' "$$" "$child" > "$ctl/ready-$$.tmp"
mv "$ctl/ready-$$.tmp" "$ctl/ready-$$"
printf '%s\n' "$$" > "$ctl/notify"
read -r _ < "$ctl/release"
echo second
`
	fake := New("", "k", func(map[string]any) Behaviour {
		return Behaviour{Reply: "done", Command: []string{"sh", "-eu", "-c", script, "sh", probe.dir}}
	})
	fake.Workspaces = t.TempDir()
	t.Cleanup(fake.Close)
	srv := httptest.NewServer(fake.Handler())
	t.Cleanup(srv.Close)
	client := lux.New(srv.URL, "k")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	run, err := client.Submit(ctx, lux.Spec{Image: lux.Image{Ref: "agent:1"},
		Workload: lux.Workload{Adapter: "acp", Prompt: "task"}}, "command-success")
	if err != nil {
		t.Fatal(err)
	}
	ready := probe.awaitReady(t)
	if err := client.Input(ctx, run.ID, lux.InputRequest{RequestID: "queued", Text: "after command"}); err != nil {
		t.Fatal(err)
	}
	probe.release(t)
	// The shell exits on its own; its process-group descendant is killed.
	probe.awaitGone(t, ready)
	turnEnds := 0
	records := followRecords(t, ctx, client, run.ID, func(f lux.Frame) bool {
		if f.Event.Type == "acp.turn_end" {
			turnEnds++
		}
		return turnEnds == 2
	})
	// Records are appended in order, so anything the held input produced
	// before the command completed would precede the command's output.
	if records[0].Event.Type != "acp.agent_message_chunk" || messageText(t, records[0]) != "first\nsecond\n" {
		t.Fatalf("first record %s %s, want the command's whole output", records[0].Event.Type, records[0].Event.Data)
	}
	if n := countText(t, records, "first\nsecond\n"); n != 1 {
		t.Fatalf("command output emitted %d times", n)
	}
	session := indexOf(t, records, 0, "lux.session", "")
	promptEnd := indexOf(t, records, session, "acp.turn_end", "")
	accepted := indexOf(t, records, promptEnd, lux.RecordInput, "queued")
	consumed := indexOf(t, records, accepted, lux.RecordInputConsumed, "queued")
	indexOf(t, records, consumed, "acp.turn_end", "")
	got, err := client.Get(ctx, run.ID)
	if err != nil || got.State != "running" || got.StateReason != "" {
		t.Fatalf("after successful command: %+v, %v", got, err)
	}
}

func TestBehaviourCommandFailureFailsThePlacement(t *testing.T) {
	fake := New("", "k", func(map[string]any) Behaviour {
		return Behaviour{Reply: "unreachable", Command: []string{"sh", "-c", "echo failing-output; exit 7"}}
	})
	fake.Workspaces = t.TempDir()
	t.Cleanup(fake.Close)
	srv := httptest.NewServer(fake.Handler())
	t.Cleanup(srv.Close)
	client := lux.New(srv.URL, "k")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	run, err := client.Submit(ctx, lux.Spec{Image: lux.Image{Ref: "agent:1"},
		Workload: lux.Workload{Adapter: "acp", Prompt: "task"}}, "command-fails")
	if err != nil {
		t.Fatal(err)
	}
	var records []lux.Frame
	end := lux.Frame{}
	err = client.Output(ctx, run.ID, "", 0, func(f lux.Frame) error {
		switch f.Kind {
		case "record":
			records = append(records, f)
		case "end":
			end = f
			return errStreamDone
		}
		return nil
	})
	if !errors.Is(err, errStreamDone) {
		t.Fatalf("output stream ended without an end frame after %d records: %v", len(records), err)
	}
	if end.State != "failed" {
		t.Fatalf("stream ended in %q, want failed", end.State)
	}
	got, err := client.Get(ctx, run.ID)
	if err != nil || got.State != "failed" || got.StateReason != "workload-failed" {
		t.Fatalf("after failing command: %+v, %v", got, err)
	}
	if len(records) != 1 || records[0].Event == nil || records[0].Event.Type != "acp.agent_message_chunk" ||
		messageText(t, records[0]) != "failing-output\n" {
		t.Fatalf("records %+v, want only the command's output and no agent turn", records)
	}
}

var errStreamDone = errors.New("stream read complete")

// commandProbe is a directory of FIFOs a workload command and its test
// synchronise on. notify carries one line per placement, written after its
// ready-PID record is renamed into place; release unblocks every waiting
// command; alive-PID is held open by a command and its descendants, so its
// reader sees EOF only once all of them have exited.
type commandProbe struct {
	dir     string
	notify  *os.File
	ready   chan string
	control *os.File
}

type commandReady struct {
	shell, child int
	alive        string
}

const probeTimeout = 10 * time.Second

func newCommandProbe(t *testing.T) *commandProbe {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"notify", "release"} {
		if err := syscall.Mkfifo(filepath.Join(dir, name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Read-write opens never block and keep a writer present, so neither the
	// test nor a command waits for the other side to open its end.
	notify, err := os.OpenFile(filepath.Join(dir, "notify"), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	control, err := os.OpenFile(filepath.Join(dir, "release"), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	p := &commandProbe{dir: dir, notify: notify, ready: make(chan string, 8), control: control}
	go func() {
		scanner := bufio.NewScanner(notify)
		for scanner.Scan() {
			if scanner.Text() == "" {
				return
			}
			p.ready <- scanner.Text()
		}
	}()
	t.Cleanup(func() {
		// An empty line ends the reader: closing a FIFO does not interrupt a
		// blocked read on every platform.
		_, _ = notify.WriteString("\n")
		notify.Close()
		control.Close()
	})
	return p
}

// awaitReady returns the next placement's complete ready record.
func (p *commandProbe) awaitReady(t *testing.T) commandReady {
	t.Helper()
	var shell string
	select {
	case shell = <-p.ready:
	case <-time.After(probeTimeout):
		entries, _ := os.ReadDir(p.dir)
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("no command became ready within %s; probe directory holds %v", probeTimeout, names)
	}
	data, err := os.ReadFile(filepath.Join(p.dir, "ready-"+shell))
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(string(data))
	if len(fields) != 2 || fields[0] != shell {
		t.Fatalf("ready record %q does not match notification %q", data, shell)
	}
	var pids [2]int
	for i, field := range fields {
		pid, err := strconv.Atoi(field)
		if err != nil || pid <= 0 {
			t.Fatalf("invalid PID %q in ready record %q: %v", field, data, err)
		}
		pids[i] = pid
	}
	return commandReady{shell: pids[0], child: pids[1], alive: filepath.Join(p.dir, "alive-"+shell)}
}

func (p *commandProbe) release(t *testing.T) {
	t.Helper()
	if _, err := p.control.WriteString("go\n"); err != nil {
		t.Fatal(err)
	}
}

// awaitGone waits for EOF on the placement's alive FIFO: every process that
// inherited it, the shell and its descendant, has exited.
func (p *commandProbe) awaitGone(t *testing.T, ready commandReady) {
	t.Helper()
	// O_NONBLOCK only so the open does not wait for a writer; reads block.
	fd, err := syscall.Open(ready.alive, syscall.O_RDONLY|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.SetNonblock(fd, false); err != nil {
		syscall.Close(fd)
		t.Fatal(err)
	}
	alive := os.NewFile(uintptr(fd), ready.alive)
	eof := make(chan error, 1)
	go func() {
		defer alive.Close()
		_, err := io.Copy(io.Discard, alive)
		eof <- err
	}()
	select {
	case err := <-eof:
		if err != nil {
			t.Fatalf("reading %s: %v", ready.alive, err)
		}
	case <-time.After(probeTimeout):
		t.Fatalf("command processes still hold %s after %s: shell %d (%v), child %d (%v)", ready.alive, probeTimeout,
			ready.shell, syscall.Kill(ready.shell, 0), ready.child, syscall.Kill(ready.child, 0))
	}
}

// awaitStateEvent follows the Run's lifecycle events until one reports want,
// failing with the last state seen.
func awaitStateEvent(t *testing.T, ctx context.Context, client *lux.HTTPClient, id, want string) {
	t.Helper()
	streamCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	last := ""
	err := client.Output(streamCtx, id, "", 0, func(f lux.Frame) error {
		switch {
		case f.Kind == "lux" && f.EventType == "state":
			var data struct{ State string }
			if err := json.Unmarshal(f.EventData, &data); err != nil {
				return err
			}
			last = data.State
		case f.Kind == "end":
			last = f.State
		default:
			return nil
		}
		if last == want {
			return errStreamDone
		}
		return nil
	})
	if !errors.Is(err, errStreamDone) {
		t.Fatalf("Run %s never reported state %s; last state %q: %v", id, want, last, err)
	}
}

// followRecords reads the Run's records from the start until done accepts
// one, and returns them all.
func followRecords(t *testing.T, ctx context.Context, client *lux.HTTPClient, id string, done func(lux.Frame) bool) []lux.Frame {
	t.Helper()
	streamCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	var records []lux.Frame
	err := client.Output(streamCtx, id, "", 0, func(f lux.Frame) error {
		if f.Kind != "record" || f.Event == nil {
			return nil
		}
		records = append(records, f)
		if done(f) {
			return errStreamDone
		}
		return nil
	})
	if !errors.Is(err, errStreamDone) {
		var types []string
		for _, r := range records {
			types = append(types, r.Event.Type)
		}
		t.Fatalf("record stream stopped before completion: %v; records %v", err, types)
	}
	return records
}

func messageText(t *testing.T, f lux.Frame) string {
	t.Helper()
	var update struct {
		Content struct{ Text string } `json:"content"`
	}
	if err := json.Unmarshal(f.Event.Data, &update); err != nil {
		t.Fatal(err)
	}
	return update.Content.Text
}

func countText(t *testing.T, records []lux.Frame, text string) int {
	t.Helper()
	n := 0
	for _, r := range records {
		if r.Event.Type == "acp.agent_message_chunk" && messageText(t, r) == text {
			n++
		}
	}
	return n
}

// indexOf finds the first record after from of type typ, and with
// requestID when it is not empty.
func indexOf(t *testing.T, records []lux.Frame, from int, typ, requestID string) int {
	t.Helper()
	for i := from + 1; i < len(records); i++ {
		if records[i].Event.Type != typ {
			continue
		}
		var data struct {
			RequestID string `json:"requestId"`
		}
		_ = json.Unmarshal(records[i].Event.Data, &data)
		if requestID == "" || data.RequestID == requestID {
			return i
		}
	}
	var types []string
	for _, r := range records {
		types = append(types, r.Event.Type)
	}
	t.Fatalf("no %s %s record after index %d in %v", typ, requestID, from, types)
	return -1
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
