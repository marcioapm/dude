package phases

import "github.com/marciomartins/dude/orchestrator/internal/delivery"

// benchSeen and benchClosest adapt the benchmarks to closestCalls.
func benchSeen(calls []string) []readCall {
	seen := make([]readCall, 0, len(calls))
	for _, c := range calls {
		seen = append(seen, readCall{c, "exit 0"})
	}
	return seen
}

func benchClosest(reported string, seen []readCall) []string { return closestCalls(reported, seen) }

// benchMatch is recordedCalls' row loop and hints over calls, each exited
// 0, without the database.
func benchMatch(checks []delivery.Check, calls []string, bases []taskBase) {
	var seen []readCall
	for _, command := range calls {
		if sc := shellOf(command, false); sc.why == "" {
			seen = append(seen, readCall{command, "exit 0"})
		}
		for _, c := range checks {
			switch {
			case c.Result == delivery.CheckPass:
				sameCommand(c.Command, command)
			case c.Result == delivery.CheckFail && len(bases) > 0:
				atBase(command, c.Command, bases)
			}
		}
	}
	for _, c := range checks {
		if c.Result == delivery.CheckPass {
			closestCalls(c.Command, seen)
		}
	}
}
