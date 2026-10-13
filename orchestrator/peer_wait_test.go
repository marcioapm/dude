package orchestrator_test

import (
	"context"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakeagent"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

// onAnEpic puts the world's tasks on one epic: their agents are peers.
func (w *world) onAnEpic(tasks ...string) string {
	w.t.Helper()
	epic := "epc_" + w.org
	mustExec(w.t, w.owner, `INSERT INTO epics (id, organization_id, project_id, title) VALUES ($1, $2, $3, 'Preview')
		ON CONFLICT DO NOTHING`, epic, w.org, w.project)
	for _, id := range tasks {
		mustExec(w.t, w.owner, `UPDATE tasks SET epic_id = $2 WHERE id = $1`, id, epic)
	}
	return epic
}

// key is a task's key in the world's project.
func (w *world) key(task string) string {
	var k string
	_ = w.owner.QueryRow(context.Background(), `SELECT 'P-' || number FROM tasks WHERE id = $1`, task).Scan(&k)
	return k
}

// peerRun is an ended implementer Run of task, made by hand: a peer to
// answer as, with no agent the syncer would drive.
func (w *world) peerRun(task string) delivery.RunRef {
	w.t.Helper()
	id := "run_peer_" + task
	mustExec(w.t, w.owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, kind)
		VALUES ($1, $2, $3, $4, 1, 'completed', 'implement', 'implementer', 'agent')`, id, w.org, w.project, task)
	return delivery.RunRef{Org: w.org, ProjectID: w.project, TaskID: task, RunID: id}
}

// send messages toTask as from's agent would, with message's tool; replyTo
// "" opens a thread.
func (w *world) send(from delivery.RunRef, toTask, replyTo, text string) delivery.Sent {
	w.t.Helper()
	ctx := context.Background()
	var sent delivery.Sent
	if err := w.app.InOrg(ctx, w.org, func(tx pgx.Tx) error {
		peer, ok, err := delivery.PeerByKey(ctx, tx, from.TaskID, w.key(toTask))
		if err != nil || !ok {
			w.t.Fatalf("%s is not a peer: %v", toTask, err)
		}
		sent, err = delivery.SendMessageTx(ctx, tx, from, delivery.MessageInput{To: peer, Text: text, ReplyTo: replyTo})
		return err
	}); err != nil {
		w.t.Fatal(err)
	}
	return sent
}

// An agent that asks a peer and runs `dude wait` waits as on a person: it
// stays live through the project's grace, then is parked; the peer's reply
// resumes it, given to it as its next input, and it finishes.
func TestWaitForReplyParksAfterTheGraceAndTheReplyResumesTheRun(t *testing.T) {
	w := newWorld(t)
	w.withTools()
	w.syncer.ParkAfter = 300 * time.Millisecond
	wi, peer := w.task(), w.task()
	w.onAnEpic(wi, peer)
	ask := `{"to":"` + w.key(peer) + `","text":"Which body does your route take?"}`
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] != "implement" {
			return fakelux.Behaviour{Hang: true}
		}
		return fakelux.Behaviour{CallTools: [][2]string{{"message", ask}, {"wait_for_reply", `{"message":"{{last.id}}"}`}},
			Reply: "Wired the client.", Commit: map[string]string{"a.md": "x\n"}, Message: "work"}
	}
	w.deliver(wi)
	var runID, message string
	w.until("the agent to wait on its peer", func() bool {
		_ = w.owner.QueryRow(context.Background(), `SELECT w.run_id, w.message_id FROM peer_waits w JOIN runs r ON r.id = w.run_id
			WHERE r.task_id = $1 AND w.closed_at IS NULL AND w.how = 'wait'`, wi).Scan(&runID, &message)
		return runID != "" && w.count(`SELECT count(*) FROM runs WHERE id = $1 AND waiting_since IS NOT NULL`, runID) == 1
	})
	w.until("the run to be parked after the grace", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND dude_pause = 'person'`, runID) == 1
	})
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.parked'
		AND payload->>'message' = 'parked while it waits on a peer''s reply'`, runID); n != 1 {
		t.Errorf("%d parks saying it waits on a peer", n)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'completed'`, runID); n != 0 {
		t.Fatal("the waiting Run completed")
	}
	for range 5 {
		w.pump()
	}
	if w.lux.Runs()[0].Resumed != 0 {
		t.Fatal("resumed with no reply")
	}
	w.send(w.peerRun(peer), wi, message, "{metric, from, period}")
	w.until("the implementer to finish", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'completed'`, runID) == 1
	})
	if lr := w.lux.Runs()[0]; lr.Resumed != 1 || len(lr.Inputs) != 1 || !strings.Contains(lr.Inputs[0], "{metric, from, period}") ||
		!strings.Contains(lr.Inputs[0], "Reply to "+message) {
		t.Errorf("resumed %d times with %q, want the reply", lr.Resumed, lr.Inputs)
	}
	if n := w.count(`SELECT count(*) FROM peer_messages WHERE reply_to = $1 AND read_at IS NOT NULL`, message); n != 1 {
		t.Errorf("the reply's read is not recorded")
	}
	if n := w.count(`SELECT count(*) FROM run_resumes rr JOIN peer_waits pw ON pw.run_id = rr.run_id
		WHERE rr.run_id = $1 AND rr.woken_at >= pw.closed_at`, runID); n != 1 {
		t.Errorf("%d resumes timed from the reply", n)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'agent.message.received'`, runID); n != 1 {
		t.Errorf("%d received events on the waiting Run", n)
	}
}

