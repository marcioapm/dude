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
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/embeddings"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
	"github.com/marciomartins/dude/orchestrator/internal/llm"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
	"github.com/marciomartins/dude/orchestrator/internal/memory"
	"github.com/marciomartins/dude/orchestrator/internal/phases"
	"github.com/marciomartins/dude/orchestrator/internal/prs"
	"github.com/marciomartins/dude/orchestrator/internal/servers"
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
	// The Web Push keys browsers subscribe with (notify.Notifier.Keys).
	PushKeys func(context.Context) (public, private string, err error)
	// GitHub, for the actions a person takes on a pull request, and the
	// pull request sync that reads one back after.
	Forges delivery.Forges
	PRs    *prs.Syncer
	// A task's servers and branch preview, through lux.
	Servers *servers.Service
	// Search by meaning for the memory pages; nil, by words alone.
	Embedder embeddings.Embedder
	// The indexer: how the embedder is doing, and ending its wait when a
	// person asks to retry.
	Indexer interface {
		Health() memory.Health
		Resume()
	}
	// GitHub's answers to who could review, kept a minute.
	candidates candidateCache
	// The LLM proxy, for the Models page's suggestions and test messages.
	LLM llm.Client
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
	mux.Handle("POST /internal/tasks/{id}/deliver", s.auth(s.deliver))
	mux.Handle("POST /internal/tasks/{id}/chat", s.auth(s.chat))
	mux.Handle("POST /internal/runs/{id}/steer", s.auth(s.steer))
	mux.Handle("POST /internal/runs/{id}/pause", s.auth(s.pause))
	mux.Handle("POST /internal/runs/{id}/resume", s.auth(s.resume))
	mux.Handle("POST /internal/runs/{id}/abort", s.auth(s.abort))
	mux.Handle("POST /internal/questions/{id}/answer", s.auth(s.answer))
	mux.Handle("GET /internal/artifacts/{id}/content", s.auth(s.artifactContent))
	mux.Handle("POST /internal/tasks/{id}/done", s.auth(s.markDone))
	mux.Handle("POST /internal/tasks/{id}/decide", s.auth(s.decideEscalation))
	mux.Handle("POST /internal/repository-requests/{id}/decide", s.auth(s.decideRepositoryRequest))
	// The factory's delivery defaults, which the settings screen shows for
	// what a project leaves unset: one definition, here, where it is applied.
	mux.Handle("GET /internal/push/key", s.auth(func(w http.ResponseWriter, r *http.Request, _ string) error {
		if s.PushKeys == nil {
			return fail(http.StatusNotFound, "not_found", "notifications are not set up")
		}
		public, _, err := s.PushKeys(r.Context())
		if err != nil {
			return err
		}
		write(w, http.StatusOK, map[string]string{"publicKey": public})
		return nil
	}))
	mux.Handle("GET /internal/delivery-defaults", s.auth(func(w http.ResponseWriter, r *http.Request, _ string) error {
		write(w, http.StatusOK, delivery.DefaultPolicy())
		return nil
	}))
	// lux's pools, for the machine sizes page and its fit check. Never an
	// error: lux out of reach is no pools and why, and sizes still work.
	mux.Handle("GET /internal/lux/pools", s.auth(func(w http.ResponseWriter, r *http.Request, _ string) error {
		write(w, http.StatusOK, s.pools(r.Context()))
		return nil
	}))
	s.githubRoutes(mux)
	s.serverRoutes(mux)
	s.memoryRoutes(mux)
	s.llmRoutes(mux)
	// dude's own prompt for each role: what an organization that never
	// edits runs, and where its first edit starts from.
	mux.Handle("GET /internal/prompts/builtin", s.auth(func(w http.ResponseWriter, r *http.Request, _ string) error {
		out := map[string]string{}
		for _, role := range delivery.PromptRoles {
			out[role] = delivery.BuiltinPrompt(role)
		}
		write(w, http.StatusOK, out)
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

// Human identity is resolved against current organization membership; identity-free
// service requests carry no person or admin authority.
func actor(r *http.Request) string {
	return principalOf(r).Actor
}

type principal struct {
	Actor, Person, ActorType string
	Admin                    bool
}

type principalContextKey struct{}

func principalOf(r *http.Request) principal {
	p, _ := r.Context().Value(principalContextKey{}).(principal)
	return p
}

func (s *Server) resolvePrincipal(r *http.Request, org string) (principal, error) {
	p := principal{Actor: r.Header.Get("X-Dude-Actor"), Person: r.Header.Get("X-Dude-Person"), ActorType: ledger.ActorHuman}
	kind := r.Header.Get("X-Dude-Credential-Kind")
	if p.Actor == "" && p.Person == "" && kind == "" {
		return principal{Actor: "unknown", ActorType: ledger.ActorSystem}, nil
	}
	if kind != "" && kind != "api_key" && kind != "person" {
		return p, fail(http.StatusForbidden, "invalid_principal", "invalid credential kind")
	}
	err := s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
		var role, personID string
		var err error
		if kind == "person" {
			if p.Person == "" || p.Actor != p.Person {
				return fail(http.StatusForbidden, "invalid_principal", "invalid person actor")
			}
			p.ActorType = "person"
			err = tx.QueryRow(r.Context(), `SELECT id, role FROM people
				WHERE id = $1 AND organization_id = $2 AND removed_at IS NULL`, p.Person, org).Scan(&personID, &role)
		} else {
			err = tx.QueryRow(r.Context(), `SELECT p.id, p.role FROM api_keys k JOIN people p ON p.id = k.person_id
				WHERE k.id = $1 AND k.organization_id = $2 AND p.organization_id = $2
				AND k.kind = 'user' AND k.revoked_at IS NULL AND p.removed_at IS NULL`, p.Actor, org).Scan(&personID, &role)
		}
		if db.IsNotFound(err) || err == nil && p.Person != "" && p.Person != personID {
			return fail(http.StatusForbidden, "invalid_principal", "principal is not an active organization member")
		}
		if err != nil {
			return err
		}
		p.Person, p.Admin = personID, role == "admin"
		return nil
	})
	return p, err
}

// split reads a comma-separated query value.
func split(v string) []string {
	var out []string
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
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
		p, err := s.resolvePrincipal(r, org)
		if err == nil {
			r = r.WithContext(context.WithValue(r.Context(), principalContextKey{}, p))
			err = h(w, r, org)
		}
		if err != nil {
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

// deliver starts the delivery workflow for a task. The task is the
// idempotency key, so a second call joins the delivery already in flight.
func (s *Server) deliver(w http.ResponseWriter, r *http.Request, org string) error {
	taskID := r.PathValue("id")
	var body struct {
		Policy  json.RawMessage `json:"policy"`
		ActorID string          `json:"actorId"`
	}
	if err := read(r, &body); err != nil {
		return err
	}
	var projectID string
	var orgPolicy, projectPolicy []byte
	err := s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
		if err := tx.QueryRow(r.Context(), `SELECT w.project_id, o.delivery_policy, p.delivery_policy FROM tasks w
			JOIN projects p ON p.id = w.project_id JOIN organizations o ON o.id = p.organization_id
			WHERE w.id = $1`, taskID).Scan(&projectID, &orgPolicy, &projectPolicy); err != nil {
			if db.IsNotFound(err) {
				return fail(http.StatusNotFound, "not_found", "task %s not found", taskID)
			}
			return err
		}
		// Which repositories it works on is the task's to say: none is
		// work that changes no code — unless its project has just one.
		return delivery.NameOnlyRepository(r.Context(), tx, taskID)
	})
	if err != nil {
		return err
	}

	// The factory's defaults, then the organization's, then the project's,
	// then this task's own: each layer sets only what it names.
	policy, err := delivery.ResolvePolicy(orgPolicy, projectPolicy)
	if err != nil {
		return fmt.Errorf("project %s: %w", projectID, err)
	}
	if len(body.Policy) > 0 {
		if err := json.Unmarshal(body.Policy, &policy); err != nil {
			return fail(http.StatusBadRequest, "bad_request", "policy: %v", err)
		}
	}
	var attempt int
	_ = s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(), `SELECT COALESCE(max(attempt), 1) FROM runs WHERE task_id = $1`, taskID).Scan(&attempt)
	})
	id, dup, err := s.Workflow.Start(r.Context(), workflow.StartOptions{
		Type: delivery.WorkflowType, OrganizationID: org, IdempotencyKey: "delivery:" + taskID,
		TaskID: taskID,
		Input: delivery.State{TaskID: taskID, ProjectID: projectID,
			Policy: policy, Branch: delivery.BranchFor(taskID, attempt)},
	})
	if err != nil {
		return err
	}
	s.kick()
	status := http.StatusCreated
	if dup {
		status = http.StatusOK
	}
	write(w, status, map[string]any{"workflowRunId": id, "taskId": taskID, "alreadyRunning": dup})
	return nil
}

