package servers

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
	"github.com/marciomartins/dude/orchestrator/internal/phases"
	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

// Wakeable previews (runs.wakeable): a preview is lux servers of its own,
// one per project server marked to start in previews, at a hostname dude
// chooses, that wake on request. Declaring one creates the servers and
// nothing else. lux says on its feed (Feed) when someone opens one with
// nothing serving it, and when one has gone unused; the sweep acts:
//
//	pending ─servers created─▶ paused (asleep, no Run yet)
//	paused ─wake─▶ scheduled (Run resumed with a sync, or submitted and the servers attached) ─lux─▶ running
//	running ─every server idle─▶ paused (Run stopped)
//	running ─branch moved─▶ running (POST /sync)
//	any ─DELETE, task over, unused past ReapAfter, servers gone─▶ completed (servers deleted, then Run cancelled)
//
// Every step is keyed on durable columns, as the old path's are: a wake is
// acted on by one orchestrator (wake_claimed_at), a resume and a submit
// carry keys lux deduplicates, and the feed's events apply once each.

// The bounds of a wakeable preview's lux servers.
const (
	// lux's own expiry, the safety net behind dude's reaper.
	previewExpireAfter = "720h"
	// How long a wake is held open for dude before lux says "no answer".
	previewWakeTimeout = "5m"
	// A wake another orchestrator claimed and has not finished is taken
	// over after this.
	wakeClaimFor = 2 * time.Minute
	// How soon a running preview with one server idle and another in use
	// is checked again.
	parkCheckEvery = 30 * time.Second
	// How often a running preview with no idle mark is checked for servers
	// lux never reports idle (never ready: starting, unreachable).
	parkSweepEvery = 5 * time.Minute
	// Starts in a row of a preview's Run that may fail before it runs
	// before dude stops trying new ones by itself (startFailed).
	previewStartAttempts = 3
)

// wakeRun is a wakeable preview as the sweep reads it.
type wakeRun struct {
	previewRun
	WakeWanted, SyncWanted *time.Time
	Generation             int
	Repos                  []string
	// Servers lux has for it, not deleted; and how many have an unanswered
	// idle.
	Servers, Idle int
	// Its servers were last woken longer ago than the reap age.
	Reap bool
	// Something to clean up in lux: servers not deleted, a Run not cancelled.
	LuxLeft bool
	// Its servers are to be checked for idleness (wakeableSelect's park_due).
	ParkDue bool
	// Starts of its lux Run in a row that failed before running.
	StartFailures int
	// lux_start_event before the request this wake's acknowledgement
	// (woken) answers: once it has moved, an event of that start or a later
	// one was applied, and is newer than the answer.
	StartBefore int64
	// lux_after_event when the wake was claimed, before lux was asked what
	// the Run is: an attach-only acknowledgement is older than any event
	// applied past it.
	ObservedAfter int64
}

// wakeableSelect: wakeable previews with something to do. $1 is the reap
// age, $2 parkCheckEvery, $3 wakeClaimFor and $5 parkSweepEvery, in
// seconds; $4 the page size. The inner query computes each row's flags once
// (its servers' counts in one lateral aggregate); the outer one keeps the
// rows with something to do and sorts them: a wake, a sync or an end due
// first (someone may be waiting on it), then a park check due, then the
// running previews that are only re-followed.
//
// The status/lux_stop_reason conjunct is what runs_wakeable_live_idx and
// runs_wakeable_open_idx (065) serve, so the scan follows the live previews
// and not every preview ever made. An ended preview is taken until endInLux
// marks it lux_stop_reason = 'cancel' (nothing of it left in lux).
const wakeableSelect = `SELECT id, organization_id, project_id, task_id, status, lux_run_id, lux_state, task_ended,
		wake_wanted_at, sync_wanted_at, lux_generation, lux_repositories, live, idle, reap, lux_left, park_due, start_failures
	FROM (SELECT r.id, r.organization_id, r.project_id, r.task_id, r.status::text AS status, r.created_at, r.start_failures,
			COALESCE(r.lux_run_id, '') AS lux_run_id, COALESCE(r.lux_state, '') AS lux_state,
			t.status IN ('done', 'failed', 'aborted') AS task_ended,
			r.wake_wanted_at, r.sync_wanted_at, r.lux_generation, r.lux_repositories, ps.live, ps.idle,
			r.wake_wanted_at IS NOT NULL
				AND (r.wake_claimed_at IS NULL OR r.wake_claimed_at < now() - make_interval(secs => $3)) AS wake_due,
			r.status IN ('pending', 'paused')
				AND COALESCE(r.lux_state, '') NOT IN ('running', 'starting', 'resuming', 'scheduled', 'submitted', 'stopping')
				AND COALESCE(ps.woken, r.created_at) < now() - make_interval(secs => $1) AS reap,
			ps.live > 0 OR (r.lux_run_id IS NOT NULL AND r.lux_stop_reason IS DISTINCT FROM 'cancel') AS lux_left,
			-- A new idle mark, or every server marked: checked at once; marks
			-- already checked, every parkCheckEvery; none, every
			-- parkSweepEvery, for servers lux never reports idle.
			r.status = 'running' AND ps.live > 0 AND (r.park_checked_at IS NULL
				OR r.park_checked_at < now() - make_interval(secs => $5)
				OR (ps.idle > 0 AND (ps.idle = ps.live OR ps.idle_at > r.park_checked_at
					OR r.park_checked_at < now() - make_interval(secs => $2)))) AS park_due
		FROM runs r JOIN tasks t ON t.id = r.task_id
		CROSS JOIN LATERAL (SELECT count(*) FILTER (WHERE s.deleted_at IS NULL) AS live,
				count(*) FILTER (WHERE s.deleted_at IS NULL AND s.idle_at IS NOT NULL) AS idle,
				max(s.last_woken_at) AS woken, max(s.idle_at) FILTER (WHERE s.deleted_at IS NULL) AS idle_at
			FROM preview_servers s WHERE s.run_id = r.id) ps
		WHERE r.kind = 'preview' AND r.wakeable
		  AND (r.status IN ('pending', 'scheduled', 'starting', 'running', 'paused') OR r.lux_stop_reason IS DISTINCT FROM 'cancel')
		  AND (r.next_attempt_at IS NULL OR r.next_attempt_at <= now())) w
	WHERE status IN ('pending', 'completed', 'failed', 'aborted') OR task_ended OR wake_due OR sync_wanted_at IS NOT NULL OR reap
	   OR (status IN ('scheduled', 'starting', 'running') AND lux_run_id <> '')
	   OR (status = 'paused' AND lux_run_id <> '' AND lux_state NOT IN ('stopped', 'failed', 'lost'))
	ORDER BY (status NOT IN ('scheduled', 'starting', 'running') OR task_ended OR wake_due OR sync_wanted_at IS NOT NULL) DESC,
	         park_due DESC, created_at
	LIMIT $4`