// An agent that waits on a peer and then moves machine keeps waiting: the
// move's resume and settlement leave the wait open and the turn held, and
// the reply after the move reaches the agent once.
func TestAWaitForReplyCarriesAcrossAMachineMoveAndTheReplyLandsOnce(t *testing.T) {
	w := newWorld(t)
	w.withTools()
	w.syncer.ParkAfter = 300 * time.Millisecond
	mustExec(t, w.owner, `INSERT INTO machine_sizes (id, organization_id, name, cpus, memory_mib, disk_gib) VALUES ('msz_big', $1, $2, 4, 16384, 40)`,
		w.org, fakeagent.MachineSize)
	wi, peer := w.task(), w.task()
	w.onAnEpic(wi, peer)
	ask := `{"to":"` + w.key(peer) + `","text":"Which body does your route take?"}`
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] != "implement" {
			return fakelux.Behaviour{Hang: true}
		}
		return fakelux.Behaviour{CallTools: [][2]string{{"message", ask}, {"wait_for_reply", `{"message":"{{last.id}}"}`}},
			Machine: fakeagent.MachineRequest, Reply: "Wired the client.", Commit: map[string]string{"a.md": "x\n"}, Message: "work"}
	}
	w.deliver(wi)
	var runID, message string
	w.until("the agent to wait on its peer and ask to move", func() bool {
		_ = w.owner.QueryRow(context.Background(), `SELECT w.run_id, w.message_id FROM peer_waits w JOIN runs r ON r.id = w.run_id
			WHERE r.task_id = $1 AND w.how = 'wait'`, wi).Scan(&runID, &message)
		return runID != "" && w.count(`SELECT count(*) FROM machine_moves WHERE run_id = $1`, runID) == 1
	})
	// The resumed turn on the new size ends with the wait still open.
	w.until("the move settled and the resumed turn over", func() bool {
		return w.count(`SELECT count(*) FROM machine_moves WHERE run_id = $1 AND settled_at IS NOT NULL`, runID) == 1 &&
			slices.Contains(w.said(runID), "Wired the client.")
	})
	for range 5 {
		w.pump()
	}
	if n := w.count(`SELECT count(*) FROM peer_waits WHERE run_id = $1 AND closed_at IS NULL`, runID); n != 1 {
		t.Errorf("%d open waits after the move, want 1", n)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND waiting_since IS NOT NULL`, runID); n != 1 {
		t.Errorf("the Run is not waiting after the move:\n%s", w.describeRuns())
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'completed'`, runID); n != 0 {
		t.Fatal("the waiting Run completed across the move")
	}
	const reply = "{metric, from, period}"
	w.send(w.peerRun(peer), wi, message, reply)
	w.until("the implementer to finish", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'completed'`, runID) == 1
	})
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'agent.message.received'`, runID); n != 1 {
		t.Errorf("%d received events on the waiting Run, want 1", n)
	}
	live := w.str(`SELECT lux_run_id FROM runs WHERE id = $1`, runID)
	for _, lr := range w.lux.Runs() {
		if lr.ID != live {
			continue
		}
		if n := strings.Count(strings.Join(lr.Inputs, "\n"), reply); n != 1 {
			t.Errorf("the reply reached the agent %d times: %q", n, lr.Inputs)
		}
	}
}

// raised records the task's implementer, and its Run, as raised to Bigger,
// so a Reset moves the Run back to Standard.
func (w *world) raised(task, runID string) {
	w.t.Helper()
	mustExec(w.t, w.owner, `INSERT INTO machine_sizes (id, organization_id, name, cpus, memory_mib, disk_gib) VALUES ('msz_big', $1, 'Bigger', 4, 16384, 40)`, w.org)
	mustExec(w.t, w.owner, `UPDATE tasks SET agent_machines=jsonb_build_object('implementer', jsonb_build_object('sizeId','msz_big','moveId','mmv_old','askedBy',$2::text,'reason','oom','at','2026-10-13T00:00:00Z')) WHERE id=$1`, task, runID)
	mustExec(w.t, w.owner, `UPDATE runs SET machine=machine||'{"sizeId":"msz_big","name":"Bigger","cpus":4,"memoryMiB":16384,"diskGiB":40,"from":"task"}' WHERE id=$1`, runID)
}