// runInfo loads what every run-control action needs, confined to org.
type runInfo struct {
	ProjectID, TaskID, Status string
	// dude paused it itself (runs.dude_pause), and would resume it on its own.
	DudePaused bool
	// Its agent role; "conductor" for the task's conductor.
	Role string
}

var liveStatuses = []string{"pending", "scheduled", "starting", "running", "paused"}

func loadRun(ctx context.Context, tx pgx.Tx, runID string) (runInfo, error) {
	var ri runInfo
	var kind string
	err := tx.QueryRow(ctx, `SELECT project_id, task_id, status::text, dude_pause IS NOT NULL, kind, COALESCE(role::text, '')
		FROM runs WHERE id = $1 FOR UPDATE`, runID).
		Scan(&ri.ProjectID, &ri.TaskID, &ri.Status, &ri.DudePaused, &kind, &ri.Role)
	if db.IsNotFound(err) {
		return ri, fail(http.StatusNotFound, "not_found", "run %s not found", runID)
	}
	if err == nil && kind != "agent" {
		// A branch preview has no agent to steer or answer, and is stopped
		// as the task's preview (DELETE /v1/tasks/{id}/preview), not
		// aborted — which would abort its task.
		return ri, fail(http.StatusConflict, "not_an_agent", "run %s is a branch preview, not an agent's", runID)
	}
	return ri, err
}

