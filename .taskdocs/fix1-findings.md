# Fix Round 1 Findings

## Outcome

All nine accepted findings and the reap override correction are addressed. Work started from rescued HEAD `a879db3d9aa97f061f4ab3bab56e7a8211cccac6`, not the implementation base. Rescued changes were inspected and tested rather than recreated. Work performed alone, with no subagents, pushes or PRs.

## Fixes

- Q1: retained the rescued shared BranchSHA path escaping. HTTP tests assert the exact decoded names `feature/login`, `feature/#123`, `feature/100%done` and `feature/%23`. Raw branch identity remains unchanged for database and git refs.
- Q2: verified rescued primary-required ref resolution. Missing primary refs are confirmed through BranchGone and end the exact run through endPreviewTx; uncertain forge/credentials results retry without default checkout. Corrected the wakeable sweep to treat the already-ended sentinel as cleanup progress rather than backing off an ended run. Tests cover deletion before first wake and while sleeping, plus forge unavailability and recovery. Secondary deletion explicitly syncs its default while retaining the primary branch. No forge HTTP calls added inside database transactions.
- Q3: carry persisted webhook received_at through dispatch to BranchEvent. Filter previews created after that cutoff, then confirm current primary absence outside transactions. Existing/recreated branches remain alive and sync; uncertain visibility retries. Receipt time is only a backlog fence, not proof of deletion time. Tests cover duplicate push/delete, out-of-order processing, delayed receipt, unavailable forge, a stored old receipt dispatched after a newer preview, and a daily absence read blocked across replacement. Ending always uses the originally selected run ID.
- P1: retained rescued metadata-only credential/setup and primary-name paths. Multi-repository running-sync regression asserts exactly one branch read per repository, not two.
- P2: CheckBranches returns processed scheduling progress, separately retaining ended for cleanup kicks. Fifty-row request bound, hourly transient retry and daily steady-state remain. A 51-healthy-preview test drains 50, 1, then 0 without unrelated kicks.
- P3: filter organization repository metadata with forge.SlugFromURL first, then select live previews only in matching projects. Removed preview-by-all-repositories fan-out and correlated primary lookup. No cache/table added. HTTPS, SCP and SSH URL variants match; unrelated projects remain unchanged.
- T1: two concurrent starts meet at a forge-lookup barrier, with no sleep synchronization. Assert one created, one reused, same ID, one run row and one run.created event. Sequential reuse coverage remains.
- T2: table-driven BranchGone HTTP test independently controls ref/repository responses: ref404/repo200 means gone; ref404/repo404 and repo502 mean error/not gone; ref502 means error; existing branch means not gone.
- T3: deployed-path E2E requires git.sync repo=greeter, ref=feature/login, status=fast-forward and to=the exact pushed commit SHA. Fixed Python fake GitHub to decode escaped branch names once; it previously interpreted feature%2Flogin literally.
- Integration: ReapAfter overrides both preview kinds unless BranchReapAfter explicitly overrides branch. Production unset defaults remain task=7d, branch=30d. Tests cover defaults, shared override, branch-only override and both overrides.

## Independent Validation

Started isolated pgvector/Postgres 17 with tmpfs storage and credentials read from internal/dbtest; zero no-test-database skips. Used the repo E2E runner, never bare pytest.

- gofmt and git diff --check: pass.
- go vet and go build: pass for internal/forge, internal/servers, internal/prs, internal/api, cmd/dude-cli and cmd/dude-orchestrator.
- All six affected Go package suites: 303 top-level PASS, 0 FAIL, 2 expected subprocess-helper SKIP (TestRunHelper, TestValidateHelper), zero database skips.
- First focused root gate: 10 PASS, 0 FAIL/SKIP. Intermediate branch-selected root gate: 50 PASS, 0 FAIL/SKIP. Final new-regression root gate: 8 top-level PASS plus 2 subtest PASS, 0 FAIL/SKIP. Two subsequently strengthened persisted-cutoff/secondary-fallback tests also pass.
- Forge contract gate: 2 top-level PASS plus 9 subtest PASS, 0 FAIL/SKIP. Reap contract gate: 1 top-level PASS plus 4 subtest PASS, 0 FAIL/SKIP. These are also included in the package-suite totals, not additional unique tests.
- Fake-Lux E2E branch plus preview secrets: 3 PASS, 0 FAIL/SKIP, 1 UI test deselected by --no-ui.
- Fake-Lux E2E existing servers plus branch preview: 6 PASS, 0 FAIL/SKIP, 1 real-Lux test deselected. Branch preview was exercised in both runs; counts are not unique-test totals.
- E2E setup initially lacked Python dependencies and Bun workspace links. Resolved using uv project dependencies and bun install --frozen-lockfile; no dependency source changes. A subsequent branch-start failure identified the fake GitHub decoding mismatch, fixed without weakening assertions.

## Negative Controls

- Removing shared BranchSHA escaping fails three exact-name subtests (hash, percent, encoded-looking percent).
- Ignoring BranchGone repository-read errors fails hidden and unavailable repository cases.
- Allowing missing-primary default fallback fails both first-wake and sleeping lifecycle tests: the mutant incorrectly leaves previews running.
- Replacing the E2E expected commit with an impossible SHA fails at the exact pushed-commit wait despite git.sync events occurring. Restored assertion rerun: 1 PASS, 0 FAIL/SKIP.
- Mutated only committed source and restored each change with apply_patch. No mutant committed. Initial new lifecycle test failures caused by using a success-waiting helper for expected failures were corrected only in those new tests.

## Test-Name Diff

Collected test declaration diff against 50bda080e: no removed or renamed tests. Added TestBranchSHAEncodesTheExactBranch, TestBranchGoneRequiresAVisibleRepository, TestPreviewReapDefaultsAndOverrides, TestConcurrentBranchPreviewStartsConverge, TestDeletedPrimaryBranchNeverWakesDefaultCode, TestUnavailablePrimaryBranchRetriesWithoutDefaultCode, TestDeletionReceiptsCannotEndRecreatedPreviews, TestDailyBranchChecksDrainHealthyPages, TestDailyBranchCheckCannotEndANewIncarnation and TestBranchEventsFilterProjectsAndPreserveCloneURLVariants.

Existing deletion fixtures now really delete the fake forge branch before notifying. Daily-check uncertainty now asserts processed=1 rather than ended=0 while preserving its alive/hourly-retry database assertions. Existing multi-repository and E2E assertions were strengthened, not removed.

## Commits And Limits

- `453a5ac7e`: first tested fix, forge contracts and shared reap overrides.
- `adcba98ff`: deletion fencing, healthy-page scheduling and lifecycle/concurrency/E2E contracts.
- Final verification/test commit: `Verify branch preview incarnation fences and exact checkout contracts` (this report included).

Full root suite deliberately not run. Control-plane implementation unchanged; public API exercised through deployed E2E. No real-Lux proof, full-root-green claim, production benchmark or exactly-once webhook claim. Parent validation is not counted as independent evidence. Logs and scratch remain local under .taskdocs and are not committed because runtime logs can contain generated test credentials.