// A Run parked only on a peer's reply is resumed for a person's Reset at
// once: the move applies, timed and caused as a move, and the agent is told
// of it and of nothing else. Its wait stays open, so it parks on the peer
// again, and is not resumed again for the settled move; the reply then
// resumes it once.
func TestAResetResumesARunParkedOnlyOnAPeersReply(t *testing.T) {
	w := newWorld(t)
	w.withTools()
	w.syncer.ParkAfter = 300 * time.Millisecond
	wi, peer := w.task(), w.task()
	w.onAnEpic(wi, peer)
	ask := `{"to":"` + w.key(peer) + `","text":"Which body does your route take?"}`
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] != "implement" {
			return fakelux.Behaviour{Hang: true}
		}
		return fakelux.Behaviour{CallTools: [][2]string{{"message", ask}, {"wait_for_reply", `{"message":"{{last.id}}"}`}},
			Reply: "Wired the client.", Commit: map[string]string{"a.md": "x\n"}, Message: "work"}
	}
	w.deliver(wi)
	var runID, message string
	w.until("the agent to be parked on its peer", func() bool {
		_ = w.owner.QueryRow(context.Background(), `SELECT w.run_id, w.message_id FROM peer_waits w JOIN runs r ON r.id = w.run_id
			WHERE r.task_id = $1 AND w.closed_at IS NULL AND r.status = 'paused' AND r.dude_pause = 'person'`, wi).Scan(&runID, &message)
		return runID != ""
	})
	w.raised(wi, runID)
	live := w.str(`SELECT lux_run_id FROM runs WHERE id = $1`, runID)
	if status, out := w.callAs(w.person("Resetter"), "/internal/agent-machines/reset", map[string]any{"taskId": wi, "role": "implementer", "note": "smaller"}); status != 200 {
		t.Fatalf("reset: %d %v", status, out)
	}
	w.until("the Reset to move the parked Run", func() bool {
		return w.count(`SELECT count(*) FROM machine_moves WHERE run_id = $1 AND kind = 'reset' AND settled_at IS NOT NULL AND refused IS NULL`, runID) == 1
	})
	if got := w.machineOf(runID); got["name"] != "Standard" {
		t.Errorf("machine=%v", got)
	}
	if in := w.lux.ResumeInputs(live); len(in) != 1 || in[0] != "Moved back to Standard by Resetter: smaller" {
		t.Errorf("resume inputs %q, want the Reset's words alone", in)
	}
	if n := w.count(`SELECT count(*) FROM run_resumes rr JOIN machine_moves m ON m.run_id = rr.run_id
		WHERE rr.run_id = $1 AND rr.cause = 'machine' AND rr.woken_at = m.created_at`, runID); n != 1 {
		t.Errorf("%d resumes caused and timed by the move", n)
	}
	w.until("the agent to park on its peer again", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND dude_pause = 'person' AND waiting_since IS NOT NULL`, runID) == 1
	})
	if n := w.count(`SELECT count(*) FROM peer_waits WHERE run_id = $1 AND closed_at IS NULL`, runID); n != 1 {
		t.Errorf("%d open waits after the move, want 1", n)
	}
	for range 5 {
		w.pump()
	}
	if n := w.luxCalls(live, "resume"); n != 1 {
		t.Fatalf("resumed %d times before the reply, want the move's alone", n)
	}
	const reply = "{metric, from, period}"
	w.send(w.peerRun(peer), wi, message, reply)
	w.until("the implementer to finish", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'completed'`, runID) == 1
	})
	if n := w.luxCalls(live, "resume"); n != 2 {
		t.Errorf("resumed %d times, want the move's and the reply's", n)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'agent.message.received'`, runID); n != 1 {
		t.Errorf("%d received events, want 1", n)
	}
	for _, lr := range w.lux.Runs() {
		if lr.ID == live {
			if n := strings.Count(strings.Join(lr.Inputs, "\n"), reply); n != 1 {
				t.Errorf("the reply reached the agent %d times: %q", n, lr.Inputs)
			}
		}
	}
}

// A conductor parked only on a peer's reply is resumed for a person's
// Reset of its role as an implementer is: the move applies, the conductor
// is told of it alone, and it parks on the peer again with its wait open.
func TestAResetResumesAConductorParkedOnlyOnAPeersReply(t *testing.T) {
	w := conducting(t)
	w.syncer.ParkAfter = 300 * time.Millisecond
	task, peer := w.task(), w.task()
	w.onAnEpic(task, peer)
	conductor := w.talk(task)
	live := w.luxRunOf(conductor)
	ask := `{"to":"` + w.key(peer) + `","text":"Which body does your route take?"}`
	if status, out := w.chat(task, "Ask the peer.\ntool: message "+ask+"\ntool: wait_for_reply {\"message\":\"{{last.id}}\"}"); status >= 300 {
		t.Fatalf("chat: %d %v", status, out)
	}
	w.until("the conductor to be parked on its peer", func() bool {
		return w.count(`SELECT count(*) FROM runs r WHERE r.id = $1 AND r.status = 'paused' AND r.dude_pause = 'person'
			AND EXISTS (SELECT 1 FROM peer_waits pw WHERE pw.run_id = r.id AND pw.closed_at IS NULL AND pw.how = 'wait')`, conductor) == 1
	})
	resumesBefore := len(w.lux.ResumeInputs(live))
	mustExec(t, w.owner, `INSERT INTO machine_sizes (id, organization_id, name, cpus, memory_mib, disk_gib) VALUES ('msz_big', $1, 'Bigger', 4, 16384, 40)`, w.org)
	mustExec(t, w.owner, `UPDATE tasks SET agent_machines=jsonb_build_object('conductor', jsonb_build_object('sizeId','msz_big','moveId','mmv_old','askedBy',$2::text,'reason','oom','at','2026-10-13T00:00:00Z')) WHERE id=$1`, task, conductor)
	mustExec(t, w.owner, `UPDATE runs SET machine=machine||'{"sizeId":"msz_big","name":"Bigger","cpus":4,"memoryMiB":16384,"diskGiB":40,"from":"task"}' WHERE id=$1`, conductor)
	if status, out := w.callAs(w.person("Resetter"), "/internal/agent-machines/reset", map[string]any{"taskId": task, "role": "conductor", "note": "smaller"}); status != 200 || out["moved"] != true {
		t.Fatalf("reset: %d %v", status, out)
	}
	w.until("the Reset to move the parked conductor", func() bool {
		return w.count(`SELECT count(*) FROM machine_moves WHERE run_id = $1 AND kind = 'reset' AND settled_at IS NOT NULL AND refused IS NULL`, conductor) == 1
	})
	if got := w.machineOf(conductor); got["name"] != "Small" {
		t.Errorf("machine=%v", got)
	}
	if in := w.lux.ResumeInputs(live)[resumesBefore:]; len(in) != 1 || in[0] != "Moved back to Small by Resetter: smaller" {
		t.Errorf("resume inputs %q, want the Reset's words alone", in)
	}
	if n := w.count(`SELECT count(*) FROM run_resumes rr JOIN machine_moves m ON m.run_id = rr.run_id
		WHERE rr.run_id = $1 AND rr.cause = 'machine' AND rr.woken_at = m.created_at`, conductor); n != 1 {
		t.Errorf("%d resumes caused and timed by the move", n)
	}
	w.until("the conductor to park on its peer again", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND dude_pause = 'person' AND waiting_since IS NOT NULL`, conductor) == 1
	})
	if n := w.count(`SELECT count(*) FROM peer_waits WHERE run_id = $1 AND closed_at IS NULL`, conductor); n != 1 {
		t.Errorf("%d open waits after the move, want 1", n)
	}
	resumed := w.luxCalls(live, "resume")
	for range 5 {
		w.pump()
	}
	if n := w.luxCalls(live, "resume"); n != resumed {
		t.Fatalf("resumed %d more times before the reply", n-resumed)
	}
}

