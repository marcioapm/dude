package orchestrator_test

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/fakeagent"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// setHarness names the harness a role of the world's project runs on.
func (w *world) setHarness(role, harness string) {
	w.t.Helper()
	mustExec(w.t, w.owner, `UPDATE projects SET agent_models = jsonb_set(agent_models, ARRAY[$2::text],
		COALESCE(agent_models->$2, '{}'::jsonb) || jsonb_build_object('harness', $3::text)) WHERE id = $1`, w.project, role, harness)
}

// A scripted delivery with every role on Claude Code, then on Codex: each
// phase goes to lux on that harness's adapter, the fake lux speaks its
// protocol, and the delivery goes through its phases as on OpenCode. The
// implementer's chat has its thought, its message, its tool calls, its
// plan, dude's own tool called through the agent's container, and its
// turn's tokens.
func TestAScriptedDeliveryOnEachHarness(t *testing.T) {
	for _, harness := range []string{"claude-code", "codex"} {
		t.Run(harness, func(t *testing.T) {
			w := newWorld(t)
			w.withTools()
			for _, role := range []string{"implementer", "reviewer", "simplifier"} {
				w.onModel(role, "fake/scripted")
				w.setHarness(role, harness)
			}
			w.onModel("implementer", fakeagent.ToolsModel)
			wi := w.task()
			w.deliver(wi)
			w.until("the loop to converge", func() bool {
				return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'simplify' AND status = 'completed'`, wi) == 1
			})
			var phases []string
			rows, _ := w.owner.Query(context.Background(), `SELECT phase::text || ':' || status::text || ':' || harness FROM runs WHERE task_id = $1 ORDER BY created_at`, wi)
			for rows.Next() {
				var p string
				_ = rows.Scan(&p)
				phases = append(phases, p)
			}
			rows.Close()
			want := []string{"implement:completed:" + harness, "review:completed:" + harness, "fix:completed:" + harness,
				"review:completed:" + harness, "simplify:completed:" + harness}
			if strings.Join(phases, " ") != strings.Join(want, " ") {
				t.Errorf("phases = %v, want %v", phases, want)
			}
			for _, r := range w.lux.Runs() {
				if !strings.Contains(string(r.Spec), `"adapter":"`+harness+`"`) {
					t.Errorf("%s went to lux on another adapter: %s", r.ID, r.Spec)
				}
			}
			var runID string
			_ = w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE task_id = $1 AND phase = 'implement'`, wi).Scan(&runID)
			for typ, want := range map[string]string{
				"agent.thought": fakeagent.ToolsThought, "agent.message": "Implemented it.",
			} {
				var text string
				_ = w.owner.QueryRow(context.Background(), `SELECT string_agg(payload->>'text', '|') FROM events WHERE run_id = $1 AND event_type = $2`,
					runID, typ).Scan(&text)
				if text != want {
					t.Errorf("%s = %q, want %q", typ, text, want)
				}
			}
			if n := w.count(`SELECT count(*) FROM events c JOIN events d ON d.run_id = c.run_id AND d.payload->>'callId' = c.payload->>'callId'
				AND d.event_type = 'agent.tool.completed'
				WHERE c.run_id = $1 AND c.event_type = 'agent.tool.called' AND c.payload->>'tool' = 'read'`, runID); n != 1 {
				t.Errorf("%d read calls with their completion, want 1", n)
			}
			if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'agent.plan.updated'`, runID); n != 1 {
				t.Errorf("%d plans, want 1", n)
			}
			if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'agent.custom.progress'`, runID); n != 2 {
				t.Errorf("%d progress events through dude's tools, want 2", n)
			}
			if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'agent.model.request.completed'
				AND (payload->>'turn')::boolean AND (payload->'tokens'->>'output')::int = 34`, runID); n != 1 {
				t.Errorf("%d turn ends with the turn's tokens, want 1", n)
			}
		})
	}
}

// An agent on Claude Code or Codex that asks a person waits for the
// answer and carries on with it.
func TestAScriptedAgentOnEachHarnessAsksAndCarriesOnWithTheAnswer(t *testing.T) {
	for _, harness := range []string{"claude-code", "codex"} {
		t.Run(harness, func(t *testing.T) {
			w := newWorld(t)
			w.withTools()
			w.setHarness("implementer", harness)
			w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
				if labels, _ := spec["labels"].(map[string]any); labels["dude.phase"] != "implement" {
					return fakelux.Behaviour{Hang: true}
				}
				return fakelux.Behaviour{Ask: `{"question":"Sorted?","choices":["yes","no"]}`, Reply: "Sorted it.",
					Commit: map[string]string{"a.md": "x\n"}, Message: "work"}
			}
			wi := w.task()
			w.deliver(wi)
			w.until("the question to reach a person", func() bool { return w.taskStatus(wi) == "awaiting_input" })
			var runID, qid string
			_ = w.owner.QueryRow(context.Background(), `SELECT run_id, id FROM questions WHERE task_id = $1`, wi).Scan(&runID, &qid)
			w.until("the agent to wait", func() bool {
				return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND waiting_since IS NOT NULL AND turn_done_at IS NULL`, runID) == 1
			})
			if status, body := w.call("/internal/questions/"+qid+"/answer", map[string]any{"text": "yes"}); status != 200 {
				t.Fatalf("answer: %d %v", status, body)
			}
			w.until("the implementer to finish", func() bool {
				return w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'completed'`, runID) == 1
			})
			if in := w.lux.Runs()[0].Inputs; len(in) != 1 || !strings.HasSuffix(in[0], "yes") {
				t.Errorf("the agent was given %v", in)
			}
			var said string
			_ = w.owner.QueryRow(context.Background(), `SELECT string_agg(payload->>'text', '|' ORDER BY cursor) FROM events
				WHERE run_id = $1 AND event_type = 'agent.message'`, runID).Scan(&said)
			if said != "I asked; waiting for the answer.|Sorted it." {
				t.Errorf("messages = %q", said)
			}
		})
	}
}

// A person's steer reaches an agent on Claude Code or Codex while its
// command runs, and is read at its next step, when the command ends.
func TestAScriptedAgentOnEachHarnessIsSteered(t *testing.T) {
	for _, harness := range []string{"claude-code", "codex"} {
		t.Run(harness, func(t *testing.T) {
			w := newWorld(t)
			w.setHarness("implementer", harness)
			w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
				return fakelux.Behaviour{Tools: []string{"bash"}, KeepToolsOpen: true, Hang: true}
			}
			wi := w.task()
			w.deliver(wi)
			w.until("the command to start", func() bool {
				return w.count(`SELECT count(*) FROM events e JOIN runs r ON r.id = e.run_id
					WHERE r.task_id = $1 AND e.event_type = 'agent.tool.called' AND e.payload->>'tool' = 'bash'`, wi) == 1
			})
			var runID string
			_ = w.owner.QueryRow(context.Background(), `SELECT id FROM runs WHERE task_id = $1 AND phase = 'implement'`, wi).Scan(&runID)
			if code, out := w.call("/internal/runs/"+runID+"/steer", map[string]any{"text": "also add a test"}); code != http.StatusCreated {
				t.Fatalf("steer: %d %v", code, out)
			}
			w.until("the harness to take it", func() bool {
				return w.count(`SELECT count(*) FROM directives WHERE run_id = $1 AND accepted_at IS NOT NULL`, runID) == 1
			})
			if n := w.count(`SELECT count(*) FROM directives WHERE run_id = $1 AND delivered_at IS NOT NULL`, runID); n != 0 {
				t.Errorf("read before the command ended")
			}
			w.lux.FinishTools(w.lux.Runs()[0].ID)
			w.until("the steer to be read", func() bool {
				return w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.directive.delivered' AND (payload->>'read')::boolean`, runID) == 1
			})
			if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'agent.tool.completed' AND payload->>'tool' = 'bash'`, runID); n != 1 {
				t.Errorf("%d bash completions, want 1", n)
			}
		})
	}
}

// A role whose harness cannot run its tier's model fails its Run saying
// so, before lux: Claude Code with an OpenAI model, Codex with Claude.
func TestARoleOnAHarnessThatCannotRunItsModelFailsSayingWhy(t *testing.T) {
	for harness, model := range map[string]string{"claude-code": "gpt-6-sol", "codex": "claude-sonnet-5"} {
		t.Run(harness, func(t *testing.T) {
			w := newWorld(t)
			w.onModel("implementer", model)
			w.setHarness("implementer", harness)
			wi := w.task()
			w.deliver(wi)
			w.until("the implementer to fail", func() bool {
				return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'implement' AND status = 'failed'`, wi) == 1
			})
			var reason string
			_ = w.owner.QueryRow(context.Background(), `SELECT error FROM runs WHERE task_id = $1 AND phase = 'implement'`, wi).Scan(&reason)
			if !strings.HasPrefix(reason, "The Implementer runs on ") || !strings.Contains(reason, model) || !strings.Contains(reason, "picks another harness or tier") {
				t.Errorf("reason = %q", reason)
			}
			if n := len(w.lux.Runs()); n != 0 {
				t.Errorf("%d Runs reached lux", n)
			}
		})
	}
}

