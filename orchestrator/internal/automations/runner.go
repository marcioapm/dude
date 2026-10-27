package automations

// The automations loop: schedules due, deliveries to match, queued runs to
// start, live runs to follow, Server containers to feed and stop, kept
// sessions to expire. Each pass does a bounded amount of each.

import (
	"bytes"
	"cmp"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/agenttools"
	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/ids"
	"github.com/marciomartins/dude/orchestrator/internal/images"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
	"github.com/marciomartins/dude/orchestrator/internal/phases"
	"github.com/marciomartins/dude/orchestrator/internal/registry"
)

// Server-kind and webhook bounds (Q3, Q16).
const (
	ServerIdle      = 30 * time.Second
	ServerHealthMax = 60 * time.Second
	RequestTimeout  = 30 * time.Second
	// How long a run waits for its kept session's lux Run to stop (resume
	// answering 409) before that Run is ended and the run starts fresh.
	ResumeConflictMax = 2 * time.Minute
)

// EventPath is where a command's or server's container finds the event(s).
const EventPath = "/workspace/.dude/event.json"

// Runner carries automations out.
type Runner struct {
	DB     *db.DB
	Lux    lux.Client
	Forges Forges
	Agent  phases.AgentConfig
	// The registry library images are pulled from; nil for none.
	Registry registry.Provider
	Log      *slog.Logger
	// HTTP posts to Server containers and webhook URLs.
	HTTP *http.Client
	// ServerURL is where a Server run's container answers, from its lux
	// Run and server; nil asks lux for the server's url.
	ServerURL func(ctx context.Context, luxRunID string, port int) (string, error)
	Now       func() time.Time
	// Workers bounds the runs followed at once, and separately the webhooks
	// POSTed at once, off the sweep; zero is DefaultWorkers.
	Workers int
	// FollowLimit bounds the webhook runs, and separately the other running
	// runs, read per pass to follow; zero is DefaultFollowLimit.
	FollowLimit int

	mu       sync.Mutex
	inflight map[string]bool
	pools    map[string]chan struct{}
	wg       sync.WaitGroup
	// The preview sign-in of each Server run's lux Run, as a Cookie header.
	previewCookies map[string]string
	// The last sign-in attempt per lux Run, and whether lux has taken a
	// cookie since (signInOrLog's backoff).
	previewTries map[string]previewTry
}

// previewTry is a lux Run's last preview sign-in attempt.
type previewTry struct {
	at       time.Time
	accepted bool
}

// SignInBackoff is how long after a preview sign-in another is not tried
// for the same lux Run while lux has taken no cookie since: a lux preview
// not in ticket mode (cloudflare-access) answers every request with its
// sign-in page, and would otherwise cost a ticket per request.
const SignInBackoff = 30 * time.Second

// DefaultWorkers is how many runs are followed, and how many webhooks
// POSTed, at once.
const DefaultWorkers = 16

// DefaultFollowLimit is how many running runs of each pool a pass reads.
const DefaultFollowLimit = 50

// Worker pools: a webhook's one-shot POST never waits for a slot held by a
// long-lived agent's follower.
const (
	poolFollow = "follow"
	poolPost   = "post"
)

// dispatch runs fn for key in the background, at most one at a time per key
// and Workers in all per pool. It says false, running nothing, while key is
// already in flight or every worker of pool is busy: the next pass tries again.
func (r *Runner) dispatch(ctx context.Context, pool, key string, fn func(context.Context)) bool {
	r.mu.Lock()
	if r.inflight == nil {
		r.inflight = map[string]bool{}
		r.pools = map[string]chan struct{}{}
	}
	if r.inflight[key] {
		r.mu.Unlock()
		return false
	}
	slots := r.pools[pool]
	if slots == nil {
		slots = make(chan struct{}, cmp.Or(r.Workers, DefaultWorkers))
		r.pools[pool] = slots
	}
	select {
	case slots <- struct{}{}:
	default:
		r.mu.Unlock()
		return false
	}
	r.inflight[key] = true
	r.wg.Add(1)
	r.mu.Unlock()
	go func() {
		defer func() {
			r.mu.Lock()
			delete(r.inflight, key)
			<-slots
			r.mu.Unlock()
			r.wg.Done()
		}()
		fn(ctx)
	}()
	return true
}

// Wait returns when the background work dispatch started has ended.
func (r *Runner) Wait() { r.wg.Wait() }

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Runner) client() *http.Client {
	if r.HTTP != nil {
		return r.HTTP
	}
	return &http.Client{Timeout: RequestTimeout}
}

// Automation is one automation as the runner reads it.
type Automation struct {
	ID, Org, ProjectID, Name, Kind string
	Enabled                        bool
	OwnerID                        string
	OwnerHeld                      bool
	Trigger                        Trigger
	SessionMode                    string
	ExpiryDays                     int
	Concurrency                    string
	ParallelMax                    int
	Repos                          json.RawMessage
	RepoFilter                     *RepoFilter
	Agent                          json.RawMessage
	MachineSizeID, ImageID         string
	SetupScript, Command           string
	Server                         json.RawMessage
	Webhook                        json.RawMessage
	WebhookSecret                  string
	// [{source: organization|project, name}] (migration 210); runs get
	// what automation_secrets_given resolves them to.
	Secrets         json.RawMessage
	NotifyOnFailure bool
	NextRunAt       *time.Time
	// An organisation incoming automation's projects (migration 211).
	ProjectIDs []string
}

func (a Automation) policy() Policy {
	return Policy{Kind: a.Kind, Concurrency: a.Concurrency, SessionMode: a.SessionMode, ParallelMax: a.ParallelMax}
}

const automationColumns = `a.id, a.organization_id, COALESCE(a.project_id, ''), a.name, a.kind, a.enabled,
	COALESCE(a.owner_id, ''), a.owner_left_at IS NOT NULL, a.trigger, a.session_mode, COALESCE(a.expiry_days, 0),
	a.concurrency, a.parallel_max, a.repos, a.repo_filter, COALESCE(a.agent, 'null'), COALESCE(a.machine_size_id, ''),
	COALESCE(a.image_id, ''), COALESCE(a.setup_script, ''), COALESCE(a.command, ''), COALESCE(a.server, 'null'),
	COALESCE(a.webhook, 'null'), COALESCE(a.webhook_secret, ''), a.secrets, a.notify_on_failure, a.next_run_at, a.project_ids`

func scanAutomation(row pgx.Row) (Automation, error) {
	var a Automation
	var trigger, filter []byte
	err := row.Scan(&a.ID, &a.Org, &a.ProjectID, &a.Name, &a.Kind, &a.Enabled, &a.OwnerID, &a.OwnerHeld, &trigger,
		&a.SessionMode, &a.ExpiryDays, &a.Concurrency, &a.ParallelMax, &a.Repos, &filter, &a.Agent, &a.MachineSizeID,
		&a.ImageID, &a.SetupScript, &a.Command, &a.Server, &a.Webhook, &a.WebhookSecret, &a.Secrets, &a.NotifyOnFailure, &a.NextRunAt,
		&a.ProjectIDs)
	if err != nil {
		return a, err
	}
	if err := json.Unmarshal(trigger, &a.Trigger); err != nil {
		// An empty trigger matches no event and has no schedule.
		slog.Warn("automation trigger unreadable; it fires on nothing", "automation", a.ID, "error", err)
		a.Trigger = Trigger{}
	}
	if len(filter) > 0 && string(filter) != "null" {
		a.RepoFilter = &RepoFilter{}
		if err := json.Unmarshal(filter, a.RepoFilter); err != nil || !knownFilterMode(a.RepoFilter.Mode) {
			// Never "every repository" by accident: an unreadable filter
			// covers none.
			slog.Warn("automation repository filter unreadable; it covers no repository", "automation", a.ID, "error", err)
			a.RepoFilter = &RepoFilter{Mode: "only"}
		}
	}
	return a, nil
}

func knownFilterMode(m string) bool { return m == "all" || m == "only" || m == "except" }

// Load reads one automation in its organisation.
func Load(ctx context.Context, tx pgx.Tx, id string) (Automation, error) {
	return scanAutomation(tx.QueryRow(ctx, `SELECT `+automationColumns+` FROM automations a WHERE a.id = $1`, id))
}

// Payload is one event a run was given.
type Payload struct {
	DeliveryID string          `json:"deliveryId"`
	Event      string          `json:"event"`
	Action     string          `json:"action"`
	Body       json.RawMessage `json:"body"`
	// An incoming delivery's group (incoming.go); a live run's group is its first payload's.
	GroupKey string `json:"groupKey,omitempty"`
}

// Sweep is one pass of the loop. Returns how much it did, so the loop runs
// again at once while there is more.
// A failing step is logged and the next one still runs: one organisation's
// trouble must not stop the others' runs.
func (r *Runner) Sweep(ctx context.Context) (int, error) {
	n := 0
	var errs []error
	for _, step := range []struct {
		name string
		do   func(context.Context) (int, error)
	}{{"schedules", r.fireSchedules}, {"deliveries", r.matchDeliveries}, {"incoming", r.matchIncoming}, {"start", r.startQueued},
		{"follow", r.followRunning}, {"expiry", r.expireSessions}} {
		did, err := step.do(ctx)
		n += did
		if err != nil {
			r.Log.Warn("an automations step failed", "step", step.name, "error", err)
			errs = append(errs, fmt.Errorf("%s: %w", step.name, err))
		}
	}
	return n, errors.Join(errs...)
}

// ---------------------------------------------------------------------------
// Firing
// ---------------------------------------------------------------------------

// Fired is what one firing did: the run that holds it, and the lux Runs of
// the runs it cancelled (Concurrency: cancel), to end once it is committed.
type Fired struct {
	Run       string
	Cancelled []Cancelled
	// What Arrive decided, for an incoming delivery's verdict.
	Action Action
}

// Cancelled is a cancelled run's lux Run; Keep says it is the automation's
// kept session, stopped (not ended) so the next run resumes it.
type Cancelled struct {
	LuxRunID string
	Keep     bool
}

