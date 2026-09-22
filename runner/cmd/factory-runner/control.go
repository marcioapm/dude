// Human intervention on a running Run — plan §24.
//
// The runner records what a human asked for and makes it real on the node.
// It never decides whether the request was allowed: that judgement already
// happened in the control plane.
package main

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/marciomartins/dude/runner/internal/client"
	"github.com/marciomartins/dude/runner/internal/protocol"
)

/*
leaseLoop renews the Run lease and carries the control channel.

Renewal runs at half the lease interval, which tolerates one lost renewal
without the control plane reclaiming the Run. The response brings back
anything a human has asked of this Run since the last tick — a pause, an
abort, or steering directives — so intervention needs no second poll.

`onControl` is called for a pause or abort; `onDirective` for steering. Both
run on this goroutine, so they must not block for long.
*/
func (d *daemon) leaseLoop(
	ctx context.Context,
	runID string,
	onControl func(control, reason string),
	onDirective func(client.Directive),
) {
	/*
	 * Poll far more often than the lease requires.
	 *
	 * Renewal alone could run at half the lease — 45s — but this is also the
	 * control channel, and a human who aborts a run should not watch it keep
	 * working for the better part of a minute. The renewal is cheap; the
	 * responsiveness is what matters.
	 */
	interval := controlPollInterval
	if lease := time.Duration(d.worker.LeaseSeconds) * time.Second / 2; lease > 0 && lease < interval {
		interval = lease
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			lease, err := d.api.RenewLease(ctx, runID)
			if err != nil {
				// The control plane no longer considers this Run ours —
				// aborted, or reclaimed after we went quiet. Either way the
				// work must stop now rather than run on unowned.
				if errors.Is(err, client.ErrRunNotLeasable) {
					d.logger.Info("run no longer leasable; stopping", "run", runID)
					if onControl != nil {
						onControl(protocol.ControlAbort, "run is no longer leasable")
					}
					return
				}
				if ctx.Err() == nil {
					d.logger.Warn("lease renewal failed", "run", runID, "error", err)
				}
				continue
			}

			for _, directive := range lease.Directives {
				d.logger.Info("directive received", "run", runID,
					"scope", directive.Scope, "text", directive.Text)
				if onDirective != nil {
					onDirective(directive)
				}
			}

			if lease.Control != "" && lease.Control != protocol.ControlNone && onControl != nil {
				d.logger.Info("control requested", "run", runID,
					"control", lease.Control, "reason", lease.ControlReason)
				onControl(lease.Control, lease.ControlReason)
			}
		}
	}
}

/*
controlPollInterval is how often the runner asks for pending interventions.

Tight because it bounds how long an aborted agent keeps working after a
human said stop. The request is a single indexed UPDATE, so the cost of
polling is small next to the cost of ignoring a person.
*/
const controlPollInterval = 3 * time.Second

/*
runControl is what a human has asked of an executing Run.

Written by the lease loop and read by the Run goroutine, so every field is
mutex-guarded. Kept deliberately small: the runner records the request and
acts on it, but never decides whether it was allowed — that judgement already
happened in the control plane.
*/
type runControl struct {
	mu         sync.Mutex
	kind       string
	reason     string
	directives []client.Directive
}

// request records a pause or abort. The first one wins: a graceful pause
// followed by an abort escalates, but an abort is never downgraded.
func (c *runControl) request(kind, reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.kind == protocol.ControlAbort {
		return
	}
	c.kind = kind
	c.reason = reason
}

func (c *runControl) addDirective(d client.Directive) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.directives = append(c.directives, d)
}

// pending reports what was asked, if anything.
func (c *runControl) pending() (kind, reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.kind, c.reason
}

// takeDirectives returns undelivered steering text and clears it, so the same
// instruction is not appended to two consecutive turns.
func (c *runControl) takeDirectives() []client.Directive {
	c.mu.Lock()
	defer c.mu.Unlock()
	taken := c.directives
	c.directives = nil
	return taken
}

// stopped reports whether the Run should stop before starting more work.
// Checked at phase boundaries, which is what makes a graceful pause graceful.
func (c *runControl) stopped() bool {
	kind, _ := c.pending()
	return kind != "" && kind != protocol.ControlNone
}

/*
applyControl reports the Run's terminal state after an intervention.

Pause and abort are different terminal shapes: an aborted Run is finished, a
paused one is waiting to be resumed and must not look like a failure. Returns
"" when nothing was asked.
*/
func (d *daemon) applyControl(ctx context.Context, r client.Run, c *runControl, log *slog.Logger) string {
	kind, reason := c.pending()
	switch kind {
	case protocol.ControlAbort:
		log.Info("run aborted by request", "reason", reason)
		// The control plane already marked it aborted; the runner's job was
		// to stop the work, which cancelling runCtx did.
		return protocol.RunAborted

	case protocol.ControlPauseGraceful, protocol.ControlPauseHard:
		log.Info("run paused by request", "mode", kind, "reason", reason)
		if err := d.api.UpdateRun(ctx, r.ID, protocol.RunPaused, reason, ""); err != nil {
			log.Warn("pause status update failed", "error", err)
		}
		return protocol.RunPaused

	default:
		return ""
	}
}
