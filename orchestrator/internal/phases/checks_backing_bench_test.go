package phases

import (
	"fmt"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// benchCalls are n recorded calls of the shapes agents run: pipefail, a
// cd, a pipe; none of them the reported command.
func benchCalls(n int) []string {
	calls := make([]string, n)
	for i := range calls {
		calls[i] = fmt.Sprintf("set -o pipefail && cd orchestrator && go test ./internal/pkg%d/... -run 'TestThing%d|TestOther' -count=1 2>&1 | tail -40", i%97, i)
	}
	return calls
}

// The nearest calls to an unmatched pass among 2000 recorded calls.
func BenchmarkClosestCalls2000(b *testing.B) {
	seen := benchSeen(benchCalls(2000))
	b.ReportAllocs()
	for b.Loop() {
		benchClosest("go test ./internal/pkg5/... -run TestThing", seen)
	}
}

// One long (4 KB) one-line command matched against itself.
func BenchmarkSameCommandLong(b *testing.B) {
	long := "go test ./internal/phases/ -run '" + strings.Repeat("TestSomethingRatherLong|", 4096/24) + "' -count=1"
	b.ReportAllocs()
	for b.Loop() {
		if _, ok := sameCommand(long+" 2>&1 | tail -40", "set -o pipefail && "+long+" 2>&1 | tail -40"); !ok {
			b.Fatal("not backed")
		}
	}
}

// The gate's matching of a 30-check report (15 unmatched passes, 15
// failures with a base) against 2000 recorded calls, as recordedCalls
// does it row by row inside its transaction, hints included.
func BenchmarkRecordedCallsMatching2000(b *testing.B) {
	calls := benchCalls(2000)
	var checks []delivery.Check
	for i := range 15 {
		checks = append(checks, delivery.Check{Command: fmt.Sprintf("go test ./internal/pkg%d/... -run TestThing", i), Result: delivery.CheckPass},
			delivery.Check{Command: fmt.Sprintf("make test%d", i), Result: delivery.CheckFail})
	}
	bases := []taskBase{{Repo: "target", Branch: "main", SHA: "0123456789abcdef0123456789abcdef01234567"}}
	b.ReportAllocs()
	for b.Loop() {
		benchMatch(checks, calls, bases)
	}
}
