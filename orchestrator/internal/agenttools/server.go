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
	"crypto/hmac"
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
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
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
	RunID, Org, ProjectID, TaskID, Role, Status string
}

func (c Caller) run() delivery.RunRef {
	return delivery.RunRef{Org: c.Org, ProjectID: c.ProjectID, TaskID: c.TaskID, RunID: c.RunID}
}

// event is a ledger event by the calling agent, on its Run.
func (c Caller) event(typ string, payload map[string]any) ledger.Event {
	return c.run().Event(typ, ledger.ActorAgent, payload)
}

// NewToken mints a random token: the value, and the hash to store.
func NewToken() (token, hash string) {
	var b [32]byte
	_, _ = rand.Read(b[:])
	token = "dude_run_" + hex.EncodeToString(b[:])
	return token, HashToken(token)
}

// RunToken is a Run's token for one start (submit, or a resume): derived
// from a server key, the Run and which start it is, so a retried submit or
// resume — whose first attempt lux may have taken, keeping its secrets —
// carries the same token rather than one lux never saw. Only its hash is
// stored.
func RunToken(key []byte, runID string, start int) (token, hash string) {
	mac := hmac.New(sha256.New, key)
	fmt.Fprintf(mac, "dude-run-token\x00%s\x00%d", runID, start)
	token = "dude_run_" + hex.EncodeToString(mac.Sum(nil))
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
	err := s.DB.Pool.QueryRow(ctx, `SELECT run_id, organization_id, project_id, task_id, role, status
		FROM lookup_run_by_mcp_token($1)`, HashToken(token)).Scan(&c.RunID, &c.Org, &c.ProjectID, &c.TaskID, &role, &c.Status)
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

// Calls a Run may make in a minute, and tasks and events it may
// create in its lifetime: enough for real work, not for a runaway loop.
const (
	callsPerMinute = 60
	createsPerRun  = 20
	eventsPerRun   = 2000
	requestsPerRun = 10
	resultInLedger = 4 << 10 // bytes of a result recorded; the rest summarized
)

// call runs a tool and records the call in the ledger, in the same
// transaction as what the tool did.
func (s *Server) call(ctx context.Context, c Caller, t tool, args json.RawMessage) (json.RawMessage, error) {
	var out json.RawMessage
	err := s.DB.InOrg(ctx, c.Org, func(tx pgx.Tx) error {
		if err := withinLimits(ctx, tx, c, t.name); err != nil {
			return err
		}
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
		// A large answer (a whole project's listing) is not copied into the
		// ledger on every call: its size is.
		recorded := any(out)
		if len(out) > resultInLedger {
			recorded = map[string]any{"bytes": len(out), "truncated": true}
		}
		_, err = ledger.Append(ctx, tx, c.event(EventType, map[string]any{"tool": t.name, "arguments": args, "result": recorded}))
		return err
	})
	return out, err
}

// withinLimits refuses a call past the Run's budget: counted from the
// ledger, where every call is.
func withinLimits(ctx context.Context, tx pgx.Tx, c Caller, tool string) error {
	var recent, sameTool int
	if err := tx.QueryRow(ctx, `SELECT
			count(*) FILTER (WHERE occurred_at > now() - interval '1 minute'),
			count(*) FILTER (WHERE payload->>'tool' = $3)
		FROM events WHERE run_id = $1 AND event_type = $2`, c.RunID, EventType, tool).Scan(&recent, &sameTool); err != nil {
		return err
	}
	limit := map[string]int{"create_task": createsPerRun, "emit_event": eventsPerRun, "request_repository": requestsPerRun}[tool]
	switch {
	case recent >= callsPerMinute:
		return refuse("too many calls: at most %d a minute; slow down", callsPerMinute)
	case limit > 0 && sameTool >= limit:
		return refuse("%s is limited to %d calls in a run; this run has used them", tool, limit)
	}
	return nil
}

// refusal is a tool declining a request, with the reason for the caller.
type refusal struct{ msg string }

func (r refusal) Error() string { return r.msg }

func refuse(format string, a ...any) error { return refusal{fmt.Sprintf(format, a...)} }