// Fire records one firing of an automation and decides what it does
// (Arrive), in the automation's transaction: a queued, merged, skipped or
// sent run row. A run to start is left queued for startQueued. The caller
// ends the cancelled runs' lux Runs after commit (Runner.EndCancelled).
func Fire(ctx context.Context, tx pgx.Tx, a Automation, source string, payloads []Payload, startedBy string, scheduledFor *time.Time) (Fired, error) {
	// One firing at a time per automation: two deliveries arriving together
	// must see each other's run.
	if _, err := tx.Exec(ctx, `SELECT 1 FROM automations WHERE id = $1 FOR UPDATE`, a.ID); err != nil {
		return Fired{}, err
	}
	live, err := liveOf(ctx, tx, a.ID)
	if err != nil {
		return Fired{}, err
	}
	d := Arrive(a.policy(), source, live)
	raw, _ := json.Marshal(nonNil(payloads))
	fired := Fired{Action: d.Action}
	switch d.Action {
	case MergeInto, SendInto:
		_, err := tx.Exec(ctx, `UPDATE automation_runs SET payloads = payloads || $2::jsonb WHERE id = $1`, d.Into, raw)
		return Fired{Run: d.Into, Action: d.Action}, err
	case Skip:
		id := ids.New(ids.AutomationRun)
		_, err := tx.Exec(ctx, `INSERT INTO automation_runs (id, organization_id, automation_id, status, reason, source, payloads,
			started_by, scheduled_for, ended_at) VALUES ($1, $2, $3, 'skipped', $4, $5, $6, NULLIF($7, ''), $8, now())`,
			id, a.Org, a.ID, d.Reason, source, raw, startedBy, scheduledFor)
		return Fired{Run: id, Action: d.Action}, err
	case CancelAndStart:
		rows, err := tx.Query(ctx, `UPDATE automation_runs SET status = 'cancelled', reason = 'a newer event restarted it (Concurrency: cancel)',
			ended_at = now() WHERE automation_id = $1 AND status IN ('queued', 'running') AND source <> 'test'
			RETURNING COALESCE(lux_run_id, ''), COALESCE(session, '') IN ('new', 'resumed')`, a.ID)
		if err != nil {
			return Fired{}, err
		}
		var c Cancelled
		if _, err := pgx.ForEachRow(rows, []any{&c.LuxRunID, &c.Keep}, func() error {
			if c.LuxRunID != "" {
				fired.Cancelled = append(fired.Cancelled, c)
			}
			return nil
		}); err != nil {
			return Fired{}, err
		}
	}
	fired.Run = ids.New(ids.AutomationRun)
	reason := ""
	if d.Action == Queue {
		reason = "waiting for the run before it"
	}
	_, err = tx.Exec(ctx, `INSERT INTO automation_runs (id, organization_id, automation_id, status, reason, source, payloads,
		started_by, scheduled_for) VALUES ($1, $2, $3, 'queued', NULLIF($4, ''), $5, $6, NULLIF($7, ''), $8)`,
		fired.Run, a.Org, a.ID, reason, source, raw, startedBy, scheduledFor)
	return fired, err
}

// EndCancelled ends the lux Runs of runs a firing cancelled: a kept
// session's is stopped, so the new run resumes it; any other's is ended.
func (r *Runner) EndCancelled(ctx context.Context, cancelled []Cancelled) {
	for _, c := range cancelled {
		r.stopOrCancel(ctx, c.LuxRunID, c.Keep)
	}
}

func nonNil(p []Payload) []Payload {
	if p == nil {
		return []Payload{}
	}
	return p
}

// liveOf is what the automation has running and waiting, test runs aside
// (they share nothing with the rest, Q21).
func liveOf(ctx context.Context, tx pgx.Tx, automationID string) (Live, error) {
	rows, err := tx.Query(ctx, `SELECT id, status FROM automation_runs
		WHERE automation_id = $1 AND status IN ('queued', 'running') AND source <> 'test' ORDER BY created_at, id`, automationID)
	if err != nil {
		return Live{}, err
	}
	var live Live
	var id, status string
	_, err = pgx.ForEachRow(rows, []any{&id, &status}, func() error {
		if status == "running" {
			live.Running = append(live.Running, id)
		} else {
			live.Queued = id
		}
		return nil
	})
	return live, err
}

// fireSchedules fires every enabled schedule whose time has come, and sets
// the next time of any that has none.
func (r *Runner) fireSchedules(ctx context.Context) (int, error) {
	now := r.now()
	var due []Automation
	if err := r.DB.InSystem(ctx, "automations", func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+automationColumns+` FROM automations a
			WHERE a.enabled AND a.trigger->>'kind' = 'schedule' AND (a.next_run_at IS NULL OR a.next_run_at <= $1)
			ORDER BY a.next_run_at NULLS FIRST LIMIT 50`, now)
		if err != nil {
			return err
		}
		due, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (Automation, error) { return scanAutomation(row) })
		return err
	}); err != nil {
		return 0, err
	}
	n := 0
	var errs []error
	for _, a := range due {
		cron, err := ParseCron(a.Trigger.Cron)
		loc, lerr := time.LoadLocation(a.Trigger.TZ)
		if err != nil || lerr != nil {
			r.Log.Warn("automation schedule unreadable", "automation", a.ID, "cron", a.Trigger.Cron, "tz", a.Trigger.TZ)
			continue
		}
		var fired Fired
		if err := r.DB.InOrg(ctx, a.Org, func(tx pgx.Tx) (err error) {
			if a.NextRunAt != nil {
				at := *a.NextRunAt
				if fired, err = Fire(ctx, tx, a, SourceSchedule, nil, "", &at); err != nil {
					return err
				}
				n++
			}
			_, err = tx.Exec(ctx, `UPDATE automations SET next_run_at = $2 WHERE id = $1`, a.ID, cron.Next(now, loc))
			return err
		}); err != nil {
			errs = append(errs, fmt.Errorf("automation %s: %w", a.ID, err))
			continue
		}
		r.EndCancelled(ctx, fired.Cancelled)
	}
	return n, errors.Join(errs...)
}

// MatchAttempts is how many passes a delivery whose matching fails is tried
// before the automations it could not be matched to get a skipped run.
const MatchAttempts = 5

// matchBackoff is the wait before a failed delivery's next try: 30 s,
// doubling, at most 10 minutes.
func matchBackoff(attempts int) time.Duration {
	d := 30 * time.Second << min(attempts, 5)
	return min(d, 10*time.Minute)
}

type pendingDelivery struct {
	ID, Org, Event string
	Payload        json.RawMessage
	Attempts       int
	Pending        []string
}

// matchDeliveries reads stored deliveries the matcher has not, and fires
// every enabled event automation they match. Each delivery is its own
// transaction; one that fails is backed off, never blocking the rest.
func (r *Runner) matchDeliveries(ctx context.Context) (int, error) {
	var batch []pendingDelivery
	if err := r.DB.InSystem(ctx, "automations", func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id, organization_id, event, payload, automations_attempts, automations_pending
			FROM webhook_deliveries WHERE automations_at IS NULL AND (automations_next_at IS NULL OR automations_next_at <= $1)
			ORDER BY received_at LIMIT 50`, r.now())
		if err != nil {
			return err
		}
		batch, err = pgx.CollectRows(rows, pgx.RowToStructByPos[pendingDelivery])
		return err
	}); err != nil {
		return 0, err
	}
	var errs []error
	for _, d := range batch {
		if err := r.matchDelivery(ctx, d); err != nil {
			// A database error: the whole delivery is tried again later.
			r.Log.Warn("matching a delivery failed", "delivery", d.ID, "attempt", d.Attempts+1, "error", err)
			last := d.Attempts+1 >= MatchAttempts
			if berr := r.DB.InOrg(ctx, d.Org, func(tx pgx.Tx) error {
				if last {
					// Dropped after its last try: each automation it was
					// still to be matched against says so in its Runs. Under
					// a savepoint: the fault that failed the match may refuse
					// these too, and the delivery is marked read regardless.
					sp, spErr := tx.Begin(ctx)
					if spErr != nil {
						return spErr
					}
					if serr := skipUnmatched(ctx, sp, d, "it could not be matched: "+err.Error()); serr != nil {
						r.Log.Warn("recording a dropped delivery's skipped runs failed", "delivery", d.ID, "error", serr)
						if rerr := sp.Rollback(ctx); rerr != nil {
							return rerr
						}
					} else if cerr := sp.Commit(ctx); cerr != nil {
						return cerr
					}
				}
				_, err := tx.Exec(ctx, `UPDATE webhook_deliveries SET automations_attempts = automations_attempts + 1,
					automations_next_at = $2, automations_at = CASE WHEN $3 THEN now() END WHERE id = $1`,
					d.ID, r.now().Add(matchBackoff(d.Attempts)), last)
				return err
			}); berr != nil {
				errs = append(errs, fmt.Errorf("delivery %s: %w", d.ID, berr))
			}
		}
	}
	return len(batch), errors.Join(errs...)
}

// pendingAutomations reads the enabled automations on d's event that d is
// still to be matched against (all of them on its first try, those pending
// after), and the payload a run of theirs carries.
func pendingAutomations(ctx context.Context, tx pgx.Tx, d pendingDelivery) ([]Automation, Event, []Payload, error) {
	rows, err := tx.Query(ctx, `SELECT `+automationColumns+` FROM automations a
		WHERE a.enabled AND a.trigger->>'kind' = 'github' AND a.trigger->>'event' = $1
		  AND ($2::text[] IS NULL OR a.id = ANY($2)) ORDER BY a.id`, d.Event, d.Pending)
	if err != nil {
		return nil, Event{}, nil, err
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Automation, error) { return scanAutomation(row) })
	if err != nil {
		return nil, Event{}, nil, err
	}
	ev := ReadEvent(d.Event, d.Payload)
	return list, ev, []Payload{{DeliveryID: d.ID, Event: d.Event, Action: ev.Action, Body: d.Payload}}, nil
}

// skipUnmatched records a skipped run for each automation d was still to be
// matched against.
func skipUnmatched(ctx context.Context, tx pgx.Tx, d pendingDelivery, reason string) error {
	list, _, payloads, err := pendingAutomations(ctx, tx, d)
	if err != nil {
		return err
	}
	for _, a := range list {
		if _, err := recordSkipped(ctx, tx, a, SourceEvent, payloads, reason); err != nil {
			return err
		}
	}
	return nil
}

// matchDelivery evaluates one delivery against its organisation's enabled
// automations on its event (on a retry, only those still pending) and
// fires the matching ones. An automation it cannot be evaluated against
// is kept pending for the next try, or after the last gets a skipped run.
func (r *Runner) matchDelivery(ctx context.Context, d pendingDelivery) error {
	var cancelled []Cancelled
	err := r.DB.InOrg(ctx, d.Org, func(tx pgx.Tx) error {
		cancelled = nil
		list, ev, payloads, err := pendingAutomations(ctx, tx, d)
		if err != nil {
			return err
		}
		last := d.Attempts+1 >= MatchAttempts
		var pending []string
		for _, a := range list {
			v, err := r.Evaluate(ctx, tx, a, ev)
			if err != nil {
				r.Log.Warn("an automation could not be matched to a delivery", "automation", a.ID, "delivery", d.ID,
					"attempt", d.Attempts+1, "error", err)
				if !last {
					pending = append(pending, a.ID)
					continue
				}
				if _, err := recordSkipped(ctx, tx, a, SourceEvent, payloads, "it could not be matched: "+err.Error()); err != nil {
					return err
				}
				continue
			}
			if !v.Matches {
				if err := recordGitHubVerdict(ctx, tx, a, d.ID, VerdictSkipped, filterWords(v), ""); err != nil {
					return err
				}
				continue
			}
			fired, err := Fire(ctx, tx, a, SourceEvent, payloads, "", nil)
			if err != nil {
				return err
			}
			cancelled = append(cancelled, fired.Cancelled...)
			verdict, reason := firedVerdict(ctx, tx, fired)
			if err := recordGitHubVerdict(ctx, tx, a, d.ID, verdict, reason, fired.Run); err != nil {
				return err
			}
		}
		if len(pending) > 0 {
			_, err = tx.Exec(ctx, `UPDATE webhook_deliveries SET automations_attempts = automations_attempts + 1,
				automations_next_at = $2, automations_pending = $3 WHERE id = $1`, d.ID, r.now().Add(matchBackoff(d.Attempts)), pending)
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE webhook_deliveries SET automations_at = now(), automations_pending = NULL WHERE id = $1`, d.ID)
		return err
	})
	if err == nil {
		r.EndCancelled(ctx, cancelled)
	}
	return err
}

