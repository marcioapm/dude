// Package api is the orchestrator's internal API.
//
// Not user-facing: the backend calls it, on a user's behalf, to change what
// runs. Authenticated with one shared service token, and every request names
// the organization it acts for — the backend has already authenticated the
// user and resolved their organization, and the orchestrator trusts it for
// that and nothing else. Row-level security still confines every query to
// the named organization.
package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/ids"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

type Server struct {
	DB *db.DB
	// Where artifacts' bytes are: dude records them, lux keeps them.
	Lux      lux.Client
	Workflow *workflow.Runtime
	Token    string
	Log      *slog.Logger
	// Wakes the loops after a change, so a person's action takes effect
	// without waiting for the next tick.
	Kick func()
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		if err := s.DB.Pool.Ping(r.Context()); err != nil {
			write(w, http.StatusServiceUnavailable, map[string]string{"status": "degraded", "error": err.Error()})
			return
		}
		write(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.Handle("POST /internal/work-items/{id}/deliver", s.auth(s.deliver))
	mux.Handle("POST /internal/runs/{id}/steer", s.auth(s.steer))
	mux.Handle("POST /internal/runs/{id}/pause", s.auth(s.pause))
	mux.Handle("POST /internal/runs/{id}/resume", s.auth(s.resume))
	mux.Handle("POST /internal/runs/{id}/abort", s.auth(s.abort))
	mux.Handle("POST /internal/questions/{id}/answer", s.auth(s.answer))
	mux.Handle("GET /internal/artifacts/{id}/content", s.auth(s.artifactContent))
	// The factory's delivery defaults, which the settings screen shows for
	// what a project leaves unset: one definition, here, where it is applied.
	mux.Handle("GET /internal/delivery-defaults", s.auth(func(w http.ResponseWriter, r *http.Request, _ string) error {
		write(w, http.StatusOK, delivery.DefaultPolicy())
		return nil
	}))
	mux.Handle("POST /internal/kick", s.auth(func(w http.ResponseWriter, r *http.Request, _ string) error {
		s.kick()
		write(w, http.StatusAccepted, map[string]bool{"ok": true})
		return nil
	}))
	return mux
}

type handler func(w http.ResponseWriter, r *http.Request, org string) error

// actor is who the backend says made the request.
func actor(r *http.Request) string {
	if a := r.Header.Get("X-Dude-Actor"); a != "" {
		return a
	}
	return "unknown"
}

// httpError is an error with a status for the caller.
type httpError struct {
	status  int
	code    string
	message string
}

func (e *httpError) Error() string { return e.message }

func fail(status int, code, format string, a ...any) error {
	return &httpError{status, code, fmt.Sprintf(format, a...)}
}

func (s *Server) auth(h handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if s.Token == "" || subtle.ConstantTimeCompare([]byte(token), []byte(s.Token)) != 1 {
			write(w, http.StatusUnauthorized, errBody("unauthorized", "missing or invalid service token"))
			return
		}
		org := r.Header.Get("X-Dude-Organization")
		if org == "" {
			write(w, http.StatusBadRequest, errBody("bad_request", "X-Dude-Organization is required"))
			return
		}
		if err := h(w, r, org); err != nil {
			var he *httpError
			if errors.As(err, &he) {
				write(w, he.status, errBody(he.code, he.message))
				return
			}
			if db.IsNotFound(err) {
				write(w, http.StatusNotFound, errBody("not_found", "not found"))
				return
			}
			s.Log.Error("internal request failed", "path", r.URL.Path, "error", err)
			write(w, http.StatusInternalServerError, errBody("internal", "internal error"))
		}
	})
}

func (s *Server) kick() {
	if s.Kick != nil {
		s.Kick()
	}
}

