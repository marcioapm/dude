package orchestrator_test

import (
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// lux keeps no secret values, so a real agent's resume must carry the LLM
// key again, or lux refuses the resume and the Run fails.
func TestAResumedRealAgentIsGivenTheLLMKeyAgain(t *testing.T) {
	w := newWorld(t)
	mustExec(t, w.owner, `UPDATE projects SET agent_models = agent_models || '{"implementer":{"model":"llm-anthropic/impl"}}'::jsonb
		WHERE id = $1`, w.project)
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
