package servers

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/ids"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
	"github.com/marciomartins/dude/orchestrator/internal/phases"
	"github.com/marciomartins/dude/orchestrator/internal/registry"
)

// Previews runs branch previews on lux: a Run with no agent that checks
// out a task's branch and serves the project's servers marked to start in
// previews — started by lux on every start of the Run, so a preview that
// moves host serves again on its own.
//
//	pending ─submit─▶ scheduled ─lux running─▶ running ─unused─▶ paused (parked)
//	   paused ─a person starts a server─▶ running (resumed; that server started)
//	   any ─DELETE, or its task ends─▶ completed (lux Run cancelled)
//
// Like the phase syncer, every step is keyed on durable columns and is safe
// to repeat, so any orchestrator picks up where another left off. What
// lux reports on the Run's stream (its state, its servers') is followed by
// one goroutine per live preview, and becomes servers.changed for the
// browser.
type Previews struct {
	*Service
	Forges delivery.Forges
	// The image when neither the project's preview settings nor the
	// project name one (DUDE_AGENT_IMAGE).
	DefaultImage string
	// The login for the registry agent images come from (DUDE_REGISTRY_AUTH);
	// nil for none. A preview of the agent image pulls it the same way.
	Registry registry.Provider
	// The idle limit's unit, for tests; zero is a minute.
	Minute time.Duration
	// A wakeable preview nobody has woken for this long is ended
	// (previews.reap_after); zero is 7 days.
	ReapAfter time.Duration
	// How many wakeable previews one sweep takes at most; zero is 1000.
	SweepLimit int

	mu        sync.Mutex
	following map[string]context.CancelFunc
}

type previewRun struct {
	ID, Org, ProjectID, TaskID, Status string
	LuxRunID, LuxState                 string
	// Its task is done, failed or aborted: the preview ends with it.
	TaskEnded     bool
	PendingStarts []string
	// Its last sign of use, and the project's idle limit in minutes.
	ActiveSince *time.Time
	IdleMinutes float64
}

