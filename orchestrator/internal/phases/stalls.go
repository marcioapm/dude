package phases

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// Stalls: a phase Run that makes no progress (noProgress) is reported with
// the facts dude can read without a model — its open calls, the
// container's processes, lux's CPU and network counters, and for a Run that
// changes code its files, tools, plan and last message. A conducted task's
// conductor is told every 30 minutes at most per Run (60 when nothing
// changed); a plain delivery's owner once per Run, after the role's time
// limit (stallDue). Nothing is judged and nothing is restarted here.

// stallBatch bounds the Runs one sweep reports.
const stallBatch = 50

// psTimeout bounds the one exec a report makes.
const psTimeout = 10 * time.Second

// inputChars bounds an open call's input, as the agent sent it, in a report.
const inputChars = 300

// psCommand is what a report runs in the container.
var psCommand = []string{"ps", "-eo", "pid,ppid,etime,pcpu,args"}

// commandTools are tools whose call runs a command as a process of its own;
// any other (read, grep, edit, task, an MCP tool) runs inside the agent.
var commandTools = map[string]bool{"bash": true, "shell": true, "execute": true, "run_command": true, "command": true}

// stallRow is what a report reads of a Run due one.
type stallRow struct {
	ID, Org, ProjectID, TaskID, Phase, Role, Category, Tier, LuxRunID string
	RunningSecs, WindowSecs                                           int64
	OpenCalls                                                         map[string]time.Time
	FilesChangedAt, ReportedAt                                        *time.Time
	Fingerprint                                                       string
	Usage                                                             []byte
	Call, Files, Conducted                                            bool
	Iteration                                                         int
	Now                                                               time.Time
}

// reportStalls reports each Run due a report (noProgress, stallDue).
func (s *Syncer) reportStalls(ctx context.Context) error {
	var due []stallRow
	if err := s.DB.InSystem(ctx, "phase-sync", func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT r.id, r.organization_id, r.project_id, r.task_id, r.phase::text,
				COALESCE(r.role::text, ''), COALESCE(r.category, ''), COALESCE(r.model_tier, ''), COALESCE(r.lux_run_id, ''),
				extract(epoch FROM now() - COALESCE(r.started_at, r.created_at))::bigint, `+stallWindow+`::bigint,
				r.open_tool_calls_at, r.files_changed_at, r.stall_reported_at, `+stallFacts+`, r.stall_usage,
				`+stalledCall+`, `+stalledFiles+`, lc.conducted,
				COALESCE((SELECT (w.state->>'iteration')::int FROM workflow_runs w WHERE w.task_id = r.task_id
					ORDER BY w.created_at DESC LIMIT 1), 0),
				now()
			FROM `+stallFrom+` WHERE `+noProgress+` AND `+stallDue+`
			ORDER BY r.created_at LIMIT $3`, delivery.StallWindow.Seconds(), delivery.StallSameFacts.Seconds(), stallBatch)
		if err != nil {
			return err
		}
		due, err = pgx.CollectRows(rows, pgx.RowToStructByPos[stallRow])
		return err
	}); err != nil {
		return err
	}
	for _, r := range due {
		if err := s.reportStall(ctx, r); err != nil {
			s.Log.Warn("reporting a stalled run failed", "run", r.ID, "error", err)
		}
	}
	return nil
}

// reportStall gathers one Run's facts and records them (RecordStallTx).
func (s *Syncer) reportStall(ctx context.Context, r stallRow) error {
	window := time.Duration(r.WindowSecs) * time.Second
	stall := delivery.Stall{RunID: r.ID, Role: r.Role, Phase: r.Phase, Category: r.Category, Tier: r.Tier,
		RunningSecs: r.RunningSecs, WindowSecs: r.WindowSecs, Calls: []delivery.OpenCall{}, Processes: []delivery.Process{}}
	if r.Call {
		stall.Reasons = append(stall.Reasons, "call")
	}
	if r.Files {
		stall.Reasons = append(stall.Reasons, "files")
	}
	switch r.Phase {
	case delivery.PhaseReview:
		// A round's reviewers run before it is counted.
		stall.Round = r.Iteration + 1
	case delivery.PhaseFix:
		stall.Round = r.Iteration
	}
	changes := delivery.Publishes[r.Phase]
	if err := s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		if err := openCalls(ctx, tx, r, &stall); err != nil {
			return err
		}
		if changes {
			return codeFacts(ctx, tx, r, window, &stall)
		}
		return nil
	}); err != nil {
		return err
	}
	s.processes(ctx, r.LuxRunID, &stall)
	usage := s.usage(ctx, r, &stall)
	return s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		if ok, err := stillStalled(ctx, tx, r); err != nil || !ok {
			return err
		}
		ref := delivery.RunRef{Org: r.Org, ProjectID: r.ProjectID, TaskID: r.TaskID, RunID: r.ID}
		return delivery.RecordStallTx(ctx, tx, ref, stall, r.Conducted, r.Fingerprint, usage, r.ReportedAt)
	})
}

// stillStalled locks the Run and says whether it is still due the report
// its facts were gathered for: no progress and due (noProgress, stallDue,
// as the sweep read them), with the same facts. Gathering them takes up to
// psTimeout and more outside any transaction; in that time its call may
// have closed, its files changed, a person been asked, its turn ended, or
// a pause or abort been asked for.
func stillStalled(ctx context.Context, tx pgx.Tx, r stallRow) (bool, error) {
	if _, err := tx.Exec(ctx, `SELECT 1 FROM runs WHERE id = $1 FOR UPDATE`, r.ID); err != nil {
		return false, err
	}
	var ok bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM `+stallFrom+` WHERE r.id = $3 AND `+noProgress+` AND `+stallDue+`
		AND `+stallFacts+` = $4)`, delivery.StallWindow.Seconds(), delivery.StallSameFacts.Seconds(), r.ID, r.Fingerprint).Scan(&ok)
	return ok, err
}