// The page a wakeable sweep takes at most; Previews.SweepLimit overrides it.
const wakeableLimit = 1000

func (p *Previews) reapAfter() time.Duration {
	if p.ReapAfter > 0 {
		return p.ReapAfter
	}
	return 7 * 24 * time.Hour
}

// sweepWakeable takes one pass over the wakeable previews with something to do.
func (p *Previews) sweepWakeable(ctx context.Context) (int, error) {
	limit := p.SweepLimit
	if limit <= 0 {
		limit = wakeableLimit
	}
	var runs []wakeRun
	if err := p.DB.InSystem(ctx, "previews", func(tx pgx.Tx) error {
		// The planner multiplies the correlated subplans' estimates by the
		// page; past jit_above_cost every 1s sweep would be JIT-compiled
		// (~40ms each) for a query that runs in ~20ms.
		if _, err := tx.Exec(ctx, `SET LOCAL jit = off`); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, wakeableSelect, p.reapAfter().Seconds(), parkCheckEvery.Seconds(), wakeClaimFor.Seconds(), limit,
			parkSweepEvery.Seconds())
		if err != nil {
			return err
		}
		runs, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (wakeRun, error) {
			var r wakeRun
			return r, row.Scan(&r.ID, &r.Org, &r.ProjectID, &r.TaskID, &r.Status, &r.LuxRunID, &r.LuxState, &r.TaskEnded,
				&r.WakeWanted, &r.SyncWanted, &r.Generation, &r.Repos, &r.Servers, &r.Idle, &r.Reap, &r.LuxLeft, &r.ParkDue,
				&r.StartFailures)
		})
		return err
	}); err != nil {
		return 0, err
	}
	var acted atomic.Int64
	var wg sync.WaitGroup
	slots := make(chan struct{}, 8)
	for _, r := range runs {
		wg.Add(1)
		slots <- struct{}{}
		go func() {
			defer func() { <-slots; wg.Done() }()
			did, err := p.advanceWakeable(ctx, r)
			if err != nil {
				p.Log.Warn("wakeable preview sync failed", "run", r.ID, "error", err)
				if rerr := p.retryLater(ctx, r.previewRun); rerr != nil {
					p.Log.Warn("backing a preview off failed", "run", r.ID, "error", rerr)
				}
				return
			}
			if did {
				acted.Add(1)
			}
		}()
	}
	wg.Wait()
	return int(acted.Load()), nil
}

func (p *Previews) advanceWakeable(ctx context.Context, r wakeRun) (bool, error) {
	live := r.Status != "completed" && r.Status != "failed" && r.Status != "aborted"
	switch {
	case !live:
		return r.LuxLeft, p.endInLux(ctx, r)
	case r.TaskEnded:
		return true, p.complete(ctx, r, "task ended", ledger.ActorSystem, r.ID)
	case r.Reap:
		return true, p.complete(ctx, r, "unused", ledger.ActorSystem, r.ID)
	case r.Status == "pending":
		return true, p.createServers(ctx, r)
	}
	did := false
	if r.WakeWanted != nil {
		woke, err := p.wake(ctx, r)
		if err != nil {
			return true, err
		}
		did = did || woke
	}
	if r.SyncWanted != nil {
		synced, err := p.syncRunning(ctx, r)
		if err != nil {
			return true, err
		}
		did = did || synced
	}
	if r.Status == "running" && r.ParkDue && r.Servers > 0 && r.WakeWanted == nil {
		parked, err := p.parkIfIdle(ctx, r)
		if err != nil {
			return true, err
		}
		did = did || parked
	}
	if r.Status == "paused" && r.LuxRunID != "" && r.LuxState == "running" && r.WakeWanted == nil {
		// Parked, and lux has not said it is stopping: a stop that failed
		// is asked again (idempotent), without keeping the loop from resting.
		if err := p.stop(ctx, r.previewRun); err != nil {
			return did, err
		}
	}
	if r.LuxRunID != "" && (r.Status != "paused" || !lux.Terminal(r.LuxState)) {
		p.follow(r.previewRun)
	}
	return did, nil
}

// previewServer is one of a wakeable preview's lux servers.
type previewServer struct {
	Name, LuxID, Hostname string
	IdleAt                *time.Time
	IdleLastRequestAt     *time.Time
}

