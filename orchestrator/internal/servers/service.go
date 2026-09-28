package servers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// KindPreview is a branch preview's runs.kind; an agent's is "agent".
const KindPreview = "preview"

// Service answers for a task's servers: which Run a person sees, its
// servers as lux has them, and the actions a person takes on them.
type Service struct {
	DB  *db.DB
	Lux lux.Client
	// Where lux's console is, for the terminal link (LUX_CONSOLE_URL; lux's
	// API base URL by default).
	ConsoleURL string
	Log        *slog.Logger
	// Wakes the loops after a change, so a preview starts or stops now.
	Kick func()
}

// Error is a refusal with a status and a code for the caller.
type Error struct {
	Status        int
	Code, Message string
}

func (e *Error) Error() string { return e.Message }

func refuse(status int, code, format string, a ...any) error {
	return &Error{status, code, fmt.Sprintf(format, a...)}
}

// TaskServers is what a task's (or a Run's) Servers tab shows.
type TaskServers struct {
	Run     *RunView        `json:"run"`
	Servers []lux.Server    `json:"servers"`
	Moved   *Moved          `json:"moved"`
	Recipes json.RawMessage `json:"recipes"`
}

// RunView is the Run the servers are on.
type RunView struct {
	ID       string `json:"id"`
	LuxRunID string `json:"luxRunId"`
	Kind     string `json:"kind"`
	Label    string `json:"label"`
	// dude's status for it, and lux's state.
	State     string     `json:"state"`
	LuxState  string     `json:"luxState"`
	Host      *string    `json:"host"`
	StartedAt *time.Time `json:"startedAt"`
	StartedBy *PersonRef `json:"startedBy"`
	Branch    *string    `json:"branch"`
	Commit    *string    `json:"commit"`
	// A preview's: how far it is from serving, and how long it may go
	// unused before it is parked.
	PreviewStage      *string  `json:"previewStage"`
	ParksAfterMinutes *float64 `json:"parksAfterMinutes"`
	TerminalURL       *string  `json:"terminalUrl"`
}

type PersonRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Moved says a Run moved host and its servers stopped with the move.
type Moved struct {
	At       time.Time `json:"at"`
	FromHost *string   `json:"fromHost"`
	ToHost   *string   `json:"toHost"`
}

// runRow is what a Run's servers are read with.
type runRow struct {
	ID, ProjectID, TaskID, Kind, Status string
	Phase, LuxRunID, LuxState           string
	Branch                              string
	BaseSHAs                            map[string]string
	Repos                               []string
	StartedAt                           *time.Time
	StartedBy                           *PersonRef
	Settings                            PreviewSettings
	// The project's recipes with a setup step: a preview's server of that
	// name starting is its setup running, as far as dude can tell.
	WithSetup []string
}

const runSelect = `SELECT r.id, r.project_id, r.task_id, r.kind, r.status::text, COALESCE(r.phase::text, ''),
	COALESCE(r.lux_run_id, ''), COALESCE(r.lux_state, ''), COALESCE(r.branch, ''), r.base_shas, r.lux_repositories,
	r.started_at,
	(SELECT json_build_object('id', p.id, 'name', p.name) FROM people p WHERE p.id = CASE WHEN r.kind = 'preview'
		THEN r.started_by ELSE (SELECT k.person_id FROM tasks t JOIN api_keys k ON k.id = t.owner_key_id WHERE t.id = r.task_id) END),
	(SELECT preview_settings(pr) FROM projects pr WHERE pr.id = r.project_id),
	ARRAY(SELECT s.name FROM project_servers s WHERE s.project_id = r.project_id AND COALESCE(s.setup, '') <> '')
	FROM runs r`

func scanRun(row pgx.Row) (runRow, error) {
	var r runRow
	var settings []byte
	err := row.Scan(&r.ID, &r.ProjectID, &r.TaskID, &r.Kind, &r.Status, &r.Phase, &r.LuxRunID, &r.LuxState, &r.Branch,
		&r.BaseSHAs, &r.Repos, &r.StartedAt, &r.StartedBy, &settings, &r.WithSetup)
	if err == nil {
		err = json.Unmarshal(settings, &r.Settings)
	}
	return r, err
}

// liveAgent and livePreview (SQL, over runs r): a task's Run whose
// servers a person can use now, or soon.
const (
	liveAgent   = `r.kind = 'agent' AND r.status IN ('scheduled', 'starting', 'running') AND r.lux_run_id IS NOT NULL`
	livePreview = `r.kind = 'preview' AND r.status IN ('pending', 'scheduled', 'starting', 'running', 'paused')`
)

