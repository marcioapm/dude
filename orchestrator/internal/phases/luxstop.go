package phases

// lux stopping a phase Run under its agent: the disk limit, memory, its
// time limit, or a move to another host (a drain, a spot preemption, a
// migrate). The agent's harness dies with the container, so its turn in
// progress fails ("session/prompt: process exited (0)", lux's ACP
// adapter) — before or after lux's own events say why, in either order.
// A failure says lux's reason in plain words, the agent's error after it;
// a move fails nothing: the Run is kept, and told to carry on once lux has
// it running again (or fails, naming the move, past moveWait).

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// luxStop is why lux is stopping the Run's placement of Epoch: its
// stopping state's reason (lux's placement stop_reason: disk, timeout,
// stop, terminate, drain, preempt, migrate), or oom from the container's
// exit. A disk stop carries disk.exceeded's numbers, in bytes.
type luxStop struct {
	Reason     string `json:"reason"`
	Epoch      int    `json:"epoch"`
	UsedBytes  int64  `json:"usedBytes,omitempty"`
	LimitBytes int64  `json:"limitBytes,omitempty"`
}

// brokenTurn is a phase Run's turn that failed because its agent's
// process went away, held until lux says why (heldWait at most), or one
// cut off by a move (Moved, its reason), held until lux has the Run
// running in a later placement (moveWait at most).
type brokenTurn struct {
	// What the failure says without lux's reason, and the agent's error.
	Error      string    `json:"error"`
	AgentError string    `json:"agentError"`
	At         time.Time `json:"at"`
	Epoch      int       `json:"epoch"`
	Moved      string    `json:"moved,omitempty"`
}

// stopState is the part of harness_state about lux stopping the Run.
type stopState struct {
	LuxStop *luxStop    `json:"luxStop,omitempty"`
	Broken  *brokenTurn `json:"brokenTurn,omitempty"`
}

// How long a turn broken by its agent's process exiting waits for lux to
// say why, and a move for lux to have the Run running again.
const (
	heldWait = time.Minute
	moveWait = 20 * time.Minute
)

// evLuxDiskExceeded is lux's event for a placement over its disk limit
// (lux proto.EvDiskExceeded): {usedBytes, limitBytes, graceSeconds,
// ceiling}; luxd stops the placement with reason disk.
const evLuxDiskExceeded = "disk.exceeded"

// agentProcessExited: the agent's harness answered the turn with lux's
// adapter's error for a process that went away (lux internal/adapter/rpc.go).
func agentProcessExited(agentErr string) bool {
	return strings.Contains(agentErr, "process exited")
}

// movedNudge is what an agent lux moved mid-turn is told once it runs on
// its new host.
const movedNudge = "Your host was replaced: lux moved you to another one, and your turn was cut off. Your workspace " +
	"and conversation are as you left them; processes you had running are not. Carry on with your task where you left off."

// luxStopWords says why lux stopped the Run, for a person. timeout is the
// Run's time limit as configured.
func luxStopWords(stop luxStop, timeout string) string {
	switch stop.Reason {
	case "disk":
		if stop.UsedBytes > 0 && stop.LimitBytes > 0 {
			return fmt.Sprintf("lux stopped the Run: disk limit exceeded (%s of %s)", decimalBytes(stop.UsedBytes), binaryBytes(stop.LimitBytes))
		}
		return "lux stopped the Run: disk limit exceeded"
	case "oom":
		return "lux stopped the Run: out of memory (the container was killed)"
	case luxTimeout:
		return "lux stopped the Run at its time limit: " + timeout + " of running"
	case "preempt":
		return "lux stopped the Run to move it: its spot host was reclaimed"
	case "drain":
		return "lux stopped the Run to move it: its host was drained"
	case "migrate":
		return "lux stopped the Run to move it to another host"
	case "stop":
		return "lux stopped the Run: an operator stopped it"
	case "terminate", "terminated", "cancelled":
		return "lux stopped the Run: it was terminated"
	}
	return "lux stopped the Run: " + stop.Reason
}