func (p *Previews) previewServers(ctx context.Context, org, runID string) ([]previewServer, error) {
	var out []previewServer
	err := p.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT name, lux_server_id, hostname, idle_at, idle_last_request_at FROM preview_servers
			WHERE run_id = $1 AND deleted_at IS NULL ORDER BY name`, runID)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (previewServer, error) {
			var s previewServer
			return s, row.Scan(&s.Name, &s.LuxID, &s.Hostname, &s.IdleAt, &s.IdleLastRequestAt)
		})
		return err
	})
	return out, err
}

// createServers makes the preview's lux servers, one per project server
// marked to start in previews, then leaves it asleep. Each is looked for
// by its hostname first, so a create whose answer was lost is not made
// twice; a hostname another preview holds (409 hostname_taken) is chosen
// again once, salted with this preview's id.
func (p *Previews) createServers(ctx context.Context, r wakeRun) error {
	var recipes []Recipe
	var settings PreviewSettings
	if err := p.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		var err error
		if recipes, err = LoadRecipes(ctx, tx, r.ProjectID); err != nil {
			return err
		}
		return loadSettings(ctx, tx, r.ProjectID, &settings)
	}); err != nil {
		return err
	}
	have, err := p.previewServers(ctx, r.Org, r.ID)
	if err != nil {
		return err
	}
	primary, err := p.primaryRepoName(ctx, r)
	if err != nil {
		return err
	}
	for _, rc := range recipes {
		if !rc.Autostart || slices.ContainsFunc(have, func(s previewServer) bool { return s.Name == rc.Name }) {
			continue
		}
		in, err := rc.Input(primary)
		if err != nil {
			p.Log.Warn("a preview server lux would refuse was left out", "run", r.ID, "server", rc.Name, "error", err)
			continue
		}
		sv, err := p.createServer(ctx, r, in, settings)
		if le, ok := lux.AsError(err); ok && !le.Retryable() {
			return p.fail(ctx, r.previewRun, fmt.Sprintf("lux refused preview server %s: %s", rc.Name, le.Message))
		}
		if err != nil {
			return err
		}
		var kept string
		if err := p.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `INSERT INTO preview_servers (run_id, organization_id, name, lux_server_id, hostname, url)
				VALUES ($1, $2, $3, $4, $5, $6)
				ON CONFLICT (run_id, name) DO UPDATE SET name = preview_servers.name
				RETURNING lux_server_id`,
				r.ID, r.Org, rc.Name, sv.ID, deref(sv.Hostname), sv.URL).Scan(&kept)
		}); err != nil {
			return err
		}
		if kept != sv.ID {
			// Another orchestrator recorded its own server for this one
			// first: the one made here is an orphan, deleted.
			if err := p.Lux.DeleteServer(ctx, sv.ID); err != nil && !lux.IsNotFound(err) {
				return err
			}
		}
	}
	return p.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE runs SET status = 'paused', dude_pause = 'unused', next_attempt_at = NULL
			WHERE id = $1 AND status = 'pending'`, r.ID)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		return phases.ServersChanged(ctx, tx, r.Org, r.ProjectID, r.TaskID, r.ID, map[string]any{"change": "asleep"})
	})
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// createServer creates (or finds) one preview server in lux: at its
// hostname, else, when another preview holds that (409 hostname_taken), at
// the one salted with this preview's id. Each attempt first adopts a server
// this preview already has there: a create whose answer was lost, or one
// another orchestrator made for it meanwhile.
func (p *Previews) createServer(ctx context.Context, r wakeRun, in lux.ServerInput, settings PreviewSettings) (lux.TenantServer, error) {
	labels := map[string]string{"dude.org": r.Org, "dude.project": r.ProjectID, "dude.task": r.TaskID,
		"dude.preview": r.ID, "dude.kind": KindPreview}
	body := lux.CreateServer{Name: in.Name, Port: in.Port, Command: in.Command, Workdir: in.Workdir, Env: in.Env,
		Labels: labels, Wake: "request", Lifetime: "owner", IdleAfter: idleAfter(settings.IdleTimeoutMinutes),
		WakeTimeout: previewWakeTimeout, ExpireAfter: previewExpireAfter}
	plain := PreviewHostname(p.PreviewDomain, in.Name, r.TaskID, r.ProjectID, "")
	sv, err := p.adoptOrCreate(ctx, r, body, plain)
	if le, ok := lux.AsError(err); !ok || le.Code != "hostname_taken" {
		return sv, err
	}
	// Taken: by another orchestrator creating this very server (adopted
	// now), or by another preview (salted).
	if sv, found, err := p.adopt(ctx, r, in.Name, plain); err != nil || found {
		return sv, err
	}
	p.Log.Warn("a preview hostname is taken; choosing another", "run", r.ID, "hostname", plain)
	return p.adoptOrCreate(ctx, r, body, PreviewHostname(p.PreviewDomain, in.Name, r.TaskID, r.ProjectID, r.ID))
}

func (p *Previews) adoptOrCreate(ctx context.Context, r wakeRun, body lux.CreateServer, hostname string) (lux.TenantServer, error) {
	if sv, found, err := p.adopt(ctx, r, body.Name, hostname); err != nil || found {
		return sv, err
	}
	body.Hostname = hostname
	return p.Lux.CreateServer(ctx, body)
}

// adopt finds this preview's server of a name at a hostname.
func (p *Previews) adopt(ctx context.Context, r wakeRun, name, hostname string) (lux.TenantServer, bool, error) {
	found, err := p.Lux.ListServers(ctx, hostname)
	if err != nil {
		return lux.TenantServer{}, false, err
	}
	for _, sv := range found {
		if sv.Labels["dude.preview"] == r.ID && sv.Name == name {
			return sv, true, nil
		}
	}
	return lux.TenantServer{}, false, nil
}

// idleAfter is the project's idle limit as lux takes it.
func idleAfter(minutes float64) string {
	if minutes <= 0 {
		minutes = 30
	}
	return (time.Duration(minutes * float64(time.Minute))).String()
}

func loadSettings(ctx context.Context, tx pgx.Tx, projectID string, out *PreviewSettings) error {
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT preview_settings(p) FROM projects p WHERE p.id = $1`, projectID).Scan(&raw); err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

// primaryRepoName is the repository the preview's servers run in: the
// first of its task's, else of its project's.
func (p *Previews) primaryRepoName(ctx context.Context, r wakeRun) (string, error) {
	refs, err := p.refs(ctx, r.previewRun)
	if err != nil || len(refs) == 0 {
		return "", err
	}
	return refs[0].Name, nil
}

// claimWake takes the wake the sweep selected for this orchestrator, once
// it is due: false when another holds it, it was done meanwhile, or it was
// replaced (startFailed's next wake, after its backoff, is the next sweep's).
func (p *Previews) claimWake(ctx context.Context, r *wakeRun) (bool, error) {
	var ok bool
	err := p.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `UPDATE runs SET wake_claimed_at = now() WHERE id = $1 AND wake_wanted_at = $3
			AND (next_attempt_at IS NULL OR next_attempt_at <= now())
			AND (wake_claimed_at IS NULL OR wake_claimed_at < now() - make_interval(secs => $2))
			RETURNING lux_start_event, lux_after_event`, r.ID, wakeClaimFor.Seconds(), *r.WakeWanted).
			Scan(&r.StartBefore, &r.ObservedAfter)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		ok = err == nil
		return err
	})
	return ok, err
}

// releaseWake lets go of this wake's claim, to be tried after retryIn. A
// wake replaced meanwhile (startFailed cleared the claim and set its own
// backoff) is left as it is.
func (p *Previews) releaseWake(ctx context.Context, r wakeRun, retryIn time.Duration) error {
	return p.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE runs SET wake_claimed_at = NULL,
			next_attempt_at = now() + make_interval(secs => $2) WHERE id = $1 AND wake_wanted_at = $3`,
			r.ID, retryIn.Seconds(), *r.WakeWanted)
		return err
	})
}