// deliver starts the delivery workflow for a work item. The work item is the
// idempotency key, so a second call joins the delivery already in flight.
func (s *Server) deliver(w http.ResponseWriter, r *http.Request, org string) error {
	workItemID := r.PathValue("id")
	var body struct {
		RepositoryID string          `json:"repositoryId"`
		Policy       json.RawMessage `json:"policy"`
		ActorID      string          `json:"actorId"`
	}
	if err := read(r, &body); err != nil {
		return err
	}
	var projectID, repositoryID, chosen string
	var projectPolicy []byte
	err := s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
		if err := tx.QueryRow(r.Context(), `SELECT w.project_id, p.delivery_policy, COALESCE(w.repository_id, '') FROM work_items w
			JOIN projects p ON p.id = w.project_id WHERE w.id = $1`, workItemID).Scan(&projectID, &projectPolicy, &chosen); err != nil {
			if db.IsNotFound(err) {
				return fail(http.StatusNotFound, "not_found", "work item %s not found", workItemID)
			}
			return err
		}
		// The repository asked for now, else the one the work item names.
		if body.RepositoryID == "" {
			body.RepositoryID = chosen
		}
		rows, err := tx.Query(r.Context(), `SELECT id FROM repositories WHERE project_id = $1
			AND ($2 = '' OR id = $2) ORDER BY name LIMIT 2`, projectID, body.RepositoryID)
		if err != nil {
			return err
		}
		repos, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		switch {
		case len(repos) == 0:
			return fail(http.StatusBadRequest, "bad_request", "this work item's project has no repository to deliver to")
		case len(repos) > 1:
			// Choosing for them would be a coin flip that lands in someone's
			// repository.
			return fail(http.StatusBadRequest, "bad_request", "this project has several repositories; name one with repositoryId")
		}
		repositoryID = repos[0]
		return nil
	})
	if err != nil {
		return err
	}

	// The factory's defaults, then the project's, then this work item's own:
	// each layer sets only what it names.
	policy := delivery.DefaultPolicy()
	if err := json.Unmarshal(projectPolicy, &policy); err != nil {
		return fmt.Errorf("project %s delivery policy: %w", projectID, err)
	}
	if len(body.Policy) > 0 {
		if err := json.Unmarshal(body.Policy, &policy); err != nil {
			return fail(http.StatusBadRequest, "bad_request", "policy: %v", err)
		}
	}
	var attempt int
	_ = s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(), `SELECT COALESCE(max(attempt), 1) FROM runs WHERE work_item_id = $1`, workItemID).Scan(&attempt)
	})
	id, dup, err := s.Workflow.Start(r.Context(), workflow.StartOptions{
		Type: delivery.WorkflowType, OrganizationID: org, IdempotencyKey: "delivery:" + workItemID,
		WorkItemID: workItemID,
		Input: delivery.State{WorkItemID: workItemID, ProjectID: projectID, RepositoryID: repositoryID,
			Policy: policy, Branch: delivery.BranchFor(workItemID, attempt)},
	})
	if err != nil {
		return err
	}
	s.kick()
	status := http.StatusCreated
	if dup {
		status = http.StatusOK
	}
	write(w, status, map[string]any{"workflowRunId": id, "workItemId": workItemID, "alreadyRunning": dup})
	return nil
}

// runInfo loads what every run-control action needs, confined to org.
type runInfo struct {
	ProjectID, WorkItemID, Status string
}

var liveStatuses = []string{"pending", "scheduled", "starting", "running", "paused"}

func loadRun(ctx context.Context, tx pgx.Tx, runID string) (runInfo, error) {
	var ri runInfo
	err := tx.QueryRow(ctx, `SELECT project_id, work_item_id, status::text FROM runs WHERE id = $1 FOR UPDATE`, runID).
		Scan(&ri.ProjectID, &ri.WorkItemID, &ri.Status)
	if db.IsNotFound(err) {
		return ri, fail(http.StatusNotFound, "not_found", "run %s not found", runID)
	}
	return ri, err
}

func isLive(status string) bool { return slices.Contains(liveStatuses, status) }

// insertDirective queues text for a Run's agent, delivered by the syncer
// and acknowledged by lux when the agent takes it.
func insertDirective(ctx context.Context, tx pgx.Tx, org, runID string, ri runInfo, text, scope, supersedes string,
	interrupt bool) (string, any, error) {
	id := ids.New(ids.Directive)
	var createdAt any
	err := tx.QueryRow(ctx, `INSERT INTO directives (id, organization_id, work_item_id, run_id, text, scope, supersedes, interrupt)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING created_at`,
		id, org, ri.WorkItemID, runID, text, scope, db.Nullable(supersedes), interrupt).Scan(&createdAt)
	return id, createdAt, err
}

