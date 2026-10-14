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

// machineParked (SQL, over runs r): a machine move of dude's is pending
// and the Run is being stopped (a control pending: the move's park, or a
// person's pause) or is paused; whichever resume follows carries the move
// and tells the agent where it runs. A running Run whose move is still
// unsettled has no such resume coming.
const machineParked = `(` + delivery.MoveUnsettled + ` AND (r.control <> 'none' OR r.status = 'paused'))`

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
	if stop == nil && !strings.Contains(agentErr, "process exited") {
		return false, nil
	}
	var at time.Time
	var dudeStopped, parked bool
	if err := tx.QueryRow(ctx, `SELECT now(), lux_stop_reason IS NOT NULL OR status NOT IN ('scheduled', 'starting', 'running'), `+machineParked+`
		FROM runs r WHERE r.id = $1`, t.run.ID).Scan(&at, &dudeStopped, &parked); err != nil {
		return false, err
	}
	if dudeStopped {
		// dude's pause or finish owns the stop; an ended Run stays ended.
		return false, nil
	}
	t.LastFailed = true
	if parked {
		// dude is stopping the Run for a machine move: the move's resume
		// continues the turn on the new machine, and tells the agent so.
		return true, nil
	}
	if t.Broken == nil {
		t.Broken = &brokenTurn{Error: reason, AgentError: agentErr, At: at, Epoch: t.epoch}
	}
	if stop == nil {
		return true, nil
	}
	return true, t.decideBroken(ctx, tx, s, *stop)
}

