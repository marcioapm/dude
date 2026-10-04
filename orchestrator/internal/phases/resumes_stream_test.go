package phases

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// gatedLux is lux whose Gets each wait to be let through, by the order
// they were asked in; output streams as streamLux's.
type gatedLux struct {
	*streamLux
	mu      sync.Mutex
	gates   []chan struct{}
	entered chan int
}

func (f *gatedLux) Get(ctx context.Context, id string) (lux.Run, error) {
	f.mu.Lock()
	n := len(f.gates)
	gate := make(chan struct{})
	f.gates = append(f.gates, gate)
	f.mu.Unlock()
	f.entered <- n
	select {
	case <-gate:
	case <-ctx.Done():
		return lux.Run{}, ctx.Err()
	}
	return f.placementLux.Get(ctx, id)
}

func (f *gatedLux) release(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	close(f.gates[n])
}

// While lux is slow to answer the Gets that read a resume's placements,
// the Run's stream goes on: its running state and the agent's first words
// are committed before either Get answers. One Get is let through and its
// follow-up seen to finish, then the other: in either order, the resume
// is timed once, for its epoch, with the placements lux reported.
func TestASlowLuxHoldsUpNoFramesAndTheResumeIsTimedOnce(t *testing.T) {
	for _, order := range [][2]int{{0, 1}, {1, 0}} {
		t.Run(map[int]string{0: "the first Get answers first", 1: "the second Get answers first"}[order[0]], func(t *testing.T) {
			w := newResumeWorld(t)
			base := time.Now().Add(-time.Minute).UTC().Truncate(time.Millisecond)
			w.sessionOn(1)
			w.resume(stoppedOnHost1(base))
			// lux has yet to report the image when the stream reaches the
			// first output, so the first output reads again.
			partial := runningAgain(base, "host-a")
			partial.Placements[1].ImageReadyAt = nil
			w.lux.set(partial, nil)
			finished := make(chan struct{}, 4)
			w.s.followedUp = func() { finished <- struct{}{} }
			st := w.following()
			gated := &gatedLux{streamLux: st, entered: make(chan int, 4)}
			w.s.Lux = gated

			st.frames <- session(2)
			st.frames <- cursorFrame(running(2), "c1")
			first := waitEntered(t, gated)
			st.frames <- busy(2)
			st.frames <- cursorFrame(spoke(2), "c2")
			second := waitEntered(t, gated)
			w.committed("c2", time.Second)
			var buffered string
			_ = w.owner.QueryRow(w.ctx, `SELECT agent_message_buffer FROM runs WHERE id = $1`, w.run.ID).Scan(&buffered)
			if buffered != "On it." {
				t.Errorf("the agent's words were not committed while lux was slow: %q", buffered)
			}
			if n := len(w.timed()); n != 0 {
				t.Fatalf("timed before lux answered: %d", n)
			}

			// Both Gets answer with the placement complete.
			w.lux.set(runningAgain(base, "host-a"), nil)
			for _, n := range order {
				gated.release([]int{first, second}[n])
				waitFinished(t, finished)
			}
			got := w.timed()
			if len(got) != 1 {
				t.Fatalf("%d run.resume.timed, want 1", len(got))
			}
			phases := got[0]["phases"].(map[string]any)
			if got[0]["epoch"] != 2.0 || len(phases) != 8 {
				t.Errorf("timed %v, want epoch 2 with every phase", got[0])
			}
			// lux's own phases, from its placement: assigned +100ms, image
			// +200ms, restored +400ms, workload +600ms.
			for name, want := range map[string]float64{"image": 100, "restore": 200, "start": 200} {
				if phases[name] != want {
					t.Errorf("%s = %v, want %v", name, phases[name], want)
				}
			}
		})
	}
}

func waitFinished(t *testing.T, finished chan struct{}) {
	t.Helper()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("the follow-up whose Get answered did not finish")
	}
}

func waitEntered(t *testing.T, g *gatedLux) int {
	t.Helper()
	select {
	case n := <-g.entered:
		return n
	case <-time.After(5 * time.Second):
		t.Fatal("lux was never asked for the resume's placements")
		return 0
	}
}