// Sweep takes one pass over every preview with something to do.
func (p *Previews) Sweep(ctx context.Context) (int, error) {
	var runs []previewRun
	if err := p.DB.InSystem(ctx, "previews", func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT r.id, r.organization_id, r.project_id, r.task_id, r.status::text,
				COALESCE(r.lux_run_id, ''), COALESCE(r.lux_state, ''),
				r.pending_starts, r.active_since,
				(preview_settings(pr)->>'idleTimeoutMinutes')::float8,
				t.status IN ('done', 'failed', 'aborted')
			FROM runs r JOIN projects pr ON pr.id = r.project_id JOIN tasks t ON t.id = r.task_id
			WHERE r.kind = 'preview' AND NOT r.wakeable
			  AND (r.status IN ('pending', 'scheduled', 'starting', 'running')
			       OR (r.status = 'paused' AND cardinality(r.pending_starts) > 0)
			       OR (r.status = 'paused' AND r.lux_state IS DISTINCT FROM 'stopped')
			       -- Parked, and its task is over: it ends with it.
			       OR (r.status = 'paused' AND t.status IN ('done', 'failed', 'aborted'))
			       -- Stopped in dude, not yet cancelled in lux (parked ones too).
			       OR (r.status IN ('completed', 'failed') AND r.lux_run_id IS NOT NULL
			           AND r.lux_stop_reason IS DISTINCT FROM 'cancel'))
			  AND (r.next_attempt_at IS NULL OR r.next_attempt_at <= now())
			-- Every live preview, those with something to do first.
			ORDER BY (r.status IN ('pending', 'completed', 'failed') OR cardinality(r.pending_starts) > 0
			          OR t.status IN ('done', 'failed', 'aborted')) DESC, r.created_at
			LIMIT 1000`)
		if err != nil {
			return err
		}
		runs, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (previewRun, error) {
			var r previewRun
			return r, row.Scan(&r.ID, &r.Org, &r.ProjectID, &r.TaskID, &r.Status, &r.LuxRunID, &r.LuxState,
				&r.PendingStarts, &r.ActiveSince, &r.IdleMinutes, &r.TaskEnded)
		})
		return err
	}); err != nil {
		return 0, err
	}
	// A few at a time, as the phase syncer does: one slow answer from lux
	// must not hold up every other preview.
	var acted atomic.Int64
	var wg sync.WaitGroup
	slots := make(chan struct{}, 8)
	for _, r := range runs {
		wg.Add(1)
		slots <- struct{}{}
		go func() {
			defer func() { <-slots; wg.Done() }()
			did, err := p.advance(ctx, r)
			if err != nil {
				p.Log.Warn("preview sync failed", "run", r.ID, "error", err)
				if rerr := p.retryLater(ctx, r); rerr != nil {
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
	n, err := p.sweepWakeable(ctx)
	return int(acted.Load()) + n, err
}

func (p *Previews) advance(ctx context.Context, r previewRun) (bool, error) {
	switch {
	case r.Status == "completed" || r.Status == "failed":
		return true, p.cancel(ctx, r)
	case r.TaskEnded:
		return true, p.endWithTask(ctx, r)
	case r.Status == "pending" && r.LuxRunID == "":
		return true, p.submit(ctx, r)
	case r.Status == "paused":
		if !lux.Terminal(r.LuxState) {
			// Still stopping: its stream says when it has.
			p.follow(r)
			if r.LuxState == "running" {
				// Parked, and lux has not said it is stopping: asked again
				// (idempotent), without keeping the loop from resting.
				return false, p.stop(ctx, r)
			}
			return false, nil
		}
		if len(r.PendingStarts) == 0 {
			return false, nil
		}
		return true, p.resume(ctx, r)
	}
	p.follow(r)
	if r.Status != "running" || r.LuxState != "running" {
		return false, nil
	}
	if len(r.PendingStarts) > 0 {
		return true, p.startPending(ctx, r)
	}
	return p.parkIfUnused(ctx, r)
}

// submit builds the preview's spec and hands it to lux.
func (p *Previews) submit(ctx context.Context, r previewRun) error {
	spec, branch, machine, err := p.spec(ctx, r)
	if err != nil {
		return err
	}
	// The dude Run id is the idempotency key: a retried submit gets the lux
	// Run the first one made.
	phases.NamePool(ctx, p.Lux, machine)
	lr, err := p.Lux.Submit(ctx, spec, r.ID)
	if reason := phases.PoolGone(err, machine); reason != "" {
		return p.fail(ctx, r, reason)
	}
	if le, ok := lux.AsError(err); ok && !le.Retryable() {
		return p.fail(ctx, r, "lux refused the preview: "+le.Message)
	}
	if err != nil {
		return err
	}
	var repos []string
	if spec.Git != nil {
		for _, repo := range spec.Git.Repositories {
			repos = append(repos, repo.Name)
		}
	}
	return p.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		// Recorded whatever the preview's status: one stopped while this
		// submit was in flight is then cancelled in lux by the next sweep.
		// machine is what it runs on, recorded once, with the lux Run.
		tag, err := tx.Exec(ctx, `UPDATE runs SET lux_run_id = $2, lux_state = $3, next_attempt_at = NULL,
			lux_repositories = $4, branch = NULLIF($5, ''), machine = $6::jsonb,
			status = CASE WHEN status = 'pending' THEN 'scheduled'::run_status ELSE status END
			WHERE id = $1 AND lux_run_id IS NULL`, r.ID, lr.ID, lr.State, db.NonNil(repos), branch, machine)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		return phases.ServersChanged(ctx, tx, r.Org, r.ProjectID, r.TaskID, r.ID, map[string]any{"change": "submitted"})
	})
}

// previewRef is one repository of a preview, at the ref it previews.
type previewRef struct{ Name, URL, Ref string }

// refs are a preview's repositories: the task's, else its project's, each
// at the task's branch where its work reached it, else its default branch.
// Read afresh each time: a branch published since is what a wake syncs to.
func (p *Previews) refs(ctx context.Context, r previewRun) ([]previewRef, error) {
	var repos []previewRef
	err := p.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) (err error) {
		repos, err = taskRefs(ctx, tx, r)
		return err
	})
	return repos, err
}

func taskRefs(ctx context.Context, tx pgx.Tx, r previewRun) ([]previewRef, error) {
	taskRepos, err := delivery.TaskRepositories(ctx, tx, r.TaskID)
	if err != nil {
		return nil, err
	}
	if len(taskRepos) == 0 {
		// A task naming none previews its project's code.
		rows, err := tx.Query(ctx, `SELECT id, name, url, default_branch, 'read' FROM repositories
			WHERE project_id = $1 ORDER BY name`, r.ProjectID)
		if err != nil {
			return nil, err
		}
		if taskRepos, err = pgx.CollectRows(rows, pgx.RowToStructByPos[delivery.Repository]); err != nil {
			return nil, err
		}
	}
	var repos []previewRef
	for _, tr := range taskRepos {
		// The task's branch in a repository its work reached; its
		// default branch where nothing was published yet.
		ref := tr.DefaultBranch
		var branch string
		err := tx.QueryRow(ctx, `SELECT branch FROM runs WHERE task_id = $1 AND branch IS NOT NULL AND heads ? $2
			ORDER BY created_at DESC LIMIT 1`, r.TaskID, tr.Name).Scan(&branch)
		switch {
		case err == nil:
			ref = branch
		case !db.IsNotFound(err):
			return nil, err
		}
		repos = append(repos, previewRef{tr.Name, tr.URL, ref})
	}
	return repos, nil
}

// spec is a preview's lux spec: the task's repositories, each at the
// task's branch where one was published (else its default branch), not
// pushed; a workload that only waits; the project's servers marked to start
// in previews; its preview settings' egress and image. Returns the branch
// the first repository — the one servers run in — is at.
func (p *Previews) spec(ctx context.Context, r previewRun) (lux.Spec, string, *delivery.Machine, error) {
	var repos []previewRef
	var recipes []Recipe
	var settings PreviewSettings
	var projectImage string
	var machine *delivery.Machine
	if err := p.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		var raw []byte
		if err := tx.QueryRow(ctx, `SELECT preview_settings(p), COALESCE(p.runtime_image, '') FROM projects p WHERE p.id = $1`,
			r.ProjectID).Scan(&raw, &projectImage); err != nil {
			return err
		}
		if err := json.Unmarshal(raw, &settings); err != nil {
			return err
		}
		sizes, err := delivery.LoadSizes(ctx, tx)
		if err != nil {
			return err
		}
		sizeID := ""
		if settings.MachineSize != nil {
			sizeID = *settings.MachineSize
		}
		if m, ok := sizes.ForPreview(sizeID); ok {
			machine = &m
		}
		if repos, err = taskRefs(ctx, tx, r); err != nil {
			return err
		}
		recipes, err = LoadRecipes(ctx, tx, r.ProjectID)
		return err
	}); err != nil {
		return lux.Spec{}, "", nil, err
	}

	image := projectImage
	if settings.Image != nil && *settings.Image != "" {
		image = *settings.Image
	}
	if image == "" {
		image = p.DefaultImage
	}
	spec := lux.Spec{
		Name:   "preview " + r.TaskID,
		Labels: map[string]string{"dude.org": r.Org, "dude.task": r.TaskID, "dude.run": r.ID, "dude.kind": KindPreview},
		Image:  lux.Image{Ref: image},
		Workload: lux.Workload{
			Adapter: "generic",
			Command: []string{"sleep", "infinity"},
			Workdir: "/workspace",
		},
		// The checkout, kept across a park: resuming does not clone again.
		Volumes: []lux.Volume{{Name: "workspace", Path: "/workspace", Kind: "state"}},
	}
	var refused []string
	spec.Network, refused = Egress(settings.Egress)
	if len(refused) > 0 {
		p.Log.Warn("preview egress entries lux would refuse were left out", "run", r.ID, "project", r.ProjectID, "entries", refused)
	}
	var primary, branch string
	if len(repos) > 0 {
		primary, branch = repos[0].Name, repos[0].Ref
		spec.Workload.Workdir = phases.RepoPath(primary)
		token, err := p.forgeToken(ctx, r.Org)
		if err != nil {
			return lux.Spec{}, "", nil, err
		}
		spec.Git = &lux.Git{}
		for _, rp := range repos {
			lr := lux.Repository{Name: rp.Name, URL: rp.URL, Ref: rp.Ref, Path: phases.RepoPath(rp.Name), Push: new(bool)}
			if token != "" {
				lr.Credential = "GIT_TOKEN"
			}
			spec.Git.Repositories = append(spec.Git.Repositories, lr)
		}
		if token != "" {
			spec.Secrets = []lux.Secret{{Name: "GIT_TOKEN", Value: token}}
		}
	}
	for _, rc := range recipes {
		if !rc.Autostart {
			continue
		}
		// One lux would refuse would fail the whole preview: left out.
		in, err := rc.Input(primary)
		if err != nil {
			p.Log.Warn("a preview server lux would refuse was left out", "run", r.ID, "server", rc.Name, "error", err)
			continue
		}
		spec.Workload.Servers = append(spec.Workload.Servers, in)
	}
	// A login that cannot be had now is an error the sweep retries.
	login, err := phases.LoginFor(ctx, p.Registry, image, nil)
	if err != nil {
		return lux.Spec{}, "", nil, err
	}
	login.Apply(&spec)
	phases.MachineSpec(machine, &spec)
	return spec, branch, machine, nil
}

func (p *Previews) forgeToken(ctx context.Context, org string) (string, error) {
	if p.Forges == nil {
		return "", nil
	}
	gh, err := p.Forges.For(ctx, org)
	if err != nil || gh == nil {
		return "", err
	}
	return gh.Token()
}

// Egress is a preview's network: the hosts (or addresses, or ranges) its
// settings allow; "*" turns filtering off. None allows nothing — lux clones
// the repository itself, so a preview that fetches nothing needs none. An
// entry lux would refuse (a wildcard host, a range that does not parse) is
// left out and returned, so one bad entry does not fail every preview; the
// API refuses them, so only settings saved before it did have any.
func Egress(allow []string) (n *lux.Network, refused []string) {
	n = &lux.Network{}
	for _, a := range allow {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		if a == "*" {
			return &lux.Network{Unrestricted: true}, nil
		}
		rule, ok := EgressRule(a)
		if !ok {
			refused = append(refused, a)
			continue
		}
		n.Egress = append(n.Egress, rule)
	}
	return n, refused
}

// hostnamePattern is a concrete hostname: dot-separated labels of letters,
// digits, '-' and '_', none starting or ending with '-'. No wildcards: lux
// resolves each host it allows.
var hostnamePattern = regexp.MustCompile(`^(?i:[a-z0-9_]([a-z0-9_-]{0,61}[a-z0-9_])?)(\.(?i:[a-z0-9_]([a-z0-9_-]{0,61}[a-z0-9_])?))*\.?$`)

// EgressRule is one allowed entry other than "*" as lux takes it: an
// address (as a one-address range), a range, or a hostname.
func EgressRule(a string) (lux.EgressRule, bool) {
	if ip := net.ParseIP(a); ip != nil {
		bits := "/128"
		if ip.To4() != nil {
			bits = "/32"
		}
		return lux.EgressRule{CIDR: ip.String() + bits}, true
	}
	if strings.Contains(a, "/") {
		if _, _, err := net.ParseCIDR(a); err != nil {
			return lux.EgressRule{}, false
		}
		return lux.EgressRule{CIDR: a}, true
	}
	if len(a) > 253 || !hostnamePattern.MatchString(a) {
		return lux.EgressRule{}, false
	}
	return lux.EgressRule{Host: a}, true
}

// follow reads a live preview's lux stream, if nothing is reading it yet.
func (p *Previews) follow(r previewRun) {
	if r.LuxRunID == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.following == nil {
		p.following = map[string]context.CancelFunc{}
	}
	if _, ok := p.following[r.ID]; ok {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	p.following[r.ID] = cancel
	go func() {
		defer func() {
			p.mu.Lock()
			delete(p.following, r.ID)
			p.mu.Unlock()
			cancel()
		}()
		if err := p.followEvents(ctx, r); err != nil && ctx.Err() == nil {
			p.Log.Warn("following a preview's lux events stopped", "run", r.ID, "error", err)
		}
	}()
}

func (p *Previews) unfollow(runID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if cancel, ok := p.following[runID]; ok {
		cancel()
		delete(p.following, runID)
	}
}

// Stop ends every follower; used on shutdown.
func (p *Previews) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, cancel := range p.following {
		cancel()
	}
}

// followEvents applies lux's lifecycle events for a preview: its state,
// where its checkout started, and its servers'. A preview has no agent,
// and lux leaves its servers' output out of the stream, so its records are
// skipped: only the event position is kept. lux ends every stream it
// finishes with an end frame: one that stops without it was cut short
// (errNoEnd).
func (p *Previews) followEvents(ctx context.Context, r previewRun) error {
	var after int64
	if err := p.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT lux_after_event FROM runs WHERE id = $1`, r.ID).Scan(&after)
	}); err != nil {
		return err
	}
	ended := false
	err := p.Lux.Output(ctx, r.LuxRunID, "", after, func(f lux.Frame) error {
		if f.Kind == "end" {
			ended = true
		}
		if f.Kind != "lux" {
			return nil
		}
		return p.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
			// Skipped: an event of a Run the preview has let go of
			// (retireRun), whose stream is still draining; and one already
			// applied, by this orchestrator's drain or another's follower.
			var current bool
			var applied int64
			if err := tx.QueryRow(ctx, `SELECT lux_run_id IS NOT DISTINCT FROM $2, lux_after_event FROM runs WHERE id = $1 FOR UPDATE`,
				r.ID, r.LuxRunID).Scan(&current, &applied); err != nil || !current || f.EventID <= applied {
				return err
			}
			if err := p.luxEvent(ctx, tx, r, f); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `UPDATE runs SET lux_after_event = GREATEST(lux_after_event, $2) WHERE id = $1`, r.ID, f.EventID)
			return err
		})
	})
	if err == nil && !ended {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errNoEnd
	}
	return err
}