// wake brings a Run up for an asleep preview someone opened: its Run
// resumed with every checkout synced to the task's branch, or, with no Run
// that can run again, a new one submitted and the servers attached to it.
func (p *Previews) wake(ctx context.Context, r wakeRun) (bool, error) {
	claimed, err := p.claimWake(ctx, &r)
	if err != nil || !claimed {
		return false, err
	}
	if err := p.wakeClaimed(ctx, r); err != nil {
		// Not left claimed for wakeClaimFor: tried again after the backoff.
		_ = p.releaseWake(ctx, r, 5*time.Second)
		return true, err
	}
	return true, nil
}

func (p *Previews) wakeClaimed(ctx context.Context, r wakeRun) error {
	if r.LuxRunID != "" {
		lr, err := p.Lux.Get(ctx, r.LuxRunID)
		switch {
		case lux.IsNotFound(err):
			// lux lost it: a new one.
		case err != nil:
			return err
		default:
			switch lr.State {
			case "stopping":
				// An idle stop under way: resumed once it has stopped. lux
				// does not ask again while this wake is open.
				return p.releaseWake(ctx, r, time.Second)
			case "failed", "lost":
				// Whether its last start ran is decided by what lux reported
				// of it, applied first: a failed start the feed has not
				// applied yet is counted now, and moves the wake on.
				if err := p.drain(ctx, r); err != nil {
					return err
				}
				ran, wake, _, err := p.startState(ctx, r)
				if err != nil {
					return err
				}
				if wake == nil || !wake.Equal(*r.WakeWanted) {
					// startFailed took this wake over (a new one, after its
					// backoff, or none past the bound) and cleared the claim.
					return nil
				}
				if ran {
					// It ran, then crashed or lost its host: lux resumes it
					// from its snapshot.
					return p.resumeWoken(ctx, r, lr)
				}
				// Its last start failed before it ran: resuming it again
				// repeats what failed (its container, its placement). A
				// new one below; this one is cancelled so lux drops what
				// it kept to resume it.
				p.Log.Warn("a preview's Run failed to start; submitting a new run", "run", r.ID, "luxRun", r.LuxRunID,
					"state", lr.State, "startFailures", r.StartFailures)
				p.unfollow(r.ID)
				if err := p.Lux.Cancel(ctx, r.LuxRunID); err != nil {
					if le, ok := lux.AsError(err); !ok || le.Retryable() {
						return err
					}
				}
			case "stopped":
				return p.resumeWoken(ctx, r, lr)
			case "succeeded", "cancelled":
				// Never runs again: a new one below.
			default:
				// On its way up or running already: its servers come with it.
				if err := p.attachAll(ctx, r, r.LuxRunID); err != nil {
					return p.attachFailed(ctx, r, r.LuxRunID, err)
				}
				return p.woken(ctx, r, r.LuxRunID, lr.State, false)
			}
		}
		if err := p.retireRun(ctx, r); err != nil {
			return err
		}
		r.LuxRunID, r.LuxState = "", ""
		r.Generation++
	}
	return p.submitWoken(ctx, r)
}