// moving warns once and queues a carry-on for when lux runs the Run again.
// A machineParked Run is told by its machine move's resume instead.
func (t *translator) moving(ctx context.Context, tx pgx.Tx, s *Syncer, reason string) error {
	if t.Broken.Moved != "" {
		return nil
	}
	t.Broken.Moved = reason
	var parked bool
	if err := tx.QueryRow(ctx, `SELECT `+machineParked+` FROM runs r WHERE r.id = $1`, t.run.ID).Scan(&parked); err != nil {
		return err
	}
	told := "it is told to carry on once it runs again"
	if parked {
		told = "it is told where it runs, and to carry on, when dude resumes it for its machine move"
	} else if _, _, err := delivery.QueueDirective(ctx, tx, t.run.ref(), delivery.Directive{Text: movedNudge, Scope: "turn"}); err != nil {
		return err
	}
	words := luxStopWords(luxStop{Reason: reason}, "")
	return s.event(ctx, tx, t.run, evAgentWarning, ledger.ActorSystem,
		map[string]any{"message": words + "; its turn was cut off, and " + told, "luxStop": reason})
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
		// Running in a later placement: what stopped the last is over. A
		// move is kept: it says lux itself placed this one (replacedByLux).
		if t.LuxStop != nil && epoch > t.LuxStop.Epoch && !lux.Moved(t.LuxStop.Reason) {
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

// overdueBroken decides a hold past its bound once dude has read all lux
// said: after a restart, the move's end (or lux's reason) can be waiting
// in lux's events, unread. With some unread the Run is followed, and the
// hold decided on a later sweep from what they say.
func (s *Syncer) overdueBroken(ctx context.Context, r phaseRun) (bool, error) {
	var after int64
	if err := s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT lux_after_event FROM runs WHERE id = $1`, r.ID).Scan(&after)
	}); err != nil {
		return true, err
	}
	page, err := s.Lux.Events(ctx, r.LuxRunID, after)
	if err != nil {
		return true, s.retryLater(ctx, r, err)
	}
	if len(page) > 0 {
		s.follow(r)
		return false, nil
	}
	return true, s.failBroken(ctx, r, after)
}

// failBroken fails a Run whose broken turn waited past its bound: lux
// never said why its agent's process exited (heldWait), or never had it
// running again after a move (moveWait), which it names.
//
// The sweep's snapshot only nominates the Run: under the Run's row lock
// (the one the follower's batches take) the hold, its classification, its
// bound and the Run's control are read again, and the Run fails only if
// the hold as it is now is overdue by its own bound, and only if no lux
// event has been read since overdueBroken found none unread (read is
// lux_after_event then).
func (s *Syncer) failBroken(ctx context.Context, r phaseRun, read int64) error {
	failed := false
	err := s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		var raw []byte
		var secs float64
		var live bool
		if err := tx.QueryRow(ctx, `SELECT harness_state->'brokenTurn',
				COALESCE(extract(epoch FROM now() - (harness_state->'brokenTurn'->>'at')::timestamptz)::float8, 0),
				status IN ('scheduled', 'starting', 'running') AND control = 'none' AND lux_stop_reason IS NULL
				  AND lux_after_event = $2
			FROM runs WHERE id = $1 FOR UPDATE`, r.ID, read).Scan(&raw, &secs, &live); err != nil {
			return err
		}
		cur := r
		cur.Broken = nil
		if err := cur.scanBroken(raw, secs); err != nil {
			return err
		}
		if !live || !s.brokenOverdue(cur) {
			return nil
		}
		b := cur.Broken
		why, more := b.Error, map[string]any{"agentError": b.AgentError}
		if b.Moved != "" {
			why = fmt.Sprintf("%s, and it was not running again within %s; %s",
				luxStopWords(luxStop{Reason: b.Moved}, ""), cmp.Or(s.MoveWait, moveWait), b.Error)
			more["luxStop"] = b.Moved
		}
		if _, err := tx.Exec(ctx, `UPDATE runs SET status = 'failed', error = $2, ended_at = now(), turn_done_at = NULL, keep = true,
				harness_state = harness_state - 'brokenTurn'
			WHERE id = $1`, r.ID, why); err != nil {
			return err
		}
		failed = true
		return s.failedTxWith(ctx, tx, r, why, more)
	})
	if err == nil && failed {
		s.unfollow(r.ID)
	}
	return err
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

// liveAfterMove handles a resumable paused Run that lux placed again itself
// after a move (replacedByLux): there is no stop of dude's to wait for.
// A person's resume that only needs the Run running is answered by
// takeUpLive. A resume that must change the Run (an unsettled machine
// move's size, an approved repository) cannot be applied to a live
// placement, so lux is asked to stop the Run again and the resume follows
// once it has stopped. False when it is not such a Run: whilePaused waits
// for it to stop and resumes it.
func (s *Syncer) liveAfterMove(ctx context.Context, r phaseRun) (bool, error) {
	if r.talker() || r.LuxRunID == "" {
		return false, nil
	}
	// Only a Run dude saw lux move is asked about: any other is stopping
	// at dude's request, and lux is not asked on every sweep while it does.
	var moved stopState
	var resumedTo int
	if err := s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		var raw []byte
		if err := tx.QueryRow(ctx, `SELECT harness_state, (SELECT COALESCE(max(epoch), 0) FROM run_resumes WHERE run_id = $1)
			FROM runs WHERE id = $1`, r.ID).Scan(&raw, &resumedTo); err != nil || raw == nil {
			return err
		}
		return json.Unmarshal(raw, &moved)
	}); err != nil {
		return false, err
	}
	if from := movedFrom(moved); from == 0 || resumedTo > from {
		// No move, or dude resumed the Run itself since (a machine move's
		// resume, a person's): the placement lux runs is not one lux
		// placed for the move.
		return false, nil
	}
	cur, err := s.Lux.Get(ctx, r.LuxRunID)
	if err != nil {
		// Asked again after the usual retry spacing, not on every sweep.
		if lux.IsNotFound(err) {
			return false, nil
		}
		s.follow(r)
		return true, s.retryLater(ctx, r, err)
	}
	state := lux.Recorded(cur.State, cur.StateReason)
	if !liveInLux(state) || !replacedByLux(cur, moved) {
		return false, nil
	}
	if r.Control == "resume" && !r.RepoApproved && !r.Moving {
		taken, changes, err := s.takeUpLive(ctx, r, state)
		if taken || err != nil || !changes {
			return taken, err
		}
		// A Reset or an approval came in since the sweep read the Run:
		// its resume must change the Run, and is had once lux stops it.
	}
	s.follow(r)
	if !assigned(cur) {
		// Waiting for a host: nothing of lux's to stop yet. Looked at
		// again after the retry spacing, not on every sweep.
		return false, s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE runs SET next_attempt_at = now() + make_interval(secs => $2) WHERE id = $1`,
				r.ID, placementPoll.Seconds())
			return err
		})
	}
	// Once lux has the stop, replacedByLux no longer holds for this
	// placement, so lux is asked once per placement.
	return false, s.ask(ctx, r, s.Lux.Stop)
}

// placementPoll is how soon a Run lux is placing after a move is looked at
// again while it waits for a host.
const placementPoll = 5 * time.Second

// assigned: lux has a placement of the Run's current epoch, one a stop
// reaches.
func assigned(cur lux.Run) bool {
	for _, p := range cur.Placements {
		if p.Epoch == cur.Epoch {
			return true
		}
	}
	return false
}