var errNoEnd = errors.New("lux's event stream stopped without its end")

func (p *Previews) luxEvent(ctx context.Context, tx pgx.Tx, r previewRun, f lux.Frame) error {
	var d map[string]any
	_ = json.Unmarshal(f.EventData, &d)
	str := func(k string) string { v, _ := d[k].(string); return v }
	switch {
	case f.EventType == "state":
		// A move is not an end: lux resumes it (lux.Recorded).
		state := lux.Recorded(str("state"), str("reason"))
		var wakeable, ran bool
		var failures int
		// Running is a start: what its idle time counts from. A preview dude
		// did not stop that ends has failed (a clone, its image); one it
		// stopped is parked or finished, as dude already recorded. A
		// wakeable one goes back to sleep instead, its error kept: lux can
		// resume a Run that crashed, and the next request wakes it. One
		// whose current start never ran (no running since it began: at a
		// resuming, or at dude's submit of a new Run) is a failed start
		// (startFailed), whatever dude's status says meanwhile. Only a
		// resuming begins a start here: an end or a running never does.
		if err := tx.QueryRow(ctx, `UPDATE runs SET lux_state = $2,
			started_at = CASE WHEN $2 = 'running' THEN COALESCE(started_at, now()) ELSE started_at END,
			active_since = CASE WHEN $2 = 'running' THEN now() ELSE active_since END,
			status = CASE WHEN $2 = 'running' AND status IN ('scheduled', 'starting') THEN 'running'::run_status
			              WHEN $3 AND lux_stop_reason IS NULL AND status IN ('scheduled', 'starting', 'running') AND wakeable THEN 'paused'::run_status
			              WHEN $3 AND lux_stop_reason IS NULL AND status IN ('scheduled', 'starting', 'running') THEN 'failed'::run_status
			              ELSE status END,
			dude_pause = CASE WHEN $3 AND lux_stop_reason IS NULL AND status IN ('scheduled', 'starting', 'running') AND wakeable
			                  THEN 'unused' ELSE dude_pause END,
			error = CASE WHEN $3 AND lux_stop_reason IS NULL AND status IN ('scheduled', 'starting', 'running')
			             THEN 'the preview stopped: ' || COALESCE(NULLIF($4, ''), $2)
			             WHEN $2 = 'running' AND start_failures > 0 THEN NULL ELSE error END,
			start_failures = CASE WHEN $2 = 'running' THEN 0 ELSE start_failures END,
			lux_start_event = CASE WHEN $2 = 'resuming' THEN GREATEST(lux_start_event, $5) ELSE lux_start_event END,
			lux_ran_event = CASE WHEN $2 = 'running' THEN GREATEST(lux_ran_event, $5) ELSE lux_ran_event END,
			ended_at = CASE WHEN $3 AND lux_stop_reason IS NULL AND status IN ('scheduled', 'starting', 'running') AND NOT wakeable
			                THEN now() ELSE ended_at END
			WHERE id = $1
			RETURNING wakeable, lux_ran_event >= lux_start_event, start_failures`, r.ID, state, lux.Terminal(state), str("reason"), f.EventID).
			Scan(&wakeable, &ran, &failures); err != nil {
			return err
		}
		change := map[string]any{"change": "state", "luxState": state}
		if wakeable && !ran && (state == "failed" || state == "lost") {
			why, err := p.startFailed(ctx, tx, r, failures+1, cmp.Or(str("reason"), state))
			if err != nil {
				return err
			}
			change["error"] = why
		}
		return phases.ServersChanged(ctx, tx, r.Org, r.ProjectID, r.TaskID, r.ID, change)
	case f.EventType == "git.checkout":
		_, err := tx.Exec(ctx, `UPDATE runs SET base_shas = jsonb_build_object($2::text, $3::text) || base_shas WHERE id = $1`,
			r.ID, str("repo"), str("base"))
		return err
	case strings.HasPrefix(f.EventType, "server."):
		return phases.ServerEvent(ctx, tx, r.Org, r.ProjectID, r.TaskID, r.ID, f.EventType, f.EventData)
	}
	return nil
}

