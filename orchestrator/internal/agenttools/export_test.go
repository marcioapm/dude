package agenttools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/capabilities"
)

func CountInspectionMetadataEncodings(t *testing.T) *int {
	t.Helper()
	count := new(int)
	original := marshalInspection
	marshalInspection = func(v any) ([]byte, error) {
		if m, ok := v.(map[string]any); ok {
			if _, present := m["recordedHeads"]; present {
				*count++
			}
		}
		return json.Marshal(v)
	}
	t.Cleanup(func() { marshalInspection = original })
	return count
}

// ResolveCapabilitiesWith makes s resolve its callers' capabilities with
// resolve in place of capabilities.Resolve.
func (s *Server) ResolveCapabilitiesWith(resolve func(context.Context, pgx.Tx, capabilities.Agent) (capabilities.Grants, error)) {
	s.resolve = resolve
}

// RecoverNext is recover_run's next step for a recovery yet to end.
var RecoverNext = recoverNext

// RecoverEndedNext is recover_run's next step for a recovery that ended.
var RecoverEndedNext = recoverEndedNext
