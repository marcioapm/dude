# E2E twelve-shard implementation report

## Starting point and constraints
- Started 2026-11-06 at pinned HEAD `450229810a734372036793fe1f882bddd87104c9`; clean checkout, detached HEAD expected.
- Work stays in this checkout; no subagents, clone, worktree, PR, or product/Go lifecycle edits.
- Target: twelve isolated E2E stacks, exact collection/result coverage, six fail-closed compatibility checks retaining `e2e (1/6)` … `e2e (6/6)`.
- Under ten minutes is a target, not a measured claim. Existing duration weights and 30-second fallback stay unchanged.

## Progress
1. Inspected `run_tests.py`, `conftest.py`, `shard.py`, existing harness tests and CI. Current harness builds before collection and has no result receipt. Suite discovery is top-level-only; empty shard currently exits successfully.
2. Installed dependencies with `bun install --frozen-lockfile` and `uv sync --frozen`: both exit 0. Local host is Linux arm64, four CPUs. Browser/PG setup and baseline collection pending.

## Gates and evidence
- Baseline collection captured directly from pytest hooks in `baseline-collection.json` (initial helper attempt failed plugin validation, corrected before snapshot).
- Implemented precollection/plan mode, a dedicated direct-hook receipt helper (keeps service-free collection separate from fixture conftest), teardown-fenced receipts, recursive suite discovery, twelve-runner CI and six compatibility checks. Existing weight file unchanged.
- Focused harness/shard/Go-partition checks: **85 passed in 0.47s**, exit 0 (`focused.log`). Includes synthetic missing/duplicate/parameter-result negatives, platform skip reason, failing test/session teardown, and fake pipeline collection/DB/test/teardown failures.
- PostgreSQL pgvector pg17-trixie is healthy on 5433, tmpfs 2 GiB, fsync/full-page writes off, max connections 800. Rootless Podman socket `/tmp/dude-podman.sock`, service stdio redirected to `/tmp/podman.log`.
- Linux arm64 has no Google Chrome channel. Playwright native Chromium **153.0.8010.12** launches successfully; real local gates will explicitly use `DUDE_TEST_BROWSER_CHANNEL=`. This is a disclosed browser fallback, not CI equivalence.
- Full runtime and twelve-plan collection gates next; no runtime or CI speedup claim yet.

## Artifacts
This report is intentionally versioned per request. Collection snapshots, receipts, JUnit, logs and patch backup will be retained in this checkout under `taskdocs/` when available; caches and temporary services are not report artifacts.