func isLive(status string) bool { return slices.Contains(liveStatuses, status) }

// stillOpen refuses to settle an ask (a question, a repository request) that
// is no longer open, saying why: one whose Run ended — cancelled — would be
// heard by nobody.
func stillOpen(what, id, status, open string) error {
	switch status {
	case open:
		return nil
	case "cancelled":
		return fail(http.StatusConflict, "no_longer_relevant", "%s %s is no longer relevant: the run that asked ended", what, id)
	}
	return fail(http.StatusConflict, "conflict", "%s %s is already %s", what, id, status)
}

// insertDirective queues text for a Run's agent (delivery.QueueDirective).
func insertDirective(ctx context.Context, tx pgx.Tx, org, runID string, ri runInfo, text, scope, supersedes string,
	interrupt bool) (string, time.Time, error) {
	return delivery.QueueDirective(ctx, tx, delivery.RunRef{Org: org, ProjectID: ri.ProjectID, TaskID: ri.TaskID, RunID: runID},
		delivery.Directive{Text: text, Scope: scope, Supersedes: supersedes, Interrupt: interrupt})
}

// ownerOnly uses the first active ordered member, independent of credentials.
func ownerOnly(ctx context.Context, tx pgx.Tx, taskID, personID, verb string) error {
	var ownerID, ownerName *string
	if err := tx.QueryRow(ctx, `SELECT owner.id, owner.name FROM tasks t
		LEFT JOIN LATERAL (SELECT p.id, p.name FROM task_people tp JOIN people p ON p.id = tp.person_id
			WHERE tp.task_id = t.id AND p.removed_at IS NULL
			ORDER BY tp.position, tp.person_id LIMIT 1) owner ON true
		WHERE t.id = $1`, taskID).Scan(&ownerID, &ownerName); err != nil {
		return err
	}
	if ownerID == nil || *ownerID == personID {
		return nil
	}
	return fail(http.StatusForbidden, "not_owner", "only %s can %s this — reassign the task to %s it", *ownerName, verb, verb)
}

