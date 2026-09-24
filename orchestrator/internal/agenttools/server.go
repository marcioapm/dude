// Package agenttools is dude's MCP server: tools that act on dude itself,
// for the agents of phase Runs (docs/design/agent-tools.md).
//
// An agent authenticates with its Run's bearer token, minted when the Run is
// submitted and good only while it runs. The token names the Run, and the
// Run is all a call may reach: its organization, its project, its work
// item. Each request gets a server built for that Run, listing only the
// tools its role may use, so a tool never has to ask who is calling.
//
// Every call is recorded in the ledger, as the agent's, so the chat shows it
// beside the agent's own tools; and what an agent makes is marked as made
// by it, never started on its own — a person decides.
package agenttools

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// EventType is the ledger event for one call of a dude tool.
const EventType = "agent.tool.dude"

// Server serves the tools over MCP streamable HTTP.
type Server struct {
	DB  *db.DB
	Log *slog.Logger
}

// Caller is the Run a token names: who is calling, and all it may reach.
type Caller struct {
	RunID, Org, ProjectID, WorkItemID, Role, Status string
}

// NewToken mints a Run's token: the value for the agent, and the hash to
// store. Only the hash is kept.
func NewToken() (token, hash string) {
	var b [32]byte
	_, _ = rand.Read(b[:])
	token = "dude_run_" + hex.EncodeToString(b[:])
	return token, HashToken(token)
}

// HashToken is how a token is stored and looked up.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Handler serves MCP. A request without a live Run's token is refused
// before any MCP is spoken.
func (s *Server) Handler() http.Handler {
	mcpHandler := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		caller, _ := r.Context().Value(callerKey{}).(Caller)
		return s.serverFor(caller)
	}, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || token == "" {
			http.Error(w, "a Run's token is required", http.StatusUnauthorized)
			return
		}
		caller, err := s.lookup(r.Context(), token)
		switch {
		case errors.Is(err, errUnknown):
			http.Error(w, "unknown or ended Run", http.StatusUnauthorized)
			return
		case err != nil:
			s.Log.Error("agent tools: token lookup failed", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		mcpHandler.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), callerKey{}, caller)))
	})
}

type callerKey struct{}

var errUnknown = errors.New("unknown token")

// lookup finds the live Run a token names. A Run that has ended — finished,
// failed, aborted — has no tools.
func (s *Server) lookup(ctx context.Context, token string) (Caller, error) {
	var c Caller
	var role *string
	err := s.DB.Pool.QueryRow(ctx, `SELECT run_id, organization_id, project_id, work_item_id, role, status
		FROM lookup_run_by_mcp_token($1)`, HashToken(token)).Scan(&c.RunID, &c.Org, &c.ProjectID, &c.WorkItemID, &role, &c.Status)
	if db.IsNotFound(err) {
		return c, errUnknown
	}
	if err != nil {
		return c, err
	}
	if role != nil {
		c.Role = *role
	}
	switch c.Status {
	case "completed", "failed", "aborted":
		return c, errUnknown
	}
	return c, nil
}

// serverFor builds the MCP server a Run sees: the tools its role may use,
// each bound to it.
func (s *Server) serverFor(c Caller) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "dude", Version: "1"}, &mcp.ServerOptions{
		Instructions: "Tools for the work you are doing in dude, the software factory that started you: " +
			"see and add to the project's work. What you create is marked as yours and waits for a person.",
	})
	for _, t := range tools {
		if t.allowed(c.Role) {
			t.add(s, srv, c)
		}
	}
	return srv
}

// tool is one tool: who may use it, and how to add it bound to a Run.
type tool struct {
	name  string
	roles []string // empty: every role
	add   func(s *Server, srv *mcp.Server, c Caller)
}

func (t tool) allowed(role string) bool {
	if len(t.roles) == 0 {
		return true
	}
	for _, r := range t.roles {
		if r == role {
			return true
		}
	}
	return false
}

// bind adds a typed tool whose calls are recorded in the ledger, in the
// Run's organization, in the same transaction as what the tool did.
func bind[In, Out any](s *Server, srv *mcp.Server, c Caller, def *mcp.Tool,
	do func(ctx context.Context, tx pgx.Tx, c Caller, in In) (Out, error)) {
	mcp.AddTool(srv, def, func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
		var out Out
		err := s.DB.InOrg(ctx, c.Org, func(tx pgx.Tx) error {
			var err error
			if out, err = do(ctx, tx, c, in); err != nil {
				return err
			}
			args, _ := json.Marshal(in)
			result, _ := json.Marshal(out)
			_, err = ledger.Append(ctx, tx, ledger.Event{
				Type: EventType, OrganizationID: c.Org, ProjectID: c.ProjectID, WorkItemID: c.WorkItemID, RunID: c.RunID,
				ActorType: ledger.ActorAgent, ActorID: c.RunID, Source: ledger.SourceOrchestrator, CorrelationID: c.WorkItemID,
				Payload: map[string]any{"tool": def.Name, "arguments": json.RawMessage(args), "result": json.RawMessage(result)},
			})
			return err
		})
		var refused refusal
		if errors.As(err, &refused) {
			// Said to the agent, which can do something about it; not a
			// failure of the server.
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: refused.Error()}}}, out, nil
		}
		return nil, out, err
	})
}

// refusal is a tool declining a request, with the reason for the agent.
type refusal struct{ msg string }

func (r refusal) Error() string { return r.msg }

func refuse(format string, a ...any) error { return refusal{fmt.Sprintf(format, a...)} }