// recordSkipped records a firing that did not run, with its reason.
func recordSkipped(ctx context.Context, tx pgx.Tx, a Automation, source string, payloads []Payload, reason string) (string, error) {
	raw, _ := json.Marshal(nonNil(payloads))
	id := ids.New(ids.AutomationRun)
	_, err := tx.Exec(ctx, `INSERT INTO automation_runs (id, organization_id, automation_id, status, reason, source, payloads, ended_at)
		VALUES ($1, $2, $3, 'skipped', left($4, 2000), $5, $6, now())`, id, a.Org, a.ID, reason, source, raw)
	return id, err
}

// Evaluate matches an event against an automation, its project's
// repositories included; paths read the pull request's files from GitHub.
func (r *Runner) Evaluate(ctx context.Context, tx pgx.Tx, a Automation, ev Event) (Verdict, error) {
	var repos []string
	if a.ProjectID != "" {
		rows, err := tx.Query(ctx, `SELECT url FROM repositories WHERE project_id = $1`, a.ProjectID)
		if err != nil {
			return Verdict{}, err
		}
		urls, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return Verdict{}, err
		}
		repos = []string{}
		for _, u := range urls {
			if slug := slugOf(u); slug != "" {
				repos = append(repos, slug)
			}
		}
	}
	files := func() ([]string, error) {
		if r.Forges == nil {
			return nil, errors.New("no GitHub to read the pull request's files from")
		}
		gh, err := r.Forges.For(ctx, a.Org)
		if err != nil {
			return nil, err
		}
		if gh == nil {
			return nil, errors.New("no GitHub to read the pull request's files from")
		}
		return gh.PullFiles(ctx, ev.Repository, ev.PullNumber, 3000)
	}
	return Match(a.Trigger, a.RepoFilter, repos, ev, files)
}

// ---------------------------------------------------------------------------
// Starting
// ---------------------------------------------------------------------------

// StartingMax is how long a claimed run may stay starting (submitted to
// lux, session not yet recorded) before it is queued again: an
// orchestrator that stopped mid-start. lux's idempotency key makes the
// second submit the same Run.
const StartingMax = 10 * time.Minute

// startQueued starts the oldest queued run of each automation that has room.
func (r *Runner) startQueued(ctx context.Context) (int, error) {
	type queued struct{ ID, Org, AutomationID, Source string }
	var list []queued
	if err := r.DB.InSystem(ctx, "automations", func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE automation_runs SET status = 'queued', started_at = NULL
			WHERE status = 'running' AND session IS NULL AND started_at < $1`, r.now().Add(-StartingMax)); err != nil {
			return err
		}
		// Heads whose automation has no room are left out here (start
		// re-checks under the lock): read first, a page of them would hold
		// back every other automation's queued run.
		rows, err := tx.Query(ctx, `SELECT q.id, q.organization_id, q.automation_id, q.source
			FROM automation_runs q JOIN automations a ON a.id = q.automation_id
			WHERE q.status = 'queued' AND NOT EXISTS (SELECT 1 FROM automation_runs o WHERE o.automation_id = q.automation_id
			  AND o.status = 'queued' AND o.source <> 'test' AND q.source <> 'test' AND (o.created_at, o.id) < (q.created_at, q.id))
			  AND (q.source = 'test' OR (SELECT count(*) FROM automation_runs l WHERE l.automation_id = q.automation_id
			    AND l.status = 'running' AND l.source <> 'test') < CASE WHEN a.concurrency = 'parallel' THEN a.parallel_max ELSE 1 END)
			ORDER BY q.created_at LIMIT 20`)
		if err != nil {
			return err
		}
		list, err = pgx.CollectRows(rows, pgx.RowToStructByPos[queued])
		return err
	}); err != nil {
		return 0, err
	}
	n := 0
	for _, q := range list {
		started, err := r.start(ctx, q.Org, q.ID)
		if err != nil {
			r.Log.Warn("automation run did not start", "run", q.ID, "error", err)
			continue
		}
		if started {
			n++
		}
	}
	return n, nil
}

// start claims one queued run and submits it, or leaves it queued while its
// automation has no room. A run that cannot start fails, saying why.
//
// The claim (status running, session NULL while it starts) is taken under
// the automation's row lock Fire takes, so a firing meanwhile sees the run
// as running: it queues, merges into a new run, or cancels this one; it
// never adds to payloads this start already read.
func (r *Runner) start(ctx context.Context, org, runID string) (bool, error) {
	var a Automation
	var run struct {
		Source   string
		Payloads []Payload
		At       *time.Time
		Kept     string
	}
	claimed := false
	if err := r.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		var automationID string
		if err := tx.QueryRow(ctx, `SELECT automation_id FROM automation_runs WHERE id = $1 AND status = 'queued'`, runID).Scan(&automationID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT 1 FROM automations WHERE id = $1 FOR UPDATE`, automationID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT source, payloads, scheduled_for FROM automation_runs WHERE id = $1 AND status = 'queued'`, runID).
			Scan(&run.Source, &run.Payloads, &run.At); err != nil {
			return err
		}
		var err error
		if a, err = Load(ctx, tx, automationID); err != nil {
			return err
		}
		var running int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM automation_runs WHERE automation_id = $1 AND status = 'running' AND source <> 'test'`,
			a.ID).Scan(&running); err != nil {
			return err
		}
		if run.Source != SourceTest && !CanStartQueued(a.policy(), running) {
			return nil
		}
		err = tx.QueryRow(ctx, `SELECT lux_run_id FROM automation_sessions WHERE automation_id = $1`, a.ID).Scan(&run.Kept)
		if err != nil && !db.IsNotFound(err) {
			return err
		}
		// A webhook run is claimed whole: its POST is sent off the sweep, as
		// followRunning finds it running.
		_, err = tx.Exec(ctx, `UPDATE automation_runs SET status = 'running', started_at = now(), reason = NULL,
			session = CASE WHEN $2 = 'webhook' THEN 'ephemeral' END WHERE id = $1`, runID, a.Kind)
		claimed = err == nil
		return err
	}); err != nil {
		if db.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	if !claimed || a.Kind == "webhook" {
		return claimed, nil
	}
	started, err := r.submit(ctx, a, runID, run.Source, run.Payloads, run.At, run.Kept)
	if err != nil || !started {
		// Not started (a busy kept session, lux unreachable): queued again
		// for the next pass, unless a firing cancelled it meanwhile.
		if rerr := r.DB.InOrg(ctx, a.Org, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE automation_runs SET status = 'queued', started_at = NULL
				WHERE id = $1 AND status = 'running' AND session IS NULL`, runID)
			return err
		}); rerr != nil {
			return false, errors.Join(err, rerr)
		}
	}
	return started, err
}

// submit resumes the kept session or submits a new lux Run for a claimed
// run; false, nil leaves it for the next pass.
func (r *Runner) submit(ctx context.Context, a Automation, runID, source string, payloads []Payload, at *time.Time, kept string) (bool, error) {
	// A test run's session is its own: fresh and ephemeral, the kept one untouched (Q21).
	resumable := a.SessionMode == "resumable" && source != SourceTest
	var luxRunID, session, startReason string
	var err error
	if resumable && kept != "" {
		var why string
		luxRunID, why, err = r.resume(ctx, a, kept, runID, payloads, at)
		session = "resumed"
		if why != "" {
			// What stops this resume (an image still building, no tier) stops
			// a fresh start too: the run fails and the kept session stays.
			return true, r.finish(ctx, a, runID, "failed", why, nil)
		}
		if le, ok := lux.AsError(err); ok && le.Status == http.StatusConflict {
			// Still stopping after the last run: tried again next pass, for
			// up to ResumeConflictMax; then the kept Run is ended and this
			// run starts fresh.
			var since time.Time
			if err := r.DB.InOrg(ctx, a.Org, func(tx pgx.Tx) error {
				return tx.QueryRow(ctx, `UPDATE automation_runs SET resume_conflict_since = COALESCE(resume_conflict_since, $2)
					WHERE id = $1 RETURNING resume_conflict_since`, runID, r.now()).Scan(&since)
			}); err != nil {
				return false, err
			}
			if r.now().Sub(since) < ResumeConflictMax {
				return false, nil
			}
			r.Log.Warn("kept automation session stayed busy; starting fresh", "automation", a.ID, "lux_run", kept)
			if err := r.StartFresh(ctx, a.Org, a.ID); err != nil {
				return false, err
			}
			startReason = fmt.Sprintf("its kept session was still busy after %s, so it started fresh", ResumeConflictMax)
			luxRunID, session, err = "", "", nil
		}
		if le, isLux := lux.AsError(err); err != nil && (!isLux || le.Retryable()) {
			// Not lux's answer (the database, a forge token), or lux out of
			// reach, failing or busy (unreachable, 5xx, 429): tried again
			// next pass, the kept session untouched.
			return false, err
		}
		if err != nil {
			// lux refused to resume the kept Run: it is ended, so no stopped
			// Run is left behind, and the run starts fresh.
			r.Log.Info("kept automation session could not be resumed; starting fresh", "automation", a.ID, "error", err)
			if err := r.StartFresh(ctx, a.Org, a.ID); err != nil {
				return false, err
			}
			luxRunID, session = "", ""
		}
	}
	if luxRunID == "" {
		spec, why, err := r.spec(ctx, a, runID, payloads, at)
		if err != nil {
			return false, err
		}
		if why != "" {
			return true, r.finish(ctx, a, runID, "failed", why, nil)
		}
		created, err := r.Lux.Submit(ctx, spec, "automation-run-"+runID)
		if err != nil {
			if le, ok := lux.AsError(err); ok && !le.Retryable() {
				return true, r.finish(ctx, a, runID, "failed", "lux refused the run: "+le.Message, nil)
			}
			return false, err
		}
		luxRunID = created.ID
		session = "ephemeral"
		if resumable {
			session = "new"
		}
	}
	claimed := false
	if err := r.DB.InOrg(ctx, a.Org, func(tx pgx.Tx) error {
		// A resumed session's output is read on from where its last run left
		// it: what it said before is not this run's.
		tag, err := tx.Exec(ctx, `UPDATE automation_runs SET reason = NULLIF($5, ''), lux_run_id = $2, session = $3,
			container_started_at = CASE WHEN $4 = 'server' THEN now() END, last_request_at = now(), inputs_sent = $6,
			lux_cursor = CASE WHEN $3 = 'resumed' THEN (SELECT p.lux_cursor FROM automation_runs p WHERE p.lux_run_id = $2 AND p.id <> $1
				ORDER BY p.created_at DESC LIMIT 1) END
			WHERE id = $1 AND status = 'running' AND session IS NULL`, runID, luxRunID, session, a.Kind, startReason, len(payloads))
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		claimed = true
		if resumable {
			_, err = tx.Exec(ctx, `INSERT INTO automation_sessions (automation_id, organization_id, lux_run_id, last_run_at)
				VALUES ($1, $2, $3, now()) ON CONFLICT (automation_id) DO UPDATE SET lux_run_id = EXCLUDED.lux_run_id, last_run_at = now()`,
				a.ID, a.Org, luxRunID)
		}
		return err
	}); err != nil {
		return false, err
	}
	if !claimed {
		// Cancelled by a firing while it was submitted: a resumed kept
		// session is stopped and stays kept; a new Run is ended.
		r.Log.Info("an automation run was cancelled while it started; its lux Run is ended", "run", runID, "lux_run", luxRunID)
		r.stopOrCancel(ctx, luxRunID, session == "resumed")
	}
	return true, nil
}

// resume supplies this firing's instructions and every declared secret:
// fresh forge tokens and current file, project, harness and registry values.
// Other secret names are excluded: lux would declare them for all later resumes.
// why names a condition that also prevents a fresh start (unbuilt image, no tier).
func (r *Runner) resume(ctx context.Context, a Automation, kept, runID string, payloads []Payload, at *time.Time) (string, string, error) {
	in := lux.ResumeInput{RequestID: "automation-" + runID}
	if a.Kind == "agent" {
		prompt, _, err := r.prompt(ctx, a, payloads, at)
		if err != nil {
			return "", "", err
		}
		in.Input = prompt
	}
	got, err := r.Lux.Get(ctx, kept)
	if err != nil {
		return "", "", err
	}
	if r.Forges != nil {
		gh, err := r.Forges.For(ctx, a.Org)
		if err != nil {
			return "", "", err
		}
		if gh != nil {
			if in.Secrets, err = phases.ResumeCredentials(ctx, gh, got.Spec, nil); err != nil {
				return "", "", err
			}
		}
	}
	if a.Kind == "agent" && r.Agent.ToolsURL != "" {
		token, err := r.toolsToken(ctx, a, runID, payloads)
		if err != nil {
			return "", "", err
		}
		in.Secrets = append(in.Secrets, lux.Secret{Name: "DUDE_TOOLS_AUTH", Value: "Bearer " + token})
	}
	given := func(name string) bool {
		return slices.ContainsFunc(in.Secrets, func(s lux.Secret) bool { return s.Name == name })
	}
	if slices.ContainsFunc(got.Spec.Secrets, func(s lux.StoredSecret) bool { return !given(s.Name) }) {
		specIn, why, err := r.specInput(ctx, a, runID, payloads, at, false)
		if err != nil || why != "" {
			return "", why, err
		}
		rest, err := phases.AutomationResumeSecrets(ctx, r.Agent, r.Registry, specIn, got.Spec)
		if err != nil {
			return "", "", err
		}
		for _, s := range rest {
			declared := slices.ContainsFunc(got.Spec.Secrets, func(d lux.StoredSecret) bool { return d.Name == s.Name })
			if declared && !given(s.Name) {
				in.Secrets = append(in.Secrets, s)
			}
		}
	}
	if _, err := r.Lux.Resume(ctx, kept, in); err != nil {
		return "", "", err
	}
	return kept, "", nil
}

// ---------------------------------------------------------------------------
// Following
// ---------------------------------------------------------------------------

// followRunning reads where each running run's lux Run is: a command's
// exit, an agent's turn ending, a Server's queue of events and idle time.
func (r *Runner) followRunning(ctx context.Context) (int, error) {
	type running struct{ ID, Org, AutomationID, LuxRunID, Source, Kind string }
	var list []running
	// Webhook runs and the rest are read with a limit each, so a burst of
	// one kind never takes the other's places; within each, the runs
	// followed longest ago (never first) lead: with more live runs than
	// workers, each gets a turn within a few passes.
	if err := r.DB.InSystem(ctx, "automations", func(tx pgx.Tx) error {
		for _, webhook := range []bool{true, false} {
			rows, err := tx.Query(ctx, `SELECT r.id, r.organization_id, r.automation_id, COALESCE(r.lux_run_id, ''), r.source, a.kind
				FROM automation_runs r JOIN automations a ON a.id = r.automation_id
				WHERE r.status = 'running' AND r.session IS NOT NULL AND (a.kind = 'webhook') = $1
				ORDER BY r.followed_at NULLS FIRST, r.started_at LIMIT $2`, webhook, cmp.Or(r.FollowLimit, DefaultFollowLimit))
			if err != nil {
				return err
			}
			page, err := pgx.CollectRows(rows, pgx.RowToStructByPos[running])
			if err != nil {
				return err
			}
			list = append(list, page...)
		}
		return nil
	}); err != nil {
		return 0, err
	}
	var followed []string
	for _, run := range list {
		pool := poolFollow
		if run.Kind == "webhook" {
			pool = poolPost
		}
		if r.dispatch(ctx, pool, "follow:"+run.ID, func(ctx context.Context) {
			if err := r.followOne(ctx, run.ID, run.Org, run.AutomationID, run.LuxRunID, run.Source); err != nil {
				r.Log.Warn("following an automation run failed", "run", run.ID, "error", err)
			}
		}) {
			followed = append(followed, run.ID)
		}
	}
	if len(followed) > 0 {
		if err := r.DB.InSystem(ctx, "automations", func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE automation_runs SET followed_at = now() WHERE id = ANY($1)`, followed)
			return err
		}); err != nil {
			return 0, err
		}
	}
	// Following is in the background: nothing here says to sweep again at once.
	return 0, nil
}