// decimalBytes is n in GB, as lux's disk use is read (10.79 GB);
// binaryBytes in GiB, as a size's disk is set (10 GiB).
func decimalBytes(n int64) string {
	return strings.TrimSuffix(strings.TrimRight(fmt.Sprintf("%.1f", float64(n)/1e9), "0"), ".") + " GB"
}

func binaryBytes(n int64) string {
	return strings.TrimSuffix(strings.TrimRight(fmt.Sprintf("%.1f", float64(n)/(1<<30)), "0"), ".") + " GiB"
}

// stopOf is lux's reason for ending the Run in state with reason, as a
// luxStop: what the state event says, else what lux said when it began
// stopping this placement. nil when lux gave no reason a person needs.
func (t *translator) stopOf(state, reason string, epoch int) *luxStop {
	known := t.LuxStop
	if known != nil && known.Epoch != epoch {
		known = nil
	}
	var r string
	switch {
	case reason == "disk limit exceeded" || strings.HasPrefix(reason, "disk limit exceeded;"):
		r = "disk"
	case strings.Contains(reason, "out of memory"):
		r = "oom"
	case reason == luxTimeout:
		r = luxTimeout
	case lux.Terminated(state) || reason == "terminated":
		r = "terminate"
	case reason == "stop" || lux.Moved(reason):
		r = reason
	}
	switch {
	case r == "" && known == nil:
		return nil
	case r == "":
		return known
	case known != nil && known.Reason == r:
		return known
	}
	return &luxStop{Reason: r, Epoch: epoch}
}

// stopSeen records what lux says about stopping the Run's placement of
// epoch: a stopping state's reason, disk.exceeded's numbers, an exit's
// oom. A turn held for lux's reason is then decided.
func (t *translator) stopSeen(ctx context.Context, tx pgx.Tx, s *Syncer, stop luxStop) error {
	if cur := t.LuxStop; cur != nil && cur.Epoch == stop.Epoch && (cur.Reason == stop.Reason || stop.Reason == "") {
		if stop.UsedBytes > 0 {
			cur.UsedBytes, cur.LimitBytes = stop.UsedBytes, stop.LimitBytes
		}
	} else if stop.Reason != "" {
		t.LuxStop = &stop
	}
	if t.Broken == nil || t.Broken.Moved != "" || t.LuxStop == nil {
		return nil
	}
	return t.decideBroken(ctx, tx, s, *t.LuxStop)
}

// decideBroken settles a turn held for lux's reason: moved, or failed
// naming it. A disk stop waits for its numbers (disk.exceeded) or the
// Run's end.
func (t *translator) decideBroken(ctx context.Context, tx pgx.Tx, s *Syncer, stop luxStop) error {
	if lux.Moved(stop.Reason) {
		return t.moving(ctx, tx, s, stop.Reason)
	}
	if stop.Reason == "disk" && stop.UsedBytes == 0 {
		return nil
	}
	return t.failForLux(ctx, tx, s, stop, t.Broken.Error, t.Broken.AgentError)
}

// luxStoppedTurn handles a phase Run's failed turn that is lux's doing:
// held when its agent's process exited and lux has not said why yet,
// carried on when lux is moving the Run, failed naming lux's reason
// otherwise. False for a failure that is the agent's own.
func (t *translator) luxStoppedTurn(ctx context.Context, tx pgx.Tx, s *Syncer, agentErr, reason string) (bool, error) {
	stop := t.LuxStop
	if stop != nil && stop.Epoch != t.epoch {
		stop = nil
	}
	if stop == nil && !agentProcessExited(agentErr) {
		return false, nil
	}
	var at time.Time
	var dudeStopped bool
	if err := tx.QueryRow(ctx, `SELECT now(), lux_stop_reason IS NOT NULL OR status NOT IN ('scheduled', 'starting', 'running')
		FROM runs WHERE id = $1`, t.run.ID).Scan(&at, &dudeStopped); err != nil {
		return false, err
	}
	if dudeStopped {
		// dude stopped it (a pause, a finish) or it is over: as before.
		return false, nil
	}
	t.LastFailed = true
	if t.Broken == nil {
		t.Broken = &brokenTurn{Error: reason, AgentError: agentErr, At: at, Epoch: t.epoch}
	}
	if stop == nil {
		return true, nil
	}
	return true, t.decideBroken(ctx, tx, s, *stop)
}

