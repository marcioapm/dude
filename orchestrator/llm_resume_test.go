package orchestrator_test

import (
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// lux keeps no secret values, so a real agent's resume must carry the LLM
// key again, or lux refuses the resume and the Run fails.
func TestAResumedRealAgentIsGivenTheLLMKeyAgain(t *testing.T) {
	w := newWorld(t)
	w.onModel("implementer", "claude-impl")
	w.lux.Decide = hang
	wi := w.task()
	w.deliver(wi)
	w.until("the implementer to be submitted", func() bool { return len(w.lux.Runs()) >= 1 })
	w.pauseAndResume(wi)

	r := w.lux.Runs()[0]
	if len(r.ResumeSecrets) != 1 {
		t.Fatalf("resumes = %d, want 1", len(r.ResumeSecrets))
	}
	var key *lux.Secret
	for i, s := range r.ResumeSecrets[0] {
		if s.Name == "DUDE_LLM_KEY" {
			key = &r.ResumeSecrets[0][i]
		}
	}
	if key == nil || key.Value != "secret-key" {
		t.Errorf("resume secrets = %+v, want DUDE_LLM_KEY with the configured key", r.ResumeSecrets[0])
	}
}

// A resume is built from the model the Run was submitted with, not its
// tier's now: a scripted Run whose tier was since moved to a real model
// resumes as the scripted agent, given no LLM key.
func TestAResumedScriptedRunIsGivenNoLLMKey(t *testing.T) {
	w := newWorld(t)
	tier := w.onModel("implementer", "fake/scripted")
	w.lux.Decide = hang
	wi := w.task()
	w.deliver(wi)
	w.until("the implementer to be submitted", func() bool { return len(w.lux.Runs()) >= 1 })
	mustExec(t, w.owner, `UPDATE model_tiers SET model = 'claude-opus-5-5' WHERE id = $1`, tier)
	w.pauseAndResume(wi)

	r := w.lux.Runs()[0]
	if len(r.ResumeSecrets) != 1 {
		t.Fatalf("resumes = %d, want 1", len(r.ResumeSecrets))
	}
	for _, s := range r.ResumeSecrets[0] {
		if s.Name == "DUDE_LLM_KEY" {
			t.Errorf("a scripted Run's resume carried the LLM key: %+v", r.ResumeSecrets[0])
		}
	}
}