func humanEvent(ctx context.Context, tx pgx.Tx, org, runID string, ri runInfo, typ string, p principal, payload map[string]any) error {
	_, err := ledger.Append(ctx, tx, ledger.Event{
		Type: typ, OrganizationID: org, ProjectID: ri.ProjectID, TaskID: ri.TaskID, RunID: runID,
		ActorType: p.ActorType, ActorID: p.Actor, Source: ledger.SourceOrchestrator,
		CorrelationID: ri.TaskID, Payload: payload,
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
		out = map[string]any{"id": id, "runId": runID, "taskId": ri.TaskID, "text": body.Text,
			"scope": body.Scope, "supersedes": db.Nullable(body.Supersedes), "interrupt": body.Interrupt,
			"deliveredAt": nil, "createdAt": createdAt}
		return humanEvent(r.Context(), tx, org, runID, ri, "run.steered", principalOf(r), map[string]any{
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
		case ri.Status == "paused" && !ri.DudePaused:
			return fail(http.StatusConflict, "conflict", "run %s is already paused", runID)
		case !isLive(ri.Status):
			return fail(http.StatusConflict, "conflict", "run %s is %s and cannot be paused", runID, ri.Status)
		}
		// A person's pause is theirs: dude does not resume it on its own,
		// even one it had made itself (to bring a repository, to park it) —
		// pausing a Run dude already stopped just makes it theirs.
		if ri.Status == "paused" {
			control = "none"
		}
		if _, err := tx.Exec(r.Context(), `UPDATE runs SET control = $2::run_control,
			control_requested_at = CASE WHEN $2 = 'none' THEN control_requested_at ELSE now() END,
			control_reason = $3, dude_pause = NULL WHERE id = $1`, runID, control, db.Nullable(body.Reason)); err != nil {
			return err
		}
		mode := body.Mode
		if mode == "" {
			mode = "graceful"
		}
		return humanEvent(r.Context(), tx, org, runID, ri, "run.paused", principalOf(r),
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
		// and moves it back to running. When it was asked is what a
		// resume's timing counts from (run_resumes.woken_at).
		var requestedAt time.Time
		if err := tx.QueryRow(r.Context(), `UPDATE runs SET control = 'resume', control_requested_at = now(),
			control_reason = $2 WHERE id = $1 RETURNING control_requested_at`, runID, db.Nullable(body.Reason)).Scan(&requestedAt); err != nil {
			return err
		}
		return humanEvent(r.Context(), tx, org, runID, ri, "run.resumed", principalOf(r),
			map[string]any{"reason": db.Nullable(body.Reason), "requestedAt": requestedAt})
	})
	if err != nil {
		return err
	}
	s.kick()
	write(w, http.StatusOK, map[string]any{"ok": true, "status": "paused", "resuming": true})
	return nil
}

// abort stops a Run and its task at once. The lux Run is cancelled by
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
		if err := stillOpen("question", questionID, status, "open"); err != nil {
			return err
		}
		ri, err := loadRun(r.Context(), tx, runID)
		if err != nil {
			return err
		}
		if !isLive(ri.Status) {
			return fail(http.StatusConflict, "conflict", "run %s is %s and can no longer be answered", runID, ri.Status)
		}
		if err := ownerOnly(r.Context(), tx, ri.TaskID, principalOf(r).Person, "answer"); err != nil {
			return err
		}
		var priorStatus string
		if err := tx.QueryRow(r.Context(), `SELECT COALESCE((SELECT e.payload->>'taskStatus' FROM events e
			WHERE e.run_id = $1 AND e.event_type = 'question.asked' AND e.payload->>'questionId' = $2 LIMIT 1), '')`,
			runID, questionID).Scan(&priorStatus); err != nil {
			return err
		}
		ref := delivery.RunRef{Org: org, ProjectID: ri.ProjectID, TaskID: ri.TaskID, RunID: runID}
		directiveID, err := answerQuestion(r.Context(), tx, ref, ri, questionID, prompt, priorStatus, body.Text, actor(r))
		if err != nil {
			return err
		}
		var answeredAt any
		if err := tx.QueryRow(r.Context(), `SELECT answered_at FROM questions WHERE id = $1`, questionID).Scan(&answeredAt); err != nil {
			return err
		}
		out = map[string]any{"id": questionID, "runId": runID, "status": "answered", "answer": body.Text, "answeredAt": answeredAt}
		return humanEvent(r.Context(), tx, org, runID, ri, "question.answered", principalOf(r),
			map[string]any{"questionId": questionID, "answer": body.Text, "directiveId": directiveID})
	})
	if err != nil {
		return err
	}
	s.kick()
	write(w, http.StatusOK, out)
	return nil
}

