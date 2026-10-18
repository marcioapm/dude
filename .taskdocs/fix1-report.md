# Review round 1 fixes report

## Commits

- `05b9e0ee` — remove the duplicated operations documentation sentence.
- `c4c5d562` — trim init output before appending; bound fake-lux init output; add the multi-MiB test.
- `fd3ee5bb` — add behavioral init-output and init-failure translation tests, including persisted error/event redaction.
- `ba84c3da` — make setup timeout an input to composition and test the production `SetupTimeout` value through the API.
- `b47d968a` — keep fake-lux exec output whole; only init output is bounded.
- `364ea07c` — deliver setup scripts as a fixed init plus the resumable `dude-setup` file-secret manifest.
- `c6cfbf2f` — add migration and API response/event coverage.

## Item 1 — manifest delivery and resume-added repositories

Changed setup delivery from per-Run init composition to:

- a fixed init script for every Run with repositories;
- one `as: file` secret named `dude-setup` at `/home/agent/.dude/setup`;
- a line-oriented manifest with base64 name, relative checkout path, and script fields;
- current manifest values supplied on every resume, including repositories added by a repository request or session link;
- marker-per-checkout behavior retained, with failure stopping init and naming the repository;
- fake-lux support for file secrets, resume replacement values, resume-added checkouts, and bounded init output.

The script parser uses only shell, coreutils, git, and base64. Hostile name/path/script coverage was extended to the manifest parser. The file-secret value is redacted by lux/dude's normal secret redaction; therefore output containing the complete manifest value may be masked. Individual script output remains visible unless it contains a secret value.

Focused fake-lux integration coverage passes for: two repositories with one script; adding a third repository on resume; marker-only rerun behavior; failed setup suppressing the workload; command-scope `GIT_CONFIG_*` clearing; repository hook chaining; and setup-before-workload ordering.

## Item 2 — init-failure translation

Added table-driven `InitOutput.Record` tests for ordinary failures, exit 124, malformed/missing markers, oversized output, interleaved non-init records, epoch reset, retained tail, and `Reason()`. Added a database integration test asserting the persisted Run error and `run.failed` payload, including redaction of a project secret printed by setup output.

## Item 3 — output memory bound

`InitOutput.Record` now trims before concatenation and keeps the tail of a frame that alone exceeds the limit, preserving valid UTF-8. Fake-lux init stdout/stderr uses a bounded tail writer. The multi-MiB test passed, and mutation testing of the old append-then-trim implementation failed as expected.

## Item 4 — timeout API

`SetupInit` accepts a timeout value. Production composition passes `SetupTimeout` (15 minutes); the behavioral test passes one second and verifies timeout exit 124, then separately verifies the production value through `SetupSpec`, with no source-text inspection.

## Item 5 — caller propagation

The shared phase builder, session builder, preview task-repository path, and preview project-fallback path now all use the manifest setup composition. Existing phase golden specs were regenerated to assert the fixed init and manifest secret. The new fake-lux Run tests exercise the resulting composition and resume behavior. A separate broad caller matrix was not added beyond these real builders and golden coverage.

## Item 6 — documentation

Removed the duplicated/unfinished hook sentence. Updated operations, GitHub, and multi-repository documentation for the manifest-secret and resume semantics.

## Item 7 — migration and API

Added migration coverage from a pre-143 database with an existing repository: NULL after migration, 16,384-byte acceptance, and 16,385-byte rejection. Extended API tests for omitted-field preservation, null/blank clearing, project response after updates, and clear-event payloads.

## Verification

- Focused Go phases tests: PASS, 20 setup/init-focused tests; database integration included when the configured test DB/Bun was available.
- Go fakelux package: PASS.
- Go servers package: PASS in the earlier focused run.
- `go vet ./internal/phases ./internal/fakelux ./internal/servers`: PASS.
- TypeScript: `PATH="$HOME/.bun/bin:$PATH" bun run typecheck`: PASS for domain, control-plane, design-system, and web.
- Control-plane API test command was attempted but the environment lacked `DATABASE_URL` and the default connection authentication failed.
- Migration test was attempted with Bun and timed out at its 120-second test timeout while creating/migrating the temporary database; no result was asserted.
- Root Go suite was allowed to finish as requested and failed in existing/non-focused preview and migration-dependent areas. The exact observed failures included `TestABranchPreviewPullsTheAgentImageWithTheLogin`, `TestThePreviewSpecIsTheGoldenOne`, several preview wake/end ordering tests, and migration-dependent tests that reported `migrate: exec: "bun": executable file not found in $PATH` in the later verbose focused invocation. No second root suite was started.
- `grep -c "no test database"` on the affected phases run output: `0`.

## Mutation checks

Mutations to marker checks, GIT_CONFIG clearing, indexed key/value removal, stdin/manifest descriptor closure, missing-checkout handling, marker write/failure propagation, manifest header parsing, and base64 name/path delivery were caught by focused tests. The old whole-frame append implementation was caught by the allocation-bound test.

## Deviations / open questions

- The requested full caller propagation matrix across every phase role was not added as a new standalone test; the real shared builder plus regenerated goldens and fake-lux integration cover the changed propagation.
- Migration/API tests are present but could not complete in this environment because the required database URL/service configuration was unavailable or Bun was absent from the invoked process environment.
- No declined performance items were implemented.