// takeUpLive answers a person's resume of a Run lux placed again itself,
// now in lux state state: it is taken back up as lux has it, its note (or
// the resume nudge) sent once lux reports it running.
//
// The sweep's snapshot only nominates the Run. Under its row lock — the one
// a paused Run's StartMove takes before recording a move — its status,
// control, unsettled move and approved repositories are read again. A Run
// whose resume now has to change it (changes) is left paused with its
// resume, for liveAfterMove to stop lux's placement and resume it after.
func (s *Syncer) takeUpLive(ctx context.Context, r phaseRun, state string) (taken, changes bool, err error) {
	// The follower is stopped first, and waited for: a batch of its
	// committing after this would write back the harness_state it loaded.
	if err := s.unfollowWait(ctx, r.ID); err != nil {
		return false, false, err
	}
	err = s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		// Locked first, read after: a statement's snapshot is taken before
		// it waits for the lock, so a move committed meanwhile is seen only
		// by the next statement.
		if _, err := tx.Exec(ctx, `SELECT 1 FROM runs WHERE id = $1 FOR UPDATE`, r.ID); err != nil {
			return err
		}
		var resumable bool
		if err := tx.QueryRow(ctx, `SELECT r.status = 'paused' AND r.control = 'resume', `+delivery.MoveUnsettled+` OR `+repoApproved+`
			FROM runs r WHERE r.id = $1`, r.ID).Scan(&resumable, &changes); err != nil {
			return err
		}
		if !resumable || changes {
			changes = resumable && changes
			return nil
		}
		// The cursor stays: what lux reported since dude last read is
		// followed as usual — the move's states, the new placement's
		// running or failure, its clones, pushes and files — and lux_state
		// ends where lux's events do. A move's earlier placement ends
		// stopped (resuming as recorded), never terminal: a move lux does
		// not resume is not one replacedByLux takes up.
		if _, err := tx.Exec(ctx, `UPDATE runs SET status = 'running', lux_state = $2, lux_stop_reason = NULL,
				control = 'none', control_requested_at = NULL, control_reason = NULL, dude_pause = NULL,
				idle_nudged_at = NULL, agent_active_at = NULL, files_changed_at = now(),
				left_running_at = CASE WHEN $2 = 'running' THEN NULL ELSE now() END,
				waiting_since = CASE WHEN $3 THEN now() END, turn_done_at = NULL, agent_busy_at = NULL,
				harness_state = harness_state - 'brokenTurn'
			WHERE id = $1`, r.ID, state, r.Waiting); err != nil {
			return err
		}
		taken = true
		if nudge := r.resumeInput(); nudge != "" {
			if _, _, err := delivery.QueueDirective(ctx, tx, r.ref(), delivery.Directive{Text: nudge, Scope: "turn"}); err != nil {
				return err
			}
		}
		return delivery.EndConductorWait(ctx, tx, r.Org, r.ProjectID, r.TaskID)
	})
	return taken, changes, err
}

// resumeInput leaves queued instructions and a person's answer as the input.
func (r phaseRun) resumeInput() string {
	if r.HasDirectives || r.Waiting {
		return ""
	}
	return resumeNudge
}

// dropMovedNudge withdraws lux's carry-on before the machine move's resume
// supplies it. Unclaimed inputs are deleted; unresolved claims are retired
// with their identity kept for late receipts. A lost answer cannot say
// whether lux took the input: retirement prevents a resend, not prior speech.
// Accepted inputs stay. No send is in flight: only running Runs send, and
// a sweep advances each Run once. Returns whether another input is queued.
func (s *Syncer) dropMovedNudge(ctx context.Context, r phaseRun) (more bool, err error) {
	err = s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM directives WHERE run_id = $1 AND text = $2
			AND sent_at IS NULL AND claimed_at IS NULL AND failed_at IS NULL`, r.ID, movedNudge); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE directives SET failed_at = now(), error = $3
			WHERE run_id = $1 AND text = $2 AND sent_at IS NULL AND claimed_at IS NOT NULL
			  AND accepted_at IS NULL AND delivered_at IS NULL AND failed_at IS NULL`, r.ID, movedNudge, movedNudgeSuperseded); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM directives d WHERE d.run_id = $1
			AND d.sent_at IS NULL AND d.failed_at IS NULL AND NOT `+delivery.HeldBack+`)`, r.ID).Scan(&more)
	})
	return more, err
}

// movedNudgeSuperseded is a retired carry-on's error.
const movedNudgeSuperseded = "superseded: the machine move's resume tells the agent where it runs"

// liveInLux: lux has the Run running, or is placing it.
func liveInLux(state string) bool {
	switch state {
	case "running", "scheduled", "starting", "resuming", "provisioning", "submitted":
		return true
	}
	return false
}

// replacedByLux: lux's live placement of cur is one lux itself placed
// after moving the Run — dude recorded the move (st) for an earlier
// placement — and nobody has asked it to stop. A Run merely still live
// after dude's own stop (a pause lux has not applied yet) is not: it
// still stops, and is resumed as any.
func replacedByLux(cur lux.Run, st stopState) bool {
	from := movedFrom(st)
	if from == 0 || cur.Epoch <= from {
		return false
	}
	for _, p := range cur.Placements {
		if p.Epoch == cur.Epoch && p.StopRequestedAt != nil {
			return false
		}
	}
	return true
}

// movedFrom is the latest placement dude saw lux move the Run from, 0
// for none.
func movedFrom(st stopState) int {
	from := 0
	if st.LuxStop != nil && lux.Moved(st.LuxStop.Reason) {
		from = st.LuxStop.Epoch
	}
	if st.Broken != nil && st.Broken.Moved != "" {
		from = max(from, st.Broken.Epoch)
	}
	return from
}
