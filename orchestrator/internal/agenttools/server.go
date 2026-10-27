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
	"cmp"
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
	"slices"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/jackc/pgx/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/marciomartins/dude/orchestrator/internal/capabilities"
	"github.com/marciomartins/dude/orchestrator/internal/chat"
	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/embeddings"
	"github.com/marciomartins/dude/orchestrator/internal/ids"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// EventType is the ledger event for one call of a dude tool.
const EventType = "agent.tool.dude"

// AttemptEventType is the ledger event for a call of a countingAttempts
// tool that passed its checks, written before its outside work; its
// result's EventType event is marked attempted and not counted again.
const AttemptEventType = "agent.tool.attempted"

// githubReader is a checked call spending more than one GitHub read: its
// attempt is recorded once per read, which the hourly count counts.
type githubReader interface{ githubReads() int }

// Server serves the tools over MCP streamable HTTP.
type Server struct {
	DB              *db.DB
	Log             *slog.Logger
	Lux             lux.Client
	ValidateMachine func(context.Context, string, *delivery.Machine, *delivery.Machine, lux.Run) error
	// Embedder lets search_memory search by meaning; nil, by words alone.
	Embedder embeddings.Embedder
	// Kick wakes the orchestrator's loops: a memory saved is embedded now.
	Kick func()
	// Forges finds the organization's GitHub client, for a conductor's
	// reply on a pull request; nil, no forge.
	Forges delivery.Forges
	// SyncPullRequest is prs.Syncer.Sync: a pull request is read back after
	// a PR action succeeded on it, and refresh_pull_requests reads the
	// task's now. nil skips the first and refuses the second.
	SyncPullRequest func(ctx context.Context, org, prID string) error
	// SyncBase asks lux for sync_base's fetch; nil, sync_base is refused.
	SyncBase SyncBaseFunc
	// How long sync_base waits for lux's reports; zero, syncBaseWait.
	SyncBaseWait time.Duration
	// PublicURL is dude's https origin (auth.public_url), the base of a
	// guide section's link; "", links are the web app's hash path alone.
	PublicURL string
	// Chat reads the conversation a chat-born session came from
	// (chat_history); nil, the tool says chat is not available.
	Chat ChatReader
	// ChatProviders are the chat providers post_to_chat posts through.
	ChatProviders chat.Registry
	// Test hook: called with the tool's name after the caller is
	// authenticated, before the tool's transaction opens.
	BeforeCall func(tool string)
	// Test hook: resolves a caller's capabilities; nil, capabilities.Resolve.
	resolve func(context.Context, pgx.Tx, capabilities.Agent) (capabilities.Grants, error)
}

// Caller is the Run a token names: who is calling, and all it may reach.
type Caller struct {
	CallID                                      string
	Child                                       bool
	RunID, Org, ProjectID, TaskID, Role, Status string
	// A session's agent: its session, in place of a task and project.
	SessionID       string
	AutomationID    string
	AutomationScope AutomationScope
	// Its capabilities (capabilities.Resolve), resolved with the token.
	grants capabilities.Grants
	// What the server lends a call: set on each, not part of who calls.
	env env
}

type AutomationScope struct {
	ProjectID   string   `json:"projectId"`
	OwnerID     string   `json:"ownerId"`
	Repos       []string `json:"repos"`
	EventRepo   string   `json:"eventRepo"`
	EventNumber int      `json:"eventNumber"`
}

// env is the server's, for the tools that need more than the database, and
// what a tool's prepare step made before its transaction opened.
type env struct {
	embedder        embeddings.Embedder
	lux             lux.Client
	validateMachine func(context.Context, string, *delivery.Machine, *delivery.Machine, lux.Run) error
	kick            func()
	forges          delivery.Forges
	syncBase        SyncBaseFunc
	syncBaseWait    time.Duration
	syncPR          func(ctx context.Context, org, prID string) error
	publicURL       string
	chat            ChatReader
	chatProviders   chat.Registry
	db              *db.DB
	prepared        any
}