// A stream replayed from the start carries every earlier placement's
// session, busy and words: none of them stamps the resume into the
// current epoch, and its own frames do.
func TestEarlierEpochsReplayedNeverStampTheCurrentResume(t *testing.T) {
	w := newResumeWorld(t)
	base := time.Now().Add(-time.Hour).UTC()
	w.lux.set(runningAgain(base, "host-a"), nil)
	w.exec(`INSERT INTO run_resumes (run_id, organization_id, epoch, cause, woken_at, requested_at,
		running_at, busy_at, first_output_at, timed_at) VALUES ($1, $2, 2, 'person', $3, $3, $3, $3, $3, $3)`,
		w.run.ID, w.run.Org, base)
	w.resume(lux.Run{ID: "lrun_1", State: "stopped", Epoch: 2, Placements: []lux.Placement{{Epoch: 2, HostName: "host-a",
		State: "exited", ExitedAt: &base}}})
	if w.row(3)["requested_at"] == nil {
		t.Fatal("no resume into epoch 3")
	}
	before2 := w.row(2)
	var replay []lux.Frame
	for _, e := range []int{1, 2} {
		replay = append(replay, session(e), busy(e), spoke(e),
			record(e, "acp.tool_call", map[string]any{"sessionUpdate": "tool_call", "toolCallId": "t", "title": "ls"}), running(e))
	}
	w.follow(replay...)
	row := w.row(3)
	for _, col := range []string{"running_at", "busy_at", "first_output_at"} {
		if row[col] != nil {
			t.Errorf("an earlier epoch's frame stamped the current resume's %s", col)
		}
	}
	if after2 := w.row(2); after2["first_output_at"] != before2["first_output_at"] || after2["busy_at"] != before2["busy_at"] {
		t.Errorf("the replay moved epoch 2's stamps")
	}
	w.follow(session(3), busy(3), spoke(3))
	row = w.row(3)
	for _, col := range []string{"running_at", "busy_at", "first_output_at"} {
		if row[col] == nil {
			t.Errorf("epoch 3's own frame did not stamp its %s", col)
		}
	}
}

// The first thing the agent says or does — a message, a thought, a tool
// call or a tool call's update — ends a resume's timing; its plan, a usage
// report or its commands list does not.
func TestWhatEndsAResumesTimingIsTheAgentSayingOrDoingSomething(t *testing.T) {
	update := func(kind string, extra map[string]any) map[string]any {
		m := map[string]any{"sessionUpdate": kind}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	notOutput := []lux.Frame{
		record(2, "acp.plan", update("plan", map[string]any{"entries": []any{map[string]any{"content": "look", "status": "pending"}}})),
		record(2, "acp.usage_update", update("usage_update", map[string]any{"used": 10, "size": 1000})),
		record(2, "acp.available_commands_update", update("available_commands_update", map[string]any{"availableCommands": []any{}})),
	}
	for kind, data := range map[string]map[string]any{
		"agent_message_chunk": {"content": map[string]any{"type": "text", "text": "On it."}},
		"agent_thought_chunk": {"content": map[string]any{"type": "text", "text": "Hmm."}},
		"tool_call":           {"toolCallId": "t1", "title": "ls", "status": "pending"},
		"tool_call_update":    {"toolCallId": "t1", "status": "completed"},
	} {
		t.Run(kind, func(t *testing.T) {
			w := newResumeWorld(t)
			w.resume(stoppedOnHost1(time.Now()))
			w.lux.set(runningAgain(time.Now(), "host-a"), nil)
			// Each frame on its own, so one that ends the timing and a later
			// one that hides it are both caught; nothing is published yet.
			for _, f := range append([]lux.Frame{running(2), busy(2)}, notOutput...) {
				w.follow(f)
				if row := w.row(2); row["first_output_at"] != nil {
					t.Fatalf("a plan, usage or commands list ended the timing")
				}
				if n := len(w.timed()); n != 0 {
					t.Fatalf("%d run.resume.timed before the agent said or did anything, want 0", n)
				}
			}
			w.follow(record(2, "acp."+kind, update(kind, data)))
			if row := w.row(2); row["first_output_at"] == nil {
				t.Errorf("%s did not end the timing", kind)
			}
			timed := w.timed()
			if len(timed) != 1 {
				t.Fatalf("%d run.resume.timed, want 1", len(timed))
			}
			phases, _ := timed[0]["phases"].(map[string]any)
			if timed[0]["totalMs"] == nil || phases == nil || phases["firstOutput"] == nil {
				t.Errorf("the timing has no total or first output: %v", timed[0])
			}
		})
	}
}