// resumeWoken resumes a stopped preview Run, syncing its checkouts.
func (p *Previews) resumeWoken(ctx context.Context, r wakeRun, lr lux.Run) error {
	phases.RecordMemoryLimit(ctx, p.DB, p.Log, r.Org, r.ID, lr)
	login, err := phases.LoginFor(ctx, p.Registry, lr.Spec.Image.Ref, &lr.Spec)
	if phases.IsLoginUnavailable(err) {
		p.Log.Warn("preview not woken: "+err.Error(), "run", r.ID)
		return p.releaseWake(ctx, r, phases.LoginRetry)
	}
	if err != nil {
		_ = p.releaseWake(ctx, r, 5*time.Second)
		return err
	}
	var spec lux.Spec
	token, err := p.forgeToken(ctx, r.Org)
	if err != nil {
		_ = p.releaseWake(ctx, r, 5*time.Second)
		return err
	}
	if token != "" {
		spec.Secrets = []lux.Secret{{Name: "GIT_TOKEN", Value: token}}
	}
	login.Apply(&spec)
	sync, err := p.syncRefs(ctx, r)
	if err != nil {
		_ = p.releaseWake(ctx, r, 5*time.Second)
		return err
	}
	// Attached before the resume, so lux starts them on its placement. A
	// failed Run takes none (409 finished) until it is resumed: then after.
	attachAfter := false
	if err := p.attachAll(ctx, r, r.LuxRunID); err != nil {
		if le, ok := lux.AsError(err); !ok || le.Code != "finished" {
			_ = p.releaseWake(ctx, r, 5*time.Second)
			return err
		}
		attachAfter = true
	}
	// After what drain applied of the Run's earlier starts: their ends are
	// older than this resume's answer, whenever they reached dude.
	if err := p.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT lux_start_event FROM runs WHERE id = $1`, r.ID).Scan(&r.StartBefore)
	}); err != nil {
		_ = p.releaseWake(ctx, r, 5*time.Second)
		return err
	}
	res, err := p.Lux.Resume(ctx, r.LuxRunID, lux.ResumeInput{Secrets: spec.Secrets, Sync: sync,
		RequestID: fmt.Sprintf("wake-%s-%d", r.ID, r.WakeWanted.UnixMilli())})
	if le, ok := lux.AsError(err); ok && le.Status == http.StatusConflict {
		// lux answers a resume of a Run resuming already with 2xx; a 409
		// (no_snapshot, not_resumable) is a resume not done. What the Run
		// is now decides.
		lr, gerr := p.Lux.Get(ctx, r.LuxRunID)
		switch {
		case gerr != nil && !lux.IsNotFound(gerr):
			_ = p.releaseWake(ctx, r, 5*time.Second)
			return gerr
		case gerr == nil && slices.Contains([]string{"scheduled", "starting", "resuming", "running"}, lr.State):
			return p.woken(ctx, r, r.LuxRunID, lr.State, true)
		case gerr == nil && lr.State == "stopping":
			return p.releaseWake(ctx, r, time.Second)
		}
	}
	if le, ok := lux.AsError(err); ok && !le.Retryable() {
		// A Run lux will not resume: a new one now.
		p.Log.Warn("lux refused to resume a preview; submitting a new run", "run", r.ID, "error", le.Message)
		if err := p.retireRun(ctx, r); err != nil {
			return err
		}
		r.LuxRunID, r.LuxState = "", ""
		r.Generation++
		return p.submitWoken(ctx, r)
	}
	if err != nil {
		_ = p.releaseWake(ctx, r, 5*time.Second)
		return err
	}
	if attachAfter {
		if err := p.attachAll(ctx, r, r.LuxRunID); err != nil {
			return p.attachFailed(ctx, r, r.LuxRunID, err)
		}
	}
	return p.woken(ctx, r, r.LuxRunID, res.State, true)
}

// syncRefs is a sync of every repository the preview's Run holds to the
// ref it previews now.
func (p *Previews) syncRefs(ctx context.Context, r wakeRun) ([]lux.SyncRef, error) {
	refs, err := p.refs(ctx, r.previewRun)
	if err != nil {
		return nil, err
	}
	var out []lux.SyncRef
	for _, rf := range refs {
		if len(r.Repos) == 0 || slices.Contains(r.Repos, rf.Name) {
			out = append(out, lux.SyncRef{Repo: rf.Name, Ref: rf.Ref})
		}
	}
	return out, nil
}

// drain applies what lux reported of a preview's Run that has ended and
// dude has not applied yet, here and now, rather than leaving it to a
// follower: whether its start ran decides what a wake does with it, and a
// failed start is counted once (luxEvent) wherever it is first applied.
// lux's stream of an ended Run ends after its last event, or after one page
// of a long backlog, and its end frame does not say which; nor does the
// Run's state, which a later start can end in again. So the Run is drained
// until a pass from the stored cursor reaches lux's end with no event past
// it, and the state applied is the Run's state in lux then; short of that
// within drainPasses, an error (the wake is tried again later).
func (p *Previews) drain(ctx context.Context, r wakeRun) error {
	for range drainPasses {
		dctx, cancel := context.WithTimeout(ctx, cmp.Or(p.DrainFor, drainFor))
		sent, err := p.followEvents(dctx, r.previewRun)
		cancel()
		if err != nil {
			return err
		}
		current, applied, err := p.appliedState(ctx, r)
		if err != nil || !current {
			return err
		}
		if sent > 0 {
			continue
		}
		lr, err := p.Lux.Get(ctx, r.LuxRunID)
		if err != nil {
			return err
		}
		if applied == lux.Recorded(lr.State, lr.StateReason) {
			return nil
		}
	}
	return fmt.Errorf("lux run %s: no pass of its events reached their end within %d passes", r.LuxRunID, drainPasses)
}

// appliedState is whether the wake's lux Run is still the preview's, and
// the state dude applied of it.
func (p *Previews) appliedState(ctx context.Context, r wakeRun) (current bool, applied string, err error) {
	err = p.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT lux_run_id IS NOT DISTINCT FROM $2, COALESCE(lux_state, '') FROM runs WHERE id = $1`,
			r.ID, r.LuxRunID).Scan(&current, &applied)
	})
	return current, applied, err
}

// drain's bounds: each pass's stream, and the passes.
const (
	drainFor    = 30 * time.Second
	drainPasses = 5
)

// startState is what dude has applied of the preview's Run: whether its
// current start ran, and the wake it holds now.
func (p *Previews) startState(ctx context.Context, r wakeRun) (ran bool, wake *time.Time, claimed bool, err error) {
	err = p.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT lux_ran_event >= lux_start_event, wake_wanted_at, wake_claimed_at IS NOT NULL
			FROM runs WHERE id = $1`, r.ID).Scan(&ran, &wake, &claimed)
	})
	return ran, wake, claimed, err
}

// startFailed records the nth start in a row of a wakeable preview's Run
// that ended (failed, lost) before it ran, in the transaction that saw it
// end, and returns the error the preview shows. The next wake submits a new
// Run (wake), since resuming this one repeats what failed. Below
// previewStartAttempts the wake is asked for here, after workflow.Backoff,
// so whoever opened the URL and is on lux's waking page gets the new Run;
// from then on only a new request (lux's next server.wake_requested, or a
// person starting a server) tries again, once each.
func (p *Previews) startFailed(ctx context.Context, tx pgx.Tx, r previewRun, n int, reason string) (string, error) {
	retry := n < previewStartAttempts
	why := fmt.Sprintf("the preview's Run failed to start (%s)", reason)
	if retry {
		why += fmt.Sprintf("; trying a new Run (start %d of %d)", n+1, previewStartAttempts)
	} else {
		why += fmt.Sprintf(" %d times in a row; starting a server, or opening a URL once lux stops waiting, tries a new Run", n)
	}
	// The wake that made this start is done with either way: below the
	// bound a new one (a new wake_wanted_at) after the backoff, else none.
	// The start's own acknowledgement (woken) is of the old one and so
	// writes nothing over this.
	_, err := tx.Exec(ctx, `UPDATE runs SET start_failures = $2, error = $3,
		wake_wanted_at = CASE WHEN $4 THEN GREATEST(clock_timestamp(), COALESCE(wake_wanted_at, '-infinity') + interval '1 millisecond') END,
		wake_claimed_at = NULL,
		next_attempt_at = CASE WHEN $4 THEN now() + make_interval(secs => $5) ELSE next_attempt_at END
		WHERE id = $1`, r.ID, n, why, retry, workflow.Backoff(n).Seconds())
	return why, err
}

// retireRun forgets a preview's lux Run that can never run again (or that
// lux no longer has), so the next submit makes another: the generation in
// the submit's key moves on.
func (p *Previews) retireRun(ctx context.Context, r wakeRun) error {
	p.unfollow(r.ID)
	return p.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE runs SET lux_run_id = NULL, lux_state = NULL, lux_after_event = 0, lux_start_event = 0, lux_ran_event = 0, lux_stop_reason = NULL,
			lux_generation = lux_generation + 1 WHERE id = $1 AND lux_run_id = $2`, r.ID, r.LuxRunID)
		return err
	})
}