func humanEvent(ctx context.Context, tx pgx.Tx, org, runID string, ri runInfo, typ, actor string, payload map[string]any) error {
	_, err := ledger.Append(ctx, tx, ledger.Event{
		Type: typ, OrganizationID: org, ProjectID: ri.ProjectID, WorkItemID: ri.WorkItemID, RunID: runID,
		ActorType: ledger.ActorHuman, ActorID: actor, Source: ledger.SourceOrchestrator,
		CorrelationID: ri.WorkItemID, Payload: payload,
	})
	return err
}

// steer records a directive; the phase syncer hands it to the agent. Durable
// either way: a Run steered while paused hears it when it resumes.
func (s *Server) steer(w http.ResponseWriter, r *http.Request, org string) error {
	runID := r.PathValue("id")
	var body struct {
		Text       string `json:"text"`
		Scope      string `json:"scope"`
		Supersedes string `json:"supersedes"`
		// Stop the agent's current turn so it hears this now. Otherwise an
		// agent that cannot take a message mid-turn hears it when the turn
		// ends.
		Interrupt bool `json:"interrupt"`
	}
	if err := read(r, &body); err != nil {
		return err
	}
	if strings.TrimSpace(body.Text) == "" {
		return fail(http.StatusBadRequest, "bad_request", "text is required")
	}
	if body.Scope == "" {
		body.Scope = "run"
	}
	var out map[string]any
	err := s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
		ri, err := loadRun(r.Context(), tx, runID)
		if err != nil {
			return err
		}
		if !isLive(ri.Status) {
			return fail(http.StatusConflict, "conflict", "run %s is %s and can no longer be steered", runID, ri.Status)
		}
		id, createdAt, err := insertDirective(r.Context(), tx, org, runID, ri, body.Text, body.Scope, body.Supersedes, body.Interrupt)
		if err != nil {
			return err
		}
		out = map[string]any{"id": id, "runId": runID, "workItemId": ri.WorkItemID, "text": body.Text,
			"scope": body.Scope, "supersedes": db.Nullable(body.Supersedes), "interrupt": body.Interrupt,
			"deliveredAt": nil, "createdAt": createdAt}
		return humanEvent(r.Context(), tx, org, runID, ri, "run.steered", actor(r), map[string]any{
			"directiveId": id, "text": body.Text, "scope": body.Scope, "supersedes": db.Nullable(body.Supersedes),
			"interrupt": body.Interrupt})
	})
	if err != nil {
		return err
	}
	s.kick()
	write(w, http.StatusCreated, out)
	return nil
}

// pause records the request; the Run becomes paused once lux has stopped it.
func (s *Server) pause(w http.ResponseWriter, r *http.Request, org string) error {
	runID := r.PathValue("id")
	var body struct{ Mode, Reason string }
	if err := read(r, &body); err != nil {
		return err
	}
	control := "pause_graceful"
	if body.Mode == "hard" {
		control = "pause_hard"
	}
	err := s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
		ri, err := loadRun(r.Context(), tx, runID)
		if err != nil {
			return err
		}
		switch {
		case ri.Status == "paused":
			return fail(http.StatusConflict, "conflict", "run %s is already paused", runID)
		case !isLive(ri.Status):
			return fail(http.StatusConflict, "conflict", "run %s is %s and cannot be paused", runID, ri.Status)
		}
		if _, err := tx.Exec(r.Context(), `UPDATE runs SET control = $2::run_control, control_requested_at = now(),
			control_reason = $3 WHERE id = $1`, runID, control, db.Nullable(body.Reason)); err != nil {
			return err
		}
		mode := body.Mode
		if mode == "" {
			mode = "graceful"
		}
		return humanEvent(r.Context(), tx, org, runID, ri, "run.paused", actor(r),
			map[string]any{"mode": mode, "requested": true, "reason": db.Nullable(body.Reason)})
	})
	if err != nil {
		return err
	}
	s.kick()
	write(w, http.StatusOK, map[string]any{"ok": true, "control": control, "pending": true})
	return nil
}

