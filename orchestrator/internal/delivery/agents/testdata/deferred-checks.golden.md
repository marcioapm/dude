## wake

The pull request is open at api at fedcba9, web at 0123456, with CI reported there: keep the checks you deferred to this later phase, then answer with decide_checks (runId run_impl, turn 42), one verdict per check in this order. Deferred is no verdict now: backed, with a reason citing the evidence (a dispatch id and its outcome, a verify result, or the pull request's CI at this head), or reject; missing is not asked again (give none). The pull request is not ready until you answer. "gh workflow run bench.yml" deferred (Criterion 4 needs the bench workflow run on GitHub on the PR branch; the conductor dispatches it (run_workflow) once the branch is published): kept (backed) or reject? "ci workflow on the pull request" deferred (the PR's CI runs it): kept (backed) or reject?

## item-conducted

3 deferred check(s) not kept: "gh workflow run bench.yml" (deferred: the conductor dispatches it (run_workflow) once the pull request exists) — the conductor is asked once CI has reported at the head; "ci workflow on the pull request" (deferred: the PR's CI runs it) — the conductor is asked to keep it at this head; "open the page in a browser" (deferred: the tester runs it) — the conductor rejected it (no tester ran): a person settles it

## item-no-conductor

1 deferred check(s) not kept: "gh workflow run bench.yml" (deferred: the conductor dispatches it (run_workflow) once the pull request exists) — no conductor decides this delivery: a person settles it