// moving keeps a Run whose turn a move cut off: said once, as lux's
// warning, and told to carry on once lux runs it again (a directive,
// sent by deliverDirectives when the Run is running).
func (t *translator) moving(ctx context.Context, tx pgx.Tx, s *Syncer, reason string) error {
	if t.Broken.Moved != "" {
		return nil
	}
	t.Broken.Moved = reason
	if _, _, err := delivery.QueueDirective(ctx, tx, t.run.ref(), delivery.Directive{Text: movedNudge, Scope: "turn"}); err != nil {
		return err
	}
	words := luxStopWords(luxStop{Reason: reason}, "")
	return s.event(ctx, tx, t.run, evAgentWarning, ledger.ActorSystem,
		map[string]any{"message": words + "; its turn was cut off, and it is told to carry on once it runs again", "luxStop": reason})
}

// failForLux fails the Run for lux's stop, the agent's failure after it.
// Kept, as a failed turn is, unless lux stopped it at its time limit.
func (t *translator) failForLux(ctx context.Context, tx pgx.Tx, s *Syncer, stop luxStop, failure, agentErr string) error {
	why := luxStopWords(stop, s.timeout())
	if failure != "" {
		why += "; " + failure
	}
	t.Broken = nil
	tag, err := tx.Exec(ctx, `UPDATE runs SET status = 'failed', error = $2, ended_at = now(), turn_done_at = NULL, keep = $3
		WHERE id = $1 AND status IN ('scheduled', 'starting', 'running') AND lux_stop_reason IS NULL`, t.run.ID, why, stop.Reason != luxTimeout)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	return s.failedTxWith(ctx, tx, t.run, why, stopPayload(stop, agentErr))
}

func stopPayload(stop luxStop, agentErr string) map[string]any {
	p := map[string]any{"luxStop": stop.Reason}
	if stop.UsedBytes > 0 {
		p["usedBytes"], p["limitBytes"] = stop.UsedBytes, stop.LimitBytes
	}
	if agentErr != "" {
		p["agentError"] = agentErr
	}
	return p
}

// stateSeen follows lux's state for a broken turn: lux resuming the Run,
// or placing it again, after a turn broke is a move; running in a later
// placement ends the move — the carry-on directive goes then.
func (t *translator) stateSeen(ctx context.Context, tx pgx.Tx, s *Syncer, state, reason string, epoch int) error {
	b := t.Broken
	switch {
	case state == "stopping" && reason != "":
		return t.stopSeen(ctx, tx, s, luxStop{Reason: reason, Epoch: epoch})
	case state == "running":
		// Running in a later placement: what stopped the last is over.
		if t.LuxStop != nil && epoch > t.LuxStop.Epoch {
			t.LuxStop = nil
		}
		if b != nil && epoch > b.Epoch {
			t.Broken = nil
		}
	case b == nil:
	case b.Moved == "" && (state == "resuming" && epoch >= b.Epoch || (state == "scheduled" || state == "starting") && epoch > b.Epoch):
		moved := strings.TrimPrefix(reason, "auto-resume after ")
		if !lux.Moved(moved) {
			moved = "migrate"
		}
		return t.moving(ctx, tx, s, moved)
	}
	return nil
}

// timeout is a phase Run's time limit as configured.
func (s *Syncer) timeout() string {
	if s.Agent.Timeout != "" {
		return s.Agent.Timeout
	}
	return DefaultTimeout
}