// submitWoken submits the preview's Run (a servers-only spec: its servers
// are lux's own, attached) and attaches every server to it.
func (p *Previews) submitWoken(ctx context.Context, r wakeRun) error {
	spec, branch, machine, err := p.spec(ctx, r.previewRun)
	if phases.IsLoginUnavailable(err) {
		return p.releaseWake(ctx, r, phases.LoginRetry)
	}
	if err != nil {
		_ = p.releaseWake(ctx, r, 5*time.Second)
		return err
	}
	spec.Workload.Servers = nil
	spec.Labels["dude.preview"] = r.ID
	phases.NamePool(ctx, p.Lux, machine)
	lr, err := p.Lux.Submit(ctx, spec, fmt.Sprintf("%s/%d", r.ID, r.Generation))
	if reason := phases.PoolGone(err, machine); reason != "" {
		return p.fail(ctx, r.previewRun, reason)
	}
	if le, ok := lux.AsError(err); ok && !le.Retryable() {
		return p.fail(ctx, r.previewRun, "lux refused the preview: "+le.Message)
	}
	if err != nil {
		_ = p.releaseWake(ctx, r, 5*time.Second)
		return err
	}
	var repos []string
	if spec.Git != nil {
		for _, repo := range spec.Git.Repositories {
			repos = append(repos, repo.Name)
		}
	}
	if err := p.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		// machine: the size this lux Run was submitted on; a later generation
		// records its own. Its first start begins here, before any of its
		// events (lux_start_event 1), so a failure before its first running
		// is a failed start; StartBefore 0 makes every event of it newer
		// than the submit's answer.
		_, err := tx.Exec(ctx, `UPDATE runs SET lux_run_id = $2, lux_state = $3, lux_repositories = $4, branch = NULLIF($5, ''),
			machine = $7::jsonb, started_at = COALESCE(started_at, now()), lux_start_event = 1
			WHERE id = $1 AND lux_run_id IS NULL AND lux_generation = $6`, r.ID, lr.ID, lr.State, db.NonNil(repos), branch, r.Generation, machine)
		return err
	}); err != nil {
		return err
	}
	r.StartBefore = 0
	if err := p.attachAll(ctx, r, lr.ID); err != nil {
		return p.attachFailed(ctx, r, lr.ID, err)
	}
	return p.woken(ctx, r, lr.ID, lr.State, true)
}

// attachFailed handles a wake's attach that failed. One lux refused as the
// Run is over (409 finished: it ended before its servers were on it) is
// not left for a later wake to misread: what lux reported of the Run is
// applied now (drain), counting a failed start; the wake is then either
// taken over by startFailed or tried again, deciding on the Run as it is.
func (p *Previews) attachFailed(ctx context.Context, r wakeRun, luxRunID string, err error) error {
	if le, ok := lux.AsError(err); !ok || le.Code != "finished" {
		_ = p.releaseWake(ctx, r, 5*time.Second)
		return err
	}
	r.LuxRunID = luxRunID
	if derr := p.drain(ctx, r); derr != nil {
		_ = p.releaseWake(ctx, r, 5*time.Second)
		return derr
	}
	return p.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE runs SET wake_claimed_at = NULL, next_attempt_at = GREATEST(next_attempt_at, now())
			WHERE id = $1 AND wake_wanted_at = $2`, r.ID, *r.WakeWanted)
		return err
	})
}

// attachAll attaches every server of the preview to its Run: one attached
// elsewhere (an old Run) is detached from there first; one lux no longer
// has is marked gone (the feed's server.deleted ends the preview).
func (p *Previews) attachAll(ctx context.Context, r wakeRun, runID string) error {
	list, err := p.previewServers(ctx, r.Org, r.ID)
	if err != nil {
		return err
	}
	for _, sv := range list {
		_, err := p.Lux.AttachServer(ctx, sv.LuxID, runID)
		if le, ok := lux.AsError(err); ok && le.Status == http.StatusConflict && le.Code == "attached" {
			if err = p.Lux.DetachServer(ctx, sv.LuxID); err == nil {
				_, err = p.Lux.AttachServer(ctx, sv.LuxID, runID)
			}
		}
		if lux.IsNotFound(err) {
			// The server or the Run: only the server's own 404 says which.
			if _, gerr := p.Lux.GetServer(ctx, sv.LuxID); lux.IsNotFound(gerr) {
				err = p.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
					_, err := tx.Exec(ctx, `UPDATE preview_servers SET deleted_at = COALESCE(deleted_at, now()) WHERE lux_server_id = $1`, sv.LuxID)
					return err
				})
			} else if gerr != nil {
				err = gerr
			}
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// woken records a wake done: lux has the Run coming up. Its idle marks are
// of the last period. Only for the wake this start was for: a failed start
// recorded meanwhile (startFailed) has moved the wake on, and wins.
//
// What lux answered may be older than what the feed applied meanwhile;
// then the applied lux_state stands and the status follows it (kept). For a
// resume or a submit (started) that is once an event of this start was
// applied (lux_start_event past StartBefore, read before the request): an
// end or running applied before that is of an earlier start. For an
// attach-only wake it is any event applied after the claim (lux_after_event
// past ObservedAfter), as lux was asked what the Run is after the claim; an
// end applied then is not acknowledged: the wake stays wanted, released.
func (p *Previews) woken(ctx context.Context, r wakeRun, luxRunID, state string, started bool) error {
	const kept = `(CASE WHEN $3 THEN lux_start_event > $5 ELSE lux_after_event > $7 END)`
	const keptEnd = `(NOT $3 AND lux_after_event > $7 AND lux_state IN ('stopped', 'failed', 'lost', 'succeeded', 'cancelled'))`
	return p.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE runs SET
			wake_wanted_at = CASE WHEN `+keptEnd+` THEN wake_wanted_at END,
			wake_claimed_at = NULL,
			next_attempt_at = CASE WHEN `+keptEnd+` THEN now() END,
			lux_state = CASE WHEN `+kept+` THEN lux_state WHEN $3 OR $2 = 'running' THEN $2 ELSE lux_state END,
			status = CASE WHEN (CASE WHEN `+kept+` THEN lux_state ELSE $2 END) = 'running' THEN 'running'::run_status
				WHEN `+kept+` AND lux_state IN ('stopped', 'failed', 'lost', 'succeeded', 'cancelled') THEN status
				WHEN status = 'paused' THEN 'scheduled'::run_status ELSE status END,
			active_since = CASE WHEN (CASE WHEN `+kept+` THEN lux_state ELSE $2 END) = 'running'
				THEN COALESCE(active_since, now()) ELSE active_since END,
			lux_stop_reason = CASE WHEN `+keptEnd+` THEN lux_stop_reason END,
			dude_pause = CASE WHEN `+keptEnd+` THEN dude_pause END,
			-- A start that failed stays said until one runs (luxEvent), as
			-- does an end kept.
			error = CASE WHEN start_failures > 0 OR `+keptEnd+` THEN error END
			WHERE id = $1 AND wake_wanted_at = $4 AND lux_run_id = $6`,
			r.ID, state, started, *r.WakeWanted, r.StartBefore, luxRunID, r.ObservedAfter)
		if err != nil {
			return err
		}
		// A wake from dude (Start) counts for the reaper as a URL's does.
		if _, err := tx.Exec(ctx, `UPDATE preview_servers SET idle_at = NULL, idle_last_request_at = NULL,
			last_woken_at = CASE WHEN $2 THEN now() ELSE last_woken_at END WHERE run_id = $1`, r.ID, started); err != nil {
			return err
		}
		if tag.RowsAffected() == 0 || !started {
			return nil
		}
		if _, err := ledger.Append(ctx, tx, ledger.Event{Type: "run.unparked", OrganizationID: r.Org, ProjectID: r.ProjectID,
			TaskID: r.TaskID, RunID: r.ID, ActorType: ledger.ActorSystem, ActorID: r.ID, Source: ledger.SourceRunner,
			CorrelationID: r.TaskID, Payload: map[string]any{"reason": "requested", "luxRunId": luxRunID}}); err != nil {
			return err
		}
		return phases.ServersChanged(ctx, tx, r.Org, r.ProjectID, r.TaskID, r.ID, map[string]any{"change": "waking"})
	})
}