// lux refuses a Reset's resume of a Run parked only on a peer's reply (the
// target's pool went after the dry run): the refusal is kept and the move
// settled, the agent is told in the refusal's words alone, and it parks on
// the peer again without being resumed for the move again.
func TestALuxRefusedResetOfAPeerParkedRunSettlesAndParksAgain(t *testing.T) {
	w := newWorld(t)
	w.withTools()
	w.syncer.ParkAfter = 300 * time.Millisecond
	mustExec(t, w.owner, `INSERT INTO machine_sizes (id, organization_id, name, cpus, memory_mib, disk_gib) VALUES ('msz_big', $1, $2, 4, 16384, 40)`,
		w.org, fakeagent.MachineSize)
	wi, peer := w.task(), w.task()
	w.onAnEpic(wi, peer)
	ask := `{"to":"` + w.key(peer) + `","text":"Which body does your route take?"}`
	// Raised on lux too, so lux's own record of the Run is on Bigger.
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] != "implement" {
			return fakelux.Behaviour{Hang: true}
		}
		return fakelux.Behaviour{CallTools: [][2]string{{"message", ask}, {"wait_for_reply", `{"message":"{{last.id}}"}`}},
			Machine: fakeagent.MachineRequest, Reply: "Wired the client.", Commit: map[string]string{"a.md": "x\n"}, Message: "work"}
	}
	w.deliver(wi)
	var runID string
	w.until("the agent raised and parked on its peer", func() bool {
		_ = w.owner.QueryRow(context.Background(), `SELECT w.run_id FROM peer_waits w JOIN runs r ON r.id = w.run_id
			WHERE r.task_id = $1 AND w.closed_at IS NULL AND r.status = 'paused' AND r.dude_pause = 'person'
			AND EXISTS (SELECT 1 FROM machine_moves m WHERE m.run_id = r.id AND m.kind = 'raise' AND m.settled_at IS NOT NULL AND m.refused IS NULL)`, wi).Scan(&runID)
		return runID != ""
	})
	if got := w.machineOf(runID); got["name"] != fakeagent.MachineSize {
		t.Fatalf("raised to %v", got)
	}
	live := w.str(`SELECT lux_run_id FROM runs WHERE id = $1`, runID)
	before := len(w.lux.ResumeInputs(live))
	mustExec(t, w.owner, `UPDATE machine_sizes SET pool_id=$2 WHERE organization_id=$1 AND name='Standard'`, w.org, bigPool)
	// The pool goes after the Reset's dry run, as the real resume arrives.
	luxAPI := w.lux.Handler()
	w.lux.OnResume = func(lr string, validateOnly bool) {
		if lr == live && !validateOnly {
			req := httptest.NewRequest("DELETE", "/v1/pools/big", nil)
			req.Header.Set("Authorization", "Bearer lux-key")
			luxAPI.ServeHTTP(httptest.NewRecorder(), req)
		}
	}
	if status, out := w.callAs(w.person("Resetter"), "/internal/agent-machines/reset", map[string]any{"taskId": wi, "role": "implementer"}); status != 200 || out["moved"] != true {
		t.Fatalf("reset: %d %v", status, out)
	}
	w.until("the Reset settled refused", func() bool {
		return w.count(`SELECT count(*) FROM machine_moves WHERE run_id = $1 AND kind = 'reset' AND settled_at IS NOT NULL AND refused = 'unknown_pool'`, runID) == 1
	})
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'machine.refused'`, runID); n != 1 {
		t.Errorf("%d machine.refused, want 1", n)
	}
	want := "You were to move back to Standard; lux kept you on Bigger: Standard's pool has no host it fits."
	if in := w.lux.ResumeInputs(live)[before:]; len(in) != 1 || in[0] != want {
		t.Errorf("resume inputs %q, want %q", in, want)
	}
	w.until("the agent to park on its peer again", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND dude_pause = 'person' AND waiting_since IS NOT NULL`, runID) == 1
	})
	if n := w.count(`SELECT count(*) FROM peer_waits WHERE run_id = $1 AND closed_at IS NULL`, runID); n != 1 {
		t.Errorf("%d open waits after the refusal, want 1", n)
	}
	resumed := w.luxCalls(live, "resume")
	for range 5 {
		w.pump()
	}
	if n := w.luxCalls(live, "resume"); n != resumed {
		t.Fatalf("resumed %d more times after the refusal", n-resumed)
	}
	if in := w.lux.ResumeInputs(live)[before:]; len(in) != 1 {
		t.Errorf("resume inputs %q, want the refusal's alone", in)
	}
}