// parkIfUnused stops a preview nobody has opened for the project's idle
// limit: its last sign of use is when it last started or the latest request
// lux saw to any of its servers. lux is asked only once the preview looks
// idle by what dude last recorded, so a preview in use costs nothing.
func (p *Previews) parkIfUnused(ctx context.Context, r previewRun) (bool, error) {
	limit := time.Duration(r.IdleMinutes * float64(p.minute()))
	if r.ActiveSince == nil || time.Since(*r.ActiveSince) < limit {
		return false, nil
	}
	list, err := p.Lux.Servers(ctx, r.LuxRunID)
	if err != nil {
		return false, err
	}
	latest := *r.ActiveSince
	for _, sv := range list {
		if sv.LastRequestAt != nil && sv.LastRequestAt.After(latest) {
			latest = *sv.LastRequestAt
		}
	}
	if time.Since(latest) < limit {
		// Used since: counted from then.
		return true, p.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE runs SET active_since = $2 WHERE id = $1 AND active_since < $2`, r.ID, latest)
			return err
		})
	}
	// Recorded before lux is asked, as a pause is: lux's "stopped" is then
	// known to be dude's doing.
	if err := p.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE runs SET lux_stop_reason = 'pause', dude_pause = 'unused', status = 'paused'
			WHERE id = $1 AND status = 'running'`, r.ID)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		if _, err := ledger.Append(ctx, tx, ledger.Event{Type: "run.parked", OrganizationID: r.Org, ProjectID: r.ProjectID,
			TaskID: r.TaskID, RunID: r.ID, ActorType: ledger.ActorSystem, ActorID: r.ID, Source: ledger.SourceRunner,
			CorrelationID: r.TaskID, Payload: map[string]any{"reason": "unused",
				"message": fmt.Sprintf("parked: nobody opened the preview for %g minutes", r.IdleMinutes)}}); err != nil {
			return err
		}
		return phases.ServersChanged(ctx, tx, r.Org, r.ProjectID, r.TaskID, r.ID, map[string]any{"change": "parked"})
	}); err != nil {
		return true, err
	}
	return true, p.stop(ctx, r)
}