// taskRun is the Run a task shows servers for: the agent at work (the
// one that publishes, if several are — reviewers fan out beside it), else
// the task's live branch preview, else none.
func taskRun(ctx context.Context, tx pgx.Tx, taskID string) (*runRow, error) {
	r, err := scanRun(tx.QueryRow(ctx, runSelect+` WHERE r.task_id = $1 AND ((`+liveAgent+`) OR (`+livePreview+`))
		ORDER BY r.kind = 'agent' DESC, r.phase IN ('implement', 'fix', 'simplify') DESC, r.created_at DESC LIMIT 1`, taskID))
	if db.IsNotFound(err) {
		return nil, nil
	}
	return &r, err
}

func loadRun(ctx context.Context, tx pgx.Tx, runID string) (runRow, error) {
	r, err := scanRun(tx.QueryRow(ctx, runSelect+` WHERE r.id = $1`, runID))
	if db.IsNotFound(err) {
		return r, refuse(http.StatusNotFound, "not_found", "run %s not found", runID)
	}
	return r, err
}

// ForTask is a task's servers: its Run's, and the project's recipes.
func (s *Service) ForTask(ctx context.Context, org, taskID string) (TaskServers, error) {
	var run *runRow
	var recipes json.RawMessage
	err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		var projectID string
		if err := tx.QueryRow(ctx, `SELECT project_id FROM tasks WHERE id = $1`, taskID).Scan(&projectID); err != nil {
			if db.IsNotFound(err) {
				return refuse(http.StatusNotFound, "not_found", "task %s not found", taskID)
			}
			return err
		}
		var err error
		if run, err = taskRun(ctx, tx, taskID); err != nil {
			return err
		}
		recipes, err = recipesJSON(ctx, tx, projectID)
		return err
	})
	if err != nil {
		return TaskServers{}, err
	}
	return s.view(ctx, run, recipes), nil
}

// ForRun is one Run's servers, whatever its state.
func (s *Service) ForRun(ctx context.Context, org, runID string) (TaskServers, error) {
	var run runRow
	var recipes json.RawMessage
	err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		var err error
		if run, err = loadRun(ctx, tx, runID); err != nil {
			return err
		}
		recipes, err = recipesJSON(ctx, tx, run.ProjectID)
		return err
	})
	if err != nil {
		return TaskServers{}, err
	}
	return s.view(ctx, &run, recipes), nil
}

// view asks lux for the Run's servers and where it is, and puts the
// answer together. lux unreachable leaves the servers empty rather than
// failing the page: the Run is still dude's to show.
func (s *Service) view(ctx context.Context, r *runRow, recipes json.RawMessage) TaskServers {
	out := TaskServers{Servers: []lux.Server{}, Recipes: recipes}
	if r == nil {
		return out
	}
	v := &RunView{ID: r.ID, LuxRunID: r.LuxRunID, Kind: r.Kind, State: r.Status, LuxState: r.LuxState,
		StartedAt: r.StartedAt, StartedBy: r.StartedBy, Branch: nonEmpty(r.Branch)}
	out.Run = v
	if len(r.Repos) > 0 {
		v.Commit = nonEmpty(r.BaseSHAs[r.Repos[0]])
	}
	if r.Kind == KindPreview {
		v.Label = "Branch preview"
		v.ParksAfterMinutes = &r.Settings.IdleTimeoutMinutes
	} else {
		v.Label = phaseLabel(r.Phase) + " run"
	}
	var luxRun lux.Run
	if r.LuxRunID != "" {
		v.TerminalURL = nonEmpty(strings.TrimRight(s.ConsoleURL, "/") + "/runs/" + r.LuxRunID + "/terminal")
		var err error
		if luxRun, err = s.Lux.Get(ctx, r.LuxRunID); err == nil {
			v.LuxState = luxRun.State
			v.Host = nonEmpty(luxRun.Host)
		} else {
			s.Log.Debug("reading the run from lux", "run", r.ID, "error", err)
		}
		if list, err := s.Lux.Servers(ctx, r.LuxRunID); err == nil && list != nil {
			out.Servers = list
		} else if err != nil {
			s.Log.Debug("reading the run's servers from lux", "run", r.ID, "error", err)
		}
	}
	if r.Kind == KindPreview {
		v.PreviewStage = Stage(r.Status, v.LuxState, out.Servers, func(name string) bool { return slices.Contains(r.WithSetup, name) })
	}
	out.Moved = moved(out.Servers, luxRun)
	return out
}

// phaseLabel names a phase's agent as the app does (SETTINGS_ROLE_LABEL).
func phaseLabel(phase string) string {
	switch phase {
	case delivery.PhaseFix:
		return "Fixer"
	case delivery.PhaseTest:
		return "Tester"
	}
	if l, ok := delivery.RoleLabel[delivery.RoleForPhase[phase]]; ok {
		return l
	}
	return "Agent"
}

