# Fix Round 1 Findings

Starting HEAD: `a879db3d9aa97f061f4ab3bab56e7a8211cccac6` (rescued, previously unvalidated).

## Status

- Q1: rescued shared BranchSHA path escaping verified by HTTP handler seeing exact decoded slash, hash, percent and percent-encoded-looking branch names. Raw identity preserved.
- Q2: rescued primary-only required resolution and run-ID-scoped ending under inspection; lifecycle regression tests pending.
- Q3: persisted receipt cutoff and current-absence verification pending.
- P1: rescued metadata-only credentials/setup paths under validation.
- P2: daily-check scheduling progress fix pending.
- P3: webhook repository-first filtering pending.
- T1: concurrent-start barrier regression pending; existing sequential test preserved.
- T2: added independent ref/repository visibility table: ref404/repo200 gone, ref404/repo404 and repo502 error, ref502 error, existing branch retained. All five cases pass.
- T3: exact successful E2E branch/SHA assertion pending.
- Integration: corrected ReapAfter to override both kinds unless BranchReapAfter explicitly overrides branch. Tests pass for production defaults (7d/30d), shared override, branch-only override and both overrides.

## Validation

Isolated tmpfs pgvector/Postgres 17 started with the dbtest source's test credentials on localhost:5433. Forge focused gate: 2 top-level PASS, 9 subtest PASS, 0 FAIL/SKIP. Reap gate: 1 top-level PASS, 4 subtest PASS, 0 FAIL/SKIP. Recovered servers package compiles. Focused root gate: 10 PASS, 0 FAIL/SKIP, zero no-database skips. `go vet` and `go build` for internal/forge and internal/servers pass. Full root suite will not be run here. Mutation results will be recorded separately; parent evidence is not treated as independent proof.
