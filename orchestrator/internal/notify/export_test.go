package notify

import "context"

// AdvanceSessions is advanceSessions, for a test to run it at a moment of
// its choosing: in a sweep it runs only once a read found nothing unsent.
func (n *Notifier) AdvanceSessions(ctx context.Context) error { return n.advanceSessions(ctx) }

// AdvanceOwners is advanceOwners, likewise.
func (n *Notifier) AdvanceOwners(ctx context.Context) error { return n.advanceOwners(ctx) }