// stop asks lux to stop a parked preview. A stop that fails for now is
// asked again by the sweep, which sees the preview parked in dude and still
// running in lux.
func (p *Previews) stop(ctx context.Context, r previewRun) error {
	if err := p.Lux.Stop(ctx, r.LuxRunID); err != nil {
		if le, ok := lux.AsError(err); !ok || le.Retryable() {
			return err
		}
	}
	return nil
}

func (p *Previews) minute() time.Duration {
	if p.Minute > 0 {
		return p.Minute
	}
	return time.Minute
}

// resume takes a parked preview up again because a person started one of
// its servers: its checkout is as it was; lux starts its spec's servers,
// and the ones people asked for are started once it runs. lux keeps no
// secret, so every one comes again, the registry login freshly minted.
func (p *Previews) resume(ctx context.Context, r previewRun) error {
	// lux's stored spec says whether the submit logged in, and to where.
	lr, err := p.Lux.Get(ctx, r.LuxRunID)
	if le, ok := lux.AsError(err); ok && !le.Retryable() {
		return p.fail(ctx, r, "lux refused to resume the preview: "+le.Message)
	}
	if err != nil {
		return err
	}
	phases.RecordMemoryLimit(ctx, p.DB, p.Log, r.Org, r.ID, lr)
	login, err := phases.LoginFor(ctx, p.Registry, lr.Spec.Image.Ref, &lr.Spec)
	if phases.IsLoginUnavailable(err) {
		return p.waitForLogin(ctx, r, err)
	}
	if err != nil {
		return err
	}
	var spec lux.Spec
	token, err := p.forgeToken(ctx, r.Org)
	if err != nil {
		return err
	}
	if token != "" {
		spec.Secrets = []lux.Secret{{Name: "GIT_TOKEN", Value: token}}
	}
	login.Apply(&spec)
	// lux answers a Run already resuming as it did the first time; a
	// refusal (cancelled or finished meanwhile) is for good.
	lr, err = p.Lux.Resume(ctx, r.LuxRunID, lux.ResumeInput{Secrets: spec.Secrets, RequestID: "resume-" + r.ID})
	if le, ok := lux.AsError(err); ok && !le.Retryable() {
		return p.fail(ctx, r, "lux refused to resume the preview: "+le.Message)
	}
	if err != nil {
		return err
	}
	return p.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE runs SET status = 'running', lux_state = $2, lux_stop_reason = NULL, dude_pause = NULL,
			active_since = now() WHERE id = $1 AND status = 'paused'`, r.ID, lr.State)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		if _, err := ledger.Append(ctx, tx, ledger.Event{Type: "run.unparked", OrganizationID: r.Org, ProjectID: r.ProjectID,
			TaskID: r.TaskID, RunID: r.ID, ActorType: ledger.ActorSystem, ActorID: r.ID, Source: ledger.SourceRunner,
			CorrelationID: r.TaskID, Payload: map[string]any{"reason": "unused"}}); err != nil {
			return err
		}
		return phases.ServersChanged(ctx, tx, r.Org, r.ProjectID, r.TaskID, r.ID, map[string]any{"change": "resumed"})
	})
}

// startPending starts the servers a person asked for while the preview
// was parked, now that it runs.
func (p *Previews) startPending(ctx context.Context, r previewRun) error {
	for _, name := range r.PendingStarts {
		_, err := p.Lux.ServerAction(ctx, r.LuxRunID, name, "start")
		if le, ok := lux.AsError(err); ok && !le.Retryable() {
			// Removed meanwhile, or no command: nothing to start.
			p.Log.Info("a server asked for while the preview was parked did not start", "run", r.ID, "server", name, "error", err)
			err = nil
		}
		if err != nil {
			return err
		}
		if err := p.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE runs SET pending_starts = array_remove(pending_starts, $2) WHERE id = $1`, r.ID, name)
			return err
		}); err != nil {
			return err
		}
	}
	return nil
}

