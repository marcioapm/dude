package phases

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
)

// due's reference: the selection as it stood before its plan settings
// changed (testdata/due-reference.sql), run on a custom plan with the
// server's JIT. dueIn must return exactly its Runs, in its order, over
// random states of Runs and of everything they read: questions,
// directives, repository requests, peer waits, machine moves, ledger
// events, sessions' repositories and the delivery policies. Every
// timestamp lands on, just before or just after the boundary it is compared
// with, in the transaction both queries run in, so now() is the same for
// both. A change to due's selection that is meant to change its result
// rewrites the reference: go test ./internal/phases -run TestDueMatches -update.
func TestDueMatchesItsReferenceOverRandomStates(t *testing.T) {
	ctx := context.Background()
	_, owner := dbtest.Open(t)
	path := filepath.Join("testdata", "due-reference.sql")
	if *update {
		if err := os.WriteFile(path, []byte(dueQuery), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ref, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	seed := uint64(time.Now().UnixNano())
	if s := os.Getenv("DUE_DIFF_SEED"); s != "" {
		if seed, err = strconv.ParseUint(s, 10, 64); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("DUE_DIFF_SEED=%d", seed)
	rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	g := newDueStates(t, ctx, owner)
	cov := dueCoverage{}
	for round := range 40 {
		s := &Syncer{
			ParkAfter:     pick(rng, 0, 90*time.Second),
			IdleAfter:     pick(rng, 0, 45*time.Second),
			ConductorWarm: pick(rng, 0, 30*time.Second),
			RetryAhead:    pick(rng, 0, time.Minute),
		}
		tx, err := owner.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		n := g.populate(tx, rng, s, round)
		limit := sweepBatch
		if rng.IntN(2) == 0 {
			limit = 1 + rng.IntN(n)
		}
		if _, err := tx.Exec(ctx, `SELECT set_config('role', 'dude_sweeper', true), set_config('plan_cache_mode', 'force_custom_plan', true)`); err != nil {
			t.Fatal(err)
		}
		rows, err := tx.Query(ctx, string(ref), append(s.limits(), s.RetryAhead.Seconds(), limit)...)
		if err != nil {
			t.Fatal(err)
		}
		want, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (phaseRun, error) { return scan(row) })
		if err != nil {
			t.Fatal(err)
		}
		got, err := s.dueIn(ctx, tx, limit)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("round %d (limit %d, %d Runs): due returned %d Runs, the reference %d; first difference: %s",
				round, limit, n, len(got), len(want), firstDifference(got, want))
		}
		cov.add(want, limit)
		_ = tx.Rollback(ctx)
	}
	cov.check(t)
}

func pick[T any](rng *rand.Rand, vs ...T) T { return vs[rng.IntN(len(vs))] }

func firstDifference(got, want []phaseRun) string {
	for i := range min(len(got), len(want)) {
		if !reflect.DeepEqual(got[i], want[i]) {
			return fmt.Sprintf("at %d\n got: %+v\nwant: %+v", i, got[i], want[i])
		}
	}
	return "one is a prefix of the other"
}

// dueCoverage keeps the generator honest: each flag due computes is seen
// both true and false, and each kind of Run due takes is seen at all.
type dueCoverage struct {
	flags     map[string][2]int
	statuses  map[string]int
	truncated int
	rows      int
}

func (c *dueCoverage) add(runs []phaseRun, limit int) {
	if c.flags == nil {
		c.flags, c.statuses = map[string][2]int{}, map[string]int{}
	}
	if len(runs) == limit {
		c.truncated++
	}
	c.rows += len(runs)
	for _, r := range runs {
		c.statuses[r.Status]++
		for name, v := range map[string]bool{"HasDirectives": r.HasDirectives, "Unread": r.Unread, "RepoApproved": r.RepoApproved,
			"Waiting": r.Waiting, "ParkNow": r.ParkNow, "Resumable": r.Resumable, "Quiet": r.Quiet, "Nudged": r.Nudged,
			"WarmOver": r.WarmOver, "KeepExpired": r.KeepExpired, "Moving": r.Moving, "Broken": r.Broken != nil} {
			f := c.flags[name]
			if v {
				f[1]++
			} else {
				f[0]++
			}
			c.flags[name] = f
		}
	}
}

func (c *dueCoverage) check(t *testing.T) {
	t.Helper()
	t.Logf("%d Runs compared, %d rounds cut by their limit; flags (false, true): %v; statuses: %v", c.rows, c.truncated, c.flags, c.statuses)
	for name, f := range c.flags {
		if f[0] == 0 || f[1] == 0 {
			t.Errorf("%s was never %v in what due returned: the states do not exercise it", name, f[1] == 0)
		}
	}
	for _, s := range []string{"pending", "scheduled", "starting", "running", "paused", "aborted", "failed"} {
		if c.statuses[s] == 0 {
			t.Errorf("no %s Run was returned", s)
		}
	}
	if c.truncated == 0 {
		t.Error("no round was cut by its limit: the order past it is not compared")
	}
}

// dueStates makes the organizations, projects and repositories a round's
// Runs belong to, committed once; each round's Runs are rolled back.
type dueStates struct {
	t     *testing.T
	ctx   context.Context
	orgs  []dueOrg
	other string
	// Names a lux Run's repositories may hold: each repository's, as a
	// task's Run and as a session's Run name it, and one no repository has.
	luxNames []string
}

type dueOrg struct {
	id string
	// Policy minutes, nil where it sets none: park, idle, warm.
	policy   [3]*float64
	projects []dueProject
}

type dueProject struct {
	id     string
	policy [3]*float64
	repos  []string
}

func newDueStates(t *testing.T, ctx context.Context, owner *pgx.Conn) *dueStates {
	g := &dueStates{t: t, ctx: ctx}
	f := func(v float64) *float64 { return &v }
	policies := [][3]*float64{{}, {f(30), f(10), f(5)}, {nil, f(0), nil}, {f(0.5), nil, f(2)}}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := owner.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	policyJSON := func(p [3]*float64) map[string]any {
		m := map[string]any{}
		for i, k := range []string{"parkAfterMinutes", "idleNudgeMinutes", "conductorWarmMinutes"} {
			if p[i] != nil {
				m[k] = *p[i]
			}
		}
		return m
	}
	for o := range 3 {
		org := dueOrg{id: fmt.Sprintf("org_due%d", o), policy: policies[o]}
		exec(`INSERT INTO organizations (id, name, slug, delivery_policy) VALUES ($1, $1, $1, $2)`, org.id, policyJSON(org.policy))
		for p := range 2 {
			prj := dueProject{id: fmt.Sprintf("prj_due%d_%d", o, p), policy: policies[(o+p+1)%len(policies)]}
			if p == 0 {
				prj.policy = [3]*float64{}
			}
			exec(`INSERT INTO projects (id, organization_id, name, slug, key_prefix, delivery_policy) VALUES ($1, $2, $1, $1, $3, $4)`,
				prj.id, org.id, fmt.Sprintf("K%d%d", o, p), policyJSON(prj.policy))
			for r, name := range []string{"api", "Web App"} {
				id := fmt.Sprintf("repo_due%d_%d_%d", o, p, r)
				exec(`INSERT INTO repositories (id, organization_id, project_id, name, url) VALUES ($1, $2, $3, $4, 'git://x/'||$1)`,
					id, org.id, prj.id, name)
				prj.repos = append(prj.repos, id)
			}
			org.projects = append(org.projects, prj)
		}
		g.orgs = append(g.orgs, org)
	}
	// The task peer messages go to.
	g.other = "wi_due_other"
	exec(`INSERT INTO tasks (id, organization_id, project_id, number, title, goal) VALUES ($1, 'org_due0', 'prj_due0_0', 1, 'T', 'G')`, g.other)
	rows, err := owner.Query(ctx, `SELECT lux_name(r.name) FROM repositories r WHERE r.id LIKE 'repo_due%'
		UNION SELECT lux_name(p.key_prefix || '-' || r.name) FROM repositories r JOIN projects p ON p.id = r.project_id WHERE r.id LIKE 'repo_due%'
		UNION SELECT 'nothing'`)
	if err != nil {
		t.Fatal(err)
	}
	if g.luxNames, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
		t.Fatal(err)
	}
	return g
}

