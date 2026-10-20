package agenttools

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/capabilities"
)

// ResolveCapabilitiesWith makes s resolve its callers' capabilities with
// resolve in place of capabilities.Resolve.
func (s *Server) ResolveCapabilitiesWith(resolve func(context.Context, pgx.Tx, capabilities.Agent) (capabilities.Grants, error)) {
	s.resolve = resolve
}