// cancel ends the lux Run of a preview a person stopped. Cancelled, not
// stopped: nothing about a finished preview is worth keeping. A lost one
// too: lux can resume a lost Run, so it holds a snapshot until cancelled.
func (p *Previews) cancel(ctx context.Context, r previewRun) error {
	p.unfollow(r.ID)
	if !lux.Terminal(r.LuxState) || r.LuxState == "stopped" || r.LuxState == "lost" {
		if err := p.Lux.Cancel(ctx, r.LuxRunID); err != nil {
			if le, ok := lux.AsError(err); !ok || le.Retryable() {
				return err
			}
		}
	}
	return p.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE runs SET lux_stop_reason = 'cancel', lux_state = 'cancelled' WHERE id = $1`, r.ID)
		return err
	})
}

// endWithTask ends a live or parked preview whose task is over, as a
// person's DELETE would, and cancels its lux Run. The task is read again:
// one retried since the sweep read it keeps its preview.
func (p *Previews) endWithTask(ctx context.Context, r previewRun) error {
	var ended bool
	if err := p.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE runs r SET status = 'completed', ended_at = now(), pending_starts = '{}'
			FROM tasks t WHERE r.id = $1 AND t.id = r.task_id AND t.status IN ('done', 'failed', 'aborted') AND `+livePreview, r.ID)
		if ended = err == nil && tag.RowsAffected() > 0; !ended {
			return err
		}
		if _, err := ledger.Append(ctx, tx, ledger.Event{Type: "run.completed", OrganizationID: r.Org, ProjectID: r.ProjectID,
			TaskID: r.TaskID, RunID: r.ID, ActorType: ledger.ActorSystem, ActorID: r.ID, Source: ledger.SourceOrchestrator,
			CorrelationID: r.TaskID, Payload: map[string]any{"status": "completed", "kind": KindPreview, "reason": "task ended"}}); err != nil {
			return err
		}
		return phases.ServersChanged(ctx, tx, r.Org, r.ProjectID, r.TaskID, r.ID, map[string]any{"change": "stopped"})
	}); err != nil || !ended {
		// Not ended here: stopped meanwhile (the next sweep cancels it),
		// or its task was taken up again.
		return err
	}
	if r.LuxRunID == "" {
		// Not submitted yet; one in flight is cancelled by a later sweep.
		p.unfollow(r.ID)
		return nil
	}
	return p.cancel(ctx, r)
}