// answerQuestion settles an open question with a person's answer, queued
// for its agent as a steer is — which starts its next turn — quoting the
// question it settles; and takes the task off waiting on a person: back to
// running, or for a conductor's question to the status it had before it
// asked (priorStatus), since a conductor changes nothing about the task.
func answerQuestion(ctx context.Context, tx pgx.Tx, ref delivery.RunRef, ri runInfo, questionID, prompt, priorStatus, text, by string) (string, error) {
	if _, err := tx.Exec(ctx, `UPDATE questions SET status = 'answered', answer = $2, answered_at = now(),
		answered_by = (SELECT id FROM users WHERE id = $3) WHERE id = $1`, questionID, text, by); err != nil {
		return "", err
	}
	directiveID, _, err := delivery.QueueDirective(ctx, tx, ref, delivery.Directive{
		Text: fmt.Sprintf("Answer to your question %q:\n\n%s", prompt, text), Scope: "run"})
	if err != nil {
		return "", err
	}
	back := "running"
	if ri.Role == delivery.RoleConductor && priorStatus != "" {
		back = priorStatus
	}
	_, err = delivery.SetTaskStatusTx(ctx, tx, ref.Org, ref.ProjectID, ref.TaskID, "awaiting_input", back, "a person answered the agent")
	return directiveID, err
}

func (s *Server) abort(w http.ResponseWriter, r *http.Request, org string) error {
	runID := r.PathValue("id")
	var body struct{ Reason string }
	if err := read(r, &body); err != nil {
		return err
	}
	var taskID string
	conductor := false
	err := s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
		ri, err := loadRun(r.Context(), tx, runID)
		if err != nil {
			return err
		}
		if !isLive(ri.Status) {
			return fail(http.StatusConflict, "conflict", "run %s is already %s", runID, ri.Status)
		}
		taskID = ri.TaskID
		if _, err := tx.Exec(r.Context(), `UPDATE runs SET status = 'aborted', control = 'abort', control_requested_at = now(),
			control_reason = $2, ended_at = now() WHERE id = $1`, runID, db.Nullable(body.Reason)); err != nil {
			return err
		}
		if ri.Role == delivery.RoleConductor {
			// The task's conductor changes nothing about the task, nor does
			// stopping it: the next message in Chat starts another.
			conductor = true
			return humanEvent(r.Context(), tx, org, runID, ri, "run.aborted", principalOf(r), map[string]any{"reason": db.Nullable(body.Reason)})
		}
		// The task stops too: an aborted Run should not leave its work
		// item looking like it is still progressing.
		// Through the one way statuses change, so it is recorded like any
		// other (what time and cost count from).
		if _, err := delivery.SetTaskStatusTx(r.Context(), tx, org, ri.ProjectID, ri.TaskID, "", "aborted",
			"a person aborted a run"); err != nil {
			return err
		}
		return humanEvent(r.Context(), tx, org, runID, ri, "run.aborted", principalOf(r), map[string]any{"reason": db.Nullable(body.Reason)})
	})
	if err != nil {
		return err
	}
	if conductor {
		s.kick()
		write(w, http.StatusOK, map[string]any{"ok": true, "status": "aborted"})
		return nil
	}
	// Its workflow stops with it, rather than waiting on a Run that will
	// never finish.
	var wfID string
	_ = s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
		err := tx.QueryRow(r.Context(), `SELECT id FROM workflow_runs WHERE task_id = $1 AND status IN ('running', 'waiting')`,
			taskID).Scan(&wfID)
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

// markDone: a person says work with nothing to merge is finished — a
// write-up read, a design accepted. Only a task in review whose
// delivery has ended (nothing left to merge) can be marked done; one with
// open pull requests is done when they are merged.
func (s *Server) markDone(w http.ResponseWriter, r *http.Request, org string) error {
	id := r.PathValue("id")
	err := s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
		var projectID, status string
		var open bool
		if err := tx.QueryRow(r.Context(), `SELECT w.project_id, w.status::text,
				EXISTS (SELECT 1 FROM workflow_runs d WHERE d.task_id = w.id AND d.status IN ('running', 'waiting'))
			FROM tasks w WHERE w.id = $1 FOR UPDATE`, id).Scan(&projectID, &status, &open); err != nil {
			if db.IsNotFound(err) {
				return fail(http.StatusNotFound, "not_found", "task %s not found", id)
			}
			return err
		}
		if status != "review" || open {
			return fail(http.StatusConflict, "conflict", "task %s is %s; only finished work waiting to be read can be marked done", id, status)
		}
		_, err := delivery.SetTaskStatusTx(r.Context(), tx, org, projectID, id, "review", "done", "marked done by "+actor(r))
		return err
	})
	if err != nil {
		return err
	}
	write(w, http.StatusOK, map[string]any{"taskId": id, "status": "done"})
	return nil
}