// failBroken fails a Run whose broken turn waited past its bound: lux
// never said why its agent's process exited (heldWait), or never had it
// running again after a move (moveWait), which it names.
func (s *Syncer) failBroken(ctx context.Context, r phaseRun) error {
	b := r.Broken
	why, more := b.Error, map[string]any{"agentError": b.AgentError}
	if b.Moved != "" {
		why = fmt.Sprintf("%s, and it was not running again within %s; %s",
			luxStopWords(luxStop{Reason: b.Moved}, ""), cmp.Or(s.MoveWait, moveWait), b.Error)
		more["luxStop"] = b.Moved
	}
	s.unfollow(r.ID)
	return s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE runs SET status = 'failed', error = $2, ended_at = now(), turn_done_at = NULL, keep = true,
				harness_state = harness_state - 'brokenTurn'
			WHERE id = $1 AND status IN ('scheduled', 'starting', 'running') AND lux_stop_reason IS NULL
			  AND harness_state->'brokenTurn'->>'at' IS NOT NULL`, r.ID, why)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		return s.failedTxWith(ctx, tx, r, why, more)
	})
}

// brokenOverdue says whether r's broken turn has waited past its bound.
func (s *Syncer) brokenOverdue(r phaseRun) bool {
	if r.Broken == nil {
		return false
	}
	wait := cmp.Or(s.HeldWait, heldWait)
	if r.Broken.Moved != "" {
		wait = cmp.Or(s.MoveWait, moveWait)
	}
	return r.BrokenFor >= wait
}

// scanBroken reads runColumns' broken turn and how long it has waited.
func (r *phaseRun) scanBroken(raw []byte, secs float64) error {
	if len(raw) == 0 {
		return nil
	}
	r.Broken = &brokenTurn{}
	r.BrokenFor = time.Duration(secs * float64(time.Second))
	return json.Unmarshal(raw, r.Broken)
}

// takeUpLive answers a person's resume of a Run lux already runs, or is
// placing again (lux resumed it itself after a move): there is no stopped
// Run to resume, so it is taken back up as lux has it, its note (or the
// resume nudge) sent once lux reports it running. lux's events since dude
// last read them are skipped: they are of the placement that stopped, and
// a stop among them, read now, would end the Run taken up. False when
// lux's Run is not live: whilePaused waits for it to stop and resumes it.
func (s *Syncer) takeUpLive(ctx context.Context, r phaseRun) (bool, error) {
	if r.Control != "resume" || r.RepoApproved || r.talker() || r.LuxRunID == "" {
		return false, nil
	}
	cur, err := s.Lux.Get(ctx, r.LuxRunID)
	if err != nil {
		s.logger().Warn("reading a resumed Run from lux failed", "run", r.ID, "error", err)
		return false, nil
	}
	state := lux.Recorded(cur.State, cur.StateReason)
	if !liveInLux(state) {
		return false, nil
	}
	var after int64
	if err := s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT lux_after_event FROM runs WHERE id = $1`, r.ID).Scan(&after)
	}); err != nil {
		return false, err
	}
	for {
		page, err := s.Lux.Events(ctx, r.LuxRunID, after)
		if err != nil {
			return false, err
		}
		if len(page) == 0 {
			break
		}
		after = page[len(page)-1].EventID
	}
	s.unfollow(r.ID)
	taken := false
	err = s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE runs SET status = 'running', lux_state = $2, lux_stop_reason = NULL,
				lux_after_event = GREATEST(lux_after_event, $4),
				control = 'none', control_requested_at = NULL, control_reason = NULL, dude_pause = NULL,
				idle_nudged_at = NULL, agent_active_at = NULL, files_changed_at = now(),
				left_running_at = CASE WHEN $2 = 'running' THEN NULL ELSE now() END,
				waiting_since = CASE WHEN $3 THEN now() END, turn_done_at = NULL, agent_busy_at = NULL,
				harness_state = harness_state - 'brokenTurn'
			WHERE id = $1 AND status = 'paused' AND control = 'resume'`, r.ID, state, r.Waiting, after)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		taken = true
		if !r.HasDirectives && !r.Waiting {
			if _, _, err := delivery.QueueDirective(ctx, tx, r.ref(), delivery.Directive{Text: resumeNudge, Scope: "turn"}); err != nil {
				return err
			}
		}
		return delivery.EndConductorWait(ctx, tx, r.Org, r.ProjectID, r.TaskID)
	})
	return taken, err
}

// liveInLux: lux has the Run running, or is placing it.
func liveInLux(state string) bool {
	switch state {
	case "running", "scheduled", "starting", "resuming", "provisioning", "submitted":
		return true
	}
	return false
}