// followOne reads where one running run is and acts on it.
func (r *Runner) followOne(ctx context.Context, runID, org, automationID, luxRunID, source string) error {
	var a Automation
	if err := r.DB.InOrg(ctx, org, func(tx pgx.Tx) (err error) {
		a, err = Load(ctx, tx, automationID)
		return err
	}); err != nil {
		return err
	}
	var err error
	switch a.Kind {
	case "server":
		_, err = r.followServer(ctx, a, runID, luxRunID, source)
	case "webhook":
		err = r.postWebhook(ctx, a, runID)
	default:
		_, err = r.followRun(ctx, a, runID, luxRunID, source)
	}
	return err
}

// followRun ends a command's run when its lux Run ends, and an agent's
// when its turn ends (busy then idle on its output): a resumable agent's
// Run is stopped and kept, an ephemeral one's cancelled.
func (r *Runner) followRun(ctx context.Context, a Automation, runID, luxRunID, source string) (bool, error) {
	got, err := r.Lux.Get(ctx, luxRunID)
	if err != nil {
		if le, ok := lux.AsError(err); ok && le.Status == http.StatusNotFound {
			return true, r.finish(ctx, a, runID, "failed", "its container is gone", nil)
		}
		return false, err
	}
	var cursor, prior string
	var busy bool
	var payloads []Payload
	var inputsSent int
	if err := r.DB.InOrg(ctx, a.Org, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT COALESCE(lux_cursor, ''), busy_seen, payloads, inputs_sent, COALESCE(log_tail, '') FROM automation_runs WHERE id = $1`, runID).
			Scan(&cursor, &busy, &payloads, &inputsSent, &prior)
	}); err != nil {
		return false, err
	}
	turnDone, log, newCursor, failed, err := r.readOutput(ctx, a.Kind, luxRunID, cursor, busy, prior)
	if err != nil {
		return false, err
	}
	// Events sent into the run while it works (Concurrency: send): each
	// reaches its agent as input, and its turn is waited for afresh.
	sent := 0
	if a.Kind == "agent" && !lux.Terminal(got.State) && failed == "" && inputsSent < len(payloads) {
		if sent, err = r.sendInputs(ctx, a, runID, luxRunID, payloads, inputsSent); err != nil {
			return false, err
		}
	}
	if err := r.DB.InOrg(ctx, a.Org, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE automation_runs SET lux_cursor = $2, busy_seen = busy_seen OR $3,
			inputs_sent = inputs_sent + $5, log_tail = right(COALESCE(log_tail, '') || $4, 16000) WHERE id = $1`,
			runID, newCursor, turnDone.busy, log, sent)
		return err
	}); err != nil {
		return false, err
	}
	if sent > 0 {
		return true, nil
	}
	cost := func() *float64 { return r.cost(ctx, luxRunID) }
	keep := a.SessionMode == "resumable" && source != SourceTest
	switch {
	case got.State == "succeeded":
		return true, r.finish(ctx, a, runID, "succeeded", "", cost())
	case lux.Terminal(got.State):
		return true, r.finish(ctx, a, runID, "failed", cmp.Or(got.StateReason, "its container ended: "+got.State), cost())
	case a.Kind == "agent" && failed != "":
		return true, r.endThenStop(ctx, a, runID, luxRunID, keep, "failed", failed, cost())
	case a.Kind == "agent" && turnDone.done:
		return r.endTurnDone(ctx, a, runID, luxRunID, keep, cost())
	}
	return false, nil
}

