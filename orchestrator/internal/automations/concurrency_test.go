package automations

import "testing"

func TestArrive(t *testing.T) {
	agent := func(concurrency, session string) Policy {
		return Policy{Kind: "agent", Concurrency: concurrency, SessionMode: session, ParallelMax: 2}
	}
	idle := Live{}
	running := Live{Running: []string{"arn_1"}}
	waiting := Live{Running: []string{"arn_1"}, Queued: "arn_2"}
	two := Live{Running: []string{"arn_1", "arn_2"}}
	for _, c := range []struct {
		name   string
		p      Policy
		source string
		live   Live
		want   Decision
	}{
		{"idle starts", agent("queue", "ephemeral"), SourceEvent, idle, Decision{Action: Start}},
		{"queue behind a live run", agent("queue", "ephemeral"), SourceEvent, running, Decision{Action: Queue}},
		{"merge into the waiting run", agent("merge", "ephemeral"), SourceEvent, waiting, Decision{Action: MergeInto, Into: "arn_2"}},
		{"merge with none waiting queues one", agent("merge", "ephemeral"), SourceEvent, running, Decision{Action: Queue}},
		{"skip, with a reason", agent("skip", "ephemeral"), SourceEvent, running, Decision{Action: Skip, Reason: "a run was still going (Concurrency: skip)"}},
		{"cancel and restart", agent("cancel", "ephemeral"), SourceEvent, running, Decision{Action: CancelAndStart}},
		{"send to the running agent", agent("send", "resumable"), SourceEvent, running, Decision{Action: SendInto, Into: "arn_1"}},
		{"parallel with room", agent("parallel", "ephemeral"), SourceEvent, running, Decision{Action: Start}},
		{"parallel at its max", agent("parallel", "ephemeral"), SourceEvent, two, Decision{Action: Queue}},
		{"a schedule's Run now queues behind a live run (Q7)", agent("skip", "ephemeral"), SourceRunNow, running, Decision{Action: Queue}},
		{"a schedule's Run now with nothing live starts", agent("skip", "ephemeral"), SourceRunNow, idle, Decision{Action: Start}},
		// Q21: a test run is always a run of its own.
		{"test vs queue", agent("queue", "ephemeral"), SourceTest, running, Decision{Action: Start}},
		{"test vs merge", agent("merge", "ephemeral"), SourceTest, waiting, Decision{Action: Start}},
		{"test vs skip", agent("skip", "ephemeral"), SourceTest, running, Decision{Action: Start}},
		{"test vs cancel", agent("cancel", "ephemeral"), SourceTest, running, Decision{Action: Start}},
		{"test vs send", agent("send", "resumable"), SourceTest, running, Decision{Action: Start}},
		{"test vs a full parallel", agent("parallel", "ephemeral"), SourceTest, two, Decision{Action: Start}},
		{"a webhook POSTs every firing (Q13)", Policy{Kind: "webhook", Concurrency: "skip"}, SourceEvent, running, Decision{Action: Start}},
		{"a server's live container takes the event", Policy{Kind: "server", Concurrency: "queue"}, SourceEvent, running, Decision{Action: SendInto, Into: "arn_1"}},
		{"a test on a server is its own container", Policy{Kind: "server", Concurrency: "queue"}, SourceTest, running, Decision{Action: Start}},
	} {
		if got := Arrive(c.p, c.source, c.live); got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestCanStartQueued(t *testing.T) {
	if CanStartQueued(Policy{Concurrency: "queue"}, 1) || !CanStartQueued(Policy{Concurrency: "queue"}, 0) {
		t.Error("queue: one at a time")
	}
	if !CanStartQueued(Policy{Concurrency: "parallel", ParallelMax: 3}, 2) || CanStartQueued(Policy{Concurrency: "parallel", ParallelMax: 3}, 3) {
		t.Error("parallel: up to its max")
	}
}