// A Run parked on a person's question is not resumed for a Reset: the move
// waits for the answer, and applies at the resume the answer starts.
func TestAResetWaitsForAPersonsAnswer(t *testing.T) {
	w := newWorld(t)
	w.withTools()
	w.syncer.ParkAfter = 300 * time.Millisecond
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] != "implement" {
			return fakelux.Behaviour{Hang: true}
		}
		return fakelux.Behaviour{Ask: `{"question":"Should the table be sorted?","choices":["yes","no"]}`, Reply: "Sorted it, as asked.",
			Commit: map[string]string{"a.md": "x\n"}, Message: "work"}
	}
	wi := w.task()
	w.deliver(wi)
	runID := ""
	w.until("the agent to be parked on its question", func() bool {
		runID = w.str(`SELECT COALESCE((SELECT id FROM runs WHERE task_id = $1 AND phase = 'implement' AND status = 'paused' AND dude_pause = 'person'), '')`, wi)
		return runID != ""
	})
	w.raised(wi, runID)
	live := w.str(`SELECT lux_run_id FROM runs WHERE id = $1`, runID)
	if status, out := w.callAs(w.person("Resetter"), "/internal/agent-machines/reset", map[string]any{"taskId": wi, "role": "implementer", "note": "smaller"}); status != 200 {
		t.Fatalf("reset: %d %v", status, out)
	}
	for range 5 {
		w.pump()
		time.Sleep(50 * time.Millisecond)
	}
	if n := w.luxCalls(live, "resume"); n != 0 {
		t.Fatalf("resumed %d times with the person's question open", n)
	}
	if n := w.count(`SELECT count(*) FROM machine_moves WHERE run_id = $1 AND settled_at IS NULL`, runID); n != 1 {
		t.Fatalf("%d pending moves, want the Reset's", n)
	}
	qid := w.str(`SELECT id FROM questions WHERE run_id = $1`, runID)
	if status, body := w.call("/internal/questions/"+qid+"/answer", map[string]any{"text": "yes"}); status != 200 {
		t.Fatalf("answer: %d %v", status, body)
	}
	w.until("the answer to resume it and the move to settle", func() bool {
		return w.count(`SELECT count(*) FROM machine_moves WHERE run_id = $1 AND settled_at IS NOT NULL AND refused IS NULL`, runID) == 1
	})
	if got := w.machineOf(runID); got["name"] != "Standard" {
		t.Errorf("machine=%v", got)
	}
	if in := w.lux.ResumeInputs(live); len(in) != 1 || in[0] != "Moved back to Standard by Resetter: smaller" {
		t.Errorf("resume inputs %q, want the Reset's words", in)
	}
	w.until("the implementer to finish on the answer", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'completed'`, runID) == 1
	})
	for _, lr := range w.lux.Runs() {
		if lr.ID == live && !slices.ContainsFunc(lr.Inputs, func(s string) bool {
			return strings.Contains(s, "Should the table be sorted?") && strings.HasSuffix(s, "yes")
		}) {
			t.Errorf("the agent was given %q, want the answer", lr.Inputs)
		}
	}
}

func TestMutualPeerWaitsResumeToReadQueuedQuestions(t *testing.T) {
	w := newWorld(t)
	w.withTools()
	w.syncer.ParkAfter = 300 * time.Millisecond
	a, b := w.task(), w.task()
	w.onAnEpic(a, b)
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		labels, _ := spec["labels"].(map[string]any)
		if labels["dude.phase"] != "implement" {
			return fakelux.Behaviour{Hang: true}
		}
		return fakelux.Behaviour{Hang: true, Reply: "Still waiting."}
	}
	w.deliver(a)
	w.deliver(b)
	var ar, br string
	w.until("both implementers to run", func() bool {
		_ = w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE task_id = $1 AND phase = 'implement'`, a).Scan(&ar)
		_ = w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE task_id = $1 AND phase = 'implement'`, b).Scan(&br)
		return ar != "" && br != "" && len(w.lux.Runs()) >= 2
	})
	w.pausePhase(ar)
	w.pausePhase(br)
	w.until("both personally paused", func() bool { return w.personallyPaused(ar) && w.personallyPaused(br) })
	ctx := context.Background()
	for _, pair := range [][2]string{{ar, b}, {br, a}} {
		fromTask := a
		if pair[0] == br {
			fromTask = b
		}
		ref := delivery.RunRef{Org: w.org, ProjectID: w.project, TaskID: fromTask, RunID: pair[0]}
		if err := w.app.InOrg(ctx, w.org, func(tx pgx.Tx) error {
			peer, _, err := delivery.PeerByKey(ctx, tx, fromTask, w.key(pair[1]))
			if err != nil {
				return err
			}
			q, err := delivery.SendMessageTx(ctx, tx, ref, delivery.MessageInput{To: peer, Text: "Question from " + fromTask})
			if err != nil {
				return err
			}
			_, err = delivery.OpenWaitTx(ctx, tx, ref, q.ID, "wait")
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	var parkedAt time.Time
	if err := w.owner.QueryRow(ctx, `SELECT now()`).Scan(&parkedAt); err != nil {
		t.Fatal(err)
	}
	mustExec(t, w.owner, `INSERT INTO events (id, organization_id, run_id, event_type, actor_type, actor_id, source, payload)
		SELECT 'evt_park_' || id, organization_id, id, 'run.parked', 'system', 'test', 'orchestrator', jsonb_build_object('parkedAt', $2::timestamptz)
		FROM runs WHERE id = ANY($1)`, []string{ar, br}, parkedAt)
	mustExec(t, w.owner, `UPDATE runs SET dude_pause = 'person', waiting_since = now(), turn_done_at = NULL
		WHERE id = ANY($1)`, []string{ar, br})
	for _, id := range []string{ar, br} {
		if err := w.app.InOrg(ctx, w.org, func(tx pgx.Tx) error {
			_, _, err := delivery.QueueDirective(ctx, tx, delivery.RunRef{Org: w.org, RunID: id}, delivery.Directive{Text: "Read your peer's question.", Scope: "run"})
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	w.until("both questions to be read", func() bool {
		return w.count(`SELECT count(*) FROM peer_messages WHERE read_at IS NOT NULL`) == 2
	})
	w.until("both Runs to wait again after reading", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = ANY($1) AND (waiting_since IS NOT NULL OR status = 'paused')`, []string{ar, br}) == 2
	})
	if n := w.count(`SELECT count(*) FROM directives WHERE run_id = ANY($1) AND sent_at IS NULL`, []string{ar, br}); n != 0 {
		t.Errorf("%d unsent questions", n)
	}
	if n := w.count(`SELECT count(*) FROM peer_waits WHERE closed_at IS NULL`); n != 2 {
		t.Errorf("%d open waits, want two", n)
	}
	if n := w.count(`SELECT count(*) FROM run_resumes WHERE run_id = ANY($1) AND woken_at >= $2`, []string{ar, br}, parkedAt); n != 2 {
		t.Errorf("%d timed resumes", n)
	}
}