// endTurnDone ends an agent run whose turn is done, under the automation's
// lock Fire takes, only once every payload it holds was given to its agent:
// an event sent into it since payloads were read keeps it live, and the
// next pass gives that event to the agent and waits for its turn.
func (r *Runner) endTurnDone(ctx context.Context, a Automation, runID, luxRunID string, keep bool, cost *float64) (bool, error) {
	ended := false
	if err := r.DB.InOrg(ctx, a.Org, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT 1 FROM automations WHERE id = $1 FOR UPDATE`, a.ID); err != nil {
			return err
		}
		var err error
		ended, err = endIn(ctx, tx, a, runID, "succeeded", "", cost, allInputs)
		return err
	}); err != nil || !ended {
		return ended, err
	}
	r.stopOrCancel(ctx, luxRunID, keep)
	return true, nil
}

// sendInputs gives a running agent the payloads after the first sent ones,
// each as the automation's prompt for that event; it says how many went.
// A request id per payload makes a retry after a lost answer one input.
func (r *Runner) sendInputs(ctx context.Context, a Automation, runID, luxRunID string, payloads []Payload, sent int) (int, error) {
	n := 0
	for i := sent; i < len(payloads); i++ {
		text, _, err := r.prompt(ctx, a, payloads[i:i+1], nil)
		if err != nil {
			return n, err
		}
		if err := r.Lux.Input(ctx, luxRunID, lux.InputRequest{Text: text, RequestID: fmt.Sprintf("automation-%s-%d", runID, i)}); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// endThenStop ends a run that is still live and only then stops or cancels
// its lux Run: a run a newer firing already cancelled (and whose kept
// session the next run may have resumed) leaves the lux Run alone.
func (r *Runner) endThenStop(ctx context.Context, a Automation, runID, luxRunID string, keep bool, status, reason string, cost *float64) error {
	ended, err := r.end(ctx, a, runID, status, reason, cost)
	if err != nil || !ended {
		return err
	}
	r.stopOrCancel(ctx, luxRunID, keep)
	return nil
}

// stopOrCancel stops (keep) or ends a run's lux Run; either way its preview
// sign-in is forgotten: a resume starts a new placement, signed in afresh.
func (r *Runner) stopOrCancel(ctx context.Context, luxRunID string, keep bool) {
	r.forgetPreview(luxRunID)
	var err error
	if keep {
		err = r.Lux.Stop(ctx, luxRunID)
	} else {
		err = r.Lux.Cancel(ctx, luxRunID)
	}
	if err != nil {
		r.Log.Warn("stopping an automation's lux Run failed", "lux_run", luxRunID, "error", err)
	}
}

type turnState struct{ busy, done bool }

// readOutput reads a Run's records since cursor without waiting for more:
// the text the workload printed, and whether its agent went busy, then idle.
// An agent's reply is read from its harness's message events (ACP chunks,
// Claude Code's assistant text, Codex's agentMessage items). lux's acp
// adapter also writes each reply as one stdout record: that copy is left
// out, matched against the reply read so far and prior, the log before
// this read; any other record, stderr included, is kept.
func (r *Runner) readOutput(ctx context.Context, kind, luxRunID, cursor string, busy bool, prior string) (turnState, string, string, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	state := turnState{busy: busy}
	var log strings.Builder
	failed := ""
	err := r.Lux.Output(ctx, luxRunID, cursor, 0, func(f lux.Frame) error {
		if f.Kind != "record" {
			return nil
		}
		if f.Cursor != "" {
			cursor = f.Cursor
		}
		if f.Event == nil {
			copied := strings.TrimSpace(f.Data)
			// Assumes the copy directly follows the reply: a record between them leaves it logged twice.
			if kind == "agent" && f.Channel == "stdout" && copied != "" && strings.HasSuffix(strings.TrimSpace(prior+log.String()), copied) {
				return nil
			}
			log.WriteString(f.Data)
			return nil
		}
		var d map[string]any
		_ = json.Unmarshal(f.Event.Data, &d)
		switch f.Event.Type {
		case "lux.activity":
			switch d["activity"] {
			case "busy":
				state.busy = true
			case "idle":
				if state.busy {
					state.done = true
					cancel()
				}
			}
		case "acp.agent_message_chunk":
			if c, ok := d["content"].(map[string]any); ok {
				if t, ok := c["text"].(string); ok {
					log.WriteString(t)
				}
			}
		case "claude.assistant":
			var line struct {
				Message struct {
					Content []struct {
						Type, Text string
					} `json:"content"`
				} `json:"message"`
			}
			_ = json.Unmarshal(f.Event.Data, &line)
			for _, b := range line.Message.Content {
				if b.Type == "text" {
					log.WriteString(b.Text)
				}
			}
		case "codex.item/completed":
			var item struct {
				Item struct {
					Type, Text string
				} `json:"item"`
			}
			_ = json.Unmarshal(f.Event.Data, &item)
			if item.Item.Type == "agentMessage" {
				log.WriteString(item.Item.Text)
			}
		case "acp.turn_end", "turn_end":
			if e, _ := d["error"].(string); e != "" {
				failed = "the agent's turn failed: " + e
			}
		}
		return nil
	})
	if err != nil && ctx.Err() == nil && !errors.Is(err, context.Canceled) {
		return state, log.String(), cursor, failed, err
	}
	return state, log.String(), cursor, failed, nil
}

// cost is what lux has priced the Run at so far, nil when unknown.
func (r *Runner) cost(ctx context.Context, luxRunID string) *float64 {
	c, err := r.Lux.Cost(ctx, luxRunID)
	if err != nil {
		return nil
	}
	total, known := 0.0, false
	for _, t := range c.Totals {
		if v, err := strconv.ParseFloat(t.Amount, 64); err == nil && t.Currency == "USD" {
			total, known = total+v, true
		}
	}
	if !known {
		return nil
	}
	return &total
}

// finish ends a run and, on a failure, tells its owner (or, held, every
// admin) when the automation says to. A queued run behind it starts next pass.
func (r *Runner) finish(ctx context.Context, a Automation, runID, status, reason string, cost *float64) error {
	_, err := r.end(ctx, a, runID, status, reason, cost)
	return err
}

// end is finish, saying whether the run was still live (and so ended now).
func (r *Runner) end(ctx context.Context, a Automation, runID, status, reason string, cost *float64) (bool, error) {
	ended := false
	err := r.DB.InOrg(ctx, a.Org, func(tx pgx.Tx) (err error) {
		ended, err = endIn(ctx, tx, a, runID, status, reason, cost, allAny)
		return err
	})
	return ended && err == nil, err
}

// What endIn requires to have reached every payload the run holds.
const (
	allAny    = ""
	allPosted = "posted"      // POSTed to a Server run's container
	allInputs = "inputs_sent" // given to an agent run's agent
)

// endIn ends a live run in tx; all names a counter (allPosted, allInputs)
// that must have reached every payload the run holds, or allAny.
func endIn(ctx context.Context, tx pgx.Tx, a Automation, runID, status, reason string, cost *float64, all string) (bool, error) {
	tag, err := tx.Exec(ctx, `UPDATE automation_runs SET status = $2, reason = NULLIF($3, ''), cost_usd = COALESCE($4, cost_usd),
		ended_at = now(), container_stopped_at = CASE WHEN container_started_at IS NOT NULL THEN COALESCE(container_stopped_at, now()) END
		WHERE id = $1 AND status IN ('queued', 'running')
		  AND ($5 <> 'posted' OR posted >= jsonb_array_length(payloads))
		  AND ($5 <> 'inputs_sent' OR inputs_sent >= jsonb_array_length(payloads))`,
		runID, status, reason, cost, all)
	if err != nil || tag.RowsAffected() == 0 {
		return false, err
	}
	if _, err := tx.Exec(ctx, `UPDATE automation_sessions SET last_run_at = now() WHERE automation_id = $1`, a.ID); err != nil {
		return false, err
	}
	if status != "failed" || !a.NotifyOnFailure {
		return true, nil
	}
	return true, notifyFailure(ctx, tx, a, runID, reason)
}

// EvAutomationFailed is the ledger's word for a failed automation run: the
// notifier tells its owner, or every admin when the owner left.
const EvAutomationFailed = "automation.failed"

func notifyFailure(ctx context.Context, tx pgx.Tx, a Automation, runID, reason string) error {
	var to []string
	if a.OwnerID != "" {
		to = []string{a.OwnerID}
	} else {
		rows, err := tx.Query(ctx, `SELECT id FROM people WHERE role = 'admin' AND removed_at IS NULL ORDER BY id`)
		if err != nil {
			return err
		}
		if to, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
			return err
		}
	}
	_, err := ledger.Append(ctx, tx, ledger.Event{Type: EvAutomationFailed, OrganizationID: a.Org, ProjectID: a.ProjectID,
		ActorType: ledger.ActorSystem, ActorID: "dude", Source: "orchestrator",
		Payload: map[string]any{"automationId": a.ID, "projectId": a.ProjectID, "name": a.Name, "runId": runID, "reason": reason, "to": to}})
	return err
}

// ---------------------------------------------------------------------------
// Server runs
// ---------------------------------------------------------------------------

// followServer feeds a Server run's container: once its server is healthy,
// each payload not yet POSTed is, one request row each; when nothing is in
// flight and none came for the idle time, the container is stopped (kept
// for a resumable automation) and the run ends.
func (r *Runner) followServer(ctx context.Context, a Automation, runID, luxRunID, source string) (bool, error) {
	var cfg struct {
		Port   int     `json:"port"`
		Path   string  `json:"path"`
		Health *string `json:"health"`
	}
	_ = json.Unmarshal(a.Server, &cfg)
	var payloads []Payload
	var posted int
	var started, last time.Time
	var failedReq bool
	if err := r.DB.InOrg(ctx, a.Org, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT payloads, posted, started_at, last_request_at,
			EXISTS (SELECT 1 FROM automation_run_requests q WHERE q.run_id = r.id AND (q.http_status IS NULL OR q.http_status >= 400)
			  AND NOT EXISTS (SELECT 1 FROM automation_run_requests q2 WHERE q2.run_id = r.id AND q2.delivery_id = q.delivery_id AND q2.try > q.try
			    AND q2.http_status BETWEEN 200 AND 299) AND (q.try = 2 OR q.http_status BETWEEN 400 AND 499))
			FROM automation_runs r WHERE id = $1`, runID).Scan(&payloads, &posted, &started, &last, &failedReq)
	}); err != nil {
		return false, err
	}
	keep := a.SessionMode == "resumable" && source != SourceTest
	if got, err := r.Lux.Get(ctx, luxRunID); err == nil && lux.Terminal(got.State) {
		r.forgetPreview(luxRunID)
		return true, r.endServer(ctx, a, runID, "failed", "error", "its container ended: "+cmp.Or(got.StateReason, got.State))
	}
	base, err := r.serverURL(ctx, luxRunID, cfg.Port)
	// Its requests go through lux's preview listener, signed in (previewDo).
	signIn := previewAuth{luxRunID: luxRunID, base: base}
	if err != nil || !r.healthy(ctx, base, signIn, cfg.Health) {
		if r.now().Sub(started) > ServerHealthMax {
			r.stopOrCancel(ctx, luxRunID, keep)
			return true, r.endServer(ctx, a, runID, "failed", "error", "its server was not healthy within 60 seconds")
		}
		return false, nil
	}
	if posted < len(payloads) {
		for i := posted; i < len(payloads); i++ {
			r.postOnce(ctx, a, runID, base+cfg.Path, signIn, payloads[i])
		}
		return true, r.DB.InOrg(ctx, a.Org, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE automation_runs SET posted = $2, last_request_at = now() WHERE id = $1`, runID, len(payloads))
			return err
		})
	}
	if r.now().Sub(last) < ServerIdle {
		return false, nil
	}
	status := "succeeded"
	reason := ""
	if failedReq {
		status, reason = "failed", "a POST to the server failed"
	}
	// Decided under the automation's lock Fire takes: an event sent into
	// this run since payloads were read keeps it up, to be POSTed next pass.
	ended := false
	if err := r.DB.InOrg(ctx, a.Org, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT 1 FROM automations WHERE id = $1 FOR UPDATE`, a.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE automation_runs SET stop_reason = 'idle', container_stopped_at = now()
			WHERE id = $1 AND status = 'running' AND posted >= jsonb_array_length(payloads)`, runID); err != nil {
			return err
		}
		var err error
		ended, err = endIn(ctx, tx, a, runID, status, reason, nil, allPosted)
		return err
	}); err != nil || !ended {
		return false, err
	}
	r.stopOrCancel(ctx, luxRunID, keep)
	return true, nil
}

func (r *Runner) endServer(ctx context.Context, a Automation, runID, status, stop, reason string) error {
	if err := r.DB.InOrg(ctx, a.Org, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE automation_runs SET stop_reason = $2, container_stopped_at = now() WHERE id = $1`, runID, stop)
		return err
	}); err != nil {
		return err
	}
	return r.finish(ctx, a, runID, status, reason, nil)
}

func (r *Runner) serverURL(ctx context.Context, luxRunID string, port int) (string, error) {
	if r.ServerURL != nil {
		return r.ServerURL(ctx, luxRunID, port)
	}
	servers, err := r.Lux.Servers(ctx, luxRunID)
	if err != nil {
		return "", err
	}
	for _, s := range servers {
		if s.Name == "automation" && s.State == lux.ServerReady {
			var v struct {
				URL string `json:"url"`
			}
			_ = json.Unmarshal(s.Raw, &v)
			if v.URL != "" {
				return strings.TrimRight(v.URL, "/"), nil
			}
		}
	}
	return "", errors.New("the server is not ready")
}