// openCalls fills in each open call's tool and input, as the agent sent
// it, and how long it has been open.
func openCalls(ctx context.Context, tx pgx.Tx, r stallRow, s *delivery.Stall) error {
	for _, id := range slices.Sorted(maps.Keys(r.OpenCalls)) {
		var tool string
		var input json.RawMessage
		err := tx.QueryRow(ctx, `SELECT COALESCE(payload->>'tool', ''), COALESCE(payload->'input', 'null')
			FROM events WHERE run_id = $1 AND event_type = $2 AND payload->>'callId' = $3 ORDER BY cursor LIMIT 1`,
			r.ID, evToolCalled, id).Scan(&tool, &input)
		if err != nil && err != pgx.ErrNoRows {
			return err
		}
		s.Calls = append(s.Calls, delivery.OpenCall{Tool: cmp.Or(tool, "tool"), Input: callInput(input),
			OpenSecs: int64(r.Now.Sub(r.OpenCalls[id]).Seconds()), Command: commandTools[strings.ToLower(tool)]})
	}
	return nil
}

// callInput is a call's input as the agent sent it, on one line, cut: a
// command's or a task's own words where it has them, else its JSON.
func callInput(raw json.RawMessage) string {
	var m map[string]any
	if json.Unmarshal(raw, &m) == nil {
		for _, k := range []string{"command", "cmd", "description", "prompt", "filePath", "pattern"} {
			if v, ok := m[k].(string); ok && v != "" {
				return cut(oneLine(v), inputChars)
			}
		}
	}
	if string(raw) == "null" || len(raw) == 0 {
		return ""
	}
	return cut(oneLine(string(raw)), inputChars)
}

// codeFacts adds what a code-changing Run did in the window: when its files
// last changed, its tool calls by tool, its latest plan and last message.
func codeFacts(ctx context.Context, tx pgx.Tx, r stallRow, window time.Duration, s *delivery.Stall) error {
	s.FilesChangedAt = r.FilesChangedAt
	s.ToolsInWindow = map[string]int{}
	rows, err := tx.Query(ctx, `SELECT COALESCE(payload->>'tool', 'tool'), count(*)::int FROM events
		WHERE run_id = $1 AND event_type = $2 AND occurred_at >= now() - make_interval(secs => $3) GROUP BY 1`,
		r.ID, evToolCalled, window.Seconds())
	if err != nil {
		return err
	}
	counts, err := pgx.CollectRows(rows, pgx.RowToStructByPos[struct {
		Tool string
		N    int
	}])
	if err != nil {
		return err
	}
	for _, c := range counts {
		// Every tool that changes files counts as an edit.
		tool := c.Tool
		if isEdit(tool) {
			tool = "edit"
		}
		s.ToolsInWindow[tool] += c.N
	}
	var todos json.RawMessage
	if err := tx.QueryRow(ctx, `SELECT COALESCE((SELECT payload->'todos' FROM events WHERE run_id = $1 AND event_type = $2
			ORDER BY cursor DESC LIMIT 1), '[]'),
			COALESCE((SELECT payload->>'text' FROM events WHERE run_id = $1 AND event_type = $3 ORDER BY cursor DESC LIMIT 1), '')`,
		r.ID, evPlanUpdated, evAgentMessage).Scan(&todos, &s.LastMessage); err != nil {
		return err
	}
	s.LastMessage = cut(oneLine(s.LastMessage), inputChars)
	var plan []struct{ Content, Status string }
	_ = json.Unmarshal(todos, &plan)
	for _, p := range plan {
		s.Plan = append(s.Plan, fmt.Sprintf("%s [%s]", cut(oneLine(p.Content), 120), p.Status))
	}
	return nil
}