// resume continues a paused Run. The agent picks up its conversation from
// the transcript lux kept; nothing starts over.
func (s *Server) resume(w http.ResponseWriter, r *http.Request, org string) error {
	runID := r.PathValue("id")
	var body struct{ Reason string }
	if err := read(r, &body); err != nil {
		return err
	}
	err := s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
		ri, err := loadRun(r.Context(), tx, runID)
		if err != nil {
			return err
		}
		if ri.Status != "paused" {
			return fail(http.StatusConflict, "conflict", "run %s is %s, not paused", runID, ri.Status)
		}
		// A request, like pause and abort; the syncer resumes the lux Run
		// and moves it back to running.
		if _, err := tx.Exec(r.Context(), `UPDATE runs SET control = 'resume', control_requested_at = now(),
			control_reason = $2 WHERE id = $1`, runID, db.Nullable(body.Reason)); err != nil {
			return err
		}
		return humanEvent(r.Context(), tx, org, runID, ri, "run.resumed", actor(r), map[string]any{"reason": db.Nullable(body.Reason)})
	})
	if err != nil {
		return err
	}
	s.kick()
	write(w, http.StatusOK, map[string]any{"ok": true, "status": "paused", "resuming": true})
	return nil
}

// abort stops a Run and its work item at once. The lux Run is cancelled by
// the syncer; its events and workspace are kept — abort stops work, it does
// not erase it.
// answer gives an agent the answer to the question it stopped on. The
// answer is delivered the way a steer is — a directive, acknowledged by lux
// when the agent takes it — so it starts the agent's next turn.
func (s *Server) answer(w http.ResponseWriter, r *http.Request, org string) error {
	questionID := r.PathValue("id")
	var body struct {
		Text string `json:"text"`
	}
	if err := read(r, &body); err != nil {
		return err
	}
	if strings.TrimSpace(body.Text) == "" {
		return fail(http.StatusBadRequest, "bad_request", "text is required")
	}
	var out map[string]any
	err := s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
		var runID, status, prompt string
		if err := tx.QueryRow(r.Context(), `SELECT run_id, status::text, prompt FROM questions WHERE id = $1 FOR UPDATE`,
			questionID).Scan(&runID, &status, &prompt); err != nil {
			if db.IsNotFound(err) {
				return fail(http.StatusNotFound, "not_found", "question %s not found", questionID)
			}
			return err
		}
		if status != "open" {
			return fail(http.StatusConflict, "conflict", "question %s is already %s", questionID, status)
		}
		ri, err := loadRun(r.Context(), tx, runID)
		if err != nil {
			return err
		}
		if !isLive(ri.Status) {
			return fail(http.StatusConflict, "conflict", "run %s is %s and can no longer be answered", runID, ri.Status)
		}
		var answeredAt any
		if err := tx.QueryRow(r.Context(), `UPDATE questions SET status = 'answered', answer = $2, answered_at = now(),
			answered_by = (SELECT id FROM users WHERE id = $3) WHERE id = $1 RETURNING answered_at`,
			questionID, body.Text, actor(r)).Scan(&answeredAt); err != nil {
			return err
		}
		// Delivered as a steer is — queued until the agent takes it, which
		// starts its next turn — and quoting the question it settles.
		text := fmt.Sprintf("Answer to your question %q:\n\n%s", prompt, body.Text)
		directiveID, _, err := insertDirective(r.Context(), tx, org, runID, ri, text, "run", "", false)
		if err != nil {
			return err
		}
		if _, err := delivery.SetWorkItemStatusTx(r.Context(), tx, org, ri.ProjectID, ri.WorkItemID, "awaiting_input",
			"running", "a person answered the agent"); err != nil {
			return err
		}
		out = map[string]any{"id": questionID, "runId": runID, "status": "answered", "answer": body.Text, "answeredAt": answeredAt}
		return humanEvent(r.Context(), tx, org, runID, ri, "question.answered", actor(r),
			map[string]any{"questionId": questionID, "answer": body.Text, "directiveId": directiveID})
	})
	if err != nil {
		return err
	}
	s.kick()
	write(w, http.StatusOK, out)
	return nil
}