func (c Caller) run() delivery.RunRef {
	return delivery.RunRef{Org: c.Org, ProjectID: c.ProjectID, TaskID: c.TaskID, RunID: c.RunID, SessionID: c.SessionID}
}

// event is a ledger event by the calling agent, on its Run; an automation
// run's is keyed by automation_run_id alone.
func (c Caller) event(typ string, payload map[string]any) ledger.Event {
	if c.AutomationID != "" {
		payload["automationId"], payload["automationRunId"], payload["actingFor"] = c.AutomationID, c.RunID, c.AutomationScope.OwnerID
		e := c.run().Event(typ, ledger.ActorAgent, payload)
		e.RunID, e.AutomationRunID = "", c.RunID
		return e
	}
	return c.run().Event(typ, ledger.ActorAgent, payload)
}

// runColumn is the events column c's own events are keyed by.
func (c Caller) runColumn() string {
	if c.AutomationID != "" {
		return "automation_run_id"
	}
	return "run_id"
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
	mux.HandleFunc("GET /git/coauthors", s.coauthors)
	mux.HandleFunc("GET /git/diff-bases", s.diffBases)
	mux.Handle("/", mcpHandler)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || token == "" {
			http.Error(w, httpTokenRequired, http.StatusUnauthorized)
			return
		}
		caller, err := s.lookup(r.Context(), token)
		// Final hooks may run after dude has already marked the Run ended.
		if err == nil && !(r.Method == "GET" && r.URL.Path == "/git/diff-bases") && (caller.Status == "completed" || caller.Status == "failed" || caller.Status == "aborted") {
			err = errUnknown
		}
		switch {
		case errors.Is(err, errUnknown):
			http.Error(w, httpUnknownRun, http.StatusUnauthorized)
			return
		case err != nil:
			s.Log.Error("agent tools: token lookup failed", "error", err)
			http.Error(w, httpInternalError, http.StatusInternalServerError)
			return
		}
		caller.CallID = r.Header.Get("X-Dude-Tool-Call-ID")
		if r.Method == http.MethodPost && r.URL.Path == "/" {
			body, err := io.ReadAll(io.LimitReader(r.Body, maxInput+1))
			if err != nil || len(body) > maxInput {
				http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			var envelope struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
			}
			if json.Unmarshal(body, &envelope) == nil && envelope.Method == "tools/call" && len(envelope.ID) > 0 {
				caller.CallID = "mcp:" + string(envelope.ID)
			}
		}
		if caller.CallID == "" {
			caller.CallID = ids.New("call")
		}
		mux.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), callerKey{}, caller)))
	})
}

// What the HTTP layer answers a request it cannot serve (refusals/http).
var (
	httpTokenRequired = delivery.MustText("refusals/http/token-required").Render(nil)
	httpUnknownRun    = delivery.MustText("refusals/http/unknown-run").Render(nil)
	httpInternalError = delivery.MustText("refusals/http/internal-error").Render(nil)
)

// listJSON is what the CLI shows for `dude tools`: the tools this Run has.
func (s *Server) listJSON(w http.ResponseWriter, r *http.Request) {
	c, _ := r.Context().Value(callerKey{}).(Caller)
	type entry struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	out := []entry{}
	for _, t := range c.tools() {
		if !c.Child || childToolAllowed(t.name) {
			out = append(out, entry{t.name, s.descriptionFor(r.Context(), c, t)})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"tools": out})
}