func (p *Previews) fail(ctx context.Context, r previewRun, reason string) error {
	p.unfollow(r.ID)
	return p.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE runs SET status = 'failed', error = $2, ended_at = now(), pending_starts = '{}'
			WHERE id = $1 AND status NOT IN ('completed', 'failed', 'aborted')`, r.ID, reason)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		return phases.ServersChanged(ctx, tx, r.Org, r.ProjectID, r.TaskID, r.ID, map[string]any{"change": "failed", "error": reason})
	})
}

func (p *Previews) retryLater(ctx context.Context, r previewRun) error {
	return p.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE runs SET next_attempt_at = now() + interval '5 seconds' WHERE id = $1`, r.ID)
		return err
	})
}

// waitForLogin leaves a parked preview parked, its pending starts kept,
// until the registry login it was submitted with is configured again, and
// checks every phases.LoginRetry rather than every sweep.
func (p *Previews) waitForLogin(ctx context.Context, r previewRun, cause error) error {
	p.Log.Warn("parked preview not resumed: "+cause.Error(), "run", r.ID)
	return p.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE runs SET next_attempt_at = now() + make_interval(secs => $2) WHERE id = $1`,
			r.ID, phases.LoginRetry.Seconds())
		return err
	})
}

// StartPreview starts a branch preview of a task, on behalf of the key
// actor: 409 if the task has one live already, or is over.
func (s *Service) StartPreview(ctx context.Context, org, taskID, actor string, identity ...string) (TaskServers, error) {
	actorType, personID := ledger.ActorHuman, ""
	if len(identity) == 2 {
		actorType, personID = identity[0], identity[1]
	}
	err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		var projectID string
		var attempt int
		var over bool
		if err := tx.QueryRow(ctx, `SELECT t.project_id, COALESCE((SELECT max(attempt) FROM runs WHERE task_id = t.id), 1),
				t.status IN ('done', 'failed', 'aborted')
			FROM tasks t WHERE t.id = $1 FOR UPDATE`, taskID).Scan(&projectID, &attempt, &over); err != nil {
			if db.IsNotFound(err) {
				return refuse(http.StatusNotFound, "not_found", "task %s not found", taskID)
			}
			return err
		}
		if over {
			// Its preview would end with it at the next sweep.
			return refuse(http.StatusConflict, "task_finished", "task %s is over; a preview ends with its task", taskID)
		}
		var live bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM runs r WHERE r.task_id = $1 AND `+livePreview+`)`,
			taskID).Scan(&live); err != nil {
			return err
		}
		if live {
			return refuse(http.StatusConflict, "preview_running", "task %s already has a branch preview", taskID)
		}
		id := ids.New(ids.Run)
		wakeable, err := s.wakeable(ctx, tx, projectID)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, kind, started_by, wakeable)
			VALUES ($1, $2, $3, $4, $5, 'pending', 'preview', COALESCE(NULLIF($7, ''),
				(SELECT k.person_id FROM api_keys k JOIN people p ON p.id = k.person_id
				 WHERE k.id = $6 AND k.organization_id = $2 AND p.organization_id = $2 AND k.revoked_at IS NULL AND p.removed_at IS NULL)), $8)`,
			id, org, projectID, taskID, attempt, actor, personID, wakeable); err != nil {
			return err
		}
		_, err = ledger.Append(ctx, tx, ledger.Event{Type: "run.created", OrganizationID: org, ProjectID: projectID,
			TaskID: taskID, RunID: id, ActorType: actorType, ActorID: actor, Source: ledger.SourceOrchestrator,
			CorrelationID: taskID, Payload: map[string]any{"kind": KindPreview, "wakeable": wakeable}})
		if err != nil {
			return err
		}
		return phases.ServersChanged(ctx, tx, org, projectID, taskID, id, map[string]any{"change": "created"})
	})
	if err != nil {
		return TaskServers{}, err
	}
	s.kick()
	return s.ForTask(ctx, org, taskID)
}

// StopPreview stops a task's live preview for good.
func (s *Service) StopPreview(ctx context.Context, org, taskID, actor string, identity ...string) (TaskServers, error) {
	actorType := ledger.ActorHuman
	if len(identity) == 2 {
		actorType = identity[0]
	}
	err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		var id, projectID string
		// The sweep cancels its lux Run, if it has one (or when a submit in
		// flight records it).
		err := tx.QueryRow(ctx, `UPDATE runs r SET status = 'completed', ended_at = now(), pending_starts = '{}'
			WHERE r.task_id = $1 AND `+livePreview+` RETURNING r.id, r.project_id`, taskID).Scan(&id, &projectID)
		if db.IsNotFound(err) {
			return refuse(http.StatusNotFound, "not_found", "task %s has no branch preview", taskID)
		}
		if err != nil {
			return err
		}
		if _, err := ledger.Append(ctx, tx, ledger.Event{Type: "run.completed", OrganizationID: org, ProjectID: projectID,
			TaskID: taskID, RunID: id, ActorType: actorType, ActorID: actor, Source: ledger.SourceOrchestrator,
			CorrelationID: taskID, Payload: map[string]any{"status": "completed", "kind": KindPreview}}); err != nil {
			return err
		}
		return phases.ServersChanged(ctx, tx, org, projectID, taskID, id, map[string]any{"change": "stopped"})
	})
	if err != nil {
		return TaskServers{}, err
	}
	s.kick()
	return s.ForTask(ctx, org, taskID)
}

// wake asks for a parked preview to be taken up again, with servers to
// start once it runs.
func (s *Service) wake(ctx context.Context, org string, r runRow, names []string) error {
	err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE runs SET pending_starts = ARRAY(SELECT DISTINCT unnest(pending_starts || $2::text[]))
			WHERE id = $1 AND kind = 'preview' AND status = 'paused'`, r.ID, names)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return refuse(http.StatusConflict, "conflict", "the preview is no longer parked; try again")
		}
		return phases.ServersChanged(ctx, tx, org, r.ProjectID, r.TaskID, r.ID, map[string]any{"change": "waking"})
	})
	if err != nil {
		return err
	}
	s.kick()
	return nil
}
