## wake

The pull request is open at api at fedcba9, web at 0123456, with CI reported there: keep the checks you deferred to this later phase, then answer with decide_checks (runId run_impl, turn 42, ask checks:run_impl:42:d1:0a1b2c3d4e5f), one verdict per check in this order. Deferred is no verdict now: backed, with a reason citing the evidence at these heads (a dispatch id and its outcome, a verify result, or the pull request's CI at this head), or reject; missing is not asked again (give none). The ask names these heads: if they move, a new wake asks again, and an answer to this one is refused. The pull request is not ready until you answer, and unanswered by the deadline it goes to a person. "gh workflow run bench.yml" deferred (Criterion 4 needs the bench workflow run on GitHub on the PR branch; the conductor dispatches it (run_workflow) once the branch is published): kept (backed) or reject? "ci workflow on the pull request" deferred (the PR's CI runs it): kept (backed) or reject?

## item-conducted

4 deferred check(s) not kept: "gh workflow run bench.yml" (deferred: the conductor dispatches it (run_workflow) once the pull request exists) — the conductor is asked once CI has reported at the head; "ci workflow on the pull request" (deferred: the PR's CI runs it) — the conductor is asked to keep it at this head; "open the page in a browser" (deferred: the tester runs it) — the conductor rejected it (no tester ran): a person settles it; "make bench" (deferred: the conductor verifies it at the head) — the conductor did not answer by the ask's deadline: a person settles it

## item-no-conductor

1 deferred check(s) not kept: "gh workflow run bench.yml" (deferred: the conductor dispatches it (run_workflow) once the pull request exists) — no conductor decides this delivery: a person settles it

## escalation

Deferred checks not kept:
- open the page in a browser: deferred, then rejected by the conductor once the pull request was open: no tester ran
- make bench: deferred, then the conductor did not answer the ask to keep it by its deadline

## escalation-no-conductor

Deferred checks not kept:
- gh workflow run bench.yml: deferred (the conductor dispatches it (run_workflow) once the pull request exists), and no conductor decides this delivery to keep it