// parkIfIdle stops a running preview once every one of its servers is
// idle: each has an unanswered server.idle and no request since (lux's
// lastRequestAt is the one the event carried), or no longer serves (exited,
// stopped), or never became ready and has had no request for its idleAfter
// (lux reports idle only for ready servers). A request to any one keeps the
// Run: its mark is dropped. One label list of lux's answers for them all.
func (p *Previews) parkIfIdle(ctx context.Context, r wakeRun) (bool, error) {
	list, err := p.previewServers(ctx, r.Org, r.ID)
	if err != nil {
		return false, err
	}
	inLux, err := p.Lux.ListServers(ctx, "", "dude.preview="+r.ID)
	if err != nil {
		return false, err
	}
	byID := map[string]lux.TenantServer{}
	for _, ts := range inLux {
		byID[ts.ID] = ts
	}
	now := time.Now()
	allIdle := len(list) > 0
	var used []string
	for _, sv := range list {
		got, ok := byID[sv.LuxID]
		switch {
		case !ok:
			// Gone from lux: its server.deleted ends the preview.
		case sv.IdleAt != nil:
			if requestedSince(got.LastRequestAt, sv.IdleLastRequestAt) {
				used = append(used, sv.LuxID)
				allIdle = false
			}
		case got.State == lux.SrvExited || got.State == lux.SrvStopped:
		case got.State != lux.SrvReady && unusedFor(got, now):
		default:
			allIdle = false
		}
	}
	if !allIdle {
		return false, p.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `UPDATE preview_servers SET idle_at = NULL, idle_last_request_at = NULL
				WHERE run_id = $1 AND lux_server_id = ANY($2)`, r.ID, db.NonNil(used)); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `UPDATE runs SET park_checked_at = now() WHERE id = $1`, r.ID)
			return err
		})
	}
	// Recorded before lux is asked, as every park is: lux's "stopped" is
	// then known to be dude's doing.
	parked := false
	if err := p.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE runs SET lux_stop_reason = 'pause', dude_pause = 'unused', status = 'paused', park_checked_at = NULL
			WHERE id = $1 AND status = 'running' AND wake_wanted_at IS NULL`, r.ID)
		if parked = err == nil && tag.RowsAffected() > 0; !parked {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE preview_servers SET idle_at = NULL, idle_last_request_at = NULL WHERE run_id = $1`, r.ID); err != nil {
			return err
		}
		if _, err := ledger.Append(ctx, tx, ledger.Event{Type: "run.parked", OrganizationID: r.Org, ProjectID: r.ProjectID,
			TaskID: r.TaskID, RunID: r.ID, ActorType: ledger.ActorSystem, ActorID: r.ID, Source: ledger.SourceRunner,
			CorrelationID: r.TaskID, Payload: map[string]any{"reason": "unused", "message": "parked: lux reported every server idle"}}); err != nil {
			return err
		}
		return phases.ServersChanged(ctx, tx, r.Org, r.ProjectID, r.TaskID, r.ID, map[string]any{"change": "parked"})
	}); err != nil || !parked {
		return false, err
	}
	return true, p.stop(ctx, r.previewRun)
}

// unusedFor: a server has had no request, and no change of state, for its
// idleAfter (0: never idle).
func unusedFor(s lux.TenantServer, now time.Time) bool {
	limit := s.IdleAfterDuration()
	if limit <= 0 {
		return false
	}
	last := s.Since
	if s.LastRequestAt != nil && s.LastRequestAt.After(last) {
		last = *s.LastRequestAt
	}
	return now.Sub(last) >= limit
}

// requestedSince: lux's lastRequestAt moved past the one its idle event
// carried. dude keeps that one at Postgres's microseconds, so a difference
// under a millisecond is the same request.
func requestedSince(now, atIdle *time.Time) bool {
	switch {
	case now == nil:
		return false
	case atIdle == nil:
		return true
	}
	return now.Sub(*atIdle) > time.Millisecond
}

