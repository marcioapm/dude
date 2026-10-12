package orchestrator_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
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

// reply answers a message as task's agent would, with message's tool.
func (w *world) reply(from delivery.RunRef, toTask, message, text string) {
	w.t.Helper()
	ctx := context.Background()
	if err := w.app.InOrg(ctx, w.org, func(tx pgx.Tx) error {
		peer, ok, err := delivery.PeerByKey(ctx, tx, from.TaskID, w.key(toTask))
		if err != nil || !ok {
			w.t.Fatalf("%s is not a peer: %v", toTask, err)
		}
		_, err = delivery.SendMessageTx(ctx, tx, from, delivery.MessageInput{To: peer, Text: text, ReplyTo: message})
		return err
	}); err != nil {
		w.t.Fatal(err)
	}
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
	w.reply(w.peerRun(peer), wi, message, "{metric, from, period}")
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
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'agent.message.received'`, runID); n != 1 {
		t.Errorf("%d received events on the waiting Run", n)
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
	from := w.peerRun(peer)
	ctx := context.Background()
	var sent delivery.Sent
	if err := w.app.InOrg(ctx, w.org, func(tx pgx.Tx) error {
		p, _, err := delivery.PeerByKey(ctx, tx, peer, w.key(wi))
		if err != nil {
			return err
		}
		sent, err = delivery.SendMessageTx(ctx, tx, from, delivery.MessageInput{To: p, Text: "Use the shared client, please."})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if sent.Lands != delivery.LandsNextRun {
		t.Fatalf("lands %s", sent.Lands)
	}
	w.deliver(wi)
	w.until("the implementer to start", func() bool { return len(w.lux.Runs()) > 0 })
	// The scripted agent's workload prompt is its script: what its prompt
	// was briefed with is recorded on the message, and in the ledger.
	var runID string
	if err := w.owner.QueryRow(ctx, `SELECT id FROM runs WHERE task_id = $1 AND phase = 'implement'`, wi).Scan(&runID); err != nil {
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