// Stage is how far a preview is from serving: scheduling (waiting for a
// host, or moving to another), cloning (its container starting: image,
// checkout), setup (a server that starts after a setup step is starting),
// starting (its servers are), ready (every server its spec starts is). Nil
// for a preview that is parked or over.
func Stage(status, luxState string, list []lux.Server, withSetup func(name string) bool) *string {
	stage := func(s string) *string { return &s }
	switch status {
	case "pending", "scheduled", "starting", "running":
	default:
		return nil
	}
	switch luxState {
	case "starting":
		return stage("cloning")
	case "running":
	default:
		// Not yet on a host, or moving to another.
		return stage("scheduling")
	}
	ready, setup := true, false
	for _, sv := range list {
		if !sv.FromSpec || sv.State == lux.ServerReady {
			continue
		}
		ready = false
		if sv.State == lux.ServerStarting && withSetup(sv.Name) {
			setup = true
		}
	}
	switch {
	case ready:
		return stage("ready")
	case setup:
		return stage("setup")
	}
	return stage("starting")
}

// moved: every server that was started stopped because the Run moved host,
// and the Run has started since on another placement — so a person is
// told why their servers are down, and that starting them serves from the
// new host.
func moved(list []lux.Server, run lux.Run) *Moved {
	var m *Moved
	fromEpoch := 0
	for _, sv := range list {
		if sv.StoppedEpoch == nil && sv.State == lux.ServerStopped {
			continue // never started: moving did not stop it
		}
		if sv.State != lux.ServerStopped || sv.StopReason == nil || *sv.StopReason != lux.StopMigrated {
			return nil
		}
		if m == nil {
			m = &Moved{}
		}
		if sv.Since != nil && sv.Since.After(m.At) {
			m.At = *sv.Since
		}
		if sv.StoppedEpoch != nil {
			fromEpoch = max(fromEpoch, *sv.StoppedEpoch)
		}
	}
	if m == nil || fromEpoch == 0 || run.Epoch <= fromEpoch {
		return nil
	}
	for _, p := range run.Placements {
		if p.Epoch == fromEpoch {
			m.FromHost = nonEmpty(p.HostName)
		}
	}
	m.ToHost = nonEmpty(run.Host)
	return m
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// luxError passes lux's refusal on as the caller's: its status and code,
// so "not_running" reaches the person as lux said it. lux unreachable is
// a 503.
func luxError(err error) error {
	le, ok := lux.AsError(err)
	if !ok {
		return err
	}
	if le.Status == 0 || le.Status >= 500 {
		return refuse(http.StatusServiceUnavailable, "lux_unavailable", "lux is unavailable: %s", le.Message)
	}
	return refuse(le.Status, le.Code, "%s", le.Message)
}

// writable loads a Run a person acts on the servers of: one lux has, and
// that is not over.
func (s *Service) writable(ctx context.Context, org, runID string) (runRow, error) {
	var r runRow
	err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) (err error) {
		r, err = loadRun(ctx, tx, runID)
		return err
	})
	if err != nil {
		return r, err
	}
	switch {
	case r.LuxRunID == "" && r.Kind == KindPreview && r.Status == "pending":
		return r, refuse(http.StatusConflict, "not_running", "the preview is still being submitted")
	case r.LuxRunID == "":
		return r, refuse(http.StatusConflict, "not_running", "run %s has not started on lux", runID)
	case slices.Contains([]string{"completed", "failed", "aborted"}, r.Status):
		return r, refuse(http.StatusConflict, "run_ended", "run %s is %s", runID, r.Status)
	}
	return r, nil
}

// parked: a preview dude parked for want of use, which a person starting
// a server wakes.
func (r runRow) parked() bool { return r.Kind == KindPreview && r.Status == "paused" }

// AddInput is what a person adds: one of the project's recipes by name,
// or a server of their own.
type AddInput struct {
	Recipe string `json:"recipe"`
	ManualServer
}

// Add adds a server to a Run: a recipe, as the project defines it, or one
// a person describes. Started at once when it has a command and the Run
// runs (lux's default).
func (s *Service) Add(ctx context.Context, org, runID string, in AddInput) (lux.Server, error) {
	r, err := s.writable(ctx, org, runID)
	if err != nil {
		return lux.Server{}, err
	}
	var input lux.ServerInput
	if err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		repo, err := primaryRepo(ctx, tx, r.Repos, r.ProjectID)
		if err != nil {
			return err
		}
		if in.Recipe == "" {
			if !ValidName(in.Name) {
				return refuse(http.StatusBadRequest, "bad_request", "a server's name is lowercase letters, digits and '-', at most 30, starting with a letter and not ending in '-'")
			}
			if in.Port < 1 || in.Port > 65535 {
				return refuse(http.StatusBadRequest, "bad_request", "port must be 1-65535")
			}
			if !ValidWorkdir(in.Workdir) {
				return refuse(http.StatusBadRequest, "bad_request", "workdir must be inside the checkout")
			}
			if input, err = in.ManualServer.Input(repo); err != nil {
				return refuse(http.StatusBadRequest, "bad_request", "%s", err.Error())
			}
			return nil
		}
		recipes, err := LoadRecipes(ctx, tx, r.ProjectID)
		if err != nil {
			return err
		}
		i := slices.IndexFunc(recipes, func(x Recipe) bool { return x.Name == in.Recipe })
		if i < 0 {
			return refuse(http.StatusNotFound, "not_found", "the project has no server %q", in.Recipe)
		}
		input = recipes[i].Input(repo)
		return nil
	}); err != nil {
		return lux.Server{}, err
	}
	if r.parked() {
		// Added now (lux takes it on a stopped Run), started once the
		// preview runs again.
		no := false
		input.Start = &no
	}
	sv, err := s.Lux.AddServer(ctx, r.LuxRunID, input)
	if err != nil {
		return sv, luxError(err)
	}
	if r.parked() && input.Command != nil {
		return sv, s.wake(ctx, org, r, []string{input.Name})
	}
	return sv, nil
}