func (s *Server) abort(w http.ResponseWriter, r *http.Request, org string) error {
	runID := r.PathValue("id")
	var body struct{ Reason string }
	if err := read(r, &body); err != nil {
		return err
	}
	var workItemID string
	err := s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
		ri, err := loadRun(r.Context(), tx, runID)
		if err != nil {
			return err
		}
		if !isLive(ri.Status) {
			return fail(http.StatusConflict, "conflict", "run %s is already %s", runID, ri.Status)
		}
		workItemID = ri.WorkItemID
		if _, err := tx.Exec(r.Context(), `UPDATE runs SET status = 'aborted', control = 'abort', control_requested_at = now(),
			control_reason = $2, ended_at = now() WHERE id = $1`, runID, db.Nullable(body.Reason)); err != nil {
			return err
		}
		// The work item stops too: an aborted Run should not leave its work
		// item looking like it is still progressing.
		if _, err := tx.Exec(r.Context(), `UPDATE work_items SET status = 'aborted'
			WHERE id = $1 AND status NOT IN ('done', 'failed', 'aborted')`, ri.WorkItemID); err != nil {
			return err
		}
		return humanEvent(r.Context(), tx, org, runID, ri, "run.aborted", actor(r), map[string]any{"reason": db.Nullable(body.Reason)})
	})
	if err != nil {
		return err
	}
	// Its workflow stops with it, rather than waiting on a Run that will
	// never finish.
	var wfID string
	_ = s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
		err := tx.QueryRow(r.Context(), `SELECT id FROM workflow_runs WHERE work_item_id = $1 AND status IN ('running', 'waiting')`,
			workItemID).Scan(&wfID)
		if db.IsNotFound(err) {
			return nil
		}
		return err
	})
	if wfID != "" {
		_ = s.Workflow.Abort(r.Context(), org, wfID, "run "+runID+" aborted")
	}
	s.kick()
	write(w, http.StatusOK, map[string]any{"ok": true, "status": "aborted"})
	return nil
}

func read(r *http.Request, v any) error {
	if r.ContentLength == 0 {
		return nil
	}
	if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20)).Decode(v); err != nil {
		return fail(http.StatusBadRequest, "bad_request", "invalid JSON body: %v", err)
	}
	return nil
}

func write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// errBody matches the backend's error shape, so it can pass an orchestrator
// refusal straight through to the user.
func errBody(code, message string) map[string]any {
	return map[string]any{"error": map[string]string{"code": code, "message": message}}
}

// artifactContent streams an artifact's bytes from lux, as the agent wrote
// them. The artifact is looked up in the caller's organization, so an id
// from another one is simply not found.
func (s *Server) artifactContent(w http.ResponseWriter, r *http.Request, org string) error {
	var key, name, ctype, sum string
	var size int64
	if err := s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(), `SELECT storage_key, name, content_type, size_bytes, sha256 FROM artifacts WHERE id = $1`,
			r.PathValue("id")).Scan(&key, &name, &ctype, &size, &sum)
	}); err != nil {
		return err
	}
	body, err := s.Lux.Download(r.Context(), key)
	if err != nil {
		if le, ok := lux.AsError(err); ok {
			switch le.Status {
			case http.StatusGone, http.StatusNotFound:
				return fail(http.StatusGone, "gone", "%s is no longer kept", name)
			case http.StatusConflict:
				return fail(http.StatusConflict, "not_ready", "%s is still being uploaded", name)
			case 0:
				return fail(http.StatusServiceUnavailable, "unavailable", "lux, which keeps %s, is unreachable", name)
			}
		}
		return err
	}
	defer body.Close()
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	w.Header().Set("X-Content-SHA256", sum)
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, body)
	return nil
}
