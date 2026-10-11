package delivery_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// replayWorld is an organisation with a task (its conductors, and a phase
// Run of theirs) and a session (its brainstorms), for Replay to read.
type replayWorld struct {
	t     *testing.T
	owner *pgx.Conn
	org   string
	n     int
	app   interface {
		InOrg(context.Context, string, func(pgx.Tx) error) error
	}
}

func newReplayWorld(t *testing.T) *replayWorld {
	app, owner := dbtest.Open(t)
	w := &replayWorld{t: t, owner: owner, org: dbtest.Org(t, owner), app: app}
	exec(t, owner, `INSERT INTO people (id, organization_id, name) VALUES ('per_ana', $1, 'Ana'), ('per_bo', $1, 'Bo')`, w.org)
	exec(t, owner, `INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_r', $1, 'P', 'p', 'RP')`, w.org)
	exec(t, owner, `INSERT INTO tasks (id, organization_id, project_id, number, title, goal) VALUES ('wi_r', $1, 'prj_r', 1, 'T', 'A goal.')`, w.org)
	exec(t, owner, `INSERT INTO sessions (id, organization_id, title) VALUES ('ses_r', $1, 'S')`, w.org)
	return w
}

// run adds a Run: role conductor or implementer on the task, brainstorm
// on the session.
func (w *replayWorld) run(id, role string) {
	if role == "brainstorm" {
		exec(w.t, w.owner, `INSERT INTO runs (id, organization_id, session_id, attempt, status, kind, role, base_refs)
			VALUES ($1, $2, 'ses_r', 1, 'completed', 'agent', 'brainstorm', '{}')`, id, w.org)
		return
	}
	phase := "implement"
	if role == "conductor" {
		phase = ""
	}
	exec(w.t, w.owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, kind, role, phase, base_refs)
		VALUES ($1, $2, 'prj_r', 'wi_r', 1, 'completed', 'agent', $3::agent_role, NULLIF($4, '')::run_phase, '{}')`, id, w.org, role, phase)
}

// event appends an event on run, by actor (a person's id, or "" for the
// agent). A session's Run is named run_s…; run "-" is the session itself.
func (w *replayWorld) event(typ, run, actor, payload string) {
	w.n++
	task, session := "wi_r", ""
	if run == "-" || strings.HasPrefix(run, "run_s") {
		task, session = "", "ses_r"
	}
	if run == "-" {
		run = ""
	}
	actorType, actorID := "agent", run
	if actor != "" {
		actorType, actorID = "human", actor
	}
	exec(w.t, w.owner, `INSERT INTO events (id, organization_id, event_type, run_id, task_id, session_id, actor_type, actor_id, source, payload)
		VALUES ($1, $2, $3, NULLIF($4, ''), NULLIF($5, ''), NULLIF($6, ''), $7, $8, 'test', $9::jsonb)`,
		fmt.Sprintf("ev_r%03d", w.n), w.org, typ, run, task, session, actorType, actorID, payload)
}

func (w *replayWorld) replay(of delivery.Talker) string {
	w.t.Helper()
	var out string
	if err := w.app.InOrg(context.Background(), w.org, func(tx pgx.Tx) (err error) {
		out, err = delivery.Replay(context.Background(), tx, of)
		return err
	}); err != nil {
		w.t.Fatal(err)
	}
	return out
}

// A conductor's replay is the task's Chat across its conductors, in order:
// never a phase Run's tool calls or words, nor a session's.
func TestAConductorsReplayIsItsChatAcrossConductors(t *testing.T) {
	w := newReplayWorld(t)
	if got := w.replay(delivery.Talker{TaskID: "wi_r"}); got != "" {
		t.Errorf("a task that never had a conductor replays %q", got)
	}
	w.run("run_c1", "conductor")
	w.run("run_impl", "implementer")
	w.run("run_c2", "conductor")
	w.event("chat.message", "run_c1", "per_ana", `{"text":"first question"}`)
	w.event("agent.tool.called", "run_c1", "", `{"tool":"findings","callId":"k1","input":{}}`)
	w.event("agent.tool.called", "run_impl", "", `{"tool":"PHASE_TOOL","callId":"k1","input":{}}`)
	w.event("agent.message", "run_impl", "", `{"text":"PHASE WORDS"}`)
	w.event("agent.tool.completed", "run_c1", "", `{"tool":"findings","callId":"k1","output":{"head":"none"}}`)
	w.event("agent.message", "run_c1", "", `{"text":"first answer"}`)
	w.event("chat.message", "run_c2", "per_bo", `{"text":"second question"}`)
	w.event("agent.message", "run_c2", "", `{"text":"second answer"}`)
	got := w.replay(delivery.Talker{TaskID: "wi_r"})
	for _, leak := range []string{"PHASE_TOOL", "PHASE WORDS"} {
		if strings.Contains(got, leak) {
			t.Errorf("a phase Run's %s in the conductor's replay:\n%s", leak, got)
		}
	}
	want := "Ana: first question\n\nfindings({}) → none\n\nYou: first answer\n\n[A new agent took over here.]\n\nBo: second question\n\nYou: second answer"
	if !strings.HasPrefix(got, "## The conversation so far\n\n") || !strings.HasSuffix(got, want) {
		t.Errorf("replay:\n%s\n\nwant it to end:\n%s", got, want)
	}
}

// A session's replay is every event of the session and its brainstorms,
// across its Runs; a message a Run never read is not in it.
func TestASessionsReplayCoversItsRuns(t *testing.T) {
	w := newReplayWorld(t)
	// Renamed before its first message: no Run yet, so nothing to replay.
	w.event("session.renamed", "-", "per_bo", `{"title":"Early","by":"per_bo"}`)
	if got := w.replay(delivery.Talker{SessionID: "ses_r"}); got != "" {
		t.Errorf("a session that never had a Run replays %q", got)
	}
	w.run("run_s1", "brainstorm")
	w.run("run_s2", "brainstorm")
	w.event("chat.message", "run_s1", "per_ana", `{"text":"hello"}`)
	w.event("agent.message", "run_s1", "", `{"text":"hi Ana"}`)
	exec(t, w.owner, `INSERT INTO directives (id, organization_id, run_id, text, failed_at) VALUES ('dir_lost', $1, 'run_s1', 'x', now())`, w.org)
	w.event("chat.message", "run_s1", "per_bo", `{"text":"UNREAD","directiveId":"dir_lost"}`)
	exec(t, w.owner, `INSERT INTO directives (id, organization_id, run_id, text, delivered_at) VALUES ('dir_read', $1, 'run_s2', 'x', now())`, w.org)
	w.event("chat.message", "run_s2", "per_bo", `{"text":"UNREAD","directiveId":"dir_read"}`)
	w.event("session.renamed", "-", "per_bo", `{"title":"Named","by":"per_bo"}`)
	got := w.replay(delivery.Talker{SessionID: "ses_r"})
	want := "Ana: hello\n\nYou: hi Ana\n\n[A new agent took over here.]\n\nBo: UNREAD\n\nBo named the session \"Named\"."
	if !strings.HasSuffix(got, want) || strings.Count(got, "UNREAD") != 1 {
		t.Errorf("replay:\n%s\n\nwant it to end:\n%s", got, want)
	}
}

// A person's Retry of a steer that failed is the words' only delivery: it
// is replayed. A resend of a steer the agent heard repeats it: it is not.
func TestARetriedSteerIsReplayedOnce(t *testing.T) {
	w := newReplayWorld(t)
	w.run("run_c1", "conductor")
	exec(t, w.owner, `INSERT INTO directives (id, organization_id, run_id, text, failed_at) VALUES ('dir_f', $1, 'run_c1', 'x', now())`, w.org)
	exec(t, w.owner, `INSERT INTO directives (id, organization_id, run_id, text, delivered_at) VALUES ('dir_r', $1, 'run_c1', 'x', now()),
		('dir_h', $1, 'run_c1', 'x', now()), ('dir_h2', $1, 'run_c1', 'x', now())`, w.org)
	w.event("run.steered", "run_c1", "per_ana", `{"text":"RETRIED","directiveId":"dir_f"}`)
	w.event("run.steered", "run_c1", "per_ana", `{"text":"RETRIED","directiveId":"dir_r","supersedes":"dir_f"}`)
	w.event("run.steered", "run_c1", "per_bo", `{"text":"HEARD","directiveId":"dir_h"}`)
	w.event("run.steered", "run_c1", "per_bo", `{"text":"HEARD","directiveId":"dir_h2","supersedes":"dir_h","interrupt":true}`)
	got := w.replay(delivery.Talker{TaskID: "wi_r"})
	if !strings.HasSuffix(got, "Ana: RETRIED\n\nBo: HEARD") || strings.Count(got, "RETRIED") != 1 || strings.Count(got, "HEARD") != 1 {
		t.Errorf("replay:\n%s", got)
	}
}

// A steer that failed before its "Interrupt now" was first sent: the
// interrupt carried the words (resends the steer), so the agent heard them
// once, and the replay says them once.
func TestAnInterruptThatCarriedAFailedSteersWordsIsReplayedOnce(t *testing.T) {
	w := newReplayWorld(t)
	w.run("run_c1", "conductor")
	exec(t, w.owner, `INSERT INTO directives (id, organization_id, run_id, text, failed_at) VALUES ('dir_s', $1, 'run_c1', 'x', now())`, w.org)
	exec(t, w.owner, `INSERT INTO directives (id, organization_id, run_id, text, supersedes, interrupt, resends, interrupt_only, delivered_at)
		VALUES ('dir_i', $1, 'run_c1', 'x', 'dir_s', true, 'dir_s', false, now())`, w.org)
	w.event("run.steered", "run_c1", "per_ana", `{"text":"MIGRATION","directiveId":"dir_s"}`)
	w.event("run.steered", "run_c1", "per_ana", `{"text":"MIGRATION","directiveId":"dir_i","supersedes":"dir_s","interrupt":true}`)
	got := w.replay(delivery.Talker{TaskID: "wi_r"})
	if !strings.HasSuffix(got, "Ana: MIGRATION") || strings.Count(got, "MIGRATION") != 1 {
		t.Errorf("replay:\n%s", got)
	}
}

// A replay read from the ledger starts at the newest compaction summary:
// the summary under its heading, then only what came after it.
func TestTheReplayFromTheLedgerStartsAtItsCompactionSummary(t *testing.T) {
	w := newReplayWorld(t)
	w.run("run_s1", "brainstorm")
	w.event("chat.message", "run_s1", "per_ana", `{"text":"EARLIER"}`)
	w.event("agent.context.compacted", "run_s1", "", `{"trigger":"auto","summary":"SUM"}`)
	w.event("chat.message", "run_s1", "per_bo", `{"text":"LATER"}`)
	got := w.replay(delivery.Talker{SessionID: "ses_r"})
	if !strings.HasSuffix(got, "## Earlier, as the agent summarised it\n\nSUM\n\n## Since then\n\nBo: LATER") || strings.Contains(got, "EARLIER") {
		t.Errorf("replay:\n%s", got)
	}
	// The ledger is read from the summary on, not whole.
	var types []string
	if err := w.app.InOrg(context.Background(), w.org, func(tx pgx.Tx) (err error) {
		types, err = delivery.ReplayEventTypes(context.Background(), tx, delivery.Talker{SessionID: "ses_r"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(types, " ") != "agent.context.compacted chat.message" {
		t.Errorf("read %v, want the summary and the message after it", types)
	}
	// Another Run's agent after the summary's: it took over.
	w.run("run_s2", "brainstorm")
	w.event("agent.message", "run_s2", "", `{"text":"NEXT"}`)
	if got := w.replay(delivery.Talker{SessionID: "ses_r"}); !strings.HasSuffix(got, "## Since then\n\nBo: LATER\n\n[A new agent took over here.]\n\nYou: NEXT") {
		t.Errorf("replay after another Run:\n%s", got)
	}
}

// A summary by one Run's agent and only another Run's events after it: a
// take-over line between them.
func TestTheReplayAfterAnotherRunsSummarySaysANewAgentTookOver(t *testing.T) {
	w := newReplayWorld(t)
	w.run("run_s1", "brainstorm")
	w.run("run_s2", "brainstorm")
	w.event("agent.context.compacted", "run_s1", "", `{"trigger":"auto","summary":"SUM"}`)
	w.event("chat.message", "run_s2", "per_bo", `{"text":"LATER"}`)
	if got := w.replay(delivery.Talker{SessionID: "ses_r"}); !strings.HasSuffix(got, "SUM\n\n## Since then\n\n[A new agent took over here.]\n\nBo: LATER") {
		t.Errorf("replay:\n%s", got)
	}
}
