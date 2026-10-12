package orchestrator_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// A talker resumed into a new harness session — lux could not reload its
// own, warned so and reported another id — records agent.session.replaced
// with lux's reason and lux's warning, and is briefed again: its own
// briefing, saying the conversation is lost, before the message that
// resumed it, which it still answers.
func TestATalkerResumedWithoutItsSessionIsBriefedAgain(t *testing.T) {
	for kind, start := range talkers(t) {
		t.Run(kind, func(t *testing.T) {
			tk, run := start(t)
			first := tk.sessionIDs(run)
			tk.lux.LoseSession(tk.luxRunOf(run))
			tk.stopOnItsOwn(run, "stopped")
			tk.parked(run)
			if status, reached := tk.write("still with me?"); status != 200 || reached != run {
				t.Fatalf("the message: %d reached %q", status, reached)
			}
			tk.resumedAnswering(run, "still with me?")
			if n := tk.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'agent.warning'
				AND payload->>'message' LIKE 'session/load failed%'`, run); n != 1 {
				t.Errorf("%d agent.warning carrying lux's text, want 1", n)
			}
			var from, to, reason string
			if err := tk.owner.QueryRow(t0(), `SELECT payload->>'from', payload->>'to', payload->>'reason' FROM events
				WHERE run_id = $1 AND event_type = 'agent.session.replaced'`, run).Scan(&from, &to, &reason); err != nil {
				t.Fatalf("no agent.session.replaced: %v", err)
			}
			if len(first) != 1 || from != first[0] || to == from || !strings.HasPrefix(reason, "session/load failed") {
				t.Errorf("replaced from %q to %q (%q); the first session was %v", from, to, reason, first)
			}
			var inputs []string
			for _, r := range tk.lux.Runs() {
				if strings.Contains(string(r.Spec), `"dude.run":"`+run+`"`) {
					inputs = append(slices.Clone(r.ResumeInputs), r.Inputs...)
				}
			}
			briefed := slices.IndexFunc(inputs, func(in string) bool {
				return strings.Contains(in, "restarted without its earlier conversation") && strings.Contains(in, "session/load failed")
			})
			if briefed < 0 {
				t.Fatalf("the agent was not briefed again: %q", inputs)
			}
			if kind == "brainstorm" && !strings.Contains(inputs[briefed], "## Linked projects") {
				t.Errorf("not the session's briefing: %q", inputs[briefed])
			}
			if kind == "conductor" && !strings.Contains(inputs[briefed], "## The task") {
				t.Errorf("not the task's briefing: %q", inputs[briefed])
			}
			if !slices.ContainsFunc(inputs, func(in string) bool { return strings.Contains(in, "still with me?") }) {
				t.Errorf("the message that resumed it never reached it: %q", inputs)
			}
		})
	}
}

// A blank harness session has no prompt but what it is sent: the briefing
// again carries everything the first prompt had around the briefing — how
// the agent works, its tools — and says the conversation is lost in dude's
// own words, not as a member's or person's message.
func TestATalkerBriefedAgainIsToldHowItWorks(t *testing.T) {
	for kind, start := range talkers(t) {
		t.Run(kind, func(t *testing.T) {
			tk, run := start(t)
			tk.syncer.Agent.ToolsURL = "http://10.9.8.7:3120/mcp"
			tk.lux.LoseSession(tk.luxRunOf(run))
			tk.stopOnItsOwn(run, "stopped")
			tk.parked(run)
			tk.write("still with me?")
			tk.until("the briefing again queued", func() bool {
				return tk.count(`SELECT count(*) FROM directives WHERE run_id = $1 AND strpos(text, 'restarted without') > 0`, run) == 1
			})
			var text string
			if err := tk.owner.QueryRow(t0(), `SELECT text FROM directives WHERE run_id = $1 AND strpos(text, 'restarted without') > 0`,
				run).Scan(&text); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(text, "## How you work") {
				t.Errorf("no role instructions:\n%s", text)
			}
			lost := strings.Index(text, "restarted without its earlier conversation")
			switch kind {
			case "brainstorm":
				if !strings.Contains(text, "You think out loud with the members of this session") ||
					!strings.Contains(text, "The dude tools read the linked projects") || !strings.Contains(text, "name_session") {
					t.Errorf("not the brainstorm's instructions and tools:\n%s", text)
				}
				heading := strings.Index(text, "## Your earlier conversation is lost")
				if strings.Contains(text, "## The first message") || heading < 0 || lost < heading {
					t.Errorf("dude's note not under a heading of its own:\n%s", text)
				}
			case "conductor":
				if !strings.Contains(text, "The people on the task talk to you in its Chat") ||
					!strings.Contains(text, "The dude tools read what dude knows about this task") {
					t.Errorf("not the conductor's instructions and tools:\n%s", text)
				}
				if !strings.Contains(text, "Conductor, dude is briefing you again") || !strings.Contains(text, "## dude's message") ||
					strings.Contains(text, "wrote in the Chat") {
					t.Errorf("dude's note framed as a person's message:\n%s", text)
				}
				if strings.Contains(text, "dude woke you") || strings.Contains(text, "a decision is yours") {
					t.Errorf("briefed again as if woken for a decision:\n%s", text)
				}
			}
			if lost < 0 {
				t.Errorf("no word of the lost conversation:\n%s", text)
			}
		})
	}
}

// A brainstorm briefed again is told of the web by the network it was
// submitted with, which its resumes keep, not by the setting as changed
// since: started able to reach any host, it is still told it can read the
// web; started on the list, it is not.
func TestABrainstormBriefedAgainIsToldTheNetworkItRunsWith(t *testing.T) {
	for _, anywhere := range []bool{true, false} {
		t.Run(map[bool]string{true: "anywhere then off", false: "list then on"}[anywhere], func(t *testing.T) {
			s := newSessionWorld(t)
			mustExec(t, s.owner, `UPDATE organizations SET agent_egress = '{pypi.org}', brainstorm_anywhere = $2 WHERE id = $1`, s.org, anywhere)
			id := s.session()
			tk := &talking{world: s.world, kind: "brainstorm", park: "session"}
			tk.write = func(text string) (int, string) {
				status, out := s.as(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": text})
				run, _ := out["runId"].(string)
				return status, run
			}
			tk.latest = func() string { run, _ := s.brainstorm(id); return run }
			tk.syncer.ConductorWarm = time.Hour
			tk.write("what do competitors charge?")
			var run string
			tk.until("the brainstorm's network to be recorded", func() bool {
				run = tk.latest()
				return run != "" && tk.count(`SELECT count(*) FROM runs WHERE id = $1 AND network IS NOT NULL AND turn_done_at IS NOT NULL`, run) == 1
			})
			if got := tk.count(`SELECT count(*) FROM runs WHERE id = $1 AND (network->>'unrestricted')::boolean IS TRUE`, run) == 1; got != anywhere {
				t.Fatalf("recorded unrestricted %v, want %v", got, anywhere)
			}
			mustExec(t, tk.owner, `UPDATE organizations SET brainstorm_anywhere = $2 WHERE id = $1`, tk.org, !anywhere)
			tk.lux.LoseSession(tk.luxRunOf(run))
			tk.stopOnItsOwn(run, "stopped")
			tk.parked(run)
			tk.write("still with me?")
			tk.until("the briefing again queued", func() bool {
				return tk.count(`SELECT count(*) FROM directives WHERE run_id = $1 AND strpos(text, 'restarted without') > 0`, run) == 1
			})
			var text string
			if err := tk.owner.QueryRow(t0(), `SELECT text FROM directives WHERE run_id = $1 AND strpos(text, 'restarted without') > 0`,
				run).Scan(&text); err != nil {
				t.Fatal(err)
			}
			if told := strings.Contains(text, webNote); told != anywhere {
				t.Errorf("told it can read the web %v, want %v:\n%s", told, anywhere, text)
			}
		})
	}
}

// A Codex brainstorm resumed after the setting changed keeps the search it
// was submitted with: started able to reach any host, its resume's
// config.toml still pins web_search live; started on the list, the resume
// pins nothing.
func TestAResumedCodexBrainstormKeepsTheSearchItWasSubmittedWith(t *testing.T) {
	for _, anywhere := range []bool{true, false} {
		t.Run(map[bool]string{true: "anywhere then off", false: "list then on"}[anywhere], func(t *testing.T) {
			s := newSessionWorld(t)
			s.syncer.ConductorWarm = time.Hour
			s.lux.Decide = hang
			mustExec(t, s.owner, `UPDATE model_tiers SET model = 'gpt-6-sol' WHERE organization_id = $1 AND name = 'Thinker'`, s.org)
			mustExec(t, s.owner, `UPDATE organizations SET agent_egress = '{pypi.org}', brainstorm_anywhere = $2 WHERE id = $1`, s.org, anywhere)
			id := s.ok(s.marcio, "POST", "/internal/sessions", map[string]any{"message": "hello", "harness": "codex"})["id"].(string)
			run, _, _, harness := s.submitted(id)
			if harness != "codex" {
				t.Fatalf("submitted on %s", harness)
			}
			if live := codexSearchesLive(t, s.luxRun(run).spec.Secrets); live != anywhere {
				t.Fatalf("submitted pinned live %v, want %v", live, anywhere)
			}
			s.runningIdle(run)
			mustExec(t, s.owner, `UPDATE organizations SET brainstorm_anywhere = $2 WHERE id = $1`, s.org, !anywhere)
			mustExec(t, s.owner, `UPDATE runs SET turn_done_at = now() - interval '2 hours' WHERE id = $1`, run)
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
			if live := codexSearchesLive(t, secrets); live != anywhere {
				t.Errorf("the resume pins live %v, want %v as submitted", live, anywhere)
			}
		})
	}
}

// codexSearchesLive says whether CODEX_CONFIG among secrets, parsed as
// TOML, pins web_search "live".
func codexSearchesLive(t *testing.T, secrets []lux.Secret) bool {
	t.Helper()
	i := slices.IndexFunc(secrets, func(s lux.Secret) bool { return s.Name == "CODEX_CONFIG" })
	if i < 0 {
		t.Fatalf("no CODEX_CONFIG among %v", secretNames(secrets))
	}
	var config map[string]any
	if err := toml.Unmarshal([]byte(secrets[i].Value), &config); err != nil {
		t.Fatalf("CODEX_CONFIG: %v", err)
	}
	return config["web_search"] == "live"
}

// A resume that keeps its session records nothing more.
func TestATalkerResumedInItsSessionIsNotBriefedAgain(t *testing.T) {
	for kind, start := range talkers(t) {
		t.Run(kind, func(t *testing.T) {
			tk, run := start(t)
			tk.stopOnItsOwn(run, "stopped")
			tk.parked(run)
			tk.write("still with me?")
			tk.resumedAnswering(run, "still with me?")
			if n := tk.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type IN ('agent.session.replaced', 'agent.warning')`, run); n != 0 {
				t.Errorf("%d replaced/warning events for a resume in the same session", n)
			}
			if n := tk.count(`SELECT count(*) FROM directives WHERE run_id = $1 AND strpos(text, 'restarted without') > 0`, run); n != 0 {
				t.Errorf("briefed again in its own session")
			}
		})
	}
}