// A real model on Claude Code goes to lux as Claude Code, and the Run
// records it.
func TestARealModelOnClaudeCodeGoesToLuxAsClaudeCode(t *testing.T) {
	w := newWorld(t)
	w.onModel("implementer", "claude-sonnet-5")
	w.setHarness("implementer", "claude-code")
	w.lux.Decide = hang
	wi := w.task()
	w.deliver(wi)
	w.until("the implementer to reach lux", func() bool { return w.specOf("implement") != nil })
	spec := w.specOf("implement")
	if spec.Workload.Adapter != "claude-code" || spec.Workload.Command[0] != "claude" || spec.Labels["dude.harness"] != "claude-code" {
		t.Errorf("workload = %+v labels = %v", spec.Workload, spec.Labels)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'implement' AND harness = 'claude-code'`, wi); n != 1 {
		t.Errorf("the Run does not record Claude Code")
	}
}

// secretNames are the names among a resume's secrets.
func secretNames(secrets []lux.Secret) []string {
	var out []string
	for _, s := range secrets {
		out = append(out, s.Name)
	}
	return out
}

// A phase Run whose role moved to another harness while it was paused
// resumes on the one it was submitted on: lux resumes Claude Code's
// command, so the resume carries Claude Code's key, not OpenCode's.
func TestAResumedRunKeepsTheHarnessItWasSubmittedOn(t *testing.T) {
	w := newWorld(t)
	w.onModel("implementer", "claude-sonnet-5")
	w.setHarness("implementer", "claude-code")
	w.lux.Decide = hang
	wi := w.task()
	w.deliver(wi)
	w.until("the implementer to be submitted", func() bool { return len(w.lux.Runs()) >= 1 })
	w.setHarness("implementer", "opencode")
	w.pauseAndResume(wi)
	r := w.lux.Runs()[0]
	if len(r.ResumeSecrets) != 1 {
		t.Fatalf("resumes = %d, want 1", len(r.ResumeSecrets))
	}
	if names := secretNames(r.ResumeSecrets[0]); !slices.Contains(names, "ANTHROPIC_API_KEY") || slices.Contains(names, "DUDE_LLM_KEY") {
		t.Errorf("resume secrets = %v, want Claude Code's key and not OpenCode's", names)
	}
}

// So does a session's agent, parked and resumed by a message.
func TestAResumedSessionKeepsTheHarnessItWasSubmittedOn(t *testing.T) {
	s := newSessionWorld(t)
	s.syncer.ConductorWarm = time.Hour
	s.lux.Decide = hang
	mustExec(t, s.owner, `UPDATE model_tiers SET model = 'claude-sonnet-5' WHERE organization_id = $1 AND name = 'Thinker'`, s.org)
	brainstormOn := func(harness string) {
		mustExec(t, s.owner, `UPDATE organizations SET default_agent_models = jsonb_set(default_agent_models, '{brainstorm}',
			COALESCE(default_agent_models->'brainstorm', '{}'::jsonb) || jsonb_build_object('harness', $2::text)) WHERE id = $1`, s.org, harness)
	}
	brainstormOn("claude-code")
	id := s.session()
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "hello"})
	var run string
	s.until("the brainstorm running", func() bool {
		run, _ = s.brainstorm(id)
		return run != "" && s.count(`SELECT count(*) FROM runs WHERE id = $1 AND lux_state = 'running' AND harness = 'claude-code'`, run) == 1
	})
	brainstormOn("opencode")
	// Its turn long done: the sweep parks it.
	mustExec(t, s.owner, `UPDATE runs SET turn_done_at = now() - interval '2 hours', agent_busy_at = now() - interval '3 hours',
		agent_active_at = now() - interval '3 hours', files_changed_at = now() - interval '3 hours',
		stall_reported_at = now() - interval '3 hours' WHERE id = $1`, run)
	s.until("the session parked", func() bool {
		return s.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'paused' AND lux_state = 'stopped'`, run) == 1
	})
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "carry on"})
	s.until("the session resumed", func() bool { v := s.luxRun(run); return v != nil && v.resumed == 1 })
	var secrets []lux.Secret
	for _, r := range s.lux.Runs() {
		if r.Resumed == 1 {
			secrets = r.ResumeSecrets[0]
		}
	}
	if names := secretNames(secrets); !slices.Contains(names, "ANTHROPIC_API_KEY") || slices.Contains(names, "DUDE_LLM_KEY") {
		t.Errorf("resume secrets = %v, want Claude Code's key and not OpenCode's", names)
	}
}