// Two scripted agents on one epic, through the tool server and the fake
// lux alone: A asks B and waits; B reads the question at its next step and
// replies to it ({{inbox.last}}); A resumes with the reply and completes.
func TestTwoScriptedPeersAskAndAnswer(t *testing.T) {
	w := newWorld(t)
	w.withTools()
	w.syncer.ParkAfter = 300 * time.Millisecond
	w.onModel("implementer", fakeagent.PeerModel)
	a, b := w.task(), w.task()
	w.onAnEpic(a, b)
	w.deliver(b)
	w.until("B's implementer to run", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'implement' AND status = 'running'
			AND agent_busy_at IS NOT NULL`, b) == 1
	})
	w.deliver(a)
	w.until("A's implementer to complete", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'implement' AND status = 'completed'`, a) == 1
	})
	var question, answer string
	if err := w.owner.QueryRow(context.Background(), `SELECT q.id, r.text FROM peer_messages q JOIN peer_messages r ON r.reply_to = q.id
		WHERE q.from_task_id = $1 AND q.to_task_id = $2`, a, b).Scan(&question, &answer); err != nil {
		t.Fatal(err)
	}
	if answer != fakeagent.PeerAnswer {
		t.Errorf("B answered %q", answer)
	}
	var ar string
	if err := w.owner.QueryRow(context.Background(), `SELECT lux_run_id FROM runs WHERE task_id = $1 AND phase = 'implement'`, a).Scan(&ar); err != nil {
		t.Fatal(err)
	}
	read := false
	for _, lr := range w.lux.Runs() {
		if lr.ID == ar {
			read = len(lr.Inputs) > 0 && strings.Contains(lr.Inputs[0], fakeagent.PeerAnswer)
		}
	}
	if !read {
		t.Error("A's lux input lacks the peer answer")
	}
	// A reply that commits before A's wait_for_reply is returned at once,
	// with no wait recorded; any wait there is must be A's own, closed by it.
	var waits []string
	if err := w.owner.QueryRow(context.Background(), `SELECT COALESCE(array_agg(how || '/' || COALESCE(closed_by, 'open')), '{}')
		FROM peer_waits WHERE message_id = $1`, question).Scan(&waits); err != nil {
		t.Fatal(err)
	}
	if closed := w.count(`SELECT count(*) FROM peer_waits WHERE message_id = $1
		AND how = 'wait' AND closed_by = 'reply' AND closed_at IS NOT NULL`, question); len(waits) > 1 || closed != len(waits) {
		t.Errorf("waits on %s: %v; want none, or one explicit wait closed by the reply", question, waits)
	}
	if n := w.count(`SELECT count(*) FROM peer_waits WHERE closed_at IS NULL`); n != 0 {
		t.Errorf("%d waits left open", n)
	}
}