func (r *Runner) healthy(ctx context.Context, base string, signIn previewAuth, health *string) bool {
	if health == nil || *health == "" {
		u := strings.TrimPrefix(strings.TrimPrefix(base, "http://"), "https://")
		conn, err := (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "tcp", hostPort(u, base))
		if err != nil {
			return false
		}
		conn.Close()
		return true
	}
	req, err := http.NewRequestWithContext(ctx, "GET", base+*health, nil)
	if err != nil {
		return false
	}
	res, err := r.previewDo(ctx, signIn, req, nil)
	if err != nil {
		return false
	}
	res.Body.Close()
	return res.StatusCode < 400
}

// previewAuth names a Server run's lux preview: its lux Run and address.
// Zero for a webhook's request, which is sent as it is.
type previewAuth struct{ luxRunID, base string }

// previewStatusHeader marks a page lux's preview listener answered itself
// (its sign-in page among them), never one the container's app answered.
const previewStatusHeader = "X-Lux-Preview"

// previewDo sends a request to a Server run's container through lux's
// preview listener, which answers its own 401 to a request without its
// sign-in cookie. It signs in (previewSignIn) before the lux Run's first
// request, and again only when lux itself refuses the cookie: then the
// app never saw the request, and it is sent once more. An app's own 401 is
// its answer. The cookie is kept per lux Run until the run ends.
func (r *Runner) previewDo(ctx context.Context, signIn previewAuth, req *http.Request, body []byte) (*http.Response, error) {
	if signIn.luxRunID == "" {
		return r.client().Do(req)
	}
	r.mu.Lock()
	_, held := r.previewCookies[signIn.luxRunID]
	r.mu.Unlock()
	if !held {
		r.signInOrLog(ctx, signIn)
	}
	send := func() (*http.Response, error) {
		r.mu.Lock()
		cookie := r.previewCookies[signIn.luxRunID]
		r.mu.Unlock()
		req.Header.Del("Cookie")
		if cookie != "" {
			req.Header.Set("Cookie", cookie)
		}
		return r.client().Do(req)
	}
	res, err := send()
	if err != nil || res.StatusCode != http.StatusUnauthorized || res.Header.Get(previewStatusHeader) != "status" {
		if err == nil && req.Header.Get("Cookie") != "" {
			r.mu.Lock()
			if t, ok := r.previewTries[signIn.luxRunID]; ok {
				t.accepted = true
				r.previewTries[signIn.luxRunID] = t
			}
			r.mu.Unlock()
		}
		return res, err
	}
	if !r.signInOrLog(ctx, signIn) {
		return res, nil
	}
	res.Body.Close()
	req = req.Clone(ctx)
	if body != nil {
		req.Body = io.NopCloser(bytes.NewReader(body))
	}
	return send()
}

// signInOrLog signs in to the lux Run's preview. A failure is logged and
// remembered as no cookie, so requests go as they are until lux itself
// asks again. Within SignInBackoff of the last attempt it does not try
// again unless lux took a cookie since: false, nothing logged.
func (r *Runner) signInOrLog(ctx context.Context, signIn previewAuth) bool {
	r.mu.Lock()
	if t, ok := r.previewTries[signIn.luxRunID]; ok && !t.accepted && r.now().Sub(t.at) < SignInBackoff {
		r.mu.Unlock()
		return false
	}
	if r.previewTries == nil {
		r.previewTries = map[string]previewTry{}
	}
	r.previewTries[signIn.luxRunID] = previewTry{at: r.now()}
	r.mu.Unlock()
	err := r.previewSignIn(ctx, signIn)
	if err == nil {
		return true
	}
	r.Log.Info("signing in to a Server run's lux preview failed", "lux_run", signIn.luxRunID, "error", err)
	r.mu.Lock()
	if r.previewCookies == nil {
		r.previewCookies = map[string]string{}
	}
	if _, ok := r.previewCookies[signIn.luxRunID]; !ok {
		r.previewCookies[signIn.luxRunID] = ""
	}
	r.mu.Unlock()
	return false
}

// previewSignIn spends a preview ticket on <base>/.lux/auth, which answers
// with the preview cookie, and keeps it for the lux Run.
func (r *Runner) previewSignIn(ctx context.Context, signIn previewAuth) error {
	ticket, err := r.Lux.PreviewTicket(ctx, signIn.luxRunID)
	if err != nil {
		return fmt.Errorf("minting a preview ticket: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, "GET", signIn.base+"/.lux/auth?ticket="+url.QueryEscape(ticket)+"&to=/", nil)
	if err != nil {
		return err
	}
	// It answers with its cookie and a redirect, which is not followed.
	client := *r.client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, err := client.Do(req)
	if err != nil {
		// The URL holds the ticket: it is no part of what is logged.
		var ue *url.Error
		if errors.As(err, &ue) {
			ue.URL = signIn.base + "/.lux/auth"
		}
		return err
	}
	res.Body.Close()
	var parts []string
	for _, c := range res.Cookies() {
		parts = append(parts, c.Name+"="+c.Value)
	}
	if len(parts) == 0 {
		return fmt.Errorf("its sign-in answered %d with no cookie", res.StatusCode)
	}
	r.mu.Lock()
	if r.previewCookies == nil {
		r.previewCookies = map[string]string{}
	}
	r.previewCookies[signIn.luxRunID] = strings.Join(parts, "; ")
	r.mu.Unlock()
	return nil
}

func (r *Runner) forgetPreview(luxRunID string) {
	r.mu.Lock()
	delete(r.previewCookies, luxRunID)
	delete(r.previewTries, luxRunID)
	r.mu.Unlock()
}

func hostPort(hostPath, base string) string {
	host, _, _ := strings.Cut(hostPath, "/")
	if strings.Contains(host, ":") {
		return host
	}
	if strings.HasPrefix(base, "https://") {
		return host + ":443"
	}
	return host + ":80"
}

// postOnce POSTs one payload with dude's headers and records the answer;
// a 5xx or no answer is tried once more, a 4xx is not. A Server run's go
// through its lux preview (previewDo).
func (r *Runner) postOnce(ctx context.Context, a Automation, runID, url string, signIn previewAuth, p Payload) bool {
	body := []byte(p.Body)
	if len(body) == 0 {
		body = []byte("{}")
	}
	for try := 1; try <= 2; try++ {
		status, ms, excerpt, err := r.post(ctx, a, runID, url, signIn, p, body)
		_ = r.DB.InOrg(ctx, a.Org, func(tx pgx.Tx) error {
			var code any
			if status > 0 {
				code = status
			}
			var msg any
			if err != nil {
				msg = err.Error()
			}
			_, e := tx.Exec(ctx, `INSERT INTO automation_run_requests (id, organization_id, run_id, delivery_id, try, http_status, ms, error, response_excerpt)
				VALUES ($1, $2, $3, NULLIF($4, ''), $5, $6, $7, $8, NULLIF($9, ''))`,
				ids.New(ids.AutomationRequest), a.Org, runID, p.DeliveryID, try, code, ms, msg, excerpt)
			return e
		})
		if err == nil && status < 500 {
			return status < 300
		}
	}
	return false
}

func (r *Runner) post(ctx context.Context, a Automation, runID, url string, signIn previewAuth, p Payload, body []byte) (int, int, string, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return 0, 0, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Dude-Automation", a.ID)
	req.Header.Set("X-Dude-Run", runID)
	if p.Event != "" {
		req.Header.Set("X-GitHub-Event", p.Event)
	}
	if p.DeliveryID != "" {
		req.Header.Set("X-GitHub-Delivery", p.DeliveryID)
	}
	if a.WebhookSecret != "" {
		req.Header.Set("X-Dude-Signature-256", Sign(a.WebhookSecret, body))
	}
	began := time.Now()
	res, err := r.previewDo(ctx, signIn, req, body)
	ms := int(time.Since(began).Milliseconds())
	if err != nil {
		return 0, ms, "", err
	}
	defer res.Body.Close()
	excerpt, _ := io.ReadAll(io.LimitReader(res.Body, 500))
	return res.StatusCode, ms, string(excerpt), nil
}

// Sign is the X-Dude-Signature-256 of a body: GitHub's scheme, HMAC-SHA256
// with the automation's secret, hex, after "sha256=".
func Sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// postWebhook is a Webhook-only run: one signed POST of its payload (or, on
// a schedule, the schedule's time), retried once on a 5xx or no answer. It
// runs off the sweep, for a run start has claimed (running).
func (r *Runner) postWebhook(ctx context.Context, a Automation, runID string) error {
	var cfg struct {
		URL string `json:"url"`
	}
	_ = json.Unmarshal(a.Webhook, &cfg)
	var payloads []Payload
	if err := r.DB.InOrg(ctx, a.Org, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT payloads FROM automation_runs WHERE id = $1 AND status = 'running'`, runID).Scan(&payloads)
	}); err != nil {
		if db.IsNotFound(err) {
			return nil
		}
		return err
	}
	// Arrive starts a run per webhook firing (Q13): one payload at most.
	p := Payload{}
	if len(payloads) > 0 {
		p = payloads[0]
	}
	if len(p.Body) == 0 {
		p.Body, _ = json.Marshal(map[string]any{"automation": a.ID, "schedule": map[string]any{"time": r.now().UTC().Format(time.RFC3339)}})
	}
	if r.postOnce(ctx, a, runID, cfg.URL, previewAuth{}, p) {
		return r.finish(ctx, a, runID, "succeeded", "", nil)
	}
	return r.finish(ctx, a, runID, "failed", "the webhook did not answer 2xx", nil)
}

// ---------------------------------------------------------------------------
// Sessions
// ---------------------------------------------------------------------------

// expireSessions cleans up kept sessions not run for their expiry days,
// whether or not the automation is on (Q10).
func (r *Runner) expireSessions(ctx context.Context) (int, error) {
	type kept struct{ AutomationID, Org, LuxRunID string }
	var list []kept
	if err := r.DB.InSystem(ctx, "automations", func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT s.automation_id, s.organization_id, s.lux_run_id FROM automation_sessions s
			JOIN automations a ON a.id = s.automation_id
			WHERE s.last_run_at < $1::timestamptz - make_interval(days => COALESCE(a.expiry_days, 0))
			  AND NOT EXISTS (SELECT 1 FROM automation_runs r WHERE r.automation_id = s.automation_id AND r.status = 'running' AND r.source <> 'test')
			LIMIT 20`, r.now())
		if err != nil {
			return err
		}
		list, err = pgx.CollectRows(rows, pgx.RowToStructByPos[kept])
		return err
	}); err != nil {
		return 0, err
	}
	var errs []error
	for _, k := range list {
		if err := r.StartFresh(ctx, k.Org, k.AutomationID); err != nil {
			errs = append(errs, fmt.Errorf("automation %s: %w", k.AutomationID, err))
		}
	}
	return len(list), errors.Join(errs...)
}

// StartFresh lets go of an automation's kept session: its lux Run is
// cancelled and the next firing starts a new one.
func StartFresh(ctx context.Context, d *db.DB, c lux.Client, org, automationID string) error {
	_, err := startFresh(ctx, d, c, org, automationID)
	return err
}

