package phases

import "github.com/marciomartins/dude/orchestrator/internal/delivery"

// benchSeen and benchClosest adapt the benchmarks to closestCalls: the
// calls as recordedCalls keeps them (read once), and one pass's search.
func benchSeen(calls []string) []readCall {
	seen := make([]readCall, 0, len(calls))
	for i, c := range calls {
		seen = append(seen, readCall{command: c, reading: "exit 0", at: i, words: callWords(shellOf(c, false))})
	}
	return seen
}

func benchClosest(reported string, seen []readCall) []string {
	return closestCalls(callWords(shellOf(reported, true)), seen)
}

// benchMatch is recordedCalls' row loop and hints over calls, each exited
// 0, without the database.
func benchMatch(checks []delivery.Check, calls []string, bases []taskBase) {
	cr := newCallReader(checks, bases)
	status, exit := "completed", 0.0
	for _, command := range calls {
		cr.read(command, &status, &exit, "out", "out")
	}
	cr.closest()
}