// Action starts, stops or restarts one of a Run's servers. Starting one on
// a parked preview wakes it: the server starts once it runs.
func (s *Service) Action(ctx context.Context, org, runID, name, action string) (lux.Server, error) {
	r, err := s.writable(ctx, org, runID)
	if err != nil {
		return lux.Server{}, err
	}
	if r.parked() {
		if action == "stop" {
			return s.server(ctx, r, name)
		}
		sv, err := s.server(ctx, r, name)
		if err != nil {
			return sv, err
		}
		return sv, s.wake(ctx, org, r, []string{name})
	}
	sv, err := s.Lux.ServerAction(ctx, r.LuxRunID, name, action)
	return sv, luxError(err)
}

// server is one of a Run's servers as lux has it, or a 404.
func (s *Service) server(ctx context.Context, r runRow, name string) (lux.Server, error) {
	list, err := s.Lux.Servers(ctx, r.LuxRunID)
	if err != nil {
		return lux.Server{}, luxError(err)
	}
	for _, sv := range list {
		if sv.Name == name {
			return sv, nil
		}
	}
	return lux.Server{}, refuse(http.StatusNotFound, "not_found", "run %s has no server %q", r.ID, name)
}

// Remove removes a server from a Run (lux stops it first).
func (s *Service) Remove(ctx context.Context, org, runID, name string) error {
	r, err := s.writable(ctx, org, runID)
	if err != nil {
		return err
	}
	if err := s.Lux.RemoveServer(ctx, r.LuxRunID, name); err != nil {
		return luxError(err)
	}
	if r.parked() {
		// Nothing to start once it wakes.
		return s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE runs SET pending_starts = array_remove(pending_starts, $2) WHERE id = $1`, r.ID, name)
			return err
		})
	}
	return nil
}

// All starts every server with a command that is not serving (or stops
// every one that is not stopped). Each is its own request to lux; the
// first refusal ends it, saying which.
func (s *Service) All(ctx context.Context, org, runID, action string) (TaskServers, error) {
	r, err := s.writable(ctx, org, runID)
	if err != nil {
		return TaskServers{}, err
	}
	list, err := s.Lux.Servers(ctx, r.LuxRunID)
	if err != nil {
		return TaskServers{}, luxError(err)
	}
	var names []string
	for _, sv := range list {
		switch {
		case action == "start" && len(sv.Command) > 0 && sv.State != lux.ServerReady && sv.State != lux.ServerStarting:
			names = append(names, sv.Name)
		case action == "stop" && sv.State != lux.ServerStopped:
			names = append(names, sv.Name)
		}
	}
	switch {
	case r.parked() && action == "start" && len(names) > 0:
		err = s.wake(ctx, org, r, names)
	case r.parked():
	default:
		for _, name := range names {
			if _, err = s.Lux.ServerAction(ctx, r.LuxRunID, name, action); err != nil {
				err = luxError(err)
				var e *Error
				if errors.As(err, &e) {
					e.Message = name + ": " + e.Message
				}
				break
			}
		}
	}
	if err != nil {
		return TaskServers{}, err
	}
	return s.ForRun(ctx, org, runID)
}

// ServerLog is a server's recent output, as lux answers it.
func (s *Service) ServerLog(ctx context.Context, org, runID, name string, tail int) (json.RawMessage, error) {
	var r runRow
	if err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) (err error) {
		r, err = loadRun(ctx, tx, runID)
		return err
	}); err != nil {
		return nil, err
	}
	if r.LuxRunID == "" {
		return json.RawMessage(`{"lines":[]}`), nil
	}
	out, err := s.Lux.ServerLog(ctx, r.LuxRunID, name, tail)
	return out, luxError(err)
}

func (s *Service) kick() {
	if s.Kick != nil {
		s.Kick()
	}
}