// StartFresh is the package's StartFresh, forgetting the cancelled lux
// Run's preview sign-in.
func (r *Runner) StartFresh(ctx context.Context, org, automationID string) error {
	luxRunID, err := startFresh(ctx, r.DB, r.Lux, org, automationID)
	if luxRunID != "" {
		r.forgetPreview(luxRunID)
	}
	return err
}

// startFresh returns the lux Run let go of ("" for none). Retryable cancel
// errors keep the session for another attempt; other refusals release it
// so an uncancellable Run cannot block later firings.
func startFresh(ctx context.Context, d *db.DB, c lux.Client, org, automationID string) (string, error) {
	var luxRunID string
	err := d.InOrg(ctx, org, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT lux_run_id FROM automation_sessions WHERE automation_id = $1`, automationID).Scan(&luxRunID)
	})
	if db.IsNotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if err := c.Cancel(ctx, luxRunID); err != nil {
		if le, ok := lux.AsError(err); !ok || le.Retryable() {
			return "", err
		}
	}
	// Only the session cancelled: a run that kept another meanwhile keeps it.
	err = d.InOrg(ctx, org, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM automation_sessions WHERE automation_id = $1 AND lux_run_id = $2`, automationID, luxRunID)
		return err
	})
	return luxRunID, err
}

// ---------------------------------------------------------------------------
// What a run is told and given
// ---------------------------------------------------------------------------

type ownAgent struct {
	Harness          string `json:"harness"`
	TierID           string `json:"tierId"`
	TimeLimitMinutes int    `json:"timeLimitMinutes"`
	MachineSizeID    string `json:"machineSizeId"`
	ImageID          string `json:"imageId"`
	Prompt           string `json:"prompt"`
}

type agentSettings struct {
	Own    *ownAgent `json:"own"`
	Preset *struct {
		Role      string   `json:"role"`
		Overrides ownAgent `json:"overrides"`
	} `json:"preset"`
	Brief string `json:"brief"`
}