// processes lists the container's processes with one exec of ps, and says
// for each command call whether a process running it is alive. A failed
// exec is said, with its error; it never fails the report.
func (s *Syncer) processes(ctx context.Context, luxRunID string, st *delivery.Stall) {
	if luxRunID == "" {
		st.PSError = "the Run has no lux Run"
		return
	}
	ctx, cancel := context.WithTimeout(ctx, psTimeout)
	defer cancel()
	res, err := s.Lux.Exec(ctx, luxRunID, psCommand)
	switch {
	case err != nil:
		st.PSError = cut(oneLine(err.Error()), 200)
		return
	case res.ExitCode != 0:
		st.PSError = cut(oneLine(fmt.Sprintf("ps exited %d: %s", res.ExitCode, res.Stderr)), 200)
		return
	}
	all := parsePS(string(res.Stdout))
	for i, c := range st.Calls {
		if !c.Command {
			continue
		}
		cmd := strings.TrimSuffix(cut(strings.TrimSpace(c.Input), 80), "…")
		st.Calls[i].Alive = cmd != "" && slices.ContainsFunc(all, func(p delivery.Process) bool { return strings.Contains(p.Args, cmd) })
	}
	st.Processes = trimPS(all)
}

// parsePS reads `ps -eo pid,ppid,etime,pcpu,args`.
func parsePS(out string) []delivery.Process {
	var procs []delivery.Process
	for i, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if i == 0 || len(f) < 5 {
			continue
		}
		pid, err := strconv.Atoi(f[0])
		if err != nil {
			continue
		}
		cpu, _ := strconv.ParseFloat(f[3], 64)
		procs = append(procs, delivery.Process{PID: pid, Elapsed: f[2], CPU: cpu, Args: strings.Join(f[4:], " ")})
	}
	return procs
}

// trimPS drops lux's shim and the report's own ps, and shows each command
// line once, so an agent running as several processes is one line; at
// most 10.
func trimPS(all []delivery.Process) []delivery.Process {
	out := []delivery.Process{}
	seen := map[string]bool{}
	for _, p := range all {
		args := cut(p.Args, 160)
		if strings.Contains(p.Args, "lux-shim") || strings.HasPrefix(p.Args, "ps -eo") || seen[args] {
			continue
		}
		seen[args] = true
		p.Args = args
		out = append(out, p)
	}
	return out[:min(len(out), 10)]
}

// usage reads lux's CPU and network counters for the Run and puts on the
// report how much they moved since the last report (or the Run's start);
// returns the counters now, for the next. lux not saying is said.
func (s *Syncer) usage(ctx context.Context, r stallRow, st *delivery.Stall) *lux.Usage {
	var prev *lux.Usage
	if len(r.Usage) > 0 {
		_ = json.Unmarshal(r.Usage, &prev)
	}
	if r.LuxRunID == "" {
		return prev
	}
	got, err := s.Lux.Get(ctx, r.LuxRunID)
	if err != nil || got.Usage == nil {
		return prev
	}
	from := lux.Usage{}
	if prev != nil {
		from = *prev
	}
	cpu := max(got.Usage.CPUSeconds-from.CPUSeconds, 0)
	net := max(got.Usage.NetRxBytes+got.Usage.NetTxBytes-from.NetRxBytes-from.NetTxBytes, 0)
	st.CPUSecs, st.NetBytes, st.UsageSecs = &cpu, &net, r.RunningSecs
	if r.ReportedAt != nil {
		st.UsageSecs = int64(r.Now.Sub(*r.ReportedAt).Seconds())
	}
	return got.Usage
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// cut is s cut to n characters, saying so.
func cut(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}
