package automations

// What a firing does when it arrives, given what the automation already has
// running or waiting. Pure: the store carries the decision out.

// Firing sources (automation_runs.source).
const (
	SourceSchedule = "schedule"
	SourceEvent    = "event"
	// A person's Run now on a schedule: queues behind a live run (Q7).
	SourceRunNow = "run_now"
	// A person's Run now on an event automation, with a past delivery or a
	// written test event: always a run of its own (Q21).
	SourceTest = "test"
)

// Concurrency modes (automations.concurrency).
const (
	ConcurrencyQueue    = "queue"
	ConcurrencyMerge    = "merge"
	ConcurrencySkip     = "skip"
	ConcurrencyCancel   = "cancel"
	ConcurrencySend     = "send"
	ConcurrencyParallel = "parallel"
)

// Action is what to do with an arriving firing.
type Action int

const (
	// Start a run now.
	Start Action = iota
	// Queue a run behind the live one(s).
	Queue
	// Add the payload to the waiting run (Into).
	MergeInto
	// Record a skipped run with Reason.
	Skip
	// Cancel the live runs, then start.
	CancelAndStart
	// Send the payload into the live run (Into): the running agent hears
	// it, or the Server's container is POSTed it.
	SendInto
)

// Live is what an automation has under way when a firing arrives.
type Live struct {
	// Running runs, oldest first, by id.
	Running []string
	// The waiting run a merge adds to, "" when none waits.
	Queued string
}

// Policy is the part of an automation the decision reads.
type Policy struct {
	Kind, Concurrency, SessionMode string
	ParallelMax                    int
}

// Decision is Arrive's answer.
type Decision struct {
	Action Action
	// The run a merge or send goes into.
	Into   string
	Reason string
}

// Arrive decides what one firing does. A test run is always its own run,
// fresh and ephemeral, whatever is live (Q21). A webhook POSTs once per
// firing (Q13). A Server's live container is POSTed every event that
// arrives while it is up.
func Arrive(p Policy, source string, live Live) Decision {
	if source == SourceTest || p.Kind == "webhook" {
		return Decision{Action: Start}
	}
	busy := len(live.Running) > 0
	if p.Kind == "server" && busy {
		return Decision{Action: SendInto, Into: live.Running[len(live.Running)-1]}
	}
	if source == SourceRunNow {
		if busy || live.Queued != "" {
			return Decision{Action: Queue}
		}
		return Decision{Action: Start}
	}
	if !busy && live.Queued == "" {
		return Decision{Action: Start}
	}
	switch p.Concurrency {
	case ConcurrencyMerge:
		if live.Queued != "" {
			return Decision{Action: MergeInto, Into: live.Queued}
		}
		return Decision{Action: Queue}
	case ConcurrencySkip:
		return Decision{Action: Skip, Reason: "a run was still going (Concurrency: skip)"}
	case ConcurrencyCancel:
		return Decision{Action: CancelAndStart}
	case ConcurrencySend:
		if busy && p.Kind == "agent" && p.SessionMode == "resumable" {
			return Decision{Action: SendInto, Into: live.Running[len(live.Running)-1]}
		}
		return Decision{Action: Queue}
	case ConcurrencyParallel:
		if len(live.Running) < max(p.ParallelMax, 1) {
			return Decision{Action: Start}
		}
		return Decision{Action: Queue}
	default:
		return Decision{Action: Queue}
	}
}

// CanStartQueued says the oldest waiting run may start now: nothing of the
// automation is running, or it runs in parallel with room for one more.
func CanStartQueued(p Policy, running int) bool {
	if p.Concurrency == ConcurrencyParallel {
		return running < max(p.ParallelMax, 1)
	}
	return running == 0
}