// prompt is what the agent is told this firing, variables substituted:
// its own prompt, or its role's prompt then the brief (Q6, Q15).
func (r *Runner) prompt(ctx context.Context, a Automation, payloads []Payload, at *time.Time) (string, string, error) {
	var s agentSettings
	_ = json.Unmarshal(a.Agent, &s)
	vars := Variables(payloads, at, a.Trigger.TZ)
	fill := func(text string) string { return Substitute(text, vars) }
	if a.Trigger.Kind == "incoming" {
		fill = func(text string) string { return incomingPrompt(text, payloads) }
	}
	if s.Own != nil {
		return fill(s.Own.Prompt), "agent", nil
	}
	if s.Preset == nil {
		return "", "", errors.New("the automation has no agent")
	}
	role := s.Preset.Role
	var orgPrompt, projectPrompt, mode *string
	if err := r.DB.InOrg(ctx, a.Org, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT
			(SELECT body FROM prompt_versions WHERE role = $1 AND project_id IS NULL ORDER BY created_at DESC, id DESC LIMIT 1),
			(SELECT body FROM prompt_versions WHERE role = $1 AND project_id = $2 ORDER BY created_at DESC, id DESC LIMIT 1),
			(SELECT mode FROM prompt_versions WHERE role = $1 AND project_id = $2 ORDER BY created_at DESC, id DESC LIMIT 1)`,
			role, a.ProjectID).Scan(&orgPrompt, &projectPrompt, &mode)
	}); err != nil {
		return "", "", err
	}
	in := delivery.InstructionsInput{}
	in.OrgPrompt = orgPrompt
	if projectPrompt != nil && mode != nil {
		in.ProjectPrompt, in.ProjectPromptMode = *projectPrompt, *mode
	}
	rolePrompt := delivery.Instructions(role, "", in)
	brief := fill(s.Brief)
	return rolePrompt + "\n\n# Your task\n\nYou were started by an automation, " + strconv.Quote(a.Name) +
		", not for a task: there is no pull request to open. You may create dude tasks and comment on GitHub as dude.\n\n" + brief, role, nil
}

// Variables are a firing's {{…}} values.
func Variables(payloads []Payload, at *time.Time, tz string) map[string]string {
	vars := map[string]string{}
	if at != nil {
		t := *at
		if loc, err := time.LoadLocation(tz); err == nil {
			t = t.In(loc)
		}
		vars["schedule.time"] = t.Format("2006-01-02 15:04 MST")
	}
	if len(payloads) == 0 {
		return vars
	}
	p := payloads[len(payloads)-1]
	ev := ReadEvent(p.Event, p.Body)
	vars["event.name"], vars["event.action"], vars["event.delivery_id"] = p.Event, ev.Action, p.DeliveryID
	vars["event.payload"] = string(p.Body)
	if len(payloads) > 1 {
		all, _ := json.Marshal(payloads)
		vars["event.payload"] = string(all)
	}
	vars["repository"], vars["branch"], vars["sender"] = ev.Repository, ev.Branch, ev.Sender
	var pr struct {
		PullRequest *struct {
			Number  int    `json:"number"`
			Title   string `json:"title"`
			HTMLURL string `json:"html_url"`
		} `json:"pull_request"`
	}
	_ = json.Unmarshal(p.Body, &pr)
	if pr.PullRequest != nil {
		vars["pr.number"], vars["pr.title"], vars["pr.url"] = strconv.Itoa(pr.PullRequest.Number), pr.PullRequest.Title, pr.PullRequest.HTMLURL
	}
	return vars
}

// Substitute replaces {{ name }} with its value; an unknown name is left as written.
func Substitute(text string, vars map[string]string) string {
	var b strings.Builder
	for {
		i := strings.Index(text, "{{")
		if i < 0 {
			b.WriteString(text)
			return b.String()
		}
		j := strings.Index(text[i:], "}}")
		if j < 0 {
			b.WriteString(text)
			return b.String()
		}
		name := strings.TrimSpace(text[i+2 : i+j])
		b.WriteString(text[:i])
		if v, ok := vars[name]; ok {
			b.WriteString(v)
		} else {
			b.WriteString(text[i : i+j+2])
		}
		text = text[i+j+2:]
	}
}

// eventEnv is a command's or server's DUDE_* environment.
func eventEnv(payloads []Payload, a Automation, runID string) map[string]string {
	env := map[string]string{"DUDE_AUTOMATION": a.ID, "DUDE_AUTOMATION_RUN": runID, "DUDE_OUTPUT_DIR": "/workspace/.dude/output"}
	if len(payloads) > 0 {
		p := payloads[len(payloads)-1]
		env["DUDE_EVENT_PATH"], env["DUDE_EVENT_NAME"], env["DUDE_EVENT_ACTION"], env["DUDE_DELIVERY_ID"] = EventPath, p.Event, p.Action, p.DeliveryID
	}
	return env
}

// EnvPath is a command's or server's file of the DUDE_* values that differ
// per firing. lux keeps a Run's env as it was first submitted, but a resume
// gives every file secret again: the command line sources this file first,
// so a resumed container sees this firing's run, delivery and event.
const EnvPath = "/workspace/.dude/env.sh"

// perFiring are the eventEnv names a resume must be able to change.
var perFiring = []string{"DUDE_AUTOMATION_RUN", "DUDE_DELIVERY_ID", "DUDE_EVENT_NAME", "DUDE_EVENT_ACTION"}

// envFile is EnvPath's content: an export line per perFiring value, each
// single-quoted for sh; one this firing has none of is unset.
func envFile(env map[string]string) string {
	var b strings.Builder
	for _, name := range perFiring {
		v, ok := env[name]
		if !ok {
			b.WriteString("unset " + name + "\n")
			continue
		}
		b.WriteString("export " + name + "='" + strings.ReplaceAll(v, "'", `'\''`) + "'\n")
	}
	return b.String()
}

func (r *Runner) toolsToken(ctx context.Context, a Automation, runID string, payloads []Payload) (string, error) {
	token, hash := agenttools.RunToken(r.Agent.ToolsKey, runID, 0)
	err := r.DB.InOrg(ctx, a.Org, func(tx pgx.Tx) error {
		scope := agenttools.AutomationScope{ProjectID: a.ProjectID, OwnerID: a.OwnerID, Repos: []string{}}
		rows, err := tx.Query(ctx, `SELECT project_id, url FROM repositories WHERE ($1 = '' OR project_id=$1) ORDER BY project_id, id`, a.ProjectID)
		if err != nil {
			return err
		}
		type coveredRepo struct{ Project, URL string }
		repos, err := pgx.CollectRows(rows, pgx.RowToStructByPos[coveredRepo])
		if err != nil {
			return err
		}
		for _, repo := range repos {
			if slug := slugOf(repo.URL); slug != "" && RepoAllowed(a.RepoFilter, slug) && inProjects(a, repo.Project) {
				scope.Repos = append(scope.Repos, slug)
			}
		}
		if scope.ProjectID == "" && a.Trigger.Kind == "incoming" && len(a.ProjectIDs) > 0 {
			// create_task files in the organisation incoming automation's first project.
			scope.ProjectID = a.ProjectIDs[0]
		}
		if len(payloads) > 0 {
			var event struct {
				Repository struct {
					FullName string `json:"full_name"`
				} `json:"repository"`
				Number int `json:"number"`
				Issue  struct {
					Number int `json:"number"`
				} `json:"issue"`
				PullRequest struct {
					Number int `json:"number"`
				} `json:"pull_request"`
			}
			_ = json.Unmarshal(payloads[len(payloads)-1].Body, &event)
			scope.EventRepo, scope.EventNumber = event.Repository.FullName, event.Number
			if event.Issue.Number > 0 {
				scope.EventNumber = event.Issue.Number
			}
			if event.PullRequest.Number > 0 {
				scope.EventNumber = event.PullRequest.Number
			}
			if scope.ProjectID == "" {
				for _, repo := range repos {
					if strings.EqualFold(slugOf(repo.URL), scope.EventRepo) && RepoAllowed(a.RepoFilter, scope.EventRepo) {
						scope.ProjectID = repo.Project
						break
					}
				}
			}
		}
		_, err = tx.Exec(ctx, `UPDATE automation_runs SET mcp_token_hash=$2, tools_scope=$3 WHERE id=$1 AND status IN ('queued', 'running')`, runID, hash, scope)
		return err
	})
	return token, err
}

// spec builds a run's lux spec; why is the reason it cannot run, in words.
func (r *Runner) spec(ctx context.Context, a Automation, runID string, payloads []Payload, at *time.Time) (lux.Spec, string, error) {
	in, why, err := r.specInput(ctx, a, runID, payloads, at, true)
	if err != nil || why != "" {
		return lux.Spec{}, why, err
	}
	var gh *forge.GitHub
	if r.Forges != nil {
		if gh, err = r.Forges.For(ctx, a.Org); err != nil {
			return lux.Spec{}, "", err
		}
	}
	spec, err := phases.AutomationSpec(ctx, r.Agent, r.Registry, gh, in)
	return spec, "", err
}

// specInput is what a run's spec is built from. withAgent adds an agent's
// prompt and tools token, which a resume gives otherwise.
func (r *Runner) specInput(ctx context.Context, a Automation, runID string, payloads []Payload, at *time.Time, withAgent bool) (phases.AutomationSpecInput, string, error) {
	in := phases.AutomationSpecInput{OrganizationID: a.Org, AutomationID: a.ID, RunID: runID, Kind: a.Kind}
	var settings agentSettings
	_ = json.Unmarshal(a.Agent, &settings)
	var imageID, sizeID, tierID, harness string
	var timeLimit int
	why := ""
	err := r.DB.InOrg(ctx, a.Org, func(tx pgx.Tx) error {
		var projectModels, orgModels json.RawMessage = []byte("{}"), nil
		if err := tx.QueryRow(ctx, `SELECT default_agent_models FROM organizations WHERE id = $1`, a.Org).Scan(&orgModels); err != nil {
			return err
		}
		if a.ProjectID != "" {
			if err := tx.QueryRow(ctx, `SELECT agent_models FROM projects WHERE id = $1`, a.ProjectID).Scan(&projectModels); err != nil {
				return err
			}
		}
		imageID, sizeID = a.ImageID, a.MachineSizeID
		if a.Kind == "agent" {
			own := settings.Own
			role := "agent"
			if settings.Preset != nil {
				role = settings.Preset.Role
				// The role's settings for this scope, as a task would get them;
				// then what the automation overrides (Q6, Q15).
				rs := delivery.ResolveRole(role, projectModels, orgModels)
				tierID, timeLimit, harness = rs.Tier, rs.TimeLimit(), rs.HarnessName()
				sizes, err := delivery.LoadSizes(ctx, tx)
				if err != nil {
					return err
				}
				if m, ok := sizes.ForRole(role, nil, projectModels, orgModels); ok {
					sizeID = m.SizeID
				}
				imageID = images.RoleImage(role, projectModels, orgModels, func(string) bool { return true })
				own = &settings.Preset.Overrides
			}
			if own != nil {
				tierID, harness = cmp.Or(own.TierID, tierID), cmp.Or(own.Harness, harness)
				sizeID, imageID = cmp.Or(own.MachineSizeID, sizeID), cmp.Or(own.ImageID, imageID)
				if own.TimeLimitMinutes > 0 {
					timeLimit = own.TimeLimitMinutes
				}
			}
			tier, reason, err := delivery.TierFor(ctx, tx, role, delivery.RoleSettings{Tier: tierID})
			if err != nil {
				return err
			}
			if reason != "" {
				why = strings.Replace(reason, "The "+delivery.RoleName(role), "The automation's agent", 1)
				return nil
			}
			in.Role, in.Harness, in.Model, in.ModelTier, in.Effort, in.Options, in.Headers = role, cmp.Or(harness, delivery.HarnessOpenCode), tier.Model, tier.Name, tier.Effort, tier.Options, tier.Headers
			if timeLimit > 0 {
				in.Timeout = strconv.Itoa(timeLimit) + "m"
			}
		}
		sizes, err := delivery.LoadSizes(ctx, tx)
		if err != nil {
			return err
		}
		if m, ok := sizes.ForPreview(sizeID); ok {
			in.Machine = &m
		}
		var site images.Site
		site.Role, site.Fallback = imageID, r.Agent.DefaultImage
		if a.ProjectID != "" {
			if err := tx.QueryRow(ctx, `SELECT COALESCE(runtime_image_id, ''), COALESCE(runtime_image, '') FROM projects WHERE id = $1`,
				a.ProjectID).Scan(&site.RuntimeID, &site.RuntimeTyped); err != nil {
				return err
			}
		}
		if err := tx.QueryRow(ctx, `SELECT COALESCE(default_image_id, '') FROM organizations WHERE id = $1`, a.Org).Scan(&site.DefaultID); err != nil {
			return err
		}
		id, ref := site.Pick()
		var got *images.RunImage
		if id != "" {
			out, err := images.Resolve(ctx, tx, id, r.Agent.Layer, "")
			if err != nil {
				return err
			}
			if out.Fail != "" || out.WaitBuild != "" {
				why = cmp.Or(out.Fail, "its image is still being built")
				return nil
			}
			ref, got = out.Ref, out.Image
		}
		in.Image, in.Nested = ref, site.Containers(got, r.Agent.NestedContainers)
		if in.Repos, err = automationRepos(ctx, tx, a, payloads); err != nil {
			return err
		}
		for i := range in.Repos {
			if in.Repos[i].Setup == "" {
				in.Repos[i].Setup = a.SetupScript
			}
		}
		if in.Secrets, err = givenSecrets(ctx, tx, a); err != nil {
			return err
		}
		var orgEgress, projectEgress []string
		if err := tx.QueryRow(ctx, `SELECT COALESCE(agent_egress, '{}') FROM organizations WHERE id = $1`, a.Org).Scan(&orgEgress); err != nil {
			return err
		}
		if a.ProjectID != "" {
			if err := tx.QueryRow(ctx, `SELECT COALESCE(agent_egress, '{}') FROM projects WHERE id = $1`, a.ProjectID).Scan(&projectEgress); err != nil {
				return err
			}
		}
		in.Egress = append(orgEgress, projectEgress...)
		return nil
	})
	if err != nil || why != "" {
		return in, why, err
	}
	switch a.Kind {
	case "agent":
		if !withAgent {
			break
		}
		prompt, _, err := r.prompt(ctx, a, payloads, at)
		if err != nil {
			return in, "", err
		}
		in.Prompt = prompt
		if r.Agent.ToolsURL != "" {
			if in.ToolsToken, err = r.toolsToken(ctx, a, runID, payloads); err != nil {
				return in, "", err
			}
		}
	case "command":
		in.Command = a.Command
	case "server":
		var cfg struct {
			Command string `json:"command"`
			Port    int    `json:"port"`
		}
		_ = json.Unmarshal(a.Server, &cfg)
		in.Command, in.Port = cfg.Command, cfg.Port
	}
	if a.Kind != "agent" {
		in.Command = ". " + EnvPath + "\n" + in.Command
		in.Env = eventEnv(payloads, a, runID)
		in.Files = map[string]string{EnvPath: envFile(in.Env)}
		if len(payloads) > 0 {
			body := []byte(payloads[len(payloads)-1].Body)
			if len(payloads) > 1 {
				body, _ = json.Marshal(payloads)
			}
			in.Files[EventPath] = string(body)
		}
	}
	return in, "", nil
}

// automationRepos are what a run checks out: a project automation's
// repositories (all, none or the ones named); an organisation's on a
// schedule, none when its repos is "none", else every project's its filter
// covers; on an event, the event's repository when one of the organisation's.
func automationRepos(ctx context.Context, tx pgx.Tx, a Automation, payloads []Payload) ([]phases.AutomationRepo, error) {
	if a.ProjectID == "" && a.Trigger.Kind == "incoming" {
		return incomingRepos(ctx, tx, a)
	}
	type repo struct{ ID, Name, URL, Branch, Setup string }
	rows, err := tx.Query(ctx, `SELECT id, name, url, default_branch, COALESCE(setup_script, '') FROM repositories
		WHERE ($1 = '' OR project_id = $1) ORDER BY project_id, name`, a.ProjectID)
	if err != nil {
		return nil, err
	}
	all, err := pgx.CollectRows(rows, pgx.RowToStructByPos[repo])
	if err != nil {
		return nil, err
	}
	var picked []repo
	var named []string
	switch {
	case a.ProjectID != "":
		var which any
		_ = json.Unmarshal(a.Repos, &which)
		switch v := which.(type) {
		case string:
			if v == "all" {
				picked = all
			}
		case []any:
			for _, x := range v {
				if s, ok := x.(string); ok {
					named = append(named, s)
				}
			}
			for _, r := range all {
				if contains(named, r.ID) {
					picked = append(picked, r)
				}
			}
		}
	case len(payloads) > 0:
		ev := ReadEvent(payloads[len(payloads)-1].Event, payloads[len(payloads)-1].Body)
		for _, r := range all {
			if strings.EqualFold(slugOf(r.URL), ev.Repository) && RepoAllowed(a.RepoFilter, ev.Repository) {
				picked = append(picked, r)
				break
			}
		}
	default:
		// An organisation schedule's Checked out None: an empty workspace.
		if string(a.Repos) == `"none"` {
			break
		}
		for _, r := range all {
			if RepoAllowed(a.RepoFilter, slugOf(r.URL)) {
				picked = append(picked, r)
			}
		}
	}
	out := make([]phases.AutomationRepo, 0, len(picked))
	seen := map[string]bool{}
	for _, r := range picked {
		if seen[lux.SpecName(r.Name)] {
			continue
		}
		seen[lux.SpecName(r.Name)] = true
		out = append(out, phases.AutomationRepo{Name: r.Name, URL: r.URL, Ref: r.Branch, Setup: r.Setup})
	}
	return out, nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// givenSecrets are the values a run gets, as env: its picks or its preset
// role's, as automation_secrets_given (migration 210) resolves them.
func givenSecrets(ctx context.Context, tx pgx.Tx, a Automation) ([]lux.Secret, error) {
	rows, err := tx.Query(ctx, `SELECT name, value FROM automation_secrets_given($1)`, a.ID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (lux.Secret, error) {
		s := lux.Secret{As: "env"}
		return s, row.Scan(&s.Name, &s.Value)
	})
}

func slugOf(url string) string { return forge.SlugFromURL(url) }

// RunNow fires an automation on a person's word: on a schedule, nothing
// (it queues behind a live run, Q7); with a past delivery, that delivery;
// with a written test event, the event (always its own run, Q21). Refused
// with the filter's name when the event would not match.
func (r *Runner) RunNow(ctx context.Context, org, automationID, person string, deliveryID string, test *Payload) (string, Verdict, error) {
	var runID string
	var verdict Verdict
	err := r.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		a, err := Load(ctx, tx, automationID)
		if err != nil {
			return err
		}
		if a.Trigger.Kind == "schedule" {
			if deliveryID != "" || test != nil {
				return fmt.Errorf("%w: a scheduled automation runs on no event", ErrBadRequest)
			}
			now := r.now()
			// Run now queues or starts (Q7), never cancels: nothing to end.
			fired, err := Fire(ctx, tx, a, SourceRunNow, nil, person, &now)
			runID, verdict = fired.Run, Verdict{Matches: true}
			return err
		}
		var p Payload
		switch {
		case test != nil:
			p = *test
		case deliveryID != "":
			if err := tx.QueryRow(ctx, `SELECT id, event, payload FROM webhook_deliveries WHERE id = $1`, deliveryID).Scan(&p.DeliveryID, &p.Event, &p.Body); err != nil {
				if db.IsNotFound(err) {
					return fmt.Errorf("%w: no stored delivery %s", ErrNotFound, deliveryID)
				}
				return err
			}
		default:
			return fmt.Errorf("%w: an event automation runs on a past delivery or a test event", ErrBadRequest)
		}
		ev := ReadEvent(p.Event, p.Body)
		p.Action = ev.Action
		if verdict, err = r.Evaluate(ctx, tx, a, ev); err != nil || !verdict.Matches {
			return err
		}
		fired, err := Fire(ctx, tx, a, SourceTest, []Payload{p}, person, nil)
		runID = fired.Run
		return err
	})
	return runID, verdict, err
}

// Refusals RunNow's callers answer with a 400 or 404.
var (
	ErrBadRequest = errors.New("bad request")
	ErrNotFound   = errors.New("not found")
)