// A turn that ends with a question to a peer unanswered waits on it, as if
// the agent had run `dude wait`: the Run does not complete, and the
// delivery does not move to review.
func TestATurnEndingWithAnUnansweredQuestionWaits(t *testing.T) {
	w := newWorld(t)
	w.withTools()
	wi, peer := w.task(), w.task()
	w.onAnEpic(wi, peer)
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] != "implement" {
			return fakelux.Behaviour{Hang: true}
		}
		return fakelux.Behaviour{CallTools: [][2]string{{"message", `{"to":"` + w.key(peer) + `","text":"Which path?"}`}},
			Reply: "Done, apart from the path.", Commit: map[string]string{"a.md": "x\n"}, Message: "work"}
	}
	w.deliver(wi)
	w.until("the turn to end waiting", func() bool {
		return w.count(`SELECT count(*) FROM peer_waits w JOIN runs r ON r.id = w.run_id WHERE r.task_id = $1
			AND w.how = 'turn_ended' AND w.closed_at IS NULL AND r.waiting_since IS NOT NULL`, wi) == 1
	})
	for range 5 {
		w.pump()
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'implement' AND status = 'completed'`, wi); n != 0 {
		t.Error("the Run completed with its question unanswered")
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review'`, wi); n != 0 {
		t.Error("the delivery moved to review")
	}
	if n := w.count(`SELECT count(*) FROM events e JOIN runs r ON r.id = e.run_id WHERE r.task_id = $1
		AND e.event_type = 'agent.peer.waiting' AND e.payload->>'how' = 'turn_ended'`, wi); n != 1 {
		t.Errorf("%d turn_ended waiting events", n)
	}
}