// seconds is the limit a Run's project, its organization, or else the
// syncer gives it: limits' order, park, idle, warm.
func (g *dueStates) seconds(o dueOrg, p *dueProject, s *Syncer) [3]float64 {
	var out [3]float64
	def := s.limits()
	for i := range out {
		out[i] = def[i].(float64)
		if o.policy[i] != nil {
			out[i] = *o.policy[i] * 60
		}
		if p != nil && p.policy[i] != nil {
			out[i] = *p.policy[i] * 60
		}
	}
	return out
}

// populate inserts one round's Runs, each with a task or a session of its
// own (so no two are one task's live conductor), and returns how many.
func (g *dueStates) populate(tx pgx.Tx, rng *rand.Rand, s *Syncer, round int) int {
	t, ctx := g.t, g.ctx
	b := &pgx.Batch{}
	// at is a time near base seconds from now(): nil, on it, a microsecond
	// either side, or well clear of it.
	at := func(base float64) *float64 {
		if rng.IntN(5) == 0 {
			return nil
		}
		v := base + pick(rng, 0, 0, -1e-6, 1e-6, -3600, 3600, -1, 1)
		return &v
	}
	maybe := func(p float64, v string) *string {
		if rng.Float64() < p {
			return &v
		}
		return nil
	}
	n := 40 + rng.IntN(60)
	for i := range n {
		id := fmt.Sprintf("run_r%d_%d", round, i)
		o := pick(rng, g.orgs...)
		session := rng.IntN(4) == 0
		var prj *dueProject
		var task, sess *string
		if session {
			v := "ses_" + id
			sess = &v
			b.Queue(`INSERT INTO sessions (id, organization_id, titled_by) VALUES ($1, $2, NULL)`, v, o.id)
			for _, p := range o.projects {
				if rng.IntN(2) == 0 {
					b.Queue(`INSERT INTO session_projects (session_id, project_id, organization_id) VALUES ($1, $2, $3)`, v, p.id, o.id)
				}
				for _, repo := range p.repos {
					if rng.IntN(2) == 0 {
						b.Queue(`INSERT INTO session_repositories (session_id, repository_id, organization_id) VALUES ($1, $2, $3)`, v, repo, o.id)
					}
				}
			}
		} else {
			p := pick(rng, o.projects...)
			prj = &p
			v := "wi_" + id
			task = &v
			b.Queue(`INSERT INTO tasks (id, organization_id, project_id, number, title, goal) VALUES ($1, $2, $3, $4, 'T', 'G')`,
				v, o.id, p.id, 1000*(round+1)+i)
		}
		secs := g.seconds(o, prj, s)
		park, idle, warm := secs[0], secs[1], secs[2]

		status := pick(rng, "pending", "scheduled", "starting", "running", "paused", "completed", "failed", "aborted")
		luxState := maybe(0.85, pick(rng, "running", "stopped", "succeeded", "failed", "cancelled", "terminated", "lost", "starting"))
		turnDone := at(-warm)
		pushRequest := maybe(0.2, "push_"+id)
		dudePause := maybe(0.8, pick(rng, "repository", "person", "idle", "unused", "conductor", "session", "machine"))
		switch rng.IntN(5) {
		case 0: // A finished turn of a running Run, not pushed: Unread is read.
			status, luxState, pushRequest = "running", ptr("running"), nil
			v := -warm + pick(rng, 0.0, -1e-6, 1e-6, 10)
			turnDone = &v
		case 1: // Paused for one of dude's reasons.
			status = "paused"
			dudePause = ptr(pick(rng, "person", "person", "conductor", "session", "machine", "repository", "idle"))
		case 2: // Mid-turn, waiting on nobody: Quiet turns on the idle limit.
			status, luxState, turnDone = "running", ptr("running"), nil
		}
		var waiting *float64
		if status != "running" || turnDone != nil || rng.IntN(3) == 0 {
			waiting = at(-park)
		}
		phase := maybe(0.6, pick(rng, "investigate", "implement", "review", "fix", "simplify", "test"))
		role := maybe(0.8, pick(rng, "conductor", "investigator", "implementer", "reviewer", "simplifier", "qa_browser", "brainstorm"))
		if session && rng.IntN(3) > 0 {
			phase, role = nil, ptr("brainstorm")
		}
		kind := "agent"
		if rng.IntN(15) == 0 {
			kind = "preview"
		}
		var luxRunID *string
		if rng.IntN(6) > 0 {
			luxRunID = ptr("lux_" + id)
		}
		luxRepos := []string{}
		for _, name := range g.luxNames {
			if rng.IntN(2) == 0 {
				luxRepos = append(luxRepos, name)
			}
		}
		openCalls := []string{}
		if rng.IntN(4) == 0 {
			openCalls = []string{"call_1"}
		}
		b.Queue(`INSERT INTO runs (id, organization_id, project_id, task_id, session_id, attempt, status, control, phase, role, kind,
			lux_run_id, lux_state, lux_stop_reason, keep,
			kept_until, next_attempt_at, turn_done_at, waiting_since, agent_busy_at, agent_active_at, idle_nudged_at,
			dude_pause, open_tool_calls, push_request_id, lux_repositories, harness_state, created_at)
			VALUES ($1, $2, $3, $4, $5, 1, $6::run_status, $7::run_control, $8::run_phase, $9::agent_role, $10,
			$11, $12, $13, $14,
			now() + make_interval(secs => $15::float8), now() + make_interval(secs => $16::float8),
			now() + make_interval(secs => $17::float8), now() + make_interval(secs => $18::float8),
			now() + make_interval(secs => $19::float8), now() + make_interval(secs => $20::float8),
			now() + make_interval(secs => $21::float8),
			$22, $23, $24, $25,
			CASE WHEN $26::float8 IS NULL THEN '{}'::jsonb ELSE jsonb_build_object('brokenTurn',
				jsonb_build_object('error', 'broke', 'epoch', 1, 'at', now() + make_interval(secs => $26::float8))) END,
			now() - make_interval(secs => $27::float8))`,
			id, o.id, projectID(prj), task, sess, status,
			pick(rng, "none", "none", "none", "pause_graceful", "pause_hard", "abort", "resume"), phase, role, kind,
			luxRunID, luxState, maybe(0.5, pick(rng, "cancel", "kept", "done")), rng.IntN(3) == 0,
			at(0), at(s.RetryAhead.Seconds()), turnDone, waiting, at(-idle), at(-idle), at(-idle),
			dudePause, openCalls, pushRequest, luxRepos, at(-30),
			float64(round*1000+i)+rng.Float64()/2)

		var questions []string
		for q := range rng.IntN(3) {
			qid := fmt.Sprintf("q_%s_%d", id, q)
			questions = append(questions, qid)
			b.Queue(`INSERT INTO questions (id, organization_id, task_id, run_id, prompt, items, status)
				VALUES ($1, $2, $3, $4, 'Q?', '[{"question":"Q?"}]', $5::question_status)`,
				qid, o.id, task, id, pick(rng, "open", "answered", "cancelled"))
		}
		var directives []string
		for d := range rng.IntN(4) {
			did := fmt.Sprintf("dir_%s_%d", id, d)
			directives = append(directives, did)
			var heldFor *string
			if len(questions) > 0 && rng.IntN(2) == 0 {
				heldFor = ptr(pick(rng, questions...))
			}
			b.Queue(`INSERT INTO directives (id, organization_id, task_id, run_id, text, sent_at, delivered_at, failed_at, held_for, interrupt_only)
				VALUES ($1, $2, $3, $4, 'do it', now() + make_interval(secs => $5::float8), now() + make_interval(secs => $6::float8),
				now() + make_interval(secs => $7::float8), $8, $9)`,
				did, o.id, task, id, at(-120), at(-60), sometimes(rng, 3, -5.0), heldFor, pick[*bool](rng, nil, ptr(true), ptr(false), ptr(false)))
		}
		events := rng.IntN(5)
		for e := range events {
			typ, payload := "agent.session.stopped", map[string]any{}
			if len(directives) > 0 && rng.IntN(2) == 0 {
				typ, payload = "run.directive.delivered", map[string]any{"directiveId": pick(rng, directives...)}
			}
			b.Queue(`INSERT INTO events (id, organization_id, run_id, event_type, actor_type, actor_id, source, payload)
				VALUES ($1, $2, $3, $4, 'system', 'dude', 'orchestrator', $5)`, fmt.Sprintf("ev_%s_%d", id, e), o.id, id, typ, payload)
		}
		if task != nil {
			for k, repo := range pickRepos(rng, o) {
				b.Queue(`INSERT INTO repository_requests (id, organization_id, task_id, run_id, repository_id, reason, status, blocking)
					VALUES ($1, $2, $3, $4, $5, 'needs it', $6::repository_request_status, $7)`,
					fmt.Sprintf("rr_%s_%d", id, k), o.id, task, id, repo,
					pick(rng, "pending", "approved", "denied", "cloned", "failed", "cancelled"), rng.IntN(2) == 0)
			}
			for w := range rng.IntN(3) {
				// At most one open: the last.
				mid := fmt.Sprintf("pm_%s_%d", id, w)
				b.Queue(`INSERT INTO peer_messages (id, organization_id, from_task_id, from_run_id, to_task_id, text)
					VALUES ($1, $2, $3, $4, $5, 'hi')`, mid, o.id, task, id, g.other)
				closed := w == 0 || rng.IntN(2) == 0
				b.Queue(`INSERT INTO peer_waits (id, organization_id, run_id, message_id, how, closed_at, closed_by)
					VALUES ($1, $2, $3, $4, 'wait', CASE WHEN $5 THEN now() END, CASE WHEN $5 THEN 'reply' END)`,
					"pw_"+mid, o.id, id, mid, closed)
			}
		}
		for m := range rng.IntN(3) {
			// At most one unsettled: the last.
			b.Queue(`INSERT INTO machine_moves (id, organization_id, task_id, session_id, run_id, role, kind, from_size, apply, settled_at)
				VALUES ($1, $2, $3, $4, $5, 'implementer', 'raise', '{}', 'none', CASE WHEN $6 THEN now() END)`,
				fmt.Sprintf("mm_%s_%d", id, m), o.id, task, sess, id, m == 0 || rng.IntN(2) == 0)
		}
	}
	if err := tx.SendBatch(ctx, b).Close(); err != nil {
		t.Fatal(err)
	}
	return n
}

func ptr[T any](v T) *T { return &v }

// sometimes is v one time in n, else nil.
func sometimes(rng *rand.Rand, n int, v float64) *float64 {
	if rng.IntN(n) == 0 {
		return &v
	}
	return nil
}

func pickRepos(rng *rand.Rand, o dueOrg) []string {
	var out []string
	for _, p := range o.projects {
		for _, r := range p.repos {
			if rng.IntN(6) == 0 {
				out = append(out, r)
			}
		}
	}
	return out
}

func projectID(p *dueProject) *string {
	if p == nil {
		return nil
	}
	return &p.id
}
