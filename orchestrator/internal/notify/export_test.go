package notify

import "context"

// Advance is advance, for a test to interleave settling and delivery.
func (n *Notifier) Advance(ctx context.Context) error { return n.advance(ctx) }

// AdvanceSessions is advanceSessions, for a test to run it at a moment of
// its choosing: in a sweep it runs only once a read found nothing unsent.
func (n *Notifier) AdvanceSessions(ctx context.Context) error { return n.advanceSessions(ctx) }

// AdvanceOwners is advanceOwners, likewise.
func (n *Notifier) AdvanceOwners(ctx context.Context) error { return n.advanceOwners(ctx) }
