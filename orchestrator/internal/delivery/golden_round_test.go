package delivery

import "testing"

// A conducted review round's wake (wakes/round) and the facts a pipeline
// stopped or parked at its fix says (wakes/facts/pipeline-*).
func TestGoldenRoundWakes(t *testing.T) {
	findings := []roundFinding{
		{ID: "fnd_1", Severity: "high", Title: "The retry loses the\nsecond write", Where: "api/store.go:42", Introduced: "yes"},
		{ID: "fnd_2", Severity: "medium", Title: "No test can fail for the empty case", Where: "api/store_test.go:9", Introduced: "no", Unverified: true},
		{ID: "fnd_3", Severity: "medium", Title: "Typo in the log line", Where: "api/log.go", Trivial: true},
	}
	fix := render(roundFixTmpl, struct {
		Phase, RunID, Outcome, Note string
		Commits                     []string
	}{"fix", "run_f", "", "", []string{"api@1111111"}})
	partial := render(roundFixTmpl, struct {
		Phase, RunID, Outcome, Note string
		Commits                     []string
	}{"fix", "run_f", OutcomePartial, "the second\ngreeting is left", []string{"api@1111111", "web@2222222"}})
	whole := render(roundRangeTmpl, struct {
		Of    string
		Repos []string
	}{"", nil})
	narrow := render(roundRangeTmpl, struct {
		Of    string
		Repos []string
	}{RangeFixRound, []string{"api@0000000..1111111"}})
	stopped := func(c string, out Outcome, queued bool) string {
		return pipelineStopped(&Pipeline{Review: []string{"tests", "quality"}}, c, out, queued)
	}
	goldenTexts(t, "wake-round", [][2]string{
		{"round-1-findings", roundWake{Round: 1, Open: 3, Below: 2, People: 1, Range: whole, Lenses: []string{"correctness", "tests"}, Shown: findings}.text()},
		{"round-2-clear", roundWake{Round: 2, Range: narrow, Lenses: []string{"tests"}, Fix: fix}.text()},
		{"round-3-more", roundWake{Round: 3, Open: 15, More: 12, Range: narrow, Lenses: []string{"tests"}, Shown: findings[:1], Fix: partial}.text()},
		{"round-2-touches", roundWake{Round: 2, Open: 2, Range: narrow, Lenses: []string{"tests"},
			Shown: []roundFinding{{ID: "fnd_4", Severity: "high", Title: "The retry loops", Where: "api/store.go:43", Introduced: "yes", Touches: true}, findings[2]}, Fix: fix}.text()},
		{"round-3-more-touches", roundWake{Round: 3, Open: 15, More: 14, Range: narrow, Lenses: []string{"tests"}, Shown: findings[:1], Fix: fix,
			all: append(findings[:1:1], roundFinding{Touches: true}, roundFinding{}, roundFinding{Touches: true})}.text()},
		{"round-8", roundWake{Round: 8, Open: 1, Range: narrow, Lenses: []string{"tests"}, Shown: findings[:1], Fix: fix}.text()},
		{"pipeline-nothing", stopped(pipelineNothing, Outcome{Succeeded: true}, true)},
		{"pipeline-nothing-pr", stopped(pipelineNothing, Outcome{Succeeded: true}, false)},
		{"pipeline-partial", stopped(pipelineOutcome, Outcome{Succeeded: true, WriterOutcome: OutcomePartial, WriterOutcomeNote: "the second\ngreeting is left"}, true)},
		{"pipeline-blocked", stopped(pipelineOutcome, Outcome{Succeeded: true, WriterOutcome: OutcomeBlocked}, true)},
		{"pipeline-failed", stopped(pipelineFailed, Outcome{Error: "exit 1"}, true)},
		{"pipeline-queued", render(pipelineQueuedTmpl, struct{ Review []string }{[]string{"tests"}})},
		{"pipeline-unreviewed-push", render(pipelineUnreviewedTmpl, nil)},
	})
}