// callJSON calls one tool with a JSON body; the result, or {"error"} with
// 422 for a request the tool refused (the caller's to fix) and 404 for a
// tool this Run does not have.
func (s *Server) callJSON(w http.ResponseWriter, r *http.Request) {
	c, _ := r.Context().Value(callerKey{}).(Caller)
	t, ok := c.findTool(r.PathValue("name"))
	if !ok {
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
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": refused.Error(), "code": refused.code})
	case err != nil:
		s.Log.Error("agent tool failed", "tool", t.name, "run", c.RunID, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
	default:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(out)
	}
}

// maxInput bounds a tool call's arguments: 1 MiB, because json.Marshal escapes `<>&` as six bytes, so a 64K goal and 16K of criteria can take ~492 KB.
const maxInput = 1 << 20

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

type callerKey struct{}

var errUnknown = errors.New("unknown token")

// lookup finds the Run a token names, and whether it is a started child. The
// handler refuses an ended Run's tools, except the final hooks' diff bases.
func (s *Server) lookup(ctx context.Context, token string) (Caller, error) {
	var c Caller
	var role *string
	err := s.DB.Pool.QueryRow(ctx, `SELECT run_id, organization_id, COALESCE(project_id, ''), COALESCE(task_id, ''),
			COALESCE(session_id, ''), role, status
		FROM lookup_run_by_mcp_token($1)`, HashToken(token)).Scan(&c.RunID, &c.Org, &c.ProjectID, &c.TaskID, &c.SessionID, &role, &c.Status)
	if db.IsNotFound(err) {
		err = s.DB.Pool.QueryRow(ctx, `SELECT run_id, organization_id, automation_id, status, scope
			FROM lookup_automation_by_mcp_token($1)`, HashToken(token)).Scan(&c.RunID, &c.Org, &c.AutomationID, &c.Status, &c.AutomationScope)
		if db.IsNotFound(err) {
			return c, errUnknown
		}
		c.ProjectID = c.AutomationScope.ProjectID
		c.Role = "automation"
		c.grants = capabilities.Of(capabilities.TasksCreate, capabilities.GitHubRead)
		return c, err
	}
	if err != nil {
		return c, err
	}
	if role != nil {
		c.Role = *role
	}
	resolve := s.resolve
	if resolve == nil {
		resolve = capabilities.Resolve
	}
	err = s.DB.InOrg(ctx, c.Org, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT source_kind IS NOT NULL FROM runs WHERE id=$1`, c.RunID).Scan(&c.Child); err != nil {
			return err
		}
		c.grants, err = resolve(ctx, tx, c.agent())
		return err
	})
	return c, err
}

var serverInstructions = delivery.ToolText("common/server-instructions", nil)

// serverFor builds the MCP server a Run sees: the tools its capabilities
// grant, each bound to it.
func (s *Server) serverFor(c Caller) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "dude", Version: "1"}, &mcp.ServerOptions{
		Instructions: serverInstructions,
	})
	for _, t := range c.tools() {
		if c.Child && !childToolAllowed(t.name) {
			continue
		}
		handler := func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			out, err := s.call(ctx, c, t, req.Params.Arguments)
			var refused refusal
			if errors.As(err, &refused) {
				// Said to the agent, which can do something about it; not
				// a failure of the server. The code, when the refusal has
				// one (lux's), is structured beside the words.
				res := &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: refused.Error()}}}
				if refused.code != "" {
					res.StructuredContent = map[string]string{"error": refused.Error(), "code": refused.code}
				}
				return res, nil
			}
			if err != nil {
				return nil, err
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(out)}},
				StructuredContent: structured(out)}, nil
		}
		srv.AddTool(&mcp.Tool{Name: t.name, Description: t.description, InputSchema: t.schema}, handler)
	}
	// The server is built for every request: descriptions made for the
	// Run (its machine's sizes) are read only when the tools are listed.
	srv.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			res, err := next(ctx, method, req)
			if list, ok := res.(*mcp.ListToolsResult); ok && err == nil {
				for i, listed := range list.Tools {
					if t, ok := c.findTool(listed.Name); ok && t.describeFor != nil {
						described := *listed
						described.Description = s.descriptionFor(ctx, c, t)
						list.Tools[i] = &described
					}
				}
			}
			return res, err
		}
	})
	return srv
}

// tool is one tool, whichever way it is called: whose definition it is, and
// what it does with JSON arguments, in the calling Run's organization.
type tool struct {
	name, description string
	// session: a session agent's own definition (brainstorm.go), answering
	// its name for a session agent in place of the task agents'.
	session bool
	schema  any
	run     func(ctx context.Context, tx pgx.Tx, c Caller, args json.RawMessage) (any, error)
	// perRun: how many calls a Run may make in its lifetime; 0, no limit
	// beyond the per-minute one.
	perRun int
	// prepare runs before the transaction opens, for slow work that must not
	// hold a connection (an embedding); its result is c.env.prepared.
	prepare func(ctx context.Context, c Caller, args json.RawMessage) any
	// outside, when set, splits the call around slow work that must hold
	// no lock (a GitHub post): run's transaction commits with its result,
	// outside does the work with no transaction open, and settle records
	// it in a second transaction, with the call's ledger event. An error
	// from either leaves the call unrecorded, as run's does.
	outside func(ctx context.Context, c Caller, checked any) (any, error)
	settle  func(ctx context.Context, tx pgx.Tx, c Caller, checked, done any) (any, error)
	// attempts: a call that passes run spends the budgets before outside
	// runs, so a call GitHub refuses still counts (see AttemptEventType).
	attempts bool
	// withhold: the call's ledger event keeps the result's size, never
	// the result (what people wrote on GitHub).
	withhold bool
	// describeFor, when set, is the description for the calling Run (its
	// own machine's sizes); "" or an error keeps description.
	describeFor func(ctx context.Context, tx pgx.Tx, c Caller) (string, error)
}

// describe gives the tool a description made for each calling Run.
func (t tool) describe(f func(ctx context.Context, tx pgx.Tx, c Caller) (string, error)) tool {
	t.describeFor = f
	return t
}

// descriptionFor is t's description as c sees it.
func (s *Server) descriptionFor(ctx context.Context, c Caller, t tool) string {
	if t.describeFor == nil {
		return t.description
	}
	c.env = env{lux: s.Lux}
	var out string
	if err := s.DB.InOrg(ctx, c.Org, func(tx pgx.Tx) (err error) {
		out, err = t.describeFor(ctx, tx, c)
		return err
	}); err != nil && s.Log != nil {
		s.Log.Warn("agent tool description failed", "tool", t.name, "run", c.RunID, "error", err)
	}
	if out == "" {
		return t.description
	}
	return out
}

// sessionAgent says role's tools are a session agent's: where it has its
// own definition of a name (brainstorm.go), that one answers it.
func sessionAgent(role string) bool { return role == delivery.RoleBrainstorm }

// allowed says an agent with grants, as role, is given t: its capabilities
// unlock t's name, and t is the definition role's audience uses.
func (t tool) allowed(role string, grants capabilities.Grants) bool {
	if !grants.Tool(t.name) {
		return false
	}
	if t.session {
		return sessionAgent(role)
	}
	return !sessionAgent(role) || !hasSessionDefinition(t.name)
}

// granted are the tools an agent with grants, as role, is given, in
// allTools' order.
func granted(role string, grants capabilities.Grants) []tool {
	var out []tool
	for _, t := range allTools() {
		if t.allowed(role, grants) {
			out = append(out, t)
		}
	}
	return out
}

func (c Caller) agent() capabilities.Agent {
	return capabilities.Agent{Role: c.Role, Org: c.Org, Project: c.ProjectID, Run: c.RunID, Session: c.SessionID}
}

func (c Caller) tools() []tool {
	if c.AutomationID != "" {
		create, _ := find("create_task", c.Role, c.grants)
		return append(append([]tool{create}, githubReadTools...), automationCommentTool, postToChatTool)
	}
	return granted(c.Role, c.grants)
}

func (c Caller) findTool(name string) (tool, bool) {
	if c.AutomationID == "" {
		return find(name, c.Role, c.grants)
	}
	for _, t := range c.tools() {
		if t.name == name {
			return t, true
		}
	}
	return tool{}, false
}

// renamed: tools' names before "work item" became "task". A Run already
// on lux when that shipped — parked ones resume days later — has the old
// dude CLI in its image, which calls them by these. (Over MCP an agent
// lists the tools afresh each session.)
var renamed = map[string]string{"list_work": "list_tasks", "create_work_item": "create_task"}

// find is the tool an agent with grants, as role, calls by name: a
// session's agent has its own tools under the names others use.
func find(name, role string, grants capabilities.Grants) (tool, bool) {
	if now, ok := renamed[name]; ok {
		name = now
	}
	for _, t := range allTools() {
		if t.name == name && t.allowed(role, grants) {
			return t, true
		}
	}
	return tool{}, false
}

// allTools are every role's tools: the task agents', then the session
// agent's.
func allTools() []tool {
	return slices.Concat(tools, prActionTools, baseSyncTools, githubReadTools, refreshTools, coordinationTools, feedbackTools, brainstormTools)
}

// hasSessionDefinition says a session agent has its own tool called name.
func hasSessionDefinition(name string) bool {
	return slices.ContainsFunc(allTools(), func(t tool) bool { return t.session && t.name == name })
}

// define makes a task agent's tool from a typed function: its input schema
// from In, its arguments decoded strictly into In. Its description and its
// parameters' are delivery/agents/tools/<name>.md's.
func define[In, Out any](name string,
	do func(ctx context.Context, tx pgx.Tx, c Caller, in In) (Out, error)) tool {
	return defineFor(name, false, do)
}

// defineSession is define for a session agent's own tool, whose texts are
// tools/session/<name>.md.
func defineSession[In, Out any](name string,
	do func(ctx context.Context, tx pgx.Tx, c Caller, in In) (Out, error)) tool {
	return defineFor(name, true, do)
}

func defineFor[In, Out any](name string, session bool,
	do func(ctx context.Context, tx pgx.Tx, c Caller, in In) (Out, error)) tool {
	schema, err := jsonschema.For[In](nil)
	if err != nil {
		panic(fmt.Sprintf("tool %s: %v", name, err))
	}
	// A session agent's tools share their names with the task agents', so
	// their texts are under session/.
	key := name
	if session {
		key = "session/" + name
	}
	describeParameters(key, schema)
	return tool{name: name, description: delivery.ToolText(key, toolTextData), session: session, schema: schema,
		run: func(ctx context.Context, tx pgx.Tx, c Caller, args json.RawMessage) (any, error) {
			var in In
			if len(args) > 0 && string(args) != "null" {
				dec := json.NewDecoder(bytes.NewReader(args))
				dec.DisallowUnknownFields()
				if err := dec.Decode(&in); err != nil {
					return nil, refuse(rArguments, err)
				}
			}
			return do(ctx, tx, c, in)
		}}
}

// toolTextData is what a tool's description may name: create_task's GoalMin.
var toolTextData = struct{ GoalMin int }{GoalMin}

// describeParameters gives schema's properties their descriptions from the
// tool's text, each by its path ("questions.header" for a field of the
// items of questions). A description naming no property panics: the
// parameter was renamed and its text left behind.
func describeParameters(key string, schema *jsonschema.Schema) {
	used := map[string]bool{}
	var walk func(prefix string, s *jsonschema.Schema)
	walk = func(prefix string, s *jsonschema.Schema) {
		if s == nil {
			return
		}
		walk(prefix, s.Items)
		for name, p := range s.Properties {
			path := prefix + name
			if text, ok := delivery.ToolParameter(key, path); ok {
				p.Description = text
				used[path] = true
			}
			walk(path+".", p)
		}
	}
	walk("", schema)
	var unused []string
	for _, path := range delivery.ToolParameters(key) {
		if !used[path] {
			unused = append(unused, path)
		}
	}
	if len(unused) > 0 {
		// Sorted: ToolParameters comes from a map, and the panic must name the same paths every run.
		slices.Sort(unused)
		panic(fmt.Sprintf("agents/tools/%s.md describes %s, which the tool has no parameter called",
			key, strings.Join(unused, ", ")))
	}
}

// limit caps how many calls a Run may make to the tool in its lifetime.
func (t tool) limit(perRun int) tool {
	t.perRun = perRun
	return t
}

// before gives the tool a step outside its transaction (see tool.prepare).
func (t tool) before(prepare func(ctx context.Context, c Caller, args json.RawMessage) any) tool {
	t.prepare = prepare
	return t
}

// countingAttempts makes a tool split by around spend its budgets on every
// checked call, whatever outside then answers.
func (t tool) countingAttempts() tool {
	t.attempts = true
	return t
}

// withholdingResult records the tool's calls without their results.
func (t tool) withholdingResult() tool {
	t.withhold = true
	return t
}

// around splits the tool's call around work done outside any transaction
// (see tool.outside): its run's result is Checked.
func around[Checked, Done, Out any](t tool, outside func(ctx context.Context, c Caller, checked Checked) (Done, error),
	settle func(ctx context.Context, tx pgx.Tx, c Caller, checked Checked, done Done) (Out, error)) tool {
	t.outside = func(ctx context.Context, c Caller, checked any) (any, error) {
		return outside(ctx, c, checked.(Checked))
	}
	t.settle = func(ctx context.Context, tx pgx.Tx, c Caller, checked, done any) (any, error) {
		return settle(ctx, tx, c, checked.(Checked), done.(Done))
	}
	return t
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
// transaction as what the tool did (for a tool split by outside, the
// transaction recording what it did outside).
func (s *Server) call(ctx context.Context, c Caller, t tool, args json.RawMessage) (outcome json.RawMessage, callErr error) {
	// Refusals roll back the tool transaction; automation feeds still need
	// an argument-bearing record for that call, including early refusals.
	// A refusal that wrote its own event (slack.message.refused) is named by
	// refusalEventId, so a reader lists that call once.
	var refusalID string
	defer func() {
		if c.AutomationID == "" || callErr == nil {
			return
		}
		// A call refused for the run's call budget writes nothing: the
		// budget would not bound those writes, as attempted records are not counted.
		var refused refusal
		isRefusal := errors.As(callErr, &refused)
		if isRefusal && refused.limited {
			return
		}
		// Only a refusal's own words reach the run's page; other errors carry database and internal text.
		said := "internal error"
		if isRefusal {
			said = callErr.Error()
		}
		if err := s.DB.InOrg(ctx, c.Org, func(tx pgx.Tx) error {
			input := args
			if len(input) == 0 {
				input = json.RawMessage("{}")
			}
			payload := map[string]any{"tool": t.name, "arguments": input, "error": said, "attempted": true}
			if refusalID != "" {
				payload["refusalEventId"] = refusalID
			}
			_, err := ledger.Append(ctx, tx, c.event(EventType, payload))
			return err
		}); err != nil {
			s.Log.Error("recording automation tool error", "error", err)
		}
	}()
	if err := s.DB.InOrg(ctx, c.Org, func(tx pgx.Tx) error {
		if c.AutomationID != "" {
			var live bool
			if err := tx.QueryRow(ctx, `SELECT status IN ('queued', 'running') AND ended_at IS NULL FROM automation_runs WHERE id=$1`, c.RunID).Scan(&live); err != nil {
				return err
			}
			if _, ok := c.findTool(t.name); !live || !ok {
				return refuse("automation run is no longer active or the tool is outside its scope")
			}
			return nil
		}
		var child bool
		if err := tx.QueryRow(ctx, `SELECT source_kind IS NOT NULL FROM runs WHERE id=$1`, c.RunID).Scan(&child); err != nil {
			return err
		}
		if child && !childToolAllowed(t.name) {
			return refuseText(rChildToolsReadOnly)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	var out json.RawMessage
	c.env = env{embedder: s.Embedder, kick: s.Kick, forges: s.Forges, lux: s.Lux, validateMachine: s.ValidateMachine,
		syncBase: s.SyncBase, syncBaseWait: cmp.Or(s.SyncBaseWait, syncBaseWait), syncPR: s.SyncPullRequest, publicURL: s.PublicURL,
		chat: s.Chat, chatProviders: s.ChatProviders, db: s.DB}
	if t.prepare != nil {
		c.env.prepared = t.prepare(ctx, c, args)
		// A prepare step may refuse the call before its arguments are redacted or counted.
		if refused, ok := c.env.prepared.(refusal); ok {
			refusalID = s.recordRefusal(ctx, c, t, refused)
			return nil, refused
		}
	}
	if s.BeforeCall != nil {
		s.BeforeCall(t.name)
	}
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	// Whatever a tool does with its arguments — a table, the ledger, a
	// pull request on GitHub, a peer — it does with the Run's secret values
	// redacted: lux redacts the agent's output stream, not its calls to dude.
	if err := s.DB.InOrg(ctx, c.Org, func(tx pgx.Tx) error {
		red, err := ledger.Redactor(ctx, tx, c.RunID)
		args = red.JSON(args)
		return err
	}); err != nil {
		return nil, err
	}
	// record keeps the call in the ledger, with its result, in tx. The
	// result of a call already counted by its attempt is marked attempted,
	// which withinLimits does not count again.
	record := func(tx pgx.Tx, result any) error {
		var err error
		if out, err = json.Marshal(result); err != nil {
			return err
		}
		// A large answer (a whole project's listing) is not copied into the
		// ledger on every call: its size is.
		recorded := any(out)
		switch {
		case t.withhold:
			recorded = map[string]any{"bytes": len(out), "withheld": true}
		case len(out) > resultInLedger:
			recorded = map[string]any{"bytes": len(out), "truncated": true}
		}
		payload := map[string]any{"tool": t.name, "arguments": args, "result": recorded}
		if t.attempts {
			payload["attempted"] = true
		}
		_, err = ledger.Append(ctx, tx, c.event(EventType, payload))
		return err
	}
	var checked any
	err := s.DB.InOrg(ctx, c.Org, func(tx pgx.Tx) error {
		if err := withinLimits(ctx, tx, c, t); err != nil {
			return err
		}
		result, err := t.run(ctx, tx, c, args)
		if err != nil {
			return err
		}
		if t.outside != nil {
			checked = result
			if t.attempts {
				// Again under the lock run took (the task's Chat for a PR
				// action), so simultaneous calls cannot both take the last one.
				if err := withinLimits(ctx, tx, c, t); err != nil {
					return err
				}
				if _, err := ledger.Append(ctx, tx, c.event(AttemptEventType, map[string]any{"tool": t.name, "arguments": args})); err != nil {
					return err
				}
				// The call's further GitHub reads: marked attempted, so
				// withinLimits counts the call once.
				if r, ok := result.(githubReader); ok {
					for range r.githubReads() - 1 {
						if _, err := ledger.Append(ctx, tx, c.event(AttemptEventType, map[string]any{"tool": t.name, "attempted": true})); err != nil {
							return err
						}
					}
				}
				return nil
			}
			return nil
		}
		return record(tx, result)
	})
	var refused refusal
	if errors.As(err, &refused) {
		refusalID = s.recordRefusal(ctx, c, t, refused)
	}
	if err != nil || t.outside == nil {
		if err == nil && (t.name == "start_agent" || t.name == "verify") && s.Kick != nil {
			s.Kick()
		}
		return out, err
	}
	done, err := t.outside(ctx, c, checked)
	if errors.As(err, &refused) && refused.event != nil {
		refusalID = s.recordRefusal(ctx, c, t, refused)
	}
	if err != nil {
		return nil, err
	}
	err = s.DB.InOrg(ctx, c.Org, func(tx pgx.Tx) error {
		result, err := t.settle(ctx, tx, c, checked, done)
		if err != nil {
			return err
		}
		return record(tx, result)
	})
	// What GitHub says now, without waiting for its webhook. A read that
	// fails is not the action failing: the webhook or the reconciler reads it later.
	if p, ok := checked.(delivery.PRActionCheck); ok && err == nil && s.SyncPullRequest != nil {
		if serr := s.SyncPullRequest(ctx, c.Org, p.PullRequestID()); serr != nil {
			s.Log.Warn("reading a pull request back after an action failed", "pr", p.PullRequestID(), "tool", t.name, "error", serr)
		}
	}
	return out, err
}

func childToolAllowed(name string) bool {
	for _, allowed := range delivery.ChildTools {
		if name == allowed {
			return true
		}
	}
	return false
}

// recordRefusal records what a refused call says of itself, once the
// call's own transaction is gone.
// It returns the id of the refusal's own event, "" for none.
func (s *Server) recordRefusal(ctx context.Context, c Caller, t tool, refused refusal) (eventID string) {
	if logged := s.DB.InOrg(ctx, c.Org, func(tx pgx.Tx) error {
		if refused.event != nil {
			var err error
			eventID, err = ledger.Append(ctx, tx, c.event(refused.event.typ, refused.event.payload))
			return err
		}
		if refused.blocked != nil {
			return delivery.RecordStartRefusedTx(ctx, tx, c.Org, c.TaskID, "start_phase", ledger.ActorAgent, c.RunID, refused.blocked.Blockers)
		}
		if refused.lifecycle != "" {
			return delivery.RecordLifecycleRefusedTx(ctx, tx, c.Org, c.TaskID, "start_phase", refused.lifecycle, ledger.ActorAgent, c.RunID)
		}
		return nil
	}); logged != nil {
		s.Log.Error("recording a refused call failed", "tool", t.name, "run", c.RunID, "error", logged)
		return ""
	}
	return eventID
}

// oldName is what a tool was called before, for the calls a Run made
// under it to count against its budget; else the name itself.
func oldName(tool string) string {
	for old, now := range renamed {
		if now == tool {
			return old
		}
	}
	return tool
}

// withinLimits refuses a call past the Run's budget: counted from the
// ledger, where every call is, once (its attempt, or its result).
func withinLimits(ctx context.Context, tx pgx.Tx, c Caller, t tool) error {
	tool := t.name
	recent, sameTool, err := callsMade(ctx, tx, c, tool)
	if err != nil {
		return err
	}
	limit := t.perRun
	switch {
	case recent >= callsPerMinute:
		return refuseLimited(rTooManyCallsAtMostMinute, callsPerMinute)
	case limit > 0 && sameTool >= limit:
		return refuseLimited(rLimitedCallsInRunThisRun, tool, limit)
	}
	return nil
}

// callsMade are the Run's tool calls in the last minute, and its calls of
// tool in its lifetime: counted from the ledger, where every call is, once
// (its attempt, or its result).
func callsMade(ctx context.Context, tx pgx.Tx, c Caller, tool string) (recent, sameTool int, err error) {
	err = tx.QueryRow(ctx, `SELECT
			count(*) FILTER (WHERE occurred_at > now() - interval '1 minute'),
			count(*) FILTER (WHERE payload->>'tool' = $3 OR payload->>'tool' = $4)
		FROM events WHERE `+c.runColumn()+` = $1 AND event_type IN ($2, $5) AND payload->>'attempted' IS NULL`,
		c.RunID, EventType, tool, oldName(tool), AttemptEventType).Scan(&recent, &sameTool)
	return recent, sameTool, err
}

// refusal is a tool declining a request, optionally recorded after rollback.
type refusal struct {
	msg     string
	code    string
	event   *refusedEvent
	blocked *delivery.BlockedError
	// A start refused as archived or closed: its reason.
	lifecycle string
	// Refused for the run's call budget (withinLimits and post_to_chat's own counts).
	limited bool
}

type refusedEvent struct {
	typ     string
	payload map[string]any
}

func (r refusal) Error() string { return r.msg }

func refuse(format string, a ...any) error { return refusal{msg: fmt.Sprintf(format, a...)} }

// refuseLimited is refuse for a call past the run's budget.
func refuseLimited(format string, a ...any) error {
	return refusal{msg: fmt.Sprintf(format, a...), limited: true}
}

// refuseRecorded is refuse with the event that records the refusal.
func refuseRecorded(typ string, payload map[string]any, msg string) error {
	return refusal{msg: msg, event: &refusedEvent{typ, payload}}
}

// structured is a tool's result as MCP structured content, which must be a
// JSON object: a list is given as {"items": [...]}. (The text content, and
// the CLI's JSON API, keep it as it is.)
func structured(out []byte) json.RawMessage {
	if trimmed := bytes.TrimSpace(out); len(trimmed) > 0 && trimmed[0] == '[' {
		wrapped, _ := json.Marshal(map[string]json.RawMessage{"items": trimmed})
		return wrapped
	}
	return out
}

// refuseText is refuse with a message that has no verbs to fill.
func refuseText(msg string) error { return refusal{msg: msg} }
