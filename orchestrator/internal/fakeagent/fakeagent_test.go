package fakeagent

import (
	"strings"
	"testing"
)

// A session agent's briefing names a session, not a task: the scripted
// reply quotes the session's title, first turn and later ones, never an
// empty task. A conductor's still quotes its task.
func TestTheScriptedReplyQuotesTheSessionsTitle(t *testing.T) {
	briefing := "Brainstorm, this is the session \"Usage-based billing\". You think with its members about their projects: read, " +
		"ask, propose. Below is what dude knows; read more with your tools.\n\n## People\n\n- Márcio (owner)\n\n## Linked projects\n\n" +
		"None yet.\n\n## The first message\n\nMárcio: where does metering go?\n\n## How you work\n\nRead."
	script := ConductorScript(briefing)
	first := script[strings.LastIndex(script, "echo ")+len("echo "):]
	if want := `In the session "Usage-based billing". You asked: "Márcio: where does metering go?". Read-only: I changed nothing.`; first != want {
		t.Errorf("first reply %q, want %q", first, want)
	}
	if strings.Contains(script, `Briefed on ""`) {
		t.Errorf("the reply names an empty task: %q", script)
	}
	if got, want := ConductorTurn(script, "Ana: and retries?"), `In the session "Usage-based billing". You asked: "Ana: and retries?". Read-only: I changed nothing.`; got != want {
		t.Errorf("next reply %q, want %q", got, want)
	}

	conductor := ConductorScript("Conductor, Márcio wrote.\n\n## The task\n\nWC-214 · tsk\n\n## Márcio's message\n\nwhy 8s?")
	if !strings.HasSuffix(conductor, `echo Briefed on "WC-214 · tsk". You asked: "why 8s?". Read-only: I changed nothing.`) {
		t.Errorf("conductor's reply: %q", conductor)
	}
	if got := ConductorTurn(conductor, "and POSTs?"); got != `Briefed on "WC-214 · tsk". You asked: "and POSTs?". Read-only: I changed nothing.` {
		t.Errorf("conductor's next reply: %q", got)
	}
}

// Every way a session's briefing opens (delivery.sessionBriefing) — named
// by its agent, named by a member, not named yet — is a session's: the reply
// quotes its title, or says it has none, and never an empty task.
func TestTheScriptedReplyKnowsEverySessionsBriefing(t *testing.T) {
	rest := " You think with its members about their projects: read, ask, propose.\n\n## The first message\n\nMárcio: where?\n\n## How you work\n\nRead."
	for lead, want := range map[string]string{
		`Brainstorm, this is the session "Billing", as you named it.`:                         `In the session "Billing". You asked: "Márcio: where?"`,
		`Brainstorm, this is the session "Billing v2", as a member named it: keep that name.`: `In the session "Billing v2". You asked: "Márcio: where?"`,
		`Brainstorm, this is a new session, not named yet.`:                                   `In the session "New session". You asked: "Márcio: where?"`,
	} {
		script := ConductorScript(lead + rest)
		if !strings.Contains(script, "echo "+want) || strings.Contains(script, "Briefed on") {
			t.Errorf("%s\n  reply %q\n  want %q", lead, script[strings.LastIndex(script, "echo "):], want)
		}
		if got := ConductorTurn(script, "Ana: and retries?"); !strings.HasPrefix(got, "In the session ") {
			t.Errorf("%s: next reply %q", lead, got)
		}
	}
}
