## round-1-findings

Review round 1 done (the whole branch; correctness, tests): 3 open at the fix bar, 2 below it, 1 people's feedback items (feedback). fnd_1 [high, introduced] api/store.go:42: The retry loses the second write; fnd_2 [medium, pre-existing, unverified] api/store_test.go:9: No test can fail for the empty case; fnd_3 [medium, trivial] api/log.go: Typo in the log line.

## round-2-clear

Review round 2 done (fix_round api@0000000..1111111; tests): 0 open at the fix bar. The fix before it: fix run_f committed at api@1111111.

## round-3-more

Review round 3 done (fix_round api@0000000..1111111; tests): 15 open at the fix bar. fnd_1 [high, introduced] api/store.go:42: The retry loses the second write. And 12 more at the bar (findings). The fix before it: fix run_f committed at api@1111111 web@2222222, reports itself partial: the second greeting is left.

## round-2-touches

Review round 2 done (fix_round api@0000000..1111111; tests): 2 open at the fix bar. fnd_4 [high, introduced, on lines the fix changed] api/store.go:43: The retry loops; fnd_3 [medium, trivial] api/log.go: Typo in the log line. The fix before it: fix run_f committed at api@1111111.

## round-3-more-touches

Review round 3 done (fix_round api@0000000..1111111; tests): 15 open at the fix bar. fnd_1 [high, introduced] api/store.go:42: The retry loses the second write. And 14 more at the bar (findings), 2 of them on lines the fix changed. The fix before it: fix run_f committed at api@1111111.

## round-8

Review round 8 done (fix_round api@0000000..1111111; tests): 1 open at the fix bar. fnd_1 [high, introduced] api/store.go:42: The retry loses the second write. The fix before it: fix run_f committed at api@1111111. 8 rounds in this loop: consider asking the person.

## pipeline-nothing

It committed nothing; the review you queued after it (tests, quality) did not start. decide next runs it; start_phase review with other reviewers replaces it.

## pipeline-nothing-pr

It committed nothing; the review you queued after it (tests, quality) did not start.

## pipeline-partial

It reports itself partial: the second greeting is left; the review you queued after it (tests, quality) did not start. decide next runs it; start_phase review with other reviewers replaces it.

## pipeline-blocked

It reports itself blocked; the review you queued after it (tests, quality) did not start. decide next runs it; start_phase review with other reviewers replaces it.

## pipeline-failed

It failed (exit 1); the review you queued after it (tests, quality) did not start. decide next runs it; start_phase review with other reviewers replaces it.

## pipeline-queued

The review you queued after it (tests) waits for you: decide next runs it; start_phase review with other reviewers replaces it.

## pipeline-unreviewed-push

 The fix is on the pull request, unreviewed: nothing is published past it until a review has run.

