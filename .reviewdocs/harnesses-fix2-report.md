# Harnesses fix round 2

Baseline: c843683e. Nothing pulled; no subagents, stash, PR, lux changes, or production containers.go edits.

## Commits
- M1: ce1fbeb5 — strict new project writes, tolerant stored domain decoding and API read normalization.
- M1 follow-up: 6faca3c8 — normalize only harness at API read boundary, preserving existing legacy model fields; real org/project settings read/correction regression.
- M1 assertion follow-up: d196b413 — echoing legacy aider remains a rejected new write with the entire database row unchanged.
- M2: 2d7433d6 — backfill original one-based IDs and infer next counter as max(stored counter, numeric loaded IDs).
- M3: 510edad2 — exact model_providers.dude replacement rejected through existing dropped/warn path; semantic Python tomllib coverage.
- Cheap fidelity Low: 27f57cf4 — script/recording end → lux.activity {activity:idle} → result, triple assertion; synthetic translator coverage retained.

## Runtime red-before evidence
- /var/tmp/hxf2-m1-red.log: 7 pass, 5 runtime assertion failures. Legacy GET retained aider; unknown string and numeric POST returned 201 rather than 400; both PATCH returned 200 rather than 400.
- /var/tmp/hxf2-m2-red.log: two runtime-failing tests. Legacy TaskUpdate2 returned empty plan; counters 0 and 9 with retained ID10 could not update newly created ID11. Initial invocation outside Go module was a setup error and is NOT counted as red.
- /var/tmp/hxf2-m3-red.log: parsed provider was string evil, and exact override absent from dropped list. One runtime-failing test, compiling normally.
- /var/tmp/hxf2-idle-red.log: triple assertion observed assistant/end/result, no idle. One runtime-failing test.
- Broader M1 gate caught a newly introduced regression in existing stored-model compatibility test (500 instead of 200), fixed by harness-only API normalization; /var/tmp/hxf2-cp.log records it.

## Read versus write decision
agentModelConfigSchema and agentModelsSchema are strict public inputs: unknown harness string and wrong type fail. Project POST/PATCH share this strict input; org/project settingsPatchSchema remains strict. No comparison against the stored row permits echoed invalid data.
storedAgentModelsSchema alone tolerates legacy harness values for projectSchema/organizationSchema domain readers. API project GET/list/PATCH response removes invalid harness fields from copies of stored role objects before editor roundtrip, without validating unrelated legacy fields or changing the row. Settings GET already uses tolerant resolveHarness for org/project layers. Go ResolveRole/HarnessName remains unchanged and its existing legacy unknown-harness executable regression passes alongside TS resolver coverage.

## Test-ID removal
Replaced only `a project holding a harness that is not one any more (aider) can still be saved, which drops it` in project-keys.test.ts. It incorrectly submitted a new invalid harness and required success. Replacement `legacy project GET normalizes harness before an editor roundtrip and accepts a valid correction` exercises actual normalized GET → edit → save and explicitly rejects an echoed raw legacy value. Existing domain unknown-stored-value assertion retained, moved to stored schema; all other test IDs retained.

## Gates
All database tests use owned hxf2-pg, 127.0.0.1:55972, dude:dude (dbtest.adminConn), app role dude_app:dude_app. Migrations applied successfully.
Darwin Go uses /var/tmp/hxf2-overlay.json pointing at the previous ignored .taskdocs/fix1/containers.go scratch overlay. Only Linux Getxattr image capability probe is bypassed; production source untouched. Final Linux gates belong to caller.
- M1 focused: 15 pass / 0 fail; final project-keys 12 pass / 0 fail.
- M2 full phases: 152 top-level pass, 2 optional print tests skipped, package PASS (23.517s).
- M3 full phases: 153 top-level pass, 2 optional print tests skipped, package PASS (23.044s).
- Final affected Go: delivery 56, phases 153, fakelux 41 top-level pass; 3 optional tests skipped total; all packages PASS. /var/tmp/hxf2-affected.log.
- Bun 1.4.2: domain 171 pass, web 594 pass, control-plane 526 pass, no failures. /var/tmp/hxf2-domain.log, hxf2-web.log, hxf2-cp-final.log.
- Typecheck all workspaces PASS; affected Go vet PASS.
- Initial Bun 1.3.9 run is not a passing gate (unsupported runtime/subprocess and image filename failures); rerun with Bun 1.4.2 first in PATH passed.
- Root Go full overlay suite: PASS, 556 top-level tests passed, 0 skipped, 0 no-test-database skips, 1202.91s. /var/tmp/hxf2-root.log. Polled at intervals of 170s or less.

## Coverage
M2 pins old state → update2 → create3 → serialize/reload → update3; recovered counters below and above retained ID; pruned database-loaded state → create251/update251.
M3 semantic checks pin protected provider object and URL/env key, exact/ancestor/descendant dropped list, sibling provider, scalar overrides, boolean, mixed array, DEL header roundtrip and mandatory CODEX_CONFIG presence. Existing string assertions retained. Existing warn path consumes dropped list; no new log mechanism.
No live models, image builds or browser E2E run.

Pushed HEAD d196b413deff751f75488c8d6b7b9db2cceb83ce to github HEAD:harnesses. Removed only owned container hxf2-pg. Final worktree had no tracked changes or untracked non-ignored files.