// decideEscalation: a task's owner answers delivery stopping for them —
// try the step again, accept the findings a review got stuck on, take what
// was merged, wait on the rest, or stop (delivery.Actions says which fit
// the reason). A note becomes one of the task's decisions, which every
// agent from then on is told.
func (s *Server) decideEscalation(w http.ResponseWriter, r *http.Request, org string) error {
	taskID := r.PathValue("id")
	var body struct {
		Action string `json:"action"`
		Note   string `json:"note"`
	}
	if err := read(r, &body); err != nil {
		return err
	}
	var wfID string
	err := s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
		var projectID, status string
		var wf struct {
			ID, Status, Step *string
			State            []byte
		}
		err := tx.QueryRow(r.Context(), `SELECT t.project_id, t.status::text, d.id, d.status::text, d.step, d.state
			FROM tasks t LEFT JOIN LATERAL (SELECT * FROM workflow_runs d WHERE d.task_id = t.id
			  ORDER BY d.created_at DESC LIMIT 1) d ON true
			WHERE t.id = $1 FOR UPDATE OF t`, taskID).Scan(&projectID, &status, &wf.ID, &wf.Status, &wf.Step, &wf.State)
		if db.IsNotFound(err) {
			return fail(http.StatusNotFound, "not_found", "task %s not found", taskID)
		}
		if err != nil {
			return err
		}
		var st delivery.State
		if wf.State != nil {
			_ = json.Unmarshal(wf.State, &st)
		}
		e := st.Escalation
		if status != "awaiting_input" || wf.ID == nil || e == nil || e.Decided != nil {
			return fail(http.StatusConflict, "conflict", "delivery of task %s is not waiting for a decision", taskID)
		}
		wfID = *wf.ID
		if *wf.Status == "completed" && e.Step == "" {
			// Stopped before a stop waited for a decision: it ended there,
			// and never said which step to go back to. Its step says, where
			// trying again from it can work: a pull request fix from then
			// kept none of the feedback it was fixing.
			if step := delivery.RetryStep[*wf.Step]; step != "prFix" {
				e.Step = step
			}
		}
		if !slices.Contains(e.Actions(), body.Action) {
			return fail(http.StatusBadRequest, "bad_request", "%q is not a way to go on after %s; one of %s",
				body.Action, e.Reason, strings.Join(e.Actions(), ", "))
		}
		if err := ownerOnly(r.Context(), tx, taskID, principalOf(r).Person, "decide"); err != nil {
			return err
		}
		// Taken now, in the workflow's own state: a second decision is
		// refused from here on, before the workflow has acted on this one.
		// One that ended (from before decisions) is reopened to wait for it.
		e.Decided = &delivery.HumanDecision{Action: body.Action, Note: strings.TrimSpace(body.Note)}
		next, _ := json.Marshal(st)
		if _, err := tx.Exec(r.Context(), `UPDATE workflow_runs SET state = $2::jsonb, status = CASE WHEN status = 'completed'
				THEN 'waiting'::workflow_run_status ELSE status END, step = 'decide',
				awaiting_signals = '["human.decision"]'::jsonb, wake_at = NULL, last_error = NULL
			WHERE id = $1`, wfID, next); err != nil {
			return err
		}
		if n := e.Decided.Note; n != "" {
			if err := delivery.RecordDecisionTx(r.Context(), tx, org, taskID,
				"Delivery stopped ("+strings.ReplaceAll(e.Reason, "_", " ")+"). How should it go on?", n); err != nil {
				return err
			}
		}
		return humanEvent(r.Context(), tx, org, "", runInfo{ProjectID: projectID, TaskID: taskID}, "task.decided", principalOf(r),
			map[string]any{"reason": e.Reason, "action": body.Action, "note": e.Decided.Note})
	})
	if err != nil {
		return err
	}
	// Wakes the workflow to carry it out; the decision is in its state.
	if err := s.Workflow.Signal(r.Context(), org, wfID, delivery.SignalHumanDecision, nil, ""); err != nil {
		return err
	}
	s.kick()
	write(w, http.StatusOK, map[string]any{"taskId": taskID, "action": body.Action})
	return nil
}

