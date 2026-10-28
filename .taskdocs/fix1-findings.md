# Fix Round 1 Findings

Starting HEAD: `a879db3d9aa97f061f4ab3bab56e7a8211cccac6` (rescued, previously unvalidated).

## Status

- Q1: rescued shared BranchSHA path escaping verified by HTTP handler seeing exact decoded slash, hash, percent and percent-encoded-looking branch names. Raw identity preserved.
- Q2: rescued primary-only resolution verified before first wake and after sleeping. Definite absence ends the exact run via endPreviewTx. Sweep now treats that sentinel as cleanup progress instead of backing off an ended run. Forge 502 retains the preview, retries, and never submits default code; recovery successfully wakes.
- Q3: persisted webhook received_at now passes through dispatch to BranchEvent. Candidate creation cutoff plus current BranchGone verification prevent stale/duplicate deletion of recreated branches; uncertain visibility retries. Termination always targets the candidate run ID (daily check also already targets its original run ID). Duplicate push/delete, out-of-order event, delayed receipt and unavailable forge regressions pass.
- P1: rescued metadata-only credentials/setup paths under validation.
- P2: CheckBranches now returns processed scheduling progress, retaining a separate ended counter for cleanup kicks. Existing 50-row bound, hourly transient retry and daily steady-state remain. A 51-healthy-preview test drains 50, 1, then 0 without unrelated kicks.
- P3: BranchEvent first filters organization repositories with SlugFromURL, then fetches only affected project previews. Removed preview-by-repository fan-out and correlated primary lookup; no cache/table added.
- T1: added truly concurrent starts blocked together at forge lookup; one Created, one reused, same ID, exactly one run and run.created event. Existing sequential test preserved.
- T2: added independent ref/repository visibility table: ref404/repo200 gone, ref404/repo404 and repo502 error, ref502 error, existing branch retained. All five cases pass.
- T3: E2E now requires exact branch, pushed commit SHA and fast-forward status matching fake lux's actual event representation. Runtime E2E setup in progress.
- Integration: corrected ReapAfter to override both kinds unless BranchReapAfter explicitly overrides branch. Tests pass for production defaults (7d/30d), shared override, branch-only override and both overrides.

## Validation

Isolated tmpfs pgvector/Postgres 17 started with the dbtest source's test credentials on localhost:5433. Forge focused gate: 2 top-level PASS, 9 subtest PASS, 0 FAIL/SKIP. Reap gate: 1 top-level PASS, 4 subtest PASS, 0 FAIL/SKIP. Recovered servers package compiles. Focused root gate: 10 PASS, 0 FAIL/SKIP, zero no-database skips. `go vet` and `go build` for internal/forge and internal/servers pass. Full root suite will not be run here. Mutation results will be recorded separately; parent evidence is not treated as independent proof.

Second focused root regression gate: 8 top-level PASS plus 2 subtest PASS, 0 FAIL/SKIP. An initial run failed because new tests incorrectly waited for successful serving after expected deletion/unavailability; corrected only the new tests to request a wake without expecting it to serve. Production sentinel cleanup also corrected as described above.

Deliberate mutants on committed forge source: removing path escaping fails 3 exact-identity subtests; ignoring repository-read errors fails hidden and unavailable repository subtests. Source restored immediately; no mutant committed.

Existing test-name changes: none removed or renamed. Existing deletion tests now actually delete the fake forge branch before notifying; this strengthens the genuine-absence contract. Existing daily-check test now asserts processed=1 instead of ended=0 on uncertainty, retaining its independent alive/hourly-retry database assertion.

E2E attempts: plain python unavailable; python3 lacked boto3. Retried with the repository's uv project dependencies. Services currently fail initial control-plane health; investigating local logs. No E2E success claimed yet.
