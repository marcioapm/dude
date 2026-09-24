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
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
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

// Handler serves the tools two ways, to the same code: MCP at / (for
// agents that take MCP servers), and plain JSON at POST /tools/<name> (for
// the dude CLI in the agent's container). A request without a live Run's
// token is refused before anything else.
func (s *Server) Handler() http.Handler {
	mcpHandler := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		caller, _ := r.Context().Value(callerKey{}).(Caller)
		return s.serverFor(caller)
	}, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /tools", s.listJSON)
	mux.HandleFunc("POST /tools/{name}", s.callJSON)
	mux.Handle("/", mcpHandler)
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
		mux.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), callerKey{}, caller)))
	})
}

// listJSON is what the CLI shows for `dude tools`: the tools this Run has.
func (s *Server) listJSON(w http.ResponseWriter, r *http.Request) {
	c, _ := r.Context().Value(callerKey{}).(Caller)
	type entry struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	out := []entry{}
	for _, t := range tools {
		if t.allowed(c.Role) {
			out = append(out, entry{t.name, t.description})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"tools": out})
}

// callJSON calls one tool with a JSON body; the result, or {"error"} with
// 422 for a request the tool refused (the caller's to fix) and 404 for a
// tool this Run does not have.
func (s *Server) callJSON(w http.ResponseWriter, r *http.Request) {
	c, _ := r.Context().Value(callerKey{}).(Caller)
	t, ok := find(r.PathValue("name"))
	if !ok || !t.allowed(c.Role) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no tool " + r.PathValue("name") + " for this Run"})
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxInput+1))
	if err != nil || len(body) > maxInput {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "request too large"})
		return
	}
	out, err := s.call(r.Context(), c, t, body)
	var refused refusal
	switch {
	case errors.As(err, &refused):
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": refused.Error()})
	case err != nil:
		s.Log.Error("agent tool failed", "tool", t.name, "run", c.RunID, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
	default:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(out)
	}
}

// maxInput bounds a tool call's arguments.
const maxInput = 256 << 10

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
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
		if !t.allowed(c.Role) {
			continue
		}
		srv.AddTool(&mcp.Tool{Name: t.name, Description: t.description, InputSchema: t.schema},
			func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				out, err := s.call(ctx, c, t, req.Params.Arguments)
				var refused refusal
				if errors.As(err, &refused) {
					// Said to the agent, which can do something about it; not
					// a failure of the server.
					return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: refused.Error()}}}, nil
				}
				if err != nil {
					return nil, err
				}
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(out)}},
					StructuredContent: json.RawMessage(out)}, nil
			})
	}
	return srv
}

// tool is one tool, whichever way it is called: who may use it, and what it
// does with JSON arguments, in the calling Run's organization.
type tool struct {
	name, description string
	roles             []string // empty: every role
	schema            any
	run               func(ctx context.Context, tx pgx.Tx, c Caller, args json.RawMessage) (any, error)
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

func find(name string) (tool, bool) {
	for _, t := range tools {
		if t.name == name {
			return t, true
		}
	}
	return tool{}, false
}

// define makes a tool from a typed function: its input schema from In,
// its arguments decoded strictly into In.
func define[In, Out any](name, description string, roles []string,
	do func(ctx context.Context, tx pgx.Tx, c Caller, in In) (Out, error)) tool {
	schema, err := jsonschema.For[In](nil)
	if err != nil {
		panic(fmt.Sprintf("tool %s: %v", name, err))
	}
	return tool{name: name, description: description, roles: roles, schema: schema,
		run: func(ctx context.Context, tx pgx.Tx, c Caller, args json.RawMessage) (any, error) {
			var in In
			if len(args) > 0 && string(args) != "null" {
				dec := json.NewDecoder(bytes.NewReader(args))
				dec.DisallowUnknownFields()
				if err := dec.Decode(&in); err != nil {
					return nil, refuse("arguments: %v", err)
				}
			}
			return do(ctx, tx, c, in)
		}}
}

// call runs a tool and records the call in the ledger, in the same
// transaction as what the tool did.
func (s *Server) call(ctx context.Context, c Caller, t tool, args json.RawMessage) (json.RawMessage, error) {
	var out json.RawMessage
	err := s.DB.InOrg(ctx, c.Org, func(tx pgx.Tx) error {
		result, err := t.run(ctx, tx, c, args)
		if err != nil {
			return err
		}
		if out, err = json.Marshal(result); err != nil {
			return err
		}
		if len(args) == 0 {
			args = json.RawMessage("{}")
		}
		_, err = ledger.Append(ctx, tx, ledger.Event{
			Type: EventType, OrganizationID: c.Org, ProjectID: c.ProjectID, WorkItemID: c.WorkItemID, RunID: c.RunID,
			ActorType: ledger.ActorAgent, ActorID: c.RunID, Source: ledger.SourceOrchestrator, CorrelationID: c.WorkItemID,
			Payload: map[string]any{"tool": t.name, "arguments": args, "result": out},
		})
		return err
	})
	return out, err
}

// refusal is a tool declining a request, with the reason for the caller.
type refusal struct{ msg string }

func (r refusal) Error() string { return r.msg }

func refuse(format string, a ...any) error { return refusal{fmt.Sprintf(format, a...)} }