// syncRunning moves a running preview's checkouts to the task's branch,
// which moved. An asleep one has nothing to do: its next wake syncs. One
// on its way up keeps the want until it runs: its resume's sync may have
// been of the older ref.
func (p *Previews) syncRunning(ctx context.Context, r wakeRun) (bool, error) {
	if r.Status != "paused" && (r.Status != "running" || r.LuxState != "running") {
		return false, nil
	}
	if r.Status == "running" && r.LuxRunID != "" {
		refs, err := p.syncRefs(ctx, r)
		if err != nil {
			return true, err
		}
		err = p.Lux.SyncRun(ctx, r.LuxRunID, fmt.Sprintf("sync-%s-%d", r.ID, r.SyncWanted.UnixMilli()), refs)
		if le, ok := lux.AsError(err); ok && (le.Code == "not_running" || !le.Retryable()) {
			p.Log.Info("a preview's sync was not taken; its next wake syncs", "run", r.ID, "error", le.Message)
			err = nil
		}
		if err != nil {
			return true, err
		}
	}
	return true, p.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE runs SET sync_wanted_at = NULL WHERE id = $1 AND sync_wanted_at = $2`, r.ID, *r.SyncWanted)
		return err
	})
}

// complete ends a live wakeable preview in dude; endInLux then deletes its
// servers and cancels its Run.
func (p *Previews) complete(ctx context.Context, r wakeRun, reason, actorType, actor string) error {
	ended := false
	if err := p.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE runs r SET status = 'completed', ended_at = now(), pending_starts = '{}',
			wake_wanted_at = NULL, wake_claimed_at = NULL WHERE r.id = $1 AND `+livePreview, r.ID)
		if ended = err == nil && tag.RowsAffected() > 0; !ended {
			return err
		}
		if _, err := ledger.Append(ctx, tx, ledger.Event{Type: "run.completed", OrganizationID: r.Org, ProjectID: r.ProjectID,
			TaskID: r.TaskID, RunID: r.ID, ActorType: actorType, ActorID: actor, Source: ledger.SourceOrchestrator,
			CorrelationID: r.TaskID, Payload: map[string]any{"status": "completed", "kind": KindPreview, "reason": reason}}); err != nil {
			return err
		}
		return phases.ServersChanged(ctx, tx, r.Org, r.ProjectID, r.TaskID, r.ID, map[string]any{"change": "stopped"})
	}); err != nil || !ended {
		return err
	}
	return p.endInLux(ctx, r)
}

// endInLux deletes an ended preview's lux servers — their hostnames then
// say "This preview is gone" — and then cancels its Run.
func (p *Previews) endInLux(ctx context.Context, r wakeRun) error {
	list, err := p.previewServers(ctx, r.Org, r.ID)
	if err != nil {
		return err
	}
	for _, sv := range list {
		if err := p.Lux.DeleteServer(ctx, sv.LuxID); err != nil && !lux.IsNotFound(err) {
			return err
		}
		if err := p.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE preview_servers SET deleted_at = COALESCE(deleted_at, now()) WHERE run_id = $1 AND lux_server_id = $2`,
				r.ID, sv.LuxID)
			return err
		}); err != nil {
			return err
		}
	}
	if r.LuxRunID == "" {
		p.unfollow(r.ID)
		// Nothing left in lux: 'cancel' takes the row out of
		// runs_wakeable_open_idx and the sweep.
		return p.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE runs SET lux_stop_reason = 'cancel' WHERE id = $1 AND lux_run_id IS NULL
				AND status IN ('completed', 'failed', 'aborted')`, r.ID)
			return err
		})
	}
	return p.cancel(ctx, r.previewRun)
}

// Wakeable says whether a new preview of the project takes the wakeable
// path: lux has a preview domain, and the project has a server to start in
// previews (a preview of none has nothing to wake by URL, and keeps the
// eager path).
func (s *Service) wakeable(ctx context.Context, tx pgx.Tx, projectID string) (bool, error) {
	if s.PreviewDomain == "" {
		return false, nil
	}
	var some bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM project_servers WHERE project_id = $1 AND autostart_in_previews)`,
		projectID).Scan(&some)
	return some, err
}

// wakeableView fills in a wakeable preview's servers from lux's own
// (/v1/servers by its dude.preview label): each as a Run's server, its
// state the process's (stopped, starting, ready, unreachable, exited) and
// lux's server state beside it as serverState; url the server's stable
// one. The stage, from lux's server states:
//
//	asleep, stopped, no answer, no wake due  → none (the preview is asleep)
//	waking or a wake due, Run not yet placed → scheduling
//	Run starting                             → cloning
//	Run running                              → setup / starting / ready, by process, as a Run-embedded preview's
func (s *Service) wakeableView(ctx context.Context, r *runRow, v *RunView, out *TaskServers) {
	list, err := s.Lux.ListServers(ctx, "", "dude.preview="+r.ID)
	if err != nil {
		s.Log.Debug("reading a preview's servers from lux", "run", r.ID, "error", err)
	}
	out.Servers = []lux.Server{}
	waking := r.WakeWanted
	for _, ts := range list {
		var m map[string]any
		if json.Unmarshal(ts.Raw, &m) != nil {
			continue
		}
		m["serverState"], m["state"], m["fromSpec"] = ts.State, ts.Process, true
		raw, _ := json.Marshal(m)
		var sv lux.Server
		if json.Unmarshal(raw, &sv) == nil {
			out.Servers = append(out.Servers, sv)
		}
		if ts.State == lux.SrvWaking {
			waking = true
		}
	}
	slices.SortFunc(out.Servers, func(a, b lux.Server) int { return strings.Compare(a.Name, b.Name) })
	if r.Error != "" {
		v.Error = &r.Error
	}
	if r.StartFailures >= previewStartAttempts && !r.WakeWanted {
		// dude stopped trying new Runs: lux may hold its wake open until
		// its wakeTimeout, but nothing is on its way. Its error says why.
		waking = false
	}
	status := r.Status
	if status == "paused" && waking {
		status = "scheduled"
	}
	v.Asleep = r.Status == "paused" && !waking
	v.PreviewStage = Stage(status, v.LuxState, out.Servers, func(name string) bool { return slices.Contains(r.WithSetup, name) })
}

// wantWake asks for an asleep wakeable preview to be woken, as a request to
// its URL would: a person started a server on it in dude.
func (s *Service) wantWake(ctx context.Context, org string, r runRow) error {
	err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE runs SET wake_wanted_at = COALESCE(wake_wanted_at, now())
			WHERE id = $1 AND kind = 'preview' AND wakeable AND status = 'paused'`, r.ID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return refuse(http.StatusConflict, "conflict", "the preview is no longer asleep; try again")
		}
		return phases.ServersChanged(ctx, tx, org, r.ProjectID, r.TaskID, r.ID, map[string]any{"change": "waking"})
	})
	if err != nil {
		return err
	}
	s.kick()
	return nil
}