// decideRepositoryRequest: a person approves or denies an agent's request
// for another repository. Approved, the repository joins the task and
// the Run is paused, to be resumed with it cloned (the syncer does both);
// denied, the agent is told. Either way it is recorded.
func (s *Server) decideRepositoryRequest(w http.ResponseWriter, r *http.Request, org string) error {
	id := r.PathValue("id")
	var body struct {
		Approve bool   `json:"approve"`
		Note    string `json:"note"`
	}
	if err := read(r, &body); err != nil {
		return err
	}
	var out map[string]any
	err := s.DB.InOrg(r.Context(), org, func(tx pgx.Tx) error {
		var runID string
		if err := tx.QueryRow(r.Context(), `SELECT run_id FROM repository_requests WHERE id = $1`, id).Scan(&runID); err != nil {
			if db.IsNotFound(err) {
				return fail(http.StatusNotFound, "not_found", "repository request %s not found", id)
			}
			return err
		}
		// The Run first, then the request: the order a Run ending takes them
		// in (its trigger cancels pending requests), so the two never deadlock.
		ri, err := loadRun(r.Context(), tx, runID)
		if err != nil {
			return err
		}
		var taskID, repoID, repoName, access, status string
		if err := tx.QueryRow(r.Context(), `SELECT q.task_id, q.repository_id, repo.name, q.access::text, q.status::text
			FROM repository_requests q JOIN repositories repo ON repo.id = q.repository_id WHERE q.id = $1 FOR UPDATE OF q`, id).
			Scan(&taskID, &repoID, &repoName, &access, &status); err != nil {
			return err
		}
		if err := stillOpen("repository request", id, status, "pending"); err != nil {
			return err
		}
		if err := ownerOnly(r.Context(), tx, taskID, principalOf(r).Person, "decide"); err != nil {
			return err
		}
		decision := "denied"
		if body.Approve {
			decision = "approved"
		}
		if _, err := tx.Exec(r.Context(), `UPDATE repository_requests SET status = $2::repository_request_status,
			decided_by = $3, decided_at = now() WHERE id = $1`, id, decision, actor(r)); err != nil {
			return err
		}
		if body.Approve {
			// The task works on it from now on — every later phase gets it too.
			if _, err := tx.Exec(r.Context(), `INSERT INTO task_repositories (organization_id, task_id, repository_id, access)
				VALUES ($1, $2, $3, $4::repository_access) ON CONFLICT (task_id, repository_id) DO NOTHING`,
				org, taskID, repoID, access); err != nil {
				return err
			}
			// The lux Run has it already (a person took it off the task
			// and back): nothing to bring, so it is settled now — nothing
			// would ever clone it.
			tag, err := tx.Exec(r.Context(), `UPDATE repository_requests q SET status = 'cloned'
				FROM runs run WHERE q.id = $1 AND run.id = q.run_id AND $2 = ANY (run.lux_repositories)`, id, repoName)
			if err != nil {
				return err
			}
			if tag.RowsAffected() > 0 {
				// Told, as a steer is: that starts the turn it may be waiting
				// for, and is the input a parked one is resumed with.
				text := fmt.Sprintf("A person approved your request: %s is already checked out at %s. Carry on.", repoName, phases.RepoPath(repoName))
				if _, _, err := insertDirective(r.Context(), tx, org, runID, ri, text, "run", "", false); err != nil {
					return err
				}
			}
		} else {
			// Told as a steer is, so the agent hears it on its next turn.
			text := fmt.Sprintf("Your request for %s was declined.", repoName)
			if n := strings.TrimSpace(body.Note); n != "" {
				text += " " + n
			}
			if _, _, err := insertDirective(r.Context(), tx, org, runID, ri, text, "run", "", false); err != nil {
				return err
			}
		}
		out = map[string]any{"id": id, "status": decision}
		return humanEvent(r.Context(), tx, org, runID, ri, "repository."+decision, principalOf(r),
			map[string]any{"requestId": id, "repository": repoName, "access": access, "note": body.Note})
	})
	if err != nil {
		return err
	}
	s.kick()
	write(w, http.StatusOK, out)
	return nil
}