// A self-reply closes the question: the turn ends, the Run completes.
func TestASelfReplyLetsTheTurnComplete(t *testing.T) {
	w := newWorld(t)
	w.withTools()
	wi, peer := w.task(), w.task()
	w.onAnEpic(wi, peer)
	to := w.key(peer)
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] != "implement" {
			return fakelux.Behaviour{Hang: true}
		}
		return fakelux.Behaviour{CallTools: [][2]string{
			{"message", `{"to":"` + to + `","text":"Which path?"}`},
			{"message", `{"to":"` + to + `","text":"Not waiting on this: going ahead assuming /v1/x.","replyTo":"{{last.id}}"}`},
		}, Reply: "Done, on my assumption.", Commit: map[string]string{"a.md": "x\n"}, Message: "work"}
	}
	w.deliver(wi)
	w.until("the implementer to complete", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'implement' AND status = 'completed'`, wi) == 1
	})
	if n := w.count(`SELECT count(*) FROM peer_waits w JOIN runs r ON r.id = w.run_id WHERE r.task_id = $1`, wi); n != 0 {
		t.Errorf("%d waits opened", n)
	}
	if n := w.count(`SELECT count(*) FROM peer_messages m JOIN runs r ON r.id = m.from_run_id WHERE r.task_id = $1`, wi); n != 2 {
		t.Errorf("%d messages sent, want the question and its self-reply", n)
	}
}

// A task that ends without replying releases the agent waiting on it: the
// parked sender is resumed with the line saying so, and finishes.
func TestAPeerEndingWithoutReplyingResumesTheParkedSender(t *testing.T) {
	w := newWorld(t)
	w.withTools()
	w.syncer.ParkAfter = 300 * time.Millisecond
	wi, peer := w.task(), w.task()
	w.onAnEpic(wi, peer)
	ask := `{"to":"` + w.key(peer) + `","text":"Which body does your route take?"}`
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] != "implement" {
			return fakelux.Behaviour{Hang: true}
		}
		return fakelux.Behaviour{CallTools: [][2]string{{"message", ask}, {"wait_for_reply", `{"message":"{{last.id}}"}`}},
			Reply: "Went ahead on my assumption.", Commit: map[string]string{"a.md": "x\n"}, Message: "work"}
	}
	w.deliver(wi)
	var runID string
	w.until("the agent to wait on its peer and be parked", func() bool {
		_ = w.owner.QueryRow(context.Background(), `SELECT w.run_id FROM peer_waits w JOIN runs r ON r.id = w.run_id
			WHERE r.task_id = $1 AND w.closed_at IS NULL AND r.status = 'paused' AND r.dude_pause = 'person'`, wi).Scan(&runID)
		return runID != ""
	})
	ctx := context.Background()
	if err := w.app.InOrg(ctx, w.org, func(tx pgx.Tx) error {
		_, err := delivery.SetTaskStatusTx(ctx, tx, w.org, w.project, peer, "", "aborted", "a person stopped it")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	w.until("the sender to finish", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'completed'`, runID) == 1
	})
	want := w.key(peer) + " ended without replying (status: aborted). Carry on with your assumption, or ask a person."
	if lr := w.lux.Runs()[0]; lr.Resumed != 1 || len(lr.Inputs) != 1 || lr.Inputs[0] != want {
		t.Errorf("resumed %d times with %q, want %q", lr.Resumed, lr.Inputs, want)
	}
	if n := w.count(`SELECT count(*) FROM peer_waits WHERE run_id = $1 AND closed_by = 'peer_ended'`, runID); n != 1 {
		t.Errorf("the wait was not closed by the peer's end")
	}
}

// A message to a task whose Run is not going is in its next Run's prompt:
// the implementer that delivery starts is briefed with it (its text is
// EpicBriefing's, delivery's TestAHeldMessageIsInTheTasksNextBriefing).
func TestAHeldMessageIsInTheNextRunsPrompt(t *testing.T) {
	w := newWorld(t)
	wi, peer := w.task(), w.task()
	w.onAnEpic(wi, peer)
	sent := w.send(w.peerRun(peer), wi, "", "Use the shared client, please.")
	if sent.Lands != delivery.LandsNextRun {
		t.Fatalf("lands %s", sent.Lands)
	}
	w.deliver(wi)
	w.until("the implementer to start", func() bool { return len(w.lux.Runs()) > 0 })
	// The scripted agent's workload prompt is its script: what its prompt
	// was briefed with is recorded on the message, and in the ledger.
	var runID string
	if err := w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE task_id = $1 AND phase = 'implement'`, wi).Scan(&runID); err != nil {
		t.Fatal(err)
	}
	if n := w.count(`SELECT count(*) FROM peer_messages WHERE id = $1 AND briefed_run_id = $2`, sent.ID, runID); n != 1 {
		t.Errorf("the message was not briefed to the implementer")
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'agent.message.received'
		AND payload->>'message' = $2 AND payload->>'via' = 'briefing'`, runID, sent.ID); n != 1 {
		t.Errorf("%d received events on the implementer", n)
	}
}
